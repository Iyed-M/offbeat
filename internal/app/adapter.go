package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/Iyed-M/offbeat/internal/ipc"
	"github.com/coder/websocket"
)

const (
	adapterProtocolVersion = 1
	adapterMaxMessageBytes = 16 << 20
)

type adapterSession struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

type pendingSnapshot struct {
	session   *adapterSession
	requestID string
	result    chan error
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
	if d.adapterSession != nil || d.adapterReserved {
		d.adapterMu.Unlock()
		d.writeAdapterError(conn, adapterErrorSessionConflict)
		return
	}
	d.adapterReserved = true
	d.adapterMu.Unlock()

	if err := writeAdapterMessage(ctx, conn, struct {
		Version int    `json:"version"`
		Type    string `json:"type"`
	}{adapterProtocolVersion, "hello.accepted"}); err != nil {
		d.releaseAdapterReservation()
		return
	}

	d.adapterMu.Lock()
	d.adapterReserved = false
	d.adapterSession = session
	d.adapterMu.Unlock()
	defer d.removeAdapterSession(session)

	for {
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
		if protocolErr := d.acceptSnapshotResponse(session, data); protocolErr != "" {
			d.removeAdapterSession(session)
			d.writeAdapterError(conn, protocolErr)
			return
		}
	}
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

func (s *adapterSession) write(ctx context.Context, value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return writeAdapterMessage(ctx, s.conn, value)
}

// handleSpotifySync owns the one M2 request until the matching response,
// disconnect, timeout, or daemon shutdown completes it.
func (d *Daemon) handleSpotifySync(ctx context.Context) (any, error) {
	requestID, err := newSnapshotRequestID()
	if err != nil {
		return nil, fmt.Errorf("generate snapshot request ID: %w", err)
	}
	pending := &pendingSnapshot{requestID: requestID, result: make(chan error, 1)}

	d.adapterMu.Lock()
	if d.adapterSession == nil {
		d.adapterMu.Unlock()
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "Spotify adapter is not connected.")
	}
	if d.pendingSnapshot != nil {
		d.adapterMu.Unlock()
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, "Spotify synchronization is already in progress.")
	}
	pending.session = d.adapterSession
	d.pendingSnapshot = pending
	d.adapterMu.Unlock()

	if err := pending.session.write(ctx, snapshotRequest{Version: adapterProtocolVersion, Type: "snapshot.request", RequestID: requestID}); err != nil {
		message := "Spotify adapter disconnected before receiving the snapshot request."
		d.completePendingSnapshot(pending, errors.New(message))
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, message)
	}

	timer := time.NewTimer(d.snapshotTimeout)
	defer timer.Stop()
	select {
	case err := <-pending.result:
		if err != nil {
			return nil, ipc.NewError(ipc.CodeFailedPrecondition, err.Error())
		}
		return struct{}{}, nil
	case <-timer.C:
		message := "Timed out waiting for Spotify adapter snapshot response."
		d.completePendingSnapshot(pending, errors.New(message))
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, message)
	case <-ctx.Done():
		message := "Spotify synchronization was cancelled."
		d.completePendingSnapshot(pending, errors.New(message))
		return nil, ipc.NewError(ipc.CodeFailedPrecondition, message)
	}
}

func newSnapshotRequestID() (string, error) {
	bytes := make([]byte, 16) // 128 bits of cryptographically random entropy.
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

type snapshotRequest struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
}

func (d *Daemon) acceptSnapshotResponse(session *adapterSession, data []byte) string {
	response, protocolErr := parseSnapshotResponse(data)
	if protocolErr != "" {
		return protocolErr
	}
	d.adapterMu.Lock()
	pending := d.pendingSnapshot
	if d.adapterSession != session || pending == nil || pending.session != session || pending.requestID != response.RequestID {
		d.adapterMu.Unlock()
		return adapterErrorInvalidMessage
	}
	d.pendingSnapshot = nil
	d.adapterMu.Unlock()
	pending.result <- nil
	return ""
}

type snapshotResponse struct {
	RequestID string
}

func parseSnapshotResponse(data []byte) (snapshotResponse, string) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return snapshotResponse{}, adapterErrorInvalidMessage
	}
	if versionField, ok := fields["version"]; ok {
		var version int
		if err := json.Unmarshal(versionField, &version); err != nil {
			return snapshotResponse{}, adapterErrorInvalidMessage
		}
		if version != adapterProtocolVersion {
			return snapshotResponse{}, adapterErrorUnsupportedVersion
		}
	}
	if len(fields) != 4 {
		return snapshotResponse{}, adapterErrorInvalidMessage
	}
	for key := range fields {
		if key != "version" && key != "type" && key != "request_id" && key != "snapshot" {
			return snapshotResponse{}, adapterErrorInvalidMessage
		}
	}
	var version int
	var messageType, requestID string
	if err := json.Unmarshal(fields["version"], &version); err != nil {
		return snapshotResponse{}, adapterErrorInvalidMessage
	}
	if version != adapterProtocolVersion {
		return snapshotResponse{}, adapterErrorUnsupportedVersion
	}
	if err := json.Unmarshal(fields["type"], &messageType); err != nil || messageType != "snapshot.response" {
		return snapshotResponse{}, adapterErrorInvalidMessage
	}
	if err := json.Unmarshal(fields["request_id"], &requestID); err != nil || requestID == "" {
		return snapshotResponse{}, adapterErrorInvalidMessage
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(fields["snapshot"], &snapshot); err != nil || len(snapshot) != 2 {
		return snapshotResponse{}, adapterErrorInvalidMessage
	}
	var kind, marker string
	if err := json.Unmarshal(snapshot["kind"], &kind); err != nil || kind != "synthetic" {
		return snapshotResponse{}, adapterErrorInvalidMessage
	}
	if err := json.Unmarshal(snapshot["marker"], &marker); err != nil || marker != "offbeat-m2" {
		return snapshotResponse{}, adapterErrorInvalidMessage
	}
	return snapshotResponse{RequestID: requestID}, ""
}

func (d *Daemon) completePendingSnapshot(pending *pendingSnapshot, err error) {
	d.adapterMu.Lock()
	if d.pendingSnapshot != pending {
		d.adapterMu.Unlock()
		return
	}
	d.pendingSnapshot = nil
	d.adapterMu.Unlock()
	pending.result <- err
}

func (d *Daemon) adapterConnected() bool {
	d.adapterMu.Lock()
	defer d.adapterMu.Unlock()
	return d.adapterSession != nil
}

func (d *Daemon) releaseAdapterReservation() {
	d.adapterMu.Lock()
	d.adapterReserved = false
	d.adapterMu.Unlock()
}

func (d *Daemon) removeAdapterSession(session *adapterSession) {
	d.adapterMu.Lock()
	if d.adapterSession == session {
		d.adapterSession = nil
	}
	pending := d.pendingSnapshot
	if pending != nil && pending.session == session {
		d.pendingSnapshot = nil
		d.adapterMu.Unlock()
		pending.result <- errors.New("Spotify adapter disconnected while waiting for snapshot response.")
		return
	}
	d.adapterMu.Unlock()
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
