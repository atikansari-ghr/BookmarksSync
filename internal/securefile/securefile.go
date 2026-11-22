// Package securefile creates and checks files private to the current service user.
package securefile

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func CreateTemp(dir, pattern string) (*os.File, error) {
	if strings.Count(pattern, "*") > 1 {
		return nil, errors.New("temporary file pattern has multiple wildcards")
	}
	for i := 0; i < 100; i++ {
		var suffix [12]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return nil, err
		}
		name := strings.Replace(pattern, "*", hex.EncodeToString(suffix[:]), 1)
		if !strings.Contains(pattern, "*") {
			name = pattern + hex.EncodeToString(suffix[:])
		}
		f, err := Create(filepath.Join(dir, name))
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	return nil, errors.New("could not allocate a private temporary file")
}

func CreateTempDir(parent, pattern string) (string, error) {
	if strings.Count(pattern, "*") > 1 {
		return "", errors.New("temporary directory pattern has multiple wildcards")
	}
	for i := 0; i < 100; i++ {
		var suffix [12]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", err
		}
		name := strings.Replace(pattern, "*", hex.EncodeToString(suffix[:]), 1)
		if !strings.Contains(pattern, "*") {
			name = pattern + hex.EncodeToString(suffix[:])
		}
		path := filepath.Join(parent, name)
		if err := MkdirPrivate(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrExist) {
			return "", err
		}
	}
	return "", errors.New("could not allocate a private temporary directory")
}

func MkdirAll(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return nil
		}
		return errors.New("path exists and is not a directory")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return os.ErrInvalid
	}
	if err := MkdirAll(parent); err != nil {
		return err
	}
	if err := MkdirPrivate(path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("path is not a directory")
		}
	}
	return nil
}
