package app

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/ipc"
	"github.com/coder/websocket"
)

func TestSpotifySyncRequiresAdapter(t *testing.T) {
	d := startAdapterDaemon(t)
	response := sendRaw(t, SocketPath(d.socketDir), []byte(`{"version":1,"command":"spotify.sync"}`+"\n"))
	assertSyncFailure(t, response, "Spotify adapter is not connected.")
}

func TestReservedAdapterHandshakeIsNotConnectedOrSyncable(t *testing.T) {
	d := startAdapterDaemon(t)
	d.adapterMu.Lock()
	d.adapterReserved = true
	d.adapterMu.Unlock()

	assertAdapterConnected(t, d, false)
	response := sendRaw(t, SocketPath(d.socketDir), []byte(`{"version":1,"command":"spotify.sync"}`+"\n"))
	assertSyncFailure(t, response, "Spotify adapter is not connected.")
}

func TestSpotifySyncCompletesOnlyForMatchingCandidateResponse(t *testing.T) {
	d := startAdapterDaemon(t)
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))

	done := sendSync(t, d)
	request := readAdapterMessage(t, adapter)
	requestID := assertSnapshotRequest(t, request)
	writeCandidateResponse(t, adapter, requestID)
	response := syncResponse(t, <-done)
	assertSyncSuccess(t, response)
	result := decodeSyncResult(t, response)
	if !result.Changed || result.StateRevision != 1 || result.PlaylistCount != 0 || result.PlaylistEntryCount != 0 || result.LikedSongsEntryCount != 0 || result.SupportedEntryOccurrences != 0 || result.UnsupportedEntryOccurrences != 0 {
		t.Fatalf("sync result = %#v", result)
	}
	_, metadata, err := d.DB.ReadDesiredSpotifyState(context.Background())
	if err != nil {
		t.Fatalf("read committed state: %v", err)
	}
	if metadata.Revision != 1 || metadata.LastCommittedAt == nil {
		t.Fatalf("state was not committed before sync success: %#v", metadata)
	}
}

func TestSpotifySyncSanitizesPersistenceFailureAndRollsBack(t *testing.T) {
	d := startAdapterDaemon(t)
	if _, err := d.DB.Exec(`CREATE TRIGGER fail_liked_entry BEFORE INSERT ON liked_entries BEGIN SELECT RAISE(ABORT, 'raw sqlite failure'); END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))
	done := sendSync(t, d)
	requestID := assertSnapshotRequest(t, readAdapterMessage(t, adapter))
	writeAdapterJSON(t, adapter, map[string]any{
		"version": 1, "type": "snapshot.response", "request_id": requestID,
		"snapshot": map[string]any{"kind": "candidate", "playlists": []any{}, "liked_songs": map[string]any{"entries": []any{map[string]any{"position": 0, "kind": "unsupported"}}}},
	})
	response := syncResponse(t, <-done)
	if response.Error == nil || response.Error.Code != ipc.CodeInternal || response.Error.Message != "could not commit Spotify desired state" || strings.Contains(response.Error.Message, "sqlite") {
		t.Fatalf("sync failure = %#v", response.Error)
	}
	state, metadata, err := d.DB.ReadDesiredSpotifyState(context.Background())
	if err != nil {
		t.Fatalf("read rolled back state: %v", err)
	}
	if len(state.Tracks) != 0 || len(state.Playlists) != 0 || len(state.LikedSongs) != 0 || metadata.Revision != 0 || metadata.LastCommittedAt != nil {
		t.Fatalf("state survived failed sync: state=%#v metadata=%#v", state, metadata)
	}
}

func TestSpotifySyncReportsCorrelatedCollectionFailure(t *testing.T) {
	d := startAdapterDaemon(t)
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))

	done := sendSync(t, d)
	requestID := assertSnapshotRequest(t, readAdapterMessage(t, adapter))
	writeAdapterJSON(t, adapter, map[string]any{
		"version": 1, "type": "snapshot.response", "request_id": requestID,
		"error": map[string]any{"operation": "playlist", "offset": 100, "message": "request failed"},
	})
	assertSyncFailure(t, syncResponse(t, <-done), "rejected during playlist at offset 100")
}

func TestSpotifySyncRejectsConcurrentRequest(t *testing.T) {
	d := startAdapterDaemon(t)
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))

	done := sendSync(t, d)
	requestID := assertSnapshotRequest(t, readAdapterMessage(t, adapter))
	response := sendRaw(t, SocketPath(d.socketDir), []byte(`{"version":1,"command":"spotify.sync"}`+"\n"))
	assertSyncFailure(t, response, "already in progress")
	writeCandidateResponse(t, adapter, requestID)
	assertSyncSuccess(t, syncResponse(t, <-done))
}

func TestSpotifySyncFailsPromptlyWhenAdapterDisconnects(t *testing.T) {
	d := startAdapterDaemon(t)
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))

	done := sendSync(t, d)
	_ = readAdapterMessage(t, adapter)
	if err := adapter.Close(websocket.StatusNormalClosure, "test disconnect"); err != nil {
		t.Fatalf("close adapter: %v", err)
	}
	select {
	case response := <-done:
		assertSyncFailure(t, syncResponse(t, response), "disconnected")
	case <-time.After(time.Second):
		t.Fatal("sync did not fail after adapter disconnect")
	}
}

func TestSpotifySyncTimesOutWithoutResponse(t *testing.T) {
	d := startAdapterDaemon(t)
	d.snapshotTimeout = 30 * time.Millisecond
	endpoint := AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port)
	adapter := authenticateAdapter(t, endpoint)

	done := sendSync(t, d)
	_ = readAdapterMessage(t, adapter)
	assertSyncFailure(t, syncResponse(t, <-done), "Timed out waiting")
	assertAdapterClosed(t, adapter)
	waitForAdapterConnected(t, d, false)

	replacement := authenticateAdapter(t, endpoint)
	done = sendSync(t, d)
	requestID := assertSnapshotRequest(t, readAdapterMessage(t, replacement))
	writeCandidateResponse(t, replacement, requestID)
	assertSyncSuccess(t, syncResponse(t, <-done))
}

func TestSpotifySyncCancellationInvalidatesOwningAdapterSession(t *testing.T) {
	d := startAdapterDaemon(t)
	endpoint := AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port)
	adapter := authenticateAdapter(t, endpoint)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := d.handleSpotifySync(ctx)
		done <- err
	}()
	_ = readAdapterMessage(t, adapter)
	cancel()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("sync error = %v, want cancellation", err)
	}
	assertAdapterClosed(t, adapter)
	waitForAdapterConnected(t, d, false)

	replacement := authenticateAdapter(t, endpoint)
	doneSync := sendSync(t, d)
	requestID := assertSnapshotRequest(t, readAdapterMessage(t, replacement))
	writeCandidateResponse(t, replacement, requestID)
	assertSyncSuccess(t, syncResponse(t, <-doneSync))
}

func TestM3SnapshotTimeoutIsFiveMinutes(t *testing.T) {
	if m3SnapshotTimeout != 5*time.Minute {
		t.Fatalf("M3 snapshot timeout = %s, want 5m", m3SnapshotTimeout)
	}
}

func TestAdapterLivenessExpiryFailsPendingSync(t *testing.T) {
	d := startAdapterDaemon(t)
	d.livenessInterval = 5 * time.Millisecond
	d.livenessWindow = 50 * time.Millisecond
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))

	done := sendSync(t, d)
	_ = readAdapterMessage(t, adapter)
	select {
	case response := <-done:
		assertSyncFailure(t, syncResponse(t, response), "unresponsive")
	case <-time.After(time.Second):
		t.Fatal("sync did not fail after liveness expiry")
	}
}

func TestShutdownCancelsSyncAndClosesEveryAdapterConnection(t *testing.T) {
	d, signals, done := startAdapterDaemonWithoutCleanup(t)

	preAuthenticated := dialAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))
	syncDone := sendSync(t, d)
	_ = readAdapterMessage(t, adapter)
	signals <- os.Interrupt

	select {
	case response := <-syncDone:
		assertSyncFailure(t, syncResponse(t, response), "cancelled")
	case <-time.After(time.Second):
		t.Fatal("sync outlived daemon shutdown")
	}
	for name, conn := range map[string]*websocket.Conn{"pre-authenticated": preAuthenticated, "authenticated": adapter} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, _, err := conn.Read(ctx)
		cancel()
		if err == nil {
			t.Fatalf("%s adapter connection remained open during shutdown", name)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not finish adapter shutdown")
	}
	assertResourcesReleased(t, d)
	if _, err := os.Stat(SocketPath(d.socketDir)); !os.IsNotExist(err) {
		t.Fatalf("control socket remained after shutdown: %v", err)
	}
}

func TestSpotifySyncRejectsUnmatchedInvalidAndDuplicateResponses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response func(t *testing.T, conn *websocket.Conn, requestID string)
	}{
		{
			name: "unmatched ID",
			response: func(t *testing.T, conn *websocket.Conn, _ string) {
				writeCandidateResponse(t, conn, "wrong-request-id")
			},
		},
		{
			name: "invalid candidate payload",
			response: func(t *testing.T, conn *websocket.Conn, requestID string) {
				writeAdapterJSON(t, conn, map[string]any{
					"version": 1, "type": "snapshot.response", "request_id": requestID,
					"snapshot": map[string]any{"kind": "candidate", "playlists": []any{}, "liked_songs": map[string]any{"entries": []any{map[string]any{"position": 1, "kind": "unsupported"}}}},
				})
			},
		},
		{
			name: "unsupported entry with unknown field",
			response: func(t *testing.T, conn *websocket.Conn, requestID string) {
				writeAdapterJSON(t, conn, map[string]any{
					"version": 1, "type": "snapshot.response", "request_id": requestID,
					"snapshot": map[string]any{"kind": "candidate", "playlists": []any{}, "liked_songs": map[string]any{"entries": []any{map[string]any{"position": 0, "kind": "unsupported", "unexpected": true}}}},
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := startAdapterDaemon(t)
			adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))
			done := sendSync(t, d)
			tc.response(t, adapter, assertSnapshotRequest(t, readAdapterMessage(t, adapter)))
			assertAdapterMessage(t, adapter, "error", adapterErrorInvalidMessage)
			assertSyncFailure(t, syncResponse(t, <-done), "disconnected")
		})
	}

	t.Run("duplicate", func(t *testing.T) {
		d := startAdapterDaemon(t)
		adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))
		done := sendSync(t, d)
		requestID := assertSnapshotRequest(t, readAdapterMessage(t, adapter))
		writeCandidateResponse(t, adapter, requestID)
		assertSyncSuccess(t, syncResponse(t, <-done))
		writeCandidateResponse(t, adapter, requestID)
		assertAdapterMessage(t, adapter, "error", adapterErrorInvalidMessage)
	})
}

func TestObsoleteAdapterSessionCannotSatisfyLaterSync(t *testing.T) {
	d := startAdapterDaemon(t)
	endpoint := AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port)
	old := authenticateAdapter(t, endpoint)

	// Simulate the old transport being removed before its handler finishes.
	// Its pointer must remain incapable of completing work for the replacement.
	d.adapterMu.Lock()
	oldSession := d.adapterSession
	d.adapterMu.Unlock()
	d.removeAdapterSession(oldSession)
	current := authenticateAdapter(t, endpoint)

	done := sendSync(t, d)
	requestID := assertSnapshotRequest(t, readAdapterMessage(t, current))
	writeCandidateResponse(t, old, requestID)
	assertAdapterMessage(t, old, "error", adapterErrorInvalidMessage)
	writeCandidateResponse(t, current, requestID)
	assertSyncSuccess(t, syncResponse(t, <-done))
}

type syncResult struct {
	response ipc.Response
	err      error
}

func sendSync(t *testing.T, d *Daemon) <-chan syncResult {
	t.Helper()
	done := make(chan syncResult, 1)
	go func() {
		response, err := sendControl(SocketPath(d.socketDir), []byte(`{"version":1,"command":"spotify.sync"}`+"\n"))
		done <- syncResult{response: response, err: err}
	}()
	return done
}

func sendControl(socketPath string, payload []byte) (ipc.Response, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return ipc.Response{}, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	if _, err := conn.Write(payload); err != nil {
		return ipc.Response{}, fmt.Errorf("write: %w", err)
	}
	frame, err := ipc.ReadFrame(bufio.NewReader(conn))
	if err != nil {
		return ipc.Response{}, fmt.Errorf("read response: %w", err)
	}
	var response ipc.Response
	if err := ipc.Decode(frame, &response); err != nil {
		return ipc.Response{}, fmt.Errorf("decode response: %w", err)
	}
	return response, nil
}

func syncResponse(t *testing.T, result syncResult) ipc.Response {
	t.Helper()
	if result.err != nil {
		t.Fatal(result.err)
	}
	return result.response
}

func assertSnapshotRequest(t *testing.T, message map[string]any) string {
	t.Helper()
	if message["version"] != float64(adapterProtocolVersion) || message["type"] != "snapshot.request" {
		t.Fatalf("snapshot request = %#v", message)
	}
	requestID, ok := message["request_id"].(string)
	if !ok || requestID == "" {
		t.Fatalf("request ID = %#v", message["request_id"])
	}
	entropy, err := base64.RawURLEncoding.DecodeString(requestID)
	if err != nil || len(entropy) != 16 {
		t.Fatalf("request ID does not contain 128-bit base64url entropy: %q (%v)", requestID, err)
	}
	return requestID
}

func writeCandidateResponse(t *testing.T, conn *websocket.Conn, requestID string) {
	t.Helper()
	writeAdapterJSON(t, conn, map[string]any{
		"version": 1, "type": "snapshot.response", "request_id": requestID,
		"snapshot": map[string]any{
			"kind":        "candidate",
			"playlists":   []any{},
			"liked_songs": map[string]any{"entries": []any{}},
		},
	})
}

func assertSyncSuccess(t *testing.T, response ipc.Response) {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("sync error = %+v", response.Error)
	}
}

func decodeSyncResult(t *testing.T, response ipc.Response) ipc.SpotifySyncResult {
	t.Helper()
	raw, err := ipc.Encode(response.Result)
	if err != nil {
		t.Fatalf("encode sync result: %v", err)
	}
	var result ipc.SpotifySyncResult
	if err := ipc.Decode(raw, &result); err != nil {
		t.Fatalf("decode sync result: %v", err)
	}
	return result
}

func assertSyncFailure(t *testing.T, response ipc.Response, message string) {
	t.Helper()
	if response.Error == nil || response.Error.Code != ipc.CodeFailedPrecondition || !strings.Contains(response.Error.Message, message) {
		t.Fatalf("sync response = %+v, want failed_precondition containing %q", response, message)
	}
}

func assertAdapterClosed(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Fatalf("adapter close status = %v, want %v", websocket.CloseStatus(err), websocket.StatusGoingAway)
	}
}
