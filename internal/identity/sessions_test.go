package identity_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// fakeClock is a mutable, concurrency-safe test clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func clockSvc(db *database.DB, c *fakeClock) *identity.Service {
	return identity.New(db.SQL(), identity.WithPasswordCost(8*1024, 1, 1), identity.WithClock(c.Now))
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// rowText returns every column of the matching row rendered as text, so a test can
// assert that a plaintext secret never appears anywhere in persisted state.
func rowText(t *testing.T, db *database.DB, table string, id int64) string {
	t.Helper()
	rows, err := db.SQL().Query(fmt.Sprintf(`SELECT * FROM %s WHERE id = ?`, table), id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		t.Fatalf("%s row %d missing", table, id)
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i, v := range vals {
		fmt.Fprintf(&b, "%s=%v|", cols[i], v)
		if raw, ok := v.([]byte); ok {
			b.Write(raw)
			b.WriteByte('|')
		}
	}
	return b.String()
}

func TestSessionSecretIsOpaque256BitRandomness(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, false)

	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		_, secret, err := svc.CreateSession(ctx, u.ID, "Firefox")
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(secret)
		if err != nil || len(raw) != 32 {
			t.Fatalf("session secret %q is not 32 random bytes in base64url (err %v)", secret, err)
		}
		if strings.Contains(secret, "alice") || strings.Contains(secret, fmt.Sprint(u.ID)+".") {
			t.Fatalf("session secret %q embeds user data", secret)
		}
		if seen[secret] {
			t.Fatal("duplicate session secret")
		}
		seen[secret] = true
	}
}

func TestSessionStoresOnlySHA256OfSecret(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	sess, secret, err := svc.CreateSession(ctx, u.ID, "Firefox")
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.SQL().QueryRow(`SELECT secret_hash FROM web_sessions WHERE id = ?`, sess.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != sha256Hex(secret) {
		t.Errorf("stored hash %q, want SHA-256 hex of the secret", stored)
	}
	if all := rowText(t, db, "web_sessions", sess.ID); strings.Contains(all, secret) {
		t.Errorf("plaintext session secret persisted: %s", all)
	}
}

func TestResolveSession(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	c := newClock()
	svc := clockSvc(db, c)
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, true)
	sess, secret, err := svc.CreateSession(ctx, u.ID, "Firefox on Linux")
	if err != nil {
		t.Fatal(err)
	}
	got, user, err := svc.ResolveSession(ctx, secret)
	if err != nil {
		t.Fatalf("ResolveSession: %v", err)
	}
	if got.ID != sess.ID || got.UserID != u.ID || got.Label != "Firefox on Linux" {
		t.Errorf("session = %+v", got)
	}
	if user.ID != u.ID || user.Username != "alice" || !user.IsAdmin || !user.Active {
		t.Errorf("user = %+v", user)
	}
	if !got.AuthenticatedAt.Equal(c.Now()) || !got.ExpiresAt.Equal(c.Now().Add(identity.SessionAbsoluteTimeout)) {
		t.Errorf("times = %+v", got)
	}
	for _, bad := range []string{"", "x", secret + "x", strings.ToUpper(secret), base64.RawURLEncoding.EncodeToString(make([]byte, 32))} {
		if _, _, err := svc.ResolveSession(ctx, bad); !errors.Is(err, identity.ErrUnauthorized) {
			t.Errorf("ResolveSession(%q) err = %v, want ErrUnauthorized", bad, err)
		}
	}
}

func TestSessionPolicyIs12HourIdleAnd30DayAbsolute(t *testing.T) {
	if identity.SessionIdleTimeout != 12*time.Hour {
		t.Errorf("idle timeout = %v", identity.SessionIdleTimeout)
	}
	if identity.SessionAbsoluteTimeout != 30*24*time.Hour {
		t.Errorf("absolute timeout = %v", identity.SessionAbsoluteTimeout)
	}
}

func TestSessionIdleExpiry(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	c := newClock()
	svc := clockSvc(db, c)
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	_, secret, _ := svc.CreateSession(ctx, u.ID, "")

	// Activity just inside the idle window keeps the session alive.
	c.Advance(identity.SessionIdleTimeout - time.Minute)
	if _, _, err := svc.ResolveSession(ctx, secret); err != nil {
		t.Fatalf("resolve inside idle window: %v", err)
	}
	c.Advance(identity.SessionIdleTimeout - time.Minute)
	if _, _, err := svc.ResolveSession(ctx, secret); err != nil {
		t.Fatalf("activity did not extend the idle window: %v", err)
	}
	// Exactly 12 hours without use ends it.
	c.Advance(identity.SessionIdleTimeout)
	if _, _, err := svc.ResolveSession(ctx, secret); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("idle-expired session resolved: %v", err)
	}
	// It stays dead even if the clock check were relaxed by later activity.
	if _, _, err := svc.ResolveSession(ctx, secret); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("idle-expired session revived: %v", err)
	}
}

func TestSessionAbsoluteExpiryDespiteActivity(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	c := newClock()
	svc := clockSvc(db, c)
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	_, secret, _ := svc.CreateSession(ctx, u.ID, "")

	elapsed := time.Duration(0)
	for elapsed+11*time.Hour < identity.SessionAbsoluteTimeout {
		c.Advance(11 * time.Hour)
		elapsed += 11 * time.Hour
		if _, _, err := svc.ResolveSession(ctx, secret); err != nil {
			t.Fatalf("active session failed after %v: %v", elapsed, err)
		}
	}
	c.Advance(identity.SessionAbsoluteTimeout - elapsed)
	if _, _, err := svc.ResolveSession(ctx, secret); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("session usable at 30-day absolute limit: %v", err)
	}
}

func TestRotateSessionReplacesSecretAndKeepsAbsoluteLimit(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	c := newClock()
	svc := clockSvc(db, c)
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	orig, oldSecret, _ := svc.CreateSession(ctx, u.ID, "Edge")

	c.Advance(time.Hour)
	rotated, newSecret, err := svc.RotateSession(ctx, oldSecret)
	if err != nil {
		t.Fatalf("RotateSession: %v", err)
	}
	if newSecret == oldSecret || newSecret == "" {
		t.Fatal("rotation did not issue a new secret")
	}
	if _, _, err := svc.ResolveSession(ctx, oldSecret); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("pre-rotation secret still valid: %v", err)
	}
	got, _, err := svc.ResolveSession(ctx, newSecret)
	if err != nil {
		t.Fatalf("rotated secret: %v", err)
	}
	if got.ID != orig.ID || rotated.ID != orig.ID {
		t.Errorf("rotation changed session identity: %d -> %d", orig.ID, got.ID)
	}
	if !got.AuthenticatedAt.Equal(c.Now()) {
		t.Errorf("authenticated_at = %v, want rotation time %v", got.AuthenticatedAt, c.Now())
	}
	if !got.ExpiresAt.Equal(orig.ExpiresAt) {
		t.Errorf("rotation extended absolute expiry: %v -> %v", orig.ExpiresAt, got.ExpiresAt)
	}
	var stored string
	_ = db.SQL().QueryRow(`SELECT secret_hash FROM web_sessions WHERE id = ?`, orig.ID).Scan(&stored)
	if stored != sha256Hex(newSecret) {
		t.Error("rotated secret hash not persisted")
	}
	// The old secret cannot be rotated again either.
	if _, _, err := svc.RotateSession(ctx, oldSecret); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("rotating a replaced secret: %v", err)
	}
}

func TestRotateExpiredOrRevokedSessionFails(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	c := newClock()
	svc := clockSvc(db, c)
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	_, idle, _ := svc.CreateSession(ctx, u.ID, "")
	s2, revoked, _ := svc.CreateSession(ctx, u.ID, "")
	if err := svc.RevokeSession(ctx, u.ID, s2.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RotateSession(ctx, revoked); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("rotated revoked session: %v", err)
	}
	c.Advance(identity.SessionIdleTimeout)
	if _, _, err := svc.RotateSession(ctx, idle); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("rotated idle-expired session: %v", err)
	}
}

func TestRevokeSessionIsPerSessionAndOwnerScoped(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	alice, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	bob, _ := svc.CreateUser(ctx, "bob", goodPassword, false)
	a1, a1s, _ := svc.CreateSession(ctx, alice.ID, "laptop")
	_, a2s, _ := svc.CreateSession(ctx, alice.ID, "phone")
	_, b1s, _ := svc.CreateSession(ctx, bob.ID, "bob")

	// Another user cannot revoke alice's session, and learns nothing about it.
	if err := svc.RevokeSession(ctx, bob.ID, a1.ID); !errors.Is(err, identity.ErrNotFound) {
		t.Fatalf("cross-user revoke err = %v, want ErrNotFound", err)
	}
	if _, _, err := svc.ResolveSession(ctx, a1s); err != nil {
		t.Fatalf("cross-user revoke affected session: %v", err)
	}
	if err := svc.RevokeSession(ctx, alice.ID, a1.ID); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, _, err := svc.ResolveSession(ctx, a1s); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("revoked session still valid: %v", err)
	}
	if _, _, err := svc.ResolveSession(ctx, a2s); err != nil {
		t.Errorf("sibling session revoked: %v", err)
	}
	if _, _, err := svc.ResolveSession(ctx, b1s); err != nil {
		t.Errorf("other user's session revoked: %v", err)
	}
	if err := svc.RevokeSession(ctx, alice.ID, a1.ID); err != nil {
		t.Errorf("second revoke should be a no-op: %v", err)
	}
	if err := svc.RevokeSession(ctx, alice.ID, 999999); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("unknown session err = %v", err)
	}
}

func TestListSessionsShowsOnlyOwnLiveSessionsWithoutSecrets(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	c := newClock()
	svc := clockSvc(db, c)
	alice, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	bob, _ := svc.CreateUser(ctx, "bob", goodPassword, false)
	old, _, _ := svc.CreateSession(ctx, alice.ID, "old")
	c.Advance(identity.SessionIdleTimeout) // "old" is now idle-expired
	live, liveSecret, _ := svc.CreateSession(ctx, alice.ID, "live")
	gone, _, _ := svc.CreateSession(ctx, alice.ID, "gone")
	_ = svc.RevokeSession(ctx, alice.ID, gone.ID)
	_, _, _ = svc.CreateSession(ctx, bob.ID, "bob")

	list, err := svc.ListSessions(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != live.ID || list[0].Label != "live" {
		t.Fatalf("ListSessions = %+v, want only %d (old=%d)", list, live.ID, old.ID)
	}
	if strings.Contains(fmt.Sprintf("%+v", list), liveSecret) || strings.Contains(fmt.Sprintf("%+v", list), sha256Hex(liveSecret)) {
		t.Error("listing exposes secret or hash")
	}
}

func TestRevokeUserSessionsCanKeepCurrentSession(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	alice, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	bob, _ := svc.CreateUser(ctx, "bob", goodPassword, false)
	cur, curS, _ := svc.CreateSession(ctx, alice.ID, "")
	_, otherS, _ := svc.CreateSession(ctx, alice.ID, "")
	_, bobS, _ := svc.CreateSession(ctx, bob.ID, "")

	if err := svc.RevokeUserSessions(ctx, alice.ID, cur.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ResolveSession(ctx, otherS); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("other session survived: %v", err)
	}
	if _, _, err := svc.ResolveSession(ctx, curS); err != nil {
		t.Errorf("kept session revoked: %v", err)
	}
	if err := svc.RevokeUserSessions(ctx, alice.ID, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.ResolveSession(ctx, curS); !errors.Is(err, identity.ErrUnauthorized) {
		t.Errorf("user-wide revoke left a session: %v", err)
	}
	if _, _, err := svc.ResolveSession(ctx, bobS); err != nil {
		t.Errorf("other user affected: %v", err)
	}
}

func TestSessionsRequireActiveUser(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	_, _ = svc.CreateUser(ctx, "alice", goodPassword, false)
	alice, _ := svc.GetUser(ctx, "alice")
	if _, _, err := svc.CreateSession(ctx, 424242, ""); !errors.Is(err, identity.ErrNotFound) {
		t.Errorf("unknown user err = %v", err)
	}
	_ = svc.SetActive(ctx, "alice", false)
	if _, _, err := svc.CreateSession(ctx, alice.ID, ""); !errors.Is(err, identity.ErrInvalid) {
		t.Errorf("inactive user err = %v, want ErrInvalid", err)
	}
}

func TestSessionLabelIsBoundedAndValidUTF8(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	svc := clockSvc(db, newClock())
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	sess, _, err := svc.CreateSession(ctx, u.ID, strings.Repeat("é", 500)+"\xff")
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(sess.Label)); n != 200 {
		t.Errorf("label runes = %d, want truncated to 200", n)
	}
	if strings.ContainsRune(sess.Label, '�') {
		t.Error("label contains replacement characters")
	}
}

func TestPurgeExpiredSessionsDeletesOnlyDeadSessions(t *testing.T) {
	ctx := context.Background()
	db, _ := open(t)
	c := newClock()
	svc := clockSvc(db, c)
	u, _ := svc.CreateUser(ctx, "alice", goodPassword, false)
	_, _, _ = svc.CreateSession(ctx, u.ID, "idle")
	rev, _, _ := svc.CreateSession(ctx, u.ID, "revoked")
	_ = svc.RevokeSession(ctx, u.ID, rev.ID)
	c.Advance(identity.SessionIdleTimeout)
	_, liveS, _ := svc.CreateSession(ctx, u.ID, "live")

	n, err := svc.PurgeExpiredSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("purged %d, want 2", n)
	}
	var left int
	_ = db.SQL().QueryRow(`SELECT COUNT(*) FROM web_sessions`).Scan(&left)
	if left != 1 {
		t.Errorf("remaining sessions = %d", left)
	}
	if _, _, err := svc.ResolveSession(ctx, liveS); err != nil {
		t.Errorf("live session purged: %v", err)
	}
}
