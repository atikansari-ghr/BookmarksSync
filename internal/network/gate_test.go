package network

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestGateDeniesEveryPathWithSameResponseBeforeCallingHandlers(t *testing.T) {
	lookup := countryMap{"198.51.100.1": "US"}
	called := false
	gate := &Gate{Lookup: lookup, LoadPolicy: func(context.Context) (Policy, error) { return DefaultPolicy(), nil }}
	h := gate.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "secret"})
		io.WriteString(w, r.URL.Path)
	}))
	var body string
	for _, path := range []string{"/", "/api/v1/auth/login", "/ocs/v2.php/cloud/capabilities?format=json", "/healthz", "/readyz", "/api/v1/version", "/missing"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "198.51.100.1:4000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", path, rec.Code)
		}
		if rec.Header().Get("Set-Cookie") != "" {
			t.Errorf("%s: denied request set a cookie", path)
		}
		if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s: content type = %q", path, rec.Header().Get("Content-Type"))
		}
		if body == "" {
			body = rec.Body.String()
		} else if rec.Body.String() != body {
			t.Errorf("%s: denial body differs: %q != %q", path, rec.Body.String(), body)
		}
	}
	if called {
		t.Fatal("denied request reached a route handler")
	}
}

func TestGateUsesResolvedProxyAddressAndOnlyTrustedXFF(t *testing.T) {
	lookup := countryMap{"198.51.100.1": "SA", "198.51.100.2": "US"}
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("203.0.113.0/24")}
	gate := &Gate{TrustedProxies: trusted, Lookup: lookup, LoadPolicy: func(context.Context) (Policy, error) { return DefaultPolicy(), nil }}
	h := gate.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.RemoteAddr) }))
	for _, tc := range []struct {
		name, peer, forwarded, want string
		status                      int
	}{
		{"trusted chain", "10.0.0.5:80", "198.51.100.1, 203.0.113.8", "198.51.100.1:0", http.StatusOK},
		{"untrusted spoof", "198.51.100.2:80", "198.51.100.1", "", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.peer
			if tc.forwarded != "" {
				req.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body.String())
			}
			if tc.want != "" && strings.TrimSpace(rec.Body.String()) != tc.want {
				t.Fatalf("handler peer = %q, want %q", rec.Body.String(), tc.want)
			}
		})
	}
}

func TestGateRequiresExplicitManagementCIDRAndTrustedProxyChain(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	management := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.10.0/24")}
	gate := &Gate{TrustedProxies: trusted, ManagementCIDRs: management, LoadPolicy: func(context.Context) (Policy, error) { return DefaultPolicy(), nil }}
	h := gate.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		name, peer, xff string
		want            int
	}{
		{"explicit direct management", "192.168.10.12:1", "", http.StatusNoContent},
		{"private but not configured", "192.168.11.12:1", "", http.StatusForbidden},
		{"trusted proxy missing XFF", "10.0.0.9:1", "", http.StatusForbidden},
		{"all XFF entries are proxies", "10.0.0.9:1", "10.0.0.8", http.StatusForbidden},
		{"untrusted XFF cannot claim management", "198.51.100.9:1", "192.168.10.12", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.peer
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestGateFailsClosedOnUnknownCountryAndUnavailablePolicy(t *testing.T) {
	unknown := &Gate{LoadPolicy: func(context.Context) (Policy, error) { return DefaultPolicy(), nil }}
	unknownHandler := unknown.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unknown country reached handler") }))
	request := func(h http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "198.51.100.4:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(unknownHandler); rec.Code != http.StatusForbidden {
		t.Fatalf("unknown country status = %d", rec.Code)
	}
	for name, load := range map[string]func(context.Context) (Policy, error){"settings unavailable": func(context.Context) (Policy, error) { return Policy{}, errors.New("db down") }} {
		t.Run(name, func(t *testing.T) {
			gate := &Gate{LoadPolicy: load, Lookup: countryMap{"198.51.100.4": "SA"}}
			rec := request(gate.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unavailable policy reached handler") })))
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", rec.Code)
			}
		})
	}
}

func TestManagementCIDRRemainsAvailableWhenPolicyStoreFails(t *testing.T) {
	called := false
	gate := &Gate{ManagementCIDRs: []netip.Prefix{netip.MustParsePrefix("192.168.10.0/24")},
		LoadPolicy: func(context.Context) (Policy, error) { return Policy{}, errors.New("database unavailable") }}
	h := gate.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.10.12:443"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !called || rec.Code != http.StatusNoContent {
		t.Fatalf("management request = %d called=%v, want 204 and handler reached", rec.Code, called)
	}
}
