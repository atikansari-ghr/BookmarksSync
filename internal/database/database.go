// Package database owns the local SQLite store: opening, pragmas and migrations.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/atikansari-ghr/BookmarksSync/internal/securefile"
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
	if err := securefile.MkdirAll(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		f, createErr := securefile.Create(path)
		if createErr != nil {
			return nil, fmt.Errorf("create database file: %w", createErr)
		}
		if closeErr := f.Close(); closeErr != nil {
			_ = os.Remove(path)
			return nil, fmt.Errorf("close database file: %w", closeErr)
		}
	} else if err != nil {
		return nil, fmt.Errorf("inspect database file: %w", err)
	} else if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("database must be a regular file")
	}
	info, err = os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect database file: %w", err)
	}
	if err := securefile.Check(path, info); err != nil {
		if restrictErr := securefile.RestrictFile(path); restrictErr != nil {
			return nil, fmt.Errorf("restrict database file permissions: %w", restrictErr)
		}
	}

	// Pragmas are passed in the DSN so every pooled connection gets them.
	q := url.Values{}
	// BEGIN IMMEDIATE for every transaction so read-modify-write writers queue on
	// busy_timeout instead of failing with SQLITE_BUSY when upgrading from a read.
	q.Add("_txlock", "immediate")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(FULL)")
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

// Ping verifies the database is usable by reading the applied schema version, so a
// missing or unreadable schema_migrations table (or a database behind this build)
// reports not-ready. It satisfies the server readiness check.
func (d *DB) Ping(ctx context.Context) error {
	v, err := d.SchemaVersion(ctx)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if v < LatestSchemaVersion {
		return fmt.Errorf("schema version %d is behind this build (%d)", v, LatestSchemaVersion)
	}
	return nil
}

// SchemaVersion returns the highest applied migration version.
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := d.sql.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

// Close releases the pool.
func (d *DB) Close() error { return d.sql.Close() }
