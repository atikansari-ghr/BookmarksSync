package floccus_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

// blockedPOST sends a POST whose body is held open. The handler authenticates and
// resolves its scope from the headers, then blocks reading the body; mid() runs while it
// is blocked (a revoke or downgrade by someone else), then the body is completed.
// A write that trusted the scope resolved before the body arrived would succeed.
func (r *rig) blockedPOST(a *account, path, first, second string, mid func()) resp {
	r.t.Helper()
	pr, pw := io.Pipe()
	req, err := http.NewRequest("POST", r.ts.URL+path, pr)
	if err != nil {
		r.t.Fatal(err)
	}
	req.SetBasicAuth(a.user.Username, a.secret)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan resp, 1)
	go func() {
		res, err := r.client().Do(req)
		if err != nil {
			done <- resp{status: -1, raw: err.Error()}
			return
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		out := resp{status: res.StatusCode, raw: string(raw)}
		_ = json.Unmarshal(raw, &out.body)
		done <- out
	}()
	_, _ = pw.Write([]byte(first))
	time.Sleep(500 * time.Millisecond) // let the handler pass authentication and block on the body
	mid()
	_, _ = pw.Write([]byte(second))
	_ = pw.Close()
	return <-done
}

const staleBody1, staleBody2 = `{"url":"https://stale.example/","ti`, `tle":"stale","folders":[-1]}`

func TestBlockedBodyThenCredentialRevokedWritesNothing(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	bar, _ := r.roots(alice)
	shared := r.mkFolder(alice, bar, "Shared")
	bc := r.shareAccount(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	res := r.blockedPOST(&bc, R+"/bookmark", staleBody1, staleBody2, func() {
		noErr(t, r.ids.RevokeCredential(bg, bc.cred.ID))
	})
	res.fail(t, 401)
	r.assertNoStale(alice)
}

func TestBlockedBodyThenShareRevokedWritesNothing(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	bar, _ := r.roots(alice)
	shared := r.mkFolder(alice, bar, "Shared")
	bc := r.shareAccount(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	res := r.blockedPOST(&bc, R+"/bookmark", staleBody1, staleBody2, func() {
		noErr(t, r.svc.RevokeShare(bg, alice.user.ID, shared, bob.ID))
	})
	res.fail(t, 401)
	r.assertNoStale(alice)
}

func TestBlockedBodyThenShareDowngradedWritesNothing(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	bar, _ := r.roots(alice)
	shared := r.mkFolder(alice, bar, "Shared")
	bc := r.shareAccount(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	res := r.blockedPOST(&bc, R+"/bookmark", staleBody1, staleBody2, func() {
		_, err := r.svc.GrantShare(bg, alice.user.ID, shared, bob.ID, identity.ModeView)
		noErr(t, err)
	})
	res.fail(t, 403)
	r.assertNoStale(alice)
}

func TestBlockedBodyThenUserDeactivatedWritesNothing(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bar, _ := r.roots(alice)
	res := r.blockedPOST(&alice, R+"/bookmark", `{"url":"https://stale.example/","title":"stale","fo`, `lders":[`+idStr(bar)+`]}`, func() {
		noErr(t, r.ids.SetActive(bg, "alice", false))
	})
	res.fail(t, 401)
	noErr(t, r.ids.SetActive(bg, "alice", true))
	fresh := r.account("alice", identity.ModeEdit)
	r.assertNoStale(fresh)
}

func (r *rig) assertNoStale(a account) {
	r.t.Helper()
	d := r.do(&a, "GET", R+"/bookmark?page=-1&url="+url.QueryEscape("https://stale.example/"), nil).ok(r.t).data()
	if len(d) != 0 {
		r.t.Fatalf("stale write landed: %v", d)
	}
}

func TestSiblingShareLocksSerializeOverWire(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	carol, _ := r.ids.CreateUser(bg, "carol", "website password carol", false)
	bar, _ := r.roots(alice)
	s1, s2 := r.mkFolder(alice, bar, "S1"), r.mkFolder(alice, bar, "S2")
	b := r.shareAccount(alice, s1, bob, identity.ModeEdit, identity.ModeEdit)
	c := r.shareAccount(alice, s2, carol, identity.ModeEdit, identity.ModeEdit)
	rec := r.mkBookmark(alice, "https://rec.example/", "rec", s1, s2)

	r.do(&b, "POST", R+"/lock", nil).ok(t)
	r.do(&c, "POST", R+"/lock", nil).fail(t, 423)
	r.do(&c, "PUT", R+"/bookmark/"+idStr(rec), map[string]any{"title": "sneaky"}).fail(t, 423)
	r.do(&c, "POST", R+"/folder", map[string]any{"parent_folder": -1, "title": "x"}).fail(t, 423)
	r.do(&alice, "POST", R+"/lock", nil).fail(t, 423)
	r.do(&c, "GET", R+"/folder/-1/children?layers=1", nil).ok(t)
	r.do(&b, "DELETE", R+"/lock", nil).ok(t)
	r.do(&c, "POST", R+"/lock", nil).ok(t)
}

func TestShareCredentialMayEditURLOfVisibleRecordHiddenPlacementsSurvive(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	bar, other := r.roots(alice)
	shared := r.mkFolder(alice, bar, "Shared")
	id := r.mkBookmark(alice, "https://both.example/", "Both", shared, other)
	bc := r.shareAccount(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)

	got := r.do(&bc, "PUT", R+"/bookmark/"+idStr(id), map[string]any{"url": "https://moved.example/"}).ok(t).item()
	if got["url"] != "https://moved.example/" {
		t.Errorf("share view = %v", got)
	}
	owner := r.do(&alice, "GET", R+"/bookmark/"+idStr(id), nil).ok(t).item()
	if owner["url"] != "https://moved.example/" || len(owner["folders"].([]any)) != 2 {
		t.Errorf("owner view = %v", owner)
	}
}

func TestStockFlowsAfterCrossScopeCollisionNeverCreateSameFolderDuplicates(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	bar, other := r.roots(alice)
	shared := r.mkFolder(alice, bar, "Shared")
	f1 := r.mkFolder(alice, other, "F1")
	x := "https://dup.example/x"
	a := r.mkBookmark(alice, x, "private", f1)
	bc := r.shareAccount(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	b := r.mkBookmark(bc, x, "mine", -1)
	if a == b {
		t.Fatal("expected separate records")
	}
	// stock single-folder POSTs resolve deterministically to the record already there
	if got := r.mkBookmark(alice, x, "t", f1); got != a {
		t.Errorf("POST into f1 = %d, want %d", got, a)
	}
	if got := r.mkBookmark(alice, x, "t", shared); got != b {
		t.Errorf("POST into shared = %d, want %d", got, b)
	}
	if got := r.mkBookmark(bc, x, "t", -1); got != b {
		t.Errorf("recipient POST = %d, want %d", got, b)
	}
	// moving one record into the folder that already holds the other is an explicit conflict
	r.do(&alice, "PUT", R+"/bookmark/"+idStr(b), map[string]any{"folders": []int64{shared, f1}}).fail(t, 409)
	r.do(&alice, "POST", R+"/bookmark", map[string]any{"url": x, "title": "t", "folders": []int64{f1, shared}}).fail(t, 409)
	// and nothing changed
	d := r.do(&alice, "GET", R+"/bookmark?page=-1&url="+url.QueryEscape(x), nil).ok(t).data()
	if len(d) != 2 {
		t.Fatalf("records for X = %d, want 2: %v", len(d), d)
	}
	for _, rec := range d {
		if f := rec.(map[string]any)["folders"].([]any); len(f) != 1 {
			t.Errorf("record %v gained placements", rec)
		}
	}
}

func TestSearchBoundsAre400(t *testing.T) {
	r := newRig(t)
	a := r.account("alice", identity.ModeEdit)
	var many strings.Builder
	for i := 0; i < 11; i++ {
		many.WriteString("&search[]=t")
	}
	r.do(&a, "GET", R+"/bookmark?page=-1"+many.String(), nil).fail(t, 400)
	r.do(&a, "GET", R+"/bookmark?page=-1&search[]="+strings.Repeat("a", 257), nil).fail(t, 400)
	r.do(&a, "GET", R+"/bookmark?page=-1&search[]="+strings.Repeat("a", 256), nil).ok(t)
	r.do(&a, "GET", R+"/bookmark?page=-1&search[]=t&search[]=u", nil).ok(t)
}

// Fix round 2: a URL present only in a folder hidden from the writer must give exactly the
// response a fresh URL gives, and nothing hidden is returned.
func TestHiddenOnlyURLHasSameWireOutcomeAsFreshURL(t *testing.T) {
	run := func(hiddenURL string) (resp, resp) {
		r := newRig(t)
		alice := r.account("alice", identity.ModeEdit)
		bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
		bar, other := r.roots(alice)
		shared := r.mkFolder(alice, bar, "Shared")
		g := r.mkFolder(alice, other, "Hidden G")
		id := r.mkBookmark(alice, "https://r.example/", "R", shared, g)
		r.mkBookmark(alice, hiddenURL, "HIDDEN-Y-TITLE", g)
		bc := r.shareAccount(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
		put := r.do(&bc, "PUT", R+"/bookmark/"+idStr(id), map[string]any{"url": "https://y.example/"})
		post := r.do(&bc, "POST", R+"/bookmark", map[string]any{"url": "https://y.example/", "title": "mine", "folders": []int64{-1}})
		own := r.do(&alice, "GET", R+"/bookmark?page=-1&url="+url.QueryEscape("https://y.example/"), nil).ok(t)
		if hiddenURL == "https://y.example/" && len(own.data()) != 2 {
			t.Errorf("owner should see both records for review: %s", own.raw)
		}
		return put, post
	}
	hp, hq := run("https://y.example/")
	fp, fq := run("https://fresh-hidden.example/")
	for _, rs := range []resp{hp, hq, fp, fq} {
		rs.ok(t)
		for _, leak := range []string{"HIDDEN", "Hidden G", "other"} {
			if strings.Contains(rs.raw, leak) {
				t.Errorf("response leaks %q: %s", leak, rs.raw)
			}
		}
	}
	if hp.status != fp.status || hq.status != fq.status {
		t.Errorf("hidden vs fresh status differ: %d/%d vs %d/%d", hp.status, hq.status, fp.status, fq.status)
	}
}

// Stock floccus is lookup-first: GET bookmark?url=X, pick a record, PUT it with its new
// folder list. After a cross-scope collision the owner sees two records and the picked one
// may be asked into the folder the other occupies. This stays an explicit 409 (never a
// merge); it is recorded for the real-browser gate as a sync error, not a success.
func TestLookupFirstPutIntoFolderOfTheOtherCollidedRecordIs409(t *testing.T) {
	r := newRig(t)
	alice := r.account("alice", identity.ModeEdit)
	bob, _ := r.ids.CreateUser(bg, "bob", "website password bob", false)
	bar, other := r.roots(alice)
	shared := r.mkFolder(alice, bar, "Shared")
	f1 := r.mkFolder(alice, other, "F1")
	x := "https://dup.example/x"
	a := r.mkBookmark(alice, x, "private", f1)
	bc := r.shareAccount(alice, shared, bob, identity.ModeEdit, identity.ModeEdit)
	b := r.mkBookmark(bc, x, "mine", -1)

	found := r.do(&alice, "GET", R+"/bookmark?page=-1&url="+url.QueryEscape(x), nil).ok(t).data()
	if len(found) != 2 {
		t.Fatalf("lookup returned %d records", len(found))
	}
	for _, pick := range found {
		rec := pick.(map[string]any)
		id := num(rec["id"])
		var want int64 = f1
		if id == a {
			want = shared
		}
		folders := append([]int64{}, folderIDs(rec)...)
		folders = append(folders, want)
		res := r.do(&alice, "PUT", R+"/bookmark/"+idStr(id), map[string]any{"folders": folders})
		res.fail(t, 409)
		if strings.Contains(res.raw, "private") || strings.Contains(res.raw, "mine") {
			t.Errorf("409 names a title: %s", res.raw)
		}
	}
	after := r.do(&alice, "GET", R+"/bookmark?page=-1&url="+url.QueryEscape(x), nil).ok(t).data()
	if len(after) != 2 {
		t.Fatalf("records changed: %v", after)
	}
	for _, rec := range after {
		if len(folderIDs(rec.(map[string]any))) != 1 {
			t.Errorf("placements changed: %v", rec)
		}
	}
	_ = b
}

func folderIDs(rec map[string]any) []int64 {
	var out []int64
	for _, f := range rec["folders"].([]any) {
		out = append(out, num(f))
	}
	return out
}
