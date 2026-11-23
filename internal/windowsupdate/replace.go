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
	if err != nil || verified.Commit != release.Commit || verified.ServerExecutable != release.ServerExecutable || verified.UpdaterExecutable != release.UpdaterExecutable || verified.BuildInfo != release.BuildInfo || verified.SHA256Sums != release.SHA256Sums {
		return ErrReleaseInvalid
	}
	checksums, err := parseChecksums(release.SHA256Sums)
	if err != nil {
		return ErrReleaseInvalid
	}
	for _, target := range []string{serverPath, updaterPath} {
		if info, err := os.Lstat(target); err == nil {
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
	stagedServer, serverHash, err := stageVerifiedBinary(installDir, release.ServerExecutable)
	if err != nil || serverHash != checksums[WindowsServerAsset] {
		if stagedServer != "" {
			_ = os.Remove(stagedServer)
		}
		return ErrReleaseInvalid
	}
	defer os.Remove(stagedServer)
	stagedUpdater, updaterHash, err := stageVerifiedBinary(installDir, release.UpdaterExecutable)
	if err != nil || updaterHash != checksums[WindowsUpdaterAsset] {
		if stagedUpdater != "" {
			_ = os.Remove(stagedUpdater)
		}
		return ErrReleaseInvalid
	}
	defer os.Remove(stagedUpdater)
	moves := []binaryMove{{target: serverPath, stage: stagedServer}, {target: updaterPath, stage: stagedUpdater}}
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
