//go:build linux

package updates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const installedHelperPath = "/usr/local/sbin/bookmarksync-update"

// InstalledHostRunner enables approval only when both sudo and the fixed helper
// are root-owned, non-writable by group/others, and executable regular files.
func InstalledHostRunner(serviceUser string) *HostRunner {
	if serviceUser == "" {
		serviceUser = "bookmarksync"
	}
	if !validServiceUser(serviceUser) {
		return nil
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil || !trustedExecutable(sudo) || !trustedExecutable(installedHelperPath) || !trustedExecutable("/usr/local/sbin/bookmarksync-installer") || !trustedSudoers("/etc/sudoers.d/bookmarksync-update") {
		return nil
	}
	rule, err := os.ReadFile("/etc/sudoers.d/bookmarksync-update")
	expected := serviceUser + " ALL=(root) NOPASSWD: " + installedHelperPath + " schedule --tag *"
	if err != nil || !strings.Contains(string(rule), expected) {
		return nil
	}
	return &HostRunner{SudoPath: sudo, HelperPath: installedHelperPath}
}

func trustedExecutable(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0111 == 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}

func trustedSudoers(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0440 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}

func validServiceUser(value string) bool {
	if len(value) < 1 || len(value) > 32 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, c := range value[1:] {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
