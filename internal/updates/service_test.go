package updates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCatalogServiceCachesUpstreamAndRecomputesInstalledState(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`[{"tag_name":"build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","draft":false,"prerelease":true,"published_at":"2026-11-23T14:00:00Z","assets":[{"name":"bookmarksync-linux-amd64"},{"name":"bookmarksync-windows-amd64.exe"},{"name":"bookmarksync-updater-windows-amd64.exe"},{"name":"BUILD_INFO.txt"},{"name":"SHA256SUMS"}]}]`))
	}))
	defer server.Close()
	service := NewCatalogService(CatalogClient{BaseURL: server.URL + "/releases?per_page=100", HTTPClient: server.Client()})

	first, err := service.List(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.List(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("upstream request count = %d", requests.Load())
	}
	if !first.UpdateAvailable || second.UpdateAvailable || second.InstalledCommit != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("catalog states = first %+v second %+v", first, second)
	}
}
