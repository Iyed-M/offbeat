package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// These tests are black-box: they build the real offbeatd and offbeat
// binaries and exercise them via exec.Command. TestMain builds the
// binaries once and exposes them as package-level paths.

var (
	offbeatdPath string
	offbeatPath  string
)

func TestMain(m *testing.M) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		_, _ = os.Stderr.WriteString("findRepoRoot: " + err.Error() + "\n")
		os.Exit(2)
	}

	dir, err := os.MkdirTemp("", "offbeat-cli-test-")
	if err != nil {
		_, _ = os.Stderr.WriteString("MkdirTemp: " + err.Error() + "\n")
		os.Exit(2)
	}
	defer os.RemoveAll(dir)

	offbeatdPath = filepath.Join(dir, "offbeatd")
	offbeatPath = filepath.Join(dir, "offbeat")

	if err := buildBinary(repoRoot, "./cmd/offbeatd", offbeatdPath); err != nil {
		_, _ = os.Stderr.WriteString("build offbeatd: " + err.Error() + "\n")
		os.Exit(2)
	}
	if err := buildBinary(repoRoot, "./cmd/offbeat", offbeatPath); err != nil {
		_, _ = os.Stderr.WriteString("build offbeat: " + err.Error() + "\n")
		os.Exit(2)
	}

	code := m.Run()
	os.Exit(code)
}

func TestCLIStatusAgainstRunningDaemon(t *testing.T) {
	home := t.TempDir()
	writeCLIAdapterConfig(t, home)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, offbeatdPath)
	cmd.Env = append(os.Environ(), "OFFBEAT_HOME="+home, "OFFBEAT_ADAPTER_CREDENTIAL=test-adapter-credential")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start offbeatd: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	}()

	if err := waitForDaemonReady(home, 5*time.Second); err != nil {
		t.Fatalf("daemon not ready: %v\nstderr:\n%s", err, stderr.String())
	}

	out, errOut, err := runCLI(t, home, "status")
	if err != nil {
		t.Fatalf("offbeat status: %v\nstderr:\n%s", err, errOut)
	}

	for _, want := range []string{
		"Offbeat daemon",
		"version       : 0.0.0-m1",
		"pid           :",
		"started_at    :",
		"database      : ready",
		"schema version 5",
		"Spotify adapter: disconnected",
		"socket        :",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\noutput:\n%s", want, out)
		}
	}
	if errOut != "" {
		// stderr should be empty on success
		t.Errorf("unexpected stderr: %q", errOut)
	}
}

func TestCLIStatusUsesBootstrapConfigWhenDaemonConfigBecomesInvalid(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "offbeat-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	configPath := writeCLIAdapterConfig(t, home)

	cmd := exec.Command(offbeatdPath)
	cmd.Env = append(os.Environ(), "OFFBEAT_HOME="+home, "OFFBEAT_ADAPTER_CREDENTIAL=test-adapter-credential")
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start offbeatd: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	}()
	if err := waitForDaemonReady(home, 5*time.Second); err != nil {
		t.Fatalf("daemon not ready: %v\nstderr:\n%s", err, stderr.String())
	}

	if err := os.WriteFile(configPath, []byte("[spotify_adapter]\nport = 0\n"), 0o600); err != nil {
		t.Fatalf("invalidate daemon-only config: %v", err)
	}

	out, errOut, err := runCLI(t, home, "status")
	if err != nil {
		t.Fatalf("offbeat status: %v\nstderr:\n%s", err, errOut)
	}
	if !strings.Contains(out, "Offbeat daemon") {
		t.Errorf("status did not reach daemon:\n%s", out)
	}
	if errOut != "" {
		t.Errorf("unexpected stderr: %q", errOut)
	}
}

func writeCLIAdapterConfig(t *testing.T, home string) string {
	t.Helper()
	port := reserveCLIAdapterPort(t)
	configPath := filepath.Join(home, ".config", "offbeat", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("[spotify_adapter]\nport = %d\n", port)), 0o600); err != nil {
		t.Fatalf("write adapter config: %v", err)
	}
	return configPath
}

func reserveCLIAdapterPort(t *testing.T) int {
	t.Helper()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve adapter port: %v", err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	if err := reserved.Close(); err != nil {
		t.Fatalf("release adapter port: %v", err)
	}
	return port
}

func TestCLISpotifySyncPrintsCandidateSuccess(t *testing.T) {
	home := t.TempDir()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve adapter port: %v", err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	if err := reserved.Close(); err != nil {
		t.Fatalf("release adapter port: %v", err)
	}
	configPath := filepath.Join(home, ".config", "offbeat", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf("[spotify_adapter]\nport = %d\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(offbeatdPath)
	cmd.Env = append(os.Environ(), "OFFBEAT_HOME="+home, "OFFBEAT_ADAPTER_CREDENTIAL=test-adapter-credential")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start offbeatd: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	}()
	if err := waitForDaemonReady(home, 5*time.Second); err != nil {
		t.Fatalf("daemon not ready: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	adapter, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://127.0.0.1:%d/v1/adapter", port), nil)
	if err != nil {
		t.Fatalf("dial adapter: %v", err)
	}
	defer adapter.Close(websocket.StatusNormalClosure, "test complete")
	writeCLIAdapterJSON(t, adapter, map[string]any{"version": 1, "type": "hello", "credential": "test-adapter-credential"})
	if message := readCLIAdapterJSON(t, adapter); message["type"] != "hello.accepted" {
		t.Fatalf("hello response = %#v", message)
	}

	type cliResult struct {
		out, errOut string
		err         error
	}
	done := make(chan cliResult, 1)
	go func() {
		out, errOut, err := runCLI(t, home, "spotify", "sync")
		done <- cliResult{out, errOut, err}
	}()
	request := readCLIAdapterJSON(t, adapter)
	requestID, ok := request["request_id"].(string)
	if !ok || request["type"] != "snapshot.request" {
		t.Fatalf("snapshot request = %#v", request)
	}
	writeCLIAdapterJSON(t, adapter, map[string]any{
		"version": 1, "type": "snapshot.response", "request_id": requestID,
		"snapshot": map[string]any{"kind": "candidate", "playlists": []any{}, "liked_songs": map[string]any{"entries": []any{}}},
	})
	result := <-done
	if result.err != nil || result.errOut != "" || result.out != "Spotify desired state committed (revision 1): 0 playlists, 0 playlist entries, 0 Liked Songs entries, 0 supported entries, 0 unsupported entries.\n" {
		t.Fatalf("offbeat spotify sync = stdout %q stderr %q err %v", result.out, result.errOut, result.err)
	}

	done = make(chan cliResult, 1)
	go func() {
		out, errOut, err := runCLI(t, home, "spotify", "sync")
		done <- cliResult{out, errOut, err}
	}()
	request = readCLIAdapterJSON(t, adapter)
	requestID, ok = request["request_id"].(string)
	if !ok || request["type"] != "snapshot.request" {
		t.Fatalf("second snapshot request = %#v", request)
	}
	writeCLIAdapterJSON(t, adapter, map[string]any{
		"version": 1, "type": "snapshot.response", "request_id": requestID,
		"snapshot": map[string]any{"kind": "candidate", "playlists": []any{}, "liked_songs": map[string]any{"entries": []any{}}},
	})
	result = <-done
	if result.err != nil || result.errOut != "" || result.out != "Spotify desired state unchanged (revision 1): 0 playlists, 0 playlist entries, 0 Liked Songs entries, 0 supported entries, 0 unsupported entries.\n" {
		t.Fatalf("equivalent offbeat spotify sync = stdout %q stderr %q err %v", result.out, result.errOut, result.err)
	}
}

func TestCLISpotifySyncWaitBudgetExceedsM3SnapshotTimeout(t *testing.T) {
	if spotifySyncReadTimeout <= 5*time.Minute {
		t.Fatalf("Spotify sync read timeout = %s, want more than 5m", spotifySyncReadTimeout)
	}
}

func writeCLIAdapterJSON(t *testing.T, conn *websocket.Conn, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatalf("write adapter message: %v", err)
	}
}

func readCLIAdapterJSON(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read adapter message: %v", err)
	}
	var message map[string]any
	if err := json.Unmarshal(data, &message); err != nil {
		t.Fatalf("decode adapter message: %v", err)
	}
	return message
}

func TestCLIStatusReportsDaemonUnavailableWhenNoDaemon(t *testing.T) {
	home := t.TempDir()

	out, errOut, err := runCLI(t, home, "status")
	if err == nil {
		t.Fatalf("expected error exit, got success\nstdout:\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code=%d want 1", exitErr.ExitCode())
	}
	if !strings.Contains(errOut, "daemon-unavailable") {
		t.Errorf("stderr missing 'daemon-unavailable': %q", errOut)
	}
	if out != "" {
		t.Errorf("expected empty stdout on failure, got: %q", out)
	}
}

func TestCLIStatusUnknownCommandExitsTwo(t *testing.T) {
	_, errOut, err := runCLI(t, t.TempDir(), "bogus")
	if err == nil {
		t.Fatal("expected error exit, got success")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code=%d want 2", exitErr.ExitCode())
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Errorf("stderr missing 'unknown command': %q", errOut)
	}
}

func TestCLIStatusRejectsExtraPositionalArgs(t *testing.T) {
	home := t.TempDir()

	out, errOut, err := runCLI(t, home, "status", "extra")
	if err == nil {
		t.Fatalf("expected error exit, got success\nstdout:\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code=%d want 2", exitErr.ExitCode())
	}
	if !strings.Contains(errOut, "usage") {
		t.Errorf("stderr missing usage hint: %q", errOut)
	}
}

func TestCLIAcquireRejectsInvalidUsage(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing arguments", args: []string{"acquire"}},
		{name: "too many arguments", args: []string{"acquire", "spotify:track:one", "https://media.example.test/one.mp3", "extra"}},
		{name: "invalid track URI", args: []string{"acquire", "spotify:playlist:one", "https://media.example.test/one.mp3"}},
		{name: "invalid source", args: []string{"acquire", "spotify:track:one", "search terms"}},
		{name: "status id is invalid", args: []string{"acquire", "status", "zero"}},
		{name: "retry id is invalid", args: []string{"acquire", "retry", "0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errOut, err := runCLI(t, t.TempDir(), tt.args[0], tt.args[1:]...)
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
				t.Fatalf("exit = %v, want code 2; stdout=%q stderr=%q", err, out, errOut)
			}
			if out != "" || errOut == "" {
				t.Fatalf("stdout=%q stderr=%q", out, errOut)
			}
		})
	}
}

func TestCLIConfigServesDaemonEffectiveConfig(t *testing.T) {
	home := t.TempDir()
	port := reserveCLIAdapterPort(t)
	cfgPath := filepath.Join(home, ".config", "offbeat", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(`
[acquisition]
concurrency = 4

[downloader]
yt_dlp_path = "/usr/local/bin/yt-dlp"

[spotify_adapter]
port = %d
`, port)), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, offbeatdPath)
	cmd.Env = append(os.Environ(), "OFFBEAT_HOME="+home, "OFFBEAT_ADAPTER_CREDENTIAL=test-adapter-credential")
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start offbeatd: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	}()

	if err := waitForDaemonReady(home, 5*time.Second); err != nil {
		t.Fatalf("daemon not ready: %v\nstderr:\n%s", err, stderr.String())
	}

	out, errOut, err := runCLI(t, home, "config")
	if err != nil {
		t.Fatalf("offbeat config: %v\nstderr:\n%s", err, errOut)
	}

	for _, want := range []string{
		"Offbeat configuration",
		"paths:",
		"music_root",
		"socket_dir",
		"acquisition:",
		"concurrency        : 4",
		"downloader:",
		"yt_dlp_path  : /usr/local/bin/yt-dlp",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\noutput:\n%s", want, out)
		}
	}
	if errOut != "" {
		t.Errorf("unexpected stderr: %q", errOut)
	}
}

// TestCLIConfigReflectsDaemonWhenLocalFileChanges is the acceptance
// criterion: even after the local config file mutates underneath us,
// `offbeat config` must report the daemon's effective configuration
// (the value loaded at startup), not the freshly-written file.
func TestCLIConfigReflectsDaemonWhenLocalFileChanges(t *testing.T) {
	home := t.TempDir()
	port := reserveCLIAdapterPort(t)
	cfgPath := filepath.Join(home, ".config", "offbeat", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(`
[acquisition]
concurrency = 4

[downloader]
yt_dlp_path = "/original/yt-dlp"

[spotify_adapter]
port = %d
`, port)), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, offbeatdPath)
	cmd.Env = append(os.Environ(), "OFFBEAT_HOME="+home, "OFFBEAT_ADAPTER_CREDENTIAL=test-adapter-credential")
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start offbeatd: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
	}()

	if err := waitForDaemonReady(home, 5*time.Second); err != nil {
		t.Fatalf("daemon not ready: %v\nstderr:\n%s", err, stderr.String())
	}

	// Mutate the file AFTER the daemon started. The socket_dir is left
	// untouched so the CLI's bootstrap-config read still resolves the
	// right socket.
	if err := os.WriteFile(cfgPath, []byte(`
[acquisition]
concurrency = 8

[downloader]
yt_dlp_path = "/mutated/yt-dlp"
`), 0o600); err != nil {
		t.Fatalf("mutate config: %v", err)
	}

	out, errOut, err := runCLI(t, home, "config")
	if err != nil {
		t.Fatalf("offbeat config: %v\nstderr:\n%s", err, errOut)
	}

	if !strings.Contains(out, "concurrency        : 4") {
		t.Errorf("expected daemon-served concurrency=4, got output:\n%s", out)
	}
	if !strings.Contains(out, "/original/yt-dlp") {
		t.Errorf("expected daemon-served yt-dlp path, got output:\n%s", out)
	}
	if strings.Contains(out, "concurrency        : 8") {
		t.Errorf("config leaked mutated concurrency=8:\n%s", out)
	}
	if strings.Contains(out, "/mutated/yt-dlp") {
		t.Errorf("config leaked mutated yt-dlp path:\n%s", out)
	}
	if errOut != "" {
		t.Errorf("unexpected stderr: %q", errOut)
	}
}

func TestCLIConfigReportsDaemonUnavailableWhenNoDaemon(t *testing.T) {
	home := t.TempDir()

	out, errOut, err := runCLI(t, home, "config")
	if err == nil {
		t.Fatalf("expected error exit, got success\nstdout:\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code=%d want 1", exitErr.ExitCode())
	}
	if !strings.Contains(errOut, "daemon-unavailable") {
		t.Errorf("stderr missing 'daemon-unavailable': %q", errOut)
	}
	// The error must mention config so the user can act on it.
	if !strings.Contains(errOut, "offbeat config") {
		t.Errorf("stderr missing 'offbeat config' prefix: %q", errOut)
	}
	// It must not silently fall back to a local file render: stdout
	// should be empty so the user is not misled into believing the
	// daemon is the source.
	if out != "" {
		t.Errorf("expected empty stdout on failure, got: %q", out)
	}
}

func TestCLIConfigRejectsExtraPositionalArgs(t *testing.T) {
	home := t.TempDir()

	out, errOut, err := runCLI(t, home, "config", "extra")
	if err == nil {
		t.Fatalf("expected error exit, got success\nstdout:\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	if exitErr.ExitCode() != 2 {
		t.Errorf("exit code=%d want 2", exitErr.ExitCode())
	}
	if !strings.Contains(errOut, "usage") {
		t.Errorf("stderr missing usage hint: %q", errOut)
	}
}

func TestDecodeConfigResultRejectsInvalidDaemonReply(t *testing.T) {
	for _, result := range []any{
		nil,
		map[string]any{},
		map[string]any{"unknown": "field"},
	} {
		if _, err := decodeConfigResult(result); err == nil {
			t.Errorf("decodeConfigResult(%#v) succeeded", result)
		}
	}
}

func runCLI(t *testing.T, home, subcommand string, extraArgs ...string) (string, string, error) {
	t.Helper()
	args := append([]string{"-home", home, subcommand}, extraArgs...)
	cmd := exec.Command(offbeatPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func waitForDaemonReady(home string, timeout time.Duration) error {
	sockPath := filepath.Join(home, ".local", "state", "offbeat", "ipc", "offbeatd.sock")
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("socket never appeared at " + sockPath)
}

func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("go.mod not found from " + wd)
}

func buildBinary(repoRoot, pkg, out string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return errors.New("go build " + pkg + ": " + err.Error() + "\n" + stderr.String())
	}
	return nil
}
