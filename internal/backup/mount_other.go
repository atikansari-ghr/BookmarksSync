//go:build !linux

package backup

import "errors"

func isNFSMount(string) (bool, error) {
	return false, errors.New("NFS mount validation is supported only on Linux")
}
