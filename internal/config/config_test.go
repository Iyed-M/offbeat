package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults("/home/test")
	want := []struct {
		field, got, want string
	}{
		{"ConfigDir", cfg.Paths.ConfigDir, "/home/test/.config/offbeat"},
		{"DataDir", cfg.Paths.DataDir, "/home/test/.local/share/offbeat"},
		{"Database", cfg.Paths.Database, "/home/test/.local/share/offbeat/offbeat.db"},
		{"MusicRoot", cfg.Paths.MusicRoot, "/home/test/Music/Offbeat"},
		{"CertsDir", cfg.Paths.CertsDir, "/home/test/.local/state/offbeat/certs"},
	}
	for _, c := range want {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
	if cfg.Acquisition.Concurrency != 2 {
		t.Errorf("Concurrency = %d, want 2", cfg.Acquisition.Concurrency)
	}
	if cfg.Downloader.YTDLPPath != "yt-dlp" {
		t.Errorf("YTDLPPath = %q", cfg.Downloader.YTDLPPath)
	}
}

func TestUserHomeDirOverride(t *testing.T) {
	t.Setenv("OFFBEAT_HOME", "/custom/home")
	h, err := UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if h != "/custom/home" {
		t.Fatalf("home=%q want /custom/home", h)
	}
}

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader(dir, filepath.Join(dir, "nope.toml"))
	cfg, err := l.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Paths.Database != filepath.Join(dir, ".local", "share", "offbeat", "offbeat.db") {
		t.Fatalf("database path: %s", cfg.Paths.Database)
	}
}

func TestLoadTOML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	contents := `
[logging]
level = "debug"
format = "json"

[paths]
database = "/var/lib/offbeat.db"

[acquisition]
concurrency = 4
temp_retry_backoff = "10s"
max_temp_retries = 7

[downloader]
yt_dlp_path = "/usr/local/bin/yt-dlp"
ffmpeg_path = "/usr/bin/ffmpeg"

[sync]
https_port = 8443
pairing_timeout = "2m"
`
	if err := os.WriteFile(cfgPath, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	l := NewLoader(dir, cfgPath)
	cfg, err := l.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level=%q", cfg.Logging.Level)
	}
	if cfg.Logging.Format != "json" {
		t.Errorf("Logging.Format=%q", cfg.Logging.Format)
	}
	if cfg.Paths.Database != "/var/lib/offbeat.db" {
		t.Errorf("Database=%q", cfg.Paths.Database)
	}
	if cfg.Acquisition.Concurrency != 4 {
		t.Errorf("Concurrency=%d", cfg.Acquisition.Concurrency)
	}
	if cfg.Acquisition.TempRetryBackoff != Duration(10*time.Second) {
		t.Errorf("TempRetryBackoff=%v", cfg.Acquisition.TempRetryBackoff)
	}
	if cfg.Acquisition.MaxTempRetries != 7 {
		t.Errorf("MaxTempRetries=%d", cfg.Acquisition.MaxTempRetries)
	}
	if cfg.Downloader.YTDLPPath != "/usr/local/bin/yt-dlp" {
		t.Errorf("YTDLPPath=%q", cfg.Downloader.YTDLPPath)
	}
	if cfg.Sync.HTTPSPort != 8443 {
		t.Errorf("HTTPSPort=%d", cfg.Sync.HTTPSPort)
	}
	if cfg.Sync.PairingTimeout != Duration(2*time.Minute) {
		t.Errorf("PairingTimeout=%v", cfg.Sync.PairingTimeout)
	}
}

func TestApplyDerivedAbsoluteDatabaseStays(t *testing.T) {
	cfg := Defaults("/home/test")
	cfg.Paths.Database = "/abs/db.sqlite"
	got := applyDerived(cfg, "/home/test")
	if got.Paths.Database != "/abs/db.sqlite" {
		t.Fatalf("absolute path rewritten: %q", got.Paths.Database)
	}
}

func TestLoadEmptyTOML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("# nothing here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := NewLoader(dir, cfgPath)
	if _, err := l.Load(); err != nil {
		t.Fatalf("Load empty: %v", err)
	}
}
