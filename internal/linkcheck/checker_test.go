package linkcheck_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck"
	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck/exclude"
)

const publicIP = "93.184.216.34"

// harness runs a real listener on loopback but lets the checker believe it is talking to
// public addresses: the injected resolver answers with public IPs and the injected dialer
// records the vetted address it was asked to dial before connecting to the local server.
type harness struct {
	t       *testing.T
	srv     *httptest.Server
	port    string
	mu      sync.Mutex
	answers map[string][][]netip.Addr // host -> successive answer sets; the last repeats
	calls   map[string]int
	dials   []string
}

func newHarness(t *testing.T, h http.Handler) *harness {
	t.Helper()
	hr := &harness{t: t, answers: map[string][][]netip.Addr{}, calls: map[string]int{}}
	hr.srv = httptest.NewServer(h)
	t.Cleanup(hr.srv.Close)
	_, hr.port, _ = net.SplitHostPort(hr.srv.Listener.Addr().String())
	return hr
}

func (h *harness) answer(host string, sets ...[]string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out [][]netip.Addr
	for _, set := range sets {
		var as []netip.Addr
		for _, a := range set {
			as = append(as, netip.MustParseAddr(a))
		}
		out = append(out, as)
	}
	h.answers[host] = out
}

func (h *harness) resolve(_ context.Context, host string) ([]netip.Addr, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls[host]++
	sets, ok := h.answers[host]
	if !ok {
		return nil, fmt.Errorf("no such host %s", host)
	}
	return sets[min(h.calls[host]-1, len(sets)-1)], nil
}

func (h *harness) resolves(host string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls[host]
}

func (h *harness) dial(ctx context.Context, network, address string) (net.Conn, error) {
	h.mu.Lock()
	h.dials = append(h.dials, address)
	h.mu.Unlock()
	var d net.Dialer
	return d.DialContext(ctx, network, h.srv.Listener.Addr().String())
}

func (h *harness) dialed() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.dials...)
}

func (h *harness) checker(cfg linkcheck.Config) *linkcheck.Checker {
	cfg.Resolve = h.resolve
	cfg.Dial = h.dial
	return linkcheck.NewChecker(cfg)
}

func (h *harness) url(host, path string) string { return "http://" + host + ":" + h.port + path }

func ok200() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hello") })
}

func portOf(r *http.Request) string {
	_, p, _ := net.SplitHostPort(r.Host)
	return p
}

// redirectingTo serves a 302 to a fixed Location for every request.
func redirectingTo(loc string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", loc)
		w.WriteHeader(http.StatusFound)
	})
}

func mustSet(t *testing.T, v string) exclude.Set {
	t.Helper()
	s, err := exclude.Parse(v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCheckerDefaultsMatchThePlan(t *testing.T) {
	if linkcheck.DefaultTimeout != 10*time.Second || linkcheck.DefaultMaxBody != 1<<20 || linkcheck.DefaultMaxRedirects != 5 {
		t.Fatalf("defaults = %v %d %d", linkcheck.DefaultTimeout, linkcheck.DefaultMaxBody, linkcheck.DefaultMaxRedirects)
	}
}

func TestCheckerReachesPublicHostByDialingOnlyTheVettedIP(t *testing.T) {
	h := newHarness(t, ok200())
	h.answer("public.test", []string{publicIP})
	r := h.checker(linkcheck.Config{}).Check(context.Background(), h.url("public.test", "/x"), exclude.Set{})
	if r.Status != linkcheck.StatusOK || r.HTTPStatus != 200 {
		t.Fatalf("result = %+v", r)
	}
	if d := h.dialed(); len(d) != 1 || d[0] != net.JoinHostPort(publicIP, h.port) {
		t.Fatalf("dialed %v, want only the vetted IP:port", d)
	}
}

func TestCheckerReportsBrokenAndNonHTTPSchemes(t *testing.T) {
	h := newHarness(t, http.NotFoundHandler())
	h.answer("public.test", []string{publicIP})
	c := h.checker(linkcheck.Config{})
	if r := c.Check(context.Background(), h.url("public.test", "/"), exclude.Set{}); r.Status != linkcheck.StatusBroken || r.HTTPStatus != 404 {
		t.Errorf("404 = %+v", r)
	}
	for _, u := range []string{"ftp://public.test/x", "javascript:alert(1)", "mailto:a@b.test", "file:///etc/passwd", "data:text/plain,x", "chrome://settings", "about:blank", "//public.test/x", "not a url", ""} {
		if r := c.Check(context.Background(), u, exclude.Set{}); r.Status != linkcheck.StatusSkipped {
			t.Errorf("%q = %+v, want skipped", u, r)
		}
	}
	if len(h.dialed()) != 1 {
		t.Errorf("skipped URLs must not touch the network: dials %v", h.dialed())
	}
}

func TestCheckerRejectsPrivateAndMappedAddresses(t *testing.T) {
	h := newHarness(t, ok200())
	c := h.checker(linkcheck.Config{})
	hosts := []string{
		"127.0.0.1", "127.1", "127.255.255.254", "10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "0.0.0.0", "0.1.2.3", "100.64.0.1", "100.127.255.255", "192.0.0.1", "192.0.2.1",
		"198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "239.255.255.255", "240.0.0.1", "255.255.255.255",
		"[::]", "[::1]", "[::ffff:127.0.0.1]", "[::ffff:10.0.0.1]", "[::ffff:169.254.169.254]", "[::ffff:7f00:1]",
		"[fe80::1]", "[fe80::1%25eth0]", "[fc00::1]", "[fd12:3456::1]", "[fec0::1]", "[ff02::1]",
		"[64:ff9b::7f00:1]", "[64:ff9b::a00:1]", "[2002:7f00:1::]", "[2001::1]", "[2001:db8::1]", "[100::1]",
		"2130706433", "0x7f.0.0.1", "0177.0.0.1", "0x7f000001", "017700000001", "1.2.3.0x4",
	}
	for _, host := range hosts {
		for _, scheme := range []string{"http", "https"} {
			r := c.Check(context.Background(), scheme+"://"+host+":"+h.port+"/", exclude.Set{})
			if r.Status != linkcheck.StatusBlocked {
				t.Errorf("%s://%s = %+v, want blocked", scheme, host, r)
			}
		}
	}
	// Names that resolve to non-public addresses are refused as well.
	for name, addr := range map[string]string{
		"localhost": "127.0.0.1", "intranet.test": "10.1.2.3", "meta.test": "169.254.169.254",
		"mapped.test": "::ffff:192.168.0.1", "v6.test": "fd00::5", "nat64.test": "64:ff9b::808:808",
	} {
		h.answer(name, []string{addr})
		if r := c.Check(context.Background(), h.url(name, "/"), exclude.Set{}); r.Status != linkcheck.StatusBlocked {
			t.Errorf("%s -> %s = %+v, want blocked", name, addr, r)
		}
	}
	if r := c.Check(context.Background(), "http://user:pw@public.test/", exclude.Set{}); r.Status != linkcheck.StatusBlocked {
		t.Errorf("userinfo = %+v, want blocked", r)
	}
	if d := h.dialed(); len(d) != 0 {
		t.Fatalf("blocked targets must never be dialed, dialed %v", d)
	}
	// A genuinely public literal is allowed through.
	if r := c.Check(context.Background(), h.url(publicIP, "/"), exclude.Set{}); r.Status != linkcheck.StatusOK {
		t.Errorf("public literal = %+v", r)
	}
}

func TestPublicAddr(t *testing.T) {
	for a, want := range map[string]bool{
		"93.184.216.34": true, "8.8.8.8": true, "1.1.1.1": true, "2606:4700::1111": true, "2a00:1450:4001::1": true,
		"::ffff:8.8.8.8": false, "127.0.0.1": false, "::ffff:127.0.0.1": false, "10.0.0.1": false, "::1": false,
	} {
		if got := linkcheck.PublicAddr(netip.MustParseAddr(a)); got != want {
			t.Errorf("PublicAddr(%s) = %v, want %v", a, got, want)
		}
	}
	if linkcheck.PublicAddr(netip.Addr{}) {
		t.Error("the zero address must not be public")
	}
}

func TestCheckerNeverDialsAnIPExcludedFromMixedPublicAnswers(t *testing.T) {
	h := newHarness(t, ok200())
	h.answer("mixed-public.test", []string{"93.184.216.34", "8.8.8.8"})
	c := h.checker(linkcheck.Config{})
	r := c.Check(context.Background(), h.url("mixed-public.test", "/"), mustSet(t, `["93.184.216.34/32"]`))
	if r.Status != linkcheck.StatusOK {
		t.Fatalf("remaining public address result = %+v, want ok", r)
	}
	if got := h.dialed(); len(got) != 1 || got[0] != net.JoinHostPort("8.8.8.8", h.port) {
		t.Fatalf("dialed %v, want only the non-excluded address", got)
	}

	h.mu.Lock()
	h.dials = nil
	h.mu.Unlock()
	r = c.Check(context.Background(), h.url("mixed-public.test", "/"), mustSet(t, `["93.184.216.34/32", "8.8.8.8/32"]`))
	if r.Status != linkcheck.StatusExcluded || len(h.dialed()) != 0 {
		t.Fatalf("all excluded answers: result %+v, dials %v; want excluded without a dial", r, h.dialed())
	}
}

func TestCheckerRejectsDNSRebinding(t *testing.T) {
	// Any non-public answer in the set condemns the name, whatever its position.
	h := newHarness(t, ok200())
	c := h.checker(linkcheck.Config{})
	h.answer("mixed.test", []string{publicIP, "10.0.0.7"})
	h.answer("mixed2.test", []string{"127.0.0.1", publicIP})
	for _, n := range []string{"mixed.test", "mixed2.test"} {
		if r := c.Check(context.Background(), h.url(n, "/"), exclude.Set{}); r.Status != linkcheck.StatusBlocked {
			t.Errorf("%s = %+v, want blocked", n, r)
		}
	}
	if len(h.dialed()) != 0 {
		t.Fatalf("dialed %v", h.dialed())
	}

	// A name that turns private between hops is caught on the redirect hop: the second
	// resolution of the same host is validated again, not cached from the first.
	h2 := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "http://rebind.test:"+portOf(r)+"/next", http.StatusFound)
			return
		}
		fmt.Fprint(w, "next")
	}))
	h2.answer("rebind.test", []string{publicIP}, []string{"127.0.0.1"})
	r := h2.checker(linkcheck.Config{}).Check(context.Background(), h2.url("rebind.test", "/start"), exclude.Set{})
	if r.Status != linkcheck.StatusBlocked {
		t.Fatalf("rebinding result = %+v, want blocked", r)
	}
	if n := h2.resolves("rebind.test"); n != 2 {
		t.Errorf("resolutions = %d, want 2 (every hop re-resolves)", n)
	}
	if d := h2.dialed(); len(d) != 1 || !strings.HasPrefix(d[0], publicIP+":") {
		t.Errorf("dials = %v, want exactly the first vetted public dial", d)
	}

	// Resolver errors are failures of the check, never a fallback to the system resolver.
	if r := c.Check(context.Background(), h.url("unknown.test", "/"), exclude.Set{}); r.Status != linkcheck.StatusError {
		t.Errorf("unresolvable = %+v, want error", r)
	}
}

func TestCheckerDialsOnlyIPLiteralsFromTheVettedSet(t *testing.T) {
	h := newHarness(t, ok200())
	h.answer("multi.test", []string{publicIP, "8.8.8.8", "2606:4700::1111"})
	r := h.checker(linkcheck.Config{}).Check(context.Background(), h.url("multi.test", "/"), exclude.Set{})
	if r.Status != linkcheck.StatusOK {
		t.Fatalf("result = %+v", r)
	}
	for _, d := range h.dialed() {
		host, _, err := net.SplitHostPort(d)
		a, perr := netip.ParseAddr(host)
		if err != nil || perr != nil || !linkcheck.PublicAddr(a) {
			t.Errorf("dialed %q: not a vetted public IP literal", d)
		}
	}
}

func TestCheckerKeepsTLSHostnameWhileDialingVettedIP(t *testing.T) {
	srv := httptest.NewTLSServer(ok200())
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	var dialed []string
	var mu sync.Mutex
	c := linkcheck.NewChecker(linkcheck.Config{
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(publicIP)}, nil
		},
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, address)
			mu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Listener.Addr().String())
		},
		TLSConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	})
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	// The httptest certificate is valid for example.com, not for the IP being dialed, so
	// success proves verification used the URL hostname.
	if r := c.Check(context.Background(), "https://example.com:"+port+"/", exclude.Set{}); r.Status != linkcheck.StatusOK {
		t.Fatalf("https result = %+v", r)
	}
	if r := c.Check(context.Background(), "https://wrong-name.test:"+port+"/", exclude.Set{}); r.Status != linkcheck.StatusError {
		t.Fatalf("hostname mismatch = %+v, want error", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) == 0 {
		t.Fatal("nothing dialed")
	}
	for _, d := range dialed {
		if !strings.HasPrefix(d, publicIP+":") {
			t.Errorf("dialed %q, want the vetted IP", d)
		}
	}
}

func TestCheckerIgnoresEnvironmentProxies(t *testing.T) {
	proxyHit := make(chan struct{}, 4)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHit <- struct{}{}
		fmt.Fprint(w, "from proxy")
	}))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	h := newHarness(t, ok200())
	h.answer("public.test", []string{publicIP})
	if r := h.checker(linkcheck.Config{}).Check(context.Background(), h.url("public.test", "/"), exclude.Set{}); r.Status != linkcheck.StatusOK {
		t.Fatalf("result = %+v", r)
	}
	select {
	case <-proxyHit:
		t.Fatal("request went through the environment proxy")
	default:
	}
	if len(h.dialed()) != 1 {
		t.Fatalf("dials = %v", h.dialed())
	}
}

func TestCheckerRevalidatesRedirectTargets(t *testing.T) {
	for _, loc := range []string{
		"http://127.0.0.1:%s/secret", "http://[::1]:%s/", "http://[::ffff:127.0.0.1]:%s/",
		"http://169.254.169.254/latest/meta-data/", "http://evil.test:%s/", "http://2130706433/", "http://localhost./",
		"http://user:pw@public.test:%s/",
		// non-HTTP(S) targets are refused, not followed
		"ftp://public.test/x", "file:///etc/passwd", "gopher://public.test/",
	} {
		var h *harness
		h = newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			redirectingTo(strings.ReplaceAll(loc, "%s", h.port)).ServeHTTP(w, r)
		}))
		h.answer("public.test", []string{publicIP})
		h.answer("evil.test", []string{"10.9.8.7"})
		h.answer("localhost", []string{"127.0.0.1"})
		h.answer("localhost.", []string{"127.0.0.1"})
		r := h.checker(linkcheck.Config{}).Check(context.Background(), h.url("public.test", "/"), exclude.Set{})
		if r.Status != linkcheck.StatusBlocked {
			t.Errorf("redirect to %s = %+v, want blocked", loc, r)
		}
		if d := h.dialed(); len(d) != 1 || !strings.HasPrefix(d[0], publicIP+":") {
			t.Errorf("redirect to %s dialed %v, want only the first public hop", loc, d)
		}
	}

	h := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/hop/"):
			var n int
			fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/hop/"), "%d", &n)
			if n == 0 {
				fmt.Fprint(w, "done")
				return
			}
			http.Redirect(w, r, fmt.Sprintf("/hop/%d", n-1), http.StatusMovedPermanently)
		case r.URL.Path == "/relative":
			http.Redirect(w, r, "/final", http.StatusSeeOther)
		default:
			fmt.Fprint(w, "final")
		}
	}))
	h.answer("public.test", []string{publicIP})
	c := h.checker(linkcheck.Config{})
	// A relative redirect to a public place is followed, and re-resolved.
	if r := c.Check(context.Background(), h.url("public.test", "/relative"), exclude.Set{}); r.Status != linkcheck.StatusOK || r.Redirects != 1 {
		t.Fatalf("relative redirect = %+v", r)
	}
	if n := h.resolves("public.test"); n != 2 {
		t.Errorf("resolutions = %d, want 2", n)
	}
	// Exactly five redirects are allowed; the sixth is an error.
	if r := c.Check(context.Background(), h.url("public.test", "/hop/5"), exclude.Set{}); r.Status != linkcheck.StatusOK || r.Redirects != 5 {
		t.Errorf("5 redirects = %+v, want ok", r)
	}
	if r := c.Check(context.Background(), h.url("public.test", "/hop/6"), exclude.Set{}); r.Status != linkcheck.StatusError || r.Detail != "too_many_redirects" {
		t.Errorf("6 redirects = %+v, want too_many_redirects", r)
	}
}

func TestCheckerBoundsTimeAndResponseSize(t *testing.T) {
	release := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hang":
			select {
			case <-release:
			case <-r.Context().Done():
			}
		case "/drip":
			w.WriteHeader(200)
			for i := 0; i < 100; i++ {
				fmt.Fprint(w, "x")
				w.(http.Flusher).Flush()
				select {
				case <-time.After(30 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
			}
		case "/big":
			w.Header().Set("Content-Length", fmt.Sprint(8<<20))
			buf := make([]byte, 64<<10)
			for i := 0; i < 128; i++ {
				if _, err := w.Write(buf); err != nil {
					return
				}
			}
		}
	})
	h := newHarness(t, handler)
	t.Cleanup(func() { close(release) })
	h.answer("public.test", []string{publicIP})

	c := h.checker(linkcheck.Config{Timeout: 200 * time.Millisecond})
	for _, p := range []string{"/hang", "/drip"} {
		start := time.Now()
		r := c.Check(context.Background(), h.url("public.test", p), exclude.Set{})
		if r.Status != linkcheck.StatusError || r.Detail != "timeout" {
			t.Errorf("%s = %+v, want timeout", p, r)
		}
		if el := time.Since(start); el > 3*time.Second {
			t.Errorf("%s took %v despite the 200ms bound", p, el)
		}
	}

	r := h.checker(linkcheck.Config{MaxBody: 4096}).Check(context.Background(), h.url("public.test", "/big"), exclude.Set{})
	if r.Status != linkcheck.StatusOK || !r.Truncated || r.BodyBytes != 4096 {
		t.Errorf("/big = %+v, want ok, truncated, exactly 4096 bytes read", r)
	}
	// The default bound is 1 MiB.
	r = h.checker(linkcheck.Config{}).Check(context.Background(), h.url("public.test", "/big"), exclude.Set{})
	if r.Status != linkcheck.StatusOK || !r.Truncated || r.BodyBytes != 1<<20 {
		t.Errorf("/big default = %+v, want exactly 1 MiB read", r)
	}
	// The caller's context also stops a check.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if r := h.checker(linkcheck.Config{}).Check(ctx, h.url("public.test", "/hang"), exclude.Set{}); r.Status != linkcheck.StatusError {
		t.Errorf("cancelled = %+v", r)
	}
}

func TestExclusionsOnlyNarrowChecks(t *testing.T) {
	h := newHarness(t, ok200())
	for _, n := range []string{"public.test", "www.skip.test", "skip.test", "notskip.test"} {
		h.answer(n, []string{publicIP})
	}
	h.answer("evil.test", []string{"10.0.0.5"})
	h.answer("evil-mixed.test", []string{"10.0.0.5", publicIP})
	c := h.checker(linkcheck.Config{})

	// Excluded domains and subdomains are skipped without any network access.
	for _, host := range []string{"skip.test", "www.skip.test", "SKIP.test"} {
		if r := c.Check(context.Background(), h.url(host, "/"), mustSet(t, `["skip.test"]`)); r.Status != linkcheck.StatusExcluded {
			t.Errorf("%s = %+v, want excluded", host, r)
		}
	}
	if len(h.dialed()) != 0 || h.resolves("skip.test") != 0 || h.resolves("www.skip.test") != 0 {
		t.Fatalf("excluded domains touched the network: dials %v", h.dialed())
	}
	// A non-matching host is checked normally.
	if r := c.Check(context.Background(), h.url("notskip.test", "/"), mustSet(t, `["skip.test"]`)); r.Status != linkcheck.StatusOK {
		t.Errorf("notskip.test = %+v, want ok", r)
	}
	// A CIDR skips targets that resolve entirely inside it, and literals inside it.
	h.mu.Lock()
	h.dials = nil
	h.mu.Unlock()
	cidr := mustSet(t, `["93.184.216.0/24"]`)
	if r := c.Check(context.Background(), h.url("public.test", "/"), cidr); r.Status != linkcheck.StatusExcluded {
		t.Errorf("CIDR match = %+v, want excluded", r)
	}
	if r := c.Check(context.Background(), h.url(publicIP, "/"), cidr); r.Status != linkcheck.StatusExcluded {
		t.Errorf("literal CIDR match = %+v, want excluded", r)
	}
	if len(h.dialed()) != 0 {
		t.Fatalf("CIDR-excluded targets were dialed: %v", h.dialed())
	}

	// Exclusions can never allow a private destination: unrelated exclusions leave the
	// public-only rule fully in force, and nothing excluded ever reports ok.
	other := mustSet(t, `["example.org", "8.8.8.0/24"]`)
	for _, u := range []string{"http://127.0.0.1:" + h.port + "/", "http://[::1]:" + h.port + "/", "http://[::ffff:10.0.0.1]/"} {
		if r := c.Check(context.Background(), u, other); r.Status != linkcheck.StatusBlocked {
			t.Errorf("%s with unrelated exclusions = %+v, want blocked", u, r)
		}
	}
	if r := c.Check(context.Background(), h.url("evil.test", "/"), other); r.Status != linkcheck.StatusBlocked {
		t.Errorf("evil.test = %+v, want blocked", r)
	}
	// Excluding a private range skips the target; it is neither checked nor reported ok.
	if r := c.Check(context.Background(), h.url("evil.test", "/"), mustSet(t, `["10.0.0.0/8"]`)); r.Status != linkcheck.StatusExcluded {
		t.Errorf("excluded private target = %+v, want excluded", r)
	}
	// A mixed answer that is only partly excluded still fails the public-only rule.
	if r := c.Check(context.Background(), h.url("evil-mixed.test", "/"), mustSet(t, `["10.0.0.0/8"]`)); r.Status != linkcheck.StatusBlocked {
		t.Errorf("partly excluded answers = %+v, want blocked", r)
	}
	if len(h.dialed()) != 0 {
		t.Fatalf("dials = %v, want none for private or excluded targets", h.dialed())
	}
	// Redirect targets are matched against exclusions too (skip only), after validation.
	hh := newHarness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectingTo("http://skip.test:"+portOf(r)+"/").ServeHTTP(w, r)
	}))
	hh.answer("public.test", []string{publicIP})
	hh.answer("skip.test", []string{publicIP})
	if r := hh.checker(linkcheck.Config{}).Check(context.Background(), hh.url("public.test", "/"), mustSet(t, `["skip.test"]`)); r.Status != linkcheck.StatusExcluded {
		t.Errorf("redirect to excluded host = %+v, want excluded", r)
	}
}
