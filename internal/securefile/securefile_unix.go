//go:build unix

package securefile

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func Create(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return f, nil
}

func Check(path string, fi os.FileInfo) error {
	if !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("file is not a regular file")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return errors.New("file permissions grant access to group or other users")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("cannot determine file owner")
	}
	if int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("file is not owned by the current user")
	}
	return nil
}

func RestrictFile(path string) error {
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return Check(path, fi)
}

func MkdirPrivate(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func CheckDirectory(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("path is not a regular directory")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return errors.New("directory permissions grant access to group or other users")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Geteuid() {
		return errors.New("directory is not owned by the current user")
	}
	return nil
}

func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
