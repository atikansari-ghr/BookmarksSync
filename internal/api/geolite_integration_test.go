package api_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atikansari-ghr/BookmarksSync/internal/windowsupdate"
)

func TestAdminGeoLiteFlowThroughAPIAndUpdaterManager(t *testing.T) {
	programData, programFiles := t.TempDir(), t.TempDir()
	paths, err := windowsupdate.GeoLitePathsForRoots(programData, programFiles)
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join("..", "network", "testdata", "GeoLite2-Country-Test.mmdb")
	host := &apiGeoLiteHost{fixture: fixture}
	manager := windowsupdate.NewGeoLiteManager(paths, host)
	e := newTEnvWithGeoLite(t, manager)
	root := e.login(e.root)
	const accountID, licenseKey = "12345", "private_key_123"
	config := map[string]any{"accountId": accountID, "licenseKey": licenseKey, "enabled": true, "days": []string{"MON", "THU"}, "startTime": "04:00"}

	configured := e.do("PUT", "/api/v1/admin/geolite", config, root.mods()...)
	if configured.Code != 200 || containsAny(configured.Body.String(), accountID, licenseKey) {
		t.Fatalf("configure response = %d %s", configured.Code, configured.Body.String())
	}
	if host.updaterRuns != 0 || !host.taskRegistered {
		t.Fatalf("configure ran an update or failed to register schedule: runs=%d registered=%v", host.updaterRuns, host.taskRegistered)
	}
	stored, err := os.ReadFile(paths.ConfigFile)
	if err != nil || !containsAny(string(stored), accountID, licenseKey) {
		t.Fatalf("paired credentials not stored by updater: err=%v", err)
	}

	run := e.do("POST", "/api/v1/admin/geolite/run", nil, root.mods()...)
	if run.Code != 202 || !containsAny(run.Body.String(), `"job":"updated"`) || host.updaterRuns != 1 || host.restarts != 1 {
		t.Fatalf("run response=%d %s updater-runs=%d restarts=%d", run.Code, run.Body.String(), host.updaterRuns, host.restarts)
	}
	status := e.do("GET", "/api/v1/admin/geolite", nil, root.mods()...)
	if status.Code != 200 || !containsAny(status.Body.String(), `"databaseAvailable":true`, `"lastResult":"updated"`) || containsAny(status.Body.String(), accountID, licenseKey) {
		t.Fatalf("post-run status=%d %s", status.Code, status.Body.String())
	}
	if _, err := os.Stat(paths.DatabaseFile); err != nil {
		t.Fatalf("active fixture database missing: %v", err)
	}

	cleared := e.do("DELETE", "/api/v1/admin/geolite", nil, root.mods()...)
	if cleared.Code != 200 || host.taskRegistered {
		t.Fatalf("clear response=%d %s scheduled=%v", cleared.Code, cleared.Body.String(), host.taskRegistered)
	}
	if _, err := os.Stat(paths.ConfigFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clear left updater credentials: %v", err)
	}
	if _, err := os.Stat(paths.DatabaseFile); err != nil {
		t.Fatalf("clear removed the active database: %v", err)
	}
	activity := e.do("GET", "/api/v1/admin/activity", nil, root.mods()...)
	if activity.Code != 200 || containsAny(activity.Body.String(), accountID, licenseKey) {
		t.Fatalf("activity response leaked GeoLite credentials: %d %s", activity.Code, activity.Body.String())
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if len(needle) != 0 && strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

type apiGeoLiteHost struct {
	fixture        string
	taskRegistered bool
	updaterRuns    int
	restarts       int
}

func (h *apiGeoLiteHost) EnsureDirectories(p windowsupdate.GeoLitePaths) error {
	for _, dir := range []string{p.ConfigDir, p.DatabaseDir, p.StageDir, p.BackupDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	return nil
}
func (h *apiGeoLiteHost) WritePrivateFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
func (*apiGeoLiteHost) RemovePrivateFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (h *apiGeoLiteHost) TaskRegistered(context.Context, string) (bool, error) {
	return h.taskRegistered, nil
}
func (h *apiGeoLiteHost) RegisterTask(context.Context, string, []byte) error {
	h.taskRegistered = true
	return nil
}
func (h *apiGeoLiteHost) RemoveTask(context.Context, string) error {
	h.taskRegistered = false
	return nil
}
func (*apiGeoLiteHost) VerifyUpdater(windowsupdate.GeoLitePaths) error { return nil }
func (h *apiGeoLiteHost) RunUpdater(_ context.Context, p windowsupdate.GeoLitePaths) error {
	h.updaterRuns++
	return copyGeoLiteFixture(h.fixture, p.StagedDatabase)
}
func (*apiGeoLiteHost) BackupDatabase(p windowsupdate.GeoLitePaths) (bool, error) {
	if _, err := os.Stat(p.DatabaseFile); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, copyGeoLiteFixture(p.DatabaseFile, p.BackupFile)
}
func (*apiGeoLiteHost) InstallDatabase(p windowsupdate.GeoLitePaths, candidate string) error {
	if err := os.Remove(p.DatabaseFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(candidate, p.DatabaseFile)
}
func (*apiGeoLiteHost) RestoreDatabase(p windowsupdate.GeoLitePaths, hadPrevious bool) error {
	if !hadPrevious {
		return (&apiGeoLiteHost{}).RemovePrivateFile(p.DatabaseFile)
	}
	if err := os.Remove(p.DatabaseFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(p.BackupFile, p.DatabaseFile)
}
func (*apiGeoLiteHost) RemoveBackup(p windowsupdate.GeoLitePaths) error {
	return (&apiGeoLiteHost{}).RemovePrivateFile(p.BackupFile)
}
func (*apiGeoLiteHost) RemoveStage(p windowsupdate.GeoLitePaths) error {
	return os.RemoveAll(p.StageDir)
}
func (h *apiGeoLiteHost) RestartApplication(context.Context) error {
	h.restarts++
	return nil
}

func copyGeoLiteFixture(source, destination string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	return os.WriteFile(destination, data, 0600)
}

var _ windowsupdate.GeoLiteHost = (*apiGeoLiteHost)(nil)
