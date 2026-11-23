//go:build !linux && !windows

package updates

// Non-Linux deployments require a platform-specific updater.
func InstalledHostRunner(string) UpdateRunner { return nil }
