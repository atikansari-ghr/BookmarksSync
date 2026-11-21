// Package linkcheck checks whether stored bookmark URLs still resolve, without ever
// letting a stored URL steer the server at a private network.
//
// Security model (SSRF): only http and https are contacted. For every hop, including every
// redirect, the host is resolved afresh, EVERY answer must be a public address, and the
// connection is then dialed to a vetted IP literal directly; the URL hostname is kept for
// TLS verification and the Host header. Environment proxies are never used, redirects are
// followed manually (at most five), and each URL gets one 10 second budget and a 1 MiB
// body limit. User exclusions can only turn a check into a skip.
package linkcheck

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck/exclude"
)

// Defaults of the per-URL bounds.
const (
	DefaultTimeout      = 10 * time.Second
	DefaultMaxBody      = int64(1 << 20)
	DefaultMaxRedirects = 5
	maxDialAttempts     = 4
	userAgent           = "BookmarksSync-LinkCheck/1"
)

// Result statuses. Detail carries a short machine-readable reason, never a URL.
const (
	StatusOK       = "ok"       // final response below 400
	StatusBroken   = "broken"   // final response 400 or above
	StatusSkipped  = "skipped"  // not an http(s) URL
	StatusExcluded = "excluded" // matched a user exclusion; not contacted
	StatusBlocked  = "blocked"  // destination refused by the public-only rules
	StatusError    = "error"    // DNS, connection, TLS, timeout or redirect-limit failure
)

// Result is the outcome of checking one URL.
type Result struct {
	Status     string
	HTTPStatus int
	Detail     string
	Redirects  int
	BodyBytes  int64
	Truncated  bool
}

// Config tunes a Checker. Zero values select the production defaults; Resolve and Dial are
// injectable so tests (and rebinding defenses) never depend on real DNS or networks.
type Config struct {
	// Resolve returns every address a host name resolves to.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// Dial connects to an address that is always a vetted IP literal "ip:port".
	Dial         func(ctx context.Context, network, address string) (net.Conn, error)
	TLSConfig    *tls.Config
	Timeout      time.Duration
	MaxBody      int64
	MaxRedirects int
}

// Checker performs public-only URL checks. It is safe for concurrent use.
type Checker struct {
	resolve      func(context.Context, string) ([]netip.Addr, error)
	dial         func(context.Context, string, string) (net.Conn, error)
	tlsCfg       *tls.Config
	timeout      time.Duration
	maxBody      int64
	maxRedirects int
}

// NewChecker builds a Checker from cfg.
func NewChecker(cfg Config) *Checker {
	c := &Checker{resolve: cfg.Resolve, dial: cfg.Dial, tlsCfg: cfg.TLSConfig, timeout: cfg.Timeout, maxBody: cfg.MaxBody, maxRedirects: cfg.MaxRedirects}
	if c.resolve == nil {
		c.resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if c.dial == nil {
		d := &net.Dialer{Timeout: DefaultTimeout}
		c.dial = d.DialContext
	}
	if c.timeout <= 0 {
		c.timeout = DefaultTimeout
	}
	if c.maxBody <= 0 {
		c.maxBody = DefaultMaxBody
	}
	if c.maxRedirects <= 0 {
		c.maxRedirects = DefaultMaxRedirects
	}
	return c
}

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		// IPv4
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		// IPv6 (IPv4-mapped addresses are unmapped and judged as IPv4)
		"::/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16",
		"3fff::/20", "fc00::/7", "fe80::/10", "fec0::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// PublicAddr reports whether a is a globally routable unicast address that a link check
// may contact. IPv4-mapped IPv6 addresses are judged as the IPv4 address they embed;
// zoned, unspecified, loopback, private, link-local, multicast, shared, documentation,
// benchmarking, reserved and translation/tunnel ranges are refused.
func PublicAddr(a netip.Addr) bool {
	if !a.IsValid() || a.Zone() != "" {
		return false
	}
	// Mapped IPv6 is a distinct representation that has caused filter bypasses in
	// network stacks. Reject it outright rather than accepting even a public v4 value.
	if a.Is4In6() {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// Check checks rawURL under the exclusions ex. It always returns a Result; failures are
// reported in it, never by panicking or by contacting a non-public destination.
func (c *Checker) Check(ctx context.Context, rawURL string, ex exclude.Set) Result {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	cur := rawURL
	redirects := 0
	for hop := 0; ; hop++ {
		res, next := c.hop(ctx, cur, hop == 0, ex)
		res.Redirects = redirects
		if next == "" {
			return res
		}
		if redirects >= c.maxRedirects {
			return Result{Status: StatusError, Detail: "too_many_redirects", Redirects: redirects}
		}
		redirects++
		cur = next
	}
}

// hop validates and contacts one URL. It returns either a final result (next == "") or the
// absolute redirect target to validate and contact as the next hop.
func (c *Checker) hop(ctx context.Context, raw string, first bool, ex exclude.Set) (Result, string) {
	reject := func(skipDetail, blockDetail string) (Result, string) {
		if first {
			return Result{Status: StatusSkipped, Detail: skipDetail}, ""
		}
		return Result{Status: StatusBlocked, Detail: blockDetail}, ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return reject("invalid_url", "invalid_redirect")
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Opaque != "" || u.Hostname() == "" {
		return reject("unsupported_scheme", "unsupported_redirect")
	}
	if u.User != nil {
		return Result{Status: StatusBlocked, Detail: "userinfo"}, ""
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	literal, parseErr := netip.ParseAddr(host)
	if parseErr != nil && numericHost(host) {
		return Result{Status: StatusBlocked, Detail: "invalid_host"}, ""
	}

	var addrs []netip.Addr
	if parseErr == nil { // an IP literal: no DNS involved
		if ex.MatchAddr(literal) {
			return Result{Status: StatusExcluded}, ""
		}
		addrs = []netip.Addr{literal}
	} else {
		if ex.MatchHost(host) {
			return Result{Status: StatusExcluded}, ""
		}
		var err error
		addrs, err = c.resolve(ctx, host)
		if err != nil {
			return failure(ctx, "dns_failure"), ""
		}
		if len(addrs) == 0 {
			return Result{Status: StatusError, Detail: "dns_no_answer"}, ""
		}
		if allExcluded(ex, addrs) {
			return Result{Status: StatusExcluded}, ""
		}
	}
	// A DNS name can have multiple public answers. Exclusions must prevent dialing
	// matching IPs even when other answers remain eligible; public validation runs
	// first so an exclusion can never make a mixed public/private answer acceptable.
	eligible := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		if !PublicAddr(a) {
			return Result{Status: StatusBlocked, Detail: "non_public_address"}, ""
		}
		if !ex.MatchAddr(a) {
			eligible = append(eligible, a)
		}
	}
	if len(eligible) == 0 {
		return Result{Status: StatusExcluded}, ""
	}
	return c.fetch(ctx, u, eligible)
}

func allExcluded(ex exclude.Set, addrs []netip.Addr) bool {
	for _, a := range addrs {
		if !ex.MatchAddr(a) {
			return false
		}
	}
	return len(addrs) > 0
}

// numericHost reports hosts whose last label starts with a digit but that are not valid IP
// literals: legacy forms such as 127.1, 2130706433 or 0x7f.0.0.1 that some resolvers and
// browsers interpret as addresses. They are refused rather than guessed at.
func numericHost(host string) bool {
	last := host
	if i := strings.LastIndexByte(host, '.'); i >= 0 {
		last = host[i+1:]
	}
	return last != "" && last[0] >= '0' && last[0] <= '9'
}

func failure(ctx context.Context, detail string) Result {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{Status: StatusError, Detail: "timeout"}
	}
	if ctx.Err() != nil {
		return Result{Status: StatusError, Detail: "canceled"}
	}
	return Result{Status: StatusError, Detail: detail}
}

// fetch issues one GET to the vetted addresses and either returns the final result or the
// absolute redirect target.
func (c *Checker) fetch(ctx context.Context, u *url.URL, addrs []netip.Addr) (Result, string) {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[strings.ToLower(u.Scheme)]
	}
	var tlsCfg *tls.Config
	if c.tlsCfg != nil {
		tlsCfg = c.tlsCfg.Clone()
	}
	tr := &http.Transport{
		// Never the environment's proxy, and never the transport's own dialer: the only
		// connection made is to a vetted IP literal, whatever address the transport asks for.
		Proxy: nil,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var lastErr error
			for i, a := range addrs {
				if i >= maxDialAttempts {
					break
				}
				conn, err := c.dial(ctx, "tcp", net.JoinHostPort(a.Unmap().String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = errors.New("no address")
			}
			return nil, lastErr
		},
		TLSClientConfig:        tlsCfg, // ServerName stays the URL hostname
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 64 << 10,
		TLSHandshakeTimeout:    c.timeout,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Result{Status: StatusSkipped, Detail: "invalid_url"}, ""
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")
	resp, err := client.Do(req)
	if err != nil {
		return failure(ctx, "connect_failed"), ""
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		if loc := resp.Header.Get("Location"); loc != "" {
			target, err := u.Parse(loc)
			if err != nil {
				return Result{Status: StatusBlocked, Detail: "invalid_redirect", HTTPStatus: resp.StatusCode}, ""
			}
			return Result{HTTPStatus: resp.StatusCode}, target.String()
		}
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, c.maxBody+1))
	res := Result{HTTPStatus: resp.StatusCode, BodyBytes: n}
	if n > c.maxBody {
		res.BodyBytes, res.Truncated = c.maxBody, true
	} else if err != nil {
		return failure(ctx, "body_failed"), ""
	}
	if resp.StatusCode >= 400 {
		res.Status = StatusBroken
	} else {
		res.Status = StatusOK
	}
	return res, ""
}
