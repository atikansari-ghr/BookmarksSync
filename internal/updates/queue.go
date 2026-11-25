package updates

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const DockerUpdateQueuePath = "/run/bookmarksync/update-queue"
const DockerUpdateQueueEnv = "BOOKMARKSYNC_DOCKER_UPDATE_QUEUE"
const DebianUpdateQueuePath = "/var/lib/bookmarksync-update-queue/inbox"
const DebianUpdateQueueEnv = "BOOKMARKSYNC_DEBIAN_UPDATE_QUEUE"

// DockerQueueRunner submits only an empty, SHA-named approval marker. The host
// worker ignores file contents and independently verifies release provenance.
// No host command, image URL or Docker control socket crosses this boundary.
type DockerQueueRunner struct {
	Directory string
	mu        sync.Mutex
}

func (r *DockerQueueRunner) Start(ctx context.Context, tag string) (string, error) {
	if ValidateApprovedTag(tag) != nil {
		return "", errors.New("invalid release tag")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !filepath.IsAbs(r.Directory) {
		return "", ErrRunnerUnavailable
	}
	info, err := os.Lstat(r.Directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrRunnerUnavailable
	}
	root, err := os.OpenRoot(r.Directory)
	if err != nil {
		return "", ErrRunnerUnavailable
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return "", ErrRunnerUnavailable
	}
	defer dir.Close()
	names, err := dir.Readdirnames(65)
	if len(names) >= 64 || err != nil && !errors.Is(err, io.EOF) {
		return "", ErrRunnerUnavailable
	}
	file, err := root.OpenFile(tag, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", ErrRunnerUnavailable
	}
	if syncErr := file.Sync(); syncErr != nil {
		_ = file.Close()
		_ = root.Remove(tag)
		return "", ErrRunnerUnavailable
	}
	if err := file.Close(); err != nil {
		_ = root.Remove(tag)
		return "", ErrRunnerUnavailable
	}
	if runtime.GOOS == "linux" {
		if err := dir.Sync(); err != nil {
			return "", ErrRunnerUnavailable
		}
	}
	return "bookmarksync-docker-update-" + strings.TrimPrefix(tag, "build-"), nil
}
