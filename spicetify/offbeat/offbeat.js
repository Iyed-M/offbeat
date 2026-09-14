(function (global) {
  "use strict";

  var PROTOCOL_VERSION = 1;
  var INITIAL_RECONNECT_DELAY_MS = 1000;
  var MAX_RECONNECT_DELAY_MS = 30000;

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
        if (typeof message.code !== "string" || typeof message.message !== "string") {
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
        send({
          version: PROTOCOL_VERSION,
          type: "snapshot.response",
          request_id: message.request_id,
          snapshot: {
            kind: "synthetic",
            marker: "offbeat-m2"
          }
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
    module.exports = { createAdapter: createAdapter };
  }

  startConfiguredAdapter();
})(typeof globalThis === "undefined" ? this : globalThis);
