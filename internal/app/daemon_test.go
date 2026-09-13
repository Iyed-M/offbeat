package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/config"
	"github.com/Iyed-M/offbeat/internal/db"
)

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
		HomeDir:    dir,
		ConfigPath: cfgPath,
	})
	if err != nil {
		t.Fatalf("NewDaemon: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
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
}

func TestNewDaemonRejectsBadConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[unknown]\nx = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewDaemon(context.Background(), Options{HomeDir: dir, ConfigPath: cfgPath})
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestRunRespondsToSignal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := NewDaemon(context.Background(), Options{HomeDir: dir, ConfigPath: cfgPath})
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

func TestRunRespondsToContextCancel(t *testing.T) {
	dir := t.TempDir()
	d, err := NewDaemon(context.Background(), Options{HomeDir: dir})
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
