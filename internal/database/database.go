// Package database owns the local SQLite store: opening, pragmas and migrations.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver
)

// DB wraps the shared connection pool. Later components reuse SQL() rather than
// opening their own connections.
type DB struct {
	sql *sql.DB
}

// Open creates (if needed) and migrates the database at path. The parent
// directory is created with mode 0700 and the file is restricted to its owner
// where the platform supports POSIX permissions.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	// Create the file ourselves so it is never world-readable, even briefly.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create database file: %w", err)
	}
	f.Close()
	// OpenFile's mode only applies to new files; tighten a pre-existing one.
	// Windows ignores POSIX bits, so a failure there is not actionable.
	if err := os.Chmod(path, 0o600); err != nil && runtime.GOOS != "windows" {
		return nil, fmt.Errorf("restrict database file permissions: %w", err)
	}

	// Pragmas are passed in the DSN so every pooled connection gets them.
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath // Windows drive paths: file:///C:/...
	}
	// url.URL escapes '#', '?', '%' and spaces in Path, so SQLite's URI parser
	// sees the exact filename instead of truncating or percent-decoding it.
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: q.Encode()}).String()

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	ms, err := loadMigrations()
	if err != nil {
		sqlDB.Close()
		return nil, err
	}
	if err := applyMigrations(ctx, sqlDB, ms); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return &DB{sql: sqlDB}, nil
}

// SQL returns the underlying pool for use by repositories.
func (d *DB) SQL() *sql.DB { return d.sql }

// Ping verifies the database is usable; it satisfies the server readiness check.
func (d *DB) Ping(ctx context.Context) error {
	var one int
	return d.sql.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
}

// SchemaVersion returns the highest applied migration version.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := d.sql.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

// Close releases the pool.
func (d *DB) Close() error { return d.sql.Close() }
