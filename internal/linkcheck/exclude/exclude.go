// Package exclude parses and matches per-user link-check exclusions. It depends only on
// the standard library so that the identity service can validate the stored setting
// without importing the link checker.
//
// An exclusion can only make the checker SKIP a target. It never relaxes the public-only
// destination rules, which the checker applies independently.
package exclude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// SettingKey is the user-setting key that stores the exclusion list.
const SettingKey = "linkcheck.exclusions"

// MaxEntries bounds the list; the whole JSON value must also fit the 8192-byte setting.
const MaxEntries = 100

// Set is a parsed exclusion list. The zero value excludes nothing.
type Set struct {
	domains  []string
	prefixes []netip.Prefix
}

// Valid reports whether value is a well-formed exclusion list.
func Valid(value string) bool {
	_, err := Parse(value)
	return err == nil
}

// Parse parses a JSON array of strings; each is a domain name (matching itself and every
// subdomain), a single IP address, or a CIDR range.
func Parse(value string) (Set, error) {
	dec := json.NewDecoder(strings.NewReader(value))
	var raw []string
	if err := dec.Decode(&raw); err != nil {
		return Set{}, errors.New("exclusions must be a JSON array of strings")
	}
	if raw == nil {
		return Set{}, errors.New("exclusions must be a JSON array of strings")
	}
	if dec.More() || dec.InputOffset() != int64(len(bytes.TrimRight([]byte(value), " \t\r\n"))) {
		return Set{}, errors.New("unexpected data after the exclusion list")
	}
	if len(raw) > MaxEntries {
		return Set{}, fmt.Errorf("at most %d exclusions are allowed", MaxEntries)
	}
	var s Set
	for _, e := range raw {
		if err := s.add(e); err != nil {
			return Set{}, err
		}
	}
	return s, nil
}

func (s *Set) add(e string) error {
	if e == "" || e != strings.TrimSpace(e) {
		return errors.New("empty or padded exclusion")
	}
	if strings.Contains(e, "/") {
		p, err := netip.ParsePrefix(e)
		if err != nil || p.Addr().Zone() != "" {
			return fmt.Errorf("invalid CIDR %q", e)
		}
		s.prefixes = append(s.prefixes, normalizePrefix(p))
		return nil
	}
	if a, err := netip.ParseAddr(e); err == nil {
		if a.Zone() != "" {
			return fmt.Errorf("invalid address %q", e)
		}
		a = a.Unmap()
		s.prefixes = append(s.prefixes, netip.PrefixFrom(a, a.BitLen()))
		return nil
	}
	d, ok := normalizeDomain(e)
	if !ok {
		return fmt.Errorf("invalid domain %q", e)
	}
	s.domains = append(s.domains, d)
	return nil
}

// normalizePrefix turns an IPv4-mapped IPv6 prefix into its IPv4 form.
func normalizePrefix(p netip.Prefix) netip.Prefix {
	if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
		return netip.PrefixFrom(a.Unmap(), p.Bits()-96).Masked()
	}
	return p.Masked()
}

// normalizeDomain lowercases and strips one trailing dot; it accepts only plain LDH
// hostnames (use the xn-- form for internationalised names).
func normalizeDomain(d string) (string, bool) {
	d = strings.ToLower(strings.TrimSuffix(d, "."))
	if d == "" || len(d) > 253 {
		return "", false
	}
	labels := strings.Split(d, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", false
			}
		}
	}
	return d, true
}

// Empty reports whether the set excludes nothing.
func (s Set) Empty() bool { return len(s.domains) == 0 && len(s.prefixes) == 0 }

// MatchHost reports whether a URL host (a domain or an IP literal, without brackets) is
// excluded: a listed domain or any subdomain of it, or an address inside a listed range.
func (s Set) MatchHost(host string) bool {
	if host == "" {
		return false
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return s.MatchAddr(a)
	}
	d, ok := normalizeDomain(host)
	if !ok {
		return false
	}
	for _, x := range s.domains {
		if d == x || strings.HasSuffix(d, "."+x) {
			return true
		}
	}
	return false
}

// MatchAddr reports whether a is inside a listed range or equals a listed address.
func (s Set) MatchAddr(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range s.prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
