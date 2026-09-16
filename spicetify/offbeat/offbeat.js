(function (global) {
  "use strict";

  var PROTOCOL_VERSION = 1;
  var INITIAL_RECONNECT_DELAY_MS = 1000;
  var MAX_RECONNECT_DELAY_MS = 30000;
  var MAX_MESSAGE_BYTES = 16 * 1024 * 1024;
  var PAGE_LIMIT = 100;

  function collectionError(operation, offset, cause) {
    var error = new Error(cause || "response could not be interpreted");
    error.operation = operation;
    error.offset = offset;
    return error;
  }

  function object(value) {
    return value !== null && typeof value === "object" && !Array.isArray(value);
  }

  function requiredString(value) {
    return typeof value === "string" && value !== "";
  }

  function page(response, operation, offset) {
    if (!object(response) || !Array.isArray(response.items) || !Number.isSafeInteger(response.totalLength) || response.totalLength < 0) {
      throw collectionError(operation, offset);
    }
    return response;
  }

  function normalizeEntry(raw, position, operation, offset) {
    if (!object(raw)) throw collectionError(operation, offset);
    var source = object(raw.item) ? raw.item : raw;
    if (!requiredString(source.type)) throw collectionError(operation, offset);
    var sourceURI = requiredString(raw.uri) ? raw.uri : (requiredString(source.uri) ? source.uri : null);
    var unsupported = { position: position, kind: "unsupported" };
    if (sourceURI) unsupported.source_uri = sourceURI;
    if (source.type !== "track" || source.isPlayable === false || source.isAvailable === false) return unsupported;
    if (!requiredString(source.uri) || !requiredString(source.name) || !Number.isSafeInteger(source.duration_ms) || source.duration_ms <= 0 || !Array.isArray(source.artists) || source.artists.length === 0 || !object(source.album)) return unsupported;
    var artists = [];
    for (var index = 0; index < source.artists.length; index += 1) {
      var artist = source.artists[index];
      if (!object(artist) || !requiredString(artist.uri) || !requiredString(artist.name)) return unsupported;
      artists.push({ uri: artist.uri, name: artist.name });
    }
    if (!requiredString(source.album.uri) || !requiredString(source.album.name)) return unsupported;
    return {
      position: position,
      kind: "supported",
      track: {
        uri: source.uri,
        name: source.name,
        artists: artists,
        album: { uri: source.album.uri, name: source.album.name },
        duration_ms: source.duration_ms
      }
    };
  }

  async function fetchEntries(fetchPage, operation) {
    var entries = [];
    var offset = 0;
    var total = null;
    while (total === null || offset < total) {
      var response;
      try {
        response = page(await fetchPage(offset, PAGE_LIMIT), operation, offset);
      } catch (error) {
        if (error && error.operation) throw error;
        throw collectionError(operation, offset, "request failed");
      }
      if (total === null) total = response.totalLength;
      if (response.totalLength !== total || offset > total || response.items.length > total - offset || (offset < total && response.items.length === 0)) {
        throw collectionError(operation, offset, "inconsistent pagination");
      }
      for (var index = 0; index < response.items.length; index += 1) entries.push(normalizeEntry(response.items[index], entries.length, operation, offset));
      var nextOffset = offset + response.items.length;
      if (nextOffset <= offset && offset < total) throw collectionError(operation, offset, "non-advancing pagination");
      offset = nextOffset;
    }
    return entries;
  }

  async function collectSnapshot(platform) {
    if (!object(platform) || !object(platform.RootlistAPI) || typeof platform.RootlistAPI.getContents !== "function" || !object(platform.PlaylistAPI) || typeof platform.PlaylistAPI.getContents !== "function" || !object(platform.LibraryAPI) || typeof platform.LibraryAPI.getTracks !== "function") {
      throw collectionError("platform", null, "required Spotify Platform API is unavailable");
    }
    var root;
    try {
      root = await platform.RootlistAPI.getContents();
    } catch (_error) {
      throw collectionError("rootlist", null, "request failed");
    }
    if (!object(root) || !Array.isArray(root.items)) throw collectionError("rootlist", null);
    var playlists = [];
    function visit(items) {
      for (var index = 0; index < items.length; index += 1) {
        var item = items[index];
        if (!object(item) || !requiredString(item.type)) throw collectionError("rootlist", null);
        if (item.type === "folder") {
          if (!Array.isArray(item.items)) throw collectionError("rootlist", null);
          visit(item.items);
        } else if (item.type === "playlist") {
          if (!requiredString(item.uri) || !requiredString(item.name)) throw collectionError("rootlist", null);
          playlists.push({ uri: item.uri, name: item.name });
        } else {
          throw collectionError("rootlist", null);
        }
      }
    }
    visit(root.items);
    for (var playlistIndex = 0; playlistIndex < playlists.length; playlistIndex += 1) {
      var playlist = playlists[playlistIndex];
      playlist.entries = await fetchEntries(function (uri) {
        return function (offset, limit) { return platform.PlaylistAPI.getContents(uri, { offset: offset, limit: limit }); };
      }(playlist.uri), "playlist");
    }
    return {
      kind: "candidate",
      playlists: playlists,
      liked_songs: { entries: await fetchEntries(function (offset, limit) { return platform.LibraryAPI.getTracks({ offset: offset, limit: limit }); }, "liked_songs") }
    };
  }

  function createAdapter(config, dependencies) {
    if (!config || typeof config.endpoint !== "string" || typeof config.credential !== "string") throw new Error("Offbeat M3 configuration requires endpoint and credential strings.");
    var WebSocketConstructor = dependencies.WebSocket;
    var setTimer = dependencies.setTimeout;
    var clearTimer = dependencies.clearTimeout;
    var platform = dependencies.Platform;
    var socket = null;
    var reconnectTimer = null;
    var reconnectDelayMs = INITIAL_RECONNECT_DELAY_MS;
    var authenticated = false;
    var stopped = false;
    var permanentlyRejected = false;
    function log(level, message) { if (authenticated && socket) send({ version: PROTOCOL_VERSION, type: "log", level: level, message: message }); }
    function scheduleReconnect() {
      if (stopped || permanentlyRejected || reconnectTimer !== null) return;
      var delay = reconnectDelayMs;
      reconnectDelayMs = Math.min(reconnectDelayMs * 2, MAX_RECONNECT_DELAY_MS);
      reconnectTimer = setTimer(function () { reconnectTimer = null; connect(); }, delay);
    }
    function send(message) { socket.send(JSON.stringify(message)); }
    function rejectPermanently(code) { permanentlyRejected = true; log("error", "Offbeat adapter rejected by daemon: " + code + ". Reconfigure or reload the extension before retrying."); if (socket) socket.close(); }
    function protocolViolation(message) { log("warn", message); if (socket) socket.close(); }
    function respondToSnapshot(requestID) {
      collectSnapshot(platform).then(function (snapshot) {
        if (authenticated && socket) {
          var entries = 0;
          var unsupported = 0;
          for (var playlistIndex = 0; playlistIndex < snapshot.playlists.length; playlistIndex += 1) {
            entries += snapshot.playlists[playlistIndex].entries.length;
            for (var entryIndex = 0; entryIndex < snapshot.playlists[playlistIndex].entries.length; entryIndex += 1) if (snapshot.playlists[playlistIndex].entries[entryIndex].kind === "unsupported") unsupported += 1;
          }
          entries += snapshot.liked_songs.entries.length;
          for (var likedIndex = 0; likedIndex < snapshot.liked_songs.entries.length; likedIndex += 1) if (snapshot.liked_songs.entries[likedIndex].kind === "unsupported") unsupported += 1;
          log("info", "Collected candidate snapshot: playlists=" + snapshot.playlists.length + " entries=" + entries + " unsupported_entries=" + unsupported + " liked_songs=" + snapshot.liked_songs.entries.length + ".");
          send({ version: PROTOCOL_VERSION, type: "snapshot.response", request_id: requestID, snapshot: snapshot });
        }
      }, function (error) {
        var failure = { operation: error && error.operation ? error.operation : "collection", message: error && requiredString(error.message) ? error.message : "collection failed" };
        if (error && Number.isSafeInteger(error.offset) && error.offset >= 0) failure.offset = error.offset;
        if (authenticated && socket) send({ version: PROTOCOL_VERSION, type: "snapshot.response", request_id: requestID, error: failure });
      });
    }
    function handleMessage(event) {
      var message;
      if (typeof event.data !== "string" || new global.TextEncoder().encode(event.data).length > MAX_MESSAGE_BYTES) return protocolViolation("Offbeat adapter received an oversized daemon message.");
      try { message = JSON.parse(event.data); } catch (_error) { return protocolViolation("Offbeat adapter received malformed daemon message."); }
      if (!message || message.version !== PROTOCOL_VERSION || typeof message.type !== "string") return protocolViolation("Offbeat adapter received an unsupported daemon message.");
      if (message.type === "error") {
        if (authenticated || typeof message.code !== "string" || typeof message.message !== "string" || Object.keys(message).length !== 4) return protocolViolation("Offbeat adapter received an invalid daemon error.");
        if (message.code === "authentication_failed" || message.code === "unsupported_version") rejectPermanently(message.code); else if (message.code !== "session_conflict") protocolViolation("Offbeat adapter received an unrecognized daemon error.");
        return;
      }
      if (message.type === "hello.accepted") { if (authenticated || Object.keys(message).length !== 2) return protocolViolation("Offbeat adapter received an invalid authentication acceptance."); authenticated = true; reconnectDelayMs = INITIAL_RECONNECT_DELAY_MS; return; }
      if (message.type === "snapshot.request" && authenticated && typeof message.request_id === "string" && message.request_id !== "" && Object.keys(message).length === 3) { respondToSnapshot(message.request_id); return; }
      protocolViolation("Offbeat adapter received an invalid daemon message.");
    }
    function connect() {
      if (stopped || permanentlyRejected || socket !== null) return;
      authenticated = false;
      try { socket = new WebSocketConstructor(config.endpoint); } catch (_error) { socket = null; scheduleReconnect(); return; }
      var connection = socket;
      connection.onopen = function () { if (socket === connection) send({ version: PROTOCOL_VERSION, type: "hello", credential: config.credential }); };
      connection.onmessage = function (event) { if (socket === connection) handleMessage(event); };
      connection.onclose = function () { if (socket !== connection) return; socket = null; authenticated = false; scheduleReconnect(); };
      connection.onerror = function () {};
    }
    return { start: connect, stop: function () { stopped = true; if (reconnectTimer !== null) { clearTimer(reconnectTimer); reconnectTimer = null; } if (socket) socket.close(); } };
  }
  function startConfiguredAdapter() {
    if (!global.OffbeatM2Config) return;
    createAdapter(global.OffbeatM2Config, { WebSocket: global.WebSocket, setTimeout: global.setTimeout.bind(global), clearTimeout: global.clearTimeout.bind(global), Platform: global.Spicetify && global.Spicetify.Platform }).start();
  }
  if (typeof module !== "undefined" && module.exports) module.exports = { createAdapter: createAdapter, collectSnapshot: collectSnapshot };
  startConfiguredAdapter();
})(typeof globalThis === "undefined" ? this : globalThis);
