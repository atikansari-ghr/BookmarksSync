// Package geolite contains the platform-neutral configuration and status
// shared by the Admin API, the Windows updater service, and its pipe protocol.
package geolite

import (
	"errors"
	"regexp"
	"time"
)

var (
	ErrInvalidConfig = errors.New("invalid GeoLite configuration")
	timePattern      = regexp.MustCompile(`^(?:[01][0-9]|2[0-3]):[0-5][0-9]$`)
)

// Config contains operator-supplied credentials and a weekly local-time schedule.
// Empty credential fields mean “keep the stored value” when updating a config.
type Config struct {
	AccountID  string   `json:"accountId,omitempty"`
	LicenseKey string   `json:"licenseKey,omitempty"`
	Enabled    bool     `json:"enabled"`
	Days       []string `json:"days"`
	StartTime  string   `json:"startTime"`
}

// Status is safe to return to the browser and intentionally has no credential fields.
type Status struct {
	Configured        bool       `json:"configured"`
	Enabled           bool       `json:"enabled"`
	Days              []string   `json:"days"`
	StartTime         string     `json:"startTime"`
	DatabaseAvailable bool       `json:"databaseAvailable"`
	DatabaseBuildTime *time.Time `json:"databaseBuildTime"`
	LastAttempt       *time.Time `json:"lastAttempt"`
	LastResult        string     `json:"lastResult"`
	LastErrorCode     string     `json:"lastErrorCode,omitempty"`
	Running           bool       `json:"running"`
}

// DefaultConfig returns the Debian-aligned schedule without enabling it before
// the administrator has entered MaxMind credentials.
func DefaultConfig() Config {
	return Config{Days: []string{"MON", "THU"}, StartTime: "04:00"}
}

// ValidateConfig checks credential field safety and the local weekly schedule.
func ValidateConfig(config Config) error {
	if len(config.AccountID) > 64 || hasControl(config.AccountID) || !digitsOnly(config.AccountID) && config.AccountID != "" {
		return ErrInvalidConfig
	}
	if len(config.LicenseKey) > 256 || hasControl(config.LicenseKey) {
		return ErrInvalidConfig
	}
	if len(config.Days) < 2 || len(config.Days) > 7 || !timePattern.MatchString(config.StartTime) {
		return ErrInvalidConfig
	}
	validDay := map[string]bool{"MON": true, "TUE": true, "WED": true, "THU": true, "FRI": true, "SAT": true, "SUN": true}
	seen := make(map[string]bool, len(config.Days))
	for _, day := range config.Days {
		if !validDay[day] || seen[day] {
			return ErrInvalidConfig
		}
		seen[day] = true
	}
	return nil
}

func digitsOnly(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func hasControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}
