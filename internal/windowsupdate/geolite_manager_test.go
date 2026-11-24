package windowsupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/geolite"
	"github.com/atikansari-ghr/BookmarksSync/internal/updateipc"
)

const geoLiteTestKey = "test_key_123"

func TestGeoLiteTaskDefinitionUsesSystemAndLocalWeeklySchedule(t *testing.T) {
	paths, err := GeoLitePathsForRoots(t.TempDir(), filepath.Join(t.TempDir(), "Program Files"))
	if err != nil {
		t.Fatal(err)
	}
	config := geolite.Config{Enabled: true, Days: []string{"MON", "THU"}, StartTime: "04:00"}
	definition, err := buildGeoLiteTaskXML(paths, config, time.Date(2026, 11, 24, 8, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	text := string(definition)
	for _, required := range []string{
		"<UserId>S-1-5-18</UserId>", "<LogonType>ServiceAccount</LogonType>", "<RunLevel>HighestAvailable</RunLevel>",
		"<StartWhenAvailable>true</StartWhenAvailable>", "<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>",
		"<ExecutionTimeLimit>PT1H</ExecutionTimeLimit>", "<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>",
		"<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>", "<StartBoundary>2026-11-24T04:00:00</StartBoundary>",
		"<Monday/>", "<Thursday/>", "<Arguments>geolite-run</Arguments>",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("task definition missing %q", required)
		}
	}
	if strings.Contains(text, "Z</StartBoundary>") || strings.Contains(text, "-04:00</StartBoundary>") || strings.Contains(text, geoLiteTestKey) {
		t.Fatal("task definition must use local time and contain no credentials")
	}
	if !strings.Contains(text, "bookmarksync-updater.exe</Command>") || strings.Contains(text, "geoipupdate.exe</Command>") {
		t.Fatalf("task must dispatch through the LocalSystem updater service, got %s", text)
	}
}

func TestGeoLiteConfigureStoresPairedCredentialsAndScheduleWithoutNetworkCall(t *testing.T) {
	manager, host, paths := newGeoLiteTestManager(t, "")
	config := testGeoLiteConfig(true)
	if err := manager.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if host.updaterRuns != 0 {
		t.Fatalf("Configure made %d MaxMind network calls", host.updaterRuns)
	}
	stored, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stored), "AccountID 12345\n") || !strings.Contains(string(stored), "LicenseKey "+geoLiteTestKey+"\n") || !strings.Contains(string(stored), "EditionIDs GeoLite2-Country\n") {
		t.Fatalf("credential file has unexpected contents: %q", stored)
	}
	if !host.taskRegistered || !strings.Contains(string(host.taskXML), "<Arguments>geolite-run</Arguments>") {
		t.Fatal("managed scheduled task was not registered")
	}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), geoLiteTestKey) || strings.Contains(string(body), "12345") {
		t.Fatalf("status returned a credential: %s", body)
	}
	if !status.Configured || !status.Enabled || strings.Join(status.Days, ",") != "MON,THU" || status.StartTime != "04:00" {
		t.Fatalf("status = %#v", status)
	}
}

func TestGeoLiteConfigurePreservesCredentialPairAndRejectsPartialRotation(t *testing.T) {
	manager, _, paths := newGeoLiteTestManager(t, "")
	if err := manager.Configure(context.Background(), testGeoLiteConfig(true)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	updated := geolite.Config{Enabled: true, Days: []string{"TUE", "FRI"}, StartTime: "08:30"}
	if err := manager.Configure(context.Background(), updated); err != nil {
		t.Fatalf("blank credential pair did not preserve stored pair: %v", err)
	}
	after, err := os.ReadFile(paths.ConfigFile)
	if err != nil || string(after) != string(before) {
		t.Fatalf("credential pair changed: err=%v", err)
	}
	updated.AccountID = "98765"
	if err := manager.Configure(context.Background(), updated); err == nil {
		t.Fatal("account ID rotation without a matching key succeeded")
	}
	after, err = os.ReadFile(paths.ConfigFile)
	if err != nil || string(after) != string(before) {
		t.Fatalf("partial rotation changed credentials: err=%v", err)
	}
}

func TestGeoLiteConfigureRollsBackWhenTaskRegistrationFails(t *testing.T) {
	manager, host, paths := newGeoLiteTestManager(t, "")
	host.fail["register"] = errors.New("credential path private failure")
	if err := manager.Configure(context.Background(), testGeoLiteConfig(true)); err == nil {
		t.Fatal("task registration failure was ignored")
	}
	if _, err := os.Stat(paths.ConfigFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed configure left credentials behind: %v", err)
	}
	if host.taskRegistered {
		t.Fatal("failed configure left a scheduled task registered")
	}
}

func TestGeoLiteClearRemovesCredentialsAndTaskButKeepsWorkingDatabase(t *testing.T) {
	manager, host, paths := newGeoLiteTestManager(t, filepath.Join("..", "network", "testdata", "GeoLite2-Country-Test.mmdb"))
	if err := manager.Configure(context.Background(), testGeoLiteConfig(true)); err != nil {
		t.Fatal(err)
	}
	if err := manager.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if host.taskRegistered {
		t.Fatal("clear left the scheduled task registered")
	}
	if _, err := os.Stat(paths.ConfigFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("clear left credentials behind: %v", err)
	}
	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Configured || status.Enabled || !status.DatabaseAvailable {
		t.Fatalf("clear status = %#v; the last valid database should remain available", status)
	}
}

func TestGeoLiteRunInstallsValidatedDatabaseThenRestartsAndCleansRecoveryFiles(t *testing.T) {
	manager, host, paths := newGeoLiteTestManager(t, "")
	if err := manager.Configure(context.Background(), testGeoLiteConfig(true)); err != nil {
		t.Fatal(err)
	}
	host.calls = nil
	result, err := manager.Run(context.Background())
	if err != nil || result != "updated" {
		t.Fatalf("Run = %q, %v", result, err)
	}
	if got := strings.Join(host.calls, ","); got != "download,install,restart" {
		t.Fatalf("run order = %s", got)
	}
	if _, err := os.Stat(paths.DatabaseFile); err != nil {
		t.Fatalf("active database missing: %v", err)
	}
	if _, err := os.Stat(paths.RecoveryFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery marker remains: %v", err)
	}
	if _, err := os.Stat(paths.BackupFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup remains: %v", err)
	}
}

func TestGeoLiteRunRejectsInvalidEditionWithoutChangingActiveDatabase(t *testing.T) {
	manager, host, paths := newGeoLiteTestManager(t, filepath.Join("..", "network", "testdata", "GeoLite2-Country-Test.mmdb"))
	if err := manager.Configure(context.Background(), testGeoLiteConfig(true)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.DatabaseFile)
	if err != nil {
		t.Fatal(err)
	}
	host.candidate = filepath.Join("..", "network", "testdata", "GeoIP2-Country-Test.mmdb")
	if _, err := manager.Run(context.Background()); err == nil {
		t.Fatal("wrong database edition was installed")
	}
	after, err := os.ReadFile(paths.DatabaseFile)
	if err != nil || string(after) != string(before) {
		t.Fatalf("active database changed: %v", err)
	}
	if strings.Contains(strings.Join(host.calls, ","), "install") {
		t.Fatalf("invalid candidate reached installation: %v", host.calls)
	}
}

func TestGeoLiteBuildTimeComparisonRejectsDowngrades(t *testing.T) {
	base := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	if got, err := compareGeoLiteBuildTimes(base.Add(-time.Second), base); !errors.Is(err, ErrGeoLiteDowngrade) || got != "" {
		t.Fatalf("older build = %q,%v", got, err)
	}
	if got, err := compareGeoLiteBuildTimes(base, base); err != nil || got != "unchanged" {
		t.Fatalf("equal build = %q,%v", got, err)
	}
	if got, err := compareGeoLiteBuildTimes(base.Add(time.Second), base); err != nil || got != "updated" {
		t.Fatalf("new build = %q,%v", got, err)
	}
}

func TestGeoLiteRunLeavesKnownGoodDatabaseWhenDownloadFails(t *testing.T) {
	manager, host, paths := newGeoLiteTestManager(t, filepath.Join("..", "network", "testdata", "GeoLite2-Country-Test.mmdb"))
	if err := manager.Configure(context.Background(), testGeoLiteConfig(true)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.DatabaseFile)
	if err != nil {
		t.Fatal(err)
	}
	host.fail["runUpdater"] = errors.New("secret updater output")
	_, runErr := manager.Run(context.Background())
	if runErr == nil {
		t.Fatal("download failure was hidden")
	}
	after, err := os.ReadFile(paths.DatabaseFile)
	if err != nil || string(after) != string(before) {
		t.Fatalf("download failure changed active DB: %v", err)
	}
	if strings.Contains(errString(runErr), "secret updater output") {
		t.Fatal("updater details escaped sanitized error")
	}
}

func TestGeoLiteRunPreservesRecoveryMarkerUntilRollbackAndRestartSucceed(t *testing.T) {
	manager, host, paths := newGeoLiteTestManager(t, "")
	if err := manager.Configure(context.Background(), testGeoLiteConfig(true)); err != nil {
		t.Fatal(err)
	}
	host.restartErrors = []error{errors.New("not ready"), errors.New("old service failed to restart")}
	if _, err := manager.Run(context.Background()); err == nil {
		t.Fatal("restart failure was hidden")
	}
	if _, err := os.Stat(paths.RecoveryFile); err != nil {
		t.Fatalf("failed recovery marker was lost: %v", err)
	}
	status, err := manager.Status(context.Background())
	if err != nil || !status.RecoveryRequired {
		t.Fatalf("status = %#v, %v", status, err)
	}
	result, err := manager.Run(context.Background())
	if err != nil || result != "updated" {
		t.Fatalf("retry = %q, %v", result, err)
	}
	if _, err := os.Stat(paths.RecoveryFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovery marker not cleared after recovery: %v", err)
	}
}

func TestSharedJobManagerSerializesReleaseConfigureAndGeoLiteRun(t *testing.T) {
	geoManager, host, _ := newGeoLiteTestManager(t, "")
	if err := geoManager.Configure(context.Background(), testGeoLiteConfig(true)); err != nil {
		t.Fatal(err)
	}
	startedRelease, releaseRelease := make(chan struct{}), make(chan struct{})
	jobs := NewJobManager(context.Background(), func(ctx context.Context, _ string) error {
		close(startedRelease)
		select {
		case <-releaseRelease:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	defer jobs.Close()
	jobs.SetGeoLiteManager(geoManager)
	releaseID, err := jobs.Submit(validTag)
	if err != nil {
		t.Fatal(err)
	}
	<-startedRelease
	if _, err := jobs.SubmitGeoLite(context.Background(), updateipc.GeoLiteRequest{Operation: updateipc.GeoLiteOperationRun}); !errors.Is(err, ErrJobBusy) {
		t.Fatalf("release/GeoLite race accepted: %v", err)
	}
	if _, err := jobs.SubmitGeoLite(context.Background(), updateipc.GeoLiteRequest{Operation: updateipc.GeoLiteOperationConfigure, Config: ptrGeoConfig(testGeoLiteConfig(true))}); !errors.Is(err, ErrJobBusy) {
		t.Fatalf("configure during release accepted: %v", err)
	}
	status, err := jobs.GeoLiteStatus(context.Background())
	if err != nil || !status.Configured {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	close(releaseRelease)
	if err := jobs.Wait(context.Background(), releaseID); err != nil {
		t.Fatal(err)
	}
	startedGeo, releaseGeo := make(chan struct{}), make(chan struct{})
	host.runStarted, host.runRelease = startedGeo, releaseGeo
	response, err := jobs.SubmitGeoLite(context.Background(), updateipc.GeoLiteRequest{Operation: updateipc.GeoLiteOperationRun})
	if err != nil || response.Job == "" {
		t.Fatalf("GeoLite queue=%#v err=%v", response, err)
	}
	<-startedGeo
	if _, err := jobs.Submit(validTag); !errors.Is(err, ErrJobBusy) {
		t.Fatalf("release accepted during GeoLite job: %v", err)
	}
	close(releaseGeo)
	if err := jobs.Wait(context.Background(), response.Job); err != nil {
		t.Fatal(err)
	}
}

func ptrGeoConfig(c geolite.Config) *geolite.Config { return &c }

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type fakeGeoLiteHost struct {
	taskRegistered bool
	taskXML        []byte
	candidate      string
	updaterRuns    int
	backupExists   bool
	restartErrors  []error
	fail           map[string]error
	calls          []string
	runStarted     chan struct{}
	runRelease     chan struct{}
}

func newGeoLiteTestManager(t *testing.T, activeFixture string) (*GeoLiteManager, *fakeGeoLiteHost, GeoLitePaths) {
	t.Helper()
	programData, programFiles := t.TempDir(), t.TempDir()
	paths, err := GeoLitePathsForRoots(programData, programFiles)
	if err != nil {
		t.Fatal(err)
	}
	host := &fakeGeoLiteHost{fail: make(map[string]error)}
	manager := NewGeoLiteManager(paths, host)
	if err := host.EnsureDirectories(paths); err != nil {
		t.Fatal(err)
	}
	if activeFixture != "" {
		active := filepath.Join("..", "network", "testdata", filepath.Base(activeFixture))
		data, err := os.ReadFile(active)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(paths.DatabaseFile, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	host.candidate = filepath.Join("..", "network", "testdata", "GeoLite2-Country-Test.mmdb")
	return manager, host, paths
}

func testGeoLiteConfig(enabled bool) geolite.Config {
	return geolite.Config{AccountID: "12345", LicenseKey: geoLiteTestKey, Enabled: enabled, Days: []string{"MON", "THU"}, StartTime: "04:00"}
}

func (h *fakeGeoLiteHost) EnsureDirectories(paths GeoLitePaths) error {
	for _, dir := range []string{paths.ConfigDir, paths.DatabaseDir, paths.StageDir, paths.BackupDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	return nil
}

func (h *fakeGeoLiteHost) WritePrivateFile(path string, data []byte) error {
	if err := h.fail["write"]; err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (h *fakeGeoLiteHost) RemovePrivateFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (h *fakeGeoLiteHost) TaskRegistered(context.Context, string) (bool, error) {
	return h.taskRegistered, nil
}

func (h *fakeGeoLiteHost) RegisterTask(_ context.Context, _ string, definition []byte) error {
	if err := h.fail["register"]; err != nil {
		return err
	}
	h.taskRegistered, h.taskXML = true, append([]byte(nil), definition...)
	h.calls = append(h.calls, "register")
	return nil
}

func (h *fakeGeoLiteHost) RemoveTask(context.Context, string) error {
	if err := h.fail["removeTask"]; err != nil {
		return err
	}
	h.taskRegistered = false
	h.calls = append(h.calls, "removeTask")
	return nil
}

func (h *fakeGeoLiteHost) VerifyUpdater(GeoLitePaths) error {
	return h.fail["verifyUpdater"]
}

func (h *fakeGeoLiteHost) RunUpdater(_ context.Context, paths GeoLitePaths) error {
	h.updaterRuns++
	h.calls = append(h.calls, "download")
	if h.runStarted != nil {
		close(h.runStarted)
		select {
		case <-h.runRelease:
		case <-time.After(5 * time.Second):
			return errors.New("test runner timed out")
		}
	}
	if err := h.fail["runUpdater"]; err != nil {
		return err
	}
	data, err := os.ReadFile(h.candidate)
	if err != nil {
		return err
	}
	return os.WriteFile(paths.StagedDatabase, data, 0600)
}

func (h *fakeGeoLiteHost) BackupDatabase(paths GeoLitePaths) (bool, error) {
	data, err := os.ReadFile(paths.DatabaseFile)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(paths.BackupFile, data, 0600); err != nil {
		return false, err
	}
	h.backupExists = true
	h.calls = append(h.calls, "backup")
	return true, nil
}

func (h *fakeGeoLiteHost) InstallDatabase(paths GeoLitePaths, candidate string) error {
	if err := h.fail["install"]; err != nil {
		return err
	}
	h.calls = append(h.calls, "install")
	return os.Rename(candidate, paths.DatabaseFile)
}

func (h *fakeGeoLiteHost) RestoreDatabase(paths GeoLitePaths, hadPrevious bool) error {
	if err := h.fail["restore"]; err != nil {
		return err
	}
	h.calls = append(h.calls, "restore")
	if !hadPrevious {
		err := os.Remove(paths.DatabaseFile)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	err := os.Rename(paths.BackupFile, paths.DatabaseFile)
	if err == nil {
		h.backupExists = false
	}
	return err
}

func (h *fakeGeoLiteHost) RemoveBackup(paths GeoLitePaths) error {
	if err := os.Remove(paths.BackupFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	h.backupExists = false
	return nil
}

func (h *fakeGeoLiteHost) RemoveStage(paths GeoLitePaths) error {
	err := os.RemoveAll(paths.StageDir)
	if err != nil {
		return err
	}
	return os.MkdirAll(paths.StageDir, 0700)
}

func (h *fakeGeoLiteHost) RestartApplication(context.Context) error {
	h.calls = append(h.calls, "restart")
	if len(h.restartErrors) == 0 {
		return nil
	}
	err := h.restartErrors[0]
	h.restartErrors = h.restartErrors[1:]
	return err
}
