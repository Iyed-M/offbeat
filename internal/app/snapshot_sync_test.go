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

func TestSpotifySyncCompletesOnlyForMatchingSyntheticResponse(t *testing.T) {
	d := startAdapterDaemon(t)
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))

	done := sendSync(t, d)
	request := readAdapterMessage(t, adapter)
	requestID := assertSnapshotRequest(t, request)
	writeSyntheticResponse(t, adapter, requestID)
	assertSyncSuccess(t, syncResponse(t, <-done))
}

func TestSpotifySyncRejectsConcurrentRequest(t *testing.T) {
	d := startAdapterDaemon(t)
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))

	done := sendSync(t, d)
	requestID := assertSnapshotRequest(t, readAdapterMessage(t, adapter))
	response := sendRaw(t, SocketPath(d.socketDir), []byte(`{"version":1,"command":"spotify.sync"}`+"\n"))
	assertSyncFailure(t, response, "already in progress")
	writeSyntheticResponse(t, adapter, requestID)
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
	adapter := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))

	done := sendSync(t, d)
	_ = readAdapterMessage(t, adapter)
	assertSyncFailure(t, syncResponse(t, <-done), "Timed out waiting")
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
				writeSyntheticResponse(t, conn, "wrong-request-id")
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
		writeSyntheticResponse(t, adapter, requestID)
		assertSyncSuccess(t, syncResponse(t, <-done))
		writeSyntheticResponse(t, adapter, requestID)
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
	writeSyntheticResponse(t, old, requestID)
	assertAdapterMessage(t, old, "error", adapterErrorInvalidMessage)
	writeSyntheticResponse(t, current, requestID)
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

func writeSyntheticResponse(t *testing.T, conn *websocket.Conn, requestID string) {
	t.Helper()
	writeAdapterJSON(t, conn, map[string]any{
		"version": 1, "type": "snapshot.response", "request_id": requestID,
		"snapshot": map[string]any{"kind": "synthetic", "marker": "offbeat-m2"},
	})
}

func assertSyncSuccess(t *testing.T, response ipc.Response) {
	t.Helper()
	if response.Error != nil {
		t.Fatalf("sync error = %+v", response.Error)
	}
}

func assertSyncFailure(t *testing.T, response ipc.Response, message string) {
	t.Helper()
	if response.Error == nil || response.Error.Code != ipc.CodeFailedPrecondition || !strings.Contains(response.Error.Message, message) {
		t.Fatalf("sync response = %+v, want failed_precondition containing %q", response, message)
	}
}
