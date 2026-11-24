//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/updateipc"
	"github.com/atikansari-ghr/BookmarksSync/internal/updates"
	"github.com/atikansari-ghr/BookmarksSync/internal/windowsupdate"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/debug"
	"golang.org/x/sys/windows/svc/eventlog"
)

const updaterServiceName = "BookmarksSyncUpdater"

func main() {
	code := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	os.Exit(code)
}

func run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "geolite-run" {
		if !isLocalSystem() {
			fmt.Fprintln(stderr, "updater: GeoLite task requires LocalSystem")
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		pipe, err := updateipc.DialPipe(ctx)
		if err == nil {
			err = updateipc.EncodeGeoLiteRequest(pipe, updateipc.GeoLiteRequest{Operation: updateipc.GeoLiteOperationRun})
			if err == nil {
				response, decodeErr := updateipc.DecodeGeoLiteResponse(pipe)
				err = decodeErr
				if err == nil && !response.OK {
					err = windowsupdate.ErrUpdateUnavailable
				}
			}
			_ = pipe.Close()
		}
		if err != nil {
			fmt.Fprintln(stderr, "updater: GeoLite run request failed")
			return 1
		}
		_, _ = fmt.Fprintln(stdout, "GeoLite update queued")
		return 0
	}
	if len(args) > 0 && args[0] == "worker" {
		if !isLocalSystem() {
			fmt.Fprintln(stderr, "updater: worker requires LocalSystem")
			return 1
		}
		tag, dir, err := parseWorkerArgs(args)
		if err != nil {
			fmt.Fprintln(stderr, "updater: invalid worker request")
			return 2
		}
		if err := runWorker(context.Background(), tag, dir); err != nil {
			fmt.Fprintln(stderr, "updater: update worker failed")
			return 1
		}
		return 0
	}
	if len(args) != 2 || args[0] != "service" || args[1] != "run" {
		fmt.Fprintln(stderr, "Usage: bookmarksync-updater service run")
		return 2
	}
	handler, err := newUpdaterService()
	if err != nil {
		fmt.Fprintln(stderr, "updater: service configuration is unavailable")
		return 1
	}
	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintln(stderr, "updater: cannot detect service context")
		return 1
	}
	if isService {
		err = svc.Run(updaterServiceName, handler)
	} else {
		err = debug.Run(updaterServiceName, handler)
	}
	if err != nil {
		fmt.Fprintln(stderr, "updater: service stopped with an error")
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "BookmarksSync updater stopped")
	return 0
}

type updaterService struct {
	serviceSID string
	manager    *windowsupdate.JobManager
	listener   func(context.Context, string, updateipc.PipeHandler) error
}

func newUpdaterService() (*updaterService, error) {
	paths, err := windowsupdate.DefaultWindowsSnapshotPaths()
	if err != nil {
		return nil, err
	}
	programFiles, programData := os.Getenv("ProgramFiles"), os.Getenv("ProgramData")
	gh := filepath.Join(programFiles, "GitHub CLI", "gh.exe")
	work := filepath.Join(programData, "BookmarksSync", "staging")
	verifier := windowsupdate.GitHubReleaseVerifier{GHPath: gh, WorkDir: work}
	ops := &windowsupdate.WindowsHostOperations{Paths: paths, Verifier: verifier, ReadyURL: "http://127.0.0.1:18080/readyz"}
	serviceSID, _, _, err := windows.LookupSID("", `NT SERVICE\BookmarksSync`)
	if err != nil {
		return nil, err
	}
	handler := &updaterService{serviceSID: serviceSID.String(), listener: updateipc.ListenAndServe}
	geoPaths, err := windowsupdate.GeoLitePathsForRoots(programData, programFiles)
	if err != nil {
		return nil, err
	}
	geoHost := &windowsupdate.WindowsGeoLiteHost{ApplicationServiceSID: serviceSID.String()}
	if err := geoHost.EnsureDirectories(geoPaths); err != nil {
		return nil, err
	}
	geoManager := windowsupdate.NewGeoLiteManager(geoPaths, geoHost)
	handler.manager = windowsupdate.NewJobManager(context.Background(), func(ctx context.Context, tag string) error {
		release, err := ops.VerifyRelease(ctx, tag)
		if err != nil {
			recordUpdateEvent(tag, windowsupdate.ErrUpdateUnavailable)
			return err
		}
		releaseDir := filepath.Dir(release.ServerExecutable)
		if !pathUnder(work, releaseDir) || rejectReparseTree(releaseDir) != nil {
			return windowsupdate.ErrUpdateUnavailable
		}
		defer os.RemoveAll(releaseDir)
		if err := launchWorker(ctx, work, release); err != nil {
			recordUpdateEvent(tag, windowsupdate.ErrUpdateUnavailable)
			return err
		}
		return nil
	})
	handler.manager.SetGeoLiteManager(geoManager)
	return handler, nil
}

func (s *updaterService) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	listenerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listenerDone := make(chan error, 1)
	go func() {
		listenerDone <- s.listener(listenerCtx, s.serviceSID, func(ctx context.Context, conn io.ReadWriteCloser) {
			windowsupdate.HandlePipeRequest(ctx, conn, s.manager)
		})
	}()
	statuses <- svc.Status{State: svc.StartPending, WaitHint: 10000}
	statuses <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	return s.wait(requests, statuses, listenerDone, cancel)
}

func (s *updaterService) wait(requests <-chan svc.ChangeRequest, statuses chan<- svc.Status, listenerDone <-chan error, cancel context.CancelFunc) (bool, uint32) {
	current := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-listenerDone:
			if err != nil {
				statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: 1}
				return false, 1
			}
			statuses <- svc.Status{State: svc.Stopped}
			return false, 0
		case request, ok := <-requests:
			if !ok || request.Cmd == svc.Stop || request.Cmd == svc.Shutdown {
				statuses <- svc.Status{State: svc.StopPending, WaitHint: 30000}
				cancel()
				done := make(chan struct{})
				go func() { s.manager.Close(); close(done) }()
				for {
					select {
					case <-done:
						<-listenerDone
						statuses <- svc.Status{State: svc.Stopped}
						return false, 0
					case <-time.After(20 * time.Second):
						statuses <- svc.Status{State: svc.StopPending, WaitHint: 30000}
					}
				}
			}
			if request.Cmd == svc.Interrogate {
				statuses <- current
			}
		}
	}
}

func launchWorker(ctx context.Context, work string, release windowsupdate.VerifiedRelease) error {
	releaseDir := filepath.Dir(release.ServerExecutable)
	if !filepath.IsAbs(work) || !pathUnder(work, releaseDir) || rejectReparseTree(releaseDir) != nil {
		return windowsupdate.ErrUpdateUnavailable
	}
	if err := os.MkdirAll(work, 0700); err != nil {
		return windowsupdate.ErrUpdateUnavailable
	}
	stage, err := os.CreateTemp(work, "worker-*.exe")
	if err != nil {
		return windowsupdate.ErrUpdateUnavailable
	}
	workerPath := stage.Name()
	defer os.Remove(workerPath)
	hash, err := copyAndHash(stage, release.UpdaterExecutable)
	if err != nil {
		_ = stage.Close()
		return windowsupdate.ErrUpdateUnavailable
	}
	_ = stage.Close()
	if expected, err := hashFromSums(release.SHA256Sums, windowsupdate.WindowsUpdaterAsset); err != nil || hash != expected {
		return windowsupdate.ErrUpdateUnavailable
	}
	if ctx.Err() != nil {
		return windowsupdate.ErrUpdateUnavailable
	}
	// Once the worker starts changing services or files, do not kill it merely
	// because the service host is stopping. The worker owns its bounded rollback.
	cmd := exec.Command(workerPath, "worker", "--tag", release.Tag, "--release-dir", filepath.Dir(release.ServerExecutable))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return windowsupdate.ErrUpdateUnavailable
	}
	return nil
}

func copyAndHash(dst io.Writer, source string) (string, error) {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 512<<20 {
		return "", windowsupdate.ErrUpdateUnavailable
	}
	file, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(file, (512<<20)+1))
	if err != nil || n != info.Size() {
		return "", windowsupdate.ErrUpdateUnavailable
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashFromSums(path, name string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if hash, file, ok := strings.Cut(line, "  "); ok && file == name && len(hash) == 64 {
			return strings.ToLower(hash), nil
		}
	}
	return "", errors.New("release checksum not found")
}

type localReleaseVerifier struct{ dir string }

func (v localReleaseVerifier) Verify(_ context.Context, tag string) (windowsupdate.VerifiedRelease, error) {
	return windowsupdate.VerifyReleaseFiles(v.dir, tag)
}

func runWorker(ctx context.Context, tag, dir string) error {
	workerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
	defer cancel()
	programData := os.Getenv("ProgramData")
	base := filepath.Join(programData, "BookmarksSync", "staging")
	abs, err := filepath.Abs(dir)
	if err != nil || !pathUnder(base, abs) {
		return windowsupdate.ErrUpdateUnavailable
	}
	if rejectReparseTree(abs) != nil {
		return windowsupdate.ErrUpdateUnavailable
	}
	ops, err := windowsupdate.NewDefaultWindowsHostOperations(localReleaseVerifier{dir: abs})
	if err != nil {
		return windowsupdate.ErrUpdateUnavailable
	}
	err = windowsupdate.ApplyUpdate(workerCtx, tag, ops)
	recordUpdateEvent(tag, err)
	return err
}

func recordUpdateEvent(tag string, result error) {
	log, err := eventlog.Open(updaterServiceName)
	if err != nil {
		return
	}
	defer log.Close()
	eventType, eventID, message := updateEventMessage(tag, result)
	switch eventType {
	case eventInfo:
		_ = log.Info(eventID, message)
	case eventWarning:
		_ = log.Warning(eventID, message)
	case eventError:
		_ = log.Error(eventID, message)
	default:
		_ = log.Error(eventID, message)
	}
}

type eventKind byte

const (
	eventInfo eventKind = iota
	eventWarning
	eventError
)

func updateEventMessage(tag string, result error) (eventKind, uint32, string) {
	if result == nil {
		return eventInfo, 101, "Update " + tag + " completed and the service passed its local readiness check."
	} else if errors.Is(result, windowsupdate.ErrUpdateRolledBack) {
		return eventWarning, 102, "Update " + tag + " failed; the previous verified snapshot was restored and the service is ready."
	} else if errors.Is(result, windowsupdate.ErrRollbackFailed) {
		message := "Update " + tag + " and automatic recovery failed."
		var failure *windowsupdate.RollbackFailure
		root := filepath.Join(os.Getenv("ProgramData"), "BookmarksSync", "snapshots")
		if errors.As(result, &failure) && filepath.IsAbs(failure.SnapshotPath) && pathUnder(root, failure.SnapshotPath) && strings.HasPrefix(filepath.Base(failure.SnapshotPath), "snapshot-") {
			message += " Verified snapshot retained at " + failure.SnapshotPath + "."
		}
		return eventError, 103, message
	} else if errors.Is(result, windowsupdate.ErrServiceRecoveryFailed) {
		return eventError, 105, "Update " + tag + " stopped before replacement, but the application service did not restart. Check the Windows service state."
	} else {
		return eventInfo, 104, "Update " + tag + " could not be applied. No successful activation was reported."
	}
}

func parseWorkerArgs(args []string) (string, string, error) {
	if len(args) != 5 || args[0] != "worker" || args[1] != "--tag" || args[3] != "--release-dir" || updates.ValidateApprovedTag(args[2]) != nil || !filepath.IsAbs(args[4]) {
		return "", "", windowsupdate.ErrUpdateUnavailable
	}
	return args[2], args[4], nil
}

func pathUnder(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func rejectReparseTree(dir string) error {
	for path := dir; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return windowsupdate.ErrUpdateUnavailable
		}
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return windowsupdate.ErrUpdateUnavailable
		}
		attrs, err := windows.GetFileAttributes(name)
		if err != nil || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return windowsupdate.ErrUpdateUnavailable
		}
		if parent := filepath.Dir(path); parent == path {
			return nil
		}
	}
}

func isLocalSystem() bool {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	return err == nil && user.User.Sid.String() == "S-1-5-18"
}

var _ svc.Handler = (*updaterService)(nil)
