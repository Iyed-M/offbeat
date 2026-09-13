package db_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Iyed-M/offbeat/internal/db"
)

func TestSchemaVersionReturnsMaxAppliedVersion(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d, err := db.OpenFile(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if _, err := d.Migrate(ctx, nil, ""); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	v, err := db.SchemaVersion(ctx, d)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 1 {
		t.Fatalf("SchemaVersion=%d want 1 (placeholder migration)", v)
	}
}

func TestSchemaVersionReturnsZeroOnEmptyDB(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d, err := db.OpenFile(ctx, filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	v, err := db.SchemaVersion(ctx, d)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != 0 {
		t.Fatalf("SchemaVersion=%d want 0", v)
	}
}
