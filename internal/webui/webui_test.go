package webui_test

import (
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/atikansari-ghr/BookmarksSync/internal/server"
	"github.com/atikansari-ghr/BookmarksSync/internal/version"
	"github.com/atikansari-ghr/BookmarksSync/internal/webui"
)

func handlerFor(t *testing.T, files fs.FS) http.Handler {
	t.Helper()
	ui, err := webui.New(files)
	if err != nil {
		t.Fatal(err)
	}
	return server.NewHandler(server.Options{Version: version.Info{Version: "test"}, Ready: okChecker{}, Register: ui.Register})
}

type okChecker struct{}

func (okChecker) Ping(context.Context) error { return nil }

var testFiles = fstest.MapFS{
	"index.html":            {Data: []byte(`<!doctype html><html><head><script type="module" src="/auth/assets/app-abc123.js"></script></head><body><div id="root"></div></body></html>`)},
	"favicon.svg":           {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)},
	"assets/app-abc123.js":  {Data: []byte(`console.log(1)`)},
	"assets/app-abc123.css": {Data: []byte(`body{}`)},
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, "http://bookmarks.example.test"+path, nil))
	return rec
}

func TestPagesServeTheAppShellWithStrictHeaders(t *testing.T) {
	h := handlerFor(t, testFiles)
	for _, p := range []string{"/auth/", "/auth/login", "/auth/security", "/auth/oidc/complete"} {
		rec := get(t, h, "GET", p)
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), `<div id="root">`) {
			t.Fatalf("%s: %d %s %q", p, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
		csp := rec.Header().Get("Content-Security-Policy")
		for _, want := range []string{"default-src 'none'", "script-src 'self'", "style-src 'self'", "connect-src 'self'", "frame-ancestors 'none'", "base-uri 'none'", "form-action 'self'"} {
			if !strings.Contains(csp, want) {
				t.Errorf("%s: CSP %q lacks %q", p, csp, want)
			}
		}
		if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
			t.Errorf("%s: CSP allows unsafe sources: %q", p, csp)
		}
		for k, v := range map[string]string{
			"Cache-Control":              "no-store",
			"X-Frame-Options":            "DENY",
			"Referrer-Policy":            "no-referrer",
			"X-Content-Type-Options":     "nosniff",
			"Cross-Origin-Opener-Policy": "same-origin",
		} {
			if rec.Header().Get(k) != v {
				t.Errorf("%s: %s = %q, want %q", p, k, rec.Header().Get(k), v)
			}
		}
	}
	if rec := get(t, h, "HEAD", "/auth/login"); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: %d len %d", rec.Code, rec.Body.Len())
	}
}

func TestAssetsAreCachedImmutablyAndMissingFilesAre404(t *testing.T) {
	h := handlerFor(t, testFiles)
	rec := get(t, h, "GET", "/auth/assets/app-abc123.js")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") ||
		rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || rec.Body.String() != "console.log(1)" {
		t.Fatalf("asset: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	if rec := get(t, h, "GET", "/auth/assets/app-abc123.css"); !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("css type %q", rec.Header().Get("Content-Type"))
	}
	if rec := get(t, h, "GET", "/auth/favicon.svg"); rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/svg+xml") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("favicon: %d %v", rec.Code, rec.Header())
	}
	for _, p := range []string{"/auth/assets/missing.js", "/auth/assets/", "/auth/index.html", "/auth/unknown-page", "/auth/security/extra", "/auth/assets/../index.html"} {
		rec := get(t, h, "GET", p)
		if rec.Code == 200 {
			t.Errorf("%s served %d %q", p, rec.Code, rec.Body.String())
		}
	}
	if rec := get(t, h, "POST", "/auth/login"); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Fatalf("POST to a page: %d", rec.Code)
	}
}

func TestRootRedirectsToSignIn(t *testing.T) {
	h := handlerFor(t, testFiles)
	rec := get(t, h, "GET", "/")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/auth/login" {
		t.Fatalf("root: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// The API namespace is untouched.
	if rec := get(t, h, "GET", "/api/v1/unknown"); rec.Code != 404 || !strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Fatalf("api: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestNewRejectsAnIncompleteBuild(t *testing.T) {
	if _, err := webui.New(fstest.MapFS{"favicon.svg": {Data: []byte("x")}}); err == nil {
		t.Fatal("accepted a build without index.html")
	}
}

// The committed build must be complete and CSP-compatible: no inline scripts or
// styles, assets referenced under /auth/assets/ only.
func TestEmbeddedBuildIsCompleteAndCSPCompatible(t *testing.T) {
	files := webui.Files()
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		t.Fatalf("embedded build has no index.html: %v", err)
	}
	html := string(index)
	if regexp.MustCompile(`<script(?:\s[^>]*)?>\s*[^<\s]`).MatchString(html) || strings.Contains(html, "<style") || strings.Contains(html, " style=") {
		t.Fatalf("index.html has inline script or style:\n%s", html)
	}
	refs := regexp.MustCompile(`(?:src|href)="([^"]+)"`).FindAllStringSubmatch(html, -1)
	if len(refs) < 2 {
		t.Fatalf("index.html references no assets:\n%s", html)
	}
	h := handlerFor(t, files)
	for _, m := range refs {
		ref := m[1]
		if !strings.HasPrefix(ref, "/auth/") {
			t.Errorf("asset outside /auth/: %s", ref)
			continue
		}
		rec := get(t, h, "GET", ref)
		if rec.Code != 200 {
			t.Errorf("embedded asset %s: %d", ref, rec.Code)
		}
		body, _ := io.ReadAll(rec.Body)
		if len(body) == 0 {
			t.Errorf("embedded asset %s is empty", ref)
		}
	}
}
