package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"github.com/Iyed-M/offbeat/internal/app"
	"github.com/Iyed-M/offbeat/internal/config"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

const (
	controlConnectTimeout  = 2 * time.Second
	controlReadTimeout     = 5 * time.Second
	spotifySyncReadTimeout = 5*time.Minute + 15*time.Second
)

// runStatus connects to the daemon over its control socket, requests the
// status payload, and renders it as human-readable text.
//
// Exit codes:
//   - 0 on success
//   - 1 when the daemon is unreachable or returns a runtime error
//   - 2 on CLI usage errors (handled by main)
func runStatus(configPath, homeDir string) int {
	bootstrap, err := loadBootstrapConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat status: config: %v\n", err)
		return 1
	}

	sockPath := app.SocketPath(bootstrap.SocketDir)

	resp, err := requestControl(sockPath, "status")
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat status: daemon-unavailable: %v\n", err)
		return 1
	}
	if resp.Error != nil {
		fmt.Fprintf(os.Stderr, "offbeat status: daemon error: %s: %s\n",
			resp.Error.Code, resp.Error.Message)
		return 1
	}

	raw, err := json.Marshal(resp.Result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeat status: unexpected daemon reply: %v\n", err)
		return 1
	}
	var status ipc.StatusResult
	if err := json.Unmarshal(raw, &status); err != nil {
		fmt.Fprintf(os.Stderr, "offbeat status: unexpected daemon reply: %v\n", err)
		return 1
	}

	printStatus(os.Stdout, status, sockPath)
	return 0
}

// requestControl opens the Unix socket, writes one request for the named
// command, reads one response, and closes the connection. It does not
// retry on failure (ADR 0007).
//
// Failures before the request bytes are fully written on the wire are
// reported as daemon-unavailable. Failures after the request was sent on
// the wire are reported as unknown-outcome because the daemon may have
// received and acted on the request before the connection dropped.
func requestControl(sockPath, command string) (ipc.Response, error) {
	return requestControlWithTimeout(sockPath, command, controlReadTimeout)
}

func requestControlWithTimeout(sockPath, command string, readTimeout time.Duration) (ipc.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), controlConnectTimeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", sockPath)
	if err != nil {
		return ipc.Response{}, fmt.Errorf("connect %s: %w", sockPath, err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(readTimeout)); err != nil {
		return ipc.Response{}, fmt.Errorf("set deadline: %w", err)
	}

	req := ipc.Request{Version: ipc.ProtocolVersion, Command: command}
	data, err := ipc.Encode(req)
	if err != nil {
		return ipc.Response{}, fmt.Errorf("encode request: %w", err)
	}
	full := append(data, ipc.FrameNewline)
	if _, err := conn.Write(full); err != nil {
		// We do not know whether the daemon received and acted on the
		// request bytes before the connection broke.
		return ipc.Response{}, fmt.Errorf("write request: %w (unknown outcome)", err)
	}

	reader := bufio.NewReader(conn)
	frame, err := ipc.ReadFrame(reader)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return ipc.Response{}, fmt.Errorf("connection closed before response (unknown outcome)")
		}
		return ipc.Response{}, fmt.Errorf("read response: %w (unknown outcome)", err)
	}

	var resp ipc.Response
	if err := ipc.Decode(frame, &resp); err != nil {
		return ipc.Response{}, fmt.Errorf("decode response: %w (unknown outcome)", err)
	}
	return resp, nil
}

// printStatus renders the StatusResult as human-readable text. The CLI
// does not print JSON: the daemon-served payload is for tools, not for
// people.
func printStatus(w io.Writer, s ipc.StatusResult, sockPath string) {
	dbState := fmt.Sprintf("%s (schema version %d)",
		boolDBReady(s.DBReady), s.SchemaVersion)
	fmt.Fprintln(w, "Offbeat daemon")
	fmt.Fprintf(w, "  version       : %s\n", s.DaemonVersion)
	fmt.Fprintf(w, "  pid           : %d\n", s.PID)
	fmt.Fprintf(w, "  started_at    : %s\n", s.StartedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(w, "  database      : %s\n", dbState)
	fmt.Fprintf(w, "  Spotify adapter: %s\n", adapterState(s.AdapterConnected))
	fmt.Fprintf(w, "  socket        : %s\n", sockPath)
}

func boolDBReady(ok bool) string {
	if ok {
		return "ready"
	}
	return "not ready"
}

func adapterState(connected bool) string {
	if connected {
		return "connected"
	}
	return "disconnected"
}

func loadBootstrapConfig(configPath, homeDir string) (config.Bootstrap, error) {
	loader := config.NewLoader(homeDir, configPath)
	bootstrap, err := loader.LoadBootstrap()
	if err != nil {
		def := configPath
		if def == "" {
			def, _ = config.DefaultConfigPath()
		}
		if def != "" {
			return bootstrap, fmt.Errorf("%w (using config %s)", err, def)
		}
		return bootstrap, err
	}
	return bootstrap, nil
}
