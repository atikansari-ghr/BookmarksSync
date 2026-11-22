// Package network resolves request addresses and applies country-based access rules.
package network

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

const maxForwardedHops = 32

// ParseCIDRs parses a comma-separated list of explicit IP networks. A single host
// must be written as a /32 (IPv4) or /128 (IPv6) prefix.
func ParseCIDRs(raw string) ([]netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	prefixes := make([]netip.Prefix, 0, len(parts))
	seen := make(map[netip.Prefix]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("invalid IP network %q", part)
		}
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, fmt.Errorf("invalid IPv4-mapped IP network %q", part)
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		prefix = prefix.Masked()
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		prefixes = append(prefixes, prefix)
	}
	return prefixes, nil
}

// ResolveClientIP returns the direct peer unless that peer belongs to an explicitly
// trusted proxy network. For trusted peers only X-Forwarded-For is accepted. Every
// proxy in a chain must append its connecting peer; parsing walks from the server
// outward and selects the first untrusted address.
func ResolveClientIP(r *http.Request, trusted []netip.Prefix) (netip.Addr, error) {
	if r == nil {
		return netip.Addr{}, errors.New("request is nil")
	}
	peer, err := parsePeer(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid TCP peer address")
	}
	if !contains(trusted, peer) {
		return peer, nil
	}

	values := r.Header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return netip.Addr{}, errors.New("trusted proxy omitted X-Forwarded-For")
	}
	var chain []string
	for _, value := range values {
		chain = append(chain, strings.Split(value, ",")...)
	}
	if len(chain) == 0 || len(chain) > maxForwardedHops {
		return netip.Addr{}, errors.New("invalid X-Forwarded-For chain length")
	}
	addresses := make([]netip.Addr, 0, len(chain))
	for _, token := range chain {
		token = strings.TrimSpace(token)
		ip, err := netip.ParseAddr(token)
		if err != nil || token == "" || ip.Zone() != "" {
			return netip.Addr{}, errors.New("invalid X-Forwarded-For address")
		}
		addresses = append(addresses, ip.Unmap())
	}
	for i := len(addresses) - 1; i >= 0; i-- {
		if !contains(trusted, addresses[i]) {
			return addresses[i], nil
		}
	}
	return netip.Addr{}, errors.New("X-Forwarded-For contains no untrusted client address")
}

func parsePeer(remote string) (netip.Addr, error) {
	addrPort, err := netip.ParseAddrPort(remote)
	if err == nil {
		return addrPort.Addr().Unmap(), nil
	}
	addr, err := netip.ParseAddr(remote)
	if err != nil {
		return netip.Addr{}, err
	}
	return addr.Unmap(), nil
}

func contains(prefixes []netip.Prefix, ip netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}
