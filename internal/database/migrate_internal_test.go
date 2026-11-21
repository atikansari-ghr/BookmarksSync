package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/api"
	"github.com/atikansari-ghr/BookmarksSync/internal/auth"
	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/floccus"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
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

type migrationReady struct{ db *sql.DB }

func (r migrationReady) Ping(ctx context.Context) error { return r.db.PingContext(ctx) }

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
		`INSERT INTO credentials(id, user_id, label, secret_hash, kind, share_id, mode, created_at) VALUES (1, 1, 'laptop', '0f007385b6f9d4b7eeb2748605afe1a984a0a3bfa3f014d09e2a784ce9e5cd1a', 'private', NULL, 'edit', 1), (2, 2, 'share', 'bb', 'share', 1, 'view', 1)`,
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
		`SELECT group_concat(secret_hash || ':' || kind, ',') FROM credentials ORDER BY id`:                    "0f007385b6f9d4b7eeb2748605afe1a984a0a3bfa3f014d09e2a784ce9e5cd1a:private,bb:share",
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

	// The retained milestone-2 Floccus secret still authenticates, and the new
	// account API sees the bookmark/placement/tags that existed before migration.
	ids := identity.New(db)
	if _, err := ids.Authenticate(ctx, "alice", "bms1.1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); err != nil {
		t.Fatalf("legacy Floccus credential after upgrade: %v", err)
	}
	authH, err := auth.New(auth.Options{Identity: ids, PublicOrigin: "https://bookmarks.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	apiH, err := api.New(api.Options{Auth: authH, Identity: ids, Bookmarks: bookmarks.New(db)})
	if err != nil {
		t.Fatal(err)
	}
	h := server.NewHandler(server.Options{Ready: migrationReady{db}, Register: func(r server.Registrar) { authH.Register(r); apiH.Register(r) }})
	_, sessionSecret, err := ids.CreateSession(ctx, 1, "migration fixture")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://bookmarks.example.test/api/v1/bookmarks", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionSecret})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var response struct {
		Items []struct {
			URL     string   `json:"url"`
			Title   string   `json:"title"`
			Tags    []string `json:"tags"`
			Folders []int64  `json:"folders"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK || len(response.Items) != 1 {
		t.Fatalf("API read of migrated bookmark: status=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
	}
	item := response.Items[0]
	if item.URL != "https://example.com/" || item.Title != "Example" || len(item.Tags) != 1 || item.Tags[0] != "work" || len(item.Folders) != 1 || item.Folders[0] != 2 {
		t.Fatalf("migrated API record = %+v", item)
	}
	// Protocol-level Floccus sync with the exact preexisting milestone-2 credential:
	// HTTP Basic with the bms1 secret whose SHA-256 is stored in fixture credential 1,
	// against the Nextcloud-Bookmarks-compatible endpoints stock floccus uses.
	floccusH := server.NewHandler(server.Options{Ready: migrationReady{db}, Register: floccus.New(ids, bookmarks.New(db), nil).Register})
	const legacySecret = "bms1.1.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	floccusDo := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, "https://bookmarks.example.test"+path, rd)
		req.SetBasicAuth("alice", legacySecret)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		floccusH.ServeHTTP(rec, req)
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: status %d, non-JSON body %q", method, path, rec.Code, rec.Body.String())
		}
		return rec.Code, out
	}
	const rest = "/index.php/apps/bookmarks/public/rest/v2"
	if code, body := floccusDo(http.MethodGet, "/ocs/v2.php/cloud/capabilities", ""); code != http.StatusOK ||
		!strings.Contains(fmt.Sprint(body), "hash-functions:[sha256]") {
		t.Fatalf("floccus capabilities with legacy credential: %d %v", code, body)
	}
	code, body := floccusDo(http.MethodGet, rest+"/bookmark?page=0&limit=10", "")
	list, _ := body["data"].([]any)
	if code != http.StatusOK || body["status"] != "success" || len(list) != 1 {
		t.Fatalf("floccus bookmark GET with legacy credential: %d %v", code, body)
	}
	wire, _ := list[0].(map[string]any)
	// Internal folder 2 is wire folder -3 (internal n <-> wire -(n+1)).
	if wire["url"] != "https://example.com/" || wire["title"] != "Example" || fmt.Sprint(wire["tags"]) != "[work]" || fmt.Sprint(wire["folders"]) != "[-3]" {
		t.Fatalf("floccus wire bookmark after upgrade = %v", wire)
	}
	// A sync round: take the lock, write into the existing folder, read the folder
	// hash, release the lock. The write must persist in the migrated database.
	if code, body := floccusDo(http.MethodPost, rest+"/lock", ""); code != http.StatusOK {
		t.Fatalf("floccus lock with legacy credential: %d %v", code, body)
	}
	if code, body := floccusDo(http.MethodPost, rest+"/bookmark", `{"url":"https://after-upgrade.example/","title":"After upgrade","folders":[-3]}`); code != http.StatusOK || body["status"] != "success" {
		t.Fatalf("floccus bookmark create with legacy credential: %d %v", code, body)
	}
	if code, body := floccusDo(http.MethodGet, rest+"/folder/-3/hash", ""); code != http.StatusOK || body["status"] != "success" || body["data"] == "" {
		t.Fatalf("floccus folder hash with legacy credential: %d %v", code, body)
	}
	if code, body := floccusDo(http.MethodDelete, rest+"/lock", ""); code != http.StatusOK {
		t.Fatalf("floccus unlock with legacy credential: %d %v", code, body)
	}
	var persisted int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bookmarks b JOIN placements p ON p.bookmark_id = b.id WHERE b.owner_id = 1 AND b.url = 'https://after-upgrade.example/' AND p.folder_id = 2`).Scan(&persisted); err != nil || persisted != 1 {
		t.Fatalf("floccus write after upgrade persisted %d rows (%v)", persisted, err)
	}
	// A wrong secret for the same credential id is still refused on the wire.
	req = httptest.NewRequest(http.MethodGet, "https://bookmarks.example.test"+rest+"/bookmark", nil)
	req.SetBasicAuth("alice", "bms1.1.BAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	rec = httptest.NewRecorder()
	floccusH.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret after upgrade: %d", rec.Code)
	}
}

// TestFavoritesMigrationUpgradesIdentityDataIntact applies migrations up to 0003, fills
// representative identity-milestone data, upgrades to the latest schema, and verifies the
// rows survive and the favorites relation enforces its keys.
func TestFavoritesMigrationUpgradesIdentityDataIntact(t *testing.T) {
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
	if len(ms) < 4 || ms[3].version != 4 {
		t.Fatalf("migration 0004 missing (have %d migrations)", len(ms))
	}
	if err := applyMigrations(ctx, db, ms[:3]); err != nil {
		t.Fatalf("identity schema: %v", err)
	}
	fixture := []string{
		`INSERT INTO users(id, username, password_hash, is_admin, active, created_at) VALUES (1, 'alice', 'ha', 1, 1, 1), (2, 'bob', 'hb', 0, 1, 2)`,
		`INSERT INTO folders(id, owner_id, kind, title, created_at, updated_at) VALUES (1, 1, 'bar', 'Bar', 1, 1), (2, 1, 'folder', 'Shared', 1, 1)`,
		`UPDATE folders SET parent_id = 1 WHERE id = 2`,
		`INSERT INTO bookmarks(id, owner_id, url, title, created_at, updated_at) VALUES (1, 1, 'https://example.com/', 'Example', 1, 1)`,
		`INSERT INTO bookmark_tags(bookmark_id, tag) VALUES (1, 'work')`,
		`INSERT INTO placements(id, bookmark_id, folder_id, position) VALUES (1, 1, 2, 0)`,
		`INSERT INTO shares(id, owner_id, folder_id, recipient_id, mode, created_at) VALUES (1, 1, 2, 2, 'view', 1)`,
		`INSERT INTO credentials(id, user_id, label, secret_hash, kind, share_id, mode, created_at) VALUES (1, 1, 'laptop', 'aa', 'private', NULL, 'edit', 1)`,
		`INSERT INTO api_tokens(id, user_id, secret_hash, scopes, created_at) VALUES (1, 1, 'tok', 'bookmarks:read', 1)`,
		`INSERT INTO user_settings(user_id, key, value, updated_at) VALUES (1, 'theme', '"ocean"', 1)`,
	}
	for _, q := range fixture {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("fixture %q: %v", q, err)
		}
	}
	if err := applyMigrations(ctx, db, ms); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	checks := map[string]string{
		`SELECT group_concat(username, ',') FROM users ORDER BY id`:                        "alice,bob",
		`SELECT url || ':' || title FROM bookmarks WHERE id = 1`:                           "https://example.com/:Example",
		`SELECT tag FROM bookmark_tags WHERE bookmark_id = 1`:                              "work",
		`SELECT folder_id FROM placements WHERE id = 1`:                                    "2",
		`SELECT recipient_id || ':' || mode FROM shares WHERE id = 1`:                      "2:view",
		`SELECT secret_hash || ':' || kind FROM credentials WHERE id = 1`:                  "aa:private",
		`SELECT secret_hash || ':' || scopes FROM api_tokens WHERE id = 1`:                 "tok:bookmarks:read",
		`SELECT value FROM user_settings WHERE user_id = 1 AND key = 'theme'`:              `"ocean"`,
		`SELECT CAST(MAX(version) AS TEXT) FROM schema_migrations`:                         "6",
		`SELECT CAST(COUNT(*) AS TEXT) FROM bookmark_favorites`:                            "0",
		`SELECT CAST(COUNT(*) AS TEXT) FROM pragma_foreign_key_list('bookmark_favorites')`: "2",
	}
	for q, want := range checks {
		var got string
		if err := db.QueryRow(q).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", q, got, err, want)
		}
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
	mustExec(`INSERT INTO bookmark_favorites(user_id, bookmark_id, created_at) VALUES (1, 1, 1), (2, 1, 1)`)
	mustFail("duplicate favorite", `INSERT INTO bookmark_favorites(user_id, bookmark_id, created_at) VALUES (2, 1, 2)`)
	mustFail("favorite for unknown user", `INSERT INTO bookmark_favorites(user_id, bookmark_id, created_at) VALUES (99, 1, 1)`)
	mustFail("favorite for unknown bookmark", `INSERT INTO bookmark_favorites(user_id, bookmark_id, created_at) VALUES (1, 99, 1)`)
	// Purging a bookmark removes its favorites.
	mustExec(`DELETE FROM bookmarks WHERE id = 1`)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM bookmark_favorites`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("favorites after bookmark delete = %d (%v)", n, err)
	}
}

// TestLinkCheckMigrationUpgradesFavoritesDataIntact applies migrations up to 0004 with
// representative data, upgrades to the latest schema, and verifies the data survives and
// the link-check relations enforce their keys and status vocabulary.
func TestLinkCheckMigrationUpgradesFavoritesDataIntact(t *testing.T) {
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
	if len(ms) < 5 || ms[4].version != 5 {
		t.Fatalf("migration 0005 missing (have %d migrations)", len(ms))
	}
	if err := applyMigrations(ctx, db, ms[:4]); err != nil {
		t.Fatalf("favorites schema: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO users(id, username, password_hash, is_admin, active, created_at) VALUES (1, 'alice', 'ha', 1, 1, 1), (2, 'bob', 'hb', 0, 1, 2)`,
		`INSERT INTO folders(id, owner_id, kind, title, created_at, updated_at) VALUES (1, 1, 'bar', 'Bar', 1, 1)`,
		`INSERT INTO bookmarks(id, owner_id, url, title, created_at, updated_at) VALUES (1, 1, 'https://example.com/', 'Example', 1, 1)`,
		`INSERT INTO placements(id, bookmark_id, folder_id, position) VALUES (1, 1, 1, 0)`,
		`INSERT INTO bookmark_favorites(user_id, bookmark_id, created_at) VALUES (1, 1, 1)`,
		`INSERT INTO user_settings(user_id, key, value, updated_at) VALUES (1, 'bookmarks.view', '"grid"', 1)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("fixture %q: %v", q, err)
		}
	}
	if err := applyMigrations(ctx, db, ms); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	checks := map[string]string{
		`SELECT CAST(MAX(version) AS TEXT) FROM schema_migrations`:                         "6",
		`SELECT url FROM bookmarks WHERE id = 1`:                                           "https://example.com/",
		`SELECT CAST(COUNT(*) AS TEXT) FROM bookmark_favorites`:                            "1",
		`SELECT value FROM user_settings WHERE user_id = 1 AND key = 'bookmarks.view'`:     `"grid"`,
		`SELECT CAST(COUNT(*) AS TEXT) FROM link_check_jobs`:                               "0",
		`SELECT CAST(COUNT(*) AS TEXT) FROM link_check_results`:                            "0",
		`SELECT CAST(COUNT(*) AS TEXT) FROM pragma_foreign_key_list('link_check_jobs')`:    "2",
		`SELECT CAST(COUNT(*) AS TEXT) FROM pragma_foreign_key_list('link_check_results')`: "1",
	}
	for q, want := range checks {
		var got string
		if err := db.QueryRow(q).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", q, got, err, want)
		}
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
	mustExec(`INSERT INTO link_check_jobs(id, user_id, owner_id, share_folder_id, status, created_at) VALUES (1, 2, 1, 0, 'queued', 1)`)
	mustFail("unknown job status", `INSERT INTO link_check_jobs(user_id, owner_id, share_folder_id, status, created_at) VALUES (1, 1, 0, 'weird', 1)`)
	mustFail("job for unknown user", `INSERT INTO link_check_jobs(user_id, owner_id, share_folder_id, status, created_at) VALUES (99, 1, 0, 'queued', 1)`)
	mustExec(`INSERT INTO link_check_results(job_id, position, bookmark_id, url, status) VALUES (1, 0, 1, 'https://example.com/', 'pending')`)
	mustFail("duplicate position", `INSERT INTO link_check_results(job_id, position, bookmark_id, url, status) VALUES (1, 0, 1, 'https://example.com/', 'pending')`)
	mustFail("unknown result status", `INSERT INTO link_check_results(job_id, position, bookmark_id, url, status) VALUES (1, 1, 1, 'https://example.com/', 'fine')`)
	mustFail("result for unknown job", `INSERT INTO link_check_results(job_id, position, bookmark_id, url, status) VALUES (9, 0, 1, 'https://example.com/', 'pending')`)
	// Purging a bookmark must not be blocked by, or silently alter, finished link checks.
	mustExec(`DELETE FROM placements WHERE id = 1`)
	mustExec(`DELETE FROM bookmarks WHERE id = 1`)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM link_check_results`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("results after bookmark purge = %d (%v)", n, err)
	}
	// Removing a job removes its results.
	mustExec(`DELETE FROM link_check_jobs WHERE id = 1`)
	if err := db.QueryRow(`SELECT COUNT(*) FROM link_check_results`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("results after job delete = %d (%v)", n, err)
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
