package bookmarks_test

import (
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func TestLockExcludesOtherCredentialsOnSameScopeAndRenews(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice")
	one, two := e.private(u, identity.ModeEdit), e.private(u, identity.ModeEdit)
	bar, _ := e.roots(one)

	noErr(t, e.svc.AcquireLock(ctx, one))
	noErr(t, e.svc.AcquireLock(ctx, one)) // renewal by the owner of the lock
	is(t, e.svc.AcquireLock(ctx, two), bookmarks.ErrLocked)
	// mutation enforcement, not advisory: the other credential cannot write...
	_, err := e.svc.CreateFolder(ctx, two, bar, "blocked")
	is(t, err, bookmarks.ErrLocked)
	_, err = e.svc.CreateBookmark(ctx, two, bookmarks.BookmarkInput{URL: "https://b.example/", Title: "b", Folders: []int64{bar}})
	is(t, err, bookmarks.ErrLocked)
	is(t, e.svc.SetChildOrder(ctx, two, bar, nil), bookmarks.ErrLocked)
	// ...but can read
	_, err = e.svc.Children(ctx, two, bookmarks.RootID, -1)
	noErr(t, err)
	// the holder can write
	_, err = e.svc.CreateFolder(ctx, one, bar, "allowed")
	noErr(t, err)
	// renewing pushes expiry out: 4 min + renew + 4 min is still locked
	e.advance(4 * time.Minute)
	noErr(t, e.svc.AcquireLock(ctx, one))
	e.advance(4 * time.Minute)
	is(t, e.svc.AcquireLock(ctx, two), bookmarks.ErrLocked)
}

func TestLockExpiresAfterTTL(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice")
	one, two := e.private(u, identity.ModeEdit), e.private(u, identity.ModeEdit)
	bar, _ := e.roots(one)
	noErr(t, e.svc.AcquireLock(ctx, one))
	e.advance(5*time.Minute + time.Second)
	_, err := e.svc.CreateFolder(ctx, two, bar, "after-expiry")
	noErr(t, err) // an expired lock no longer blocks writers
	noErr(t, e.svc.AcquireLock(ctx, two))
	is(t, e.svc.AcquireLock(ctx, one), bookmarks.ErrLocked)
}

func TestOnlyLockOwnerCanRelease(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice")
	one, two := e.private(u, identity.ModeEdit), e.private(u, identity.ModeEdit)
	noErr(t, e.svc.AcquireLock(ctx, one))
	is(t, e.svc.ReleaseLock(ctx, two), bookmarks.ErrLocked) // not yours
	is(t, e.svc.AcquireLock(ctx, two), bookmarks.ErrLocked) // still held
	noErr(t, e.svc.ReleaseLock(ctx, one))
	noErr(t, e.svc.ReleaseLock(ctx, one)) // idempotent
	noErr(t, e.svc.AcquireLock(ctx, two))
}

func TestViewOnlyCredentialsMayLockButNotWrite(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice")
	view := e.private(u, identity.ModeView)
	edit := e.private(u, identity.ModeEdit)
	bar, _ := e.roots(edit)
	noErr(t, e.svc.AcquireLock(ctx, view))
	_, err := e.svc.CreateFolder(ctx, view, bar, "x")
	is(t, err, bookmarks.ErrForbidden)
	noErr(t, e.svc.ReleaseLock(ctx, view))
}

func TestUnrelatedUsersLockIndependently(t *testing.T) {
	e := newEnv(t)
	a, b := e.private(e.user("alice"), identity.ModeEdit), e.private(e.user("bob"), identity.ModeEdit)
	noErr(t, e.svc.AcquireLock(ctx, a))
	noErr(t, e.svc.AcquireLock(ctx, b))
	bBar, _ := e.roots(b)
	_, err := e.svc.CreateFolder(ctx, b, bBar, "independent")
	noErr(t, err)
}

func TestOwnerRootLockOverlapsRecipientShareLockAndViceVersa(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	aBar, _ := e.roots(as)
	shared := e.folder(as, aBar, "Shared")
	other := e.folder(as, aBar, "Unrelated")
	inner := e.folder(as, shared, "Inner")
	ss, _, _ := e.shared(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	innerShare, _, _ := e.shared(alice, inner, e.user("carol"), identity.ModeEdit, identity.ModeEdit)
	otherShare, _, _ := e.shared(alice, other, e.user("dave"), identity.ModeEdit, identity.ModeEdit)

	// owner's whole-collection lock blocks the recipient and vice versa
	noErr(t, e.svc.AcquireLock(ctx, as))
	is(t, e.svc.AcquireLock(ctx, ss), bookmarks.ErrLocked)
	_, err := e.svc.CreateFolder(ctx, ss, bookmarks.RootID, "blocked")
	is(t, err, bookmarks.ErrLocked)
	noErr(t, e.svc.ReleaseLock(ctx, as))

	noErr(t, e.svc.AcquireLock(ctx, ss))
	is(t, e.svc.AcquireLock(ctx, as), bookmarks.ErrLocked)
	_, err = e.svc.CreateFolder(ctx, as, aBar, "owner-blocked")
	is(t, err, bookmarks.ErrLocked)
	// fix round 1: every scope under one owner collection shares one lock key, so a nested
	// share AND a disjoint sibling share are excluded too (shared records span scopes)
	is(t, e.svc.AcquireLock(ctx, innerShare), bookmarks.ErrLocked)
	is(t, e.svc.AcquireLock(ctx, otherShare), bookmarks.ErrLocked)
	noErr(t, e.svc.ReleaseLock(ctx, ss))
	noErr(t, e.svc.AcquireLock(ctx, otherShare))
	is(t, e.svc.AcquireLock(ctx, as), bookmarks.ErrLocked) // otherShare now holds the owner's collection
}

func TestTwoRecipientsOfTheSameShareSerialize(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	as := e.private(alice, identity.ModeEdit)
	aBar, _ := e.roots(as)
	shared := e.folder(as, aBar, "Shared")
	s1, _, _ := e.shared(alice, shared, e.user("bob"), identity.ModeEdit, identity.ModeEdit)
	s2, _, _ := e.shared(alice, shared, e.user("carol"), identity.ModeEdit, identity.ModeEdit)
	noErr(t, e.svc.AcquireLock(ctx, s1))
	is(t, e.svc.AcquireLock(ctx, s2), bookmarks.ErrLocked)
}
