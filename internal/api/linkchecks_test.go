package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck"
	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck/exclude"
)

// okChecker answers every check without touching any network.
type okChecker struct{}

func (okChecker) Check(_ context.Context, u string, _ exclude.Set) linkcheck.Result {
	if strings.Contains(u, "gone.example") {
		return linkcheck.Result{Status: linkcheck.StatusBroken, HTTPStatus: 404}
	}
	return linkcheck.Result{Status: linkcheck.StatusOK, HTTPStatus: 200}
}

const secretLinkURL = "https://alice-secret.example/private-path"

type linkFixture struct {
	ids              []int64 // alice's private bookmarks
	shared           int64   // folder shared with bob
	inShare          int64   // bookmark inside it
	viewShare, other int64   // share ids: view-only grant and edit grant (set by editShare)
}

func linkSetup(t *testing.T, e *tenv) linkFixture {
	t.Helper()
	ctx := context.Background()
	sc, err := e.bm.PrivateScope(ctx, e.alice.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	kids, err := e.bm.FolderTree(ctx, sc, bookmarks.RootID, 1)
	if err != nil || len(kids) < 2 {
		t.Fatalf("roots: %v %v", kids, err)
	}
	shared, err := e.bm.CreateFolder(ctx, sc, kids[1].ID, "Shared")
	if err != nil {
		t.Fatal(err)
	}
	var f linkFixture
	for _, u := range []string{secretLinkURL, "https://gone.example/x"} {
		b, err := e.bm.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: u, Title: "SECRET title", Folders: []int64{kids[0].ID}})
		if err != nil {
			t.Fatal(err)
		}
		f.ids = append(f.ids, b.ID)
	}
	in, err := e.bm.CreateBookmark(ctx, sc, bookmarks.BookmarkInput{URL: "https://in-share.example/", Title: "S", Folders: []int64{shared.ID}})
	if err != nil {
		t.Fatal(err)
	}
	sh, err := e.bm.GrantShare(ctx, e.alice.ID, shared.ID, e.bob.ID, identity.ModeView)
	if err != nil {
		t.Fatal(err)
	}
	f.shared, f.inShare, f.viewShare = shared.ID, in.ID, sh.ID
	return f
}

func submitLinks(e *tenv, query string, ids []int64, mods ...mod) (int, map[string]any) {
	rec := e.do("POST", "/api/v1/link-checks"+query, map[string]any{"bookmarkIds": ids}, mods...)
	var out map[string]any
	if rec.Code == http.StatusAccepted {
		out = decode(e.t, rec)
	}
	return rec.Code, out
}

func TestLinkCheckSubmitProcessAndStatus(t *testing.T) {
	e := newTEnv(t)
	f := linkSetup(t, e)
	alice := e.login(e.alice)

	code, job := submitLinks(e, "", f.ids, alice.mods()...)
	if code != http.StatusAccepted {
		t.Fatalf("submit = %d", code)
	}
	id := int64(job["id"].(float64))
	if job["status"] != "queued" || job["total"].(float64) != 2 || job["done"].(float64) != 0 || job["finishedAt"] != nil {
		t.Fatalf("submitted = %v", job)
	}
	rec := e.do("GET", fmt.Sprintf("/api/v1/link-checks/%d", id), nil, alice.mods()...)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET queued = %d %v", rec.Code, rec.Header())
	}
	if _, err := e.lc.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := decode(t, e.do("GET", fmt.Sprintf("/api/v1/link-checks/%d", id), nil, alice.mods()...))
	if got["status"] != "completed" || got["done"].(float64) != 2 || got["finishedAt"] == nil {
		t.Fatalf("completed = %v", got)
	}
	res := got["results"].([]any)
	r0, r1 := res[0].(map[string]any), res[1].(map[string]any)
	if r0["bookmarkId"].(float64) != float64(f.ids[0]) || r0["status"] != "ok" || r0["httpStatus"].(float64) != 200 || r0["url"] != secretLinkURL {
		t.Errorf("result 0 = %v", r0)
	}
	if r1["status"] != "broken" || r1["httpStatus"].(float64) != 404 {
		t.Errorf("result 1 = %v", r1)
	}
	for _, k := range []string{"bookmarkId", "url", "status", "httpStatus", "detail", "redirects", "checkedAt"} {
		if _, ok := r0[k]; !ok {
			t.Errorf("result lacks %q: %v", k, r0)
		}
	}
	// Titles, tags and folders are not part of link-check results.
	if body := e.do("GET", fmt.Sprintf("/api/v1/link-checks/%d", id), nil, alice.mods()...).Body.String(); strings.Contains(body, "SECRET title") {
		t.Errorf("result leaks bookmark titles: %s", body)
	}
}

func TestLinkCheckTokenScopesAndValidation(t *testing.T) {
	e := newTEnv(t)
	f := linkSetup(t, e)
	read := e.token(e.alice, identity.ScopeBookmarksRead)
	write := e.token(e.alice, identity.ScopeBookmarksWrite)
	both := e.token(e.alice, identity.ScopeBookmarksRead, identity.ScopeBookmarksWrite)

	if code, _ := submitLinks(e, "", f.ids, withBearer(read)); code != http.StatusForbidden {
		t.Errorf("read-only token submit = %d, want 403", code)
	}
	code, job := submitLinks(e, "", f.ids, withBearer(write))
	if code != http.StatusAccepted {
		t.Fatalf("write token submit = %d", code)
	}
	url := fmt.Sprintf("/api/v1/link-checks/%d", int64(job["id"].(float64)))
	if rec := e.do("GET", url, nil, withBearer(write)); rec.Code != http.StatusForbidden {
		t.Errorf("write-only token read = %d, want 403", rec.Code)
	}
	if rec := e.do("GET", url, nil, withBearer(read)); rec.Code != http.StatusOK {
		t.Errorf("read token read = %d, want 200", rec.Code)
	}
	if rec := e.do("GET", url, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous read = %d", rec.Code)
	}
	if rec := e.do("POST", "/api/v1/link-checks", map[string]any{"bookmarkIds": f.ids}); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous submit = %d", rec.Code)
	}

	many := make([]int64, 51)
	for i := range many {
		many[i] = int64(i + 1)
	}
	for name, body := range map[string]any{
		"empty list": map[string]any{"bookmarkIds": []int64{}}, "missing list": map[string]any{},
		"zero id": map[string]any{"bookmarkIds": []int64{0}}, "negative id": map[string]any{"bookmarkIds": []int64{-3}},
		"duplicate ids": map[string]any{"bookmarkIds": []int64{f.ids[0], f.ids[0]}}, "51 ids": map[string]any{"bookmarkIds": many},
		"unknown field": map[string]any{"bookmarkIds": f.ids, "url": "http://127.0.0.1/"}, "string id": map[string]any{"bookmarkIds": []string{"1"}},
	} {
		rec := e.do("POST", "/api/v1/link-checks", body, withBearer(both))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	for _, id := range []string{"abc", "0", "-1", "99999"} {
		if rec := e.do("GET", "/api/v1/link-checks/"+id, nil, withBearer(both)); rec.Code != http.StatusNotFound {
			t.Errorf("GET /link-checks/%s = %d, want 404", id, rec.Code)
		}
	}
	if rec := e.do("GET", "/api/v1/link-checks/1?share=0", nil, withBearer(both)); rec.Code != http.StatusBadRequest {
		t.Errorf("bad share = %d", rec.Code)
	}
}

func TestLinkCheckResultsAreIsolatedAcrossUsersAndScopes(t *testing.T) {
	e := newTEnv(t)
	f := linkSetup(t, e)
	alice, bob := e.login(e.alice), e.login(e.bob)

	_, job := submitLinks(e, "", f.ids, alice.mods()...)
	aliceJob := fmt.Sprintf("/api/v1/link-checks/%d", int64(job["id"].(float64)))
	if _, err := e.lc.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Another user cannot read it, by cookie, token, or through a share they belong to.
	for name, mods := range map[string][]mod{
		"bob session": bob.mods(), "bob token": {withBearer(e.token(e.bob, identity.ScopeBookmarksRead))},
	} {
		if rec := e.do("GET", aliceJob, nil, mods...); rec.Code != http.StatusNotFound {
			t.Errorf("%s reading alice's job = %d, want 404", name, rec.Code)
		}
	}
	if rec := e.do("GET", aliceJob+fmt.Sprintf("?share=%d", f.viewShare), nil, bob.mods()...); rec.Code != http.StatusNotFound {
		t.Errorf("bob reading alice's private job through the share = %d, want 404", rec.Code)
	}
	// Bob cannot check alice's bookmarks, nor ones outside his share.
	if code, _ := submitLinks(e, "", f.ids, bob.mods()...); code != http.StatusNotFound {
		t.Errorf("bob submitting alice's bookmarks = %d, want 404", code)
	}
	if code, _ := submitLinks(e, fmt.Sprintf("?share=%d", f.viewShare), f.ids, bob.mods()...); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("bob submitting outside his view share = %d", code)
	}
	// A view-only share recipient may not queue checks at all (nor with a write token).
	if code, _ := submitLinks(e, fmt.Sprintf("?share=%d", f.viewShare), []int64{f.inShare}, bob.mods()...); code != http.StatusForbidden {
		t.Errorf("view-only share submit = %d, want 403", code)
	}
	if code, _ := submitLinks(e, fmt.Sprintf("?share=%d", f.viewShare), []int64{f.inShare}, withBearer(e.token(e.bob, identity.ScopeBookmarksWrite))); code != http.StatusForbidden {
		t.Errorf("view-only share submit with write token = %d, want 403", code)
	}

	// With an edit grant bob can; the job is visible only in that share, to him.
	if _, err := e.bm.UpdateShare(context.Background(), e.alice.ID, f.viewShare, identity.ModeEdit); err != nil {
		t.Fatal(err)
	}
	q := fmt.Sprintf("?share=%d", f.viewShare)
	code, bj := submitLinks(e, q, []int64{f.inShare}, bob.mods()...)
	if code != http.StatusAccepted {
		t.Fatalf("edit share submit = %d", code)
	}
	bobURL := fmt.Sprintf("/api/v1/link-checks/%d", int64(bj["id"].(float64)))
	if _, err := e.lc.ProcessNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec := e.do("GET", bobURL+q, nil, bob.mods()...); rec.Code != 200 {
		t.Errorf("bob reading his share job = %d", rec.Code)
	}
	if rec := e.do("GET", bobURL, nil, bob.mods()...); rec.Code != http.StatusNotFound {
		t.Errorf("bob's share job visible in his private scope = %d, want 404", rec.Code)
	}
	if rec := e.do("GET", bobURL, nil, alice.mods()...); rec.Code != http.StatusNotFound {
		t.Errorf("owner reading the recipient's job = %d, want 404", rec.Code)
	}
	if rec := e.do("GET", bobURL+q, nil, alice.mods()...); rec.Code != http.StatusNotFound {
		t.Errorf("owner reading the recipient's job via share = %d, want 404", rec.Code)
	}
	// Revoking the share ends access to the share's job.
	if err := e.bm.RevokeShare(context.Background(), e.alice.ID, f.shared, e.bob.ID); err != nil {
		t.Fatal(err)
	}
	if rec := e.do("GET", bobURL+q, nil, bob.mods()...); rec.Code != http.StatusNotFound {
		t.Errorf("revoked share job = %d, want 404", rec.Code)
	}

	// Link-check data never shows up in admin responses.
	root := e.login(e.root)
	for _, p := range []string{"/api/v1/admin/system", "/api/v1/admin/users"} {
		body := e.do("GET", p, nil, root.mods()...).Body.String()
		for _, secret := range []string{"alice-secret", "gone.example", "in-share.example", "SECRET"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s exposes %q", p, secret)
			}
		}
	}
}

func TestLinkCheckActiveJobLimitIs429(t *testing.T) {
	e := newTEnv(t)
	f := linkSetup(t, e)
	alice := e.login(e.alice)
	for i := 0; i < linkcheck.MaxActiveJobs; i++ {
		if code, _ := submitLinks(e, "", f.ids, alice.mods()...); code != http.StatusAccepted {
			t.Fatalf("submit %d = %d", i, code)
		}
	}
	rec := e.do("POST", "/api/v1/link-checks", map[string]any{"bookmarkIds": f.ids}, alice.mods()...)
	if rec.Code != http.StatusTooManyRequests || errCode(t, rec) != "too_many_jobs" {
		t.Fatalf("over the limit = %d %s", rec.Code, rec.Body.String())
	}
}

func TestLinkCheckExclusionSettingIsValidatedAndPerUser(t *testing.T) {
	e := newTEnv(t)
	alice, bob := e.login(e.alice), e.login(e.bob)
	patch := func(s sess, v any) *httptest.ResponseRecorder {
		return e.do("PATCH", "/api/v1/me/settings", map[string]any{exclude.SettingKey: v}, s.mods()...)
	}
	good := `["example.com", "10.0.0.0/8", "93.184.216.34"]`
	if rec := patch(alice, good); rec.Code != 200 {
		t.Fatalf("valid exclusions = %d %s", rec.Code, rec.Body.String())
	}
	got := decode(t, e.do("GET", "/api/v1/me/settings", nil, alice.mods()...))["settings"].(map[string]any)
	if got[exclude.SettingKey] != good {
		t.Fatalf("settings = %v", got)
	}
	for _, bad := range []string{`{}`, `["http://example.com"]`, `["*.example.com"]`, `["10.0.0.0/33"]`, `not json`, `["a b"]`, ``} {
		if rec := patch(alice, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("exclusions %q = %d, want 400", bad, rec.Code)
		}
	}
	if rec := e.do("PATCH", "/api/v1/me/settings", map[string]any{exclude.SettingKey: `["ok.example"]`, "bookmarks.view": "nonsense"}, alice.mods()...); rec.Code != 400 {
		t.Errorf("mixed batch = %d", rec.Code)
	}
	got = decode(t, e.do("GET", "/api/v1/me/settings", nil, alice.mods()...))["settings"].(map[string]any)
	if got[exclude.SettingKey] != good {
		t.Errorf("a rejected value changed the stored setting: %v", got)
	}
	if s := decode(t, e.do("GET", "/api/v1/me/settings", nil, bob.mods()...))["settings"].(map[string]any); len(s) != 0 {
		t.Errorf("exclusions leaked to another user: %v", s)
	}
	if rec := patch(alice, nil); rec.Code != 200 {
		t.Errorf("clearing = %d", rec.Code)
	}
	if s := decode(t, e.do("GET", "/api/v1/me/settings", nil, alice.mods()...))["settings"].(map[string]any); len(s) != 0 {
		t.Errorf("settings after clear = %v", s)
	}
}
