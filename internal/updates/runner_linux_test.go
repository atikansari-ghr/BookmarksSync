//go:build linux

package updates

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// Exercise discovery as the real unprivileged application, without sudo, inside
// a private filesystem. No production paths or host service accounts are changed.
func TestInstalledDebianQueueUnprivileged(t *testing.T) {
	if os.Getenv("BOOKMARKSYNC_QUEUE_TEST_CHILD") == "1" {
		// Enter the private filesystem after the executable and its shared
		// libraries load, so this also works with the race detector enabled.
		if err := syscall.Chroot(os.Getenv("BOOKMARKSYNC_QUEUE_TEST_ROOT")); err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir("/"); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Setgroups(nil); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Setgid(65532); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Setuid(65532); err != nil {
			t.Fatal(err)
		}
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			t.Fatal(err)
		}
		r := InstalledHostRunner("bookmarksync")
		if os.Getenv("BOOKMARKSYNC_QUEUE_TEST_REJECT") == "1" {
			if r != nil {
				t.Fatal("unsafe or ambiguous queue was enabled")
			}
			return
		}
		if r == nil {
			t.Fatal("protected Debian queue unavailable to unprivileged hardened service")
		}
		if _, err := r.Start(context.Background(), "build-"+strings.Repeat("a", 40)); err != nil {
			t.Fatal(err)
		}
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root to create a private chroot and drop to UID65532")
	}
	jail := t.TempDir()
	if err := os.Chmod(jail, 0755); err != nil {
		t.Fatal(err)
	}
	queue := filepath.Join(jail, "var/lib/bookmarksync-update-queue/inbox")
	if err := os.MkdirAll(queue, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(queue, 65532, 65532); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(queue, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestInstalledDebianQueueUnprivileged$", "-test.v")
	cmd.Env = []string{"BOOKMARKSYNC_QUEUE_TEST_CHILD=1", "BOOKMARKSYNC_QUEUE_TEST_ROOT=" + jail, "BOOKMARKSYNC_DEBIAN_UPDATE_QUEUE=/var/lib/bookmarksync-update-queue/inbox"}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unprivileged queue: %v\n%s", err, output)
	}
	info, err := os.Stat(filepath.Join(queue, "build-"+strings.Repeat("a", 40)))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != 65532 {
		t.Fatalf("unsafe request: %+v", info)
	}
	for _, tc := range []struct {
		name    string
		prepare func() error
		restore func() error
		env     []string
	}{
		{"writable parent", func() error { return os.Chmod(filepath.Dir(queue), 0777) }, func() error { return os.Chmod(filepath.Dir(queue), 0755) }, nil},
		{"foreign owner", func() error { return os.Chown(queue, 0, 0) }, func() error { return os.Chown(queue, 65532, 65532) }, nil},
		{"exposed inbox", func() error { return os.Chmod(queue, 0755) }, func() error { return os.Chmod(queue, 0700) }, nil},
		{"symlink inbox", func() error {
			if err := os.Rename(queue, queue+".real"); err != nil {
				return err
			}
			return os.Symlink("inbox.real", queue)
		}, func() error {
			if err := os.Remove(queue); err != nil {
				return err
			}
			return os.Rename(queue+".real", queue)
		}, nil},
		{"ambiguous platform", nil, nil, []string{DockerUpdateQueueEnv + "=" + DockerUpdateQueuePath}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.prepare != nil {
				if err := tc.prepare(); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := tc.restore(); err != nil {
						t.Error(err)
					}
				}()
			}
			child := exec.Command(executable, "-test.run=^TestInstalledDebianQueueUnprivileged$", "-test.v")
			child.Env = append(append([]string{}, cmd.Env...), "BOOKMARKSYNC_QUEUE_TEST_REJECT=1")
			child.Env = append(child.Env, tc.env...)
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("queue rejection: %v\n%s", err, output)
			}
		})
	}
}
