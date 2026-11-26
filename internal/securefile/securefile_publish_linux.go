//go:build linux

package securefile

import (
	"os"

	"golang.org/x/sys/unix"
)

func PublishFile(source, destination string) error {
	err := unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
	if err == nil {
		return nil
	}
	if err != unix.ENOSYS && err != unix.EINVAL && err != unix.EOPNOTSUPP {
		return err
	}
	// Hard-link publication is also atomic and never replaces an existing file.
	return os.Link(source, destination)
}

func PublishDir(source, destination string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE)
}
