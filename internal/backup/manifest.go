// Package backup creates, verifies, and restores portable full-system archives.
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/version"
)

const FormatVersion = 1

const (
	manifestName        = "manifest.json"
	databaseName        = "database/bookmarksync.db"
	runtimeName         = "config/runtime.json"
	keyName             = "instance.key"
	maxManifest         = 64 << 10
	maxRuntime          = 64 << 10
	maxCentralDirectory = 32 << 20
	maxFiles            = 256
	maxDatabaseSize     = int64(8 << 30)
	maxBrandingSize     = int64(2 << 20)
	maxTotalSize        = int64(16 << 30)
	maxArchiveSize      = int64(17 << 30)
)

var brandingName = regexp.MustCompile(`^branding/[a-f0-9]{32}\.(png|jpg|webp)$`)

type Entry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	FormatVersion   int          `json:"formatVersion"`
	CreatedAt       time.Time    `json:"createdAt"`
	Application     version.Info `json:"application"`
	SchemaVersion   int          `json:"schemaVersion"`
	PublicOrigin    string       `json:"publicOrigin,omitempty"`
	OIDCProviderIDs []string     `json:"oidcProviderIds,omitempty"`
	Entries         []Entry      `json:"entries"`
}

func (m Manifest) validate() error {
	if m.FormatVersion != FormatVersion {
		return fmt.Errorf("unsupported backup format version %d", m.FormatVersion)
	}
	if m.CreatedAt.IsZero() || m.SchemaVersion < 1 {
		return errors.New("backup manifest is incomplete")
	}
	if len(m.Entries) < 2 || len(m.Entries) > maxFiles {
		return errors.New("backup manifest entry count is invalid")
	}
	seen := map[string]bool{}
	var total int64
	hasDB, hasRuntime := false, false
	for _, e := range m.Entries {
		if !allowedName(e.Name) || e.Name == manifestName || seen[e.Name] {
			return errors.New("backup manifest contains an invalid or duplicate entry")
		}
		limit, ok := entrySizeLimit(e.Name)
		if !ok || e.Size < 0 || e.Size > limit || len(e.SHA256) != 64 {
			return errors.New("backup manifest entry metadata is invalid")
		}
		if e.Name == keyName && e.Size != identity.InstanceKeySize {
			return errors.New("backup instance key size is invalid")
		}
		if decoded, err := hex.DecodeString(e.SHA256); err != nil || len(decoded) != sha256.Size {
			return errors.New("backup manifest contains an invalid checksum")
		}
		seen[e.Name] = true
		total += e.Size
		if total > maxTotalSize {
			return errors.New("backup expands beyond the supported size limit")
		}
		hasDB = hasDB || e.Name == databaseName
		hasRuntime = hasRuntime || e.Name == runtimeName
	}
	if !hasDB || !hasRuntime {
		return errors.New("backup is missing required system files")
	}
	return nil
}

func entrySizeLimit(name string) (int64, bool) {
	switch {
	case name == databaseName:
		return maxDatabaseSize, true
	case name == runtimeName:
		return maxRuntime, true
	case name == keyName:
		return identity.InstanceKeySize, true
	case brandingName.MatchString(name):
		return maxBrandingSize, true
	default:
		return 0, false
	}
}

func allowedName(name string) bool {
	return name == databaseName || name == runtimeName || name == keyName || brandingName.MatchString(name)
}

func validAssetID(id string) bool {
	return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == ""
}

func encodeManifest(m Manifest) ([]byte, error) {
	if err := m.validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(m, "", "  ")
}
