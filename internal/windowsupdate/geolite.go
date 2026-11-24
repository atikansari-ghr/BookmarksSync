package windowsupdate

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/geolite"
	"github.com/atikansari-ghr/BookmarksSync/internal/network"
)

const GeoLiteTaskName = `\BookmarksSync\GeoLiteUpdate`

var ErrGeoLiteUnavailable = errors.New("GeoLite update unavailable")
var ErrGeoLiteDowngrade = errors.New("GeoLite database build time would move backwards")

type GeoLitePaths struct {
	ConfigDir, ConfigFile, DatabaseDir, DatabaseFile string
	StageDir, StagedDatabase, BackupDir, BackupFile  string
	UpdaterFile                                      string
	SettingsFile                                     string
	UpdaterManifest                                  string
	RecoveryFile                                     string
	DispatcherFile                                   string
}

func GeoLitePathsForRoots(programData, programFiles string) (GeoLitePaths, error) {
	if !filepath.IsAbs(programData) || !filepath.IsAbs(programFiles) {
		return GeoLitePaths{}, ErrGeoLiteUnavailable
	}
	data := filepath.Join(programData, "BookmarksSync", "geolite")
	install := filepath.Join(programFiles, "BookmarksSync")
	p := GeoLitePaths{ConfigDir: filepath.Join(data, "config"), ConfigFile: filepath.Join(data, "config", "GeoIP.conf"), DatabaseDir: filepath.Join(data, "database"), DatabaseFile: filepath.Join(data, "database", "GeoLite2-Country.mmdb"), StageDir: filepath.Join(data, "stage"), StagedDatabase: filepath.Join(data, "stage", "GeoLite2-Country.mmdb"), BackupDir: filepath.Join(data, "backup"), BackupFile: filepath.Join(data, "backup", "GeoLite2-Country.mmdb"), UpdaterFile: filepath.Join(install, "geoipupdate.exe"), SettingsFile: filepath.Join(data, "config", "settings.json"), UpdaterManifest: filepath.Join(install, "SHA256SUMS"), RecoveryFile: filepath.Join(data, "backup", "recovery-required"), DispatcherFile: filepath.Join(install, "bookmarksync-updater.exe")}
	return p, nil
}

type GeoLiteHost interface {
	EnsureDirectories(GeoLitePaths) error
	WritePrivateFile(string, []byte) error
	RemovePrivateFile(string) error
	TaskRegistered(context.Context, string) (bool, error)
	RegisterTask(context.Context, string, []byte) error
	RemoveTask(context.Context, string) error
	VerifyUpdater(GeoLitePaths) error
	RunUpdater(context.Context, GeoLitePaths) error
	BackupDatabase(GeoLitePaths) (bool, error)
	InstallDatabase(GeoLitePaths, string) error
	RestoreDatabase(GeoLitePaths, bool) error
	RemoveBackup(GeoLitePaths) error
	RemoveStage(GeoLitePaths) error
	RestartApplication(context.Context) error
}

type GeoLiteManager struct {
	paths   GeoLitePaths
	host    GeoLiteHost
	mu      sync.Mutex
	running bool
}

type geoLiteSettings struct {
	Days          []string   `json:"days"`
	StartTime     string     `json:"startTime"`
	Enabled       bool       `json:"enabled"`
	LastAttempt   *time.Time `json:"lastAttempt,omitempty"`
	LastResult    string     `json:"lastResult,omitempty"`
	LastErrorCode string     `json:"lastErrorCode,omitempty"`
}

func NewGeoLiteManager(paths GeoLitePaths, host GeoLiteHost) *GeoLiteManager {
	return &GeoLiteManager{paths: paths, host: host}
}

func (m *GeoLiteManager) Configure(ctx context.Context, requested geolite.Config) error {
	if m == nil || m.host == nil || geolite.ValidateConfig(requested) != nil {
		return geolite.ErrInvalidConfig
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return ErrJobBusy
	}
	if err := m.host.EnsureDirectories(m.paths); err != nil {
		return ErrGeoLiteUnavailable
	}
	old, readErr := readGeoLitePrivateFile(m.paths.ConfigFile, 4096)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return ErrGeoLiteUnavailable
	}
	stored, parseErr := decodeGeoLiteConfig(old)
	if parseErr != nil && readErr == nil {
		return ErrGeoLiteUnavailable
	}
	if requested.AccountID == "" && requested.LicenseKey == "" {
		if stored.AccountID == "" || stored.LicenseKey == "" {
			return geolite.ErrInvalidConfig
		}
		requested.AccountID, requested.LicenseKey = stored.AccountID, stored.LicenseKey
	}
	if requested.AccountID == "" || requested.LicenseKey == "" {
		return geolite.ErrInvalidConfig
	}
	content := []byte(fmt.Sprintf("AccountID %s\nLicenseKey %s\nEditionIDs GeoLite2-Country\n", requested.AccountID, requested.LicenseKey))
	if err := m.host.WritePrivateFile(m.paths.ConfigFile, content); err != nil {
		return ErrGeoLiteUnavailable
	}
	oldSettings, settingsErr := readGeoLitePrivateFile(m.paths.SettingsFile, 4096)
	if settingsErr != nil && !errors.Is(settingsErr, os.ErrNotExist) {
		_ = restoreGeoLiteConfig(m.host, m.paths.ConfigFile, old, readErr)
		return ErrGeoLiteUnavailable
	}
	settings := geoLiteSettings{Days: requested.Days, StartTime: requested.StartTime, Enabled: requested.Enabled}
	var previous geoLiteSettings
	_ = json.Unmarshal(oldSettings, &previous)
	settings.LastAttempt, settings.LastResult, settings.LastErrorCode = previous.LastAttempt, previous.LastResult, previous.LastErrorCode
	settingsBytes, _ := json.Marshal(settings)
	if err := m.host.WritePrivateFile(m.paths.SettingsFile, settingsBytes); err != nil {
		_ = restoreGeoLiteConfig(m.host, m.paths.ConfigFile, old, readErr)
		return ErrGeoLiteUnavailable
	}
	if requested.Enabled {
		xml, err := buildGeoLiteTaskXML(m.paths, requested, time.Now())
		if err != nil {
			_ = restoreGeoLiteConfig(m.host, m.paths.ConfigFile, old, readErr)
			_ = restoreGeoLiteConfig(m.host, m.paths.SettingsFile, oldSettings, settingsErr)
			return ErrGeoLiteUnavailable
		}
		if err = m.host.RegisterTask(ctx, GeoLiteTaskName, xml); err != nil {
			_ = restoreGeoLiteConfig(m.host, m.paths.ConfigFile, old, readErr)
			_ = restoreGeoLiteConfig(m.host, m.paths.SettingsFile, oldSettings, settingsErr)
			return ErrGeoLiteUnavailable
		}
	} else if err := m.host.RemoveTask(ctx, GeoLiteTaskName); err != nil {
		_ = restoreGeoLiteConfig(m.host, m.paths.ConfigFile, old, readErr)
		_ = restoreGeoLiteConfig(m.host, m.paths.SettingsFile, oldSettings, settingsErr)
		return ErrGeoLiteUnavailable
	}
	return nil
}

func restoreGeoLiteConfig(host GeoLiteHost, path string, old []byte, readErr error) error {
	if errors.Is(readErr, os.ErrNotExist) {
		return host.RemovePrivateFile(path)
	}
	return host.WritePrivateFile(path, old)
}

func decodeGeoLiteConfig(raw []byte) (geolite.Config, error) {
	var c geolite.Config
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		key, value, ok := strings.Cut(line, " ")
		if !ok || value == "" || seen[key] {
			return geolite.Config{}, ErrGeoLiteUnavailable
		}
		seen[key] = true
		switch key {
		case "AccountID":
			c.AccountID = value
		case "LicenseKey":
			c.LicenseKey = value
		case "EditionIDs":
			if value != "GeoLite2-Country" {
				return geolite.Config{}, ErrGeoLiteUnavailable
			}
		default:
			return geolite.Config{}, ErrGeoLiteUnavailable
		}
	}
	if len(seen) != 3 || c.AccountID == "" || c.LicenseKey == "" {
		return geolite.Config{}, ErrGeoLiteUnavailable
	}
	return c, nil
}

func readGeoLitePrivateFile(path string, max int64) ([]byte, error) {
	if rejectReparseAncestors(path) != nil {
		return nil, ErrGeoLiteUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 || info.Size() > max {
		return nil, ErrGeoLiteUnavailable
	}
	return os.ReadFile(path)
}

func (m *GeoLiteManager) Clear(ctx context.Context) error {
	if m == nil || m.host == nil {
		return ErrGeoLiteUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return ErrJobBusy
	}
	if err := m.host.RemoveTask(ctx, GeoLiteTaskName); err != nil {
		return ErrGeoLiteUnavailable
	}
	if err := m.host.RemovePrivateFile(m.paths.ConfigFile); err != nil {
		return ErrGeoLiteUnavailable
	}
	if err := m.host.RemovePrivateFile(m.paths.SettingsFile); err != nil {
		return ErrGeoLiteUnavailable
	}
	return nil
}

func (m *GeoLiteManager) Status(ctx context.Context) (geolite.Status, error) {
	if m == nil || m.host == nil {
		return geolite.Status{}, ErrGeoLiteUnavailable
	}
	cfgRaw, err := readGeoLitePrivateFile(m.paths.ConfigFile, 4096)
	configured := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return geolite.Status{}, ErrGeoLiteUnavailable
	}
	cfg, _ := decodeGeoLiteConfig(cfgRaw)
	var saved geoLiteSettings
	if raw, e := readGeoLitePrivateFile(m.paths.SettingsFile, 4096); e == nil {
		_ = json.Unmarshal(raw, &saved)
	}
	registered, taskErr := m.host.TaskRegistered(ctx, GeoLiteTaskName)
	if taskErr != nil {
		return geolite.Status{}, ErrGeoLiteUnavailable
	}
	status := geolite.Status{Configured: configured && cfg.AccountID != "" && cfg.LicenseKey != "", Enabled: registered, Days: saved.Days, StartTime: saved.StartTime, LastAttempt: saved.LastAttempt, LastResult: saved.LastResult, LastErrorCode: saved.LastErrorCode}
	if status.Days == nil {
		d := geolite.DefaultConfig()
		status.Days = d.Days
		status.StartTime = d.StartTime
	}
	if info, e := os.Lstat(m.paths.DatabaseFile); e == nil && info.Mode().IsRegular() {
		db, e := network.OpenGeoLiteCountryDatabase(m.paths.DatabaseFile)
		if e == nil {
			status.DatabaseAvailable = true
			t := db.BuildTime()
			status.DatabaseBuildTime = &t
			_ = db.Close()
		}
	}
	if _, e := os.Lstat(m.paths.RecoveryFile); e == nil {
		status.RecoveryRequired = true
	}
	m.mu.Lock()
	status.Running = m.running
	m.mu.Unlock()
	return status, nil
}

func (m *GeoLiteManager) Run(ctx context.Context) (result string, runErr error) {
	if m == nil || m.host == nil {
		return "", ErrGeoLiteUnavailable
	}
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return "", ErrJobBusy
	}
	m.running = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.running = false; m.mu.Unlock() }()
	attempt := time.Now().UTC()
	defer func() {
		if result == "" {
			result = "failed"
		}
		m.recordResult(attempt, result, runErr)
	}()
	credentialBytes, credentialErr := readGeoLitePrivateFile(m.paths.ConfigFile, 4096)
	if credentialErr != nil {
		return "failed", ErrGeoLiteUnavailable
	}
	credentials, decodeErr := decodeGeoLiteConfig(credentialBytes)
	if decodeErr != nil || credentials.AccountID == "" || credentials.LicenseKey == "" {
		return "failed", ErrGeoLiteUnavailable
	}
	if _, err := os.Lstat(m.paths.RecoveryFile); err == nil {
		if err := m.resumeRecovery(ctx); err != nil {
			return "recovery-required", ErrGeoLiteUnavailable
		}
	}
	if err := m.host.VerifyUpdater(m.paths); err != nil {
		return "", ErrGeoLiteUnavailable
	}
	if err := m.host.RemoveStage(m.paths); err != nil {
		return "", ErrGeoLiteUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	if err := m.host.RunUpdater(ctx, m.paths); err != nil {
		return "", ErrGeoLiteUnavailable
	}
	candidate, err := network.OpenGeoLiteCountryDatabase(m.paths.StagedDatabase)
	if err != nil {
		return "", ErrGeoLiteUnavailable
	}
	newBuild := candidate.BuildTime()
	_ = candidate.Close()
	if current, e := network.OpenGeoLiteCountryDatabase(m.paths.DatabaseFile); e == nil {
		oldBuild := current.BuildTime()
		_ = current.Close()
		disposition, compareErr := compareGeoLiteBuildTimes(newBuild, oldBuild)
		if compareErr != nil {
			return "failed", ErrGeoLiteUnavailable
		}
		if disposition == "unchanged" {
			return "unchanged", nil
		}
	}
	hadPrevious, err := m.host.BackupDatabase(m.paths)
	if err != nil {
		return "", ErrGeoLiteUnavailable
	}
	marker := []byte("previous\n")
	if !hadPrevious {
		marker = []byte("empty\n")
	}
	if err = m.host.WritePrivateFile(m.paths.RecoveryFile, marker); err != nil {
		_ = m.host.RemoveBackup(m.paths)
		return "", ErrGeoLiteUnavailable
	}
	if err = m.host.InstallDatabase(m.paths, m.paths.StagedDatabase); err != nil {
		if m.host.RestoreDatabase(m.paths, hadPrevious) == nil {
			_ = m.host.RemovePrivateFile(m.paths.RecoveryFile)
			_ = m.host.RemoveBackup(m.paths)
		}
		return "", ErrGeoLiteUnavailable
	}
	if err = m.host.RestartApplication(ctx); err != nil {
		rollback := m.host.RestoreDatabase(m.paths, hadPrevious)
		restart := m.host.RestartApplication(ctx)
		if rollback != nil || restart != nil {
			return "recovery-required", ErrGeoLiteUnavailable
		}
		_ = m.host.RemoveBackup(m.paths)
		_ = m.host.RemovePrivateFile(m.paths.RecoveryFile)
		return "rolled-back", ErrGeoLiteUnavailable
	}
	if err = m.host.RemoveBackup(m.paths); err != nil {
		return "recovery-required", ErrGeoLiteUnavailable
	}
	if err = m.host.RemovePrivateFile(m.paths.RecoveryFile); err != nil {
		return "recovery-required", ErrGeoLiteUnavailable
	}
	_ = m.host.RemoveStage(m.paths)
	return "updated", nil
}

func compareGeoLiteBuildTimes(candidate, current time.Time) (string, error) {
	if candidate.Before(current) {
		return "", ErrGeoLiteDowngrade
	}
	if candidate.Equal(current) {
		return "unchanged", nil
	}
	return "updated", nil
}

func (m *GeoLiteManager) recordResult(at time.Time, result string, err error) {
	var settings geoLiteSettings
	if raw, e := readGeoLitePrivateFile(m.paths.SettingsFile, 4096); e == nil {
		_ = json.Unmarshal(raw, &settings)
	}
	settings.LastAttempt = &at
	settings.LastResult = result
	settings.LastErrorCode = ""
	if err != nil {
		if result == "recovery-required" {
			settings.LastErrorCode = "recovery_required"
		} else {
			settings.LastErrorCode = "update_failed"
		}
	}
	raw, e := json.Marshal(settings)
	if e == nil {
		_ = m.host.WritePrivateFile(m.paths.SettingsFile, raw)
	}
}

func (m *GeoLiteManager) resumeRecovery(ctx context.Context) error {
	marker, err := readGeoLitePrivateFile(m.paths.RecoveryFile, 16)
	if err != nil {
		return err
	}
	hadPrevious := strings.TrimSpace(string(marker)) == "previous"
	if !hadPrevious && strings.TrimSpace(string(marker)) != "empty" {
		return ErrGeoLiteUnavailable
	}
	if err = m.host.RestoreDatabase(m.paths, hadPrevious); err != nil {
		return err
	}
	if err = m.host.RestartApplication(ctx); err != nil {
		return err
	}
	if err = m.host.RemoveBackup(m.paths); err != nil {
		return err
	}
	return m.host.RemovePrivateFile(m.paths.RecoveryFile)
}

func (m *GeoLiteManager) RunScheduled(ctx context.Context) error {
	var settings geoLiteSettings
	raw, err := readGeoLitePrivateFile(m.paths.SettingsFile, 4096)
	if err != nil {
		return ErrGeoLiteUnavailable
	}
	if json.Unmarshal(raw, &settings) != nil || !settings.Enabled {
		return ErrGeoLiteUnavailable
	}
	_, err = m.Run(ctx)
	return err
}

func buildGeoLiteTaskXML(paths GeoLitePaths, config geolite.Config, now time.Time) ([]byte, error) {
	if geolite.ValidateConfig(geolite.Config{Enabled: config.Enabled, Days: config.Days, StartTime: config.StartTime}) != nil || !filepath.IsAbs(paths.DispatcherFile) {
		return nil, ErrGeoLiteUnavailable
	}
	parts := strings.Split(config.StartTime, ":")
	hour, minute := parts[0], parts[1]
	boundary := now.Format("2006-01-02T") + hour + ":" + minute + ":00"
	type trigger struct {
		Start    string `xml:"StartBoundary"`
		Enabled  bool   `xml:"Enabled"`
		Schedule struct {
			Days []string `xml:"Monday,omitempty"`
		} `xml:"ScheduleByWeek"`
	}
	var days strings.Builder
	for _, day := range config.Days {
		tag := map[string]string{"MON": "Monday", "TUE": "Tuesday", "WED": "Wednesday", "THU": "Thursday", "FRI": "Friday", "SAT": "Saturday", "SUN": "Sunday"}[day]
		days.WriteString("<" + tag + "/>")
	}
	var command strings.Builder
	if err := xml.EscapeText(&command, []byte(paths.DispatcherFile)); err != nil {
		return nil, ErrGeoLiteUnavailable
	}
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><Triggers><CalendarTrigger><StartBoundary>%s</StartBoundary><Enabled>true</Enabled><ScheduleByWeek><DaysOfWeek>%s</DaysOfWeek><WeeksInterval>1</WeeksInterval></ScheduleByWeek></CalendarTrigger></Triggers><Principals><Principal id="Author"><UserId>S-1-5-18</UserId><LogonType>ServiceAccount</LogonType><RunLevel>HighestAvailable</RunLevel></Principal></Principals><Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><StartWhenAvailable>true</StartWhenAvailable><ExecutionTimeLimit>PT1H</ExecutionTimeLimit><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries></Settings><Actions Context="Author"><Exec><Command>%s</Command><Arguments>geolite-run</Arguments></Exec></Actions></Task>`, boundary, days.String(), command.String())
	return []byte(body), nil
}
