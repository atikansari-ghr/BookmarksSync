package windowsupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

func ReplaceInstalledBinaries(installDir, serverPath, updaterPath string, release VerifiedRelease) error {
	return replaceInstalledBinaries(installDir, serverPath, updaterPath, release, os.Rename)
}

func replaceInstalledBinaries(installDir, serverPath, updaterPath string, release VerifiedRelease, rename func(string, string) error) error {
	if !filepath.IsAbs(installDir) || filepath.Dir(serverPath) != installDir || filepath.Dir(updaterPath) != installDir || filepath.Base(serverPath) == "" || filepath.Base(updaterPath) == "" || !release.IsComplete() {
		return ErrReleaseInvalid
	}
	if rejectReparseAncestors(installDir) != nil {
		return ErrReleaseInvalid
	}
	verified, err := VerifyReleaseFiles(filepath.Dir(release.ServerExecutable), release.Tag)
	if err != nil || verified.Commit != release.Commit || verified.ServerExecutable != release.ServerExecutable || verified.UpdaterExecutable != release.UpdaterExecutable || verified.BuildInfo != release.BuildInfo || verified.SHA256Sums != release.SHA256Sums || verified.GeoLiteUpdater != release.GeoLiteUpdater || verified.GeoLiteLicense != release.GeoLiteLicense {
		return ErrReleaseInvalid
	}
	checksums, err := parseChecksums(release.SHA256Sums)
	if err != nil {
		return ErrReleaseInvalid
	}
	type asset struct {
		source, target, name string
		checked              bool
	}
	assets := []asset{{release.ServerExecutable, serverPath, WindowsServerAsset, true}, {release.UpdaterExecutable, updaterPath, WindowsUpdaterAsset, true}, {release.GeoLiteUpdater, filepath.Join(installDir, "geoipupdate.exe"), WindowsGeoLiteAsset, true}, {release.GeoLiteLicense, filepath.Join(installDir, GeoLiteLicenseAsset), GeoLiteLicenseAsset, true}, {release.SHA256Sums, filepath.Join(installDir, ChecksumsAsset), ChecksumsAsset, false}}
	for _, item := range assets {
		if info, err := os.Lstat(item.target); err == nil {
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return ErrReleaseInvalid
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrReleaseInvalid
		}
	}
	if err := os.MkdirAll(installDir, 0700); err != nil {
		return ErrReleaseInvalid
	}
	if rejectReparseAncestors(installDir) != nil {
		return ErrReleaseInvalid
	}
	moves := make([]binaryMove, 0, len(assets))
	for _, item := range assets {
		staged, digest, err := stageVerifiedBinary(installDir, item.source)
		if err != nil || (item.checked && digest != checksums[item.name]) {
			if staged != "" {
				_ = os.Remove(staged)
			}
			for _, move := range moves {
				_ = os.Remove(move.stage)
			}
			return ErrReleaseInvalid
		}
		moves = append(moves, binaryMove{target: item.target, stage: staged})
	}
	defer func() {
		for _, move := range moves {
			_ = os.Remove(move.stage)
		}
	}()
	for i := range moves {
		if _, err := os.Lstat(moves[i].target); err == nil {
			backupFile, err := os.CreateTemp(installDir, ".bookmarksync-update-backup-*")
			if err != nil {
				rollbackBinaryMoves(moves[:i], rename)
				return ErrReleaseInvalid
			}
			moves[i].backup = backupFile.Name()
			if backupFile.Close() != nil || os.Remove(moves[i].backup) != nil {
				rollbackBinaryMoves(moves[:i], rename)
				return ErrReleaseInvalid
			}
			if err := rename(moves[i].target, moves[i].backup); err != nil {
				rollbackBinaryMoves(moves[:i], rename)
				return ErrReleaseInvalid
			}
		}
		if err := rename(moves[i].stage, moves[i].target); err != nil {
			rollbackBinaryMoves(moves[:i+1], rename)
			return ErrReleaseInvalid
		}
		moves[i].installed = true
	}
	for _, move := range moves {
		if move.backup != "" {
			_ = os.Remove(move.backup)
		}
	}
	return nil
}

func stageVerifiedBinary(dir, source string) (string, string, error) {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 512<<20 {
		return "", "", ErrReleaseInvalid
	}
	in, err := os.Open(source)
	if err != nil {
		return "", "", ErrReleaseInvalid
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, ".bookmarksync-update-stage-*")
	if err != nil {
		return "", "", ErrReleaseInvalid
	}
	path := out.Name()
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, (512<<20)+1))
	if copyErr == nil && n == info.Size() {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil || n != info.Size() {
		_ = os.Remove(path)
		return "", "", ErrReleaseInvalid
	}
	return path, hex.EncodeToString(h.Sum(nil)), nil
}

type binaryMove struct {
	target, stage, backup string
	installed             bool
}

func rollbackBinaryMoves(moves []binaryMove, rename func(string, string) error) {
	for i := len(moves) - 1; i >= 0; i-- {
		move := moves[i]
		if move.installed {
			_ = os.Remove(move.target)
		}
		if move.backup != "" {
			_ = rename(move.backup, move.target)
		}
	}
}
