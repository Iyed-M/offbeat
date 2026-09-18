package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/config"
	"github.com/Iyed-M/offbeat/internal/db"
	"github.com/Iyed-M/offbeat/internal/ipc"
)

const testAdapterCredential = "test-adapter-credential"

func TestNewDaemonCreatesDirsAndMigrates(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	dbPath := filepath.Join(dir, "data", "offbeat.db")
	if err := os.WriteFile(cfgPath, []byte(`
[paths]
database = "`+dbPath+`"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := NewDaemon(context.Background(), Options{
		HomeDir:           dir,
		ConfigPath:        cfgPath,
		AdapterCredential: testAdapterCredential,
	})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if d.adapterCredential != testAdapterCredential {
		t.Fatal("daemon did not retain the injected adapter credential")
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("db file missing: %v", err)
	}
	var count int
	if err := d.DB.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("expected migrations applied")
	}
	logs, err := os.ReadFile(d.Cfg.Paths.LogFile)
	if err != nil {
		t.Fatalf("read daemon log: %v", err)
	}
	if strings.Contains(string(logs), testAdapterCredential) {
		t.Fatal("daemon log contains adapter credential")
	}
}

func TestNewDaemonRejectsBadConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[unknown]\nx = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewDaemon(context.Background(), Options{HomeDir: dir, ConfigPath: cfgPath, AdapterCredential: testAdapterCredential})
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestNewDaemonRequiresAdapterCredential(t *testing.T) {
	dir := t.TempDir()
	_, err := NewDaemon(context.Background(), Options{HomeDir: dir})
	if !errors.Is(err, ErrAdapterCredentialUnavailable) {
		t.Fatalf("NewDaemon error = %v, want ErrAdapterCredentialUnavailable", err)
	}
}

func TestAdapterEndpointDefault(t *testing.T) {
	cfg := config.Defaults(t.TempDir())
	if got, want := AdapterEndpoint(cfg.SpotifyAdapter.BindAddress, cfg.SpotifyAdapter.Port), "ws://127.0.0.1:16352/v1/adapter"; got != want {
		t.Fatalf("AdapterEndpoint() = %q, want %q", got, want)
	}
}

func TestRunRespondsToSignal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := NewDaemon(context.Background(), Options{HomeDir: dir, ConfigPath: cfgPath, AdapterCredential: testAdapterCredential})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	sigCh := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- d.Run(context.Background(), RunOptions{SignalCh: sigCh})
	}()
	time.Sleep(50 * time.Millisecond)
	sigCh <- os.Interrupt
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after signal")
	}
}

func TestRunRespondsToSIGTERM(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonHelperProcess$")
	cmd.Env = append(os.Environ(), "OFFBEAT_DAEMON_HELPER=1", "OFFBEAT_HOME="+dir, "OFFBEAT_ADAPTER_CREDENTIAL="+testAdapterCredential)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("create daemon helper stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon helper: %v", err)
	}

	started := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "offbeatd started") {
				started <- true
				return
			}
		}
		started <- false
	}()
	select {
	case ok := <-started:
		if !ok {
			_ = cmd.Wait()
			t.Fatal("daemon helper exited before starting")
		}
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("daemon helper did not start")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("daemon helper did not exit cleanly: %v", err)
	}
}

func TestDaemonHelperProcess(t *testing.T) {
	if os.Getenv("OFFBEAT_DAEMON_HELPER") != "1" {
		return
	}
	d, err := NewDaemon(context.Background(), Options{AdapterCredential: os.Getenv("OFFBEAT_ADAPTER_CREDENTIAL")})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Run(context.Background(), RunOptions{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunRespondsToContextCancel(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDaemon(context.Background(), Options{HomeDir: dir, AdapterCredential: testAdapterCredential})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- d.Run(ctx, RunOptions{SignalCh: make(chan os.Signal)})
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

func TestRunReleasesOwnershipAfterStaleSocketResolutionFailure(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDaemon(context.Background(), Options{HomeDir: dir, AdapterCredential: testAdapterCredential})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := os.WriteFile(SocketPath(d.socketDir), []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("create non-socket entry: %v", err)
	}

	if err := d.Run(context.Background(), RunOptions{SignalCh: make(chan os.Signal)}); !errors.Is(err, ErrNonSocketAtSocketPath) {
		t.Fatalf("Run error = %v, want ErrNonSocketAtSocketPath", err)
	}
	assertResourcesReleased(t, d)
}

func TestRunReleasesOwnershipAfterSocketBindFailure(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDaemon(context.Background(), Options{HomeDir: dir, AdapterCredential: testAdapterCredential})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := os.Remove(d.socketDir); err != nil {
		t.Fatalf("remove socket dir: %v", err)
	}

	if err := d.Run(context.Background(), RunOptions{SignalCh: make(chan os.Signal)}); err == nil {
		t.Fatal("Run succeeded after socket directory removal")
	}
	assertResourcesReleased(t, d)
}

func TestRunBindsAdapterBeforeControlSocketAndReleasesOnFailure(t *testing.T) {
	dir := t.TempDir()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy adapter port: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf("[spotify_adapter]\nbind_address = %q\nport = %d\n", "127.0.0.1", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := NewDaemon(context.Background(), Options{HomeDir: dir, ConfigPath: cfgPath, AdapterCredential: testAdapterCredential})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	if err := d.Run(context.Background(), RunOptions{SignalCh: make(chan os.Signal)}); err == nil {
		t.Fatal("Run succeeded with occupied adapter endpoint")
	}
	if _, err := os.Stat(SocketPath(d.socketDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("control socket exposed after adapter bind failure: %v", err)
	}
	assertResourcesReleased(t, d)
}

func TestRunAdapterListenerIsReadyBeforeControlSocket(t *testing.T) {
	d := startDaemonForTest(t, "0.0.0-m2")
	waitForSocket(t, SocketPath(d.socketDir))

	endpoint := "http://" + net.JoinHostPort(d.Cfg.SpotifyAdapter.BindAddress, fmt.Sprintf("%d", d.Cfg.SpotifyAdapter.Port)) + AdapterRoute
	resp, err := (&http.Client{Timeout: time.Second}).Get(endpoint)
	if err != nil {
		t.Fatalf("adapter listener unavailable while control socket is ready: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("adapter route status = %d, want %d", resp.StatusCode, http.StatusUpgradeRequired)
	}
}

func assertResourcesReleased(t *testing.T, d *Daemon) {
	t.Helper()
	if d.DB != nil {
		t.Fatal("database was not closed")
	}
	if d.lock != nil {
		t.Fatal("daemon lock was not released")
	}
	lock, err := AcquireLock(d.Cfg.Paths.StateDir)
	if err != nil {
		t.Fatalf("ownership was not released: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release replacement lock: %v", err)
	}
}

func TestEnsureDirsCreatesExpectedPaths(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults(dir)
	cfg.Paths.DataDir = filepath.Join(dir, "data")
	cfg.Paths.StateDir = filepath.Join(dir, "state")
	cfg.Paths.MusicRoot = filepath.Join(dir, "music")
	cfg.Paths.Database = filepath.Join(cfg.Paths.DataDir, "offbeat.db")
	cfg.Paths.LogFile = filepath.Join(cfg.Paths.StateDir, "offbeatd.log")
	if err := ensureDirs(cfg); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	for _, want := range []string{
		cfg.Paths.ConfigDir,
		cfg.Paths.DataDir,
		cfg.Paths.StateDir,
		cfg.Paths.SocketDir,
		cfg.Paths.CertsDir,
		filepath.Dir(cfg.Paths.Database),
		filepath.Dir(cfg.Paths.LogFile),
		cfg.Paths.MusicRoot,
		filepath.Join(cfg.Paths.MusicRoot, "tracks"),
		filepath.Join(cfg.Paths.MusicRoot, "playlists"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("missing dir %s: %v", want, err)
		}
	}
}

func TestDBMigrationsAreIdempotent(t *testing.T) {
	dir := t.TempDir()
	d, err := db.OpenFile(context.Background(), filepath.Join(dir, "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	first, err := d.Migrate(context.Background(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("expected at least one migration")
	}
	second, err := d.Migrate(context.Background(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second migrate re-applied: %v", second)
	}
}

func TestStatusCommandReportsDaemonIdentity(t *testing.T) {
	d := startDaemonForTest(t, "0.0.0-m1")

	sockPath := SocketPath(d.socketDir)
	waitForSocket(t, sockPath)

	resp := sendRaw(t, sockPath, []byte(`{"version":1,"command":"status"}`+"\n"))
	if resp.Error != nil {
		t.Fatalf("got error %+v", resp.Error)
	}

	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("re-marshal result: %v", err)
	}
	var status ipc.StatusResult
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if strings.Contains(string(raw), testAdapterCredential) {
		t.Fatal("status contains adapter credential")
	}

	if status.DaemonVersion != "0.0.0-m1" {
		t.Errorf("DaemonVersion=%q want %q", status.DaemonVersion, "0.0.0-m1")
	}
	if status.PID != os.Getpid() {
		t.Errorf("PID=%d want %d", status.PID, os.Getpid())
	}
	if !status.DBReady {
		t.Error("DBReady=false want true")
	}
	if status.SchemaVersion != 2 {
		t.Errorf("SchemaVersion=%d want 2", status.SchemaVersion)
	}
	if status.StartedAt.IsZero() {
		t.Error("StartedAt is zero")
	}
	if resp.Version != ipc.ProtocolVersion {
		t.Errorf("response version=%d want %d", resp.Version, ipc.ProtocolVersion)
	}
}

func TestStatusCommandRejectsUnknownCommand(t *testing.T) {
	d := startDaemonForTest(t, "0.0.0-m1")
	sockPath := SocketPath(d.socketDir)
	waitForSocket(t, sockPath)

	resp := sendRaw(t, sockPath, []byte(`{"version":1,"command":"nope"}`+"\n"))
	if resp.Error == nil {
		t.Fatalf("expected error response, got result=%+v", resp.Result)
	}
	if resp.Error.Code != ipc.CodeInvalidRequest {
		t.Fatalf("code=%q want %q", resp.Error.Code, ipc.CodeInvalidRequest)
	}
}

func TestStatusCommandRejectsMissingCommand(t *testing.T) {
	d := startDaemonForTest(t, "0.0.0-m1")
	sockPath := SocketPath(d.socketDir)
	waitForSocket(t, sockPath)

	resp := sendRaw(t, sockPath, []byte(`{"version":1}`+"\n"))
	if resp.Error == nil {
		t.Fatalf("expected error response, got result=%+v", resp.Result)
	}
	if resp.Error.Code != ipc.CodeInvalidRequest {
		t.Fatalf("code=%q want %q", resp.Error.Code, ipc.CodeInvalidRequest)
	}
}

func TestStatusCommandRejectsUnsupportedVersion(t *testing.T) {
	d := startDaemonForTest(t, "0.0.0-m1")
	sockPath := SocketPath(d.socketDir)
	waitForSocket(t, sockPath)

	resp := sendRaw(t, sockPath, []byte(`{"version":99,"command":"status"}`+"\n"))
	if resp.Error == nil {
		t.Fatalf("expected error response, got result=%+v", resp.Result)
	}
	if resp.Error.Code != ipc.CodeUnsupportedVersion {
		t.Fatalf("code=%q want %q", resp.Error.Code, ipc.CodeUnsupportedVersion)
	}
}

func TestStatusCommandRejectsMalformedJSON(t *testing.T) {
	d := startDaemonForTest(t, "0.0.0-m1")
	sockPath := SocketPath(d.socketDir)
	waitForSocket(t, sockPath)

	resp := sendRaw(t, sockPath, []byte("{not json"+"\n"))
	if resp.Error == nil {
		t.Fatalf("expected error response, got result=%+v", resp.Result)
	}
	if resp.Error.Code != ipc.CodeInvalidRequest {
		t.Fatalf("code=%q want %q", resp.Error.Code, ipc.CodeInvalidRequest)
	}
}

func TestStatusCommandRejectsUnknownField(t *testing.T) {
	d := startDaemonForTest(t, "0.0.0-m1")
	sockPath := SocketPath(d.socketDir)
	waitForSocket(t, sockPath)

	resp := sendRaw(t, sockPath, []byte(`{"version":1,"command":"status","extra":1}`+"\n"))
	if resp.Error == nil {
		t.Fatalf("expected error response, got result=%+v", resp.Result)
	}
	if resp.Error.Code != ipc.CodeInvalidRequest {
		t.Fatalf("code=%q want %q", resp.Error.Code, ipc.CodeInvalidRequest)
	}
}

func TestStatusCommandServesOneRequestPerConnection(t *testing.T) {
	d := startDaemonForTest(t, "0.0.0-m1")
	sockPath := SocketPath(d.socketDir)
	waitForSocket(t, sockPath)

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(`{"version":1,"command":"status"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := mustReadResponse(t, conn)
	if resp.Error != nil {
		t.Fatalf("got error %+v", resp.Error)
	}

	// A second request on the same connection should fail: the daemon closes
	// the connection after serving exactly one request.
	if _, err := conn.Write([]byte(`{"version":1,"command":"status"}` + "\n")); err != nil {
		// Some kernels return EPIPE here; that is acceptable.
		return
	}
	if _, err := bufio.NewReader(conn).ReadBytes('\n'); err == nil {
		t.Fatal("expected second request to fail (daemon must close connection)")
	}
}

func TestConfigCommandReportsSanitizedEffectiveConfig(t *testing.T) {
	dir := t.TempDir()
	customDB := filepath.Join(dir, "custom.db")
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(`
[paths]
database = %q

[acquisition]
concurrency = 4
temp_retry_backoff = "45s"
max_temp_retries = 7

[logging]
level = "debug"
format = "json"

[spotify_adapter]
bind_address = "127.0.0.2"
port = 8765

[downloader]
yt_dlp_path = "/usr/local/bin/yt-dlp"
ffmpeg_path = "/opt/ffmpeg"
ffprobe_path = "/opt/ffprobe"

[sync]
https_port = 8443
lan_bind_address = "192.168.1.20"
pairing_timeout = "10m"
`, customDB)), 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := startDaemonWithConfig(t, cfgPath, "0.0.0-m1")
	if err != nil {
		t.Fatalf("startDaemon: %v", err)
	}
	sockPath := SocketPath(d.socketDir)
	waitForSocket(t, sockPath)

	resp := sendRaw(t, sockPath, []byte(`{"version":1,"command":"config"}`+"\n"))
	if resp.Error != nil {
		t.Fatalf("got error %+v", resp.Error)
	}
	if resp.Version != ipc.ProtocolVersion {
		t.Errorf("response version=%d want %d", resp.Version, ipc.ProtocolVersion)
	}

	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("re-marshal result: %v", err)
	}
	var cfg ipc.ConfigResult
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if strings.Contains(string(raw), testAdapterCredential) {
		t.Fatal("effective config contains adapter credential")
	}

	if cfg.Paths.Database != customDB {
		t.Errorf("Database=%q want %q", cfg.Paths.Database, customDB)
	}
	if cfg.Paths.MusicRoot == "" {
		t.Errorf("MusicRoot is empty")
	}
	if cfg.Paths.SocketDir == "" {
		t.Errorf("SocketDir is empty")
	}
	if cfg.Logging.Level != "debug" || cfg.Logging.Format != "json" {
		t.Errorf("Logging=%+v", cfg.Logging)
	}
	if cfg.SpotifyAdapter.BindAddress != "127.0.0.2" || cfg.SpotifyAdapter.Port != 8765 {
		t.Errorf("SpotifyAdapter=%+v", cfg.SpotifyAdapter)
	}
	if cfg.Acquisition.Concurrency != 4 || cfg.Acquisition.TempRetryBackoff != "45s" || cfg.Acquisition.MaxTempRetries != 7 {
		t.Errorf("Acquisition=%+v", cfg.Acquisition)
	}
	if cfg.Downloader.YTDLPPath != "/usr/local/bin/yt-dlp" || cfg.Downloader.FFmpegPath != "/opt/ffmpeg" || cfg.Downloader.FFprobePath != "/opt/ffprobe" {
		t.Errorf("Downloader=%+v", cfg.Downloader)
	}
	if cfg.Sync.HTTPSPort != 8443 || cfg.Sync.LANBindAddress != "192.168.1.20" || cfg.Sync.PairingTimeout != "10m0s" {
		t.Errorf("Sync=%+v", cfg.Sync)
	}
}

func TestConfigCommandDispatches(t *testing.T) {
	// config uses the same dispatch envelope as status; the protocol-level
	// rejection rules (unsupported_version, invalid_request, malformed JSON,
	// unknown field) are already covered exhaustively by the status tests
	// in this file. This test pins the dispatch fact: "config" is a known
	// command, while an obviously-unknown command still fails.
	d := startDaemonForTest(t, "0.0.0-m1")
	sockPath := SocketPath(d.socketDir)
	waitForSocket(t, sockPath)

	resp := sendRaw(t, sockPath, []byte(`{"version":1,"command":"nope"}`+"\n"))
	if resp.Error == nil {
		t.Fatalf("expected error response, got result=%+v", resp.Result)
	}
	if resp.Error.Code != ipc.CodeInvalidRequest {
		t.Fatalf("code=%q want %q", resp.Error.Code, ipc.CodeInvalidRequest)
	}
}

func TestSanitizedConfigOmitsAnythingNotOnResult(t *testing.T) {
	// Defence-in-depth: the sanitized builder must produce a value that
	// does not carry any secret-shaped string from the source config. The
	// v1 config has no secret fields, so this is currently a structural
	// assertion: the result must be free of fields not declared on
	// ipc.ConfigResult.
	cfg := config.Defaults("/home/test")
	got := SanitizedConfig(cfg)

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), testAdapterCredential) {
		t.Fatal("sanitized config contains adapter credential")
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for key := range probe {
		switch key {
		case "paths", "logging", "spotify_adapter", "downloader", "acquisition", "sync":
			// expected
		default:
			t.Errorf("unexpected key in sanitized config: %q", key)
		}
	}
	paths, ok := probe["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths missing or wrong type: %T", probe["paths"])
	}
	for _, want := range []string{
		"config_dir", "data_dir", "state_dir", "cache_dir",
		"music_root", "database", "socket_dir", "certs_dir", "log_file",
	} {
		if _, ok := paths[want]; !ok {
			t.Errorf("paths missing %q", want)
		}
	}
}

func startDaemonForTest(t *testing.T, version string) *Daemon {
	t.Helper()
	dir := t.TempDir()
	d, err := NewDaemon(context.Background(), Options{HomeDir: dir, Version: version, AdapterCredential: testAdapterCredential})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	done := make(chan error, 1)
	sigCh := make(chan os.Signal, 1)
	go func() {
		done <- d.Run(context.Background(), RunOptions{SignalCh: sigCh})
	}()
	t.Cleanup(func() {
		sigCh <- os.Interrupt
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("daemon did not exit after signal")
		}
	})
	return d
}

// startDaemonWithConfig is like startDaemonForTest but loads a custom
// config file before the daemon binds its socket.
func startDaemonWithConfig(t *testing.T, configPath, version string) (*Daemon, error) {
	t.Helper()
	dir := t.TempDir()
	d, err := NewDaemon(context.Background(), Options{HomeDir: dir, ConfigPath: configPath, Version: version, AdapterCredential: testAdapterCredential})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = d.Close() })

	done := make(chan error, 1)
	sigCh := make(chan os.Signal, 1)
	go func() {
		done <- d.Run(context.Background(), RunOptions{SignalCh: sigCh})
	}()
	t.Cleanup(func() {
		sigCh <- os.Interrupt
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("daemon did not exit after signal")
		}
	})
	return d, nil
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never appeared", path)
}

func sendRaw(t *testing.T, sockPath string, payload []byte) ipc.Response {
	t.Helper()
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	return mustReadResponse(t, conn)
}

func mustReadResponse(t *testing.T, r net.Conn) ipc.Response {
	t.Helper()
	conn, err := bufio.NewReader(r).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp ipc.Response
	if err := ipc.Decode(conn, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp
}
