"use strict";

var assert = require("node:assert/strict");
var test = require("node:test");
var createAdapter = require("./offbeat.js").createAdapter;

function createPeer() {
  var sockets = [];
  function WebSocket(url) {
    this.url = url;
    this.sent = [];
    this.closed = false;
    sockets.push(this);
  }
  WebSocket.prototype.send = function (message) { this.sent.push(JSON.parse(message)); };
  WebSocket.prototype.close = function () {
    this.closed = true;
    if (this.onclose) this.onclose();
  };
  WebSocket.prototype.open = function () { this.onopen(); };
  WebSocket.prototype.receive = function (message) { this.onmessage({ data: JSON.stringify(message) }); };
  WebSocket.prototype.receiveRaw = function (message) { this.onmessage({ data: message }); };
  WebSocket.prototype.disconnect = function () { this.onclose(); };

  var timers = [];
  return {
    WebSocket: WebSocket,
    sockets: sockets,
    timers: timers,
    setTimeout: function (callback, delay) {
      var timer = { callback: callback, delay: delay, cancelled: false };
      timers.push(timer);
      return timer;
    },
    clearTimeout: function (timer) { timer.cancelled = true; },
    runNextTimer: function () {
      var timer = timers.shift();
      assert.ok(timer, "expected reconnect timer");
      if (!timer.cancelled) timer.callback();
    }
  };
}

function start(peer, platform) {
  var adapter = createAdapter({ endpoint: "ws://127.0.0.1:16352/v1/adapter", credential: "development-credential" }, {
    WebSocket: peer.WebSocket,
    setTimeout: peer.setTimeout,
    clearTimeout: peer.clearTimeout,
    logger: { error: function () {}, warn: function () {} },
    Platform: platform || emptyPlatform()
  });
  adapter.start();
  return adapter;
}

function emptyPlatform() {
  return {
    RootlistAPI: { getContents: async function () { return { items: [] }; } },
    PlaylistAPI: { getContents: async function () { return { items: [], totalLength: 0 }; } },
    LibraryAPI: { getTracks: async function () { return { items: [], totalLength: 0 }; } }
  };
}

function supportedTrack(uri, name) {
  return { type: "track", uri: uri, name: name, duration_ms: 1000, artists: [{ uri: "spotify:artist:one", name: "Artist" }], album: { uri: "spotify:album:one", name: "Album" } };
}

function desktopTrack(uri, name) {
  return { type: "track", uri: uri, name: name, duration: { milliseconds: 1000 }, artists: [{ uri: "spotify:artist:one", name: "Artist" }], album: { uri: "spotify:album:one", name: "Album" } };
}

test("sends a normalized candidate only for correlated snapshot requests after acceptance", async function () {
  var peer = createPeer();
  start(peer);
  var socket = peer.sockets[0];
  socket.open();
  assert.deepEqual(socket.sent, [{ version: 1, type: "hello", credential: "development-credential" }]);

  socket.receive({ version: 1, type: "hello.accepted" });
  socket.receive({ version: 1, type: "snapshot.request", request_id: "opaque-request-id" });
  await new Promise(function (resolve) { setImmediate(resolve); });
  assert.deepEqual(socket.sent[2], {
    version: 1,
    type: "snapshot.response",
    request_id: "opaque-request-id",
    snapshot: { kind: "candidate", playlists: [], liked_songs: { entries: [] } }
  });
});

test("collects nested playlists and paginated liked songs without dropping duplicates or unsupported entries", async function () {
  var calls = [];
  var snapshot = await require("./offbeat.js").collectSnapshot({
    RootlistAPI: { getContents: async function () { return { items: [{ type: "folder", items: [{ type: "playlist", uri: "spotify:playlist:nested", name: "Nested" }] }, { type: "playlist", uri: "spotify:playlist:empty", name: "Empty" }] }; } },
    PlaylistAPI: { getContents: async function (uri, request) {
      calls.push([uri, request.offset]);
      if (uri === "spotify:playlist:empty") return { items: [], totalLength: 0 };
      if (request.offset === 0) return { items: [supportedTrack("spotify:track:duplicate", "Duplicate")], totalLength: 3 };
      return { items: [supportedTrack("spotify:track:duplicate", "Duplicate"), { type: "episode" }], totalLength: 3 };
    } },
    LibraryAPI: { getTracks: async function (request) {
      if (request.offset === 0) return { items: [supportedTrack("spotify:track:liked", "Liked")], totalLength: 2 };
      return { items: [{ type: "track", uri: "spotify:track:unplayable", isPlayable: false }], totalLength: 2 };
    } }
  });
  assert.deepEqual(calls, [["spotify:playlist:nested", 0], ["spotify:playlist:nested", 1], ["spotify:playlist:empty", 0]]);
  assert.equal(snapshot.playlists[0].entries[1].track.uri, "spotify:track:duplicate");
  assert.deepEqual(snapshot.playlists[0].entries[2], { position: 2, kind: "unsupported" });
  assert.deepEqual(snapshot.liked_songs.entries[1], { position: 1, kind: "unsupported", source_uri: "spotify:track:unplayable" });
});

test("normalizes Desktop duration objects and completes Liked Songs when its count is unavailable", async function () {
  var snapshot = await require("./offbeat.js").collectSnapshot({
    RootlistAPI: { getContents: async function () { return { items: [] }; } },
    PlaylistAPI: emptyPlatform().PlaylistAPI,
    LibraryAPI: { getTracks: async function (request) {
      assert.deepEqual(request, { offset: 0, limit: 100 });
      return { items: Array.from({ length: 65 }, function (_, index) { return desktopTrack("spotify:track:" + index, "Track " + index); }), totalLength: 0 };
    } }
  });
  assert.equal(snapshot.liked_songs.entries.length, 65);
  assert.deepEqual(snapshot.liked_songs.entries[0], {
    position: 0,
    kind: "supported",
    track: {
      uri: "spotify:track:0",
      name: "Track 0",
      artists: [{ uri: "spotify:artist:one", name: "Artist" }],
      album: { uri: "spotify:album:one", name: "Album" },
      duration_ms: 1000
    }
  });
});

test("paginates an unavailable Liked Songs count by page length and rejects a later count", async function () {
  var calls = [];
  var snapshot = await require("./offbeat.js").collectSnapshot({
    RootlistAPI: { getContents: async function () { return { items: [] }; } },
    PlaylistAPI: emptyPlatform().PlaylistAPI,
    LibraryAPI: { getTracks: async function (request) {
      calls.push(request.offset);
      return {
        items: Array.from({ length: request.offset === 0 ? 100 : 1 }, function (_, index) { return desktopTrack("spotify:track:" + (request.offset + index), "Track " + (request.offset + index)); }),
        totalLength: 0
      };
    } }
  });
  assert.deepEqual(calls, [0, 100]);
  assert.equal(snapshot.liked_songs.entries.length, 101);

  await assert.rejects(require("./offbeat.js").collectSnapshot({
    RootlistAPI: { getContents: async function () { return { items: [] }; } },
    PlaylistAPI: emptyPlatform().PlaylistAPI,
    LibraryAPI: { getTracks: async function (request) {
      return {
        items: Array.from({ length: request.offset === 0 ? 100 : 1 }, function (_, index) { return desktopTrack("spotify:track:" + (request.offset + index), "Track " + (request.offset + index)); }),
        totalLength: request.offset === 0 ? 0 : 101
      };
    } }
  }), /inconsistent pagination/);
});

test("rejects malformed pages and unavailable Platform APIs", async function () {
  await assert.rejects(require("./offbeat.js").collectSnapshot({}), /required Spotify Platform API/);
  await assert.rejects(require("./offbeat.js").collectSnapshot({
    RootlistAPI: { getContents: async function () { return { items: [{ type: "playlist", uri: "spotify:playlist:one", name: "One" }] }; } },
    PlaylistAPI: { getContents: async function () { return { items: [], totalLength: 1 }; } },
    LibraryAPI: { getTracks: async function () { return { items: [], totalLength: 0 }; } }
  }), /inconsistent pagination/);
});

test("rejects malformed rootlists, items, and page counters", async function () {
  var base = emptyPlatform();
  await assert.rejects(require("./offbeat.js").collectSnapshot({
    RootlistAPI: { getContents: async function () { return { items: [{ type: "folder" }] }; } },
    PlaylistAPI: base.PlaylistAPI,
    LibraryAPI: base.LibraryAPI
  }), /response could not be interpreted/);
  await assert.rejects(require("./offbeat.js").collectSnapshot({
    RootlistAPI: { getContents: async function () { return { items: [{ type: "playlist", uri: "spotify:playlist:one", name: "One" }] }; } },
    PlaylistAPI: { getContents: async function () { return { items: [{ item: null }], totalLength: 1 }; } },
    LibraryAPI: base.LibraryAPI
  }), /response could not be interpreted/);
  await assert.rejects(require("./offbeat.js").collectSnapshot({
    RootlistAPI: { getContents: async function () { return { items: [] }; } },
    PlaylistAPI: base.PlaylistAPI,
    LibraryAPI: { getTracks: async function () { return { items: [] }; } }
  }), /response could not be interpreted/);
});

test("rejects pages whose reported offset does not match the requested offset", async function () {
  await assert.rejects(require("./offbeat.js").collectSnapshot({
    RootlistAPI: { getContents: async function () { return { items: [] }; } },
    PlaylistAPI: emptyPlatform().PlaylistAPI,
    LibraryAPI: { getTracks: async function (request) {
      return {
        offset: request.offset === 0 ? 0 : 50,
        items: Array.from({ length: 100 }, function (_, index) { return supportedTrack("spotify:track:" + (request.offset + index), "Track " + (request.offset + index)); }),
        totalLength: 200
      };
    } }
  }), /inconsistent pagination: requested offset 100, received 50/);
});

test("treats Spotify's zero page offset as unavailable pagination metadata", async function () {
  var warnings = [];
  var snapshot = await require("./offbeat.js").collectSnapshot({
    RootlistAPI: { getContents: async function () { return { items: [] }; } },
    PlaylistAPI: emptyPlatform().PlaylistAPI,
    LibraryAPI: { getTracks: async function (request) {
      return {
        offset: 0,
        items: Array.from({ length: 100 }, function (_, index) { return supportedTrack("spotify:track:" + (request.offset + index), "Track " + (request.offset + index)); }),
        totalLength: 200
      };
    } }
  }, function (operation, offset) { warnings.push({ operation: operation, offset: offset }); });
  assert.equal(snapshot.liked_songs.entries.length, 200);
  assert.deepEqual(warnings, [{ operation: "liked_songs", offset: 100 }]);
});

test("sends one bounded collection error instead of a partial candidate", async function () {
  var peer = createPeer();
  start(peer, {
    RootlistAPI: { getContents: async function () { return { items: [{ type: "playlist", uri: "spotify:playlist:one", name: "One" }] }; } },
    PlaylistAPI: { getContents: async function () { throw new Error("private API stack"); } },
    LibraryAPI: { getTracks: async function () { return { items: [], totalLength: 0 }; } }
  });
  var socket = peer.sockets[0];
  socket.open();
  socket.receive({ version: 1, type: "hello.accepted" });
  socket.receive({ version: 1, type: "snapshot.request", request_id: "failed-request" });
  await new Promise(function (resolve) { setImmediate(resolve); });
  assert.deepEqual(socket.sent[1], {
    version: 1,
    type: "snapshot.response",
    request_id: "failed-request",
    error: { operation: "playlist", offset: 0, message: "request failed" }
  });
});

test("discards a completed collection when its requesting session disconnects", async function () {
  var peer = createPeer();
  var resolveRootlist;
  var rootlistCalls = 0;
  start(peer, {
    RootlistAPI: { getContents: function () {
      rootlistCalls += 1;
      if (rootlistCalls > 1) return Promise.resolve({ items: [] });
      return new Promise(function (resolve) { resolveRootlist = resolve; });
    } },
    PlaylistAPI: emptyPlatform().PlaylistAPI,
    LibraryAPI: emptyPlatform().LibraryAPI
  });
  var sessionA = peer.sockets[0];
  sessionA.open();
  sessionA.receive({ version: 1, type: "hello.accepted" });
  sessionA.receive({ version: 1, type: "snapshot.request", request_id: "request-a" });
  sessionA.disconnect();
  peer.runNextTimer();
  var sessionB = peer.sockets[1];
  sessionB.open();
  sessionB.receive({ version: 1, type: "hello.accepted" });
  resolveRootlist({ items: [] });
  await new Promise(function (resolve) { setImmediate(resolve); });
  assert.equal(sessionA.sent.some(function (message) { return message.type === "snapshot.response"; }), false);
  assert.deepEqual(sessionB.sent, [{ version: 1, type: "hello", credential: "development-credential" }]);
  sessionB.receive({ version: 1, type: "snapshot.request", request_id: "request-b" });
  await new Promise(function (resolve) { setImmediate(resolve); });
  assert.equal(sessionB.sent[2].request_id, "request-b");
});

test("reconnects after transport loss with capped backoff and resets after authentication", function () {
  var peer = createPeer();
  start(peer);
  peer.sockets[0].disconnect();
  assert.equal(peer.timers[0].delay, 1000);
  peer.runNextTimer();
  peer.sockets[1].disconnect();
  assert.equal(peer.timers[0].delay, 2000);
  peer.runNextTimer();
  peer.sockets[2].open();
  peer.sockets[2].receive({ version: 1, type: "hello.accepted" });
  peer.sockets[2].disconnect();
  assert.equal(peer.timers[0].delay, 1000);
});

test("caps recoverable reconnect delay at thirty seconds", function () {
  var peer = createPeer();
  start(peer);
  for (var index = 0; index < 6; index += 1) {
    peer.sockets[index].disconnect();
    peer.runNextTimer();
  }
  peer.sockets[6].disconnect();
  assert.equal(peer.timers[0].delay, 30000);
});

test("authentication and version rejection stop reconnecting", function () {
  ["authentication_failed", "unsupported_version"].forEach(function (code) {
    var peer = createPeer();
    start(peer);
    peer.sockets[0].open();
    peer.sockets[0].receive({ version: 1, type: "error", code: code, message: "rejected" });
    assert.equal(peer.sockets[0].closed, true);
    assert.equal(peer.timers.length, 0);
  });
});

test("closes and retries after malformed or out-of-state daemon messages", function () {
  var peer = createPeer();
  start(peer);
  peer.sockets[0].open();
  peer.sockets[0].receive({ version: 1, type: "snapshot.request", request_id: "before-authentication" });
  assert.equal(peer.sockets[0].closed, true);
  assert.equal(peer.timers[0].delay, 1000);
});

test("forwards adapter logs to the daemon after authentication", function () {
  var peer = createPeer();
  start(peer);
  peer.sockets[0].open();
  peer.sockets[0].receive({ version: 1, type: "hello.accepted" });
  peer.sockets[0].receive({ version: 1, type: "unexpected" });
  assert.deepEqual(peer.sockets[0].sent[1], {
    version: 1,
    type: "log",
    level: "warn",
    message: "Offbeat adapter received an invalid daemon message."
  });
});

test("closes and retries after an oversized daemon message", function () {
  var peer = createPeer();
  start(peer);
  peer.sockets[0].open();
  peer.sockets[0].receiveRaw("x".repeat(16 * 1024 * 1024 + 1));
  assert.equal(peer.sockets[0].closed, true);
  assert.equal(peer.timers[0].delay, 1000);
});

test("session conflict remains recoverable", function () {
  var peer = createPeer();
  start(peer);
  peer.sockets[0].open();
  peer.sockets[0].receive({ version: 1, type: "error", code: "session_conflict", message: "busy" });
  peer.sockets[0].disconnect();
  assert.equal(peer.timers[0].delay, 1000);
});
