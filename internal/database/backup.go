package database

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/securefile"
	"modernc.org/sqlite"
)

type sqliteBackuper interface {
	NewBackup(string) (*sqlite.Backup, error)
}

// BackupTo writes a consistent SQLite snapshot to a new, private file. It uses
// SQLite's online backup API so committed WAL pages are included; the live
// database, WAL, and SHM files are never copied directly.
func (d *DB) BackupTo(ctx context.Context, destination string) (retErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if destination == "" {
		return errors.New("backup destination is required")
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve backup destination: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	file, err := securefile.Create(absolute)
	if err != nil {
		return fmt.Errorf("create backup destination: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(absolute)
		return fmt.Errorf("close backup destination: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(absolute)
		}
	}()

	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	destinationURI := (&url.URL{Scheme: "file", Path: uriPath}).String()
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire database connection: %w", err)
	}
	defer conn.Close()
	var backup *sqlite.Backup
	if err := conn.Raw(func(raw any) error {
		backuper, ok := raw.(sqliteBackuper)
		if !ok {
			return errors.New("SQLite driver does not support online backups")
		}
		backup, err = backuper.NewBackup(destinationURI)
		return err
	}); err != nil {
		return fmt.Errorf("start SQLite backup: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			if err := backup.Finish(); retErr == nil && err != nil {
				retErr = fmt.Errorf("finish SQLite backup: %w", err)
			}
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		more, err := backup.Step(128)
		if err != nil {
			if isSQLiteBusy(err) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(10 * time.Millisecond):
					continue
				}
			}
			return fmt.Errorf("copy SQLite pages: %w", err)
		}
		if !more {
			break
		}
	}
	if err := backup.Finish(); err != nil {
		finished = true
		return fmt.Errorf("finish SQLite backup: %w", err)
	}
	finished = true
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := syncFile(absolute); err != nil {
		return fmt.Errorf("sync backup file: %w", err)
	}
	complete = true
	return nil
}

func isSQLiteBusy(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & 0xff
	return code == 5 || code == 6 // SQLITE_BUSY / SQLITE_LOCKED
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
