package identity_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func boolp(b bool) *bool { return &b }

func TestListUsersAndGetByID(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	list, err := e.svc.ListUsers(ctx)
	if err != nil || len(list) != 2 || list[0].Username != "alice" || list[1].Username != "bob" || list[0].CreatedAt.IsZero() {
		t.Fatalf("users = %+v, %v", list, err)
	}
	u, err := e.svc.GetUserByID(ctx, e.bob.ID)
	if err != nil || u.Username != "bob" || !u.Active {
		t.Fatalf("by id = %+v, %v", u, err)
	}
	if _, err := e.svc.GetUserByID(ctx, 9999); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestUpdateUserProtectsTheLastActiveAdmin(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	root, err := e.svc.CreateUser(ctx, "root", goodPassword, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UpdateUser(ctx, root.ID, boolp(false), nil); !errors.Is(err, identity.ErrLastAdmin) {
		t.Fatalf("deactivating the last admin: %v", err)
	}
	if _, err := e.svc.UpdateUser(ctx, root.ID, nil, boolp(false)); !errors.Is(err, identity.ErrLastAdmin) {
		t.Fatalf("demoting the last admin: %v", err)
	}
	if u, _ := e.svc.GetUserByID(ctx, root.ID); !u.Active || !u.IsAdmin {
		t.Fatal("refused change was applied")
	}
	// Promote alice; now root may step down.
	if u, err := e.svc.UpdateUser(ctx, e.alice.ID, nil, boolp(true)); err != nil || !u.IsAdmin {
		t.Fatalf("promote = %+v, %v", u, err)
	}
	if u, err := e.svc.UpdateUser(ctx, root.ID, nil, boolp(false)); err != nil || u.IsAdmin {
		t.Fatalf("demote with another admin = %+v, %v", u, err)
	}
	// An inactive admin does not count.
	if _, err := e.svc.UpdateUser(ctx, root.ID, nil, boolp(true)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UpdateUser(ctx, root.ID, boolp(false), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.UpdateUser(ctx, e.alice.ID, nil, boolp(false)); !errors.Is(err, identity.ErrLastAdmin) {
		t.Fatalf("demoting the last *active* admin: %v", err)
	}
	if _, err := e.svc.UpdateUser(ctx, 9999, boolp(true), nil); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
}

func TestDeactivationThroughUpdateUserRevokesEverything(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	_, sessSecret, _ := e.svc.CreateSession(ctx, e.bob.ID, "s")
	_, tokSecret, _ := e.svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: e.bob.ID, Scopes: []identity.Scope{identity.ScopeBookmarksRead}})
	cred, credSecret, _ := e.svc.CreateCredential(ctx, identity.NewCredential{UserID: e.bob.ID, Mode: identity.ModeEdit})
	if _, err := e.svc.UpdateUser(ctx, e.bob.ID, boolp(false), nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.ResolveSession(ctx, sessSecret); err == nil {
		t.Fatal("session survived deactivation")
	}
	if _, err := e.svc.ResolveAPIToken(ctx, tokSecret); err == nil {
		t.Fatal("token survived deactivation")
	}
	e.svc.UpdateUser(ctx, e.bob.ID, boolp(true), nil)
	if _, err := e.svc.Authenticate(ctx, "bob", credSecret); err == nil {
		t.Fatalf("credential %d revived by reactivation", cred.ID)
	}
}

func TestConcurrentDemotionsLeaveAnAdmin(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	e.svc.UpdateUser(ctx, e.alice.ID, nil, boolp(true))
	e.svc.UpdateUser(ctx, e.bob.ID, nil, boolp(true))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, id := range []int64{e.alice.ID, e.bob.ID} {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			<-start
			e.svc.UpdateUser(ctx, id, nil, boolp(false))
		}(id)
	}
	close(start)
	wg.Wait()
	var admins int
	e.db.SQL().QueryRow(`SELECT count(*) FROM users WHERE is_admin = 1 AND active = 1`).Scan(&admins)
	if admins != 1 {
		t.Fatalf("%d active admins after concurrent demotions, want 1", admins)
	}
}

func TestChangePasswordRotatesAndRevokes(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	acting, actingSecret, _ := e.svc.CreateSession(ctx, e.alice.ID, "acting")
	_, other, _ := e.svc.CreateSession(ctx, e.alice.ID, "other")
	_, tok, _ := e.svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: e.alice.ID, Scopes: []identity.Scope{identity.ScopeBookmarksRead}})
	_, credSecret, _ := e.svc.CreateCredential(ctx, identity.NewCredential{UserID: e.alice.ID, Mode: identity.ModeEdit})
	if _, err := e.svc.ChangePassword(ctx, e.alice.ID, acting.ID, "short"); !errors.Is(err, identity.ErrInvalid) {
		t.Fatalf("weak password: %v", err)
	}
	rot, err := e.svc.ChangePassword(ctx, e.alice.ID, acting.ID, "a brand new long password")
	if err != nil || rot.Secret == "" || rot.Session.ID != acting.ID {
		t.Fatalf("change = %+v, %v", rot, err)
	}
	if _, err := e.svc.VerifyPassword(ctx, "alice", goodPassword); err == nil {
		t.Fatal("old password still works")
	}
	if _, err := e.svc.VerifyPassword(ctx, "alice", "a brand new long password"); err != nil {
		t.Fatal("new password rejected")
	}
	for name, secret := range map[string]string{"acting (pre-rotation)": actingSecret, "other": other} {
		if _, _, err := e.svc.ResolveSession(ctx, secret); err == nil {
			t.Fatalf("%s session survived", name)
		}
	}
	if _, _, err := e.svc.ResolveSession(ctx, rot.Secret); err != nil {
		t.Fatal("rotated session dead")
	}
	if _, err := e.svc.ResolveAPIToken(ctx, tok); err == nil {
		t.Fatal("API token survived the password change")
	}
	if _, err := e.svc.Authenticate(ctx, "alice", credSecret); err != nil {
		t.Fatal("floccus credential was revoked by a password change")
	}
}

func TestAdminResetsRevokeSessionsAndTokensButNotConnections(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	_, sess, _ := e.svc.CreateSession(ctx, e.bob.ID, "s")
	_, tok, _ := e.svc.CreateAPIToken(ctx, identity.NewAPIToken{UserID: e.bob.ID, Scopes: []identity.Scope{identity.ScopeBookmarksRead}})
	_, credSecret, _ := e.svc.CreateCredential(ctx, identity.NewCredential{UserID: e.bob.ID, Mode: identity.ModeEdit})
	if err := e.svc.ResetPassword(ctx, e.bob.ID, "an admin chosen password"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.ResolveSession(ctx, sess); err == nil {
		t.Fatal("session survived the reset")
	}
	if _, err := e.svc.ResolveAPIToken(ctx, tok); err == nil {
		t.Fatal("token survived the reset")
	}
	if _, err := e.svc.Authenticate(ctx, "bob", credSecret); err != nil {
		t.Fatal("floccus credential revoked by an admin password reset")
	}
	if _, err := e.svc.VerifyPassword(ctx, "bob", "an admin chosen password"); err != nil {
		t.Fatal("reset password rejected")
	}
	if err := e.svc.ResetPassword(ctx, 9999, "an admin chosen password"); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	// Factor reset.
	e.enrollTOTP(t, e.bob)
	bs, _, _ := e.svc.CreateSession(ctx, e.bob.ID, "s2")
	if _, _, err := e.svc.LinkOIDCIdentity(ctx, identity.OIDCLinkRequest{UserID: e.bob.ID, SessionID: bs.ID, Provider: "google", Issuer: "https://i.test", Subject: "s"}); err != nil {
		t.Fatal(err)
	}
	_, sess2, _ := e.svc.CreateSession(ctx, e.bob.ID, "s3")
	if err := e.svc.ResetFactors(ctx, e.bob.ID, identity.FactorReset{TOTP: true}); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.svc.TOTPStatus(ctx, e.bob.ID); st.Enabled || st.RecoveryCodesRemaining != 0 {
		t.Fatalf("TOTP not reset: %+v", st)
	}
	if ids, _ := e.svc.ListOIDCIdentities(ctx, e.bob.ID); len(ids) != 1 {
		t.Fatal("OIDC link removed although not selected")
	}
	if _, _, err := e.svc.ResolveSession(ctx, sess2); err == nil {
		t.Fatal("session survived the factor reset")
	}
	if err := e.svc.ResetFactors(ctx, e.bob.ID, identity.FactorReset{OIDC: true, Passkeys: true}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := e.svc.ListOIDCIdentities(ctx, e.bob.ID); len(ids) != 0 {
		t.Fatal("OIDC link survived")
	}
	if err := e.svc.ResetFactors(ctx, e.bob.ID, identity.FactorReset{}); !errors.Is(err, identity.ErrInvalid) {
		t.Fatalf("empty factor reset: %v", err)
	}
}

func TestRevokeUserCredentialIsScoped(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	cred, secret, _ := e.svc.CreateCredential(ctx, identity.NewCredential{UserID: e.alice.ID, Mode: identity.ModeEdit})
	if err := e.svc.RevokeUserCredential(ctx, e.bob.ID, cred.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("bob revoked alice's credential: %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, "alice", secret); err != nil {
		t.Fatal("credential affected by a refused revoke")
	}
	if err := e.svc.RevokeUserCredential(ctx, e.alice.ID, cred.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, "alice", secret); err == nil {
		t.Fatal("revoked credential still authenticates")
	}
}

func TestUserSettingsWhitelist(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	s := func(v string) *string { return &v }
	got, err := e.svc.UpdateSettings(ctx, e.alice.ID, map[string]*string{"bookmarks.view": s("grid"), "bookmarks.sort": s("title"), "ui.timezone": s("Asia/Riyadh")})
	if err != nil || got["bookmarks.view"] != "grid" || got["bookmarks.sort"] != "title" || got["ui.timezone"] != "Asia/Riyadh" {
		t.Fatalf("update = %v, %v", got, err)
	}
	for name, ch := range map[string]map[string]*string{
		"unknown key":      {"theme": s("dark")},
		"bad view":         {"bookmarks.view": s("table")},
		"bad sort":         {"bookmarks.sort": s("random")},
		"bad timezone":     {"ui.timezone": s("Mars/Olympus")},
		"local timezone":   {"ui.timezone": s("Local")},
		"long timezone":    {"ui.timezone": s(strings.Repeat("A", 65))},
		"path-ish zone":    {"ui.timezone": s("../../etc/passwd")},
		"empty change set": {},
	} {
		if _, err := e.svc.UpdateSettings(ctx, e.alice.ID, ch); !errors.Is(err, identity.ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// A refused batch changes nothing; null removes a key; users are isolated.
	if got, _ := e.svc.Settings(ctx, e.alice.ID); got["bookmarks.view"] != "grid" {
		t.Fatalf("refused batch changed settings: %v", got)
	}
	got, _ = e.svc.UpdateSettings(ctx, e.alice.ID, map[string]*string{"bookmarks.view": nil})
	if _, ok := got["bookmarks.view"]; ok {
		t.Fatal("null did not remove the key")
	}
	if bob, _ := e.svc.Settings(ctx, e.bob.ID); len(bob) != 0 {
		t.Fatalf("bob sees settings: %v", bob)
	}
}
