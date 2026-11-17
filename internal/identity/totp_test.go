package identity_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

var testInstanceKey = bytes.Repeat([]byte{0x5a}, 32)

type factorEnv struct {
	db    *database.DB
	svc   *identity.Service
	clock *fakeClock
	alice identity.User
	bob   identity.User
}

func newFactorEnv(t *testing.T, opts ...identity.Option) *factorEnv {
	t.Helper()
	db, _ := open(t)
	clock := newClock()
	all := append([]identity.Option{identity.WithPasswordCost(8*1024, 1, 1), identity.WithClock(clock.Now),
		identity.WithInstanceKey(testInstanceKey)}, opts...)
	svc := identity.New(db.SQL(), all...)
	ctx := context.Background()
	alice, err := svc.CreateUser(ctx, "alice", goodPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := svc.CreateUser(ctx, "bob", goodPassword, false)
	if err != nil {
		t.Fatal(err)
	}
	return &factorEnv{db: db, svc: svc, clock: clock, alice: alice, bob: bob}
}

// testTOTP is an independent RFC 6238 (SHA-1, 6 digits, 30 s) implementation.
func testTOTP(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		t.Fatalf("secret %q is not base32: %v", secret, err)
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(at.Unix()/30))
	m := hmac.New(sha1.New, seed)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}

// enrollTOTP enrolls and confirms TOTP for u, returning the secret and recovery codes.
func (e *factorEnv) enrollTOTP(t *testing.T, u identity.User) (string, []string) {
	t.Helper()
	ctx := context.Background()
	enr, err := e.svc.BeginTOTPEnrollment(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	codes, _, err := e.svc.ConfirmTOTPEnrollment(ctx, u.ID, 0, testTOTP(t, enr.Secret, e.clock.Now()))
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(identity.TOTPPeriod) // the confirmation step is now spent
	return enr.Secret, codes
}

func TestTOTPEnrollmentProvisioningURIAndPendingState(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	enr, err := e.svc.BeginTOTPEnrollment(ctx, e.alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(enr.Secret)
	if err != nil || len(seed) != 20 {
		t.Fatalf("secret %q: %d bytes, %v; want a 160-bit base32 seed", enr.Secret, len(seed), err)
	}
	u, err := url.Parse(enr.URI)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme != "otpauth" || u.Host != "totp" || q.Get("secret") != enr.Secret || q.Get("issuer") != "BookmarksSync" ||
		q.Get("algorithm") != "SHA1" || q.Get("digits") != "6" || q.Get("period") != "30" ||
		!strings.Contains(u.Path, "alice") {
		t.Fatalf("provisioning URI = %s", enr.URI)
	}
	// Pending enrollment is not an enabled factor.
	st, err := e.svc.TOTPStatus(ctx, e.alice.ID)
	if err != nil || st.Enabled {
		t.Fatalf("status before confirmation = %+v, %v", st, err)
	}
	if err := e.svc.VerifyTOTP(ctx, e.alice.ID, testTOTP(t, enr.Secret, e.clock.Now())); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("pending seed verified a login code: %v", err)
	}
	// A wrong confirmation code does not enable it.
	if _, _, err := e.svc.ConfirmTOTPEnrollment(ctx, e.alice.ID, 0, "000000"); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("wrong confirmation code: %v", err)
	}
	// Restarting enrollment replaces the pending seed.
	enr2, err := e.svc.BeginTOTPEnrollment(ctx, e.alice.ID)
	if err != nil || enr2.Secret == enr.Secret {
		t.Fatalf("re-enrollment = %+v, %v", enr2, err)
	}
	if _, _, err := e.svc.ConfirmTOTPEnrollment(ctx, e.alice.ID, 0, testTOTP(t, enr.Secret, e.clock.Now())); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("replaced seed confirmed enrollment: %v", err)
	}
	codes, _, err := e.svc.ConfirmTOTPEnrollment(ctx, e.alice.ID, 0, testTOTP(t, enr2.Secret, e.clock.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != identity.RecoveryCodeCount || identity.RecoveryCodeCount != 10 {
		t.Fatalf("%d recovery codes", len(codes))
	}
	st, _ = e.svc.TOTPStatus(ctx, e.alice.ID)
	if !st.Enabled || st.RecoveryCodesRemaining != 10 {
		t.Fatalf("status = %+v", st)
	}
	// Enabled TOTP cannot be silently re-enrolled.
	if _, err := e.svc.BeginTOTPEnrollment(ctx, e.alice.ID); !errors.Is(err, identity.ErrExists) {
		t.Fatalf("re-enrolling an enabled factor: %v", err)
	}
}

func TestPendingTOTPEnrollmentExpires(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	enr, err := e.svc.BeginTOTPEnrollment(ctx, e.alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(identity.TOTPEnrollmentTTL + time.Second)
	if _, _, err := e.svc.ConfirmTOTPEnrollment(ctx, e.alice.ID, 0, testTOTP(t, enr.Secret, e.clock.Now())); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("expired enrollment confirmed: %v", err)
	}
}

func TestTOTPSeedIsEncryptedAtRestAndBoundToTheUser(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	secret, _ := e.enrollTOTP(t, e.alice)
	seed, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	var ct []byte
	var keyVersion int
	if err := e.db.SQL().QueryRow(`SELECT seed_ciphertext, key_version FROM totp_secrets WHERE user_id = ?`, e.alice.ID).
		Scan(&ct, &keyVersion); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, seed) || bytes.Contains(ct, []byte(secret)) || keyVersion != 1 || len(ct) < len(seed)+28 {
		t.Fatalf("seed stored in the clear or without AEAD overhead (len %d, version %d)", len(ct), keyVersion)
	}
	// Copying alice's ciphertext to bob must not give bob a working seed.
	e.enrollTOTP(t, e.bob)
	if _, err := e.db.SQL().Exec(`UPDATE totp_secrets SET seed_ciphertext = ?, last_used_step = NULL WHERE user_id = ?`, ct, e.bob.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.VerifyTOTP(ctx, e.bob.ID, testTOTP(t, secret, e.clock.Now())); err == nil {
		t.Fatal("ciphertext transplanted to another user verified")
	}
	// A service with a different instance key cannot use the seed.
	other := identity.New(e.db.SQL(), identity.WithClock(e.clock.Now), identity.WithInstanceKey(bytes.Repeat([]byte{1}, 32)))
	if err := other.VerifyTOTP(ctx, e.alice.ID, testTOTP(t, secret, e.clock.Now())); err == nil {
		t.Fatal("seed decrypted under another instance key")
	}
	// Without an instance key TOTP is unavailable rather than insecure.
	none := identity.New(e.db.SQL(), identity.WithClock(e.clock.Now))
	if _, err := none.BeginTOTPEnrollment(ctx, e.bob.ID); !errors.Is(err, identity.ErrNotConfigured) {
		t.Fatalf("enrollment without instance key: %v", err)
	}
}

func TestTOTPVerificationWindowAndReplay(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	secret, _ := e.enrollTOTP(t, e.alice)
	now := e.clock.Now()

	// Accepted skew is exactly one step either side.
	if err := e.svc.VerifyTOTP(ctx, e.alice.ID, testTOTP(t, secret, now.Add(-2*identity.TOTPPeriod))); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("code two steps old accepted: %v", err)
	}
	if err := e.svc.VerifyTOTP(ctx, e.alice.ID, testTOTP(t, secret, now.Add(2*identity.TOTPPeriod))); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("code two steps ahead accepted: %v", err)
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "000000"} {
		if err := e.svc.VerifyTOTP(ctx, e.alice.ID, bad); !errors.Is(err, identity.ErrUnauthorized) {
			t.Fatalf("code %q: %v", bad, err)
		}
	}
	code := testTOTP(t, secret, now)
	if err := e.svc.VerifyTOTP(ctx, e.alice.ID, code); err != nil {
		t.Fatalf("current code: %v", err)
	}
	if err := e.svc.VerifyTOTP(ctx, e.alice.ID, code); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("replayed code accepted: %v", err)
	}
	// An older (still in-window) step is rejected once a later step was accepted.
	if err := e.svc.VerifyTOTP(ctx, e.alice.ID, testTOTP(t, secret, now.Add(-identity.TOTPPeriod))); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("earlier step accepted after a later one: %v", err)
	}
	// The next step is accepted (one step of forward skew), then the clock catches up.
	if err := e.svc.VerifyTOTP(ctx, e.alice.ID, testTOTP(t, secret, now.Add(identity.TOTPPeriod))); err != nil {
		t.Fatalf("next-step code: %v", err)
	}
	e.clock.Advance(identity.TOTPPeriod)
	if err := e.svc.VerifyTOTP(ctx, e.alice.ID, testTOTP(t, secret, e.clock.Now())); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("step accepted twice across clock movement: %v", err)
	}
	// Codes are per user.
	if err := e.svc.VerifyTOTP(ctx, e.bob.ID, testTOTP(t, secret, e.clock.Now().Add(identity.TOTPPeriod))); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("alice's code verified bob: %v", err)
	}
}

func TestConcurrentTOTPReplayAcceptsExactlyOnce(t *testing.T) {
	e := newFactorEnv(t)
	secret, _ := e.enrollTOTP(t, e.alice)
	code := testTOTP(t, secret, e.clock.Now())
	const n = 16
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { results <- e.svc.VerifyTOTP(context.Background(), e.alice.ID, code) }()
	}
	ok := 0
	for i := 0; i < n; i++ {
		if err := <-results; err == nil {
			ok++
		} else if !errors.Is(err, identity.ErrUnauthorized) {
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d concurrent uses of one code accepted, want 1", ok)
	}
}

func TestRecoveryCodesAreHashedAndSingleUse(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	_, codes := e.enrollTOTP(t, e.alice)
	seen := map[string]bool{}
	for _, c := range codes {
		norm := strings.ReplaceAll(c, "-", "")
		if len(norm) < 16 || seen[c] {
			t.Fatalf("weak or duplicate recovery code %q", c)
		}
		seen[c] = true
	}
	rows, err := e.db.SQL().Query(`SELECT code_hash FROM recovery_codes WHERE user_id = ?`, e.alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored []string
	for rows.Next() {
		var h string
		rows.Scan(&h)
		stored = append(stored, h)
	}
	rows.Close()
	if len(stored) != len(codes) {
		t.Fatalf("%d stored hashes for %d codes", len(stored), len(codes))
	}
	for _, h := range stored {
		for _, c := range codes {
			if strings.Contains(h, c) || strings.Contains(h, strings.ReplaceAll(c, "-", "")) {
				t.Fatal("recovery code stored in plaintext")
			}
		}
	}
	// Formatting is forgiving (case, spaces, hyphens), use is single.
	loose := " " + strings.ToUpper(strings.ReplaceAll(codes[0], "-", " ")) + " "
	if err := e.svc.ConsumeRecoveryCode(ctx, e.alice.ID, loose); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if err := e.svc.ConsumeRecoveryCode(ctx, e.alice.ID, codes[0]); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("reused recovery code: %v", err)
	}
	if err := e.svc.ConsumeRecoveryCode(ctx, e.bob.ID, codes[1]); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("alice's code consumed by bob: %v", err)
	}
	if err := e.svc.ConsumeRecoveryCode(ctx, e.alice.ID, "not-a-code"); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("garbage code: %v", err)
	}
	st, _ := e.svc.TOTPStatus(ctx, e.alice.ID)
	if st.RecoveryCodesRemaining != len(codes)-1 {
		t.Fatalf("remaining = %d", st.RecoveryCodesRemaining)
	}
	// Regeneration invalidates every earlier code.
	fresh, _, err := e.svc.RegenerateRecoveryCodes(ctx, e.alice.ID, 0)
	if err != nil || len(fresh) != identity.RecoveryCodeCount {
		t.Fatalf("regenerate = %d codes, %v", len(fresh), err)
	}
	if err := e.svc.ConsumeRecoveryCode(ctx, e.alice.ID, codes[2]); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("old code survived regeneration: %v", err)
	}
	if err := e.svc.ConsumeRecoveryCode(ctx, e.alice.ID, fresh[0]); err != nil {
		t.Fatal(err)
	}
	// Recovery codes require an enabled TOTP factor.
	if _, _, err := e.svc.RegenerateRecoveryCodes(ctx, e.bob.ID, 0); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("regenerate without TOTP: %v", err)
	}
}

func TestConcurrentRecoveryCodeUseAcceptsExactlyOnce(t *testing.T) {
	e := newFactorEnv(t)
	_, codes := e.enrollTOTP(t, e.alice)
	const n = 16
	results := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { results <- e.svc.ConsumeRecoveryCode(context.Background(), e.alice.ID, codes[3]) }()
	}
	ok := 0
	for i := 0; i < n; i++ {
		if <-results == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d concurrent uses accepted, want 1", ok)
	}
}

func TestTOTPChangesRevokeOtherSessions(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	keep, _, _ := e.svc.CreateSession(ctx, e.alice.ID, "keep")
	_, other, _ := e.svc.CreateSession(ctx, e.alice.ID, "other")
	_, bobs, _ := e.svc.CreateSession(ctx, e.bob.ID, "bob")
	enr, _ := e.svc.BeginTOTPEnrollment(ctx, e.alice.ID)
	if _, _, err := e.svc.ConfirmTOTPEnrollment(ctx, e.alice.ID, keep.ID, testTOTP(t, enr.Secret, e.clock.Now())); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.ResolveSession(ctx, other); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("other session survived enabling TOTP: %v", err)
	}
	if _, _, err := e.svc.ResolveSession(ctx, bobs); err != nil {
		t.Fatalf("bob's session affected: %v", err)
	}
	_, other2, _ := e.svc.CreateSession(ctx, e.alice.ID, "other2")
	if _, err := e.svc.DisableTOTP(ctx, e.alice.ID, keep.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.ResolveSession(ctx, other2); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("other session survived disabling TOTP: %v", err)
	}
	var n int
	e.db.SQL().QueryRow(`SELECT (SELECT count(*) FROM totp_secrets WHERE user_id = ?1) + (SELECT count(*) FROM recovery_codes WHERE user_id = ?1)`, e.alice.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("%d TOTP/recovery rows left after disable", n)
	}
	if st, _ := e.svc.TOTPStatus(ctx, e.alice.ID); st.Enabled {
		t.Fatal("still enabled")
	}
	if _, err := e.svc.DisableTOTP(ctx, e.alice.ID, keep.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("disabling twice: %v", err)
	}
}

func TestSecondFactorFlowIsSingleUseShortLivedAndAttemptBounded(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	secret, codes := e.enrollTOTP(t, e.alice)

	flow, err := e.svc.BeginSecondFactor(ctx, e.alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(flow.ID) < 43 || !flow.ExpiresAt.Equal(e.clock.Now().Add(identity.SecondFactorTTL)) || identity.SecondFactorTTL > 5*time.Minute {
		t.Fatalf("flow = %+v", flow)
	}
	var stored string
	e.db.SQL().QueryRow(`SELECT flow_hash FROM auth_flows WHERE kind = 'second_factor'`).Scan(&stored)
	if stored == "" || stored == flow.ID || stored != sha256Hex(flow.ID) {
		t.Fatal("flow handle not stored as its SHA-256 only")
	}
	// The pending handle is not a session.
	if _, _, err := e.svc.ResolveSession(ctx, flow.ID); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("flow handle resolved as a session: %v", err)
	}
	u, err := e.svc.CompleteSecondFactor(ctx, flow.ID, identity.SecondFactorProof{TOTP: testTOTP(t, secret, e.clock.Now())})
	if err != nil || u.ID != e.alice.ID {
		t.Fatalf("complete = %+v, %v", u, err)
	}
	if _, err := e.svc.CompleteSecondFactor(ctx, flow.ID, identity.SecondFactorProof{RecoveryCode: codes[0]}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("flow reused: %v", err)
	}
	if st, _ := e.svc.TOTPStatus(ctx, e.alice.ID); st.RecoveryCodesRemaining != len(codes) {
		t.Fatal("a refused flow still consumed a recovery code")
	}

	// Recovery code path.
	flow2, _ := e.svc.BeginSecondFactor(ctx, e.alice.ID)
	if _, err := e.svc.CompleteSecondFactor(ctx, flow2.ID, identity.SecondFactorProof{RecoveryCode: codes[0]}); err != nil {
		t.Fatal(err)
	}
	flow3, _ := e.svc.BeginSecondFactor(ctx, e.alice.ID)
	if _, err := e.svc.CompleteSecondFactor(ctx, flow3.ID, identity.SecondFactorProof{RecoveryCode: codes[0]}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("recovery code reused through a new flow: %v", err)
	}

	// Expiry.
	flow4, _ := e.svc.BeginSecondFactor(ctx, e.alice.ID)
	e.clock.Advance(identity.SecondFactorTTL)
	if _, err := e.svc.CompleteSecondFactor(ctx, flow4.ID, identity.SecondFactorProof{TOTP: testTOTP(t, secret, e.clock.Now())}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("expired flow completed: %v", err)
	}

	// Attempts are bounded per flow: after the limit even a correct code fails.
	e.clock.Advance(identity.TOTPPeriod)
	flow5, _ := e.svc.BeginSecondFactor(ctx, e.alice.ID)
	for i := 0; i < identity.SecondFactorMaxAttempts; i++ {
		if _, err := e.svc.CompleteSecondFactor(ctx, flow5.ID, identity.SecondFactorProof{TOTP: "000000"}); !errors.Is(err, identity.ErrUnauthorized) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := e.svc.CompleteSecondFactor(ctx, flow5.ID, identity.SecondFactorProof{TOTP: testTOTP(t, secret, e.clock.Now())}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("flow usable after %d failed attempts: %v", identity.SecondFactorMaxAttempts, err)
	}
	// Exactly one proof must be supplied.
	flow6, _ := e.svc.BeginSecondFactor(ctx, e.alice.ID)
	if _, err := e.svc.CompleteSecondFactor(ctx, flow6.ID, identity.SecondFactorProof{TOTP: testTOTP(t, secret, e.clock.Now()), RecoveryCode: codes[5]}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("two proofs accepted: %v", err)
	}
	// A second factor flow cannot be started for a user without TOTP.
	if _, err := e.svc.BeginSecondFactor(ctx, e.bob.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("flow for user without TOTP: %v", err)
	}
}

func TestSecondFactorCompletionRequiresAnActiveAccount(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	secret, codes := e.enrollTOTP(t, e.alice)
	flow, _ := e.svc.BeginSecondFactor(ctx, e.alice.ID)
	if err := e.svc.SetActive(ctx, "alice", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CompleteSecondFactor(ctx, flow.ID, identity.SecondFactorProof{TOTP: testTOTP(t, secret, e.clock.Now())}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("suspended user completed a pending login: %v", err)
	}
	if _, err := e.svc.CompleteSecondFactor(ctx, flow.ID, identity.SecondFactorProof{RecoveryCode: codes[0]}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("suspended user completed a pending login with a recovery code: %v", err)
	}
	if st, _ := e.svc.TOTPStatus(ctx, e.alice.ID); st.RecoveryCodesRemaining != len(codes) {
		t.Fatal("refused completion consumed a recovery code")
	}
}

func TestNewSecondFactorFlowSupersedesThePendingOne(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	secret, _ := e.enrollTOTP(t, e.alice)
	first, _ := e.svc.BeginSecondFactor(ctx, e.alice.ID)
	second, _ := e.svc.BeginSecondFactor(ctx, e.alice.ID)
	code := testTOTP(t, secret, e.clock.Now())
	if _, err := e.svc.CompleteSecondFactor(ctx, first.ID, identity.SecondFactorProof{TOTP: code}); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("superseded flow completed: %v", err)
	}
	if _, err := e.svc.CompleteSecondFactor(ctx, second.ID, identity.SecondFactorProof{TOTP: code}); err != nil {
		t.Fatalf("latest flow: %v", err)
	}
}

// A wrong proof on a live flow identifies the flow's account (and only then), so the
// HTTP layer can count second-factor failures per account.
func TestWrongSecondFactorProofReportsTheFlowOwner(t *testing.T) {
	e := newFactorEnv(t)
	ctx := context.Background()
	e.enrollTOTP(t, e.alice)
	flow, _ := e.svc.BeginSecondFactor(ctx, e.alice.ID)
	u, err := e.svc.CompleteSecondFactor(ctx, flow.ID, identity.SecondFactorProof{TOTP: "000000"})
	if !errors.Is(err, identity.ErrUnauthorized) || u.ID != e.alice.ID || u.Active {
		t.Fatalf("wrong proof = %+v, %v; want alice's id only, not an authenticated user", u, err)
	}
	u, err = e.svc.CompleteSecondFactor(ctx, "unknown", identity.SecondFactorProof{TOTP: "000000"})
	if !errors.Is(err, identity.ErrUnauthorized) || u.ID != 0 {
		t.Fatalf("unknown flow = %+v, %v", u, err)
	}
}

func TestConcurrentSecondFactorBeginsLeaveOneUsableFlow(t *testing.T) {
	for round := 0; round < 5; round++ {
		e := newFactorEnv(t)
		ctx := context.Background()
		e.enrollTOTP(t, e.alice)
		const n = 16
		ids := make(chan string, n)
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			go func() {
				<-start
				f, err := e.svc.BeginSecondFactor(ctx, e.alice.ID)
				if err != nil {
					t.Errorf("begin: %v", err)
				}
				ids <- f.ID
			}()
		}
		close(start)
		var flows []string
		for i := 0; i < n; i++ {
			flows = append(flows, <-ids)
		}
		var live int
		e.db.SQL().QueryRow(`SELECT count(*) FROM auth_flows WHERE user_id = ? AND kind = 'second_factor' AND consumed_at IS NULL`, e.alice.ID).Scan(&live)
		if live != 1 {
			t.Fatalf("round %d: %d pending second-factor flows after concurrent begins, want 1", round, live)
		}
		// Only one handle is usable: probe each with a wrong code and count the live ones.
		usable := 0
		for _, f := range flows {
			if u, _ := e.svc.CompleteSecondFactor(ctx, f, identity.SecondFactorProof{TOTP: "000000"}); u.ID != 0 {
				usable++
			}
		}
		if usable != 1 {
			t.Fatalf("round %d: %d usable flows, want 1", round, usable)
		}
	}
}
