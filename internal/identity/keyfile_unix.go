//go:build unix

package identity

import (
	"os"

	"github.com/atikansari-ghr/BookmarksSync/internal/securefile"
)

func createPrivateFile(path string) (*os.File, error)       { return securefile.Create(path) }
func checkKeyFilePrivate(path string, fi os.FileInfo) error { return securefile.Check(path, fi) }
