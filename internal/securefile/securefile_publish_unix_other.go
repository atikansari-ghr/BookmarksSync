//go:build unix && !linux

package securefile

import (
	"errors"
	"os"
)

func PublishFile(source, destination string) error { return os.Link(source, destination) }
func PublishDir(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, destination)
}
