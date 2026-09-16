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
      return { items: [supportedTrack("spotify:track:duplicate", "Duplicate"), supportedTrack("spotify:track:duplicate", "Duplicate"), { type: "episode", uri: "spotify:episode:one" }], totalLength: 3 };
    } },
    LibraryAPI: { getTracks: async function (request) {
      if (request.offset === 0) return { items: [supportedTrack("spotify:track:liked", "Liked")], totalLength: 2 };
      return { items: [{ type: "track", uri: "spotify:track:unplayable", isPlayable: false }], totalLength: 2 };
    } }
  });
  assert.deepEqual(calls, [["spotify:playlist:nested", 0], ["spotify:playlist:empty", 0]]);
  assert.equal(snapshot.playlists[0].entries[1].track.uri, "spotify:track:duplicate");
  assert.deepEqual(snapshot.playlists[0].entries[2], { position: 2, kind: "unsupported", source_uri: "spotify:episode:one" });
  assert.deepEqual(snapshot.liked_songs.entries[1], { position: 1, kind: "unsupported", source_uri: "spotify:track:unplayable" });
});

test("rejects malformed pages and unavailable Platform APIs", async function () {
  await assert.rejects(require("./offbeat.js").collectSnapshot({}), /required Spotify Platform API/);
  await assert.rejects(require("./offbeat.js").collectSnapshot({
    RootlistAPI: { getContents: async function () { return { items: [{ type: "playlist", uri: "spotify:playlist:one", name: "One" }] }; } },
    PlaylistAPI: { getContents: async function () { return { items: [], totalLength: 1 }; } },
    LibraryAPI: { getTracks: async function () { return { items: [], totalLength: 0 }; } }
  }), /inconsistent pagination/);
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
