package identity_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func TestTokenScopesAreExactlyTheFourDocumentedScopes(t *testing.T) {
	want := []identity.Scope{"bookmarks:read", "bookmarks:write", "shares:read", "shares:manage"}
	got := identity.TokenScopes()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("TokenScopes = %v, want %v", got, want)
	}
	if identity.ScopeBookmarksRead != want[0] || identity.ScopeBookmarksWrite != want[1] ||
		identity.ScopeSharesRead != want[2] || identity.ScopeSharesManage != want[3] {
		t.Error("scope constants do not match documented names")
	}
}

func TestAPITokenSecretIsOpaqueAndShownOnce(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	tok, secret, err := svc.CreateAPIToken(ctx, identity.NewAPIToken{
		UserID: u.ID, Label: "backup script", Scopes: []identity.Scope{identity.ScopeBookmarksRead},
	})
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	body, ok := strings.CutPrefix(secret, "bmt1_")
	if !ok {
		t.Fatalf("token %q lacks the bmt1_ prefix", secret)
	}
	if raw, err := base64.RawURLEncoding.DecodeString(body); err != nil || len(raw) != 32 {
		t.Fatalf("token body is not 32 random bytes in base64url: %v", err)
	}
	_, secret2, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: u.ID, Scopes: []identity.Scope{identity.ScopeBookmarksRead}})
	if secret2 == secret {
		t.Fatal("duplicate token secrets")
	}

	var stored string
	if err := db.SQL().QueryRow(`SELECT secret_hash FROM api_tokens WHERE id = ?`, tok.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != sha256Hex(secret) {
		t.Errorf("stored hash %q, want SHA-256 hex of token", stored)
	}
	if all := rowText(t, db, "api_tokens", tok.ID); strings.Contains(all, body) {
		t.Errorf("plaintext token persisted: %s", all)
	}
	// After creation, no read path returns the secret or its hash again.
	list, err := svc.ListAPITokens(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != tok.ID || list[0].Label != "backup script" {
		t.Fatalf("ListAPITokens = %+v", list)
	}
	dump := fmt.Sprintf("%+v", list)
	if strings.Contains(dump, body) || strings.Contains(dump, stored) {
		t.Error("token listing exposes secret or hash")
	}
	p, err := svc.ResolveAPIToken(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%+v", p), body) {
		t.Error("resolved principal exposes secret")
	}
}

func TestResolveAPIToken(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, true)
	tok, secret, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{
		UserID: u.ID, Label: "ci", Scopes: []identity.Scope{identity.ScopeSharesRead, identity.ScopeBookmarksWrite},
	})
	p, err := svc.ResolveAPIToken(ctx, secret)
	if err != nil {
		t.Fatalf("ResolveAPIToken: %v", err)
	}
	if p.Token.ID != tok.ID || p.User.ID != u.ID || p.User.Username != "alice" || !p.User.Active {
		t.Errorf("principal = %+v", p)
	}
	if fmt.Sprint(p.Token.Scopes) != "[bookmarks:write shares:read]" {
		t.Errorf("scopes = %v, want canonical order", p.Token.Scopes)
	}
	for _, bad := range []string{"", "bmt1_", secret + "A", strings.TrimPrefix(secret, "bmt1_"), "bmt1_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))} {
		if _, err := svc.ResolveAPIToken(ctx, bad); !errors.Is(err, identity.ErrUnauthorized) {
			t.Errorf("ResolveAPIToken(%q) err = %v", bad, err)
		}
	}
}

func TestAPITokenScopesAreExactAndNeverAdmin(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	admin, _ := svc.CreateUser(ctx, "root", goodPassword, true)
	_, secret, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{
		UserID: admin.ID, Scopes: []identity.Scope{identity.ScopeBookmarksWrite, identity.ScopeSharesManage},
	})
	p, err := svc.ResolveAPIToken(ctx, secret)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[identity.Scope]bool{
		identity.ScopeBookmarksWrite: true,
		identity.ScopeSharesManage:   true,
		identity.ScopeBookmarksRead:  false, // write does not imply read
		identity.ScopeSharesRead:     false, // manage does not imply read
		"admin":                      false,
		"":                           false,
	}
	for scope, want := range cases {
		if got := p.Allows(scope); got != want {
			t.Errorf("Allows(%q) = %v, want %v", scope, got, want)
		}
	}
	if p.AuthorizesAdmin() {
		t.Error("a personal API token of an admin user authorizes admin endpoints")
	}
}

func TestCreateAPITokenValidation(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	c := newClock()
	svc := clockSvc(db, c)
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	read := []identity.Scope{identity.ScopeBookmarksRead}
	bad := map[string]identity.NewAPIToken{
		"no scopes":        {UserID: u.ID},
		"unknown scope":    {UserID: u.ID, Scopes: []identity.Scope{"admin"}},
		"empty scope":      {UserID: u.ID, Scopes: []identity.Scope{""}},
		"duplicate scope":  {UserID: u.ID, Scopes: []identity.Scope{identity.ScopeBookmarksRead, identity.ScopeBookmarksRead}},
		"label too long":   {UserID: u.ID, Scopes: read, Label: strings.Repeat("x", 201)},
		"invalid utf8":     {UserID: u.ID, Scopes: read, Label: "\xff"},
		"expiry in past":   {UserID: u.ID, Scopes: read, ExpiresAt: c.Now().Add(-time.Second)},
		"expiry right now": {UserID: u.ID, Scopes: read, ExpiresAt: c.Now()},
	}
	for name, nt := range bad {
		if _, _, err := svc.CreateAPIToken(ctx, nt); !errors.Is(err, identity.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if _, _, err := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: 9999, Scopes: read}); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("unknown user err = %v", err)
	}
	_ = svc.SetActive(ctx, "alice", false)
	if _, _, err := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: u.ID, Scopes: read}); !errors.Is(err, identity.ErrInvalid) {
		t.Errorf("inactive user err = %v", err)
	}
}

func TestAPITokenExpiry(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	c := newClock()
	svc := clockSvc(db, c)
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	read := []identity.Scope{identity.ScopeBookmarksRead}
	_, expiring, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: u.ID, Scopes: read, ExpiresAt: c.Now().Add(24 * time.Hour)})
	_, forever, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: u.ID, Scopes: read})

	c.Advance(24*time.Hour - time.Millisecond)
	if _, err := svc.ResolveAPIToken(ctx, expiring); err != nil {
		t.Fatalf("token rejected before expiry: %v", err)
	}
	c.Advance(time.Millisecond)
	if _, err := svc.ResolveAPIToken(ctx, expiring); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("token accepted at expiry: %v", err)
	}
	if _, err := svc.ResolveAPIToken(ctx, forever); err != nil {
		t.Fatalf("token without expiry rejected: %v", err)
	}
	list, _ := svc.ListAPITokens(ctx, u.ID)
	if len(list) != 2 || !list[0].Expired || list[1].Expired || !list[1].ExpiresAt.IsZero() {
		t.Errorf("listing expiry state = %+v", list)
	}
}

func TestRevokeAPITokenIsOwnerScoped(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	alice, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	bob, _ := svc.CreateUser(ctx, "bob", goodPassword, false)
	read := []identity.Scope{identity.ScopeBookmarksRead}
	t1, s1, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: alice.ID, Scopes: read})
	_, s2, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: alice.ID, Scopes: read})

	if err := svc.RevokeAPIToken(ctx, bob.ID, t1.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("cross-user revoke err = %v", err)
	}
	if _, err := svc.ResolveAPIToken(ctx, s1); err != nil {
		t.Fatalf("cross-user revoke took effect: %v", err)
	}
	if err := svc.RevokeAPIToken(ctx, alice.ID, t1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveAPIToken(ctx, s1); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("revoked token valid: %v", err)
	}
	if _, err := svc.ResolveAPIToken(ctx, s2); err != nil {
		t.Errorf("sibling token revoked: %v", err)
	}
	if err := svc.RevokeAPIToken(ctx, alice.ID, t1.ID); err != nil {
		t.Errorf("second revoke not a no-op: %v", err)
	}
	list, _ := svc.ListAPITokens(ctx, alice.ID)
	if len(list) != 2 || !list[0].Revoked || list[1].Revoked {
		t.Errorf("listing revoke state = %+v", list)
	}
	if bl, _ := svc.ListAPITokens(ctx, bob.ID); len(bl) != 0 {
		t.Errorf("bob sees tokens: %+v", bl)
	}
}

func TestRevokeUserAPITokens(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	alice, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	bob, _ := svc.CreateUser(ctx, "bob", goodPassword, false)
	read := []identity.Scope{identity.ScopeBookmarksRead}
	_, a1, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: alice.ID, Scopes: read})
	_, a2, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: alice.ID, Scopes: read})
	_, b1, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: bob.ID, Scopes: read})
	if err := svc.RevokeUserAPITokens(ctx, alice.ID); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{a1, a2} {
		if _, err := svc.ResolveAPIToken(ctx, s); !errors.Is(err, identity.ErrUnauthorized) {
			t.Errorf("token survived user-wide revoke: %v", err)
		}
	}
	if _, err := svc.ResolveAPIToken(ctx, b1); err != nil {
		t.Errorf("other user's token revoked: %v", err)
	}
}

func TestAPITokenLastUsedIsThrottled(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	c := newClock()
	svc := clockSvc(db, c)
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	tok, secret, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: u.ID, Scopes: []identity.Scope{identity.ScopeBookmarksRead}})
	first := c.Now()
	_, _ = svc.ResolveAPIToken(ctx, secret)
	c.Advance(10 * time.Second)
	_, _ = svc.ResolveAPIToken(ctx, secret)
	list, _ := svc.ListAPITokens(ctx, u.ID)
	if len(list) != 1 || list[0].ID != tok.ID || !list[0].LastUsedAt.Equal(first) {
		t.Errorf("last used = %+v, want %v", list, first)
	}
}

func TestDeactivationRevokesSessionsAndAPITokensWithCredentials(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	alice, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	bob, _ := svc.CreateUser(ctx, "bob", goodPassword, false)
	read := []identity.Scope{identity.ScopeBookmarksRead}
	_, aSess, _ := svc.CreateSession(ctx, alice.ID, "")
	_, aTok, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: alice.ID, Scopes: read})
	_, bSess, _ := svc.CreateSession(ctx, bob.ID, "")
	_, bTok, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: bob.ID, Scopes: read})

	if err := svc.SetActive(ctx, "alice", false); err != nil {
		t.Fatal(err)
	}
	var liveSess, liveTok int
	_ = db.SQL().QueryRow(`SELECT COUNT(*) FROM web_sessions WHERE user_id = ? AND revoked_at IS NULL`, alice.ID).Scan(&liveSess)
	_ = db.SQL().QueryRow(`SELECT COUNT(*) FROM api_tokens WHERE user_id = ? AND revoked_at IS NULL`, alice.ID).Scan(&liveTok)
	if liveSess != 0 || liveTok != 0 {
		t.Fatalf("deactivation left %d sessions and %d tokens unrevoked", liveSess, liveTok)
	}
	// Reactivation does not resurrect anything.
	if err := svc.SetActive(ctx, "alice", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ResolveSession(ctx, aSess); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("session revived after reactivation: %v", err)
	}
	if _, err := svc.ResolveAPIToken(ctx, aTok); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("token revived after reactivation: %v", err)
	}
	if _, _, err := svc.ResolveSession(ctx, bSess); err != nil {
		t.Errorf("bob's session affected: %v", err)
	}
	if _, err := svc.ResolveAPIToken(ctx, bTok); err != nil {
		t.Errorf("bob's token affected: %v", err)
	}
}

func TestInactiveUserCannotResolveEvenUnrevokedSecrets(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	alice, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	_, sess, _ := svc.CreateSession(ctx, alice.ID, "")
	_, tok, _ := svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: alice.ID, Scopes: []identity.Scope{identity.ScopeBookmarksRead}})
	// Simulate a user disabled outside SetActive (e.g. direct admin repair).
	if _, err := db.SQL().Exec(`UPDATE users SET active = 0 WHERE id = ?`, alice.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ResolveSession(ctx, sess); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("inactive user's session resolved: %v", err)
	}
	if _, err := svc.ResolveAPIToken(ctx, tok); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("inactive user's token resolved: %v", err)
	}
}

func TestDeactivationIsAtomicWithCredentialRevocation(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	alice, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	_, _, _ = svc.CreateSession(ctx, alice.ID, "")
	if _, _, err := svc.CreateCredential(ctx, identity.NewCredential{UserID: alice.ID, Mode: identity.ModeEdit}); err != nil {
		t.Fatal(err)
	}
	// Make token revocation fail inside the transaction; nothing else may commit.
	if _, err := db.SQL().Exec(`CREATE TRIGGER fail_token_revoke BEFORE UPDATE ON api_tokens
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, _, _ = svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: alice.ID, Scopes: []identity.Scope{identity.ScopeBookmarksRead}})
	if err := svc.SetActive(ctx, "alice", false); err == nil {
		t.Fatal("SetActive succeeded despite injected failure")
	}
	var active, revokedCreds, revokedSess int
	_ = db.SQL().QueryRow(`SELECT active FROM users WHERE id = ?`, alice.ID).Scan(&active)
	_ = db.SQL().QueryRow(`SELECT COUNT(*) FROM credentials WHERE revoked_at IS NOT NULL`).Scan(&revokedCreds)
	_ = db.SQL().QueryRow(`SELECT COUNT(*) FROM web_sessions WHERE revoked_at IS NOT NULL`).Scan(&revokedSess)
	if active != 1 || revokedCreds != 0 || revokedSess != 0 {
		t.Errorf("partial deactivation committed: active=%d creds=%d sessions=%d", active, revokedCreds, revokedSess)
	}
}
