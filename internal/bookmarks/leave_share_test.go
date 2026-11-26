package bookmarks_test

import (
	"errors"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func TestRecipientCanLeaveOnlyTheirOwnShare(t *testing.T) {
	e := newEnv(t)
	alice, bob, carol := e.user("leave-owner"), e.user("leave-recipient"), e.user("leave-other")
	sc := e.apiPrivate(alice)
	f := e.folder(sc, e.otherRoot(sc), "Shared")
	sh, err := e.svc.GrantShare(ctx, alice.ID, f, bob.ID, identity.ModeEdit)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = e.shared(alice, f, bob, identity.ModeEdit, identity.ModeEdit)
	if err := e.svc.LeaveShare(ctx, carol.ID, sh.ID); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("non-recipient leave = %v, want not found", err)
	}
	if _, err := e.svc.ShareScopeFor(ctx, bob.ID, sh.ID, true); err != nil {
		t.Fatalf("foreign leave removed share: %v", err)
	}
	if err := e.svc.LeaveShare(ctx, bob.ID, sh.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ShareScopeFor(ctx, bob.ID, sh.ID, true); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("share remains after recipient left: %v", err)
	}
	if err := e.svc.LeaveShare(ctx, bob.ID, sh.ID); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("second leave = %v, want not found", err)
	}
}

func TestOwnerCanUpdateShareModeByID(t *testing.T) {
	e := newEnv(t)
	alice, bob, carol := e.user("mode-owner"), e.user("mode-recipient"), e.user("mode-other")
	sc := e.apiPrivate(alice)
	f := e.folder(sc, e.otherRoot(sc), "Shared")
	sh, err := e.svc.GrantShare(ctx, alice.ID, f, bob.ID, identity.ModeView)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := e.svc.UpdateShare(ctx, alice.ID, sh.ID, identity.ModeEdit)
	if err != nil || updated.Mode != identity.ModeEdit {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if _, err := e.svc.UpdateShare(ctx, carol.ID, sh.ID, identity.ModeView); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("non-owner update = %v, want not found", err)
	}
	if _, err := e.svc.UpdateShare(ctx, alice.ID, sh.ID, "invalid"); !errors.Is(err, bookmarks.ErrInvalid) {
		t.Fatalf("invalid mode = %v, want invalid", err)
	}
}
