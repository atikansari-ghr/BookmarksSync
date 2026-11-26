package bookmarks_test

import (
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// Every operation, aimed at another user's ids, must answer NotFound and change nothing.
func TestTwoUsersAreIsolatedOnEveryOperation(t *testing.T) {
	e := newEnv(t)
	alice := e.private(e.user("alice"), identity.ModeEdit)
	bob := e.private(e.user("bob"), identity.ModeEdit)
	aBar, aOther := e.roots(alice)
	bBar, _ := e.roots(bob)
	aFolder := e.folder(alice, aBar, "alice-secret")
	aBm := e.bm(alice, "https://alice-only.example/", "alice-bm", aFolder)
	bFolder := e.folder(bob, bBar, "bob-folder")
	bBm := e.bm(bob, "https://bob-only.example/", "bob-bm", bFolder)
	aliceBefore, bobBefore := e.snapshot(alice), e.snapshot(bob)

	_, err := e.svc.GetFolder(ctx, bob, aFolder)
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.GetFolder(ctx, bob, aBar)
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.Children(ctx, bob, aFolder, -1)
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.FolderHash(ctx, bob, aFolder)
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.CreateFolder(ctx, bob, aFolder, "intrusion")
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.UpdateFolder(ctx, bob, aFolder, nil, ptr("pwned"))
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.UpdateFolder(ctx, bob, bFolder, ptr(aFolder), nil) // move my folder into theirs
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.UpdateFolder(ctx, alice, aFolder, ptr(bFolder), nil) // and theirs into mine
	is(t, err, bookmarks.ErrNotFound)
	is(t, e.svc.DeleteFolder(ctx, bob, aFolder), bookmarks.ErrNotFound)
	is(t, e.svc.SetChildOrder(ctx, bob, aFolder, nil), bookmarks.ErrNotFound)
	_, err = e.svc.GetBookmark(ctx, bob, aBm.ID)
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.UpdateBookmark(ctx, bob, aBm.ID, bookmarks.BookmarkUpdate{Title: ptr("pwned")})
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.UpdateBookmark(ctx, bob, bBm.ID, bookmarks.BookmarkUpdate{Folders: &[]int64{aFolder}})
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.CreateBookmark(ctx, bob, bookmarks.BookmarkInput{URL: "https://x.example/", Title: "x", Folders: []int64{aFolder}})
	is(t, err, bookmarks.ErrNotFound)
	is(t, e.svc.RemovePlacement(ctx, bob, aFolder, aBm.ID), bookmarks.ErrNotFound)
	is(t, e.svc.DeleteBookmark(ctx, bob, aBm.ID), bookmarks.ErrNotFound)
	_, err = e.svc.ImportTree(ctx, bob, aFolder, []bookmarks.ImportNode{{Title: "a", URL: "https://i.example/"}})
	is(t, err, bookmarks.ErrNotFound)
	_ = aOther

	if e.snapshot(alice) != aliceBefore || e.snapshot(bob) != bobBefore {
		t.Error("cross-user attempts modified data")
	}
	// the other user's URL does not exist for me: same URL is a fresh private record
	mine := e.bm(bob, "https://alice-only.example/", "mine", bFolder)
	if mine.ID == aBm.ID {
		t.Error("bob's POST of alice's URL attached to alice's record")
	}
	list, _ := e.svc.ListBookmarks(ctx, bob, bookmarks.ListQuery{Page: -1, Search: []string{"alice"}})
	for _, b := range list {
		if b.ID == aBm.ID {
			t.Error("alice's bookmark leaked into bob's search")
		}
	}
}

func TestSameURLInTwoUsersCollectionsAreSeparateRecords(t *testing.T) {
	e := newEnv(t)
	alice := e.private(e.user("alice"), identity.ModeEdit)
	bob := e.private(e.user("bob"), identity.ModeEdit)
	aBar, _ := e.roots(alice)
	bBar, _ := e.roots(bob)
	a := e.bm(alice, "https://same.example/", "A", aBar)
	b := e.bm(bob, "https://same.example/", "B", bBar)
	if a.ID == b.ID {
		t.Fatal("two collections share one record")
	}
	got, _ := e.svc.GetBookmark(ctx, alice, a.ID)
	if got.Title != "A" {
		t.Errorf("alice title = %q", got.Title)
	}
}

func TestShareGrantValidation(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	aBar, _ := e.roots(as)
	f := e.folder(as, aBar, "shared")
	bs := e.private(bob, identity.ModeEdit)
	bBar, _ := e.roots(bs)
	bf := e.folder(bs, bBar, "bobs")

	_, err := e.svc.GrantShare(ctx, alice.ID, aBar, bob.ID, identity.ModeView) // canonical root
	is(t, err, bookmarks.ErrInvalid)
	_, err = e.svc.GrantShare(ctx, alice.ID, f, alice.ID, identity.ModeView) // self
	is(t, err, bookmarks.ErrInvalid)
	_, err = e.svc.GrantShare(ctx, alice.ID, f, 9999, identity.ModeView) // unknown user
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.GrantShare(ctx, alice.ID, bf, bob.ID, identity.ModeView) // not owner's folder
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.GrantShare(ctx, alice.ID, f, bob.ID, "write")
	is(t, err, bookmarks.ErrInvalid)
	_, err = e.svc.GrantShare(ctx, alice.ID, bookmarks.RootID, bob.ID, identity.ModeView)
	is(t, err, bookmarks.ErrInvalid)
	noErr(t, e.ids.SetActive(ctx, "bob", false))
	_, err = e.svc.GrantShare(ctx, alice.ID, f, bob.ID, identity.ModeView) // inactive recipient
	is(t, err, bookmarks.ErrInvalid)
	noErr(t, e.ids.SetActive(ctx, "bob", true))

	sh, err := e.svc.GrantShare(ctx, alice.ID, f, bob.ID, identity.ModeView)
	noErr(t, err)
	again, err := e.svc.GrantShare(ctx, alice.ID, f, bob.ID, identity.ModeEdit) // upsert changes mode
	noErr(t, err)
	if again.ID != sh.ID || again.Mode != identity.ModeEdit {
		t.Errorf("regrant = %+v, first %+v", again, sh)
	}
	list, err := e.svc.ListShares(ctx, alice.ID)
	noErr(t, err)
	if len(list) != 1 || list[0].RecipientID != bob.ID {
		t.Errorf("shares = %+v", list)
	}
	is(t, e.svc.RevokeShare(ctx, alice.ID, f, bob.ID), nil)
	is(t, e.svc.RevokeShare(ctx, alice.ID, f, bob.ID), bookmarks.ErrNotFound)
}

func TestShareScopeSeesOnlyTheSharedSubtreeAndRootAliasesToIt(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	aBar, aOther := e.roots(as)
	shared := e.folder(as, aBar, "Shared")
	inner := e.folder(as, shared, "Inner")
	secret := e.folder(as, aBar, "Secret")
	e.bm(as, "https://in-share.example/", "in", shared)
	e.bm(as, "https://in-inner.example/", "inner", inner)
	e.bm(as, "https://secret.example/", "secret", secret)
	bs := e.private(bob, identity.ModeEdit)
	bBar, _ := e.roots(bs)
	e.folder(bs, bBar, "bob-private")

	ss, _, _ := e.shared(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)

	kids, err := e.svc.Children(ctx, ss, bookmarks.RootID, -1)
	noErr(t, err)
	if len(kids) != 2 || kids[0].Title != "in" && kids[1].Title != "in" {
		t.Fatalf("share root children = %+v", kids)
	}
	// real id of share root works as alias, bookmarks report -1 as their folder
	viaReal, err := e.svc.Children(ctx, ss, shared, -1)
	noErr(t, err)
	if len(viaReal) != len(kids) {
		t.Errorf("real id and alias differ")
	}
	list, err := e.svc.ListBookmarks(ctx, ss, bookmarks.ListQuery{Page: -1})
	noErr(t, err)
	if len(list) != 2 {
		t.Fatalf("share lists %d bookmarks, want 2: %+v", len(list), list)
	}
	for _, b := range list {
		if strings.Contains(b.URL, "secret") {
			t.Error("share leaked a bookmark outside the shared subtree")
		}
		if b.URL == "https://in-share.example/" && (len(b.Folders) != 1 || b.Folders[0] != bookmarks.RootID) {
			t.Errorf("share-root placement serialized as %v, want [-1]", b.Folders)
		}
	}
	// out-of-scope ids never resolve, in any operation
	for _, id := range []int64{aBar, aOther, secret} {
		_, err = e.svc.GetFolder(ctx, ss, id)
		is(t, err, bookmarks.ErrNotFound)
		_, err = e.svc.Children(ctx, ss, id, 1)
		is(t, err, bookmarks.ErrNotFound)
		_, err = e.svc.CreateFolder(ctx, ss, id, "x")
		is(t, err, bookmarks.ErrNotFound)
	}
	// the share is not mounted in bob's private tree
	bobRoot, _ := e.svc.Children(ctx, bs, bookmarks.RootID, -1)
	if strings.Contains(fmtTitles(bobRoot), "Shared") || strings.Contains(fmtTitles(bobRoot), "in-share") {
		t.Errorf("incoming share appears in the private adapter root: %s", fmtTitles(bobRoot))
	}
	// alice's own view is unchanged and complete
	own, _ := e.svc.Children(ctx, as, bookmarks.RootID, -1)
	if !strings.Contains(fmtTitles(own), "Secret") || !strings.Contains(fmtTitles(own), "Inner") {
		t.Errorf("owner lost data: %s", fmtTitles(own))
	}
}

func fmtTitles(ns []bookmarks.Node) string {
	var sb strings.Builder
	var walk func([]bookmarks.Node)
	walk = func(ns []bookmarks.Node) {
		for _, n := range ns {
			sb.WriteString(n.Title + ";")
			walk(n.Children)
		}
	}
	walk(ns)
	return sb.String()
}

func TestShareScopeCannotTouchItsRootOrEscape(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	aBar, _ := e.roots(as)
	shared := e.folder(as, aBar, "Shared")
	inner := e.folder(as, shared, "Inner")
	secret := e.folder(as, aBar, "Secret")
	ss, _, _ := e.shared(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	before := e.snapshot(as)

	// the share root: not renamable, movable or deletable by the recipient (alias or real id)
	for _, id := range []int64{bookmarks.RootID, shared} {
		_, err := e.svc.UpdateFolder(ctx, ss, id, nil, ptr("renamed"))
		is(t, err, bookmarks.ErrInvalid)
		is(t, e.svc.DeleteFolder(ctx, ss, id), bookmarks.ErrInvalid)
	}
	// moving out of the share or into private areas is not possible
	_, err := e.svc.UpdateFolder(ctx, ss, inner, ptr(secret), nil)
	is(t, err, bookmarks.ErrNotFound)
	_, err = e.svc.UpdateFolder(ctx, ss, inner, ptr(aBar), nil)
	is(t, err, bookmarks.ErrNotFound)
	// inside the share it works, including to the root alias
	deeper := e.folder(ss, bookmarks.RootID, "FromRecipient")
	_, err = e.svc.UpdateFolder(ctx, ss, inner, ptr(deeper), nil)
	noErr(t, err)
	if e.snapshot(as) == before {
		t.Error("recipient edit inside the share should be visible to the owner")
	}
	got, _ := e.svc.GetFolder(ctx, as, deeper)
	if got.ParentID != shared {
		t.Errorf("recipient's folder parent as seen by owner = %d, want real id %d", got.ParentID, shared)
	}
}

func TestShareUpdatesPreserveHiddenPlacementsAndFilterFolders(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	aBar, aOther := e.roots(as)
	shared := e.folder(as, aBar, "Shared")
	sub := e.folder(as, shared, "Sub")
	hidden := e.folder(as, aOther, "Hidden")
	b := e.bm(as, "https://both.example/", "Both", shared, hidden)
	ss, _, _ := e.shared(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)

	got, err := e.svc.GetBookmark(ctx, ss, b.ID)
	noErr(t, err)
	if len(got.Folders) != 1 || got.Folders[0] != bookmarks.RootID {
		t.Fatalf("folders[] leaked or wrong: %v (hidden=%d)", got.Folders, hidden)
	}
	list, _ := e.svc.ListBookmarks(ctx, ss, bookmarks.ListQuery{Page: -1})
	if len(list) != 1 || len(list[0].Folders) != 1 {
		t.Fatalf("list folders leaked: %+v", list)
	}

	// recipient moves its placement into Sub: hidden placement must survive
	upd, err := e.svc.UpdateBookmark(ctx, ss, b.ID, bookmarks.BookmarkUpdate{Title: ptr("Renamed in share"), Folders: &[]int64{sub}})
	noErr(t, err)
	if len(upd.Folders) != 1 || upd.Folders[0] != sub {
		t.Errorf("folders = %v", upd.Folders)
	}
	owner, err := e.svc.GetBookmark(ctx, as, b.ID)
	noErr(t, err)
	has := map[int64]bool{}
	for _, f := range owner.Folders {
		has[f] = true
	}
	if !has[hidden] || !has[sub] || has[shared] || len(owner.Folders) != 2 {
		t.Errorf("owner placements = %v, want hidden+sub only", owner.Folders)
	}
	// recipient can reference only visible folders
	_, err = e.svc.UpdateBookmark(ctx, ss, b.ID, bookmarks.BookmarkUpdate{Folders: &[]int64{hidden}})
	is(t, err, bookmarks.ErrNotFound)
	// deleting the last visible placement through the share leaves the hidden one alive
	noErr(t, e.svc.RemovePlacement(ctx, ss, sub, b.ID))
	owner, err = e.svc.GetBookmark(ctx, as, b.ID)
	noErr(t, err)
	if len(owner.Folders) != 1 || owner.Folders[0] != hidden {
		t.Errorf("owner placements after share delete = %v", owner.Folders)
	}
	_, err = e.svc.GetBookmark(ctx, ss, b.ID)
	is(t, err, bookmarks.ErrNotFound)
	// removing a placement outside the scope is not found
	is(t, e.svc.RemovePlacement(ctx, ss, hidden, b.ID), bookmarks.ErrNotFound)
}

func TestShareScopeURLEditOfRecordWithHiddenPlacementsKeepsThem(t *testing.T) {
	// Fix round 1 (ruling): an already-visible shared record's URL may be edited through
	// the share like its title/tags; the hidden placement rows must survive. Detailed
	// assertions: TestShareScopeMayEditURLOfVisibleRecordAndHiddenPlacementsSurvive.
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	aBar, aOther := e.roots(as)
	shared := e.folder(as, aBar, "Shared")
	b := e.bm(as, "https://both.example/", "Both", shared, aOther)
	ss, _, _ := e.shared(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	_, err := e.svc.UpdateBookmark(ctx, ss, b.ID, bookmarks.BookmarkUpdate{URL: ptr("https://renamed.example/")})
	noErr(t, err)
	got, _ := e.svc.GetBookmark(ctx, as, b.ID)
	if got.URL != "https://renamed.example/" || len(got.Folders) != 2 {
		t.Errorf("owner view = %+v", got)
	}
	// a bookmark entirely inside the share may change URL
	only := e.bm(as, "https://only.example/", "Only", shared)
	_, err = e.svc.UpdateBookmark(ctx, ss, only.ID, bookmarks.BookmarkUpdate{URL: ptr("https://only2.example/")})
	noErr(t, err)
}

func TestViewGrantAndViewCredentialAreReadOnlyAndRevocationIsImmediate(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	aBar, _ := e.roots(as)
	shared := e.folder(as, aBar, "Shared")
	e.bm(as, "https://s.example/", "s", shared)

	viewScope, cred, share := e.shared(alice, shared, bob, identity.ModeView, identity.ModeEdit)
	if viewScope.Writable {
		t.Fatal("view grant with edit credential must be read-only")
	}
	_, err := e.svc.CreateFolder(ctx, viewScope, bookmarks.RootID, "nope")
	is(t, err, bookmarks.ErrForbidden)

	// upgrade the grant: takes effect immediately for the same credential
	_, err = e.svc.GrantShare(ctx, alice.ID, shared, bob.ID, identity.ModeEdit)
	noErr(t, err)
	edit := e.scopeOf(bob, cred)
	if !edit.Writable {
		t.Fatal("upgraded grant not reflected")
	}
	_, err = e.svc.CreateFolder(ctx, edit, bookmarks.RootID, "ok")
	noErr(t, err)
	// a view credential stays read-only even with an edit grant
	viewCred, _, err := e.ids.CreateCredential(ctx, identity.NewCredential{UserID: bob.ID, Kind: identity.KindShare, ShareID: share.ID, Mode: identity.ModeView})
	noErr(t, err)
	if e.scopeOf(bob, viewCred).Writable {
		t.Error("view credential became writable through an edit grant")
	}
	// downgrade applies immediately
	_, err = e.svc.GrantShare(ctx, alice.ID, shared, bob.ID, identity.ModeView)
	noErr(t, err)
	down := e.scopeOf(bob, cred)
	if down.Writable {
		t.Fatal("downgrade not reflected")
	}
	// revoke: credential no longer resolves
	noErr(t, e.svc.RevokeShare(ctx, alice.ID, shared, bob.ID))
	_, err = e.svc.ResolveScope(ctx, identity.Principal{CredentialID: cred.ID, User: bob, Kind: cred.Kind, ShareID: cred.ShareID, Mode: cred.Mode})
	is(t, err, identity.ErrUnauthorized)
}

func TestShareScopeFailsWhenRecipientInactiveOrFolderTrashed(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	aBar, _ := e.roots(as)
	shared := e.folder(as, aBar, "Shared")
	_, cred, _ := e.shared(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	p := identity.Principal{CredentialID: cred.ID, User: bob, Kind: cred.Kind, ShareID: cred.ShareID, Mode: cred.Mode}

	// a credential cannot be replayed by another user
	carol := e.user("carol")
	stolen := p
	stolen.User = carol
	_, err := e.svc.ResolveScope(ctx, stolen)
	is(t, err, identity.ErrUnauthorized)

	noErr(t, e.svc.DeleteFolder(ctx, as, shared)) // owner trashes the shared folder
	_, err = e.svc.ResolveScope(ctx, p)
	is(t, err, identity.ErrUnauthorized)
}

func TestPrivateScopeNeverIncludesAnotherUsersShareCollection(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	aBar, _ := e.roots(as)
	shared := e.folder(as, aBar, "Shared")
	e.bm(as, "https://shared-bm.example/", "sb", shared)
	e.shared(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	bs := e.private(bob, identity.ModeEdit)
	list, err := e.svc.ListBookmarks(ctx, bs, bookmarks.ListQuery{Page: -1})
	noErr(t, err)
	if len(list) != 0 {
		t.Errorf("bob's private scope lists %d bookmarks from alice's share", len(list))
	}
	_, err = e.svc.GetFolder(ctx, bs, shared)
	is(t, err, bookmarks.ErrNotFound)
}
