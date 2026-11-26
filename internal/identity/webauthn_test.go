package identity_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"

	"github.com/go-webauthn/webauthn/protocol"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/webauthntest"
)

const (
	rpOrigin = "https://bookmarks.example.test"
	rpID     = "bookmarks.example.test"
)

type passkeyEnv struct {
	*factorEnv
	aliceSess, bobSess identity.Session
}

func newPasskeyEnv(t *testing.T) *passkeyEnv {
	t.Helper()
	rp, err := identity.NewRelyingParty(rpOrigin)
	if err != nil {
		t.Fatal(err)
	}
	e := newFactorEnv(t, identity.WithRelyingParty(rp))
	ctx := context.Background()
	as, _, err := e.svc.CreateSession(ctx, e.alice.ID, "a")
	if err != nil {
		t.Fatal(err)
	}
	bs, _, err := e.svc.CreateSession(ctx, e.bob.ID, "b")
	if err != nil {
		t.Fatal(err)
	}
	return &passkeyEnv{factorEnv: e, aliceSess: as, bobSess: bs}
}

// register runs a full registration ceremony for u on its session.
func (e *passkeyEnv) register(t *testing.T, a *webauthntest.Authenticator, u identity.User, sess identity.Session) (identity.Passkey, *webauthntest.Credential) {
	t.Helper()
	ctx := context.Background()
	c, err := e.svc.BeginPasskeyRegistration(ctx, u.ID, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	resp, cred, err := a.Register(c.Options, webauthntest.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	pk, _, err := e.svc.FinishPasskeyRegistration(ctx, u.ID, sess.ID, c.FlowID, "Laptop", resp)
	if err != nil {
		t.Fatalf("finish registration: %v", err)
	}
	return pk, cred
}

func (e *passkeyEnv) login(t *testing.T, a *webauthntest.Authenticator, o webauthntest.Overrides) (identity.User, error) {
	t.Helper()
	ctx := context.Background()
	c, err := e.svc.BeginPasskeyLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := a.Assert(c.Options, o)
	if err != nil {
		t.Fatal(err)
	}
	return e.svc.FinishPasskeyLogin(ctx, c.FlowID, resp)
}

type optionsView struct {
	PublicKey struct {
		Challenge string `json:"challenge"`
		RP        struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"rp"`
		RPID string `json:"rpId"`
		User struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"user"`
		AuthenticatorSelection struct {
			ResidentKey      string `json:"residentKey"`
			UserVerification string `json:"userVerification"`
		} `json:"authenticatorSelection"`
		UserVerification   string `json:"userVerification"`
		ExcludeCredentials []struct {
			ID string `json:"id"`
		} `json:"excludeCredentials"`
		AllowCredentials []any `json:"allowCredentials"`
	} `json:"publicKey"`
}

func view(t *testing.T, raw []byte) optionsView {
	t.Helper()
	var v optionsView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("options %s: %v", raw, err)
	}
	return v
}

func TestRelyingPartyComesOnlyFromThePublicOrigin(t *testing.T) {
	for _, bad := range []string{"", "https://bookmarks.example.test/path", "http://bookmarks.example.test", "https://192.0.2.1", "http://127.0.0.1:8080"} {
		if _, err := identity.NewRelyingParty(bad); err == nil {
			t.Errorf("relying party accepted origin %q", bad)
		}
	}
	if _, err := identity.NewRelyingParty("http://localhost:8080"); err != nil {
		t.Errorf("loopback development origin rejected: %v", err)
	}
	e := newPasskeyEnv(t)
	ctx := context.Background()
	c, err := e.svc.BeginPasskeyRegistration(ctx, e.alice.ID, e.aliceSess.ID)
	if err != nil {
		t.Fatal(err)
	}
	v := view(t, c.Options)
	if v.PublicKey.RP.ID != rpID || v.PublicKey.AuthenticatorSelection.ResidentKey != "required" ||
		v.PublicKey.AuthenticatorSelection.UserVerification != "required" {
		t.Fatalf("creation options = %+v", v.PublicKey)
	}
	l, err := e.svc.BeginPasskeyLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lv := view(t, l.Options)
	if lv.PublicKey.RPID != rpID || lv.PublicKey.UserVerification != "required" || len(lv.PublicKey.AllowCredentials) != 0 {
		t.Fatalf("request options = %+v (discoverable login must not list credentials)", lv.PublicKey)
	}
}

func TestUserHandleIsRandomAndNotTheDatabaseID(t *testing.T) {
	e := newPasskeyEnv(t)
	ctx := context.Background()
	c1, _ := e.svc.BeginPasskeyRegistration(ctx, e.alice.ID, e.aliceSess.ID)
	c2, _ := e.svc.BeginPasskeyRegistration(ctx, e.alice.ID, e.aliceSess.ID)
	cb, _ := e.svc.BeginPasskeyRegistration(ctx, e.bob.ID, e.bobSess.ID)
	h1, _ := base64.RawURLEncoding.DecodeString(view(t, c1.Options).PublicKey.User.ID)
	h2, _ := base64.RawURLEncoding.DecodeString(view(t, c2.Options).PublicKey.User.ID)
	hb, _ := base64.RawURLEncoding.DecodeString(view(t, cb.Options).PublicKey.User.ID)
	if len(h1) < 32 || !bytes.Equal(h1, h2) || bytes.Equal(h1, hb) {
		t.Fatalf("user handles: alice %x / %x, bob %x", h1, h2, hb)
	}
	for _, id := range []int64{e.alice.ID} {
		var le [8]byte
		binary.LittleEndian.PutUint64(le[:], uint64(id))
		if bytes.Contains(h1, []byte("alice")) || bytes.Equal(h1, le[:]) || string(h1) == "1" {
			t.Fatal("user handle derived from the account")
		}
	}
	var stored []byte
	e.db.SQL().QueryRow(`SELECT user_handle FROM webauthn_users WHERE user_id = ?`, e.alice.ID).Scan(&stored)
	if !bytes.Equal(stored, h1) {
		t.Fatal("user handle not persisted")
	}
}

func TestPasskeyRegistrationAndDiscoverableLogin(t *testing.T) {
	e := newPasskeyEnv(t)
	a := webauthntest.New(rpOrigin)
	a.BE, a.BS = true, true
	pk, cred := e.register(t, a, e.alice, e.aliceSess)
	if pk.Name != "Laptop" || !pk.BackupEligible || !pk.BackupState {
		t.Fatalf("passkey = %+v", pk)
	}
	var signCount, uv int
	e.db.SQL().QueryRow(`SELECT sign_count, user_verified FROM webauthn_credentials WHERE id = ?`, pk.ID).Scan(&signCount, &uv)
	if uv != 1 {
		t.Fatal("user verification not recorded")
	}
	u, err := e.login(t, a, webauthntest.Overrides{})
	if err != nil || u.ID != e.alice.ID {
		t.Fatalf("login = %+v, %v", u, err)
	}
	e.db.SQL().QueryRow(`SELECT sign_count FROM webauthn_credentials WHERE id = ?`, pk.ID).Scan(&signCount)
	if uint32(signCount) != cred.SignCount {
		t.Fatalf("stored counter %d, authenticator %d", signCount, cred.SignCount)
	}
	list, err := e.svc.ListPasskeys(context.Background(), e.alice.ID)
	if err != nil || len(list) != 1 || list[0].LastUsedAt.IsZero() {
		t.Fatalf("list = %+v, %v", list, err)
	}
	// Existing credentials are excluded from a new registration.
	c, _ := e.svc.BeginPasskeyRegistration(context.Background(), e.alice.ID, e.aliceSess.ID)
	if ex := view(t, c.Options).PublicKey.ExcludeCredentials; len(ex) != 1 || ex[0].ID != base64.RawURLEncoding.EncodeToString(cred.ID) {
		t.Fatalf("excludeCredentials = %+v", ex)
	}
}

func TestPasskeyChallengeIsSingleUse(t *testing.T) {
	e := newPasskeyEnv(t)
	a := webauthntest.New(rpOrigin)
	e.register(t, a, e.alice, e.aliceSess)
	ctx := context.Background()
	c, _ := e.svc.BeginPasskeyLogin(ctx)
	resp, _ := a.Assert(c.Options, webauthntest.Overrides{})
	if _, err := e.svc.FinishPasskeyLogin(ctx, c.FlowID, resp); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.FinishPasskeyLogin(ctx, c.FlowID, resp); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("replayed assertion accepted: %v", err)
	}
	// A fresh assertion over a used challenge, presented on another flow, fails too.
	c2, _ := e.svc.BeginPasskeyLogin(ctx)
	stale, _ := a.Assert(c.Options, webauthntest.Overrides{})
	if _, err := e.svc.FinishPasskeyLogin(ctx, c2.FlowID, stale); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("assertion for another challenge accepted: %v", err)
	}
	// Registration flows are single use as well.
	r, _ := e.svc.BeginPasskeyRegistration(ctx, e.alice.ID, e.aliceSess.ID)
	reg, _, _ := a.Register(r.Options, webauthntest.Overrides{})
	if _, _, err := e.svc.FinishPasskeyRegistration(ctx, e.alice.ID, e.aliceSess.ID, r.FlowID, "Two", reg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.FinishPasskeyRegistration(ctx, e.alice.ID, e.aliceSess.ID, r.FlowID, "Two", reg); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("registration flow reused: %v", err)
	}
	// Expired flows fail.
	c3, _ := e.svc.BeginPasskeyLogin(ctx)
	e.clock.Advance(identity.PasskeyFlowTTL)
	late, _ := a.Assert(c3.Options, webauthntest.Overrides{})
	if _, err := e.svc.FinishPasskeyLogin(ctx, c3.FlowID, late); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("expired flow accepted: %v", err)
	}
	// Unknown flow handles fail.
	c4, _ := e.svc.BeginPasskeyLogin(ctx)
	ok, _ := a.Assert(c4.Options, webauthntest.Overrides{})
	if _, err := e.svc.FinishPasskeyLogin(ctx, "bogus", ok); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("unknown flow accepted: %v", err)
	}
}

func TestPasskeyFlowHandlesAreStoredHashed(t *testing.T) {
	e := newPasskeyEnv(t)
	c, _ := e.svc.BeginPasskeyLogin(context.Background())
	var n int
	e.db.SQL().QueryRow(`SELECT count(*) FROM auth_flows WHERE flow_hash = ?`, sha256Hex(c.FlowID)).Scan(&n)
	var leak int
	e.db.SQL().QueryRow(`SELECT count(*) FROM auth_flows WHERE flow_hash = ?1 OR instr(data, ?1) > 0`, c.FlowID).Scan(&leak)
	if n != 1 || leak != 0 {
		t.Fatalf("flow handle stored as hash: %d, plaintext occurrences: %d", n, leak)
	}
}

func TestPasskeyOriginAndRPBinding(t *testing.T) {
	e := newPasskeyEnv(t)
	a := webauthntest.New(rpOrigin)
	e.register(t, a, e.alice, e.aliceSess)
	for name, o := range map[string]webauthntest.Overrides{
		"foreign origin":     {Origin: "https://evil.example"},
		"http downgrade":     {Origin: "http://bookmarks.example.test"},
		"sibling origin":     {Origin: "https://other.bookmarks.example.test"},
		"foreign RP ID hash": {RPID: "evil.example"},
		"create type":        {Type: "webauthn.create"},
		"wrong challenge":    {Challenge: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := e.login(t, a, o); !errors.Is(err, identity.ErrUnauthorized) {
				t.Fatalf("login accepted: %v", err)
			}
		})
	}
	// Registration is bound the same way.
	ctx := context.Background()
	for name, o := range map[string]webauthntest.Overrides{
		"foreign origin":     {Origin: "https://evil.example"},
		"foreign RP ID hash": {RPID: "evil.example"},
		"get type":           {Type: "webauthn.get"},
	} {
		t.Run("register "+name, func(t *testing.T) {
			c, _ := e.svc.BeginPasskeyRegistration(ctx, e.alice.ID, e.aliceSess.ID)
			resp, _, _ := a.Register(c.Options, o)
			if _, _, err := e.svc.FinishPasskeyRegistration(ctx, e.alice.ID, e.aliceSess.ID, c.FlowID, "x", resp); !errors.Is(err, identity.ErrUnauthorized) {
				t.Fatalf("registration accepted: %v", err)
			}
		})
	}
}

func TestPasskeyRequiresUserVerification(t *testing.T) {
	e := newPasskeyEnv(t)
	ctx := context.Background()
	noUV := webauthntest.New(rpOrigin)
	noUV.UV = false
	c, _ := e.svc.BeginPasskeyRegistration(ctx, e.alice.ID, e.aliceSess.ID)
	resp, _, _ := noUV.Register(c.Options, webauthntest.Overrides{})
	if _, _, err := e.svc.FinishPasskeyRegistration(ctx, e.alice.ID, e.aliceSess.ID, c.FlowID, "x", resp); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("registration without UV accepted: %v", err)
	}
	a := webauthntest.New(rpOrigin)
	e.register(t, a, e.alice, e.aliceSess)
	a.UV = false
	if _, err := e.login(t, a, webauthntest.Overrides{}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("login without UV accepted: %v", err)
	}
}

func TestPasskeyRegistrationIsBoundToTheAccountAndSession(t *testing.T) {
	e := newPasskeyEnv(t)
	ctx := context.Background()
	a := webauthntest.New(rpOrigin)
	c, _ := e.svc.BeginPasskeyRegistration(ctx, e.alice.ID, e.aliceSess.ID)
	resp, _, _ := a.Register(c.Options, webauthntest.Overrides{})
	// Bob cannot finish alice's ceremony, and another alice session cannot either.
	if _, _, err := e.svc.FinishPasskeyRegistration(ctx, e.bob.ID, e.bobSess.ID, c.FlowID, "x", resp); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("other account finished the ceremony: %v", err)
	}
	other, _, _ := e.svc.CreateSession(ctx, e.alice.ID, "other")
	if _, _, err := e.svc.FinishPasskeyRegistration(ctx, e.alice.ID, other.ID, c.FlowID, "x", resp); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("another session finished the ceremony: %v", err)
	}
	// A mismatched attempt does not burn the owner's ceremony.
	if _, _, err := e.svc.FinishPasskeyRegistration(ctx, e.alice.ID, e.aliceSess.ID, c.FlowID, "Mine", resp); err != nil {
		t.Fatalf("owner could not finish after foreign attempts: %v", err)
	}
	if list, _ := e.svc.ListPasskeys(ctx, e.bob.ID); len(list) != 0 {
		t.Fatal("credential attached to bob")
	}
}

func TestPasskeyRegistrationRevokesOtherSessions(t *testing.T) {
	e := newPasskeyEnv(t)
	ctx := context.Background()
	_, otherSecret, _ := e.svc.CreateSession(ctx, e.alice.ID, "other")
	pk, _ := e.register(t, webauthntest.New(rpOrigin), e.alice, e.aliceSess)
	if _, _, err := e.svc.ResolveSession(ctx, otherSecret); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("other session survived passkey registration: %v", err)
	}
	var live int
	e.db.SQL().QueryRow(`SELECT count(*) FROM web_sessions WHERE id = ? AND revoked_at IS NULL`, e.aliceSess.ID).Scan(&live)
	if live != 1 {
		t.Fatal("the registering session was revoked")
	}
	_, other2, _ := e.svc.CreateSession(ctx, e.alice.ID, "other2")
	if _, err := e.svc.DeletePasskey(ctx, e.alice.ID, pk.ID, e.aliceSess.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.ResolveSession(ctx, other2); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("other session survived passkey removal: %v", err)
	}
}

func TestPasskeyOwnershipOnLogin(t *testing.T) {
	e := newPasskeyEnv(t)
	aliceAuth := webauthntest.New(rpOrigin)
	bobAuth := webauthntest.New(rpOrigin)
	_, aliceCred := e.register(t, aliceAuth, e.alice, e.aliceSess)
	_, bobCred := e.register(t, bobAuth, e.bob, e.bobSess)
	// Bob's credential presented with alice's user handle must not sign in anyone.
	if _, err := e.login(t, bobAuth, webauthntest.Overrides{Cred: bobCred, UserHandle: aliceCred.UserHandle}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("credential used under another user's handle: %v", err)
	}
	// Alice's credential id signed with an attacker key fails.
	attacker, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if _, err := e.login(t, aliceAuth, webauthntest.Overrides{SignWith: attacker}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("forged signature accepted: %v", err)
	}
	// An unknown user handle fails.
	if _, err := e.login(t, aliceAuth, webauthntest.Overrides{UserHandle: bytes.Repeat([]byte{9}, 64)}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("unknown user handle accepted: %v", err)
	}
	// Management is scoped to the owner.
	list, _ := e.svc.ListPasskeys(context.Background(), e.alice.ID)
	if err := e.svc.RenamePasskey(context.Background(), e.bob.ID, list[0].ID, "pwned"); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("bob renamed alice's passkey: %v", err)
	}
	if _, err := e.svc.DeletePasskey(context.Background(), e.bob.ID, list[0].ID, e.bobSess.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("bob deleted alice's passkey: %v", err)
	}
	if err := e.svc.RenamePasskey(context.Background(), e.alice.ID, list[0].ID, "Phone"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", string(bytes.Repeat([]byte("x"), 65)), "bad\x00name"} {
		if err := e.svc.RenamePasskey(context.Background(), e.alice.ID, list[0].ID, bad); !errors.Is(err, identity.ErrInvalid) {
			t.Fatalf("name %q: %v", bad, err)
		}
	}
	if u, err := e.login(t, aliceAuth, webauthntest.Overrides{}); err != nil || u.ID != e.alice.ID {
		t.Fatalf("legit login after attacks = %+v, %v", u, err)
	}
}

func TestPasskeySignatureCounterRegressionIsRejected(t *testing.T) {
	e := newPasskeyEnv(t)
	a := webauthntest.New(rpOrigin)
	pk, cred := e.register(t, a, e.alice, e.aliceSess)
	five := uint32(5)
	if _, err := e.login(t, a, webauthntest.Overrides{SignCount: &five}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []uint32{5, 3} {
		c := c
		if _, err := e.login(t, a, webauthntest.Overrides{SignCount: &c}); !errors.Is(err, identity.ErrUnauthorized) {
			t.Fatalf("counter %d after 5 accepted: %v", c, err)
		}
	}
	var stored, clone int
	e.db.SQL().QueryRow(`SELECT sign_count, clone_warning FROM webauthn_credentials WHERE id = ?`, pk.ID).Scan(&stored, &clone)
	if stored != 5 || clone != 1 {
		t.Fatalf("stored counter %d clone warning %d, want 5 and 1", stored, clone)
	}
	_ = cred
	// Authenticators that never count (always 0) remain usable.
	z := webauthntest.New(rpOrigin)
	zero := uint32(0)
	pkz, credz := e.register(t, z, e.bob, e.bobSess)
	for i := 0; i < 2; i++ {
		if _, err := e.login(t, z, webauthntest.Overrides{Cred: credz, SignCount: &zero}); err != nil {
			t.Fatalf("zero-counter login %d: %v", i, err)
		}
	}
	_ = pkz
}

func TestPasskeyBackupFlagConsistency(t *testing.T) {
	e := newPasskeyEnv(t)
	a := webauthntest.New(rpOrigin)
	a.BE = false
	e.register(t, a, e.alice, e.aliceSess)
	a.BE = true // eligibility cannot change after registration
	if _, err := e.login(t, a, webauthntest.Overrides{}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("backup eligibility change accepted: %v", err)
	}
	a.BE, a.BS = false, true // BS without BE is invalid
	if _, err := e.login(t, a, webauthntest.Overrides{}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("BS=1 BE=0 accepted: %v", err)
	}
	// Backup state changes for an eligible credential are recorded.
	b := webauthntest.New(rpOrigin)
	b.BE = true
	pk, _ := e.register(t, b, e.bob, e.bobSess)
	b.BS = true
	if _, err := e.login(t, b, webauthntest.Overrides{}); err != nil {
		t.Fatal(err)
	}
	var bs int
	e.db.SQL().QueryRow(`SELECT backup_state FROM webauthn_credentials WHERE id = ?`, pk.ID).Scan(&bs)
	if bs != 1 {
		t.Fatal("backup state not updated")
	}
	// Invalid flags at registration are refused.
	flags := protocol.FlagUserPresent | protocol.FlagUserVerified | protocol.FlagBackupState | protocol.FlagAttestedCredentialData
	c, _ := e.svc.BeginPasskeyRegistration(context.Background(), e.bob.ID, e.bobSess.ID)
	resp, _, _ := b.Register(c.Options, webauthntest.Overrides{Flags: &flags})
	if _, _, err := e.svc.FinishPasskeyRegistration(context.Background(), e.bob.ID, e.bobSess.ID, c.FlowID, "x", resp); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("registration with BS=1 BE=0 accepted: %v", err)
	}
}

func TestPasskeyCompletionRequiresAnActiveAccount(t *testing.T) {
	e := newPasskeyEnv(t)
	ctx := context.Background()
	a := webauthntest.New(rpOrigin)
	e.register(t, a, e.alice, e.aliceSess)
	c, _ := e.svc.BeginPasskeyLogin(ctx)
	resp, _ := a.Assert(c.Options, webauthntest.Overrides{})
	if err := e.svc.SetActive(ctx, "alice", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.FinishPasskeyLogin(ctx, c.FlowID, resp); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("suspended user signed in with a passkey: %v", err)
	}
	// A registration begun while active cannot be finished once suspended.
	if err := e.svc.SetActive(ctx, "alice", true); err != nil {
		t.Fatal(err)
	}
	sess, _, _ := e.svc.CreateSession(ctx, e.alice.ID, "new")
	r, _ := e.svc.BeginPasskeyRegistration(ctx, e.alice.ID, sess.ID)
	reg, _, _ := a.Register(r.Options, webauthntest.Overrides{})
	if err := e.svc.SetActive(ctx, "alice", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.FinishPasskeyRegistration(ctx, e.alice.ID, sess.ID, r.FlowID, "x", reg); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("suspended user registered a passkey: %v", err)
	}
	if _, err := e.svc.BeginPasskeyRegistration(ctx, e.alice.ID, sess.ID); err == nil {
		t.Fatal("inactive user began a registration")
	}
}

func TestDuplicateCredentialIsRejected(t *testing.T) {
	e := newPasskeyEnv(t)
	ctx := context.Background()
	a := webauthntest.New(rpOrigin)
	_, cred := e.register(t, a, e.alice, e.aliceSess)
	// Replaying the same attestation (same credential id) into bob's ceremony.
	c, _ := e.svc.BeginPasskeyRegistration(ctx, e.bob.ID, e.bobSess.ID)
	resp, newCred, _ := a.Register(c.Options, webauthntest.Overrides{})
	_ = newCred
	if _, err := e.db.SQL().Exec(`UPDATE webauthn_credentials SET credential_id = ? WHERE user_id = ?`, newCred.ID, e.alice.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.FinishPasskeyRegistration(ctx, e.bob.ID, e.bobSess.ID, c.FlowID, "x", resp); !errors.Is(err, identity.ErrExists) {
		t.Fatalf("duplicate credential id: %v", err)
	}
	_ = cred
}

func TestPasskeysUnavailableWithoutRelyingParty(t *testing.T) {
	e := newFactorEnv(t)
	if _, err := e.svc.BeginPasskeyLogin(context.Background()); !errors.Is(err, identity.ErrNotConfigured) {
		t.Fatalf("begin without RP: %v", err)
	}
}
