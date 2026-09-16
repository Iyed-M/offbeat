(function (global) {
  "use strict";

  var PROTOCOL_VERSION = 1;
  var INITIAL_RECONNECT_DELAY_MS = 1000;
  var MAX_RECONNECT_DELAY_MS = 30000;
  var MAX_MESSAGE_BYTES = 16 * 1024 * 1024;
  var PAGE_SIZE = 100;

  // PROTOTYPE: Keep Spotify Desktop's private Platform API at this boundary.
  // Later milestones can replace this normalized shape without touching IPC.
  function createSpotifyAdapter(platform) {
    function fail(message) {
      throw new Error("Offbeat snapshot collection: " + message);
    }

    function text(value) {
      return typeof value === "string" ? value : null;
    }

    function normalizeEntry(item, position) {
      var uri = item && text(item.uri);
      var artists = item && Array.isArray(item.artists) ? item.artists.map(function (artist) {
        return text(artist) || text(artist && artist.name);
      }).filter(Boolean) : [];
      return {
        position: position,
        uri: uri,
        supported: Boolean(item && item.isPlayable && uri && uri.indexOf("spotify:track:") === 0),
        name: item && (text(item.name) || text(item.title)),
        artists: artists
      };
    }

    function collectPlaylists(items, path, playlists) {
      if (!Array.isArray(items)) {
        fail("rootlist items are missing");
      }
      items.forEach(function (item, index) {
        if (!item || typeof item !== "object") {
          fail("rootlist item " + index + " is malformed");
        }
        if (item.type === "playlist") {
          if (!text(item.uri)) {
            fail("playlist " + index + " has no URI");
          }
          playlists.push({ uri: item.uri, name: text(item.name), path: path.concat(index) });
        } else if (item.type === "folder") {
          collectPlaylists(item.items, path.concat(index), playlists);
        }
      });
    }

    async function fetchPlaylist(playlist) {
      var entries = [];
      var offset = 0;
      var total = null;
      while (total === null || offset < total) {
        var page = await platform.PlaylistAPI.getContents(playlist.uri, { offset: offset, limit: PAGE_SIZE });
        if (!page || !Array.isArray(page.items) || typeof page.totalLength !== "number" || page.totalLength < 0) {
          fail("playlist " + playlist.uri + " returned a malformed page at offset " + offset);
        }
        if (total !== null && total !== page.totalLength) {
          fail("playlist " + playlist.uri + " changed size during collection");
        }
        total = page.totalLength;
        if (page.items.length === 0 && offset < total) {
          fail("playlist " + playlist.uri + " returned an empty page at offset " + offset);
        }
        page.items.forEach(function (item, index) {
          entries.push(normalizeEntry(item, offset + index));
        });
        offset += page.items.length;
        if (offset > total) {
          fail("playlist " + playlist.uri + " returned too many entries");
        }
      }
      return { uri: playlist.uri, name: playlist.name, path: playlist.path, entries: entries };
    }

    return {
      collectSnapshot: async function () {
        if (!platform || !platform.RootlistAPI || !platform.PlaylistAPI || typeof platform.RootlistAPI.getContents !== "function" || typeof platform.PlaylistAPI.getContents !== "function") {
          fail("Spotify Desktop collection APIs are unavailable");
        }
        if (typeof platform.PlaylistAPI.getCapabilities === "function") {
          var capabilities = await platform.PlaylistAPI.getCapabilities();
          if (!capabilities || capabilities.canFetchAllTracks !== true) {
            fail("Spotify Desktop cannot fetch complete playlist tracks");
          }
        }
        var root = await platform.RootlistAPI.getContents();
        var playlists = [];
        collectPlaylists(root && root.items, [], playlists);
        return {
          captured_at: new Date().toISOString(),
          playlists: await Promise.all(playlists.map(fetchPlaylist))
        };
      }
    };
  }

  function createAdapter(config, dependencies) {
    if (!config || typeof config.endpoint !== "string" || typeof config.credential !== "string") {
      throw new Error("Offbeat M2 configuration requires endpoint and credential strings.");
    }

    var WebSocketConstructor = dependencies.WebSocket;
    var setTimer = dependencies.setTimeout;
    var clearTimer = dependencies.clearTimeout;
    var logger = dependencies.logger || global.console;
    var socket = null;
    var reconnectTimer = null;
    var reconnectDelayMs = INITIAL_RECONNECT_DELAY_MS;
    var authenticated = false;
    var stopped = false;
    var permanentlyRejected = false;
    var spotifyAdapter = dependencies.spotifyAdapter || createSpotifyAdapter(global.Spicetify && global.Spicetify.Platform);

    function log(level, message) {
      if (logger && typeof logger[level] === "function") {
        logger[level](message);
      }
    }

    function scheduleReconnect() {
      if (stopped || permanentlyRejected || reconnectTimer !== null) {
        return;
      }

      var delay = reconnectDelayMs;
      reconnectDelayMs = Math.min(reconnectDelayMs * 2, MAX_RECONNECT_DELAY_MS);
      reconnectTimer = setTimer(function () {
        reconnectTimer = null;
        connect();
      }, delay);
    }

    function send(message) {
      socket.send(JSON.stringify(message));
    }

    function rejectPermanently(code) {
      permanentlyRejected = true;
      log("error", "Offbeat adapter rejected by daemon: " + code + ". Reconfigure or reload the extension before retrying.");
      if (socket) {
        socket.close();
      }
    }

    function protocolViolation(message) {
      log("warn", message);
      if (socket) {
        socket.close();
      }
    }

    function handleMessage(event) {
      var message;
      if (typeof event.data !== "string" || new global.TextEncoder().encode(event.data).length > MAX_MESSAGE_BYTES) {
        protocolViolation("Offbeat adapter received an oversized daemon message.");
        return;
      }
      try {
        message = JSON.parse(event.data);
      } catch (_error) {
        protocolViolation("Offbeat adapter received malformed daemon message.");
        return;
      }

      if (!message || message.version !== PROTOCOL_VERSION || typeof message.type !== "string") {
        protocolViolation("Offbeat adapter received an unsupported daemon message.");
        return;
      }

      if (message.type === "error") {
        if (authenticated || typeof message.code !== "string" || typeof message.message !== "string" || Object.keys(message).length !== 4) {
          protocolViolation("Offbeat adapter received an invalid daemon error.");
          return;
        }
        if (message.code === "authentication_failed" || message.code === "unsupported_version") {
          rejectPermanently(message.code);
        } else if (message.code !== "session_conflict") {
          protocolViolation("Offbeat adapter received an unrecognized daemon error.");
        }
        return;
      }

      if (message.type === "hello.accepted") {
        if (authenticated || Object.keys(message).length !== 2) {
          protocolViolation("Offbeat adapter received an invalid authentication acceptance.");
          return;
        }
        authenticated = true;
        reconnectDelayMs = INITIAL_RECONNECT_DELAY_MS;
        return;
      }

      if (message.type === "snapshot.request" && authenticated && typeof message.request_id === "string" && Object.keys(message).length === 3) {
        spotifyAdapter.collectSnapshot().then(function (snapshot) {
          send({
            version: PROTOCOL_VERSION,
            type: "snapshot.response",
            request_id: message.request_id,
            snapshot: snapshot
          });
        }).catch(function (error) {
          log("error", error.message);
          if (socket) socket.close();
        });
        return;
      }

      protocolViolation("Offbeat adapter received an invalid daemon message.");
    }

    function connect() {
      if (stopped || permanentlyRejected || socket !== null) {
        return;
      }

      authenticated = false;
      try {
        socket = new WebSocketConstructor(config.endpoint);
      } catch (_error) {
        socket = null;
        scheduleReconnect();
        return;
      }

      var connection = socket;
      connection.onopen = function () {
        if (socket !== connection) {
          return;
        }
        send({
          version: PROTOCOL_VERSION,
          type: "hello",
          credential: config.credential
        });
      };
      connection.onmessage = function (event) {
        if (socket === connection) {
          handleMessage(event);
        }
      };
      connection.onclose = function () {
        if (socket !== connection) {
          return;
        }
        socket = null;
        authenticated = false;
        scheduleReconnect();
      };
      socket.onerror = function () {
        // A close event normally follows; it owns the reconnect transition.
      };
    }

    return {
      start: connect,
      stop: function () {
        stopped = true;
        if (reconnectTimer !== null) {
          clearTimer(reconnectTimer);
          reconnectTimer = null;
        }
        if (socket) {
          socket.close();
        }
      }
    };
  }

  function startConfiguredAdapter() {
    if (!global.OffbeatM2Config) {
      return;
    }

    createAdapter(global.OffbeatM2Config, {
      WebSocket: global.WebSocket,
      setTimeout: global.setTimeout.bind(global),
      clearTimeout: global.clearTimeout.bind(global),
      logger: global.console
    }).start();
  }

  if (typeof module !== "undefined" && module.exports) {
    module.exports = { createAdapter: createAdapter, createSpotifyAdapter: createSpotifyAdapter };
  }

  startConfiguredAdapter();
})(typeof globalThis === "undefined" ? this : globalThis);
