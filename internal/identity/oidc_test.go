package identity_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

const (
	issuerA = "https://accounts.example.test"
	issuerB = "https://other-issuer.example.test"
)

func (e *factorEnv) beginOIDC(t *testing.T, st identity.OIDCFlowStart) string {
	t.Helper()
	h, exp, err := e.svc.BeginOIDCFlow(context.Background(), st)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if !exp.Equal(e.clock.Now().Add(identity.OIDCFlowTTL)) || identity.OIDCFlowTTL != 10*time.Minute {
		t.Fatalf("expiry %v", exp)
	}
	return h
}

func loginStart(binding string) identity.OIDCFlowStart {
	return identity.OIDCFlowStart{Kind: identity.OIDCLogin, Provider: "google", Nonce: "nonce-value", Verifier: "verifier-value", Binding: binding}
}

func TestOIDCFlowIsHashedSingleUseBoundAndExpiring(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	h := e.beginOIDC(t, loginStart("binding-secret"))
	var stored, data string
	if err := e.db.SQL().QueryRow(`SELECT flow_hash, data FROM auth_flows WHERE kind = 'oidc_login'`).Scan(&stored, &data); err != nil {
		t.Fatal(err)
	}
	if stored != sha256Hex(h) || strings.Contains(data, h) || strings.Contains(data, "binding-secret") || len(h) < 43 {
		t.Fatalf("flow handle or binding stored in plaintext (data %s)", data)
	}
	// Wrong binding or provider: refused and not consumed.
	if _, err := e.svc.ConsumeOIDCFlow(ctx, h, "google", "other-binding", 0, 0); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("wrong binding: %v", err)
	}
	if _, err := e.svc.ConsumeOIDCFlow(ctx, h, "authentik", "binding-secret", 0, 0); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("wrong provider: %v", err)
	}
	if _, err := e.svc.ConsumeOIDCFlow(ctx, "", "google", "binding-secret", 0, 0); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("empty handle: %v", err)
	}
	f, err := e.svc.ConsumeOIDCFlow(ctx, h, "google", "binding-secret", 0, 0)
	if err != nil || f.Kind != identity.OIDCLogin || f.Provider != "google" || f.Nonce != "nonce-value" || f.Verifier != "verifier-value" || f.UserID != 0 {
		t.Fatalf("consume = %+v, %v", f, err)
	}
	if _, err := e.svc.ConsumeOIDCFlow(ctx, h, "google", "binding-secret", 0, 0); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("flow reused: %v", err)
	}
	h2 := e.beginOIDC(t, loginStart("b2"))
	e.clock.Advance(identity.OIDCFlowTTL)
	if _, err := e.svc.ConsumeOIDCFlow(ctx, h2, "google", "b2", 0, 0); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("expired flow: %v", err)
	}
	// Invalid starts are refused.
	for name, st := range map[string]identity.OIDCFlowStart{
		"no nonce":      {Kind: identity.OIDCLogin, Provider: "google", Verifier: "v", Binding: "b"},
		"no verifier":   {Kind: identity.OIDCLogin, Provider: "google", Nonce: "n", Binding: "b"},
		"no binding":    {Kind: identity.OIDCLogin, Provider: "google", Nonce: "n", Verifier: "v"},
		"no provider":   {Kind: identity.OIDCLogin, Nonce: "n", Verifier: "v", Binding: "b"},
		"unknown kind":  {Kind: "oidc_other", Provider: "google", Nonce: "n", Verifier: "v", Binding: "b"},
		"login w/ user": {Kind: identity.OIDCLogin, Provider: "google", Nonce: "n", Verifier: "v", Binding: "b", UserID: e.alice.ID},
	} {
		if _, _, err := e.svc.BeginOIDCFlow(ctx, st); !errors.Is(err, identity.ErrInvalid) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestOIDCLinkFlowIsBoundToTheAccountAndSession(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	sess, _, _ := e.svc.CreateSession(ctx, e.alice.ID, "a")
	other, _, _ := e.svc.CreateSession(ctx, e.alice.ID, "other")
	bobs, _, _ := e.svc.CreateSession(ctx, e.bob.ID, "b")
	link := identity.OIDCFlowStart{Kind: identity.OIDCLink, Provider: "google", Nonce: "n", Verifier: "v", Binding: "bind", UserID: e.alice.ID, SessionID: sess.ID}
	h := e.beginOIDC(t, link)
	for name, ids := range map[string][2]int64{
		"anonymous":        {0, 0},
		"other session":    {e.alice.ID, other.ID},
		"other user":       {e.bob.ID, bobs.ID},
		"session of other": {e.alice.ID, bobs.ID},
	} {
		if _, err := e.svc.ConsumeOIDCFlow(ctx, h, "google", "bind", ids[0], ids[1]); !errors.Is(err, identity.ErrUnauthorized) {
			t.Fatalf("%s consumed the link flow: %v", name, err)
		}
	}
	f, err := e.svc.ConsumeOIDCFlow(ctx, h, "google", "bind", e.alice.ID, sess.ID)
	if err != nil || f.Kind != identity.OIDCLink || f.UserID != e.alice.ID || f.SessionID != sess.ID {
		t.Fatalf("owner consume = %+v, %v", f, err)
	}
	// A link flow needs a live session of that user to begin.
	if err := e.svc.RevokeSession(ctx, e.alice.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	for name, st := range map[string]identity.OIDCFlowStart{
		"revoked session": {Kind: identity.OIDCLink, Provider: "google", Nonce: "n", Verifier: "v", Binding: "b", UserID: e.alice.ID, SessionID: other.ID},
		"foreign session": {Kind: identity.OIDCLink, Provider: "google", Nonce: "n", Verifier: "v", Binding: "b", UserID: e.alice.ID, SessionID: bobs.ID},
		"no session":      {Kind: identity.OIDCLink, Provider: "google", Nonce: "n", Verifier: "v", Binding: "b", UserID: e.alice.ID},
	} {
		if _, _, err := e.svc.BeginOIDCFlow(ctx, st); err == nil {
			t.Fatalf("%s began a link flow", name)
		}
	}
}

func (e *factorEnv) link(t *testing.T, u identity.User, sessID int64, provider, issuer, subject string) (identity.OIDCIdentity, identity.SessionRotation, error) {
	t.Helper()
	return e.svc.LinkOIDCIdentity(context.Background(), identity.OIDCLinkRequest{UserID: u.ID, SessionID: sessID,
		Provider: provider, Issuer: issuer, Subject: subject, DisplayName: "Alice Example"})
}

func TestOIDCLoginRequiresALinkedActiveAccount(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	if _, err := e.svc.LoginWithOIDC(ctx, issuerA, "sub-1"); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("unlinked login: %v", err)
	}
	var users int
	e.db.SQL().QueryRow(`SELECT count(*) FROM users`).Scan(&users)
	if users != 2 {
		t.Fatalf("an OIDC login created an account (%d users)", users)
	}
	sess, _, _ := e.svc.CreateSession(ctx, e.alice.ID, "a")
	if _, _, err := e.link(t, e.alice, sess.ID, "google", issuerA, "sub-1"); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	u, err := e.svc.LoginWithOIDC(ctx, issuerA, "sub-1")
	if err != nil || u.ID != e.alice.ID || !u.Active {
		t.Fatalf("login = %+v, %v", u, err)
	}
	list, _ := e.svc.ListOIDCIdentities(ctx, e.alice.ID)
	if len(list) != 1 || !list[0].LastLoginAt.Equal(e.clock.Now().Truncate(time.Millisecond)) {
		t.Fatalf("last login not recorded: %+v", list)
	}
	// Issuer and subject must both match exactly.
	for _, c := range [][2]string{{issuerB, "sub-1"}, {issuerA, "SUB-1"}, {issuerA, "sub-1 "}, {issuerA + "/", "sub-1"}, {issuerA, ""}} {
		if _, err := e.svc.LoginWithOIDC(ctx, c[0], c[1]); !errors.Is(err, identity.ErrUnauthorized) {
			t.Fatalf("login with %q/%q: %v", c[0], c[1], err)
		}
	}
	if err := e.svc.SetActive(ctx, "alice", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.LoginWithOIDC(ctx, issuerA, "sub-1"); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("suspended account signed in: %v", err)
	}
}

func TestOIDCLinkingRules(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	acting, actingSecret, _ := e.svc.CreateSession(ctx, e.alice.ID, "acting")
	_, otherSecret, _ := e.svc.CreateSession(ctx, e.alice.ID, "other")
	bobs, _, _ := e.svc.CreateSession(ctx, e.bob.ID, "b")

	long := strings.Repeat("é", 300)
	id, rot, err := e.svc.LinkOIDCIdentity(ctx, identity.OIDCLinkRequest{UserID: e.alice.ID, SessionID: acting.ID,
		Provider: "google", Issuer: issuerA, Subject: "sub-1", DisplayName: long + "\x00"})
	if err != nil {
		t.Fatal(err)
	}
	if id.Provider != "google" || len([]rune(id.DisplayName)) > 200 || strings.ContainsRune(id.DisplayName, 0) {
		t.Fatalf("identity = %+v", id)
	}
	var iss, sub string
	e.db.SQL().QueryRow(`SELECT issuer, subject FROM oidc_identities WHERE id = ?`, id.ID).Scan(&iss, &sub)
	if iss != issuerA || sub != "sub-1" {
		t.Fatalf("stored %q/%q", iss, sub)
	}
	// Linking is an authenticator change: others revoked, acting session rotated.
	if rot.Secret == "" || rot.Session.ID != acting.ID {
		t.Fatalf("rotation = %+v", rot)
	}
	if _, _, err := e.svc.ResolveSession(ctx, actingSecret); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("pre-link acting secret still works")
	}
	if _, _, err := e.svc.ResolveSession(ctx, otherSecret); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("other session survived linking")
	}
	if _, _, err := e.svc.ResolveSession(ctx, rot.Secret); err != nil {
		t.Fatalf("rotated session: %v", err)
	}
	// Conflicts.
	if _, _, err := e.link(t, e.bob, bobs.ID, "google", issuerA, "sub-1"); !errors.Is(err, identity.ErrExists) {
		t.Fatalf("identity linked to a second account: %v", err)
	}
	if _, _, err := e.link(t, e.alice, acting.ID, "google", issuerA, "sub-1"); !errors.Is(err, identity.ErrExists) {
		t.Fatalf("relinking the same identity: %v", err)
	}
	if _, _, err := e.link(t, e.alice, acting.ID, "google", issuerA, "sub-2"); !errors.Is(err, identity.ErrExists) {
		t.Fatalf("second link for one provider: %v", err)
	}
	if _, _, err := e.link(t, e.alice, acting.ID, "authentik", issuerB, "sub-1"); err != nil {
		t.Fatalf("link for another provider: %v", err)
	}
	// Validation and liveness.
	for _, sub := range []string{"", strings.Repeat("s", 256)} {
		if _, _, err := e.link(t, e.bob, bobs.ID, "google", issuerA, sub); !errors.Is(err, identity.ErrInvalid) {
			t.Fatalf("subject of %d bytes: %v", len(sub), err)
		}
	}
	if _, _, err := e.link(t, e.bob, bobs.ID, "google", "", "x"); !errors.Is(err, identity.ErrInvalid) {
		t.Fatalf("empty issuer: %v", err)
	}
	if err := e.svc.RevokeSession(ctx, e.bob.ID, bobs.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.link(t, e.bob, bobs.ID, "google", issuerA, "bob-sub"); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("link from a revoked session: %v", err)
	}
	if list, _ := e.svc.ListOIDCIdentities(ctx, e.bob.ID); len(list) != 0 {
		t.Fatal("refused link was stored")
	}
}

func TestConcurrentLinksOfOneIdentityYieldExactlyOneLink(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	as, _, _ := e.svc.CreateSession(ctx, e.alice.ID, "a")
	bs, _, _ := e.svc.CreateSession(ctx, e.bob.ID, "b")
	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i, pair := range [][2]int64{{e.alice.ID, as.ID}, {e.bob.ID, bs.ID}} {
		wg.Add(1)
		go func(i int, uid, sid int64) {
			defer wg.Done()
			<-start
			_, _, errs[i] = e.svc.LinkOIDCIdentity(ctx, identity.OIDCLinkRequest{UserID: uid, SessionID: sid, Provider: "google", Issuer: issuerA, Subject: "shared"})
		}(i, pair[0], pair[1])
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if !errors.Is(err, identity.ErrExists) {
			t.Fatalf("unexpected: %v", err)
		}
	}
	var n int
	e.db.SQL().QueryRow(`SELECT count(*) FROM oidc_identities WHERE issuer = ? AND subject = 'shared'`, issuerA).Scan(&n)
	if ok != 1 || n != 1 {
		t.Fatalf("%d successes, %d rows", ok, n)
	}
}

func TestOIDCUnlinkIsScopedAndRotates(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	as, _, _ := e.svc.CreateSession(ctx, e.alice.ID, "a")
	id, rot, err := e.link(t, e.alice, as.ID, "google", issuerA, "sub-1")
	if err != nil {
		t.Fatal(err)
	}
	bs, _, _ := e.svc.CreateSession(ctx, e.bob.ID, "b")
	if _, err := e.svc.UnlinkOIDCIdentity(ctx, e.bob.ID, id.ID, bs.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("bob unlinked alice's identity: %v", err)
	}
	_, other, _ := e.svc.CreateSession(ctx, e.alice.ID, "other")
	rot2, err := e.svc.UnlinkOIDCIdentity(ctx, e.alice.ID, id.ID, as.ID)
	if err != nil || rot2.Secret == "" || rot2.Secret == rot.Secret {
		t.Fatalf("unlink = %+v, %v", rot2, err)
	}
	if _, _, err := e.svc.ResolveSession(ctx, other); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("other session survived unlink")
	}
	if _, err := e.svc.LoginWithOIDC(ctx, issuerA, "sub-1"); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("login after unlink: %v", err)
	}
	if _, err := e.svc.UnlinkOIDCIdentity(ctx, e.alice.ID, id.ID, as.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("unlink twice: %v", err)
	}
}
