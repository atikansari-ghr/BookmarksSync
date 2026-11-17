package bookmarks_test

import (
	"errors"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// otherRoot returns the id of the owner's canonical "Other Bookmarks" folder.
func (e *env) otherRoot(sc bookmarks.Scope) int64 {
	e.t.Helper()
	kids, err := e.svc.Children(ctx, sc, bookmarks.RootID, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, k := range kids {
		if k.Type == "folder" && k.Title == "Other Bookmarks" {
			return k.ID
		}
	}
	e.t.Fatal("no Other Bookmarks")
	return 0
}

func (e *env) apiPrivate(u identity.User) bookmarks.Scope {
	e.t.Helper()
	sc, err := e.svc.PrivateScope(ctx, u.ID, true)
	if err != nil {
		e.t.Fatal(err)
	}
	return sc
}

func TestAPIScopesComeFromLiveState(t *testing.T) {
	e := newEnv(t)
	alice, bob, carol := e.user("alice"), e.user("bob"), e.user("carol")
	asc := e.apiPrivate(alice)
	if asc.OwnerID != alice.ID || asc.RootFolderID != 0 || !asc.Writable || asc.Actor != alice.ID || asc.Holder != 0 {
		t.Fatalf("private scope = %+v", asc)
	}
	ro, _ := e.svc.PrivateScope(ctx, alice.ID, false)
	if ro.Writable {
		t.Fatal("read-only private scope is writable")
	}
	f, _ := e.svc.CreateFolder(ctx, asc, e.otherRoot(asc), "Shared")
	sh, err := e.svc.GrantShare(ctx, alice.ID, f.ID, bob.ID, identity.ModeView)
	if err != nil {
		t.Fatal(err)
	}
	bsc, err := e.svc.ShareScopeFor(ctx, bob.ID, sh.ID, true)
	if err != nil || bsc.OwnerID != alice.ID || bsc.RootFolderID != f.ID || bsc.Writable || bsc.Actor != bob.ID {
		t.Fatalf("share scope = %+v, %v", bsc, err)
	}
	if _, err := e.svc.ShareScopeFor(ctx, carol.ID, sh.ID, true); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("non-recipient got a share scope: %v", err)
	}
	if _, err := e.svc.ShareScopeFor(ctx, alice.ID, sh.ID, true); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("owner got the recipient's share scope: %v", err)
	}
	// View grant: writes refused.
	if _, err := e.svc.CreateFolder(ctx, bsc, bookmarks.RootID, "x"); !errors.Is(err, bookmarks.ErrForbidden) {
		t.Fatalf("write through a view grant: %v", err)
	}
	// Upgrade to edit: writes allowed, but a caller without write permission stays read-only.
	if _, err := e.svc.GrantShare(ctx, alice.ID, f.ID, bob.ID, identity.ModeEdit); err != nil {
		t.Fatal(err)
	}
	bsc, _ = e.svc.ShareScopeFor(ctx, bob.ID, sh.ID, true)
	if _, err := e.svc.CreateFolder(ctx, bsc, bookmarks.RootID, "inside"); err != nil {
		t.Fatalf("write through an edit grant: %v", err)
	}
	if roShare, _ := e.svc.ShareScopeFor(ctx, bob.ID, sh.ID, false); roShare.Writable {
		t.Fatal("allowWrite=false still writable")
	}
	// A scope resolved before a downgrade or revoke cannot write afterwards.
	stale := bsc
	if _, err := e.svc.GrantShare(ctx, alice.ID, f.ID, bob.ID, identity.ModeView); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateFolder(ctx, stale, bookmarks.RootID, "late"); !errors.Is(err, bookmarks.ErrForbidden) {
		t.Fatalf("stale scope wrote after downgrade: %v", err)
	}
	if err := e.svc.RevokeShare(ctx, alice.ID, f.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateFolder(ctx, stale, bookmarks.RootID, "late"); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("stale scope wrote after revoke: %v", err)
	}
	// A suspended actor's scope cannot write, even its private collection.
	if err := e.ids.SetActive(ctx, "alice", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CreateFolder(ctx, asc, bookmarks.RootID, "x"); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatalf("suspended actor: %v", err)
	}
	// A trashed shared folder yields no scope.
	dsc := e.apiPrivate(carol)
	cf, _ := e.svc.CreateFolder(ctx, dsc, e.otherRoot(dsc), "C")
	csh, _ := e.svc.GrantShare(ctx, carol.ID, cf.ID, bob.ID, identity.ModeEdit)
	if err := e.svc.DeleteFolder(ctx, dsc, cf.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ShareScopeFor(ctx, bob.ID, csh.ID, true); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("scope for a trashed share: %v", err)
	}
}

func TestListTagsAndExactTagFilterRespectScope(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	sc := e.apiPrivate(alice)
	other := e.otherRoot(sc)
	shared, _ := e.svc.CreateFolder(ctx, sc, other, "S")
	tg := func(v ...string) *[]string { return &v }
	e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://a.test", Title: "A", Folders: []int64{other}, Tags: tg("go", "news")})
	e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://b.test", Title: "B", Folders: []int64{shared.ID}, Tags: tg("go")})
	e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://c.test", Title: "C", Folders: []int64{shared.ID}, Tags: tg("golang")})
	tags, err := e.svc.ListTags(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	want := []bookmarks.TagCount{{Name: "go", Count: 2}, {Name: "golang", Count: 1}, {Name: "news", Count: 1}}
	if len(tags) != 3 || tags[0] != want[0] || tags[1] != want[1] || tags[2] != want[2] {
		t.Fatalf("tags = %+v", tags)
	}
	sh, _ := e.svc.GrantShare(ctx, alice.ID, shared.ID, bob.ID, identity.ModeView)
	bsc, _ := e.svc.ShareScopeFor(ctx, bob.ID, sh.ID, false)
	tags, _ = e.svc.ListTags(ctx, bsc)
	if len(tags) != 2 || tags[0].Name != "go" || tags[0].Count != 1 || tags[1].Name != "golang" {
		t.Fatalf("share-scope tags = %+v (must not see 'news')", tags)
	}
	got, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Limit: 10, Tags: []string{"go"}})
	if err != nil || len(got) != 2 {
		t.Fatalf("exact tag filter = %d, %v (must not match 'golang')", len(got), err)
	}
	got, _ = e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Limit: 10, Tags: []string{"go", "news"}})
	if len(got) != 1 || got[0].URL != "https://a.test" {
		t.Fatalf("all tags must match: %+v", got)
	}
	if _, err := e.svc.ListBookmarks(ctx, sc, bookmarks.ListQuery{Limit: 10, Tags: make([]string, bookmarks.MaxSearchTerms+1)}); !errors.Is(err, bookmarks.ErrInvalid) {
		t.Fatalf("too many tag filters: %v", err)
	}
}

func TestTrashListRestorePurge(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	sc := e.apiPrivate(alice)
	other := e.otherRoot(sc)
	top, _ := e.svc.CreateFolder(ctx, sc, other, "Top")
	sub, _ := e.svc.CreateFolder(ctx, sc, top.ID, "Sub")
	inSub, _ := e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://in-sub.test", Title: "in sub", Folders: []int64{sub.ID}})
	loose, _ := e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://loose.test", Title: "loose", Folders: []int64{other}})
	if err := e.svc.DeleteFolder(ctx, sc, top.ID); err != nil {
		t.Fatal(err)
	}
	e.advance(time.Minute)
	if err := e.svc.DeleteBookmark(ctx, sc, loose.ID); err != nil {
		t.Fatal(err)
	}
	items, err := e.svc.ListTrash(ctx, sc, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("trash = %+v (only the top folder and the loose bookmark are entries)", items)
	}
	byType := map[string]bookmarks.TrashItem{}
	for _, it := range items {
		byType[it.Type] = it
	}
	if f := byType["folder"]; f.ID != top.ID || f.Title != "Top" || !f.PurgeAt.Equal(f.DeletedAt.Add(bookmarks.TrashRetention)) || bookmarks.TrashRetention != 30*24*time.Hour {
		t.Fatalf("folder entry = %+v", f)
	}
	if b := byType["bookmark"]; b.ID != loose.ID || b.URL != "https://loose.test" {
		t.Fatalf("bookmark entry = %+v", b)
	}
	// Bob cannot see or touch Alice's trash.
	bsc := e.apiPrivate(bob)
	if bi, _ := e.svc.ListTrash(ctx, bsc, 10, 0); len(bi) != 0 {
		t.Fatalf("bob sees %d trash items", len(bi))
	}
	if _, err := e.svc.RestoreTrash(ctx, bsc, "folder", top.ID); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("bob restored alice's folder: %v", err)
	}
	if err := e.svc.PurgeTrash(ctx, bsc, "bookmark", loose.ID); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("bob purged alice's bookmark: %v", err)
	}
	// Restore the folder: subtree, placement and bookmark come back under the original parent.
	if _, err := e.svc.RestoreTrash(ctx, sc, "folder", top.ID); err != nil {
		t.Fatal(err)
	}
	if f, err := e.svc.GetFolder(ctx, sc, sub.ID); err != nil || f.ParentID != top.ID {
		t.Fatalf("sub after restore = %+v, %v", f, err)
	}
	if b, err := e.svc.GetBookmark(ctx, sc, inSub.ID); err != nil || len(b.Folders) != 1 || b.Folders[0] != sub.ID {
		t.Fatalf("bookmark after restore = %+v, %v", b, err)
	}
	if f, _ := e.svc.GetFolder(ctx, sc, top.ID); f.ParentID != other {
		t.Fatalf("top restored under %d, want %d", f.ParentID, other)
	}
	// A share (and a share credential) inside the subtree, purged with it later.
	sh, err := e.svc.GrantShare(ctx, alice.ID, sub.ID, bob.ID, identity.ModeEdit)
	if err != nil {
		t.Fatal(err)
	}
	cred, _, err := e.ids.CreateCredential(ctx, identity.NewCredential{UserID: bob.ID, Kind: identity.KindShare, ShareID: sh.ID, Mode: identity.ModeEdit})
	if err != nil {
		t.Fatal(err)
	}
	// Restoring a child whose parent is trashed goes to Other Bookmarks.
	child, _ := e.svc.CreateFolder(ctx, sc, top.ID, "Child")
	if err := e.svc.DeleteFolder(ctx, sc, child.ID); err != nil {
		t.Fatal(err)
	}
	e.advance(time.Minute)
	if err := e.svc.DeleteFolder(ctx, sc, top.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.RestoreTrash(ctx, sc, "folder", child.ID); err != nil {
		t.Fatal(err)
	}
	if f, _ := e.svc.GetFolder(ctx, sc, child.ID); f.ParentID != other {
		t.Fatalf("orphan restored under %d, want Other (%d)", f.ParentID, other)
	}
	// Restoring a bookmark whose folder is gone goes to Other Bookmarks too.
	if _, err := e.svc.RestoreTrash(ctx, sc, "bookmark", loose.ID); err != nil {
		t.Fatal(err)
	}
	if b, _ := e.svc.GetBookmark(ctx, sc, loose.ID); len(b.Folders) != 1 || b.Folders[0] != other {
		t.Fatalf("loose restored to %v", b.Folders)
	}
	// Restoring something that is not in the trash is not found.
	if _, err := e.svc.RestoreTrash(ctx, sc, "bookmark", loose.ID); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("restore of a live bookmark: %v", err)
	}
	if _, err := e.svc.RestoreTrash(ctx, sc, "widget", loose.ID); !errors.Is(err, bookmarks.ErrInvalid) {
		t.Fatalf("unknown type: %v", err)
	}
	// Purge the trashed top folder (with its shares and share credentials).
	if err := e.svc.PurgeTrash(ctx, sc, "folder", top.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	e.db.SQL().QueryRow(`SELECT (SELECT count(*) FROM folders WHERE id IN (?1, ?2)) + (SELECT count(*) FROM shares WHERE id = ?3) +
		(SELECT count(*) FROM credentials WHERE id = ?4) + (SELECT count(*) FROM bookmarks WHERE id = ?5)`,
		top.ID, sub.ID, sh.ID, cred.ID, inSub.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("%d rows survived the purge", n)
	}
	if _, err := e.svc.RestoreTrash(ctx, sc, "folder", top.ID); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("restore after purge: %v", err)
	}
	// Live folders and bookmarks cannot be purged.
	if err := e.svc.PurgeTrash(ctx, sc, "folder", child.ID); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("purge of a live folder: %v", err)
	}
	// Share scopes and read-only scopes cannot use the trash.
	shared, _ := e.svc.CreateFolder(ctx, sc, other, "Shared2")
	sh2, _ := e.svc.GrantShare(ctx, alice.ID, shared.ID, bob.ID, identity.ModeEdit)
	bshare, _ := e.svc.ShareScopeFor(ctx, bob.ID, sh2.ID, true)
	if _, err := e.svc.ListTrash(ctx, bshare, 10, 0); !errors.Is(err, bookmarks.ErrForbidden) {
		t.Fatalf("trash through a share scope: %v", err)
	}
	ro, _ := e.svc.PrivateScope(ctx, alice.ID, false)
	if err := e.svc.EmptyTrash(ctx, ro); !errors.Is(err, bookmarks.ErrForbidden) {
		t.Fatalf("empty through a read-only scope: %v", err)
	}
}

func TestEmptyAndExpiredTrashPurge(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	sc, bsc := e.apiPrivate(alice), e.apiPrivate(bob)
	f1, _ := e.svc.CreateFolder(ctx, sc, e.otherRoot(sc), "Old")
	b1, _ := e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://old.test", Title: "old", Folders: []int64{f1.ID}})
	bf, _ := e.svc.CreateFolder(ctx, bsc, e.otherRoot(bsc), "BobOld")
	e.svc.DeleteFolder(ctx, sc, f1.ID)
	e.svc.DeleteFolder(ctx, bsc, bf.ID)
	e.advance(20 * 24 * time.Hour)
	f2, _ := e.svc.CreateFolder(ctx, sc, e.otherRoot(sc), "Recent")
	e.svc.DeleteFolder(ctx, sc, f2.ID)
	e.advance(11 * 24 * time.Hour) // f1/bf are now 31 days old, f2 11 days
	n, err := e.svc.PurgeExpiredTrash(ctx)
	if err != nil || n < 2 {
		t.Fatalf("expired purge = %d, %v", n, err)
	}
	items, _ := e.svc.ListTrash(ctx, sc, 10, 0)
	if len(items) != 1 || items[0].ID != f2.ID {
		t.Fatalf("after expiry purge = %+v", items)
	}
	if _, err := e.svc.RestoreTrash(ctx, sc, "folder", f1.ID); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("expired folder still restorable: %v", err)
	}
	var left int
	e.db.SQL().QueryRow(`SELECT count(*) FROM bookmarks WHERE id = ?`, b1.ID).Scan(&left)
	if left != 0 {
		t.Fatal("expired bookmark survived")
	}
	if err := e.svc.EmptyTrash(ctx, sc); err != nil {
		t.Fatal(err)
	}
	if items, _ := e.svc.ListTrash(ctx, sc, 10, 0); len(items) != 0 {
		t.Fatalf("trash not empty: %+v", items)
	}
}

func TestStatsAreAggregateOnly(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	sc := e.apiPrivate(alice)
	f, _ := e.svc.CreateFolder(ctx, sc, e.otherRoot(sc), "F")
	tags := []string{"t1"}
	e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://x.test", Title: "X", Folders: []int64{f.ID}, Tags: &tags})
	e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://y.test", Title: "Y", Folders: []int64{f.ID}})
	gone, _ := e.svc.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://z.test", Title: "Z", Folders: []int64{f.ID}})
	e.svc.DeleteBookmark(ctx, sc, gone.ID)
	stats, err := e.svc.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a := stats[alice.ID]
	if a.Bookmarks != 2 || a.Folders != 1 || a.StorageBytes < int64(len("https://x.test")+len("X")+len("t1")) {
		t.Fatalf("alice stats = %+v", a)
	}
	if b := stats[bob.ID]; b.Bookmarks != 0 || b.Folders != 0 {
		t.Fatalf("bob stats = %+v", b)
	}
}

func TestExportTreeAndImportTags(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	sc := e.apiPrivate(alice)
	other := e.otherRoot(sc)
	_, err := e.svc.ImportTree(ctx, sc, other, []bookmarks.ImportNode{
		{Folder: true, Title: "Dev", Children: []bookmarks.ImportNode{
			{Title: "Go", URL: "https://go.dev", Tags: []string{"lang", "google"}},
		}},
		{Title: "Top", URL: "https://top.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ImportTree(ctx, sc, other, []bookmarks.ImportNode{{Title: "Bad", URL: "https://bad.test", Tags: []string{""}}}); !errors.Is(err, bookmarks.ErrInvalid) {
		t.Fatalf("empty tag imported: %v", err)
	}
	tree, err := e.svc.ExportTree(ctx, sc, other)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 2 || !tree[0].Folder || tree[0].Title != "Dev" || len(tree[0].Children) != 1 {
		t.Fatalf("export = %+v", tree)
	}
	g := tree[0].Children[0]
	if g.URL != "https://go.dev" || len(g.Tags) != 2 || g.Tags[0] != "google" || g.Tags[1] != "lang" {
		t.Fatalf("exported bookmark = %+v", g)
	}
	whole, err := e.svc.ExportTree(ctx, sc, bookmarks.RootID)
	if err != nil || len(whole) != 2 || whole[0].Title != "Bookmarks Bar" || whole[1].Title != "Other Bookmarks" {
		t.Fatalf("whole export = %+v, %v", whole, err)
	}
}

func TestShareListingsCarryFolderTitles(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	sc := e.apiPrivate(alice)
	f, _ := e.svc.CreateFolder(ctx, sc, e.otherRoot(sc), "Team")
	sh, _ := e.svc.GrantShare(ctx, alice.ID, f.ID, bob.ID, identity.ModeEdit)
	owned, _ := e.svc.ListShares(ctx, alice.ID)
	if len(owned) != 1 || owned[0].FolderTitle != "Team" {
		t.Fatalf("owned = %+v", owned)
	}
	in, err := e.svc.ListIncomingShares(ctx, bob.ID)
	if err != nil || len(in) != 1 || in[0].ID != sh.ID || in[0].OwnerID != alice.ID || in[0].FolderTitle != "Team" {
		t.Fatalf("incoming = %+v, %v", in, err)
	}
	if got, err := e.svc.ShareByID(ctx, sh.ID); err != nil || got.FolderID != f.ID || got.RecipientID != bob.ID {
		t.Fatalf("by id = %+v, %v", got, err)
	}
	if _, err := e.svc.ShareByID(ctx, 99999); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Fatalf("unknown share: %v", err)
	}
	// A trashed shared folder is not listed as incoming.
	e.svc.DeleteFolder(ctx, sc, f.ID)
	if in, _ := e.svc.ListIncomingShares(ctx, bob.ID); len(in) != 0 {
		t.Fatalf("trashed share listed: %+v", in)
	}
}
