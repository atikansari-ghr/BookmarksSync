//go:build !linux

package updates

// Host updates are supported only on Linux.
func InstalledHostRunner(string) UpdateRunner { return nil }
