package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func TestAdminHealthRequiresActiveAdmin(t *testing.T) {
	e := newTEnv(t)
	if rec := e.do("GET", "/api/v1/admin/health", nil); rec.Code != 401 {
		t.Fatalf("anonymous status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := e.do("GET", "/api/v1/admin/health", nil, e.login(e.alice).mods()...); rec.Code != 403 || errCode(t, rec) != "admin_required" {
		t.Fatalf("user health status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec := e.do("GET", "/api/v1/admin/health", nil, e.login(e.root).mods()...); rec.Code != 200 {
		t.Fatalf("admin health status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminHealthContainsNoBookmarkContentOrSecrets(t *testing.T) {
	e := newTEnv(t)
	ctx := context.Background()
	scope, err := e.bm.PrivateScope(ctx, e.alice.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := e.bm.FolderTree(ctx, scope, bookmarks.RootID, 1)
	if err != nil || len(roots) == 0 {
		t.Fatalf("folders = %v, %v", roots, err)
	}
	_, err = e.bm.CreateBookmark(ctx, scope, bookmarks.BookmarkInput{URL: "https://bookmark-secret.invalid/path", Title: "PRIVATE_BOOKMARK_TITLE", Folders: []int64{roots[0].ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := e.ids.CreateCredential(ctx, identity.NewCredential{UserID: e.alice.ID, Mode: identity.ModeEdit})
	if err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/api/v1/admin/health", nil, e.login(e.root).mods()...)
	if rec.Code != 200 {
		t.Fatalf("health status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"PRIVATE_BOOKMARK_TITLE", "bookmark-secret.invalid", secret, "password_hash", "secret_hash"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("health response leaked %q: %s", forbidden, body)
		}
	}
	var report map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report["dbReady"] != true {
		t.Fatalf("dbReady = %v", report["dbReady"])
	}
	if _, ok := report["syncErrors"]; !ok {
		t.Fatalf("syncErrors absent: %v", report)
	}
}
