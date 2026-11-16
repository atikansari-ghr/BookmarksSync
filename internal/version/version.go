// Package version exposes build identification injected at link time.
package version

import "fmt"

// These are overridden with -ldflags "-X .../internal/version.Version=..." at build time.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Info describes the running build.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
}

// Current returns the build information for this binary.
func Current() Info {
	return Info{Version: Version, Commit: Commit, BuildDate: Date}
}

func (i Info) String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", i.Version, i.Commit, i.BuildDate)
}
