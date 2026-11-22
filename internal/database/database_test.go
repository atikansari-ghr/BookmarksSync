package database_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/database"
)

func tempDB(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "data", "test.db")
}

func TestOpenCreatesDatabaseAtLatestSchema(t *testing.T) {
	ctx := context.Background()
	path := tempDB(t)
	db, err := database.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	v, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v != database.LatestSchemaVersion || v < 1 {
		t.Errorf("schema version = %d, want %d (>=1)", v, database.LatestSchemaVersion)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("database file missing: %v", err)
	}
}

func TestOpenReadOnlyDoesNotApplyPendingMigrations(t *testing.T) {
	ctx := context.Background()
	path := tempDB(t)
	db, err := database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `DELETE FROM schema_migrations WHERE version=?`, database.LatestSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := database.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	version, err := readOnly.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != database.LatestSchemaVersion-1 {
		t.Fatalf("schema version = %d; read-only open applied a migration", version)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	verify, err := database.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer verify.Close()
	version, err = verify.SchemaVersion(ctx)
	if err != nil || version != database.LatestSchemaVersion-1 {
		t.Fatalf("persisted schema version = %d, %v", version, err)
	}
}

func TestOpenReadOnlyUsesExactSpecialPath(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "snapshot # percent% unicode-书签.db")
	db, err := database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO app_metadata(key,value) VALUES('readonly-path','exact')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := database.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	var got string
	if err := readOnly.SQL().QueryRowContext(ctx, `SELECT value FROM app_metadata WHERE key='readonly-path'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "exact" {
		t.Fatalf("read-only special path value = %q", got)
	}
}

func TestReopenKeepsDataAndDoesNotReapplyMigrations(t *testing.T) {
	ctx := context.Background()
	path := tempDB(t)

	db, err := database.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO app_metadata(key, value) VALUES ('probe', 'persisted')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for i := 0; i < 2; i++ {
		db, err = database.Open(ctx, path)
		if err != nil {
			t.Fatalf("reopen %d: %v", i, err)
		}
		var got string
		if err := db.SQL().QueryRowContext(ctx, `SELECT value FROM app_metadata WHERE key='probe'`).Scan(&got); err != nil {
			t.Fatalf("select after reopen: %v", err)
		}
		if got != "persisted" {
			t.Errorf("value = %q, want persisted", got)
		}
		var applied int
		if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
			t.Fatalf("count: %v", err)
		}
		if applied != database.LatestSchemaVersion {
			t.Errorf("schema_migrations rows = %d, want %d", applied, database.LatestSchemaVersion)
		}
		db.Close()
	}
}

func TestEveryPooledConnectionHasSafetyPragmas(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, tempDB(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Hold several connections at once so the pool must create new ones.
	for i := 0; i < 3; i++ {
		conn, err := db.SQL().Conn(ctx)
		if err != nil {
			t.Fatalf("Conn: %v", err)
		}
		defer conn.Close()
		var journal string
		var fk, busy int
		if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journal); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busy); err != nil {
			t.Fatal(err)
		}
		var sync int
		if err := conn.QueryRowContext(ctx, `PRAGMA synchronous`).Scan(&sync); err != nil {
			t.Fatal(err)
		}
		if journal != "wal" || fk != 1 || busy < 1000 {
			t.Errorf("conn %d: journal=%q foreign_keys=%d busy_timeout=%d", i, journal, fk, busy)
		}
		// 2 == FULL: an acknowledged write survives power loss even in WAL mode.
		if sync != 2 {
			t.Errorf("conn %d: synchronous=%d, want 2 (FULL)", i, sync)
		}
	}
}

func TestPingReadsSchemaMigrations(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, tempDB(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		t.Fatalf("Ping on healthy db: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `ALTER TABLE schema_migrations RENAME TO schema_migrations_gone`); err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(ctx); err == nil {
		t.Fatal("Ping succeeded although schema_migrations is missing")
	}
}

func TestPingFailsWhenSchemaIsBehindThisBuild(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, tempDB(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if _, err := db.SQL().ExecContext(ctx, `DELETE FROM schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(ctx); err == nil {
		t.Fatal("Ping succeeded with no recorded schema version")
	}
}

func TestPingFailsAfterClose(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, tempDB(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Ping(ctx); err != nil {
		t.Fatalf("Ping on open db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := db.Ping(ctx); err == nil {
		t.Fatal("Ping on closed db succeeded, want error")
	}
}

func TestOpenRefusesDatabaseFromNewerVersion(t *testing.T) {
	ctx := context.Background()
	path := tempDB(t)
	db, err := database.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (9999, 'future')`); err != nil {
		t.Fatalf("seed future version: %v", err)
	}
	db.Close()

	if db, err := database.Open(ctx, path); err == nil {
		db.Close()
		t.Fatal("Open accepted a database newer than this build")
	}
}

func TestOpenFailsWhenPathIsUnusable(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A regular file cannot act as the parent directory.
	if db, err := database.Open(context.Background(), filepath.Join(blocker, "x.db")); err == nil {
		db.Close()
		t.Fatal("Open succeeded with a file as parent directory")
	}
}

func TestOpenCancelledContextFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if db, err := database.Open(ctx, tempDB(t)); err == nil {
		db.Close()
		t.Fatal("Open succeeded with cancelled context")
	}
}

func TestPrivatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	path := tempDB(t)
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat data dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("dir perm = %v, want 0700", dirInfo.Mode().Perm())
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat database file: %v", err)
	}
	if fileInfo.Mode().Perm()&0o077 != 0 {
		t.Errorf("file perm = %v, want no group/other access", fileInfo.Mode().Perm())
	}
}

func TestOpenUsesExactPathForAwkwardFileNames(t *testing.T) {
	names := []string{"plain.db", "hash#name.db", "per%cent.db", "per%23cent.db", "with space.db", "ünï-é.db", "amp&eq=.db", "plus+sign.db"}
	if runtime.GOOS != "windows" {
		names = append(names, "q?mark.db")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			db, err := database.Open(ctx, path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer db.Close()

			var file string
			if err := db.SQL().QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&file); err != nil {
				t.Fatalf("database_list: %v", err)
			}
			want, err := os.Stat(path)
			if err != nil {
				t.Fatalf("intended file %q was not created: %v", path, err)
			}
			got, err := os.Stat(file)
			if err != nil {
				t.Fatalf("SQLite opened %q, which does not exist: %v", file, err)
			}
			if !os.SameFile(want, got) {
				t.Errorf("SQLite opened %q, want %q", file, path)
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if n := e.Name(); n != name && n != name+"-wal" && n != name+"-shm" {
					t.Errorf("unexpected stray file %q next to %q", n, name)
				}
			}
		})
	}
}

func TestOpenTightensPermissionsOfExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	path := tempDB(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat database file: %v", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("existing file perm = %v, want no group/other access", fi.Mode().Perm())
	}
}

// Transactions must start with BEGIN IMMEDIATE: a deferred transaction that reads
// and then writes fails at once with SQLITE_BUSY instead of waiting on the busy
// timeout when another writer committed in between. Read-modify-write sync
// operations rely on writers queueing.
func TestConcurrentReadModifyWriteTransactionsQueueInsteadOfFailing(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, tempDB(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if _, err := db.SQL().ExecContext(ctx, `INSERT INTO app_metadata(key, value) VALUES ('counter', '0')`); err != nil {
		t.Fatal(err)
	}
	const workers, rounds = 8, 25
	errs := make(chan error, workers*rounds)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				tx, err := db.SQL().BeginTx(ctx, nil)
				if err != nil {
					errs <- err
					return
				}
				var v int
				var s string
				if err := tx.QueryRowContext(ctx, `SELECT value FROM app_metadata WHERE key='counter'`).Scan(&s); err != nil {
					tx.Rollback()
					errs <- err
					return
				}
				v, _ = strconv.Atoi(s)
				if _, err := tx.ExecContext(ctx, `UPDATE app_metadata SET value=? WHERE key='counter'`, strconv.Itoa(v+1)); err != nil {
					tx.Rollback()
					errs <- err
					return
				}
				if err := tx.Commit(); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("transaction failed: %v", err)
	}
	var final string
	if err := db.SQL().QueryRowContext(ctx, `SELECT value FROM app_metadata WHERE key='counter'`).Scan(&final); err != nil {
		t.Fatal(err)
	}
	if final != strconv.Itoa(workers*rounds) {
		t.Errorf("counter = %s, want %d (lost updates)", final, workers*rounds)
	}
}
