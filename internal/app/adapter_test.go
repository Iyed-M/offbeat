package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/ipc"
	"github.com/coder/websocket"
)

func TestAdapterAuthenticationSessionAndStatus(t *testing.T) {
	d := startAdapterDaemon(t)
	waitForSocket(t, SocketPath(d.socketDir))
	endpoint := AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port)

	assertAdapterConnected(t, d, false)
	first := dialAdapter(t, endpoint)
	writeAdapterJSON(t, first, map[string]any{"version": 1, "type": "hello", "credential": testAdapterCredential})
	assertAdapterMessage(t, first, "hello.accepted", "")
	// The session is published after hello.accepted is written, so wait for
	// that asynchronous state transition rather than assuming both are atomic.
	waitForAdapterConnected(t, d, true)

	second := dialAdapter(t, endpoint)
	writeAdapterJSON(t, second, map[string]any{"version": 1, "type": "hello", "credential": testAdapterCredential})
	assertAdapterMessage(t, second, "error", adapterErrorSessionConflict)
	assertAdapterConnected(t, d, true)

	if err := first.Close(websocket.StatusNormalClosure, "test complete"); err != nil {
		t.Fatalf("close active adapter: %v", err)
	}
	waitForAdapterConnected(t, d, false)
}

func TestAdapterRejectsInvalidAuthenticationAndProtocolTraffic(t *testing.T) {
	d := startAdapterDaemon(t)
	waitForSocket(t, SocketPath(d.socketDir))
	endpoint := AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port)

	for _, tc := range []struct {
		name    string
		payload any
		code    string
	}{
		{"bad credential", map[string]any{"version": 1, "type": "hello", "credential": "wrong"}, adapterErrorAuthenticationFailed},
		{"unknown first type", map[string]any{"version": 1, "type": "snapshot.response"}, adapterErrorInvalidMessage},
		{"unsupported version", map[string]any{"version": 2, "type": "hello", "credential": testAdapterCredential}, adapterErrorUnsupportedVersion},
		{"unknown field", map[string]any{"version": 1, "type": "hello", "credential": testAdapterCredential, "extra": true}, adapterErrorInvalidMessage},
		{"malformed JSON", []byte(`{"version":`), adapterErrorInvalidMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := dialAdapter(t, endpoint)
			if raw, ok := tc.payload.([]byte); ok {
				if err := conn.Write(context.Background(), websocket.MessageText, raw); err != nil {
					t.Fatalf("write: %v", err)
				}
			} else {
				writeAdapterJSON(t, conn, tc.payload)
			}
			message := readAdapterMessage(t, conn)
			if message["type"] != "error" || message["code"] != tc.code {
				t.Fatalf("error = %#v, want code %q", message, tc.code)
			}
			if strings.Contains(fmt.Sprint(message), testAdapterCredential) {
				t.Fatal("protocol error reflected credential")
			}
			waitForAdapterConnected(t, d, false)
		})
	}
}

func TestAdapterRejectsBinaryHello(t *testing.T) {
	d := startAdapterDaemon(t)
	waitForSocket(t, SocketPath(d.socketDir))
	conn := dialAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte(`{"version":1,"type":"hello","credential":"test-adapter-credential"}`)); err != nil {
		t.Fatalf("write binary hello: %v", err)
	}
	assertAdapterMessage(t, conn, "error", adapterErrorInvalidMessage)
	assertAdapterConnected(t, d, false)
}

func TestAdapterRequiresHelloWithinFiveSeconds(t *testing.T) {
	d := startAdapterDaemon(t)
	waitForSocket(t, SocketPath(d.socketDir))
	conn := dialAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("unauthenticated adapter received a message instead of timing out")
	}
	assertAdapterConnected(t, d, false)
}

func TestAdapterLivenessKeepsResponsiveSessionConnected(t *testing.T) {
	d := startAdapterDaemon(t)
	d.livenessInterval = 5 * time.Millisecond
	d.livenessWindow = 50 * time.Millisecond
	conn := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = conn.Close(websocket.StatusNormalClosure, "test complete")
		<-readDone
	})

	time.Sleep(3 * d.livenessWindow)
	assertAdapterConnected(t, d, true)
}

func TestAdapterLivenessExpiresUnresponsiveSession(t *testing.T) {
	d := startAdapterDaemon(t)
	d.livenessInterval = 5 * time.Millisecond
	d.livenessWindow = 50 * time.Millisecond
	conn := authenticateAdapter(t, AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port))

	waitForAdapterConnected(t, d, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("unresponsive adapter was not closed")
	}
}

func TestAdapterRejectsPostAuthenticationAndOversizedMessages(t *testing.T) {
	d := startAdapterDaemon(t)
	waitForSocket(t, SocketPath(d.socketDir))
	endpoint := AdapterEndpoint(d.Cfg.SpotifyAdapter.BindAddress, d.Cfg.SpotifyAdapter.Port)

	for _, tc := range []struct {
		name    string
		payload []byte
		code    string
	}{
		{"unknown type", []byte(`{"version":1,"type":"unknown"}`), adapterErrorInvalidMessage},
		{"unsupported version", []byte(`{"version":2,"type":"unknown"}`), adapterErrorUnsupportedVersion},
		{"oversized", append([]byte(`{"version":1,"type":"`), append(make([]byte, adapterMaxMessageBytes), []byte(`"}`)...)...), adapterErrorInvalidMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := authenticateAdapter(t, endpoint)
			if err := conn.Write(context.Background(), websocket.MessageText, tc.payload); err != nil {
				t.Fatalf("write: %v", err)
			}
			assertAdapterMessage(t, conn, "error", tc.code)
			waitForAdapterConnected(t, d, false)
		})
	}
}

func authenticateAdapter(t *testing.T, endpoint string) *websocket.Conn {
	t.Helper()
	conn := dialAdapter(t, endpoint)
	writeAdapterJSON(t, conn, map[string]any{"version": 1, "type": "hello", "credential": testAdapterCredential})
	assertAdapterMessage(t, conn, "hello.accepted", "")
	return conn
}

func startAdapterDaemon(t *testing.T) *Daemon {
	t.Helper()
	d, signals, done := startAdapterDaemonWithoutCleanup(t)
	t.Cleanup(func() {
		signals <- os.Interrupt
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("daemon Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
		_ = d.Close()
	})
	return d
}

func startAdapterDaemonWithoutCleanup(t *testing.T) (*Daemon, chan os.Signal, <-chan error) {
	t.Helper()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve adapter port: %v", err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	if err := reserved.Close(); err != nil {
		t.Fatalf("release adapter port: %v", err)
	}
	dir, err := os.MkdirTemp("/tmp", "offbeat-adapter-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("[spotify_adapter]\nport = %d\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := NewDaemon(context.Background(), Options{HomeDir: dir, ConfigPath: configPath, Version: "0.0.0-m2", AdapterCredential: testAdapterCredential})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	done := make(chan error, 1)
	signals := make(chan os.Signal, 1)
	go func() { done <- d.Run(context.Background(), RunOptions{SignalCh: signals}) }()
	waitForSocket(t, SocketPath(d.socketDir))
	return d, signals, done
}

func dialAdapter(t *testing.T, endpoint string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		t.Fatalf("dial adapter: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test cleanup") })
	return conn
}

func writeAdapterJSON(t *testing.T, conn *websocket.Conn, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := conn.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readAdapterMessage(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read adapter response: %v", err)
	}
	var message map[string]any
	if err := json.Unmarshal(data, &message); err != nil {
		t.Fatalf("decode adapter response: %v", err)
	}
	return message
}

func assertAdapterMessage(t *testing.T, conn *websocket.Conn, messageType, code string) {
	t.Helper()
	message := readAdapterMessage(t, conn)
	if message["version"] != float64(adapterProtocolVersion) || message["type"] != messageType || (code != "" && message["code"] != code) {
		t.Fatalf("adapter response = %#v", message)
	}
}

func assertAdapterConnected(t *testing.T, d *Daemon, want bool) {
	t.Helper()
	response := sendRaw(t, SocketPath(d.socketDir), []byte(`{"version":1,"command":"status"}`+"\n"))
	if response.Error != nil {
		t.Fatalf("status error: %+v", response.Error)
	}
	raw, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var status ipc.StatusResult
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if status.AdapterConnected != want {
		t.Fatalf("adapter connected = %t, want %t", status.AdapterConnected, want)
	}
}

func waitForAdapterConnected(t *testing.T, d *Daemon, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if d.adapterConnected() == want {
			assertAdapterConnected(t, d, want)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("adapter did not become connected=%t", want)
}
