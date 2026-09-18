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
	if cfg.SpotifyAdapter.BindAddress != "127.0.0.1" || cfg.SpotifyAdapter.Port != 16352 {
		t.Errorf("SpotifyAdapter=%+v", cfg.SpotifyAdapter)
	}
}

func TestValidateSpotifyAdapter(t *testing.T) {
	tests := []struct {
		name    string
		adapter SpotifyAdapter
		wantErr bool
	}{
		{"ipv4 loopback", SpotifyAdapter{BindAddress: "127.0.0.1", Port: 16352}, false},
		{"ipv6 loopback", SpotifyAdapter{BindAddress: "::1", Port: 16352}, false},
		{"non-loopback", SpotifyAdapter{BindAddress: "0.0.0.0", Port: 16352}, true},
		{"hostname", SpotifyAdapter{BindAddress: "localhost", Port: 16352}, true},
		{"zero port", SpotifyAdapter{BindAddress: "127.0.0.1", Port: 0}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSpotifyAdapter(tt.adapter)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateSpotifyAdapter() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
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

func TestLoadUsesProvidedHomeForDefaultConfigPath(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".config", AppDirName)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[logging]\nlevel = \"debug\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := NewLoader(dir, "").Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logging.Level != "debug" {
		t.Fatalf("Logging.Level=%q want debug", cfg.Logging.Level)
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

func TestAcquisitionConcurrencyBounds(t *testing.T) {
	for _, value := range []string{"0", "-1", "33"} {
		t.Run(value, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "config.toml")
			if err := os.WriteFile(path, []byte("[acquisition]\nconcurrency="+value+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewLoader(home, path).Load(); err == nil {
				t.Fatal("invalid concurrency accepted")
			}
		})
	}
}
