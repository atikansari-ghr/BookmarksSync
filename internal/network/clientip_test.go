package network

import (
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCIDRsAcceptsIPv4AndIPv6AndRejectsBadEntries(t *testing.T) {
	got, err := ParseCIDRs(" 192.0.2.8/32, 2001:db8::/32 ")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != netip.MustParsePrefix("192.0.2.8/32") || got[1] != netip.MustParsePrefix("2001:db8::/32") {
		t.Fatalf("ParseCIDRs = %v, want IPv4 /32 and IPv6 /32 prefixes", got)
	}
	if got, err := ParseCIDRs("2001:db8::1/128"); err != nil || len(got) != 1 || got[0] != netip.MustParsePrefix("2001:db8::1/128") {
		t.Fatalf("IPv6 host CIDR = %v, %v", got, err)
	}
	for _, raw := range []string{"192.0.2.1", "192.0.2.0/99", "10.0.0.0/8,,2001:db8::/32", "not-a-cidr"} {
		if _, err := ParseCIDRs(raw); err == nil {
			t.Errorf("ParseCIDRs(%q) unexpectedly succeeded", raw)
		}
	}
	if got, err := ParseCIDRs(""); err != nil || len(got) != 0 {
		t.Fatalf("empty ParseCIDRs = %v, %v; want empty list", got, err)
	}
}

func TestParseCIDRsNormalizesIPv4MappedIPv6Prefixes(t *testing.T) {
	got, err := ParseCIDRs("::ffff:10.0.0.9/128,::ffff:192.0.2.0/120")
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.9/32"), netip.MustParsePrefix("192.0.2.0/24")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("mapped CIDRs = %v, want %v", got, want)
	}
	if _, err := ParseCIDRs("::ffff:0:0/80"); err == nil {
		t.Fatal("mapped IPv6 prefix shorter than the mapped address width was accepted")
	}
}

func TestNormalizeCountryCodesRequiresOneTwoLetterItem(t *testing.T) {
	got, err := NormalizeCountryCodes([]string{"in", "SA"})
	if err != nil || strings.Join(got, ",") != "IN,SA" {
		t.Fatalf("NormalizeCountryCodes = %v, %v", got, err)
	}
	for _, input := range [][]string{{""}, {"IN,SA"}, {" in"}, {"éN"}} {
		if _, err := NormalizeCountryCodes(input); err == nil {
			t.Errorf("NormalizeCountryCodes(%q) succeeded", input)
		}
	}
	if got, err := NormalizeCountryCodes([]string{}); err != nil || len(got) != 0 {
		t.Fatalf("empty NormalizeCountryCodes = %v, %v", got, err)
	}
}

func TestResolveClientIPUsesOnlyConfiguredProxyXFFChain(t *testing.T) {
	trusted, err := ParseCIDRs("10.0.0.0/8,203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", "http://bookmarks.local/", nil)
	req.RemoteAddr = "10.0.0.10:8080"
	req.Header.Add("X-Forwarded-For", "198.51.100.7")
	req.Header.Add("X-Forwarded-For", "203.0.113.8, 10.0.0.9")
	req.Header.Set("CF-Connecting-IP", "192.0.2.99")
	got, err := ResolveClientIP(req, trusted)
	if err != nil || got != netip.MustParseAddr("198.51.100.7") {
		t.Fatalf("ResolveClientIP = %v, %v; want 198.51.100.7", got, err)
	}
}

func TestResolveClientIPIgnoresSpoofedHeadersFromUntrustedPeer(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://bookmarks.local/", nil)
	req.RemoteAddr = "198.51.100.8:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("Forwarded", "for=203.0.113.10")
	req.Header.Set("X-Real-IP", "203.0.113.11")
	req.Header.Set("CF-Connecting-IP", "203.0.113.12")
	got, err := ResolveClientIP(req, nil)
	if err != nil || got != netip.MustParseAddr("198.51.100.8") {
		t.Fatalf("ResolveClientIP = %v, %v; want direct peer 198.51.100.8", got, err)
	}
}

func TestResolveClientIPRejectsTrustedPeerWithoutXFFDespiteOtherIPHeaders(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://bookmarks.local/", nil)
	req.RemoteAddr = "10.0.0.9:8080"
	req.Header.Set("Forwarded", "for=198.51.100.1")
	req.Header.Set("X-Real-IP", "198.51.100.2")
	req.Header.Set("CF-Connecting-IP", "198.51.100.3")
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	if _, err := ResolveClientIP(req, trusted); err == nil {
		t.Fatal("ignored headers were accepted as a trusted proxy address")
	}
}

func TestResolveClientIPUnmapsIPv4MappedIPv6AndCapsForwardedHops(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://bookmarks.local/", nil)
	req.RemoteAddr = "[::ffff:198.51.100.8]:1234"
	got, err := ResolveClientIP(req, nil)
	if err != nil || got != netip.MustParseAddr("198.51.100.8") {
		t.Fatalf("mapped peer = %v, %v; want 198.51.100.8", got, err)
	}
	parts := make([]string, maxForwardedHops+1)
	for i := range parts {
		parts[i] = "198.51.100.1"
	}
	req.RemoteAddr = "10.0.0.9:8080"
	req.Header.Set("X-Forwarded-For", strings.Join(parts, ","))
	if _, err := ResolveClientIP(req, []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}); err == nil {
		t.Fatal("overlong forwarded chain was accepted")
	}
}

func TestResolveClientIPRejectsUnparseableTrustedChains(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	for name, chain := range map[string]string{"missing": "", "empty token": "198.51.100.1,,10.0.0.1", "unknown": "unknown", "port": "198.51.100.1:443", "zone": "fe80::1%eth0", "all proxies": "10.0.0.1"} {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "http://bookmarks.local/", nil)
			req.RemoteAddr = "10.0.0.9:8080"
			if chain != "" {
				req.Header.Set("X-Forwarded-For", chain)
			}
			if _, err := ResolveClientIP(req, trusted); err == nil {
				t.Fatalf("ResolveClientIP accepted chain %q", chain)
			}
		})
	}
}

type countryMap map[string]string

func (c countryMap) LookupCountry(ip netip.Addr) (string, error) { return c[ip.String()], nil }

func TestCountryPolicyDefaultsAndExplicitManagementCIDRs(t *testing.T) {
	policy := DefaultPolicy()
	if policy.Mode != "allow" || strings.Join(policy.Countries, ",") != "IN,SA" {
		t.Fatalf("DefaultPolicy = %#v, want allow IN,SA", policy)
	}
	lookup := countryMap{"198.51.100.1": "SA", "198.51.100.2": "US", "198.51.100.3": ""}
	management, err := ParseCIDRs("192.168.10.0/24")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		ip   string
		want bool
	}{{"198.51.100.1", true}, {"198.51.100.2", false}, {"198.51.100.3", false}, {"192.168.10.25", true}, {"192.168.11.25", false}, {"127.0.0.1", false}} {
		got, err := CountryAllowed(netip.MustParseAddr(tc.ip), policy, management, lookup)
		if err != nil || got != tc.want {
			t.Errorf("CountryAllowed(%s) = %v, %v; want %v", tc.ip, got, err, tc.want)
		}
	}
}

func TestCountryPolicyBlockModeOnlyAllowsKnownNonBlockedCountries(t *testing.T) {
	policy := Policy{Mode: "block", Countries: []string{"US"}}
	lookup := countryMap{"198.51.100.1": "SA", "198.51.100.2": "US", "198.51.100.3": ""}
	for ip, want := range map[string]bool{"198.51.100.1": true, "198.51.100.2": false, "198.51.100.3": false} {
		got, err := CountryAllowed(netip.MustParseAddr(ip), policy, nil, lookup)
		if err != nil || got != want {
			t.Errorf("CountryAllowed(%s) = %v, %v; want %v", ip, got, err, want)
		}
	}
	policy.Countries = nil
	if got, err := CountryAllowed(netip.MustParseAddr("198.51.100.1"), policy, nil, lookup); err != nil || !got {
		t.Fatalf("empty block list = %v, %v; want known country allowed", got, err)
	}
	policy.Mode = "allow"
	if got, err := CountryAllowed(netip.MustParseAddr("198.51.100.1"), policy, nil, lookup); err != nil || got {
		t.Fatalf("empty allow list = %v, %v; want denied", got, err)
	}
}

func TestCountryAllowedRejectsInvalidPolicyAndMissingLookupData(t *testing.T) {
	ip := netip.MustParseAddr("198.51.100.1")
	for _, policy := range []Policy{{Mode: "permit"}, {Mode: "allow", Countries: []string{"ZZ"}}} {
		if got, err := CountryAllowed(ip, policy, nil, countryMap{"198.51.100.1": "SA"}); err == nil || got {
			t.Errorf("CountryAllowed(%#v) = %v, %v; want policy error and deny", policy, got, err)
		}
	}
	if got, err := CountryAllowed(ip, DefaultPolicy(), nil, nil); err != nil || got {
		t.Fatalf("missing lookup = %v, %v; want deny without error", got, err)
	}
}

func TestParseCountryCodesNormalizesAndRejectsMalformedValues(t *testing.T) {
	got, err := ParseCountryCodes("sa, in,US")
	if err != nil || strings.Join(got, ",") != "IN,SA,US" {
		t.Fatalf("ParseCountryCodes = %v, %v", got, err)
	}
	for _, raw := range []string{"USA", "S1", "SA,,IN", "unknown", "ZZ"} {
		if _, err := ParseCountryCodes(raw); err == nil {
			t.Errorf("ParseCountryCodes(%q) unexpectedly succeeded", raw)
		}
	}
}

func TestPolicyFromSettingsDefaultsAndRejectsCorruptStoredValues(t *testing.T) {
	got, err := PolicyFromSettings(nil)
	if err != nil || got.Mode != "allow" || strings.Join(got.Countries, ",") != "IN,SA" {
		t.Fatalf("default policy = %#v, %v", got, err)
	}
	got, err = PolicyFromSettings(map[string]string{SettingGeoMode: "block", SettingGeoCountries: "us"})
	if err != nil || got.Mode != "block" || strings.Join(got.Countries, ",") != "US" {
		t.Fatalf("stored policy = %#v, %v", got, err)
	}
	got, err = PolicyFromSettings(map[string]string{SettingGeoMode: "allow", SettingGeoCountries: ""})
	if err != nil || got.Mode != "allow" || len(got.Countries) != 0 {
		t.Fatalf("empty stored allow list = %#v, %v", got, err)
	}
	if _, err := PolicyFromSettings(map[string]string{SettingGeoMode: "invalid"}); err == nil {
		t.Fatal("invalid stored mode was accepted")
	}
}

func TestCountryDatabaseAcceptsCountryEditionsAndReportsBuildTime(t *testing.T) {
	for _, name := range []string{"GeoIP2-Country-Test.mmdb", "GeoLite2-Country-Test.mmdb"} {
		t.Run(name, func(t *testing.T) {
			db, err := OpenCountryDatabase(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if db.BuildTime().IsZero() {
				t.Fatal("country database build time is empty")
			}
			country, err := db.LookupCountry(netip.MustParseAddr("81.2.69.160"))
			if err != nil || country != "GB" {
				t.Fatalf("LookupCountry = %q, %v; want GB", country, err)
			}
		})
	}
}

func TestCountryDatabaseRejectsMissingCorruptAndWrongKindFiles(t *testing.T) {
	if _, err := OpenCountryDatabase(filepath.Join(t.TempDir(), "missing.mmdb")); err == nil {
		t.Fatal("missing database was accepted")
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt.mmdb")
	if err := os.WriteFile(corrupt, []byte("not a MaxMind database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCountryDatabase(corrupt); err == nil {
		t.Fatal("corrupt database was accepted")
	}
	if _, err := OpenCountryDatabase(filepath.Join("testdata", "GeoIP2-City-Test.mmdb")); err == nil {
		t.Fatal("City database was accepted as a Country database")
	}
}

func TestCountryDatabaseReturnsUnknownForPrivateAddress(t *testing.T) {
	db, err := OpenCountryDatabase(filepath.Join("testdata", "GeoIP2-Country-Test.mmdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got, err := db.LookupCountry(netip.MustParseAddr("192.168.10.2"))
	if err != nil || got != "" {
		t.Fatalf("private lookup = %q, %v; want unknown", got, err)
	}
}
