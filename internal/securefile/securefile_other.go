//go:build !unix && !windows

package securefile

import (
	"errors"
	"os"
)

var errUnsupported = errors.New("private file permissions are not supported on this platform")

func Create(string) (*os.File, error)              { return nil, errUnsupported }
func Check(string, os.FileInfo) error              { return errUnsupported }
func RestrictFile(string) error                    { return errUnsupported }
func MkdirPrivate(string) error                    { return errUnsupported }
func CheckDirectory(string) error                  { return errUnsupported }
func SyncDir(string) error                         { return nil }
func PublishFile(source, destination string) error { return os.Link(source, destination) }
func PublishDir(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, destination)
}
