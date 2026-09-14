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

function start(peer) {
  var adapter = createAdapter({ endpoint: "ws://127.0.0.1:16352/v1/adapter", credential: "development-credential" }, {
    WebSocket: peer.WebSocket,
    setTimeout: peer.setTimeout,
    clearTimeout: peer.clearTimeout,
    logger: { error: function () {}, warn: function () {} }
  });
  adapter.start();
  return adapter;
}

test("sends the exact hello and only answers correlated snapshot requests after acceptance", function () {
  var peer = createPeer();
  start(peer);
  var socket = peer.sockets[0];
  socket.open();
  assert.deepEqual(socket.sent, [{ version: 1, type: "hello", credential: "development-credential" }]);

  socket.receive({ version: 1, type: "hello.accepted" });
  socket.receive({ version: 1, type: "snapshot.request", request_id: "opaque-request-id" });
  assert.deepEqual(socket.sent[1], {
    version: 1,
    type: "snapshot.response",
    request_id: "opaque-request-id",
    snapshot: { kind: "synthetic", marker: "offbeat-m2" }
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
