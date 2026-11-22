//go:build linux

package backup

import (
	"os"
)

func isNFSMount(path string) (bool, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	t := mountTypeFromInfo(path, string(data))
	return t == "nfs" || t == "nfs4", nil
}
