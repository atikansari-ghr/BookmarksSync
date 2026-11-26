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

	_ "modernc.org/sqlite"
)

// OpenReadOnly opens an existing database without creating files or applying
// migrations. It is intended for backup and inspection commands.
func OpenReadOnly(ctx context.Context, path string) (*DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect database: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("database must be a regular file")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: q.Encode()}).String()
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open read-only database: %w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("open read-only database: %w", err)
	}
	db := &DB{sql: sqlDB}
	schema, err := db.SchemaVersion(ctx)
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("read database schema: %w", err)
	}
	if schema < 1 || schema > LatestSchemaVersion {
		sqlDB.Close()
		if schema > LatestSchemaVersion {
			return nil, errors.New("database schema is newer than this application")
		}
		return nil, errors.New("database has no applied schema")
	}
	return db, nil
}
