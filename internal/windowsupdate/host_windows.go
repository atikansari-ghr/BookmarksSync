//go:build windows

package windowsupdate

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/updates"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type ReleaseVerifier interface {
	Verify(context.Context, string) (VerifiedRelease, error)
}

type WindowsHostOperations struct {
	Paths      SnapshotPaths
	Verifier   ReleaseVerifier
	ReadyURL   string
	HTTPClient *http.Client
}

func (o *WindowsHostOperations) VerifyRelease(ctx context.Context, tag string) (VerifiedRelease, error) {
	if o == nil || o.Verifier == nil || updates.ValidateApprovedTag(tag) != nil {
		return VerifiedRelease{}, ErrUpdateUnavailable
	}
	release, err := o.Verifier.Verify(ctx, tag)
	if err != nil || release.Tag != tag || release.Commit != tag[len("build-"):] || !release.IsComplete() {
		return VerifiedRelease{}, ErrUpdateUnavailable
	}
	return release, nil
}

func (o *WindowsHostOperations) StopService(ctx context.Context, name string) error {
	return o.controlService(ctx, name, false)
}

func (o *WindowsHostOperations) StartService(ctx context.Context, name string) error {
	return o.controlService(ctx, name, true)
}

func (o *WindowsHostOperations) controlService(ctx context.Context, name string, start bool) error {
	if name != AppServiceName {
		return ErrUpdateUnavailable
	}
	manager, err := mgr.Connect()
	if err != nil {
		return ErrUpdateUnavailable
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(name)
	if err != nil {
		return ErrUpdateUnavailable
	}
	defer service.Close()
	status, err := service.Query()
	if err != nil {
		return ErrUpdateUnavailable
	}
	if start {
		if status.State == svc.Running {
			return nil
		}
		if status.State != svc.StartPending {
			if err := service.Start(); err != nil {
				return ErrUpdateUnavailable
			}
		}
		return waitWindowsService(ctx, service, svc.Running)
	}
	if status.State == svc.Stopped {
		return nil
	}
	if status.State != svc.StopPending {
		if _, err := service.Control(svc.Stop); err != nil {
			return ErrUpdateUnavailable
		}
	}
	return waitWindowsService(ctx, service, svc.Stopped)
}

func waitWindowsService(ctx context.Context, service *mgr.Service, target svc.State) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := service.Query()
		if err != nil {
			return ErrUpdateUnavailable
		}
		if status.State == target {
			return nil
		}
		select {
		case <-ctx.Done():
			return ErrUpdateUnavailable
		case <-ticker.C:
		}
	}
}

func (o *WindowsHostOperations) CreateVerifiedSnapshot(ctx context.Context, tag string) (Snapshot, error) {
	return CreateSnapshot(ctx, o.Paths, tag)
}

func (o *WindowsHostOperations) ReplaceInstalledFiles(_ context.Context, release VerifiedRelease) error {
	verified, err := VerifyReleaseFiles(filepath.Dir(release.ServerExecutable), release.Tag)
	if err != nil || verified.Commit != release.Commit || verified.ServerExecutable != release.ServerExecutable || verified.UpdaterExecutable != release.UpdaterExecutable || verified.BuildInfo != release.BuildInfo || verified.SHA256Sums != release.SHA256Sums || verified.GeoLiteUpdater != release.GeoLiteUpdater || verified.GeoLiteLicense != release.GeoLiteLicense {
		return ErrUpdateUnavailable
	}
	if err := ReplaceInstalledBinaries(o.Paths.InstallDir, o.Paths.ServerExecutable, o.Paths.UpdaterExecutable, release); err != nil {
		return ErrUpdateUnavailable
	}
	return nil
}

func (o *WindowsHostOperations) WaitReady(ctx context.Context, rawURL string) error {
	if rawURL == "" {
		rawURL = o.ReadyURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/readyz" {
		return ErrUpdateUnavailable
	}
	client := o.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		response, err := client.Do(request)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ErrUpdateUnavailable
		case <-deadline.C:
			return ErrUpdateUnavailable
		case <-ticker.C:
		}
	}
}

func (o *WindowsHostOperations) RestoreSnapshot(ctx context.Context, snapshot Snapshot) error {
	return RestoreSnapshot(ctx, o.Paths, snapshot)
}

func DefaultWindowsSnapshotPaths() (SnapshotPaths, error) {
	programFiles := os.Getenv("ProgramFiles")
	programData := os.Getenv("ProgramData")
	if programFiles == "" || programData == "" {
		return SnapshotPaths{}, errors.New("Windows installation paths are unavailable")
	}
	install := filepath.Join(programFiles, "BookmarksSync")
	data := filepath.Join(programData, "BookmarksSync")
	paths := SnapshotPaths{
		InstallDir:        install,
		DataDir:           filepath.Join(data, "data"),
		EnvFile:           filepath.Join(data, "bookmarksync.env"),
		RcloneConfig:      filepath.Join(data, "rclone", "rclone.conf"),
		SnapshotDir:       filepath.Join(data, "snapshots"),
		ServerExecutable:  filepath.Join(install, "bookmarksync.exe"),
		UpdaterExecutable: filepath.Join(install, "bookmarksync-updater.exe"),
	}
	if err := validateSnapshotPaths(paths); err != nil {
		return SnapshotPaths{}, ErrUpdateUnavailable
	}
	return paths, nil
}

func NewDefaultWindowsHostOperations(verifier ReleaseVerifier) (*WindowsHostOperations, error) {
	paths, err := DefaultWindowsSnapshotPaths()
	if err != nil {
		return nil, err
	}
	return &WindowsHostOperations{Paths: paths, Verifier: verifier, ReadyURL: "http://127.0.0.1:18080/readyz"}, nil
}

var _ HostOperations = (*WindowsHostOperations)(nil)
