package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d, err := OpenFile(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestOpenClose(t *testing.T) {
	d := newTestDB(t)
	if err := d.PingContext(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestMigrateRunsOnce(t *testing.T) {
	d := newTestDB(t)
	ctx := context.Background()
	first, err := d.Migrate(ctx, nil, "")
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if len(first) != 1 || first[0] != 1 {
		t.Fatalf("first applied=%v", first)
	}
	second, err := d.Migrate(ctx, nil, "")
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second applied=%v want empty", second)
	}
}

func TestMigrationsTablePresent(t *testing.T) {
	d := newTestDB(t)
	if _, err := d.Migrate(context.Background(), nil, ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var count int
	if err := d.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 1 {
		t.Fatalf("expected at least 1 applied migration, got %d", count)
	}
}

func TestSplitMigrationFilename(t *testing.T) {
	v, name, ok := splitMigrationFilename("0042_add_index.sql")
	if !ok || v != 42 || name != "add_index" {
		t.Fatalf("got v=%d name=%q ok=%v", v, name, ok)
	}
	if _, _, ok := splitMigrationFilename("notanumber_x.sql"); ok {
		t.Fatal("expected not ok")
	}
}

func TestMigrateForeignKeysEnabled(t *testing.T) {
	d := newTestDB(t)
	if _, err := d.Migrate(context.Background(), nil, ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var on int
	if err := d.QueryRow("PRAGMA foreign_keys").Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on != 1 {
		t.Fatalf("foreign_keys=%d want 1", on)
	}
}

func TestMigrateJournalModeWAL(t *testing.T) {
	d := newTestDB(t)
	if _, err := d.Migrate(context.Background(), nil, ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var mode string
	if err := d.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode=%q want wal", mode)
	}
}

func TestEmbeddedMigrationsAreOrdered(t *testing.T) {
	migs, err := LoadMigrations(nil, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(migs) == 0 {
		t.Fatal("expected at least one embedded migration")
	}
	for i := 1; i < len(migs); i++ {
		if migs[i].Version <= migs[i-1].Version {
			t.Fatalf("not sorted: %d <= %d", migs[i].Version, migs[i-1].Version)
		}
	}
}
