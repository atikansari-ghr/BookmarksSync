package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func rawDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "raw.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestFailedMigrationRollsBackCompletely(t *testing.T) {
	ctx := context.Background()
	db := rawDB(t)
	bad := []migration{
		{version: 1, sql: `CREATE TABLE ok_table(id INTEGER)`},
		{version: 2, sql: `CREATE TABLE half(id INTEGER); THIS IS NOT SQL`},
	}
	if err := applyMigrations(ctx, db, bad); err == nil {
		t.Fatal("applyMigrations succeeded with invalid SQL")
	}
	var half int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='half'`).Scan(&half); err != nil || half != 0 {
		t.Errorf("failed migration left table behind (count=%d err=%v)", half, err)
	}
	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version); err != nil || version != 1 {
		t.Errorf("recorded version = %d (err %v), want 1 (only the good migration)", version, err)
	}
}

func TestMigrationsAreContiguousAndEmbedded(t *testing.T) {
	ms, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(ms) == 0 {
		t.Fatal("no embedded migrations")
	}
	for i, m := range ms {
		if m.version != i+1 {
			t.Errorf("migration %d has version %d; versions must be contiguous from 1", i, m.version)
		}
	}
	if ms[len(ms)-1].version != LatestSchemaVersion {
		t.Errorf("LatestSchemaVersion = %d, last migration = %d", LatestSchemaVersion, ms[len(ms)-1].version)
	}
}
