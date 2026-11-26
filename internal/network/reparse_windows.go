//go:build windows

package network

import (
	"errors"
	"golang.org/x/sys/windows"
	"path/filepath"
)

func rejectGeoLiteReparsePath(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		name, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return err
		}
		attrs, err := windows.GetFileAttributes(name)
		if err != nil {
			return err
		}
		if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return errors.New("reparse path")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}
