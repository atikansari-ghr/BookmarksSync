package identity

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// A use of the same code that lands between matching and recording the step must make
// the earlier request fail: the conditional step update is the replay guard.
func TestTOTPReplayInterleavedBetweenMatchAndRecordIsRejected(t *testing.T) {
	db := mustOpenDB(t, t.TempDir())
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	s := New(db, WithPasswordCost(8*1024, 1, 1), WithClock(func() time.Time { return now }),
		WithInstanceKey(bytes.Repeat([]byte{0x5a}, 32)))
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "alice", "correct horse battery staple", false)
	if err != nil {
		t.Fatal(err)
	}
	enr, _ := s.BeginTOTPEnrollment(ctx, u.ID)
	seed, _ := b32.DecodeString(enr.Secret)
	if _, err := s.ConfirmTOTPEnrollment(ctx, u.ID, 0, hotp(seed, uint64(totpStep(now)), TOTPDigits)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(TOTPPeriod)
	code := hotp(seed, uint64(totpStep(now)), TOTPDigits)
	var inner error
	fired := false
	s.beforeTOTPStepRecord = func() {
		if fired {
			return
		}
		fired = true
		inner = s.VerifyTOTP(ctx, u.ID, code)
	}
	outer := s.VerifyTOTP(ctx, u.ID, code)
	if inner != nil {
		t.Fatalf("interleaved use: %v", inner)
	}
	if !errors.Is(outer, ErrUnauthorized) {
		t.Fatalf("both uses of one TOTP code accepted (outer err = %v)", outer)
	}
}
