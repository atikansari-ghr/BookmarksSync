package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const pinnedRcloneVersion = "1.76.0"

type RcloneRunner interface {
	Run(context.Context, string, []string) ([]byte, error)
}

type execRcloneRunner struct{}

func (execRcloneRunner) Run(ctx context.Context, program string, args []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return out.Bytes(), err
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	const max = 4096
	n := len(p)
	remaining := max - b.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = b.Buffer.Write(p[:remaining])
	}
	return n, nil
}

type RcloneDriver struct {
	BinaryPath string
	ConfigPath string
	Runner     RcloneRunner
}

func (d RcloneDriver) run(ctx context.Context, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	program := d.BinaryPath
	if program == "" {
		program = "rclone"
	}
	runner := d.Runner
	if runner == nil {
		runner = execRcloneRunner{}
	}
	if d.ConfigPath != "" {
		args = append([]string{"--config", d.ConfigPath}, args...)
	}
	out, err := runner.Run(ctx, program, args)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, errors.New("rclone operation failed")
	}
	return out, nil
}

func (d RcloneDriver) ensureVersion(ctx context.Context) error {
	out, err := d.run(ctx, "version")
	if err != nil {
		return err
	}
	first, _, _ := strings.Cut(string(out), "\n")
	if strings.TrimSpace(first) != "rclone v"+pinnedRcloneVersion {
		return errors.New("configured rclone version is unsupported")
	}
	return nil
}

func (d RcloneDriver) Test(ctx context.Context, destination Destination) error {
	if destination.Kind != DestinationRclone || validateRcloneReference(destination.Reference) != nil {
		return errors.New("rclone destination reference is invalid")
	}
	if err := d.ensureVersion(ctx); err != nil {
		return err
	}
	if _, err := d.run(ctx, "mkdir", destination.Reference); err != nil {
		return err
	}
	localDir, err := os.MkdirTemp("", "bookmarksync-rclone-probe-*")
	if err != nil {
		return errors.New("rclone destination check could not create a probe")
	}
	defer os.RemoveAll(localDir)
	probeName := "probe.bin"
	localProbe := filepath.Join(localDir, probeName)
	tmp, err := os.OpenFile(localProbe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("rclone destination check could not create a probe file")
	}
	if _, err := io.WriteString(tmp, "bookmarksync destination verification\n"); err != nil {
		tmp.Close()
		return errors.New("rclone destination check could not write a probe")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("rclone destination check could not close a probe")
	}
	probe, err := randomHex(16)
	if err != nil {
		return errors.New("rclone destination check could not create an identifier")
	}
	remoteDir := remotePath(destination.Reference, ".bookmarksync-probe-"+probe)
	remote := remotePath(remoteDir, probeName)
	defer func() {
		d.cleanupRemote(remote)
		d.cleanupRemoteDir(remoteDir)
	}()
	if _, err := d.run(ctx, "mkdir", remoteDir); err != nil {
		return err
	}
	if _, err := d.run(ctx, "copyto", "--immutable", localProbe, remote); err != nil {
		return err
	}
	if _, err := d.run(ctx, "check", "--download", "--include", probeName, localDir, remoteDir); err != nil {
		return errors.New("rclone destination verification failed")
	}
	return nil
}

func (d RcloneDriver) Publish(ctx context.Context, destination Destination, sourcePath, name string) error {
	if destination.Kind != DestinationRclone || validateRcloneReference(destination.Reference) != nil || !validArchiveName(name) {
		return errors.New("rclone backup target is invalid")
	}
	if err := d.ensureVersion(ctx); err != nil {
		return err
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return errors.New("verified backup archive cannot be opened")
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || !info.Mode().IsRegular() {
		return errors.New("verified backup archive is not a regular file")
	}
	id, err := randomHex(16)
	if err != nil {
		return errors.New("rclone transfer could not create an identifier")
	}
	tmpRemoteDir := remotePath(destination.Reference, ".bookmarksync-upload-"+id)
	tmpRemote := remotePath(tmpRemoteDir, name)
	finalRemote := remotePath(destination.Reference, name)
	if _, err := d.run(ctx, "mkdir", destination.Reference); err != nil {
		return err
	}
	cleanupNeeded := true
	defer func() {
		if cleanupNeeded {
			d.cleanupRemote(tmpRemote)
			d.cleanupRemoteDir(tmpRemoteDir)
		}
	}()
	if _, err := d.run(ctx, "mkdir", tmpRemoteDir); err != nil {
		return err
	}
	if _, err := d.run(ctx, "copyto", "--immutable", sourcePath, tmpRemote); err != nil {
		return err
	}
	if _, err := d.run(ctx, "check", "--download", "--include", name, filepath.Dir(sourcePath), tmpRemoteDir); err != nil {
		return errors.New("temporary backup verification failed")
	}
	if existing, err := d.run(ctx, "lsf", "--files-only", "--include", name, destination.Reference); err != nil {
		return err
	} else if strings.TrimSpace(string(existing)) != "" {
		return errors.New("generated backup name already exists at destination")
	}
	publicationComplete := false
	defer func() {
		if !publicationComplete {
			d.cleanupRemote(finalRemote)
		}
	}()
	if _, err := d.run(ctx, "copyto", "--immutable", tmpRemote, finalRemote); err != nil {
		return err
	}
	if _, err := d.run(ctx, "deletefile", tmpRemote); err != nil {
		return errors.New("temporary backup object could not be removed")
	}
	if _, err := d.run(ctx, "rmdir", tmpRemoteDir); err != nil {
		return errors.New("temporary backup folder could not be removed")
	}
	cleanupNeeded = false
	if _, err := d.run(ctx, "check", "--download", "--include", name, filepath.Dir(sourcePath), destination.Reference); err != nil {
		return errors.New("published backup verification failed")
	}
	publicationComplete = true
	return nil
}

func (d RcloneDriver) Delete(ctx context.Context, destination Destination, name string) error {
	if destination.Kind != DestinationRclone || validateRcloneReference(destination.Reference) != nil || !validArchiveName(name) {
		return errors.New("rclone backup target is invalid")
	}
	if err := d.ensureVersion(ctx); err != nil {
		return err
	}
	_, err := d.run(ctx, "deletefile", remotePath(destination.Reference, name))
	return err
}

func (d RcloneDriver) cleanupRemote(remote string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = d.run(ctx, "deletefile", remote)
}

func (d RcloneDriver) cleanupRemoteDir(remoteDir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = d.run(ctx, "rmdir", remoteDir)
}

func remotePath(reference, name string) string {
	if strings.HasSuffix(reference, ":") {
		return reference + name
	}
	return strings.TrimSuffix(reference, "/") + "/" + name
}

var _ DestinationDriver = RcloneDriver{}

func (d RcloneDriver) String() string { return fmt.Sprintf("rclone(%s)", d.BinaryPath) }
