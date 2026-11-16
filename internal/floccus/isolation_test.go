package floccus_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// dump captures everything an account can see, to prove failed/forbidden calls changed nothing.
func dump(t *testing.T, r *rig, a account) string {
	t.Helper()
	root := r.do(&a, "GET", R+"/folder/-1/children?layers=-1", nil).ok(t).raw
	list := r.do(&a, "GET", R+"/bookmark?page=-1&search[]=%25", nil).raw
	all := r.do(&a, "GET", R+"/bookmark?page=0&limit=1000", nil).raw
	return root + list + all
}

func TestTwoUsersAreIsolatedOnEveryRoute(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bob := r.account("bob", identity.ModeEdit)
	aBar, _ := r.roots(alice)
	bBar, _ := r.roots(bob)
	aFolder := r.mkFolder(alice, aBar, "alice-secret-folder")
	aBm := r.mkBookmark(alice, "https://alice-only.example/", "alice-secret-title", aFolder)
	bFolder := r.mkFolder(bob, bBar, "bob-folder")
	bBm := r.mkBookmark(bob, "https://bob-only.example/", "bob-title", bFolder)
	beforeA, beforeB := dump(t, r, alice), dump(t, r, bob)

	type call struct {
		name, method, path string
		body               any
		want               int
	}
	f, b := idStr(aFolder), idStr(aBm)
	calls := []call{
		{"folder children", "GET", "/folder/" + f + "/children?layers=-1", nil, 404},
		{"folder children of alice's bar", "GET", "/folder/" + idStr(aBar) + "/children?layers=1", nil, 404},
		{"folder hash", "GET", "/folder/" + f + "/hash?hashFn=sha256", nil, 404},
		{"folder tree root", "GET", "/folder?root=" + f + "&layers=0", nil, 404},
		{"create in", "POST", "/folder", map[string]any{"parent_folder": aFolder, "title": "x"}, 404},
		{"rename", "PUT", "/folder/" + f, map[string]any{"parent_folder": bBar, "title": "pwned"}, 404},
		{"move mine into theirs", "PUT", "/folder/" + idStr(bFolder), map[string]any{"parent_folder": aFolder, "title": "bob-folder"}, 404},
		{"delete folder", "DELETE", "/folder/" + f, nil, 404},
		{"order", "PATCH", "/folder/" + f + "/childorder", map[string]any{"data": []any{}}, 404},
		{"import", "IMPORT", "/folder/" + f + "/import", nil, 404},
		{"get bookmark", "GET", "/bookmark/" + b, nil, 404},
		{"put bookmark", "PUT", "/bookmark/" + b, map[string]any{"url": "https://alice-only.example/", "title": "pwned", "folders": []int64{bBar}}, 404},
		{"put my bookmark into theirs", "PUT", "/bookmark/" + idStr(bBm), map[string]any{"url": "https://bob-only.example/", "title": "bob-title", "folders": []int64{aFolder}}, 404},
		{"post into", "POST", "/bookmark", map[string]any{"url": "https://x.example/", "title": "x", "folders": []int64{aFolder}}, 404},
		{"delete placement", "DELETE", "/folder/" + f + "/bookmarks/" + b, nil, 404},
		{"delete bookmark", "DELETE", "/bookmark/" + b, nil, 404},
	}
	for _, c := range calls {
		var res resp
		if c.method == "IMPORT" {
			res = r.multipart(&bob, R+c.path, "bm_import", "u.html", `<DL><p><DT><A HREF="https://i.example/">i</A></DL>`)
		} else {
			res = r.do(&bob, c.method, R+c.path, c.body)
		}
		if res.status != c.want {
			t.Errorf("%s: status %d (%s)", c.name, res.status, res.raw)
		}
		for _, leak := range []string{"alice-secret", "alice-only"} {
			if strings.Contains(res.raw, leak) {
				t.Errorf("%s: response leaks %q: %s", c.name, leak, res.raw)
			}
		}
	}
	if dump(t, r, alice) != beforeA || dump(t, r, bob) != beforeB {
		t.Error("cross-user calls changed data")
	}
	// searches and lists never include the other user's data
	for _, q := range []string{"page=-1&search[]=alice", "page=0&limit=100", "url=https%3A%2F%2Falice-only.example%2F"} {
		res := r.do(&bob, "GET", R+"/bookmark?"+q, nil).ok(t)
		if strings.Contains(res.raw, "alice") {
			t.Errorf("list %q leaks: %s", q, res.raw)
		}
	}
}

func TestReadOnlyCredentialGets403OnEveryContentMutationAndStillReads(t *testing.T) {
	r := newRig(t)
	rw := r.account("alice", identity.ModeEdit)
	bar, other := r.roots(rw)
	f := r.mkFolder(rw, bar, "f")
	b := r.mkBookmark(rw, "https://ro.example/", "ro", f)
	ro := r.credFor(rw.user, identity.NewCredential{UserID: rw.user.ID, Mode: identity.ModeView})
	before := dump(t, r, ro)

	muts := []struct {
		name, method, path string
		body               any
	}{
		{"create folder", "POST", "/folder", map[string]any{"parent_folder": bar, "title": "x"}},
		{"update folder", "PUT", "/folder/" + idStr(f), map[string]any{"parent_folder": bar, "title": "y"}},
		{"delete folder", "DELETE", "/folder/" + idStr(f), nil},
		{"order", "PATCH", "/folder/" + idStr(bar) + "/childorder", map[string]any{"data": []any{}}},
		{"import", "IMPORT", "/folder/" + idStr(other) + "/import", nil},
		{"create bookmark", "POST", "/bookmark", map[string]any{"url": "https://n.example/", "title": "n", "folders": []int64{bar}}},
		{"update bookmark", "PUT", "/bookmark/" + idStr(b), map[string]any{"url": "https://ro.example/", "title": "z", "folders": []int64{f}}},
		{"delete placement", "DELETE", "/folder/" + idStr(f) + "/bookmarks/" + idStr(b), nil},
		{"delete bookmark", "DELETE", "/bookmark/" + idStr(b), nil},
		{"malformed body still 403 first", "POST", "/folder", `{`},
		{"unknown ids still 403", "DELETE", "/folder/987654", nil},
	}
	for _, m := range muts {
		var res resp
		if m.method == "IMPORT" {
			res = r.multipart(&ro, R+m.path, "bm_import", "u.html", `<DL><p><DT><A HREF="https://i.example/">i</A></DL>`)
		} else {
			res = r.do(&ro, m.method, R+m.path, m.body)
		}
		res.fail(t, 403)
		if !strings.Contains(strings.ToLower(res.raw), "read-only") {
			t.Errorf("%s: 403 body should say read-only: %s", m.name, res.raw)
		}
	}
	if dump(t, r, ro) != before {
		t.Error("read-only credential changed data")
	}
	// reads and the lock verbs (the only write verbs of a download-only sync) are allowed
	r.do(&ro, "GET", R+"/bookmark/"+idStr(b), nil).ok(t)
	r.do(&ro, "GET", R+"/folder/-1/hash?hashFn=sha256", nil).ok(t)
	r.do(&ro, "POST", R+"/lock", nil).ok(t)
	r.do(&ro, "DELETE", R+"/lock", nil).ok(t)
}

func TestShareCredentialSeesOnlySharedSubtreeWithFilteredFolders(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	bar, other := r.roots(alice)
	shared := r.mkFolder(alice, bar, "Shared")
	sub := r.mkFolder(alice, shared, "Sub")
	secret := r.mkFolder(alice, other, "Secret")
	both := r.mkBookmark(alice, "https://both.example/", "both", shared, secret)
	r.mkBookmark(alice, "https://secret.example/", "secret", secret)
	bc := r.shareAccount(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)

	// -1 is the share; no private tree leaks
	d := children(t, r, bc, -1, -1)
	if titles(d) != "Sub|both" {
		t.Fatalf("share root = %s", titles(d))
	}
	// folders[] holds only the visible placement, as -1
	got := r.do(&bc, "GET", R+"/bookmark/"+idStr(both), nil).ok(t).item()
	if fmt.Sprint(got["folders"]) != "[-1]" {
		t.Errorf("folders = %v (secret folder %d leaked?)", got["folders"], secret)
	}
	list := r.do(&bc, "GET", R+"/bookmark?page=-1&search[]=%25", nil).ok(t)
	if strings.Contains(list.raw, "secret") {
		t.Errorf("list leaks: %s", list.raw)
	}
	// out-of-scope ids are 404 for every verb
	for _, id := range []int64{bar, other, secret} {
		r.do(&bc, "GET", R+"/folder/"+idStr(id)+"/children?layers=1", nil).fail(t, 404)
		r.do(&bc, "POST", R+"/folder", map[string]any{"parent_folder": id, "title": "x"}).fail(t, 404)
	}
	// the share root cannot be renamed/deleted through its alias or real id
	r.do(&bc, "PUT", R+"/folder/-1", map[string]any{"parent_folder": -1, "title": "renamed"}).fail(t, 400)
	r.do(&bc, "DELETE", R+"/folder/"+idStr(shared), nil).fail(t, 400)
	// edit within the share: move bookmark into Sub; hidden placement survives
	r.do(&bc, "PUT", R+"/bookmark/"+idStr(both), map[string]any{"url": "https://both.example/", "title": "both2", "folders": []int64{sub}}).ok(t)
	ownerView := r.do(&alice, "GET", R+"/bookmark/"+idStr(both), nil).ok(t).item()
	fs := fmt.Sprint(ownerView["folders"])
	if !strings.Contains(fs, idStr(secret)) || !strings.Contains(fs, idStr(sub)) || strings.Contains(fs, idStr(shared)) {
		t.Errorf("owner placements after recipient edit = %s", fs)
	}
	// the recipient's PRIVATE credential tree does not include the share
	bp := r.credFor(bob, identity.NewCredential{UserID: bob.ID, Mode: identity.ModeEdit})
	if titles(children(t, r, bp, -1, -1)) != "Bookmarks Bar|Other Bookmarks" || strings.Contains(r.do(&bp, "GET", R+"/folder/-1/children?layers=-1", nil).raw, "Shared") {
		t.Error("incoming share is mounted in the private adapter root")
	}
	r.do(&bp, "GET", R+"/folder/"+idStr(shared)+"/children?layers=1", nil).fail(t, 404)
}

func TestShareDowngradeAndRevokeApplyImmediately(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	bar, _ := r.roots(alice)
	shared := r.mkFolder(alice, bar, "Shared")
	bc := r.shareAccount(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	r.do(&bc, "POST", R+"/folder", map[string]any{"parent_folder": -1, "title": "ok"}).ok(t)

	_, err := r.svc.GrantShare(bg, alice.user.ID, shared, bob.ID, identity.ModeView) // downgrade
	noErr(t, err)
	r.do(&bc, "POST", R+"/folder", map[string]any{"parent_folder": -1, "title": "nope"}).fail(t, 403)
	r.do(&bc, "GET", R+"/folder/-1/children?layers=1", nil).ok(t)
	_, err = r.svc.GrantShare(bg, alice.user.ID, shared, bob.ID, identity.ModeEdit) // upgrade back
	noErr(t, err)
	r.do(&bc, "POST", R+"/folder", map[string]any{"parent_folder": -1, "title": "again"}).ok(t)

	noErr(t, r.svc.RevokeShare(bg, alice.user.ID, shared, bob.ID))
	r.do(&bc, "GET", R+"/folder/-1/children?layers=1", nil).fail(t, 401)
	r.do(&bc, "POST", R+"/lock", nil).fail(t, 401)

	// trashing the shared folder kills share credentials too
	bc2 := r.shareAccount(alice, r.mkFolder(alice, bar, "Second"), bob, identity.ModeEdit, identity.ModeEdit)
	r.do(&bc2, "GET", R+"/folder/-1/children?layers=1", nil).ok(t)
	noErr(t, r.svc.DeleteFolder(bg, alice.scope, bc2.scope.RootFolderID))
	r.do(&bc2, "GET", R+"/folder/-1/children?layers=1", nil).fail(t, 401)
}

func TestLocksOverWire(t *testing.T) {
	r := newRig(t)
	one := r.account("alice", identity.ModeEdit)
	two := r.credFor(one.user, identity.NewCredential{UserID: one.user.ID, Mode: identity.ModeEdit})
	bar, _ := r.roots(one)

	r.do(&one, "POST", R+"/lock", nil).ok(t)
	r.do(&one, "POST", R+"/lock", nil).ok(t) // renewal (floccus re-POSTs every 2 minutes)
	r.do(&two, "POST", R+"/lock", nil).fail(t, 423)
	// enforcement, not advisory
	r.do(&two, "POST", R+"/folder", map[string]any{"parent_folder": bar, "title": "blocked"}).fail(t, 423)
	r.do(&two, "DELETE", R+"/lock", nil).fail(t, 423) // cannot release someone else's lock
	r.do(&two, "POST", R+"/lock", nil).fail(t, 423)   // still held
	r.do(&two, "GET", R+"/folder/-1/children?layers=1", nil).ok(t)
	r.do(&one, "POST", R+"/folder", map[string]any{"parent_folder": bar, "title": "mine"}).ok(t)
	r.do(&one, "DELETE", R+"/lock", nil).ok(t)
	r.do(&one, "DELETE", R+"/lock", nil).ok(t) // idempotent
	r.do(&two, "POST", R+"/lock", nil).ok(t)
	// expiry
	r.advance(6 * time.Minute)
	r.do(&one, "POST", R+"/lock", nil).ok(t)
	// an unrelated user is independent
	bob := r.account("bob", identity.ModeEdit)
	r.do(&bob, "POST", R+"/lock", nil).ok(t)
	bBar, _ := r.roots(bob)
	r.do(&bob, "POST", R+"/folder", map[string]any{"parent_folder": bBar, "title": "independent"}).ok(t)
}

func TestOverlappingOwnerAndShareLocksOverWire(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	bar, _ := r.roots(alice)
	shared := r.mkFolder(alice, bar, "Shared")
	bc := r.shareAccount(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)

	r.do(&alice, "POST", R+"/lock", nil).ok(t)
	r.do(&bc, "POST", R+"/lock", nil).fail(t, 423)
	r.do(&bc, "POST", R+"/folder", map[string]any{"parent_folder": -1, "title": "blocked"}).fail(t, 423)
	r.do(&alice, "DELETE", R+"/lock", nil).ok(t)
	r.do(&bc, "POST", R+"/lock", nil).ok(t)
	r.do(&alice, "POST", R+"/lock", nil).fail(t, 423)
	r.do(&alice, "POST", R+"/folder", map[string]any{"parent_folder": bar, "title": "blocked"}).fail(t, 423)
	// read-only share credentials may lock (download-only syncs always do)
	r.do(&bc, "DELETE", R+"/lock", nil).ok(t)
	carol, _ := r.ids.CreateUser(bg, "carol", "website password carol", false)
	other := r.mkFolder(alice, bar, "Other share")
	ro := r.shareAccount(alice, other, carol, identity.ModeView, identity.ModeView)
	r.do(&ro, "POST", R+"/lock", nil).ok(t)
	r.do(&ro, "DELETE", R+"/lock", nil).ok(t)
}
