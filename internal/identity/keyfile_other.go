//go:build !unix && !windows

package identity

import (
	"errors"
	"os"
)

var errUnsupportedKeyPlatform = errors.New("private key files are not supported on this platform")

func createPrivateFile(string) (*os.File, error)    { return nil, errUnsupportedKeyPlatform }
func checkKeyFilePrivate(string, os.FileInfo) error { return errUnsupportedKeyPlatform }
