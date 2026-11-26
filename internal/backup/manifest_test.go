package backup

import (
	"strings"
	"testing"
	"time"
)

func TestManifestEnforcesPerFileSizeLimits(t *testing.T) {
	manifest := Manifest{FormatVersion: FormatVersion, CreatedAt: time.Now().UTC(), SchemaVersion: 1, Entries: []Entry{{Name: databaseName, Size: 1, SHA256: strings.Repeat("0", 64)}, {Name: runtimeName, Size: 1, SHA256: strings.Repeat("0", 64)}}}
	if err := manifest.validate(); err != nil {
		t.Fatalf("small required files rejected: %v", err)
	}
	manifest.Entries = append(manifest.Entries, Entry{Name: "branding/0123456789abcdef0123456789abcdef.png", Size: maxBrandingSize + 1, SHA256: strings.Repeat("0", 64)})
	if err := manifest.validate(); err == nil {
		t.Fatal("accepted branding entry above upload limit")
	}
	manifest.Entries[2] = Entry{Name: keyName, Size: 31, SHA256: strings.Repeat("0", 64)}
	if err := manifest.validate(); err == nil {
		t.Fatal("accepted malformed instance-key length")
	}
}
