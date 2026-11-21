package linkcheck_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck"
	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck/exclude"
)

var bg = context.Background()

// fakeChecker records every URL it is asked to check and answers from a table.
type fakeChecker struct {
	mu    sync.Mutex
	calls []string
	sets  []exclude.Set
	out   map[string]linkcheck.Result
	hook  func(url string)
}

func (f *fakeChecker) Check(_ context.Context, u string, ex exclude.Set) linkcheck.Result {
	f.mu.Lock()
	f.calls = append(f.calls, u)
	f.sets = append(f.sets, ex)
	hook := f.hook
	r, ok := f.out[u]
	f.mu.Unlock()
	if hook != nil {
		hook(u)
	}
	if !ok {
		r = linkcheck.Result{Status: linkcheck.StatusOK, HTTPStatus: 200}
	}
	return r
}

func (f *fakeChecker) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type env struct {
	t     *testing.T
	path  string
	db    *database.DB
	ids   *identity.Service
	bm    *bookmarks.Service
	svc   *linkcheck.Service
	check *fakeChecker
	now   time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, path: filepath.Join(t.TempDir(), "lc.db"), now: time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC)}
	e.open(&fakeChecker{})
	return e
}

// open (re)opens the database and builds fresh services on it, as a process restart would.
func (e *env) open(c *fakeChecker) {
	e.t.Helper()
	db, err := database.Open(bg, e.path)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { db.Close() })
	e.db = db
	clock := func() time.Time { return e.now }
	e.ids = identity.New(db.SQL(), identity.WithPasswordCost(8*1024, 1, 1), identity.WithClock(clock))
	e.bm = bookmarks.New(db.SQL(), bookmarks.WithClock(clock))
	e.check = c
	e.svc = linkcheck.NewService(db.SQL(), e.bm, e.ids, c, linkcheck.WithClock(clock))
}

func (e *env) restart(c *fakeChecker) {
	e.t.Helper()
	if err := e.db.Close(); err != nil {
		e.t.Fatal(err)
	}
	e.open(c)
}

func (e *env) user(name string) identity.User {
	e.t.Helper()
	u, err := e.ids.CreateUser(bg, name, "a perfectly fine password", false)
	if err != nil {
		e.t.Fatal(err)
	}
	return u
}

func (e *env) scope(u identity.User, write bool) bookmarks.Scope {
	e.t.Helper()
	sc, err := e.bm.PrivateScope(bg, u.ID, write)
	if err != nil {
		e.t.Fatal(err)
	}
	return sc
}

// add creates bookmarks in the user's Bookmarks Bar and returns their IDs.
func (e *env) add(u identity.User, urls ...string) []int64 {
	e.t.Helper()
	sc := e.scope(u, true)
	nodes, err := e.bm.Children(bg, sc, bookmarks.RootID, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	var ids []int64
	for _, url := range urls {
		b, err := e.bm.CreateBookmark(bg, sc, bookmarks.BookmarkInput{URL: url, Title: "t " + url, Folders: []int64{nodes[0].ID}})
		if err != nil {
			e.t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	return ids
}

func (e *env) submit(sc bookmarks.Scope, ids ...int64) linkcheck.Job {
	e.t.Helper()
	j, err := e.svc.SubmitCheck(bg, sc, ids)
	if err != nil {
		e.t.Fatalf("SubmitCheck: %v", err)
	}
	return j
}

func (e *env) drain() int {
	e.t.Helper()
	n := 0
	for {
		did, err := e.svc.ProcessNext(bg)
		if err != nil {
			e.t.Fatalf("ProcessNext: %v", err)
		}
		if !did {
			return n
		}
		n++
	}
}

func (e *env) status(sc bookmarks.Scope, id int64) linkcheck.Job {
	e.t.Helper()
	j, err := e.svc.CheckStatus(bg, sc, id)
	if err != nil {
		e.t.Fatalf("CheckStatus: %v", err)
	}
	return j
}

func TestSubmitValidatesInput(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	ids := e.add(alice, "https://a.example/", "https://b.example/")
	sc := e.scope(alice, true)
	many := make([]int64, linkcheck.MaxBookmarksPerJob+1)
	for i := range many {
		many[i] = int64(i + 1)
	}
	for name, in := range map[string][]int64{
		"empty": nil, "zero": {0}, "negative": {-1}, "duplicate": {ids[0], ids[0]}, "too many": many,
	} {
		if _, err := e.svc.SubmitCheck(bg, sc, in); !errors.Is(err, bookmarks.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	// Exactly the maximum is accepted when every bookmark is visible.
	var urls []string
	for i := 0; i < linkcheck.MaxBookmarksPerJob; i++ {
		urls = append(urls, "https://max.example/"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	all := e.add(alice, urls...)
	if j := e.submit(sc, all...); j.Total != linkcheck.MaxBookmarksPerJob || j.Status != "queued" {
		t.Errorf("max job = %+v", j)
	}
}

func TestSubmitProcessesAndStoresPerBookmarkResults(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	ids := e.add(alice, "https://ok.example/", "https://gone.example/", "ftp://files.example/x")
	e.check.out = map[string]linkcheck.Result{
		"https://gone.example/": {Status: linkcheck.StatusBroken, HTTPStatus: 404, Redirects: 2},
		"ftp://files.example/x": {Status: linkcheck.StatusSkipped, Detail: "unsupported_scheme"},
	}
	sc := e.scope(alice, true)
	j := e.submit(sc, ids...)
	if j.ID <= 0 || j.Status != "queued" || j.Total != 3 || j.Done != 0 || len(j.Results) != 3 {
		t.Fatalf("submitted job = %+v", j)
	}
	for _, r := range j.Results {
		if r.Status != "pending" {
			t.Errorf("fresh result = %+v, want pending", r)
		}
	}
	if n := e.drain(); n != 1 {
		t.Fatalf("processed %d jobs, want 1", n)
	}
	got := e.status(e.scope(alice, false), j.ID) // reading needs no write authority
	if got.Status != "completed" || got.Done != 3 || got.Total != 3 || got.FinishedAt.IsZero() {
		t.Fatalf("job = %+v", got)
	}
	want := []struct {
		id     int64
		status string
		http   int
	}{{ids[0], "ok", 200}, {ids[1], "broken", 404}, {ids[2], "skipped", 0}}
	for i, w := range want {
		r := got.Results[i]
		if r.BookmarkID != w.id || r.Status != w.status || r.HTTPStatus != w.http || r.CheckedAt.IsZero() {
			t.Errorf("result %d = %+v, want %+v", i, r, w)
		}
	}
	if got.Results[1].Redirects != 2 || got.Results[2].Detail != "unsupported_scheme" || got.Results[0].URL != "https://ok.example/" {
		t.Errorf("result details = %+v", got.Results)
	}
}

func TestSubmitRequiresWriteAuthorityAndVisibleBookmarks(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	aliceIDs := e.add(alice, "https://alice.example/")
	bobIDs := e.add(bob, "https://bob.example/")

	// A read-only scope may not queue checks.
	if _, err := e.svc.SubmitCheck(bg, e.scope(alice, false), aliceIDs); !errors.Is(err, bookmarks.ErrForbidden) {
		t.Errorf("read-only private scope: err = %v, want ErrForbidden", err)
	}
	// Bookmarks of another user look like missing ones, and nothing is queued at all.
	if _, err := e.svc.SubmitCheck(bg, e.scope(alice, true), []int64{aliceIDs[0], bobIDs[0]}); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Errorf("foreign bookmark: err = %v, want ErrNotFound", err)
	}
	if _, err := e.svc.SubmitCheck(bg, e.scope(alice, true), []int64{987654}); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Errorf("missing bookmark: err = %v, want ErrNotFound", err)
	}
	if n := e.drain(); n != 0 {
		t.Errorf("refused submissions left %d queued jobs", n)
	}
	// A scope with no actor (an internal/Floccus scope) cannot own jobs.
	if _, err := e.svc.SubmitCheck(bg, bookmarks.Scope{OwnerID: alice.ID, Writable: true}, aliceIDs); !errors.Is(err, bookmarks.ErrForbidden) {
		t.Errorf("actorless scope: err = %v, want ErrForbidden", err)
	}

	// Shares: a view-only recipient may not queue checks; an edit recipient may, but only
	// for what the share shows.
	sc := e.scope(alice, true)
	nodes, _ := e.bm.Children(bg, sc, bookmarks.RootID, 1)
	folder, err := e.bm.CreateFolder(bg, sc, nodes[0].ID, "shared")
	if err != nil {
		t.Fatal(err)
	}
	inShare, err := e.bm.CreateBookmark(bg, sc, bookmarks.BookmarkInput{URL: "https://in-share.example/", Title: "s", Folders: []int64{folder.ID}})
	if err != nil {
		t.Fatal(err)
	}
	view, err := e.bm.GrantShare(bg, alice.ID, folder.ID, bob.ID, identity.ModeView)
	if err != nil {
		t.Fatal(err)
	}
	bobView, err := e.bm.ShareScopeFor(bg, bob.ID, view.ID, true) // allowWrite still yields read-only for a view grant
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SubmitCheck(bg, bobView, []int64{inShare.ID}); !errors.Is(err, bookmarks.ErrForbidden) {
		t.Errorf("view-only share: err = %v, want ErrForbidden", err)
	}
	if _, err := e.bm.UpdateShare(bg, alice.ID, view.ID, identity.ModeEdit); err != nil {
		t.Fatal(err)
	}
	bobEdit, err := e.bm.ShareScopeFor(bg, bob.ID, view.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SubmitCheck(bg, bobEdit, []int64{inShare.ID}); err != nil {
		t.Errorf("edit share: %v", err)
	}
	// Alice's bookmark outside the share is not visible to bob through it.
	if _, err := e.svc.SubmitCheck(bg, bobEdit, aliceIDs); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Errorf("outside the share: err = %v, want ErrNotFound", err)
	}
}

func TestResultsAreIsolatedPerUserAndScope(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	aliceIDs := e.add(alice, "https://alice-secret.example/")
	aliceScope := e.scope(alice, true)
	nodes, _ := e.bm.Children(bg, aliceScope, bookmarks.RootID, 1)
	folder, _ := e.bm.CreateFolder(bg, aliceScope, nodes[0].ID, "shared")
	inShare, _ := e.bm.CreateBookmark(bg, aliceScope, bookmarks.BookmarkInput{URL: "https://shared.example/", Title: "s", Folders: []int64{folder.ID}})
	sh, err := e.bm.GrantShare(bg, alice.ID, folder.ID, bob.ID, identity.ModeEdit)
	if err != nil {
		t.Fatal(err)
	}
	bobShare, err := e.bm.ShareScopeFor(bg, bob.ID, sh.ID, true)
	if err != nil {
		t.Fatal(err)
	}

	privateJob := e.submit(aliceScope, aliceIDs...)
	ownerShareJob := e.submit(aliceScope, inShare.ID)
	bobJob := e.submit(bobShare, inShare.ID)
	e.drain()

	notFound := func(who string, sc bookmarks.Scope, id int64) {
		t.Helper()
		if _, err := e.svc.CheckStatus(bg, sc, id); !errors.Is(err, bookmarks.ErrNotFound) {
			t.Errorf("%s reading job %d: err = %v, want ErrNotFound", who, id, err)
		}
	}
	notFound("bob (share scope)", bobShare, privateJob.ID)
	notFound("bob (share scope)", bobShare, ownerShareJob.ID) // alice's job over the same folder is not bob's
	notFound("bob (private scope)", e.scope(bob, false), privateJob.ID)
	notFound("bob (private scope)", e.scope(bob, false), bobJob.ID) // bob's share job is not visible in his private scope
	notFound("alice (private scope)", e.scope(alice, false), bobJob.ID)
	notFound("alice (private scope)", aliceScope, 424242)
	if _, err := e.svc.CheckStatus(bg, bookmarks.Scope{OwnerID: alice.ID}, privateJob.ID); !errors.Is(err, bookmarks.ErrForbidden) && !errors.Is(err, bookmarks.ErrNotFound) {
		t.Errorf("actorless scope: err = %v", err)
	}
	// The owners do see their own.
	if j := e.status(aliceScope, privateJob.ID); j.Status != "completed" || len(j.Results) != 1 {
		t.Errorf("alice job = %+v", j)
	}
	if j := e.status(bobShare, bobJob.ID); j.Status != "completed" || len(j.Results) != 1 || j.Results[0].URL != "https://shared.example/" {
		t.Errorf("bob share job = %+v", j)
	}
	// Revoking the share ends bob's access to his job as well (the share scope is gone).
	if err := e.bm.RevokeShare(bg, alice.ID, folder.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.bm.ShareScopeFor(bg, bob.ID, sh.ID, true); !errors.Is(err, bookmarks.ErrNotFound) {
		t.Errorf("scope after revoke: %v", err)
	}
	// A stale share scope held across the revoke still cannot read the job.
	if _, err := e.svc.CheckStatus(bg, bobShare, bobJob.ID); err == nil {
		t.Error("a revoked share scope still reads its job")
	}
}

func TestWorkerRevalidatesShareBeforeEveryCheck(t *testing.T) {
	for _, mode := range []string{"revoke", "downgrade"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			alice, bob := e.user("alice"), e.user("bob")
			sc := e.scope(alice, true)
			roots, err := e.bm.Children(bg, sc, bookmarks.RootID, 1)
			if err != nil {
				t.Fatal(err)
			}
			folder, err := e.bm.CreateFolder(bg, sc, roots[0].ID, "shared")
			if err != nil {
				t.Fatal(err)
			}
			item, err := e.bm.CreateBookmark(bg, sc, bookmarks.BookmarkInput{URL: "https://private-link.example/", Title: "private", Folders: []int64{folder.ID}})
			if err != nil {
				t.Fatal(err)
			}
			share, err := e.bm.GrantShare(bg, alice.ID, folder.ID, bob.ID, identity.ModeEdit)
			if err != nil {
				t.Fatal(err)
			}
			bobScope, err := e.bm.ShareScopeFor(bg, bob.ID, share.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			job := e.submit(bobScope, item.ID)
			if mode == "revoke" {
				err = e.bm.RevokeShare(bg, alice.ID, folder.ID, bob.ID)
			} else {
				_, err = e.bm.UpdateShare(bg, alice.ID, share.ID, identity.ModeView)
			}
			if err != nil {
				t.Fatal(err)
			}
			if did, err := e.svc.ProcessNext(bg); !did || err != nil {
				t.Fatalf("ProcessNext = %v, %v", did, err)
			}
			if got := e.check.called(); len(got) != 0 {
				t.Fatalf("revoked/downgraded share still contacted links: %v", got)
			}
			var jobStatus, resultStatus, detail string
			if err := e.db.SQL().QueryRow(`SELECT status FROM link_check_jobs WHERE id = ?`, job.ID).Scan(&jobStatus); err != nil {
				t.Fatal(err)
			}
			if err := e.db.SQL().QueryRow(`SELECT status, detail FROM link_check_results WHERE job_id = ?`, job.ID).Scan(&resultStatus, &detail); err != nil {
				t.Fatal(err)
			}
			if jobStatus != "completed" || resultStatus != linkcheck.StatusBlocked || detail != "scope_revoked" {
				t.Fatalf("revoked job/result = %q/%q %q", jobStatus, resultStatus, detail)
			}
		})
	}
}

func TestProcessNextRetriesTransientStoreErrorsWithoutRepeatingCompletedResults(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	ids := e.add(alice, "https://first.example/", "https://second.example/")
	job := e.submit(e.scope(alice, true), ids...)
	// The first URL is durable; fail the write for the second to simulate a transient
	// storage error after partial progress.
	if _, err := e.db.SQL().Exec(`CREATE TRIGGER fail_second_result BEFORE UPDATE ON link_check_results WHEN NEW.position = 1 BEGIN SELECT RAISE(FAIL, 'temporary write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ProcessNext(bg); err == nil {
		t.Fatal("ProcessNext succeeded despite the injected storage failure")
	}
	var status string
	if err := e.db.SQL().QueryRow(`SELECT status FROM link_check_jobs WHERE id = ?`, job.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "queued" {
		t.Fatalf("job after transient error = %q, want queued for retry", status)
	}
	if _, err := e.db.SQL().Exec(`DROP TRIGGER fail_second_result`); err != nil {
		t.Fatal(err)
	}
	if did, err := e.svc.ProcessNext(bg); !did || err != nil {
		t.Fatalf("retry ProcessNext = %v, %v", did, err)
	}
	if got := e.check.called(); len(got) != 3 || got[0] != "https://first.example/" || got[1] != "https://second.example/" || got[2] != "https://second.example/" {
		t.Fatalf("checks = %v; completed first URL must not repeat", got)
	}
	if result := e.status(e.scope(alice, true), job.ID); result.Status != "completed" || result.Done != 2 {
		t.Fatalf("job after retry = %+v", result)
	}
}

func TestQueuedLinkCheckResumesAfterRestart(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	ids := e.add(alice, "https://one.example/", "https://two.example/", "https://three.example/", "https://four.example/")
	sc := e.scope(alice, true)
	running := e.submit(sc, ids[:3]...)
	queued := e.submit(sc, ids[3])

	// The process dies while checking the second URL of the first job.
	ctx, cancel := context.WithCancel(bg)
	e.check.hook = func(u string) {
		if u == "https://two.example/" {
			cancel()
		}
	}
	if did, err := e.svc.ProcessNext(ctx); !did || err == nil {
		t.Fatalf("interrupted ProcessNext = %v, %v; want it to report the interruption", did, err)
	}
	if got := e.check.called(); len(got) != 2 {
		t.Fatalf("calls before crash = %v", got)
	}
	mid := e.status(sc, running.ID)
	if mid.Status != "running" || mid.Done != 1 {
		t.Fatalf("job mid-crash = %+v (the interrupted URL must not be recorded as a result)", mid)
	}

	// Restart: new services over the same database file.
	after := &fakeChecker{}
	e.restart(after)
	sc = e.scope(alice, true)
	if got := e.status(sc, queued.ID); got.Status != "queued" {
		t.Fatalf("queued job after restart = %+v", got)
	}
	if err := e.svc.Resume(bg); err != nil {
		t.Fatal(err)
	}
	if n := e.drain(); n != 2 {
		t.Fatalf("processed %d jobs after restart, want 2", n)
	}
	// The URL completed before the crash is NOT repeated; the interrupted and untouched ones run.
	calls := after.called()
	want := []string{"https://two.example/", "https://three.example/", "https://four.example/"}
	if len(calls) != len(want) {
		t.Fatalf("calls after restart = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls after restart = %v, want %v", calls, want)
		}
	}
	for _, id := range []int64{running.ID, queued.ID} {
		if j := e.status(sc, id); j.Status != "completed" || j.Done != j.Total {
			t.Errorf("job %d after resume = %+v", id, j)
		}
	}

	// A second restart must not repeat anything: completed jobs stay completed.
	third := &fakeChecker{}
	e.restart(third)
	if err := e.svc.Resume(bg); err != nil {
		t.Fatal(err)
	}
	if n := e.drain(); n != 0 {
		t.Errorf("a completed job was processed again (%d)", n)
	}
	if got := third.called(); len(got) != 0 {
		t.Errorf("completed jobs retried: %v", got)
	}
	if j := e.status(e.scope(alice, true), running.ID); j.Status != "completed" || j.Results[0].Status != "ok" {
		t.Errorf("completed job changed: %+v", j)
	}
}

func TestExclusionsAreTheSubmittingUsersOwnSetting(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	aIDs := e.add(alice, "https://skip.example/")
	bIDs := e.add(bob, "https://skip.example/")
	if _, err := e.ids.UpdateSettings(bg, alice.ID, map[string]*string{exclude.SettingKey: ptr(`["skip.example"]`)}); err != nil {
		t.Fatal(err)
	}
	e.submit(e.scope(alice, true), aIDs...)
	e.submit(e.scope(bob, true), bIDs...)
	e.drain()
	e.check.mu.Lock()
	defer e.check.mu.Unlock()
	if len(e.check.sets) != 2 {
		t.Fatalf("checks = %d", len(e.check.sets))
	}
	if !e.check.sets[0].MatchHost("skip.example") {
		t.Error("alice's exclusions were not applied to her job")
	}
	if e.check.sets[1].MatchHost("skip.example") || !e.check.sets[1].Empty() {
		t.Error("alice's exclusions leaked into bob's job")
	}
}

func TestStoredInvalidExclusionsNeverBreakOrWidenChecks(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	ids := e.add(alice, "https://x.example/")
	// Bypass validation as a corrupted row would.
	if _, err := e.db.SQL().Exec(`INSERT INTO user_settings(user_id, key, value, updated_at) VALUES (?, ?, ?, 1)`, alice.ID, exclude.SettingKey, `{"not":"a list"}`); err != nil {
		t.Fatal(err)
	}
	j := e.submit(e.scope(alice, true), ids...)
	e.drain()
	if got := e.status(e.scope(alice, true), j.ID); got.Status != "completed" {
		t.Fatalf("job = %+v", got)
	}
	e.check.mu.Lock()
	defer e.check.mu.Unlock()
	if !e.check.sets[0].Empty() {
		t.Error("corrupt setting produced exclusions")
	}
}

func TestActiveJobsPerUserAreBounded(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	ids := e.add(alice, "https://a.example/")
	bobIDs := e.add(bob, "https://b.example/")
	sc := e.scope(alice, true)
	for i := 0; i < linkcheck.MaxActiveJobs; i++ {
		e.submit(sc, ids...)
	}
	if _, err := e.svc.SubmitCheck(bg, sc, ids); !errors.Is(err, linkcheck.ErrTooManyJobs) {
		t.Fatalf("err = %v, want ErrTooManyJobs", err)
	}
	if _, err := e.svc.SubmitCheck(bg, e.scope(bob, true), bobIDs); err != nil {
		t.Errorf("bob blocked by alice's queue: %v", err)
	}
	e.drain()
	if _, err := e.svc.SubmitCheck(bg, sc, ids); err != nil {
		t.Errorf("after the queue drained: %v", err)
	}
}

func TestRunProcessesQueuedJobsInTheBackground(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	ids := e.add(alice, "https://bg.example/")
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { e.svc.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	j := e.submit(e.scope(alice, true), ids...)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got := e.status(e.scope(alice, true), j.ID); got.Status == "completed" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never completed the job")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunDoesNotLetOneUsersQueueBlockOtherAccounts(t *testing.T) {
	e := newEnv(t)
	alice, bob := e.user("alice"), e.user("bob")
	aliceIDs := e.add(alice, "https://a0.example/", "https://a1.example/", "https://a2.example/", "https://a3.example/", "https://a4.example/")
	bobIDs := e.add(bob, "https://bob.example/")
	for _, id := range aliceIDs {
		e.submit(e.scope(alice, true), id)
	}
	e.submit(e.scope(bob, true), bobIDs[0])
	started := make(chan string, 8)
	release := make(chan struct{})
	var releaseOnce sync.Once
	e.check.hook = func(url string) { started <- url; <-release }
	ctx, cancel := context.WithCancel(bg)
	done := make(chan struct{})
	go func() { e.svc.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		releaseOnce.Do(func() { close(release) })
		<-done
	})
	seen := map[string]bool{}
	deadline := time.After(3 * time.Second)
	for len(seen) < 2 {
		select {
		case url := <-started:
			seen[url] = true
		case <-deadline:
			t.Fatalf("only these URLs started while checks were held: %v", seen)
		}
	}
	if !seen["https://a0.example/"] || !seen["https://bob.example/"] {
		t.Fatalf("expected an Alice and Bob check to run concurrently, got %v", seen)
	}
	releaseOnce.Do(func() { close(release) })
}

func TestServiceNeverContactsPrivateTargets(t *testing.T) {
	e := newEnv(t)
	alice := e.user("alice")
	ids := e.add(alice, "http://127.0.0.1:1/", "http://[::ffff:10.0.0.1]/", "http://169.254.169.254/")
	dialed := false
	real := linkcheck.NewChecker(linkcheck.Config{
		Resolve: func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no dns") },
		Dial: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("must not dial")
		},
	})
	e.svc = linkcheck.NewService(e.db.SQL(), e.bm, e.ids, real)
	j := e.submit(e.scope(alice, true), ids...)
	e.drain()
	got := e.status(e.scope(alice, true), j.ID)
	for _, r := range got.Results {
		if r.Status != linkcheck.StatusBlocked {
			t.Errorf("result %+v, want blocked", r)
		}
	}
	if dialed {
		t.Fatal("a private destination was dialed")
	}
}

func ptr(s string) *string { return &s }
