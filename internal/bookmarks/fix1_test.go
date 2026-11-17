package bookmarks_test

import (
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// ---- fix round 1, finding 1: one lock key per owner collection ---------------

func TestSiblingShareLocksSerializeUnderOneOwner(t *testing.T) {
	e := newEnv(t)
	alice, bob, carol := e.user("alice"), e.user("bob"), e.user("carol")
	as := e.private(alice, identity.ModeEdit)
	aBar, _ := e.roots(as)
	s1, s2 := e.folder(as, aBar, "S1"), e.folder(as, aBar, "S2")
	ss1, _, _ := e.shared(alice, s1, bob, identity.ModeEdit, identity.ModeEdit)
	ss2, _, _ := e.shared(alice, s2, carol, identity.ModeEdit, identity.ModeEdit)
	// one record placed in both siblings: a write through either share reaches the other
	rec := e.bm(as, "https://rec.example/", "rec", s1, s2)

	noErr(t, e.svc.AcquireLock(ctx, ss1))
	is(t, e.svc.AcquireLock(ctx, ss2), bookmarks.ErrLocked)
	_, err := e.svc.UpdateBookmark(ctx, ss2, rec.ID, bookmarks.BookmarkUpdate{Title: ptr("sneaky")})
	is(t, err, bookmarks.ErrLocked)
	_, err = e.svc.CreateFolder(ctx, ss2, bookmarks.RootID, "x")
	is(t, err, bookmarks.ErrLocked)
	got, _ := e.svc.GetBookmark(ctx, as, rec.ID)
	if got.Title != "rec" {
		t.Errorf("record changed under a sibling lock: %+v", got)
	}
	// a different owner is unaffected
	dave := e.private(e.user("dave"), identity.ModeEdit)
	noErr(t, e.svc.AcquireLock(ctx, dave))
	// releasing frees the sibling
	noErr(t, e.svc.ReleaseLock(ctx, ss1))
	noErr(t, e.svc.AcquireLock(ctx, ss2))
}

// ---- finding 2: no same-folder duplicate URL placements ----------------------

func (e *env) assertNoSameFolderDuplicates() {
	e.t.Helper()
	rows, err := e.db.SQL().QueryContext(ctx, `
		SELECT p.folder_id, b.url, COUNT(*) FROM placements p JOIN bookmarks b ON b.id = p.bookmark_id
		WHERE p.deleted_at IS NULL AND b.deleted_at IS NULL
		GROUP BY p.folder_id, b.url HAVING COUNT(*) > 1`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var f, n int64
		var u string
		_ = rows.Scan(&f, &u, &n)
		e.t.Errorf("folder %d holds %d live placements of %s", f, n, u)
	}
}

func (e *env) livePlacements() int {
	e.t.Helper()
	var n int
	if err := e.db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM placements WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// dupSetup: the owner holds private record A (URL X) in folder f1; the recipient of
// the shared folder s then POSTs X and, by the visibility ruling, gets separate record B.
func dupSetup(t *testing.T) (e *env, as, ss bookmarks.Scope, a, b bookmarks.Bookmark, f1, s int64) {
	t.Helper()
	e = newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as = e.private(alice, identity.ModeEdit)
	aBar, aOther := e.roots(as)
	s = e.folder(as, aBar, "Shared")
	f1 = e.folder(as, aOther, "F1")
	a = e.bm(as, "https://dup.example/x", "private", f1)
	ss, _, _ = e.shared(alice, s, bob, identity.ModeEdit, identity.ModeEdit)
	b = e.bm(ss, "https://dup.example/x", "mine", bookmarks.RootID)
	if a.ID == b.ID {
		t.Fatal("precondition: records must be separate")
	}
	return
}

func TestOwnerCannotCreateSameFolderDuplicateByPUT(t *testing.T) {
	e, as, _, a, b, f1, s := dupSetup(t)
	before := e.livePlacements()
	_, err := e.svc.UpdateBookmark(ctx, as, b.ID, bookmarks.BookmarkUpdate{Folders: &[]int64{s, f1}})
	is(t, err, bookmarks.ErrConflict)
	_, err = e.svc.UpdateBookmark(ctx, as, a.ID, bookmarks.BookmarkUpdate{Folders: &[]int64{f1, s}})
	is(t, err, bookmarks.ErrConflict)
	if got := e.livePlacements(); got != before {
		t.Errorf("placements %d -> %d after refused updates", before, got)
	}
	if ga, _ := e.svc.GetBookmark(ctx, as, a.ID); len(ga.Folders) != 1 || ga.Folders[0] != f1 {
		t.Errorf("A folders = %v", ga.Folders)
	}
	if gb, _ := e.svc.GetBookmark(ctx, as, b.ID); len(gb.Folders) != 1 || gb.Folders[0] != s {
		t.Errorf("B folders = %v", gb.Folders)
	}
	e.assertNoSameFolderDuplicates()
}

func TestOwnerPOSTAcrossCollidedRecordsConflictsInsteadOfDuplicating(t *testing.T) {
	e, as, _, a, b, f1, s := dupSetup(t)
	before := e.livePlacements()
	_, err := e.svc.CreateBookmark(ctx, as, bookmarks.BookmarkInput{URL: "https://dup.example/x", Title: "t", Folders: []int64{f1, s}})
	is(t, err, bookmarks.ErrConflict)
	if got := e.livePlacements(); got != before {
		t.Errorf("placements %d -> %d after refused POST", before, got)
	}
	// single-folder POSTs (the only shape stock floccus sends) keep succeeding and are deterministic
	g1, err := e.svc.CreateBookmark(ctx, as, bookmarks.BookmarkInput{URL: "https://dup.example/x", Title: "t", Folders: []int64{f1}})
	noErr(t, err)
	if g1.ID != a.ID {
		t.Errorf("POST into f1 returned %d, want the record already there (%d)", g1.ID, a.ID)
	}
	g2, err := e.svc.CreateBookmark(ctx, as, bookmarks.BookmarkInput{URL: "https://dup.example/x", Title: "t", Folders: []int64{s}})
	noErr(t, err)
	if g2.ID != b.ID {
		t.Errorf("POST into s returned %d, want %d", g2.ID, b.ID)
	}
	other := e.folder(as, f1, "F2")
	g3, err := e.svc.CreateBookmark(ctx, as, bookmarks.BookmarkInput{URL: "https://dup.example/x", Title: "t", Folders: []int64{other}})
	noErr(t, err)
	if g3.ID != min(a.ID, b.ID) {
		t.Errorf("POST into a free folder returned %d, want the oldest record %d", g3.ID, min(a.ID, b.ID))
	}
	e.assertNoSameFolderDuplicates()
}

func TestRecipientFlowsAfterCollisionStayUnchanged(t *testing.T) {
	e, _, ss, a, b, _, _ := dupSetup(t)
	again, err := e.svc.CreateBookmark(ctx, ss, bookmarks.BookmarkInput{URL: "https://dup.example/x", Title: "mine", Folders: []int64{bookmarks.RootID}})
	noErr(t, err)
	if again.ID != b.ID || again.ID == a.ID {
		t.Errorf("recipient POST returned %d, want own record %d", again.ID, b.ID)
	}
	_, err = e.svc.UpdateBookmark(ctx, ss, b.ID, bookmarks.BookmarkUpdate{Folders: &[]int64{bookmarks.RootID}})
	noErr(t, err)
	e.assertNoSameFolderDuplicates()
}

// Fix round 2 ruling: collision checks consider only what the writer can see. A URL that
// exists only in a folder hidden from the writer must behave exactly like a fresh URL (no
// 409 oracle); the resulting duplicate in the hidden folder is visible to the owner.
func TestURLEditToAURLPresentOnlyInAHiddenFolderBehavesLikeAFreshURL(t *testing.T) {
	setup := func(t *testing.T, hiddenURL string) (*env, bookmarks.Scope, bookmarks.Scope, bookmarks.Bookmark) {
		e := newEnv(t)
		alice, bob := e.user("alice"), e.user("bob")
		as := e.private(alice, identity.ModeEdit)
		aBar, aOther := e.roots(as)
		s := e.folder(as, aBar, "Shared")
		g := e.folder(as, aOther, "Hidden G")
		r := e.bm(as, "https://r.example/", "R", s, g)
		e.bm(as, hiddenURL, "HIDDEN-Y-TITLE", g)
		ss, _, _ := e.shared(alice, s, bob, identity.ModeEdit, identity.ModeEdit)
		return e, as, ss, r
	}
	eH, asH, ssH, rH := setup(t, "https://y.example/")
	gotH, errH := eH.svc.UpdateBookmark(ctx, ssH, rH.ID, bookmarks.BookmarkUpdate{URL: ptr("https://y.example/")})
	eF, _, ssF, rF := setup(t, "https://other-hidden.example/")
	gotF, errF := eF.svc.UpdateBookmark(ctx, ssF, rF.ID, bookmarks.BookmarkUpdate{URL: ptr("https://y.example/")})
	noErr(t, errF)
	noErr(t, errH) // identical outcome: success, no oracle
	if gotH.URL != gotF.URL || gotH.Title != gotF.Title || len(gotH.Folders) != len(gotF.Folders) || gotH.Folders[0] != gotF.Folders[0] {
		t.Errorf("hidden-URL outcome %+v differs from fresh-URL outcome %+v", gotH, gotF)
	}
	if strings.Contains(gotH.Title, "HIDDEN") {
		t.Errorf("hidden title returned: %+v", gotH)
	}
	// the writer still cannot see the hidden record; the owner can see both for later review
	list, _ := eH.svc.ListBookmarks(ctx, ssH, bookmarks.ListQuery{Page: -1, URL: "https://y.example/"})
	if len(list) != 1 || list[0].ID != rH.ID {
		t.Errorf("recipient sees %+v", list)
	}
	own, _ := eH.svc.ListBookmarks(ctx, asH, bookmarks.ListQuery{Page: -1, URL: "https://y.example/"})
	if len(own) != 2 {
		t.Errorf("owner should see the duplicate for review, sees %+v", own)
	}
}

// A placement added through a share into a hidden folder is not a concern either: the
// writer can only address visible folders, so an add never probes hidden URLs.
func TestVisibleCollisionStill409AndNeverMerges(t *testing.T) {
	e, as, ss, a, b, f1, s := dupSetup(t)
	_ = ss
	_, err := e.svc.UpdateBookmark(ctx, as, a.ID, bookmarks.BookmarkUpdate{Folders: &[]int64{f1, s}})
	is(t, err, bookmarks.ErrConflict)
	if ga, _ := e.svc.GetBookmark(ctx, as, a.ID); ga.ID == b.ID {
		t.Error("records merged")
	}
}

func TestFindByURLHasNoFirst100Cap(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	as := e.private(alice, identity.ModeEdit)
	_, aOther := e.roots(as)
	target := e.folder(as, aOther, "Target")
	// 150 visible records with one URL, each in its own folder (so the one-URL-per-folder
	// invariant holds); only the 150th is also placed in the target folder.
	tx, err := e.db.SQL().BeginTx(ctx, nil)
	noErr(t, err)
	var last int64
	for i := 0; i < 150; i++ {
		fr, err := tx.ExecContext(ctx, `INSERT INTO folders(owner_id, parent_id, kind, title, position, created_at, updated_at) VALUES (?, ?, 'folder', ?, ?, 1, 1)`, alice.ID, aOther, "f"+itoa(int64(i)), 10+i)
		noErr(t, err)
		fid, _ := fr.LastInsertId()
		res, err := tx.ExecContext(ctx, `INSERT INTO bookmarks(owner_id, url, title, created_at, updated_at) VALUES (?, 'https://many.example/x', 't', 1, 1)`, alice.ID)
		noErr(t, err)
		last, _ = res.LastInsertId()
		_, err = tx.ExecContext(ctx, `INSERT INTO placements(bookmark_id, folder_id, position) VALUES (?, ?, 0)`, last, fid)
		noErr(t, err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO placements(bookmark_id, folder_id, position) VALUES (?, ?, 0)`, last, target)
	noErr(t, err)
	noErr(t, tx.Commit())

	got, err := e.svc.CreateBookmark(ctx, as, bookmarks.BookmarkInput{URL: "https://many.example/x", Title: "t", Folders: []int64{target}})
	noErr(t, err)
	if got.ID != last {
		t.Errorf("POST chose record %d, want %d (the one already in the target, id beyond the 100th)", got.ID, last)
	}
	e.assertNoSameFolderDuplicates()
}

// ---- finding 3: revalidate inside the write transaction ----------------------

func revalSetup(t *testing.T) (e *env, alice, bob identity.User, as, ss bookmarks.Scope, cred identity.Credential, shared int64) {
	t.Helper()
	e = newEnv(t)
	alice, bob = e.user("alice"), e.user("bob")
	as = e.private(alice, identity.ModeEdit)
	aBar, _ := e.roots(as)
	shared = e.folder(as, aBar, "Shared")
	ss, cred, _ = e.shared(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	return
}

func TestWritesRevalidateGrantAndCredentialAfterTheScopeWasResolved(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(t *testing.T, e *env, alice, bob identity.User, cred identity.Credential, shared int64, as bookmarks.Scope)
		want   error
	}{
		{"credential revoked", func(t *testing.T, e *env, _, _ identity.User, c identity.Credential, _ int64, _ bookmarks.Scope) {
			noErr(t, e.ids.RevokeCredential(ctx, c.ID))
		}, identity.ErrUnauthorized},
		{"share revoked", func(t *testing.T, e *env, a, b identity.User, _ identity.Credential, sh int64, _ bookmarks.Scope) {
			noErr(t, e.svc.RevokeShare(ctx, a.ID, sh, b.ID))
		}, identity.ErrUnauthorized},
		{"share downgraded", func(t *testing.T, e *env, a, b identity.User, _ identity.Credential, sh int64, _ bookmarks.Scope) {
			_, err := e.svc.GrantShare(ctx, a.ID, sh, b.ID, identity.ModeView)
			noErr(t, err)
		}, bookmarks.ErrForbidden},
		{"recipient deactivated", func(t *testing.T, e *env, _, _ identity.User, _ identity.Credential, _ int64, _ bookmarks.Scope) {
			noErr(t, e.ids.SetActive(ctx, "bob", false))
		}, identity.ErrUnauthorized},
		{"shared folder trashed", func(t *testing.T, e *env, _, _ identity.User, _ identity.Credential, sh int64, as bookmarks.Scope) {
			noErr(t, e.svc.DeleteFolder(ctx, as, sh))
		}, identity.ErrUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, alice, bob, as, ss, cred, shared := revalSetup(t)
			c.mutate(t, e, alice, bob, cred, shared, as)
			after := e.snapshot(as)
			_, err := e.svc.CreateFolder(ctx, ss, bookmarks.RootID, "stale")
			is(t, err, c.want)
			_, err = e.svc.CreateBookmark(ctx, ss, bookmarks.BookmarkInput{URL: "https://stale.example/", Title: "s", Folders: []int64{bookmarks.RootID}})
			is(t, err, c.want)
			if got := e.snapshot(as); got != after {
				t.Errorf("stale scope wrote data:\nafter mutation:\n%s\nafter writes:\n%s", after, got)
			}
		})
	}
}

func TestPrivateScopeWritesRevalidateCredential(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	c, _, err := e.ids.CreateCredential(ctx, identity.NewCredential{UserID: alice.ID, Mode: identity.ModeEdit})
	noErr(t, err)
	sc := e.scopeOf(alice, c)
	bar, _ := e.roots(sc)
	noErr(t, e.ids.RevokeCredential(ctx, c.ID))
	_, err = e.svc.CreateFolder(ctx, sc, bar, "stale")
	is(t, err, identity.ErrUnauthorized)
	is(t, e.svc.AcquireLock(ctx, sc), identity.ErrUnauthorized)
}

func TestRevokingACredentialReleasesItsLock(t *testing.T) {
	e := newEnv(t)
	u := e.user("alice")
	c1, _, err := e.ids.CreateCredential(ctx, identity.NewCredential{UserID: u.ID, Mode: identity.ModeEdit})
	noErr(t, err)
	one := e.scopeOf(u, c1)
	two := e.private(u, identity.ModeEdit)
	noErr(t, e.svc.AcquireLock(ctx, one))
	is(t, e.svc.AcquireLock(ctx, two), bookmarks.ErrLocked)
	noErr(t, e.ids.RevokeCredential(ctx, c1.ID))
	noErr(t, e.svc.AcquireLock(ctx, two))
}

// ---- finding 5: URL edits of an already-visible shared record ----------------

func TestShareScopeMayEditURLOfVisibleRecordAndHiddenPlacementsSurvive(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	as := e.private(alice, identity.ModeEdit)
	aBar, aOther := e.roots(as)
	shared := e.folder(as, aBar, "Shared")
	hidden := e.folder(as, aOther, "Hidden")
	b, err := e.svc.CreateBookmark(ctx, as, bookmarks.BookmarkInput{URL: "https://both.example/", Title: "Both", Folders: []int64{shared, hidden}, Tags: &[]string{"keep"}})
	noErr(t, err)
	ss, _, _ := e.shared(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)

	got, err := e.svc.UpdateBookmark(ctx, ss, b.ID, bookmarks.BookmarkUpdate{URL: ptr("https://moved.example/")})
	noErr(t, err)
	if got.URL != "https://moved.example/" || got.ID != b.ID || len(got.Folders) != 1 || got.Folders[0] != bookmarks.RootID {
		t.Errorf("share view after URL edit = %+v", got)
	}
	owner, err := e.svc.GetBookmark(ctx, as, b.ID)
	noErr(t, err)
	if owner.URL != "https://moved.example/" || len(owner.Folders) != 2 || strings.Join(owner.Tags, ",") != "keep" {
		t.Errorf("owner view = %+v (URL edit applies to every reference of the already-shared record; hidden placement and tags must survive)", owner)
	}
	e.assertNoSameFolderDuplicates()
}

// ---- minors: bounded search -------------------------------------------------

func TestSearchTermCountAndLengthAreBounded(t *testing.T) {
	e := newEnv(t)
	as := e.private(e.user("alice"), identity.ModeEdit)
	many := make([]string, bookmarks.MaxSearchTerms+1)
	for i := range many {
		many[i] = "t"
	}
	_, err := e.svc.ListBookmarks(ctx, as, bookmarks.ListQuery{Page: -1, Search: many})
	is(t, err, bookmarks.ErrInvalid)
	_, err = e.svc.ListBookmarks(ctx, as, bookmarks.ListQuery{Page: -1, Search: []string{strings.Repeat("a", bookmarks.MaxSearchTermBytes+1)}})
	is(t, err, bookmarks.ErrInvalid)
	_, err = e.svc.ListBookmarks(ctx, as, bookmarks.ListQuery{Page: -1, Search: many[:bookmarks.MaxSearchTerms]})
	noErr(t, err)
	_, err = e.svc.ListBookmarks(ctx, as, bookmarks.ListQuery{Page: -1, Search: []string{strings.Repeat("a", bookmarks.MaxSearchTermBytes)}})
	noErr(t, err)
}
