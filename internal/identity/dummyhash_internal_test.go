package identity

import (
	"context"
	"sync"
	"testing"
)

// VerifyPassword's unknown-user path must not race on shared state and must equalise
// work against the service's own Argon2id cost, computed exactly once per service.
func TestDummyHashIsPerServiceAndComputedOnceUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := mustOpenDB(t, dir)
	cheap := New(db, WithPasswordCost(8*1024, 1, 1))
	dear := New(db, WithPasswordCost(16*1024, 2, 1))

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = cheap.VerifyPassword(ctx, "nobody", "whatever password")
		}()
	}
	wg.Wait()
	if n := cheap.dummyComputations.Load(); n != 1 {
		t.Errorf("dummy hash computed %d times, want exactly once", n)
	}
	_, _ = dear.VerifyPassword(ctx, "nobody", "whatever password")
	if cheap.dummy() == dear.dummy() {
		t.Error("services with different Argon2id costs share one dummy hash")
	}
	if got := cheap.dummy(); got[:30] != cheap.dummy()[:30] || !hasParams(got, 8*1024, 1) {
		t.Errorf("cheap dummy hash does not carry the service cost: %s", got)
	}
	if got := dear.dummy(); !hasParams(got, 16*1024, 2) {
		t.Errorf("dear dummy hash does not carry the service cost: %s", got)
	}
}
