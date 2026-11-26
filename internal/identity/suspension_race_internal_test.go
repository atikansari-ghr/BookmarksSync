package identity

import (
	"context"
	"errors"
	"testing"
)

// raceSetup returns a service whose next issuing statement is preceded by a
// suspension committed through an independent service on the same database, which
// deterministically reproduces SetActive(false) landing between the active-user check
// and the insert.
func raceSetup(t *testing.T) (svc, admin *Service, userID int64) {
	t.Helper()
	db := mustOpenDB(t, t.TempDir())
	svc = New(db, WithPasswordCost(8*1024, 1, 1))
	admin = New(db, WithPasswordCost(8*1024, 1, 1))
	u, err := svc.CreateUser(context.Background(), "alice", "correct horse battery staple", false)
	if err != nil {
		t.Fatal(err)
	}
	svc.beforeIssue = func(ctx context.Context) {
		svc.beforeIssue = nil
		if err := admin.SetActive(ctx, "alice", false); err != nil {
			t.Fatalf("concurrent suspension: %v", err)
		}
	}
	return svc, admin, u.ID
}

func unrevokedCount(t *testing.T, svc *Service, table string) int {
	t.Helper()
	var n int
	if err := svc.db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE revoked_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateSessionRacingSuspensionIsNotIssued(t *testing.T) {
	ctx := context.Background()
	svc, admin, uid := raceSetup(t)

	_, secret, err := svc.CreateSession(ctx, uid, "")
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("CreateSession during suspension err = %v, want ErrInvalid", err)
	}
	if n := unrevokedCount(t, svc, "web_sessions"); n != 0 {
		t.Errorf("%d unrevoked session(s) escaped suspension", n)
	}
	if err := admin.SetActive(ctx, "alice", true); err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		if _, _, err := svc.ResolveSession(ctx, secret); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("session issued during suspension usable after reactivation: %v", err)
		}
	}
}

func TestCreateAPITokenRacingSuspensionIsNotIssued(t *testing.T) {
	ctx := context.Background()
	svc, admin, uid := raceSetup(t)

	_, secret, err := svc.CreateAPIToken(ctx, NewAPIToken{UserID: uid, Scopes: []Scope{ScopeBookmarksRead}})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("CreateAPIToken during suspension err = %v, want ErrInvalid", err)
	}
	if n := unrevokedCount(t, svc, "api_tokens"); n != 0 {
		t.Errorf("%d unrevoked token(s) escaped suspension", n)
	}
	if err := admin.SetActive(ctx, "alice", true); err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		if _, err := svc.ResolveAPIToken(ctx, secret); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("token issued during suspension usable after reactivation: %v", err)
		}
	}
}
