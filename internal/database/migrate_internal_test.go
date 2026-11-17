package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
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

// TestIdentityMigrationUpgradesMilestone2DataIntact applies migrations up to 0002, fills
// representative milestone-2 data, then upgrades to the latest schema and verifies the
// existing rows survive and the identity tables exist.
func TestIdentityMigrationUpgradesMilestone2DataIntact(t *testing.T) {
	ctx := context.Background()
	db := rawDB(t)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 3 || ms[2].version != 3 {
		t.Fatalf("migration 0003 missing (have %d migrations)", len(ms))
	}
	if err := applyMigrations(ctx, db, ms[:2]); err != nil {
		t.Fatalf("milestone-2 schema: %v", err)
	}
	fixture := []string{
		`INSERT INTO users(id, username, password_hash, is_admin, active, created_at) VALUES (1, 'alice', '$argon2id$fixture-a', 1, 1, 1), (2, 'bob', '$argon2id$fixture-b', 0, 1, 2)`,
		`INSERT INTO folders(id, owner_id, kind, title, created_at, updated_at) VALUES (1, 1, 'bar', 'Bar', 1, 1), (2, 1, 'folder', 'Shared', 1, 1)`,
		`UPDATE folders SET parent_id = 1 WHERE id = 2`,
		`INSERT INTO bookmarks(id, owner_id, url, title, created_at, updated_at) VALUES (1, 1, 'https://example.com/', 'Example', 1, 1)`,
		`INSERT INTO bookmark_tags(bookmark_id, tag) VALUES (1, 'work')`,
		`INSERT INTO placements(id, bookmark_id, folder_id, position) VALUES (1, 1, 2, 0)`,
		`INSERT INTO shares(id, owner_id, folder_id, recipient_id, mode, created_at) VALUES (1, 1, 2, 2, 'edit', 1)`,
		`INSERT INTO credentials(id, user_id, label, secret_hash, kind, share_id, mode, created_at) VALUES (1, 1, 'laptop', 'aa', 'private', NULL, 'edit', 1), (2, 2, 'share', 'bb', 'share', 1, 'view', 1)`,
	}
	for _, q := range fixture {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("fixture %q: %v", q, err)
		}
	}
	if err := applyMigrations(ctx, db, ms); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != LatestSchemaVersion {
		t.Fatalf("version = %d (%v), want %d", version, err, LatestSchemaVersion)
	}
	checks := map[string]string{
		`SELECT group_concat(username || ':' || password_hash || ':' || is_admin, ',') FROM users ORDER BY id`: "alice:$argon2id$fixture-a:1,bob:$argon2id$fixture-b:0",
		`SELECT url || ':' || title FROM bookmarks WHERE id = 1`:                                               "https://example.com/:Example",
		`SELECT tag FROM bookmark_tags WHERE bookmark_id = 1`:                                                  "work",
		`SELECT folder_id FROM placements WHERE id = 1`:                                                        "2",
		`SELECT recipient_id || ':' || mode FROM shares WHERE id = 1`:                                          "2:edit",
		`SELECT group_concat(secret_hash || ':' || kind, ',') FROM credentials ORDER BY id`:                    "aa:private,bb:share",
	}
	for q, want := range checks {
		var got string
		if err := db.QueryRow(q).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", q, got, err, want)
		}
	}
	for _, table := range []string{"web_sessions", "api_tokens", "webauthn_users", "webauthn_credentials",
		"totp_secrets", "recovery_codes", "oidc_identities", "auth_flows", "user_settings"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing after upgrade (%v)", table, err)
		}
	}
}

// TestIdentitySchemaEnforcesKeyConstraints pins the schema-level invariants later
// tasks rely on: unique secret hashes, one OIDC link per issuer+subject, and bounded settings.
func TestIdentitySchemaEnforcesKeyConstraints(t *testing.T) {
	ctx := context.Background()
	db := rawDB(t)
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	// Match database.Open, which enables foreign keys on every connection.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	if err := applyMigrations(ctx, db, ms); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustFail := func(why, q string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q); err == nil {
			t.Errorf("%s: %s succeeded", why, q)
		}
	}
	mustExec(`INSERT INTO users(id, username, password_hash, created_at) VALUES (1, 'a', 'h', 1), (2, 'b', 'h', 1)`)
	mustExec(`INSERT INTO web_sessions(user_id, secret_hash, created_at, last_seen_at, authenticated_at, expires_at) VALUES (1, 's1', 1, 1, 1, 2)`)
	mustFail("duplicate session hash", `INSERT INTO web_sessions(user_id, secret_hash, created_at, last_seen_at, authenticated_at, expires_at) VALUES (2, 's1', 1, 1, 1, 2)`)
	mustFail("session for unknown user", `INSERT INTO web_sessions(user_id, secret_hash, created_at, last_seen_at, authenticated_at, expires_at) VALUES (99, 's2', 1, 1, 1, 2)`)
	mustExec(`INSERT INTO api_tokens(user_id, secret_hash, scopes, created_at) VALUES (1, 't1', 'bookmarks:read', 1)`)
	mustFail("duplicate token hash", `INSERT INTO api_tokens(user_id, secret_hash, scopes, created_at) VALUES (2, 't1', 'bookmarks:read', 1)`)
	mustFail("token without scopes", `INSERT INTO api_tokens(user_id, secret_hash, scopes, created_at) VALUES (1, 't2', '', 1)`)
	mustExec(`INSERT INTO oidc_identities(user_id, provider, issuer, subject, created_at) VALUES (1, 'google', 'https://accounts.google.com', 'sub-1', 1)`)
	mustFail("OIDC subject linked twice", `INSERT INTO oidc_identities(user_id, provider, issuer, subject, created_at) VALUES (2, 'google', 'https://accounts.google.com', 'sub-1', 1)`)
	mustExec(`INSERT INTO webauthn_credentials(user_id, credential_id, public_key, created_at) VALUES (1, x'01', x'02', 1)`)
	mustFail("passkey registered twice", `INSERT INTO webauthn_credentials(user_id, credential_id, public_key, created_at) VALUES (2, x'01', x'02', 1)`)
	mustExec(`INSERT INTO recovery_codes(user_id, code_hash, created_at) VALUES (1, 'r1', 1)`)
	mustFail("duplicate recovery code", `INSERT INTO recovery_codes(user_id, code_hash, created_at) VALUES (1, 'r1', 1)`)
	mustExec(`INSERT INTO auth_flows(flow_hash, kind, data, created_at, expires_at) VALUES ('f1', 'oidc_login', '{}', 1, 2)`)
	mustFail("duplicate flow hash", `INSERT INTO auth_flows(flow_hash, kind, data, created_at, expires_at) VALUES ('f1', 'oidc_login', '{}', 1, 2)`)
	mustExec(`INSERT INTO totp_secrets(user_id, seed_ciphertext, created_at) VALUES (1, x'00', 1)`)
	mustFail("second TOTP seed for one user", `INSERT INTO totp_secrets(user_id, seed_ciphertext, created_at) VALUES (1, x'00', 1)`)
	mustExec(`INSERT INTO user_settings(user_id, key, value, updated_at) VALUES (1, 'theme', '"ocean"', 1)`)
	mustFail("oversized setting", `INSERT INTO user_settings(user_id, key, value, updated_at) VALUES (1, 'big', '`+strings.Repeat("x", 8193)+`', 1)`)
}
