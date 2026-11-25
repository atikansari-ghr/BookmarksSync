package api_test

import (
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/api"
)

func TestAdminNetworkStatusTracksReloadedCountryDatabase(t *testing.T) {
	available := false
	built := time.Time{}
	e := newTEnvWithOptions(t, nil, nil, func(opts *api.Options) {
		opts.System.GeoIPStatus = func() (bool, time.Time) { return available, built }
	})
	admin := e.login(e.root)
	first := e.do("GET", "/api/v1/admin/network-access", nil, admin.mods()...)
	if first.Code != 200 || decode(t, first)["geoDatabaseAvailable"] != false {
		t.Fatalf("missing startup: %s", first.Body.String())
	}
	available, built = true, time.Date(2026, 11, 20, 4, 0, 0, 0, time.UTC)
	second := e.do("GET", "/api/v1/admin/network-access", nil, admin.mods()...)
	if second.Code != 200 || decode(t, second)["geoDatabaseAvailable"] != true || decode(t, second)["geoDatabaseBuildTime"] != built.Format(time.RFC3339) {
		t.Fatalf("refreshed status: %s", second.Body.String())
	}
	updated := e.do("PATCH", "/api/v1/admin/network-access", map[string]any{"mode": "allow", "countries": []string{"GB"}}, admin.mods()...)
	if updated.Code != 200 || decode(t, updated)["geoDatabaseAvailable"] != true {
		t.Fatalf("updated policy status: %s", updated.Body.String())
	}
}
