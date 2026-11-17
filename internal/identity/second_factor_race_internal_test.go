package identity

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Parallel wrong proofs against one pending login must never let more than
// SecondFactorMaxAttempts proofs reach verification: counting an attempt has to be
// atomic with evaluating it.
func TestConcurrentWrongSecondFactorProofsCannotExceedMaxAttempts(t *testing.T) {
	for round := 0; round < 5; round++ {
		db := mustOpenDB(t, t.TempDir())
		now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
		var checks atomic.Int32
		s := New(db, WithPasswordCost(8*1024, 1, 1), WithClock(func() time.Time { return now }),
			WithInstanceKey(bytes.Repeat([]byte{0x5a}, 32)))
		s.secondFactorProofChecked = func() {
			checks.Add(1)
			time.Sleep(2 * time.Millisecond) // widen any window between check and count
		}
		ctx := context.Background()
		u, err := s.CreateUser(ctx, "alice", "correct horse battery staple", false)
		if err != nil {
			t.Fatal(err)
		}
		enr, err := s.BeginTOTPEnrollment(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		seed, _ := b32.DecodeString(enr.Secret)
		if _, _, err := s.ConfirmTOTPEnrollment(ctx, u.ID, 0, hotp(seed, uint64(totpStep(now)), TOTPDigits)); err != nil {
			t.Fatal(err)
		}
		now = now.Add(TOTPPeriod)
		flow, err := s.BeginSecondFactor(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		const n = 24
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if _, err := s.CompleteSecondFactor(ctx, flow.ID, SecondFactorProof{TOTP: "000000"}); !errors.Is(err, ErrUnauthorized) {
					t.Errorf("wrong proof: %v", err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if got := checks.Load(); got > SecondFactorMaxAttempts {
			t.Fatalf("round %d: %d wrong proofs reached verification, max is %d", round, got, SecondFactorMaxAttempts)
		}
		good := hotp(seed, uint64(totpStep(now)), TOTPDigits)
		if _, err := s.CompleteSecondFactor(ctx, flow.ID, SecondFactorProof{TOTP: good}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("round %d: flow still usable after the attempt limit: %v", round, err)
		}
	}
}
