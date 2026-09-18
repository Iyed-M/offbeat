package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/Iyed-M/offbeat/internal/desired"
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
	result    chan snapshotCompletion
	ctx       context.Context
	cancel    context.CancelFunc
	applying  bool
}

type snapshotCompletion struct {
	result ipc.SpotifySyncResult
	err    error
}

func (d *Daemon) serveAdapter(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	if !d.addAdapterConnection(conn) {
		_ = conn.Close(websocket.StatusGoingAway, "Daemon shutting down.")
		return
	}
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
	if d.adapterStopping || d.adapterSession != nil || d.adapterReserved {
		d.adapterMu.Unlock()
		if d.adapterStopping {
			_ = conn.Close(websocket.StatusGoingAway, "Daemon shutting down.")
		} else {
			d.writeAdapterError(conn, adapterErrorSessionConflict)
		}
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
	if d.adapterStopping {
		d.adapterMu.Unlock()
		_ = conn.Close(websocket.StatusGoingAway, "Daemon shutting down.")
		return
	}
	d.adapterSession = session
	d.adapterMu.Unlock()
	defer d.removeAdapterSession(session)
	sessionCtx, stopLiveness := context.WithCancel(ctx)
	livenessDone := make(chan struct{})
	go func() {
		defer close(livenessDone)
		d.maintainAdapterLiveness(sessionCtx, session)
	}()
	defer func() {
		stopLiveness()
		<-livenessDone
	}()

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
		if protocolErr := d.acceptAdapterMessage(session, data); protocolErr != "" {
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

// handleSpotifySync owns the one M3 request until the matching response,
// disconnect, timeout, or daemon shutdown completes it.
func (d *Daemon) handleSpotifySync(ctx context.Context) (any, error) {
	requestID, err := newSnapshotRequestID()
	if err != nil {
		return nil, fmt.Errorf("generate snapshot request ID: %w", err)
	}
	applyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	pending := &pendingSnapshot{requestID: requestID, result: make(chan snapshotCompletion, 1), ctx: applyCtx, cancel: cancel}

	d.adapterMu.Lock()
	if d.adapterStopping || d.adapterSession == nil {
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
	case completion := <-pending.result:
		return spotifySyncCompletion(completion)
	case <-timer.C:
		message := "Timed out waiting for Spotify adapter snapshot response."
		if d.invalidateSnapshotResponseWait(pending.session, pending, errors.New(message), message) {
			return nil, ipc.NewError(ipc.CodeFailedPrecondition, message)
		}
		return spotifySyncCompletion(<-pending.result)
	case <-ctx.Done():
		message := "Spotify synchronization was cancelled."
		if d.invalidateAdapterSession(pending.session, pending, errors.New(message), message) {
			return nil, ipc.NewError(ipc.CodeFailedPrecondition, message)
		}
		return spotifySyncCompletion(<-pending.result)
	}
}

func spotifySyncCompletion(completion snapshotCompletion) (any, error) {
	if completion.err == nil {
		return completion.result, nil
	}
	var persistenceErr *persistenceError
	if errors.As(completion.err, &persistenceErr) {
		return nil, ipc.NewError(ipc.CodeInternal, "could not commit Spotify desired state")
	}
	return nil, ipc.NewError(ipc.CodeFailedPrecondition, completion.err.Error())
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
	if pending.applying {
		d.adapterMu.Unlock()
		return adapterErrorInvalidMessage
	}
	pending.applying = true
	d.adapterMu.Unlock()
	if response.Err != nil {
		d.completeSnapshotResponse(pending, snapshotCompletion{err: response.Err})
		return ""
	}
	metadata, summary, err := d.DB.ApplyInitialDesiredSpotifyState(pending.ctx, response.Candidate)
	if err != nil {
		d.completeSnapshotResponse(pending, snapshotCompletion{err: &persistenceError{err}})
		return ""
	}
	d.completeSnapshotResponse(pending, snapshotCompletion{result: ipc.SpotifySyncResult{
		Changed: true, StateRevision: metadata.Revision,
		PlaylistCount: summary.PlaylistCount, PlaylistEntryCount: summary.PlaylistEntryCount,
		LikedSongsEntryCount:        summary.LikedSongsEntryCount,
		SupportedEntryOccurrences:   summary.SupportedEntryOccurrences,
		UnsupportedEntryOccurrences: summary.UnsupportedEntryOccurrences,
	}})
	return ""
}

type persistenceError struct{ error }

func (d *Daemon) completeSnapshotResponse(pending *pendingSnapshot, completion snapshotCompletion) {
	d.adapterMu.Lock()
	if d.pendingSnapshot != pending {
		d.adapterMu.Unlock()
		return
	}
	d.pendingSnapshot = nil
	d.adapterMu.Unlock()
	pending.result <- completion
}

// invalidateSnapshotResponseWait only expires a request that is still waiting
// for the Adapter response. Once a matching response is accepted, persistence
// owns completion and must be allowed to report its committed result.
func (d *Daemon) invalidateSnapshotResponseWait(session *adapterSession, expected *pendingSnapshot, pendingErr error, closeReason string) bool {
	d.adapterMu.Lock()
	if d.adapterSession != session || d.pendingSnapshot != expected || expected.applying {
		d.adapterMu.Unlock()
		return false
	}
	d.adapterSession = nil
	d.pendingSnapshot = nil
	d.adapterMu.Unlock()
	expected.cancel()
	expected.result <- snapshotCompletion{err: pendingErr}
	_ = session.conn.Close(websocket.StatusGoingAway, closeReason)
	return true
}

func (d *Daemon) acceptAdapterMessage(session *adapterSession, data []byte) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return adapterErrorInvalidMessage
	}
	if versionField, ok := fields["version"]; ok {
		var version int
		if err := json.Unmarshal(versionField, &version); err != nil {
			return adapterErrorInvalidMessage
		}
		if version != adapterProtocolVersion {
			return adapterErrorUnsupportedVersion
		}
	}
	messageType, ok := fields["type"]
	if !ok {
		return adapterErrorInvalidMessage
	}
	var messageTypeValue string
	if err := json.Unmarshal(messageType, &messageTypeValue); err != nil {
		return adapterErrorInvalidMessage
	}
	if messageTypeValue == "snapshot.response" {
		return d.acceptSnapshotResponse(session, data)
	}
	if messageTypeValue != "log" {
		return adapterErrorInvalidMessage
	}

	level, message, protocolErr := parseAdapterLog(data)
	if protocolErr != "" {
		return protocolErr
	}
	if d.Logger != nil {
		d.Logger.Log(context.Background(), level, message, "source", "spicetify")
	}
	return ""
}

func parseAdapterLog(data []byte) (slog.Level, string, string) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return 0, "", adapterErrorInvalidMessage
	}
	if len(fields) != 4 {
		return 0, "", adapterErrorInvalidMessage
	}
	for key := range fields {
		if key != "version" && key != "type" && key != "level" && key != "message" {
			return 0, "", adapterErrorInvalidMessage
		}
	}
	var version int
	var messageType, levelName, message string
	if err := json.Unmarshal(fields["version"], &version); err != nil {
		return 0, "", adapterErrorInvalidMessage
	}
	if version != adapterProtocolVersion {
		return 0, "", adapterErrorUnsupportedVersion
	}
	if err := json.Unmarshal(fields["type"], &messageType); err != nil || messageType != "log" {
		return 0, "", adapterErrorInvalidMessage
	}
	if err := json.Unmarshal(fields["level"], &levelName); err != nil {
		return 0, "", adapterErrorInvalidMessage
	}
	if err := json.Unmarshal(fields["message"], &message); err != nil || message == "" {
		return 0, "", adapterErrorInvalidMessage
	}
	switch levelName {
	case "debug":
		return slog.LevelDebug, message, ""
	case "info":
		return slog.LevelInfo, message, ""
	case "warn":
		return slog.LevelWarn, message, ""
	case "error":
		return slog.LevelError, message, ""
	default:
		return 0, "", adapterErrorInvalidMessage
	}
}

type snapshotResponse struct {
	RequestID string
	Err       error
	Candidate desired.Candidate
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
		if key != "version" && key != "type" && key != "request_id" && key != "snapshot" && key != "error" {
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
	if snapshot, ok := fields["snapshot"]; ok {
		candidate, err := parseCandidateSnapshot(snapshot)
		if _, ok := fields["error"]; ok || err != nil {
			return snapshotResponse{}, adapterErrorInvalidMessage
		}
		return snapshotResponse{RequestID: requestID, Candidate: candidate}, ""
	}
	collectionErr, ok := parseCollectionError(fields["error"])
	if !ok {
		return snapshotResponse{}, adapterErrorInvalidMessage
	}
	return snapshotResponse{RequestID: requestID, Err: collectionErr}, ""
}

// parseCandidateSnapshot deliberately accepts only normalized M3 data and
// materializes it into Offbeat-owned values. Spotify Desktop objects stay
// contained in the extension-local adapter.
func parseCandidateSnapshot(data json.RawMessage) (desired.Candidate, error) {
	var snapshot struct {
		Kind       string            `json:"kind"`
		Playlists  []json.RawMessage `json:"playlists"`
		LikedSongs json.RawMessage   `json:"liked_songs"`
	}
	if !decodeExactObject(data, &snapshot, "kind", "playlists", "liked_songs") || snapshot.Kind != "candidate" || snapshot.Playlists == nil {
		return desired.Candidate{}, errors.New("invalid candidate snapshot")
	}
	candidate := desired.Candidate{Playlists: make([]desired.CandidatePlaylist, 0, len(snapshot.Playlists))}
	tracks := make(map[string]desired.Track)
	playlists := make(map[string]struct{}, len(snapshot.Playlists))
	for position, playlist := range snapshot.Playlists {
		var item struct {
			URI     string            `json:"uri"`
			Name    string            `json:"name"`
			Entries []json.RawMessage `json:"entries"`
		}
		if !decodeExactObject(playlist, &item, "uri", "name", "entries") || item.URI == "" || item.Name == "" || item.Entries == nil {
			return desired.Candidate{}, errors.New("invalid candidate playlist")
		}
		if _, exists := playlists[item.URI]; exists {
			return desired.Candidate{}, errors.New("duplicate candidate playlist")
		}
		playlists[item.URI] = struct{}{}
		entries, err := materializeEntries(item.Entries, tracks)
		if err != nil {
			return desired.Candidate{}, fmt.Errorf("materialize playlist entries: %w", err)
		}
		candidate.Playlists = append(candidate.Playlists, desired.CandidatePlaylist{URI: item.URI, Name: item.Name, Position: position, Entries: entries})
	}
	var liked struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if !decodeExactObject(snapshot.LikedSongs, &liked, "entries") || liked.Entries == nil {
		return desired.Candidate{}, errors.New("invalid liked songs")
	}
	entries, err := materializeEntries(liked.Entries, tracks)
	if err != nil {
		return desired.Candidate{}, fmt.Errorf("materialize liked songs entries: %w", err)
	}
	candidate.LikedSongs = entries
	return candidate, nil
}

func materializeEntries(entries []json.RawMessage, tracks map[string]desired.Track) ([]desired.CandidateEntry, error) {
	result := make([]desired.CandidateEntry, 0, len(entries))
	for position, raw := range entries {
		entry, ok := decodeObject(raw)
		if !ok {
			return nil, errors.New("invalid entry")
		}
		var actualPosition int
		if value, ok := entry["position"]; !ok || json.Unmarshal(value, &actualPosition) != nil || actualPosition != position {
			return nil, errors.New("non-contiguous entry position")
		}
		var kind string
		if value, ok := entry["kind"]; !ok || json.Unmarshal(value, &kind) != nil {
			return nil, errors.New("invalid entry kind")
		}
		switch kind {
		case "supported":
			if len(entry) != 3 {
				return nil, errors.New("invalid supported entry")
			}
			track, err := materializeTrack(entry["track"])
			if err != nil {
				return nil, err
			}
			if known, exists := tracks[track.URI]; exists && !reflect.DeepEqual(known, track) {
				return nil, fmt.Errorf("conflicting metadata for track %q", track.URI)
			}
			tracks[track.URI] = track
			trackCopy := track
			result = append(result, desired.CandidateEntry{Position: position, Kind: desired.EntrySupported, Track: &trackCopy})
		case "unsupported":
			if len(entry) != 2 && len(entry) != 3 {
				return nil, errors.New("invalid unsupported entry")
			}
			for name := range entry {
				if name != "position" && name != "kind" && name != "source_uri" {
					return nil, errors.New("invalid unsupported entry")
				}
			}
			candidateEntry := desired.CandidateEntry{Position: position, Kind: desired.EntryUnsupported}
			if value, ok := entry["source_uri"]; ok {
				var sourceURI string
				if json.Unmarshal(value, &sourceURI) != nil || sourceURI == "" {
					return nil, errors.New("invalid unsupported source URI")
				}
				candidateEntry.SourceURI = sourceURI
			}
			result = append(result, candidateEntry)
		default:
			return nil, errors.New("unknown entry kind")
		}
	}
	return result, nil
}

func materializeTrack(data json.RawMessage) (desired.Track, error) {
	var track struct {
		URI        string            `json:"uri"`
		Name       string            `json:"name"`
		Artists    []json.RawMessage `json:"artists"`
		Album      json.RawMessage   `json:"album"`
		DurationMS int               `json:"duration_ms"`
	}
	if !decodeExactObject(data, &track, "uri", "name", "artists", "album", "duration_ms") || track.URI == "" || track.Name == "" || track.DurationMS <= 0 || len(track.Artists) == 0 {
		return desired.Track{}, errors.New("invalid track")
	}
	result := desired.Track{URI: track.URI, Name: track.Name, Artists: make([]desired.NamedURI, 0, len(track.Artists)), DurationMS: track.DurationMS}
	for _, rawArtist := range track.Artists {
		artist, err := materializeNamedURI(rawArtist)
		if err != nil {
			return desired.Track{}, err
		}
		result.Artists = append(result.Artists, artist)
	}
	album, err := materializeNamedURI(track.Album)
	if err != nil {
		return desired.Track{}, err
	}
	result.Album = album
	return result, nil
}

func materializeNamedURI(data json.RawMessage) (desired.NamedURI, error) {
	var value struct {
		URI  string `json:"uri"`
		Name string `json:"name"`
	}
	if !decodeExactObject(data, &value, "uri", "name") || value.URI == "" || value.Name == "" {
		return desired.NamedURI{}, errors.New("invalid named URI")
	}
	return desired.NamedURI{URI: value.URI, Name: value.Name}, nil
}

func parseCollectionError(data json.RawMessage) (error, bool) {
	var value struct {
		Operation string `json:"operation"`
		Offset    *int   `json:"offset"`
		Message   string `json:"message"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) < 2 || len(fields) > 3 || fields["operation"] == nil || fields["message"] == nil {
		return nil, false
	}
	for key := range fields {
		if key != "operation" && key != "offset" && key != "message" {
			return nil, false
		}
	}
	if json.Unmarshal(data, &value) != nil || value.Operation == "" || value.Message == "" || (value.Offset != nil && *value.Offset < 0) {
		return nil, false
	}
	if value.Offset == nil {
		return fmt.Errorf("Spotify snapshot rejected during %s: %s", value.Operation, value.Message), true
	}
	return fmt.Errorf("Spotify snapshot rejected during %s at offset %d: %s", value.Operation, *value.Offset, value.Message), true
}

func decodeExactObject(data json.RawMessage, target any, names ...string) bool {
	fields, ok := decodeObject(data)
	if !ok || len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	return json.Unmarshal(data, target) == nil
}

// decodeObject rejects duplicate member names, which encoding/json's map
// decoding would otherwise silently overwrite.
func decodeObject(data json.RawMessage) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, false
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, exists := fields[name]; exists {
			return nil, false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, false
		}
		fields[name] = value
	}
	token, err = decoder.Token()
	if err != nil {
		return nil, false
	}
	delimiter, ok = token.(json.Delim)
	if !ok || delimiter != '}' {
		return nil, false
	}
	var extra any
	return fields, decoder.Decode(&extra) == io.EOF
}

func (d *Daemon) completePendingSnapshot(pending *pendingSnapshot, err error) {
	d.adapterMu.Lock()
	if d.pendingSnapshot != pending {
		d.adapterMu.Unlock()
		return
	}
	d.pendingSnapshot = nil
	d.adapterMu.Unlock()
	pending.cancel()
	pending.result <- snapshotCompletion{err: err}
}

func (d *Daemon) adapterConnected() bool {
	d.adapterMu.Lock()
	defer d.adapterMu.Unlock()
	return !d.adapterStopping && d.adapterSession != nil
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
		pending.cancel()
		pending.result <- snapshotCompletion{err: errors.New("Spotify adapter disconnected while waiting for snapshot response.")}
		return
	}
	d.adapterMu.Unlock()
}

// stopAdapterWork prevents new adapter work and resolves the active request
// before closing transports, so a control request cannot outlive the daemon.
func (d *Daemon) stopAdapterWork() {
	d.adapterMu.Lock()
	d.adapterStopping = true
	pending := d.pendingSnapshot
	d.pendingSnapshot = nil
	connections := make([]*websocket.Conn, 0, len(d.adapterConnections))
	for conn := range d.adapterConnections {
		connections = append(connections, conn)
	}
	d.adapterMu.Unlock()
	if pending != nil {
		pending.cancel()
		pending.result <- snapshotCompletion{err: errors.New("Spotify synchronization was cancelled because the daemon is shutting down.")}
	}
	for _, conn := range connections {
		_ = conn.Close(websocket.StatusGoingAway, "Daemon shutting down.")
	}
}

func (d *Daemon) addAdapterConnection(conn *websocket.Conn) bool {
	d.adapterMu.Lock()
	defer d.adapterMu.Unlock()
	if d.adapterStopping {
		return false
	}
	if d.adapterConnections == nil {
		d.adapterConnections = make(map[*websocket.Conn]struct{})
	}
	d.adapterConnections[conn] = struct{}{}
	return true
}

func (d *Daemon) removeAdapterConnection(conn *websocket.Conn) {
	d.adapterMu.Lock()
	defer d.adapterMu.Unlock()
	delete(d.adapterConnections, conn)
}

func (d *Daemon) maintainAdapterLiveness(ctx context.Context, session *adapterSession) {
	ticker := time.NewTicker(d.livenessInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			window := d.livenessWindow - d.livenessInterval
			if window <= 0 {
				window = d.livenessWindow
			}
			pingCtx, cancel := context.WithTimeout(ctx, window)
			err := session.conn.Ping(pingCtx)
			cancel()
			if err == nil || ctx.Err() != nil {
				continue
			}
			d.expireAdapterSession(session)
			return
		}
	}
}

func (d *Daemon) expireAdapterSession(session *adapterSession) {
	d.invalidateAdapterSession(session, nil, errors.New("Spotify adapter became unresponsive while waiting for snapshot response."), "Adapter session liveness expired.")
}

// invalidateAdapterSession atomically removes the active session and, when
// requested, only the exact pending snapshot it owns. This lets timeout and
// cancellation win or lose cleanly against a simultaneous response.
func (d *Daemon) invalidateAdapterSession(session *adapterSession, expected *pendingSnapshot, pendingErr error, closeReason string) bool {
	d.adapterMu.Lock()
	if d.adapterSession != session {
		d.adapterMu.Unlock()
		return false
	}
	if expected != nil && d.pendingSnapshot != expected {
		d.adapterMu.Unlock()
		return false
	}
	d.adapterSession = nil
	pending := d.pendingSnapshot
	if pending != nil && pending.session == session {
		d.pendingSnapshot = nil
	} else {
		pending = nil
	}
	d.adapterMu.Unlock()
	if pending != nil {
		pending.cancel()
		pending.result <- snapshotCompletion{err: pendingErr}
	}
	_ = session.conn.Close(websocket.StatusGoingAway, closeReason)
	return true
}
