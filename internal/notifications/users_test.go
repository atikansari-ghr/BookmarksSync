package notifications

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

type recordingVerificationSender struct {
	kind, destination, code string
	err                     error
	notices                 []string
	noticeErr               error
}

func (s *recordingVerificationSender) SendVerification(_ context.Context, kind, destination, code string) error {
	s.kind, s.destination, s.code = kind, destination, code
	return s.err
}

func (s *recordingVerificationSender) SendNotice(_ context.Context, kind, destination, category string, occurredAt time.Time) error {
	s.notices = append(s.notices, kind+":"+destination+":"+category)
	if occurredAt.IsZero() {
		return errors.New("missing event time")
	}
	return s.noticeErr
}

func TestUserNotificationBindingIsHashedExpiringSingleUseAndIsolated(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := identity.New(db.SQL(), identity.WithPasswordCost(8*1024, 1, 1))
	alice, err := ids.CreateUser(ctx, "alice", "a long enough password", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := ids.CreateUser(ctx, "bob", "a long enough password", false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	sender := &recordingVerificationSender{}
	svc := NewUserService(db.SQL(), sender, func() time.Time { return now })
	if err := svc.Begin(ctx, alice.ID, "email", "alice@example.test"); err != nil {
		t.Fatal(err)
	}
	if sender.kind != "email" || sender.destination != "alice@example.test" || len(sender.code) != 8 {
		t.Fatalf("verification delivery = %#v", sender)
	}
	if err := svc.Begin(ctx, bob.ID, "email", "alice@example.test"); !errors.Is(err, ErrUserNotification) {
		t.Fatalf("cross-account destination rate limit err=%v", err)
	}
	var stored string
	if err := db.SQL().QueryRowContext(ctx, `SELECT CAST(code_hash AS TEXT) FROM notification_challenges WHERE user_id=? AND kind='email'`, alice.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, sender.code) {
		t.Fatal("plaintext verification code stored")
	}
	if err := svc.Begin(ctx, alice.ID, "email", "other@example.test"); !errors.Is(err, ErrUserNotification) {
		t.Fatalf("rate-limited begin err=%v", err)
	}
	if err := svc.Confirm(ctx, bob.ID, "email", sender.code); !errors.Is(err, ErrUserNotification) {
		t.Fatalf("cross-user confirmation err=%v", err)
	}
	if err := svc.Confirm(ctx, alice.ID, "email", sender.code); err != nil {
		t.Fatal(err)
	}
	if err := svc.Confirm(ctx, alice.ID, "email", sender.code); !errors.Is(err, ErrUserNotification) {
		t.Fatalf("replay err=%v", err)
	}
	if err := svc.SetPreferences(ctx, alice.ID, true, false); err != nil {
		t.Fatal(err)
	}
	a, err := svc.Settings(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Channels) != 1 || a.Channels[0].Destination != "alice@example.test" || !a.SecurityAlerts || a.SharingAlerts {
		t.Fatalf("alice settings=%+v", a)
	}
	b, err := svc.Settings(ctx, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Channels) != 0 || b.SecurityAlerts || b.SharingAlerts {
		t.Fatalf("bob settings=%+v", b)
	}
	svc.deliverAlert(ctx, userAlert{userID: alice.ID, category: "security", occurredAt: now})
	svc.deliverAlert(ctx, userAlert{userID: alice.ID, category: "sharing", occurredAt: now})
	svc.deliverAlert(ctx, userAlert{userID: bob.ID, category: "security", occurredAt: now})
	if len(sender.notices) != 1 || sender.notices[0] != "email:alice@example.test:security" {
		t.Fatalf("opt-in notice delivery=%v", sender.notices)
	}
	if !svc.QueueAlert(alice.ID, "security") {
		t.Fatal("security notice did not enter the queue")
	}
	shutdown, cancel := context.WithCancel(ctx)
	cancel()
	_ = svc.Run(shutdown)
	if len(sender.notices) != 2 || sender.notices[1] != "email:alice@example.test:security" {
		t.Fatalf("queued notice was not drained on shutdown: %v", sender.notices)
	}
	sender.noticeErr = errors.New("private delivery failure")
	svc.deliverAlert(ctx, userAlert{userID: alice.ID, category: "security", occurredAt: now})
	if svc.DeliveryFailures() != 1 {
		t.Fatalf("user delivery failures=%d, want 1", svc.DeliveryFailures())
	}
	if err := svc.Remove(ctx, alice.ID, "email"); err != nil {
		t.Fatal(err)
	}
	a, err = svc.Settings(ctx, alice.ID)
	if err != nil || len(a.Channels) != 0 {
		t.Fatalf("remove result=%+v err=%v", a, err)
	}
}

func TestUserNotificationBindingExpiryAttemptsAndDeliveryFailure(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "users.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ids := identity.New(db.SQL(), identity.WithPasswordCost(8*1024, 1, 1))
	u, err := ids.CreateUser(ctx, "alice", "a long enough password", false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	sender := &recordingVerificationSender{err: errors.New("private provider error")}
	svc := NewUserService(db.SQL(), sender, func() time.Time { return now })
	if err := svc.Begin(ctx, u.ID, "telegram", "1234567"); !errors.Is(err, ErrUserNotification) {
		t.Fatalf("delivery failure = %v", err)
	}
	var n int
	if err := db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM notification_challenges`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed-send challenge count=%d err=%v", n, err)
	}
	sender.err = nil
	if err := svc.Begin(ctx, u.ID, "telegram", "1234567"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := svc.Confirm(ctx, u.ID, "telegram", "00000000"); !errors.Is(err, ErrUserNotification) {
			t.Fatalf("wrong code attempt %d = %v", i+1, err)
		}
	}
	var attempts int
	if err := db.SQL().QueryRowContext(ctx, `SELECT attempts FROM notification_challenges WHERE user_id=?`, u.ID).Scan(&attempts); err != nil || attempts != 5 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	if err := svc.Confirm(ctx, u.ID, "telegram", sender.code); !errors.Is(err, ErrUserNotification) {
		t.Fatalf("correct code after max attempts = %v", err)
	}
	now = now.Add(11 * time.Minute)
	if err := svc.Begin(ctx, u.ID, "telegram", "1234567"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Minute)
	if err := svc.Confirm(ctx, u.ID, "telegram", sender.code); !errors.Is(err, ErrUserNotification) {
		t.Fatalf("expired code = %v", err)
	}
	var still sql.NullString
	if err := db.SQL().QueryRowContext(ctx, `SELECT destination FROM notification_channels WHERE user_id=?`, u.ID).Scan(&still); err == nil {
		t.Fatal("expired channel was verified")
	}
}
