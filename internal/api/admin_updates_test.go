package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/updates"
)

func TestAdminReleaseCatalogRequiresAdminAndReturnsSafeMetadata(t *testing.T) {
	called := false
	commit := "dddddddddddddddddddddddddddddddddddddddd"
	issued := updates.Catalog{InstalledCommit: commit, UpdateAvailable: true, Releases: []updates.Release{{Tag: "build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", PublishedAt: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC), URL: "https://github.com/atikansari-ghr/BookmarksSync/releases/tag/build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	e := newTEnvWithCatalog(t, func(_ context.Context, installed string) (updates.Catalog, error) {
		called = true
		if installed != commit {
			t.Fatalf("installed commit = %q", installed)
		}
		return issued, nil
	}, nil)
	if rec := e.do(http.MethodGet, "/api/v1/admin/updates", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous catalog request = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodGet, "/api/v1/admin/updates", nil, e.login(e.alice).mods()...); rec.Code != http.StatusForbidden {
		t.Fatalf("user catalog request = %d %s", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("unauthorized request reached catalog client")
	}
	rec := e.do(http.MethodGet, "/api/v1/admin/updates", nil, e.login(e.root).mods()...)
	if rec.Code != http.StatusOK || !called || !strings.Contains(rec.Body.String(), `"updateAvailable":true`) || !strings.Contains(rec.Body.String(), issued.Releases[0].Tag) {
		t.Fatalf("admin catalog response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminReleaseCatalogHidesUpstreamErrors(t *testing.T) {
	e := newTEnvWithCatalog(t, func(context.Context, string) (updates.Catalog, error) {
		return updates.Catalog{}, errors.New("upstream secret response")
	}, nil)
	rec := e.do(http.MethodGet, "/api/v1/admin/updates", nil, e.login(e.root).mods()...)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "upstream secret") {
		t.Fatalf("upstream failure response = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminApprovalRequiresRecentAuthAndPublishedTag(t *testing.T) {
	const tag = "build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var approved string
	e := newTEnvWithCatalog(t, func(context.Context, string) (updates.Catalog, error) {
		return updates.Catalog{Releases: []updates.Release{{Tag: tag}}}, nil
	}, func(_ context.Context, selected string) (string, error) {
		approved = selected
		return "bookmarksync-update-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
	})
	if rec := e.do(http.MethodPost, "/api/v1/admin/updates/approve", map[string]string{"tag": tag}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous approval = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodPost, "/api/v1/admin/updates/approve", map[string]string{"tag": tag}, e.login(e.alice).mods()...); rec.Code != http.StatusForbidden {
		t.Fatalf("user approval = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(http.MethodPost, "/api/v1/admin/updates/approve", map[string]string{"tag": "latest"}, e.login(e.root).mods()...); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid-tag approval = %d %s", rec.Code, rec.Body.String())
	}
	if approved != "" {
		t.Fatal("invalid tag reached the updater")
	}
	admin := e.login(e.root)
	queued := e.do(http.MethodPost, "/api/v1/admin/updates/approve", map[string]string{"tag": tag}, admin.mods()...)
	if queued.Code != http.StatusAccepted || approved != tag || !strings.Contains(queued.Body.String(), `"status":"accepted"`) {
		t.Fatalf("approval = %d %s, approved %q", queued.Code, queued.Body.String(), approved)
	}
	activity := e.do(http.MethodGet, "/api/v1/admin/activity", nil, admin.mods()...)
	if activity.Code != http.StatusOK || !strings.Contains(activity.Body.String(), "software_update_approved") {
		t.Fatalf("approval audit = %d %s", activity.Code, activity.Body.String())
	}
}

func TestAdminApprovalRejectsStaleSessionAndUnlistedRelease(t *testing.T) {
	const tag = "build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var called int
	e := newTEnvWithCatalog(t, func(context.Context, string) (updates.Catalog, error) {
		return updates.Catalog{}, nil
	}, func(context.Context, string) (string, error) { called++; return "job", nil })
	admin := e.login(e.root)
	e.clock.Advance(6 * time.Minute)
	stale := e.do(http.MethodPost, "/api/v1/admin/updates/approve", map[string]string{"tag": tag}, admin.mods()...)
	if stale.Code != http.StatusForbidden || errCode(t, stale) != "reauthentication_required" {
		t.Fatalf("stale approval = %d %s", stale.Code, stale.Body.String())
	}
	if called != 0 {
		t.Fatal("stale session reached updater")
	}
	admin = e.login(e.root)
	unlisted := e.do(http.MethodPost, "/api/v1/admin/updates/approve", map[string]string{"tag": tag}, admin.mods()...)
	if unlisted.Code != http.StatusConflict || errCode(t, unlisted) != "release_unavailable" {
		t.Fatalf("unlisted release = %d %s", unlisted.Code, unlisted.Body.String())
	}
	if called != 0 {
		t.Fatal("unlisted release reached updater")
	}
}

func TestAdminApprovalReportsMissingHostRunner(t *testing.T) {
	const tag = "build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	e := newTEnvWithCatalog(t, func(context.Context, string) (updates.Catalog, error) {
		return updates.Catalog{Releases: []updates.Release{{Tag: tag}}}, nil
	}, nil)
	admin := e.login(e.root)
	rec := e.do(http.MethodPost, "/api/v1/admin/updates/approve", map[string]string{"tag": tag}, admin.mods()...)
	if rec.Code != http.StatusServiceUnavailable || errCode(t, rec) != "updates_unavailable" {
		t.Fatalf("approval without a host runner = %d %s", rec.Code, rec.Body.String())
	}
}
