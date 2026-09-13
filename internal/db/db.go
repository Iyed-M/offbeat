package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const MigrationsTableSchema = `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version    INTEGER PRIMARY KEY,
	name       TEXT    NOT NULL,
	applied_at TEXT    NOT NULL
);
`

//go:embed migrations/*.sql
var embedded embed.FS

type Migration struct {
	Version int
	Name    string
	SQL     string
}

type Source interface {
	ReadDir(name string) ([]fs.DirEntry, error)
	ReadFile(name string) ([]byte, error)
}

type embedSource struct{ fs fs.FS }

func (e embedSource) ReadDir(n string) ([]fs.DirEntry, error) { return fs.ReadDir(e.fs, n) }
func (e embedSource) ReadFile(n string) ([]byte, error)       { return fs.ReadFile(e.fs, n) }

func DefaultSource() Source { return embedSource{fs: embedded} }

func LoadMigrations(src Source, dir string) ([]Migration, error) {
	if src == nil {
		src = DefaultSource()
	}
	if dir == "" {
		dir = "migrations"
	}
	entries, err := src.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir %q: %w", dir, err)
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		v, name, ok := splitMigrationFilename(e.Name())
		if !ok {
			return nil, fmt.Errorf("invalid migration filename %q", e.Name())
		}
		data, err := src.ReadFile(dir + "/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}
		out = append(out, Migration{Version: v, Name: name, SQL: string(data)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func splitMigrationFilename(name string) (int, string, bool) {
	base := strings.TrimSuffix(name, ".sql")
	idx := strings.Index(base, "_")
	if idx <= 0 {
		return 0, "", false
	}
	v, err := strconv.Atoi(base[:idx])
	if err != nil {
		return 0, "", false
	}
	return v, base[idx+1:], true
}

type DB struct {
	*sql.DB
}

func Open(ctx context.Context, dsn string) (*DB, error) {
	if dsn == "" {
		return nil, errors.New("empty database dsn")
	}
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	d.SetMaxOpenConns(1)
	d.SetMaxIdleConns(1)
	d.SetConnMaxLifetime(0)
	if err := d.PingContext(ctx); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return &DB{DB: d}, nil
}

func OpenFile(ctx context.Context, path string) (*DB, error) {
	dsn := buildDSN(path)
	return Open(ctx, dsn)
}

func buildDSN(path string) string {
	params := "_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	return "file:" + path + "?" + params
}

func (d *DB) Migrate(ctx context.Context, src Source, dir string) (applied []int, err error) {
	if src == nil {
		src = DefaultSource()
	}
	if _, err := d.ExecContext(ctx, MigrationsTableSchema); err != nil {
		return nil, fmt.Errorf("ensure migrations table: %w", err)
	}
	migs, err := LoadMigrations(src, dir)
	if err != nil {
		return nil, err
	}
	current, err := d.appliedVersions(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range migs {
		if _, ok := current[m.Version]; ok {
			continue
		}
		if err := d.applyOne(ctx, m); err != nil {
			return applied, fmt.Errorf("apply migration %04d_%s: %w", m.Version, m.Name, err)
		}
		applied = append(applied, m.Version)
	}
	return applied, nil
}

func (d *DB) appliedVersions(ctx context.Context) (map[int]struct{}, error) {
	rows, err := d.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("query migrations: %w", err)
	}
	defer rows.Close()
	out := make(map[int]struct{})
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = struct{}{}
	}
	return out, rows.Err()
}

func (d *DB) applyOne(ctx context.Context, m Migration) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, ?)",
		m.Version, m.Name, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (d *DB) Close() error {
	if d == nil || d.DB == nil {
		return nil
	}
	return d.DB.Close()
}
