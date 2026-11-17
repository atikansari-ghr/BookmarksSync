//go:build unix

package identity

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func createPrivateFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil { // independent of the umask
		f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return f, nil
}

// checkKeyFilePrivate requires the file to be owned by the service user and to grant
// no group or other permissions.
func checkKeyFilePrivate(_ string, fi os.FileInfo) error {
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("mode %v grants group/other access; want 0600", perm)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot determine the file owner")
	}
	if int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("owned by uid %d, not the service user %d", st.Uid, os.Geteuid())
	}
	return nil
}
