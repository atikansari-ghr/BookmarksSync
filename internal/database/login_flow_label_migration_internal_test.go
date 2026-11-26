package database

import (
	"context"
	"testing"
)

// TestLoginFlowLabelMigrationKeepsInFlightFlowsAndCredentials upgrades a 0014
// database holding pending and approved Login Flow rows plus an existing
// credential. Migration 0015 must be additive: flows survive with the legacy
// connection name and existing credential labels and secrets are untouched.
func TestLoginFlowLabelMigrationKeepsInFlightFlowsAndCredentials(t *testing.T) {
	ctx := context.Background()
	db := rawDB(t)
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	ms, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) < 15 || ms[13].version != 14 || ms[14].version != 15 {
		t.Fatalf("migration 0015 missing (have %d migrations)", len(ms))
	}
	if err := applyMigrations(ctx, db, ms[:14]); err != nil {
		t.Fatalf("0014 schema: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO users(id, username, password_hash, is_admin, active, created_at) VALUES (1, 'alice', 'ha', 0, 1, 1)`,
		`INSERT INTO web_sessions(id, user_id, secret_hash, created_at, last_seen_at, authenticated_at, expires_at) VALUES (7, 1, 'sh', 1, 1, 1, 9999999999999)`,
		`INSERT INTO login_flows_v2(browser_hash, poll_hash, expires_at, session_id) VALUES ('b-pending', 'p-pending', 9999999999999, NULL), ('b-approved', 'p-approved', 9999999999999, 7)`,
		`INSERT INTO credentials(id, user_id, label, secret_hash, kind, share_id, mode, created_at) VALUES (1, 1, 'Login Flow v2 connection', 'aa', 'private', NULL, 'edit', 1), (2, 1, 'laptop', 'bb', 'private', NULL, 'view', 2)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("fixture %q: %v", q, err)
		}
	}
	if err := applyMigrations(ctx, db, ms); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	for _, check := range [][2]string{
		{`SELECT group_concat(browser_hash || ':' || coalesce(session_id, '-') || ':' || credential_label, ',') FROM (SELECT * FROM login_flows_v2 ORDER BY browser_hash)`,
			"b-approved:7:Login Flow v2 connection,b-pending:-:Login Flow v2 connection"},
		{`SELECT group_concat(id || ':' || label || ':' || secret_hash || ':' || mode, ',') FROM (SELECT * FROM credentials ORDER BY id)`,
			"1:Login Flow v2 connection:aa:edit,2:laptop:bb:view"},
		{`SELECT "notnull" || ':' || dflt_value FROM pragma_table_info('login_flows_v2') WHERE name = 'credential_label'`,
			"1:'Login Flow v2 connection'"},
	} {
		q, want := check[0], check[1]
		var got string
		if err := db.QueryRow(q).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", q, got, err, want)
		}
	}
	// Writers that predate the column still insert successfully.
	if _, err := db.ExecContext(ctx, `INSERT INTO login_flows_v2(browser_hash, poll_hash, expires_at) VALUES ('b-new', 'p-new', 1)`); err != nil {
		t.Fatalf("legacy insert: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE login_flows_v2 SET credential_label = NULL WHERE browser_hash = 'b-new'`); err == nil {
		t.Error("credential_label accepted NULL")
	}
}
