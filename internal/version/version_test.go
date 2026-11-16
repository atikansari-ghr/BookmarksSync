package version_test

import (
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/version"
)

func TestCurrentReportsInjectedValues(t *testing.T) {
	oldV, oldC, oldD := version.Version, version.Commit, version.Date
	t.Cleanup(func() { version.Version, version.Commit, version.Date = oldV, oldC, oldD })
	version.Version, version.Commit, version.Date = "1.2.3", "abc123", "2026-11-16T00:00:00Z"

	got := version.Current()
	if got.Version != "1.2.3" || got.Commit != "abc123" || got.BuildDate != "2026-11-16T00:00:00Z" {
		t.Errorf("Current() = %+v", got)
	}
	if got.String() != "1.2.3 (commit abc123, built 2026-11-16T00:00:00Z)" {
		t.Errorf("String() = %q", got.String())
	}
}

func TestDefaultsIdentifyDevelopmentBuild(t *testing.T) {
	if got := version.Current(); got.Version == "" || got.Commit == "" || got.BuildDate == "" {
		t.Errorf("defaults must be non-empty: %+v", got)
	}
}
