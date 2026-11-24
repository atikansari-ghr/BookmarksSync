//go:build windows

package windowsupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/atikansari-ghr/BookmarksSync/internal/network"
	"golang.org/x/sys/windows"
)

const geoipUpdateVersion = "8.0.0"

type WindowsGeoLiteHost struct{ ApplicationServiceSID string }

func DefaultGeoLitePaths() (GeoLitePaths, error) {
	p, err := GeoLitePathsForRoots(os.Getenv("ProgramData"), os.Getenv("ProgramFiles"))
	if err != nil {
		return GeoLitePaths{}, err
	}
	p.UpdaterManifest = filepath.Join(filepath.Dir(p.UpdaterFile), ChecksumsAsset)
	return p, nil
}

func (h *WindowsGeoLiteHost) EnsureDirectories(p GeoLitePaths) error {
	for _, dir := range []string{filepath.Dir(p.ConfigDir), p.ConfigDir, p.DatabaseDir, p.StageDir, p.BackupDir} {
		if err := ensureSystemAdminDir(dir); err != nil {
			return ErrGeoLiteUnavailable
		}
	}
	if err := setGeoLiteDirectoryACL(filepath.Dir(p.ConfigDir), h.ApplicationServiceSID); err != nil {
		return ErrGeoLiteUnavailable
	}
	if err := setGeoLiteDirectoryACL(p.DatabaseDir, h.ApplicationServiceSID); err != nil {
		return ErrGeoLiteUnavailable
	}
	return nil
}

func ensureSystemAdminDir(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrGeoLiteUnavailable
	}
	return setGeoLiteACL(path, false, "")
}

func setGeoLiteACL(path string, appRead bool, appSID string) error {
	sddl := "O:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if appRead {
		if appSID == "" || strings.ContainsAny(appSID, "\r\n;") {
			return ErrGeoLiteUnavailable
		}
		sddl += "(A;;GR;;;" + appSID + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION | windows.OWNER_SECURITY_INFORMATION)
	owner, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, owner, nil, dacl, nil)
}

func setGeoLiteDirectoryACL(path, appSID string) error {
	if appSID == "" || strings.ContainsAny(appSID, "\r\n;") {
		return ErrGeoLiteUnavailable
	}
	sd, err := windows.SecurityDescriptorFromString("O:SYD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;;X;;;" + appSID + ")")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	owner, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION | windows.OWNER_SECURITY_INFORMATION)
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, owner, nil, dacl, nil)
}

func (h *WindowsGeoLiteHost) WritePrivateFile(path string, data []byte) error {
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || setGeoLiteACL(parent, false, "") != nil {
		return ErrGeoLiteUnavailable
	}
	tmp, err := os.CreateTemp(parent, ".geolite-secret-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := setGeoLiteACL(name, false, ""); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	from, _ := windows.UTF16PtrFromString(name)
	to, _ := windows.UTF16PtrFromString(path)
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return err
	}
	return setGeoLiteACL(path, false, "")
}

func (*WindowsGeoLiteHost) RemovePrivateFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func taskCommand() string { return filepath.Join(os.Getenv("SystemRoot"), "System32", "schtasks.exe") }
func (*WindowsGeoLiteHost) TaskRegistered(ctx context.Context, name string) (bool, error) {
	out, err := exec.CommandContext(ctx, taskCommand(), "/Query", "/TN", name).CombinedOutput()
	if err == nil {
		return true, nil
	}
	if strings.Contains(strings.ToLower(string(out)), "cannot find") || strings.Contains(strings.ToLower(string(out)), "does not exist") {
		return false, nil
	}
	return false, ErrGeoLiteUnavailable
}
func (*WindowsGeoLiteHost) RegisterTask(ctx context.Context, name string, definition []byte) error {
	return withTaskXML(definition, func(path string) error { return runSchtasks(ctx, "/Create", "/F", "/TN", name, "/XML", path) })
}
func (*WindowsGeoLiteHost) RemoveTask(ctx context.Context, name string) error {
	if err := runSchtasks(ctx, "/Delete", "/F", "/TN", name); err != nil {
		exists, e := (&WindowsGeoLiteHost{}).TaskRegistered(ctx, name)
		if e == nil && !exists {
			return nil
		}
		return err
	}
	return nil
}
func runSchtasks(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, taskCommand(), args...)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if cmd.Run() != nil {
		return ErrGeoLiteUnavailable
	}
	return nil
}
func withTaskXML(data []byte, fn func(string) error) error {
	f, err := os.CreateTemp(os.TempDir(), "bookmarksync-geolite-task-*.xml")
	if err != nil {
		return ErrGeoLiteUnavailable
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return ErrGeoLiteUnavailable
	}
	if err = f.Close(); err != nil {
		return ErrGeoLiteUnavailable
	}
	return fn(path)
}

func (*WindowsGeoLiteHost) VerifyUpdater(p GeoLitePaths) error {
	if p.UpdaterManifest == "" {
		return ErrGeoLiteUnavailable
	}
	manifestInfo, err := os.Lstat(p.UpdaterManifest)
	if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Size() < 1 || manifestInfo.Size() > 4096 || manifestInfo.Mode()&os.ModeSymlink != 0 || rejectReparseAncestors(p.UpdaterManifest) != nil {
		return ErrGeoLiteUnavailable
	}
	manifest, err := os.ReadFile(p.UpdaterManifest)
	if err != nil {
		return ErrGeoLiteUnavailable
	}
	fields := strings.Fields(string(manifest))
	if len(fields) != 2 || len(fields[0]) != 64 || fields[1] != WindowsGeoLiteAsset {
		return ErrGeoLiteUnavailable
	}
	info, err := os.Lstat(p.UpdaterFile)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 128<<20 || info.Mode()&os.ModeSymlink != 0 {
		return ErrGeoLiteUnavailable
	}
	f, err := os.Open(p.UpdaterFile)
	if err != nil {
		return ErrGeoLiteUnavailable
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(f, 128<<20+1)); err != nil {
		return ErrGeoLiteUnavailable
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(fields[0]) {
		return ErrGeoLiteUnavailable
	}
	return nil
}

func (*WindowsGeoLiteHost) RunUpdater(ctx context.Context, p GeoLitePaths) error {
	if err := rejectReparseAncestors(p.ConfigFile); err != nil {
		return ErrGeoLiteUnavailable
	}
	cmd := exec.CommandContext(ctx, p.UpdaterFile, "-f", p.ConfigFile, "-d", p.StageDir)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if cmd.Run() != nil {
		return ErrGeoLiteUnavailable
	}
	return nil
}

func (h *WindowsGeoLiteHost) BackupDatabase(p GeoLitePaths) (bool, error) {
	if rejectReparseAncestors(p.DatabaseFile) != nil {
		return false, ErrGeoLiteUnavailable
	}
	source, err := os.Open(p.DatabaseFile)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, ErrGeoLiteUnavailable
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 64<<20 {
		return false, ErrGeoLiteUnavailable
	}
	tmp, err := os.CreateTemp(p.BackupDir, ".backup-*")
	if err != nil {
		return false, ErrGeoLiteUnavailable
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := setGeoLiteACL(name, false, ""); err != nil {
		tmp.Close()
		return false, ErrGeoLiteUnavailable
	}
	if _, err = io.Copy(tmp, source); err != nil {
		tmp.Close()
		return false, ErrGeoLiteUnavailable
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return false, ErrGeoLiteUnavailable
	}
	if err = tmp.Close(); err != nil {
		return false, ErrGeoLiteUnavailable
	}
	from, _ := windows.UTF16PtrFromString(name)
	to, _ := windows.UTF16PtrFromString(p.BackupFile)
	if windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH) != nil {
		return false, ErrGeoLiteUnavailable
	}
	return true, nil
}

func (h *WindowsGeoLiteHost) InstallDatabase(p GeoLitePaths, candidate string) error {
	if candidate != p.StagedDatabase {
		return ErrGeoLiteUnavailable
	}
	if rejectReparseAncestors(p.DatabaseFile) != nil {
		return ErrGeoLiteUnavailable
	}
	if info, err := os.Lstat(p.DatabaseFile); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return ErrGeoLiteUnavailable
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrGeoLiteUnavailable
	}
	db, err := network.OpenGeoLiteCountryDatabase(candidate)
	if err != nil {
		return ErrGeoLiteUnavailable
	}
	_ = db.Close()
	from, _ := windows.UTF16PtrFromString(candidate)
	to, _ := windows.UTF16PtrFromString(p.DatabaseFile)
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return ErrGeoLiteUnavailable
	}
	if err := setGeoLiteACL(p.DatabaseFile, true, h.ApplicationServiceSID); err != nil {
		return ErrGeoLiteUnavailable
	}
	return nil
}

func (h *WindowsGeoLiteHost) RestoreDatabase(p GeoLitePaths, hadPrevious bool) error {
	if !hadPrevious {
		err := os.Remove(p.DatabaseFile)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if rejectReparseAncestors(p.BackupFile) != nil {
		return ErrGeoLiteUnavailable
	}
	from, _ := windows.UTF16PtrFromString(p.BackupFile)
	to, _ := windows.UTF16PtrFromString(p.DatabaseFile)
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return ErrGeoLiteUnavailable
	}
	return setGeoLiteACL(p.DatabaseFile, true, h.ApplicationServiceSID)
}
func (*WindowsGeoLiteHost) RemoveBackup(p GeoLitePaths) error {
	err := os.Remove(p.BackupFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (*WindowsGeoLiteHost) RemoveStage(p GeoLitePaths) error {
	if err := rejectReparseAncestors(p.StageDir); err != nil {
		return ErrGeoLiteUnavailable
	}
	if err := os.RemoveAll(p.StageDir); err != nil {
		return err
	}
	return ensureSystemAdminDir(p.StageDir)
}

func (*WindowsGeoLiteHost) RestartApplication(ctx context.Context) error {
	ops := &WindowsHostOperations{}
	if err := ops.StopService(ctx, AppServiceName); err != nil {
		return ErrGeoLiteUnavailable
	}
	if err := ops.StartService(ctx, AppServiceName); err != nil {
		return ErrGeoLiteUnavailable
	}
	return ops.WaitReady(ctx, "http://127.0.0.1:18080/readyz")
}

var _ GeoLiteHost = (*WindowsGeoLiteHost)(nil)
