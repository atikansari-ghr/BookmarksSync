package database_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	_ "modernc.org/sqlite"
)

func TestBackupToCapturesCommittedWALData(t *testing.T) {
	ctx := context.Background()
	source, err := database.Open(ctx, tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.SQL().ExecContext(ctx, `INSERT INTO app_metadata(key,value) VALUES('backup-probe','from-wal')`); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "backup.db")
	if err := source.BackupTo(ctx, destination); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}

	copy, err := sql.Open("sqlite", destination)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var got string
	if err := copy.QueryRowContext(ctx, `SELECT value FROM app_metadata WHERE key='backup-probe'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "from-wal" {
		t.Fatalf("backup value = %q", got)
	}
	var schema int
	if err := copy.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if schema != database.LatestSchemaVersion {
		t.Fatalf("schema version = %d", schema)
	}
}

func TestBackupToHandlesSpecialDestinationPaths(t *testing.T) {
	ctx := context.Background()
	source, err := database.Open(ctx, tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, name := range []string{"space name.db", "hash#name.db", "percent%name.db", "unicode-书签.db"} {
		path := filepath.Join(t.TempDir(), name)
		if err := source.BackupTo(ctx, path); err != nil {
			t.Errorf("BackupTo(%q): %v", name, err)
			continue
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("backup %q missing: %v", name, err)
		}
	}
}

func TestBackupToHonorsCancellationAndRemovesPartialOutput(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source, err := database.Open(context.Background(), tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination := filepath.Join(t.TempDir(), "cancelled.db")
	if err := source.BackupTo(ctx, destination); err == nil {
		t.Fatal("BackupTo succeeded with cancelled context")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("partial destination remains: %v", err)
	}
}

func TestBackupFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions are unavailable on Windows")
	}
	ctx := context.Background()
	source, err := database.Open(ctx, tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	path := filepath.Join(t.TempDir(), "private.db")
	if err := source.BackupTo(ctx, path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("backup permissions = %o", info.Mode().Perm())
	}
}

func TestBackupToDoesNotReplaceExistingFile(t *testing.T) {
	source, err := database.Open(context.Background(), tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination := filepath.Join(t.TempDir(), "existing.db")
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := source.BackupTo(context.Background(), destination); err == nil {
		t.Fatal("BackupTo replaced an existing destination")
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != "keep" {
		t.Fatalf("existing destination changed: %q, %v", got, err)
	}
}
