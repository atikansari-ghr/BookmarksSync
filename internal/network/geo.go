package network

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/oschwald/geoip2-golang/v2"
)

const (
	SettingGeoMode      = "network.geo-mode"
	SettingGeoCountries = "network.geo-countries"
)

type Policy struct {
	Mode      string
	Countries []string
}

// CountryLookup is intentionally small so enforcement tests do not need a real
// GeoIP database. Empty codes mean no location data was available.
type CountryLookup interface {
	LookupCountry(netip.Addr) (string, error)
}

type CountryDatabase struct {
	reader       *geoip2.Reader
	build        time.Time
	databaseType string
}

func DefaultPolicy() Policy { return Policy{Mode: "allow", Countries: []string{"IN", "SA"}} }

// PolicyFromSettings applies the approved defaults only when a key is absent. An
// explicitly stored empty country list remains empty (deny all for allow mode).
func PolicyFromSettings(settings map[string]string) (Policy, error) {
	policy := DefaultPolicy()
	if mode, ok := settings[SettingGeoMode]; ok {
		policy.Mode = mode
	}
	if raw, ok := settings[SettingGeoCountries]; ok {
		codes, err := ParseCountryCodes(raw)
		if err != nil {
			return Policy{}, err
		}
		policy.Countries = codes
	}
	if err := ValidatePolicy(policy); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

// ParseCountryCodes accepts comma-separated ISO 3166-1 alpha-2 codes, normalizes
// case and order, and rejects duplicates or invalid/empty entries.
func ParseCountryCodes(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}, nil
	}
	seen := make(map[string]bool)
	codes := strings.Split(raw, ",")
	for i, code := range codes {
		code = strings.ToUpper(strings.TrimSpace(code))
		if !validCountryCodes[code] {
			return nil, fmt.Errorf("invalid ISO country code %q", strings.TrimSpace(codes[i]))
		}
		if seen[code] {
			return nil, fmt.Errorf("duplicate ISO country code %q", code)
		}
		seen[code] = true
		codes[i] = code
	}
	// Use a stable ordering for storage, display, and equality checks.
	for i := 1; i < len(codes); i++ {
		for j := i; j > 0 && codes[j] < codes[j-1]; j-- {
			codes[j], codes[j-1] = codes[j-1], codes[j]
		}
	}
	return codes, nil
}

// NormalizeCountryCodes validates one country code per API/UI list item and
// returns stable uppercase ordering. Empty lists are valid for either mode.
func NormalizeCountryCodes(input []string) ([]string, error) {
	for _, code := range input {
		if len(code) != 2 || !isASCIILetter(code[0]) || !isASCIILetter(code[1]) {
			return nil, fmt.Errorf("invalid ISO country code %q", code)
		}
	}
	return ParseCountryCodes(strings.Join(input, ","))
}

func isASCIILetter(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' }

func ValidatePolicy(p Policy) error {
	if p.Mode != "allow" && p.Mode != "block" {
		return errors.New("country mode must be allow or block")
	}
	seen := make(map[string]bool, len(p.Countries))
	for _, code := range p.Countries {
		if len(code) != 2 || !validCountryCodes[code] {
			return fmt.Errorf("invalid ISO country code %q", code)
		}
		if seen[code] {
			return fmt.Errorf("duplicate ISO country code %q", code)
		}
		seen[code] = true
	}
	return nil
}

// CountryAllowed applies an explicit management-network exception before country
// lookup. Private, loopback, link-local, and unspecified addresses are not exempt by
// address class; operators must list any private management networks explicitly.
func CountryAllowed(ip netip.Addr, policy Policy, management []netip.Prefix, lookup CountryLookup) (bool, error) {
	if !ip.IsValid() {
		return false, nil
	}
	ip = ip.Unmap()
	if contains(management, ip) {
		return true, nil
	}
	if err := ValidatePolicy(policy); err != nil {
		return false, err
	}
	if lookup == nil {
		return false, nil
	}
	code, err := lookup.LookupCountry(ip)
	if err != nil {
		return false, err
	}
	code = strings.ToUpper(code)
	if !validCountryCodes[code] {
		return false, nil
	}
	listed := false
	for _, candidate := range policy.Countries {
		if candidate == code {
			listed = true
			break
		}
	}
	if policy.Mode == "allow" {
		return listed, nil
	}
	return !listed, nil
}

// OpenCountryDatabase loads an offline GeoLite2-Country or GeoIP2-Country database.
// This fixed reader stays on the opened file. Servers use ReloadingCountryDatabase
// to observe validated replacements without restarting.
func OpenCountryDatabase(path string) (*CountryDatabase, error) {
	return openCountryDatabase(path, "")
}

// OpenGeoLiteCountryDatabase opens only GeoLite2 Country files and rejects
// non-regular files and files larger than 64 MiB before memory-mapping them.
func OpenGeoLiteCountryDatabase(path string) (*CountryDatabase, error) {
	if err := rejectGeoLiteReparsePath(path); err != nil {
		return nil, errors.New("invalid GeoLite country database path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("open GeoLite country database: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 64<<20 {
		return nil, errors.New("invalid GeoLite country database file")
	}
	return openCountryDatabase(path, "GeoLite2-Country")
}

func openCountryDatabase(path, requiredType string) (*CountryDatabase, error) {
	reader, err := geoip2.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open country database: %w", err)
	}
	metadata := reader.Metadata()
	if requiredType != "" && metadata.DatabaseType != requiredType {
		_ = reader.Close()
		return nil, fmt.Errorf("unsupported GeoLite database type %q", metadata.DatabaseType)
	}
	if metadata.DatabaseType != "GeoLite2-Country" && metadata.DatabaseType != "GeoIP2-Country" {
		_ = reader.Close()
		return nil, fmt.Errorf("unsupported GeoIP database type %q", metadata.DatabaseType)
	}
	build := metadata.BuildTime()
	if build.IsZero() || build.After(time.Now().Add(time.Hour)) {
		_ = reader.Close()
		return nil, errors.New("GeoIP database has invalid build time")
	}
	return &CountryDatabase{reader: reader, build: build, databaseType: metadata.DatabaseType}, nil
}

func (db *CountryDatabase) BuildTime() time.Time { return db.build }

func (db *CountryDatabase) DatabaseType() string {
	if db == nil {
		return ""
	}
	return db.databaseType
}

func (db *CountryDatabase) Close() error {
	if db == nil || db.reader == nil {
		return nil
	}
	return db.reader.Close()
}

func (db *CountryDatabase) LookupCountry(ip netip.Addr) (string, error) {
	if db == nil || db.reader == nil {
		return "", nil
	}
	record, err := db.reader.Country(ip.Unmap())
	if err != nil {
		return "", err
	}
	if !record.HasData() {
		return "", nil
	}
	return record.Country.ISOCode, nil
}
