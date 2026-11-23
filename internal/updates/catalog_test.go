package updates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCatalogListsOnlyPublishedCompleteSHAReleases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/releases" || r.URL.Query().Get("per_page") != "100" {
			t.Fatalf("unexpected release request: %s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
{"tag_name":"build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","draft":false,"prerelease":true,"published_at":"2026-11-23T12:00:00Z","assets":[{"name":"bookmarksync-linux-amd64"},{"name":"BUILD_INFO.txt"},{"name":"SHA256SUMS"}]},
{"tag_name":"build-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","draft":true,"prerelease":true,"published_at":"2026-11-23T13:00:00Z","assets":[{"name":"bookmarksync-linux-amd64"},{"name":"BUILD_INFO.txt"},{"name":"SHA256SUMS"}]},
{"tag_name":"build-cccccccccccccccccccccccccccccccccccccccc","draft":false,"prerelease":true,"published_at":"2026-11-23T14:00:00Z","assets":[{"name":"bookmarksync-linux-amd64"}]},
{"tag_name":"latest","draft":false,"prerelease":true,"published_at":"2026-11-23T15:00:00Z","assets":[{"name":"bookmarksync-linux-amd64"},{"name":"BUILD_INFO.txt"},{"name":"SHA256SUMS"}]},
{"tag_name":"build-dddddddddddddddddddddddddddddddddddddddd","draft":false,"prerelease":true,"published_at":"2026-11-23T11:00:00Z","assets":[{"name":"bookmarksync-linux-amd64"},{"name":"BUILD_INFO.txt"},{"name":"SHA256SUMS"}]}
]`))
	}))
	defer server.Close()

	got, err := (CatalogClient{BaseURL: server.URL + "/releases?per_page=100", HTTPClient: server.Client()}).List(context.Background(), "dddddddddddddddddddddddddddddddddddddddd")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Releases) != 2 || got.Releases[0].Tag != "build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || got.Releases[1].Tag != "build-dddddddddddddddddddddddddddddddddddddddd" {
		t.Fatalf("eligible releases = %+v", got.Releases)
	}
	if got.InstalledCommit != "dddddddddddddddddddddddddddddddddddddddd" || !got.UpdateAvailable || got.Latest == nil || got.Latest.Tag != got.Releases[0].Tag {
		t.Fatalf("catalog summary = %+v", got)
	}
}

func TestCatalogRejectsErrorMalformedAndOversizedResponses(t *testing.T) {
	tests := []struct {
		name      string
		code      int
		body      string
		wantEmpty bool
	}{
		{name: "http error", code: http.StatusBadGateway, body: `[]`},
		{name: "invalid json", code: http.StatusOK, body: `{`},
		{name: "wrong root type", code: http.StatusOK, body: `{}`},
		{name: "missing timestamp is ignored", code: http.StatusOK, body: `[{"tag_name":"build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","draft":false,"prerelease":true,"published_at":"bad","assets":[{"name":"bookmarksync-linux-amd64"},{"name":"BUILD_INFO.txt"},{"name":"SHA256SUMS"}]}]`, wantEmpty: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.code)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			got, err := (CatalogClient{BaseURL: server.URL + "/releases?per_page=100", HTTPClient: server.Client()}).List(context.Background(), "")
			if tt.wantEmpty {
				if err != nil || len(got.Releases) != 0 {
					t.Fatalf("catalog = %+v, error = %v", got, err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected catalog error")
			}
		})
	}
}

func TestCatalogClientUsesBoundedTimeoutByDefault(t *testing.T) {
	client := CatalogClient{}
	if got := client.timeout(); got <= 0 || got > 15*time.Second {
		t.Fatalf("timeout = %s", got)
	}
}

func TestCatalogErrorDoesNotExposeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("private release data"))
	}))
	defer server.Close()
	_, err := (CatalogClient{BaseURL: server.URL + "/releases?per_page=100", HTTPClient: server.Client()}).List(context.Background(), "")
	if err == nil || strings.Contains(err.Error(), "private release data") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestValidateApprovedTagAcceptsOnlyFullLowercaseBuildHashes(t *testing.T) {
	if err := ValidateApprovedTag("build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"latest", "build-aaaaaaaaaaaa", "build-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa;id", "refs/heads/main"} {
		if err := ValidateApprovedTag(invalid); err == nil {
			t.Errorf("ValidateApprovedTag(%q) succeeded", invalid)
		}
	}
}
