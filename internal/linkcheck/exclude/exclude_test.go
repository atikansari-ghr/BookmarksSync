package exclude_test

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck/exclude"
)

func TestValidAcceptsOnlyDomainsAddressesAndCIDRs(t *testing.T) {
	good := []string{
		`[]`,
		`["example.com"]`,
		`["Example.COM.", "sub.example.org", "10.0.0.0/8", "93.184.216.34", "2001:db8::/32", "::1"]`,
		`["xn--bcher-kva.example"]`,
	}
	for _, v := range good {
		if !exclude.Valid(v) {
			t.Errorf("Valid(%s) = false, want true", v)
		}
	}
	bad := []string{
		``, `null`, `{}`, `"example.com"`, `["example.com", 5]`, `[""]`, `[" example.com"]`,
		`["http://example.com"]`, `["example.com/path"]`, `["example.com:80"]`, `["user@example.com"]`,
		`["*.example.com"]`, `["*"]`, `["a b.example"]`, `["-bad.example"]`, `["bad-.example"]`,
		`["a..example"]`, `["10.0.0.0/33"]`, `["10.0.0.0/"]`, `["::1/129"]`, `["fe80::1%eth0"]`,
		`["` + strings.Repeat("a", 64) + `.example"]`,
		`["` + strings.Repeat("a.", 130) + `example"]`,
		`["example.com"] trailing`, `["example.com"]["x.example"]`,
		`["localhost"]x`,
	}
	for _, v := range bad {
		if exclude.Valid(v) {
			t.Errorf("Valid(%q) = true, want false", v)
		}
	}
	entries := make([]string, exclude.MaxEntries+1)
	for i := range entries {
		entries[i] = fmt.Sprintf(`"h%d.example"`, i)
	}
	if exclude.Valid("[" + strings.Join(entries, ",") + "]") {
		t.Errorf("more than %d entries accepted", exclude.MaxEntries)
	}
	if !exclude.Valid("[" + strings.Join(entries[:exclude.MaxEntries], ",") + "]") {
		t.Errorf("exactly %d entries rejected", exclude.MaxEntries)
	}
}

func TestSetMatchesExactDomainsSubdomainsAndCIDRsOnly(t *testing.T) {
	s, err := exclude.Parse(`["Example.com.", "10.0.0.0/8", "93.184.216.34", "2001:db8::/32"]`)
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]bool{
		"example.com": true, "EXAMPLE.com": true, "www.example.com": true, "a.b.example.com": true, "example.com.": true,
		"notexample.com": false, "example.com.evil.test": false, "com": false, "": false,
		"10.1.2.3": true, "11.0.0.1": false, "93.184.216.34": true, "93.184.216.35": false,
		"2001:db8::1": true, "2001:db9::1": false, "::ffff:10.1.2.3": true,
	} {
		if got := s.MatchHost(host); got != want {
			t.Errorf("MatchHost(%q) = %v, want %v", host, got, want)
		}
	}
	if !s.MatchAddr(netip.MustParseAddr("10.9.9.9")) || s.MatchAddr(netip.MustParseAddr("9.9.9.9")) {
		t.Error("MatchAddr mismatch")
	}
	var zero exclude.Set
	if zero.MatchHost("example.com") || zero.MatchAddr(netip.MustParseAddr("1.1.1.1")) {
		t.Error("zero Set must match nothing")
	}
	if _, err := exclude.Parse(`["bad entry"]`); err == nil {
		t.Error("Parse accepted an invalid entry")
	}
}
