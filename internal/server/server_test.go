package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
	"github.com/atikansari-ghr/BookmarksSync/internal/version"
)

type failingChecker struct{}

func (failingChecker) Ping(context.Context) error { return errors.New("db exploded") }

type okChecker struct{}

func (okChecker) Ping(context.Context) error { return nil }

func testInfo() version.Info {
	return version.Info{Version: "9.9.9", Commit: "deadbeef", BuildDate: "2026-01-02T03:04:05Z"}
}

func newHandler(t *testing.T, opts server.Options) http.Handler {
	t.Helper()
	if opts.Ready == nil {
		opts.Ready = okChecker{}
	}
	opts.Version = testInfo()
	return server.NewHandler(opts)
}

func do(h http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, body))
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json (body %q)", ct, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, rec.Body.String())
	}
	return m
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	m := decode(t, rec)
	e, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error object in %v", m)
	}
	if e["message"] == "" || e["message"] == nil {
		t.Errorf("error has no message: %v", e)
	}
	code, _ := e["code"].(string)
	return code
}

func TestHealthzIsAlwaysOK(t *testing.T) {
	h := newHandler(t, server.Options{Ready: failingChecker{}})
	rec := do(h, http.MethodGet, "/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if decode(t, rec)["status"] != "ok" {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestReadyzReflectsRealDatabase(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(ctx, filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(t, server.Options{Ready: db})

	if rec := do(h, http.MethodGet, "/readyz", nil); rec.Code != http.StatusOK || decode(t, rec)["status"] != "ready" {
		t.Fatalf("open DB: status=%d body=%s", rec.Code, rec.Body)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	rec := do(h, http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed DB: status = %d, want 503", rec.Code)
	}
	if got := decode(t, rec)["status"]; got != "unavailable" {
		t.Errorf("status field = %v", got)
	}
	if strings.Contains(rec.Body.String(), "closed") {
		t.Errorf("readyz must not leak internal error text: %s", rec.Body)
	}
}

func TestReadyzUnhealthyChecker(t *testing.T) {
	h := newHandler(t, server.Options{Ready: failingChecker{}})
	rec := do(h, http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "exploded") {
		t.Errorf("readyz leaked internal error: %s", rec.Body)
	}
}

func TestVersionEndpoint(t *testing.T) {
	h := newHandler(t, server.Options{})
	rec := do(h, http.MethodGet, "/api/v1/version", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	m := decode(t, rec)
	if m["version"] != "9.9.9" || m["commit"] != "deadbeef" || m["build_date"] != "2026-01-02T03:04:05Z" {
		t.Errorf("body = %v", m)
	}
}

func TestMethodMismatchReturnsJSON405WithAllow(t *testing.T) {
	h := newHandler(t, server.Options{})
	for _, path := range []string{"/api/v1/version", "/healthz", "/readyz"} {
		rec := do(h, http.MethodPost, path, strings.NewReader("{}"))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", path, rec.Code)
			continue
		}
		if code := errorCode(t, rec); code != "method_not_allowed" {
			t.Errorf("%s: code = %q", path, code)
		}
		if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") {
			t.Errorf("%s: Allow = %q", path, allow)
		}
	}
}

func TestUnknownAPIRouteReturnsJSON404(t *testing.T) {
	h := newHandler(t, server.Options{})
	for _, target := range []string{"/api/v1/nope", "/api/", "/api/v2/version", "/api/v1/version/extra"} {
		rec := do(h, http.MethodGet, target, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, rec.Code)
			continue
		}
		if code := errorCode(t, rec); code != "not_found" {
			t.Errorf("%s: code = %q", target, code)
		}
	}
	// Any method on an unknown API path is 404, not 405.
	if rec := do(h, http.MethodDelete, "/api/v1/nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE unknown: status = %d", rec.Code)
	}
}

func TestRegisteredRoutesGetAPIMiddlewareButVersionDoesNot(t *testing.T) {
	type ctxKey struct{}
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-Scope") == "" {
				server.WriteError(w, http.StatusUnauthorized, "unauthorized", "scope required")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, r.Header.Get("X-Test-Scope"))))
		})
	}
	h := newHandler(t, server.Options{
		APIMiddleware: mw,
		Register: func(r server.Registrar) {
			r.Handle("GET /api/v1/whoami", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				server.WriteJSON(w, http.StatusOK, map[string]any{"scope": req.Context().Value(ctxKey{})})
			}))
		},
	})

	if rec := do(h, http.MethodGet, "/api/v1/whoami", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no scope: status = %d, want 401", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	req.Header.Set("X-Test-Scope", "user-1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || decode(t, rec)["scope"] != "user-1" {
		t.Fatalf("with scope: status=%d body=%s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodGet, "/api/v1/version", nil); rec.Code != http.StatusOK {
		t.Errorf("version must stay public, status = %d", rec.Code)
	}
	// Registered routes participate in 405 detection.
	if rec := do(h, http.MethodPost, "/api/v1/whoami", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST whoami: status = %d, want 405", rec.Code)
	}
}

func TestRequestBodyIsLimited(t *testing.T) {
	var readErr error
	h := newHandler(t, server.Options{
		MaxBodyBytes: 16,
		Register: func(r server.Registrar) {
			r.Handle("POST /api/v1/echo", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				_, readErr = io.ReadAll(req.Body)
				if readErr != nil {
					server.WriteError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "body too large")
					return
				}
				server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
			}))
		},
	})
	if rec := do(h, http.MethodPost, "/api/v1/echo", strings.NewReader(strings.Repeat("a", 17))); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized: status = %d", rec.Code)
	}
	var mbe *http.MaxBytesError
	if !errors.As(readErr, &mbe) {
		t.Errorf("read error = %v, want *http.MaxBytesError", readErr)
	}
	if rec := do(h, http.MethodPost, "/api/v1/echo", strings.NewReader(strings.Repeat("a", 16))); rec.Code != http.StatusOK {
		t.Errorf("at-limit body: status = %d", rec.Code)
	}
}

func TestRouteSpecificBodyLimitOverridesDefault(t *testing.T) {
	var largeErr error
	h := newHandler(t, server.Options{
		MaxBodyBytes: 16,
		BodyLimit: func(r *http.Request) int64 {
			if r.Method == http.MethodPost && r.URL.Path == "/api/v1/large" {
				return 64
			}
			return 0
		},
		Register: func(r server.Registrar) {
			for _, path := range []string{"/api/v1/large", "/api/v1/small"} {
				p := path
				r.Handle("POST "+p, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					_, err := io.ReadAll(req.Body)
					if p == "/api/v1/large" {
						largeErr = err
					}
					if err != nil {
						server.WriteError(w, http.StatusRequestEntityTooLarge, "payload_too_large", "body too large")
						return
					}
					server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
				}))
			}
		},
	})
	if rec := do(h, http.MethodPost, "/api/v1/large", strings.NewReader(strings.Repeat("x", 32))); rec.Code != http.StatusOK {
		t.Fatalf("route-specific body: status=%d", rec.Code)
	}
	if largeErr != nil {
		t.Fatalf("route-specific body read: %v", largeErr)
	}
	if rec := do(h, http.MethodPost, "/api/v1/small", strings.NewReader(strings.Repeat("x", 32))); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("default body: status=%d, want 413", rec.Code)
	}
}

func TestPanicsBecomeJSON500(t *testing.T) {
	h := newHandler(t, server.Options{
		Register: func(r server.Registrar) {
			r.Handle("GET /api/v1/boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("kaboom") }))
		},
	})
	rec := do(h, http.MethodGet, "/api/v1/boom", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	if code := errorCode(t, rec); code != "internal_error" {
		t.Errorf("code = %q", code)
	}
	if strings.Contains(rec.Body.String(), "kaboom") {
		t.Errorf("panic value leaked: %s", rec.Body)
	}
}

func TestResponsesCarrySecurityHeaders(t *testing.T) {
	rec := do(newHandler(t, server.Options{}), http.MethodGet, "/healthz", nil)
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options: nosniff")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
}

func TestHTTPServerHasTimeoutsAndLimits(t *testing.T) {
	s := server.NewHTTPServer("127.0.0.1:8080", http.NotFoundHandler())
	if s.ReadHeaderTimeout <= 0 || s.ReadTimeout <= 0 || s.WriteTimeout <= 0 || s.IdleTimeout <= 0 {
		t.Errorf("timeouts not set: %+v", s)
	}
	if s.MaxHeaderBytes <= 0 || s.MaxHeaderBytes > 1<<20 {
		t.Errorf("MaxHeaderBytes = %d", s.MaxHeaderBytes)
	}
}

func TestServeShutsDownGracefullyAndFinishesInFlightRequests(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	h := newHandler(t, server.Options{
		Register: func(r server.Registrar) {
			r.Handle("GET /api/v1/slow", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				close(started)
				<-release
				server.WriteJSON(w, http.StatusOK, map[string]any{"done": true})
			}))
		},
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(ctx, ln, h, 5*time.Second) }()

	type result struct {
		body string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/api/v1/slow")
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		resCh <- result{body: string(b)}
	}()

	<-started
	cancel()
	select {
	case err := <-serveDone:
		t.Fatalf("Serve returned (%v) while a request was still in flight", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)

	r := <-resCh
	if r.err != nil || !strings.Contains(r.body, "done") {
		t.Fatalf("in-flight request: body=%q err=%v", r.body, r.err)
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Errorf("Serve returned %v, want nil after clean shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after shutdown")
	}
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), 500*time.Millisecond); err == nil {
		c.Close()
		t.Error("listener still accepting after shutdown")
	}
}

func TestServeReturnsListenerErrors(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close() // Serve on a dead listener must report, not hang.
	done := make(chan error, 1)
	go func() { done <- server.Serve(context.Background(), ln, http.NotFoundHandler(), time.Second) }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("want error from closed listener")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve hung on closed listener")
	}
}
