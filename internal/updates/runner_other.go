//go:build !linux

package updates

// Non-Linux deployments require a platform-specific updater.
func InstalledHostRunner(string) *HostRunner { return nil }
