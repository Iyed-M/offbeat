package app

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

const (
	adapterProtocolVersion = 1
	adapterMaxMessageBytes = 16 << 20
)

type adapterSession struct {
	conn *websocket.Conn
}

func (d *Daemon) serveAdapter(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	d.addAdapterConnection(conn)
	defer d.removeAdapterConnection(conn)
	defer conn.CloseNow()
	// Keep the transport bound while allowing the protocol layer to reject a
	// slightly oversized JSON message with its required application error.
	conn.SetReadLimit(adapterMaxMessageBytes + 1024)

	helloCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	messageType, data, err := conn.Read(helloCtx)
	if err != nil {
		if errors.Is(err, websocket.ErrMessageTooBig) {
			d.writeAdapterError(conn, adapterErrorInvalidMessage)
		}
		return
	}
	if messageType != websocket.MessageText || len(data) > adapterMaxMessageBytes {
		d.writeAdapterError(conn, adapterErrorInvalidMessage)
		return
	}
	credential, protocolErr := parseHello(data)
	if protocolErr != "" {
		d.writeAdapterError(conn, protocolErr)
		return
	}
	if subtle.ConstantTimeCompare([]byte(credential), []byte(d.adapterCredential)) != 1 {
		d.writeAdapterError(conn, adapterErrorAuthenticationFailed)
		return
	}

	session := &adapterSession{conn: conn}
	d.adapterMu.Lock()
	if d.adapterSession != nil {
		d.adapterMu.Unlock()
		d.writeAdapterError(conn, adapterErrorSessionConflict)
		return
	}
	d.adapterSession = session
	d.adapterMu.Unlock()
	defer d.removeAdapterSession(session)

	if err := writeAdapterMessage(ctx, conn, struct {
		Version int    `json:"version"`
		Type    string `json:"type"`
	}{adapterProtocolVersion, "hello.accepted"}); err != nil {
		return
	}

	// This ticket establishes session ownership only. Any post-authentication
	// application message is invalid until snapshot exchange work is added.
	messageType, data, err = conn.Read(ctx)
	if err != nil {
		if errors.Is(err, websocket.ErrMessageTooBig) {
			d.removeAdapterSession(session)
			d.writeAdapterError(conn, adapterErrorInvalidMessage)
		}
		return
	}
	if messageType != websocket.MessageText || len(data) > adapterMaxMessageBytes {
		d.removeAdapterSession(session)
		d.writeAdapterError(conn, adapterErrorInvalidMessage)
		return
	}
	d.removeAdapterSession(session)
	d.writeAdapterError(conn, postAuthenticationError(data))
}

const (
	adapterErrorAuthenticationFailed = "authentication_failed"
	adapterErrorSessionConflict      = "session_conflict"
	adapterErrorUnsupportedVersion   = "unsupported_version"
	adapterErrorInvalidMessage       = "invalid_message"
)

func parseHello(data []byte) (string, string) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return "", adapterErrorInvalidMessage
	}
	if len(fields) != 3 {
		return "", adapterErrorInvalidMessage
	}
	for key := range fields {
		if key != "version" && key != "type" && key != "credential" {
			return "", adapterErrorInvalidMessage
		}
	}
	var version int
	var messageType, credential string
	if err := json.Unmarshal(fields["version"], &version); err != nil {
		return "", adapterErrorInvalidMessage
	}
	if version != adapterProtocolVersion {
		return "", adapterErrorUnsupportedVersion
	}
	if err := json.Unmarshal(fields["type"], &messageType); err != nil || messageType != "hello" {
		return "", adapterErrorInvalidMessage
	}
	if err := json.Unmarshal(fields["credential"], &credential); err != nil || credential == "" {
		return "", adapterErrorInvalidMessage
	}
	return credential, ""
}

func postAuthenticationError(data []byte) string {
	var message struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &message); err == nil && message.Version != adapterProtocolVersion {
		return adapterErrorUnsupportedVersion
	}
	return adapterErrorInvalidMessage
}

func (d *Daemon) writeAdapterError(conn *websocket.Conn, code string) {
	messages := map[string]string{
		adapterErrorAuthenticationFailed: "Adapter authentication failed.",
		adapterErrorSessionConflict:      "A Spotify adapter is already connected.",
		adapterErrorUnsupportedVersion:   "Unsupported adapter protocol version.",
		adapterErrorInvalidMessage:       "Invalid adapter message.",
	}
	_ = writeAdapterMessage(context.Background(), conn, struct {
		Version int    `json:"version"`
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}{adapterProtocolVersion, "error", code, messages[code]})
	_ = conn.Close(websocket.StatusPolicyViolation, messages[code])
}

func writeAdapterMessage(ctx context.Context, conn *websocket.Conn, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func (d *Daemon) adapterConnected() bool {
	d.adapterMu.Lock()
	defer d.adapterMu.Unlock()
	return d.adapterSession != nil
}

func (d *Daemon) removeAdapterSession(session *adapterSession) {
	d.adapterMu.Lock()
	defer d.adapterMu.Unlock()
	if d.adapterSession == session {
		d.adapterSession = nil
	}
}

func (d *Daemon) closeAdapterSession() {
	d.adapterMu.Lock()
	connections := make([]*websocket.Conn, 0, len(d.adapterConnections))
	for conn := range d.adapterConnections {
		connections = append(connections, conn)
	}
	d.adapterMu.Unlock()
	for _, conn := range connections {
		_ = conn.Close(websocket.StatusGoingAway, "Daemon shutting down.")
	}
}

func (d *Daemon) addAdapterConnection(conn *websocket.Conn) {
	d.adapterMu.Lock()
	defer d.adapterMu.Unlock()
	if d.adapterConnections == nil {
		d.adapterConnections = make(map[*websocket.Conn]struct{})
	}
	d.adapterConnections[conn] = struct{}{}
}

func (d *Daemon) removeAdapterConnection(conn *websocket.Conn) {
	d.adapterMu.Lock()
	defer d.adapterMu.Unlock()
	delete(d.adapterConnections, conn)
}
