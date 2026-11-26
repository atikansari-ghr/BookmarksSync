//go:build linux

package updates

import (
	"os"
	"path/filepath"
	"syscall"
)

// InstalledHostRunner discovers a fixed protected inbox. The service can submit
// a SHA marker without reading root-only configuration or elevating privileges.
func InstalledHostRunner(_ string) UpdateRunner {
	docker, debian := os.Getenv(DockerUpdateQueueEnv), os.Getenv(DebianUpdateQueueEnv)
	if docker != "" && debian != "" {
		return nil
	}
	if queue := docker; queue != "" {
		if queue == DockerUpdateQueuePath && protectedQueueDirectory(queue) {
			return &DockerQueueRunner{Directory: queue}
		}
		return nil
	}
	if debian == DebianUpdateQueuePath && protectedQueueDirectory(debian) {
		return &DockerQueueRunner{Directory: debian}
	}
	return nil
}

// The inbox is writable only by this app UID. Its fixed ancestors are root
// controlled so the application cannot replace the mount point or worker path.
func protectedQueueDirectory(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return false
	}
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return false
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return false
		}
		if parent == "/" {
			break
		}
	}
	return true
}
