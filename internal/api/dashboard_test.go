package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func getDashboard(t *testing.T, e *tenv, query string, mods ...mod) map[string]any {
	t.Helper()
	rec := e.do("GET", "/api/v1/me/dashboard"+query, nil, mods...)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /me/dashboard%s: %d %s", query, rec.Code, rec.Body.String())
	}
	return decode(t, rec)
}

func TestDashboardEmptyAccount(t *testing.T) {
	e := newTEnv(t)
	d := getDashboard(t, e, "", e.login(e.alice).mods()...)
	rc := d["recentChanges"].(map[string]any)
	counts := rc["counts"].(map[string]any)
	items, ok := rc["items"].([]any)
	if d["totalBookmarks"].(float64) != 0 || d["duplicateCount"].(float64) != 0 || d["lastContactAt"] != nil ||
		!ok || len(items) != 0 || rc["hasMore"] != false || rc["windowDays"].(float64) != 7 ||
		counts["created"].(float64) != 0 || counts["updated"].(float64) != 0 || counts["deleted"].(float64) != 0 {
		t.Fatalf("empty dashboard = %v", d)
	}
	wantSince := e.clock.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	if got, _ := rc["since"].(string); got != wantSince {
		t.Fatalf("since = %q, want %q", got, wantSince)
	}
}

func TestDashboardOwnSummaryDuplicatesAndLastContact(t *testing.T) {
	e := newTEnv(t)
	dupSetup(t, e) // alice: 3 live records, one duplicate pair
	alice := e.login(e.alice)

	d := getDashboard(t, e, "", alice.mods()...)
	if d["totalBookmarks"].(float64) != 3 || d["duplicateCount"].(float64) != 1 || d["lastContactAt"] != nil {
		t.Fatalf("dashboard = %v", d)
	}
	rc := d["recentChanges"].(map[string]any)
	if c := rc["counts"].(map[string]any); c["created"].(float64) != 3 || c["updated"].(float64) != 0 || c["deleted"].(float64) != 0 {
		t.Fatalf("counts = %v", c)
	}
	item := rc["items"].([]any)[0].(map[string]any)
	for _, k := range []string{"type", "id", "title", "url", "at"} {
		if _, ok := item[k]; !ok {
			t.Errorf("recent item lacks %q: %v", k, item)
		}
	}

	// lastContactAt is the newest Floccus credential use.
	ctx := context.Background()
	c1, s1, err := e.ids.CreateCredential(ctx, identity.NewCredential{UserID: e.alice.ID, Label: "a", Mode: identity.ModeEdit})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	contact := e.clock.Now()
	if _, err := e.ids.Authenticate(ctx, e.alice.Username, s1); err != nil {
		t.Fatal(err)
	}
	_ = c1
	d = getDashboard(t, e, "", alice.mods()...)
	if got, _ := d["lastContactAt"].(string); got != contact.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("lastContactAt = %v, want %s", d["lastContactAt"], contact.UTC().Format(time.RFC3339Nano))
	}
	// A later contact from a revoked connection is historical, not current sync status.
	c2, s2, err := e.ids.CreateCredential(ctx, identity.NewCredential{UserID: e.alice.ID, Label: "revoked", Mode: identity.ModeEdit})
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Minute)
	if _, err := e.ids.Authenticate(ctx, e.alice.Username, s2); err != nil {
		t.Fatal(err)
	}
	if err := e.ids.RevokeUserCredential(ctx, e.alice.ID, c2.ID); err != nil {
		t.Fatal(err)
	}
	d = getDashboard(t, e, "", alice.mods()...)
	if got, _ := d["lastContactAt"].(string); got != contact.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("lastContactAt included a revoked credential: got %v, want latest active contact %s", d["lastContactAt"], contact.UTC().Format(time.RFC3339Nano))
	}

	// A change after the window falls out of recent changes but stays in the total.
	read := e.token(e.alice, identity.ScopeBookmarksRead) // the web session itself expires in 8 days
	e.clock.Advance(8 * 24 * time.Hour)
	d = getDashboard(t, e, "", withBearer(read))
	rc = d["recentChanges"].(map[string]any)
	if d["totalBookmarks"].(float64) != 3 || len(rc["items"].([]any)) != 0 {
		t.Fatalf("old changes still recent: %v", d)
	}
}

func TestDashboardPaginationAndValidation(t *testing.T) {
	e := newTEnv(t)
	dupSetup(t, e)
	alice := e.login(e.alice)
	rc := getDashboard(t, e, "?limit=2", alice.mods()...)["recentChanges"].(map[string]any)
	if len(rc["items"].([]any)) != 2 || rc["hasMore"] != true || rc["limit"].(float64) != 2 || rc["page"].(float64) != 0 {
		t.Fatalf("page 0 = %v", rc)
	}
	rc = getDashboard(t, e, "?limit=2&page=1", alice.mods()...)["recentChanges"].(map[string]any)
	if len(rc["items"].([]any)) != 1 || rc["hasMore"] != false || rc["counts"].(map[string]any)["created"].(float64) != 3 {
		t.Fatalf("page 1 = %v", rc)
	}
	for _, q := range []string{"page=-1", "limit=0", "page=x", "limit=x", "page=1000001"} {
		if rec := e.do("GET", "/api/v1/me/dashboard?"+q, nil, alice.mods()...); rec.Code != http.StatusBadRequest || errCode(t, rec) != "invalid_request" {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body.String())
		}
	}
	if rec := e.do("GET", "/api/v1/me/dashboard?share=1", nil, alice.mods()...); rec.Code != http.StatusBadRequest {
		t.Errorf("share selector: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDashboardDoesNotIncludeSharedOrOtherUserRecords(t *testing.T) {
	e := newTEnv(t)
	f := dupSetup(t, e) // alice owns everything; bob created one record inside alice's share
	bob := e.login(e.bob)
	bobOther := e.otherFolder(bob.mods()...)
	e.mkBookmark(bob, bobOther, "https://bob-own.example/")

	// Bob's dashboard counts only his own collection: not alice's records, and not the
	// record he created inside alice's shared folder (it belongs to alice's collection).
	rec := e.do("GET", "/api/v1/me/dashboard", nil, bob.mods()...)
	body := rec.Body.String()
	d := decode(t, rec)
	if d["totalBookmarks"].(float64) != 1 || d["duplicateCount"].(float64) != 0 ||
		strings.Contains(body, "SECRET") || strings.Contains(body, "dup.example") || strings.Contains(body, "shared copy") {
		t.Fatalf("bob dashboard leaked: %s", body)
	}
	// The share selector is not a dashboard scope.
	if rec := e.do("GET", fmt.Sprintf("/api/v1/me/dashboard?share=%d", f.share), nil, bob.mods()...); rec.Code != http.StatusBadRequest {
		t.Fatalf("share dashboard: %d %s", rec.Code, rec.Body.String())
	}
	alice := e.login(e.alice)
	if body := e.do("GET", "/api/v1/me/dashboard", nil, alice.mods()...).Body.String(); strings.Contains(body, "bob-own") {
		t.Fatalf("alice dashboard leaked bob's record: %s", body)
	}
}

func TestDashboardReadTokenAuthorization(t *testing.T) {
	e := newTEnv(t)
	dupSetup(t, e)
	read := e.token(e.alice, identity.ScopeBookmarksRead)
	if rec := e.do("GET", "/api/v1/me/dashboard", nil, withBearer(read)); rec.Code != http.StatusOK {
		t.Fatalf("read token: %d %s", rec.Code, rec.Body.String())
	}
	for name, tok := range map[string]string{
		"write only":   e.token(e.alice, identity.ScopeBookmarksWrite),
		"shares only":  e.token(e.alice, identity.ScopeSharesRead),
		"all but read": e.token(e.alice, allScopesExcept(identity.ScopeBookmarksRead)...),
	} {
		if rec := e.do("GET", "/api/v1/me/dashboard", nil, withBearer(tok)); rec.Code != http.StatusForbidden || errCode(t, rec) != "insufficient_scope" {
			t.Errorf("%s token: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := e.do("GET", "/api/v1/me/dashboard", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %s", rec.Code, rec.Body.String())
	}
	// A bearer token acts for its own user only.
	bobTok := e.token(e.bob, identity.ScopeBookmarksRead)
	if d := getDashboard(t, e, "", withBearer(bobTok)); d["totalBookmarks"].(float64) != 0 {
		t.Fatalf("bob token saw alice's data: %v", d)
	}
}
