package identity_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

const goodPassword = "correct horse battery staple"

func open(t *testing.T) (*database.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "id.db")
	db, err := database.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func newSvc(db *database.DB) *identity.Service {
	// Cheap Argon2id cost keeps the suite fast; production uses the defaults.
	return identity.New(db.SQL(), identity.WithPasswordCost(8*1024, 1, 1))
}

func TestCreateUserPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	db, path := open(t)
	svc := newSvc(db)
	u, err := svc.CreateUser(ctx, "alice", goodPassword, true)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == 0 || u.Username != "alice" || !u.IsAdmin || !u.Active {
		t.Fatalf("unexpected user %+v", u)
	}
	db.Close()

	db2, err := database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	got, err := newSvc(db2).GetUser(ctx, "ALICE") // case-insensitive
	if err != nil {
		t.Fatalf("GetUser after reopen: %v", err)
	}
	if got.ID != u.ID || !got.IsAdmin {
		t.Errorf("got %+v want id %d admin", got, u.ID)
	}
}

func TestPasswordIsStoredAsSaltedArgon2id(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := newSvc(db)
	if _, err := svc.CreateUser(ctx, "alice", goodPassword, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateUser(ctx, "bob", goodPassword, false); err != nil {
		t.Fatal(err)
	}
	rows, err := db.SQL().QueryContext(ctx, `SELECT password_hash FROM users ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var hashes []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, h)
	}
	if len(hashes) != 2 {
		t.Fatalf("got %d hashes", len(hashes))
	}
	for _, h := range hashes {
		if !strings.HasPrefix(h, "$argon2id$v=19$m=") {
			t.Errorf("hash %q is not a PHC argon2id string", h)
		}
		if strings.Contains(h, goodPassword) {
			t.Error("hash contains the plaintext password")
		}
	}
	if hashes[0] == hashes[1] {
		t.Error("identical passwords produced identical hashes: salt is not random")
	}
}

func TestDefaultPasswordCostMeetsArgon2idGuidance(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := identity.New(db.SQL()) // production defaults
	if _, err := svc.CreateUser(ctx, "alice", goodPassword, false); err != nil {
		t.Fatal(err)
	}
	var h string
	if err := db.SQL().QueryRowContext(ctx, `SELECT password_hash FROM users`).Scan(&h); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h, "m=65536,t=3,p=") {
		t.Errorf("default params weaker than 64MiB/3 iterations: %q", h)
	}
}

func TestCreateUserValidation(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := newSvc(db)
	if _, err := svc.CreateUser(ctx, "alice", goodPassword, false); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, user, pass string
		want             error
	}{
		{"duplicate", "alice", goodPassword, identity.ErrExists},
		{"duplicate different case", "ALICE", goodPassword, identity.ErrExists},
		{"empty username", "", goodPassword, identity.ErrInvalid},
		{"colon breaks basic auth", "a:b", goodPassword, identity.ErrInvalid},
		{"control character", "a\nb", goodPassword, identity.ErrInvalid},
		{"surrounding space", " bob", goodPassword, identity.ErrInvalid},
		{"too long", strings.Repeat("a", 65), goodPassword, identity.ErrInvalid},
		{"short password", "bob", "short", identity.ErrInvalid},
		{"huge password", "bob", strings.Repeat("x", 5000), identity.ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := svc.CreateUser(ctx, c.user, c.pass, false); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestVerifyPassword(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := newSvc(db)
	if _, err := svc.CreateUser(ctx, "alice", goodPassword, false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyPassword(ctx, "alice", goodPassword); err != nil {
		t.Errorf("good password rejected: %v", err)
	}
	for name, c := range map[string][2]string{
		"wrong password": {"alice", "wrong password entirely"},
		"unknown user":   {"nobody", goodPassword},
		"empty":          {"alice", ""},
	} {
		if _, err := svc.VerifyPassword(ctx, c[0], c[1]); !errors.Is(err, identity.ErrUnauthorized) {
			t.Errorf("%s: err = %v, want ErrUnauthorized", name, err)
		}
	}
	if err := svc.SetActive(ctx, "alice", false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyPassword(ctx, "alice", goodPassword); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("inactive user verified: %v", err)
	}
}

func TestSetActiveUnknownUser(t *testing.T) {
	db, _ := open(t)
	if err := newSvc(db).SetActive(context.Background(), "ghost", false); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func mkUserCred(t *testing.T, svc *identity.Service, mode identity.Mode) (identity.User, identity.Credential, string) {
	t.Helper()
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, "alice", goodPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	c, secret, err := svc.CreateCredential(ctx, identity.NewCredential{UserID: u.ID, Label: "laptop", Mode: mode})
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	return u, c, secret
}

func TestCredentialSecretHas256BitsAndOnlyItsHashIsStored(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := newSvc(db)
	_, c, secret := mkUserCred(t, svc, identity.ModeEdit)
	_, c2, secret2 := mkUserCred2(t, svc)
	if secret == secret2 {
		t.Fatal("two credentials share a secret")
	}
	// secret = bms1.<id>.<base64url(>=32 random bytes)>
	parts := strings.Split(secret, ".")
	if len(parts) != 3 || parts[0] != "bms1" {
		t.Fatalf("unexpected secret shape")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(raw) < 32 {
		t.Fatalf("random part decodes to %d bytes (err %v), want >=32", len(raw), err)
	}
	if c.Kind != identity.KindPrivate || c.Mode != identity.ModeEdit || c.ID == c2.ID {
		t.Errorf("unexpected credential %+v", c)
	}
	// Search every column of every table for the secret: nothing may contain it.
	for _, tbl := range []string{"users", "credentials"} {
		rows, err := db.SQL().QueryContext(ctx, `SELECT * FROM `+tbl)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				if s, ok := v.(string); ok && (strings.Contains(s, parts[2]) || s == secret) {
					t.Errorf("%s.%s contains the plaintext secret", tbl, cols[i])
				}
			}
		}
		rows.Close()
	}
	var hash string
	if err := db.SQL().QueryRowContext(ctx, `SELECT secret_hash FROM credentials WHERE id=?`, c.ID).Scan(&hash); err != nil || len(hash) != 64 {
		t.Errorf("secret_hash = %q (err %v), want 64 hex chars", hash, err)
	}
}

func mkUserCred2(t *testing.T, svc *identity.Service) (identity.User, identity.Credential, string) {
	t.Helper()
	ctx := context.Background()
	u, err := svc.CreateUser(ctx, "bob", goodPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	c, s, err := svc.CreateCredential(ctx, identity.NewCredential{UserID: u.ID, Label: "phone", Mode: identity.ModeView})
	if err != nil {
		t.Fatal(err)
	}
	return u, c, s
}

func TestAuthenticate(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := newSvc(db)
	u, c, secret := mkUserCred(t, svc, identity.ModeView)

	p, err := svc.Authenticate(ctx, "alice", secret)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.CredentialID != c.ID || p.User.ID != u.ID || p.Mode != identity.ModeView || p.Kind != identity.KindPrivate {
		t.Errorf("principal = %+v", p)
	}
	if _, err := svc.Authenticate(ctx, "ALICE", secret); err != nil {
		t.Errorf("username should be case-insensitive: %v", err)
	}

	tampered := secret[:len(secret)-1] + flip(secret[len(secret)-1])
	for name, c := range map[string][2]string{
		"wrong secret":            {"alice", tampered},
		"wrong user":              {"bob", secret},
		"website password":        {"alice", goodPassword},
		"empty secret":            {"alice", ""},
		"malformed secret":        {"alice", "bms1.x.y"},
		"unknown credential id":   {"alice", "bms1.99999." + strings.Split(secret, ".")[2]},
		"truncated":               {"alice", secret[:20]},
		"empty username":          {"", secret},
		"secret with extra parts": {"alice", secret + ".x"},
	} {
		if _, err := svc.Authenticate(ctx, c[0], c[1]); !errors.Is(err, identity.ErrUnauthorized) {
			t.Errorf("%s: err = %v, want ErrUnauthorized", name, err)
		}
	}
}

func flip(b byte) string {
	if b == 'A' {
		return "B"
	}
	return "A"
}

func TestRevokedAndInactiveCredentialsFailImmediately(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := newSvc(db)
	_, c, secret := mkUserCred(t, svc, identity.ModeEdit)
	if _, err := svc.Authenticate(ctx, "alice", secret); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetActive(ctx, "alice", false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, "alice", secret); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("inactive user authenticated: %v", err)
	}
	if err := svc.SetActive(ctx, "alice", true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, "alice", secret); err != nil {
		t.Errorf("reactivated user rejected: %v", err)
	}
	if err := svc.RevokeCredential(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, "alice", secret); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("revoked credential authenticated: %v", err)
	}
	if err := svc.RevokeCredential(ctx, 424242); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("revoke unknown = %v, want ErrNotFound", err)
	}
	if err := svc.RevokeCredential(ctx, c.ID); err != nil {
		t.Errorf("revoking twice should be idempotent: %v", err)
	}
}

func TestCredentialValidation(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := newSvc(db)
	u, err := svc.CreateUser(ctx, "alice", goodPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	bad := map[string]identity.NewCredential{
		"unknown user":          {UserID: 999, Mode: identity.ModeEdit},
		"bad mode":              {UserID: u.ID, Mode: "write"},
		"share without share":   {UserID: u.ID, Kind: identity.KindShare, Mode: identity.ModeView},
		"private with share id": {UserID: u.ID, Kind: identity.KindPrivate, ShareID: 5, Mode: identity.ModeView},
		"missing share row":     {UserID: u.ID, Kind: identity.KindShare, ShareID: 5, Mode: identity.ModeView},
		"label too long":        {UserID: u.ID, Label: strings.Repeat("x", 201), Mode: identity.ModeView},
	}
	for name, nc := range bad {
		if _, _, err := svc.CreateCredential(ctx, nc); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	inactive, _ := svc.CreateUser(ctx, "carol", goodPassword, false)
	if err := svc.SetActive(ctx, "carol", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateCredential(ctx, identity.NewCredential{UserID: inactive.ID, Mode: identity.ModeEdit}); err == nil {
		t.Error("credential for inactive user accepted")
	}
}

func TestLastUsedIsRecordedWithoutExtraWritesPerRequest(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	svc := identity.New(db.SQL(), identity.WithPasswordCost(8*1024, 1, 1), identity.WithClock(func() time.Time { return now }))
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	c, secret, _ := svc.CreateCredential(ctx, identity.NewCredential{UserID: u.ID, Mode: identity.ModeEdit})
	if _, err := svc.Authenticate(ctx, "alice", secret); err != nil {
		t.Fatal(err)
	}
	lastUsed := func() sql.NullInt64 {
		var v sql.NullInt64
		if err := db.SQL().QueryRowContext(ctx, `SELECT last_used_at FROM credentials WHERE id=?`, c.ID).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	first := lastUsed()
	if !first.Valid || first.Int64 != now.UnixMilli() {
		t.Fatalf("last_used_at = %v, want %d", first, now.UnixMilli())
	}
	now = now.Add(10 * time.Second)
	_, _ = svc.Authenticate(ctx, "alice", secret)
	if lastUsed() != first {
		t.Error("last_used_at rewritten within the throttle window")
	}
	now = now.Add(2 * time.Minute)
	_, _ = svc.Authenticate(ctx, "alice", secret)
	if lastUsed().Int64 != now.UnixMilli() {
		t.Error("last_used_at not refreshed after the throttle window")
	}
}

func TestListCredentialsShowsMetadataButNeverSecrets(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := newSvc(db)
	u, c1, secret := mkUserCred(t, svc, identity.ModeEdit)
	c2, _, err := svc.CreateCredential(ctx, identity.NewCredential{UserID: u.ID, Label: "view only", Mode: identity.ModeView})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeCredential(ctx, c2.ID); err != nil {
		t.Fatal(err)
	}
	list, err := svc.ListCredentials(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListCredentials: %v", err)
	}
	if len(list) != 2 || list[0].ID != c1.ID || list[1].ID != c2.ID {
		t.Fatalf("list = %+v", list)
	}
	if list[0].Revoked || !list[1].Revoked || list[1].Mode != identity.ModeView || list[0].Label != "laptop" {
		t.Errorf("metadata wrong: %+v", list)
	}
	if strings.Contains(fmt.Sprintf("%+v", list), strings.Split(secret, ".")[2]) {
		t.Error("listing contains the secret")
	}
}
