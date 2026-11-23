package windowsupdate

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/updates"
)

var ErrSnapshotInvalid = errors.New("verified snapshot could not be created or restored")

type SnapshotPaths struct {
	InstallDir        string
	DataDir           string
	EnvFile           string
	RcloneConfig      string
	SnapshotDir       string
	ServerExecutable  string
	UpdaterExecutable string
}

type snapshotManifest struct {
	Tag       string          `json:"tag"`
	CreatedAt time.Time       `json:"createdAt"`
	Files     []snapshotEntry `json:"files"`
}

type snapshotEntry struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func CreateSnapshot(ctx context.Context, paths SnapshotPaths, tag string) (Snapshot, error) {
	if updatesValidateTag(tag) != nil || validateSnapshotPaths(paths) != nil {
		return Snapshot{}, ErrSnapshotInvalid
	}
	if rejectReparseAncestors(paths.SnapshotDir) != nil || os.MkdirAll(paths.SnapshotDir, 0700) != nil || rejectReparseAncestors(paths.SnapshotDir) != nil {
		return Snapshot{}, ErrSnapshotInvalid
	}
	inputs, err := snapshotInputs(ctx, paths)
	if err != nil || len(inputs) < 4 {
		return Snapshot{}, ErrSnapshotInvalid
	}
	manifest := snapshotManifest{Tag: tag, CreatedAt: time.Now().UTC()}
	for _, input := range inputs {
		entry, err := hashSnapshotFile(ctx, input.source, input.name)
		if err != nil {
			return Snapshot{}, ErrSnapshotInvalid
		}
		manifest.Files = append(manifest.Files, entry)
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return Snapshot{}, ErrSnapshotInvalid
	}
	name := "snapshot-" + time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + strings.TrimPrefix(tag, "build-")[:12] + ".zip"
	path := filepath.Join(paths.SnapshotDir, name)
	tmp, err := os.CreateTemp(paths.SnapshotDir, ".snapshot-*.tmp")
	if err != nil {
		return Snapshot{}, ErrSnapshotInvalid
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	zw := zip.NewWriter(tmp)
	for _, input := range inputs {
		if ctx.Err() != nil {
			_ = zw.Close()
			_ = tmp.Close()
			return Snapshot{}, ErrSnapshotInvalid
		}
		header := &zip.FileHeader{Name: input.name, Method: zip.Deflate}
		header.SetMode(0600)
		writer, err := zw.CreateHeader(header)
		if err != nil {
			_ = zw.Close()
			_ = tmp.Close()
			return Snapshot{}, ErrSnapshotInvalid
		}
		if err := copyRegularFile(ctx, writer, input.source); err != nil {
			_ = zw.Close()
			_ = tmp.Close()
			return Snapshot{}, ErrSnapshotInvalid
		}
	}
	manifestWriter, err := zw.Create("manifest.json")
	if err == nil {
		_, err = manifestWriter.Write(manifestBytes)
	}
	closeZipErr := zw.Close()
	syncErr := tmp.Sync()
	fileCloseErr := tmp.Close()
	if err != nil || closeZipErr != nil || syncErr != nil || fileCloseErr != nil || os.Rename(tmpName, path) != nil {
		return Snapshot{}, ErrSnapshotInvalid
	}
	verified, err := verifySnapshotArchive(ctx, path, "")
	if err != nil || verified.Tag != tag || len(verified.Files) != len(manifest.Files) {
		_ = os.Remove(path)
		return Snapshot{}, ErrSnapshotInvalid
	}
	archiveHash, err := digestFileLimit(path, 8<<30)
	if err != nil {
		_ = os.Remove(path)
		return Snapshot{}, ErrSnapshotInvalid
	}
	if pruneSnapshots(paths.SnapshotDir) != nil {
		_ = os.Remove(path)
		return Snapshot{}, ErrSnapshotInvalid
	}
	manifestSum := sha256.Sum256(manifestBytes)
	return Snapshot{Tag: tag, Path: path, ManifestSHA256: hex.EncodeToString(manifestSum[:]), ArchiveSHA256: archiveHash, Verified: true}, nil
}

func RestoreSnapshot(ctx context.Context, paths SnapshotPaths, snapshot Snapshot) error {
	if validateSnapshotPaths(paths) != nil || rejectReparseAncestors(snapshot.Path) != nil || !snapshot.Verified || !filepath.IsAbs(snapshot.Path) || !within(paths.SnapshotDir, snapshot.Path) {
		return ErrSnapshotInvalid
	}
	archiveHash, err := digestFileLimit(snapshot.Path, 8<<30)
	if err != nil || archiveHash != snapshot.ArchiveSHA256 {
		return ErrSnapshotInvalid
	}
	manifest, err := verifySnapshotArchive(ctx, snapshot.Path, snapshot.ManifestSHA256)
	if err != nil || manifest.Tag != snapshot.Tag {
		return ErrSnapshotInvalid
	}
	stage, err := os.MkdirTemp(filepath.Dir(paths.DataDir), ".bookmarksync-restore-*")
	if err != nil {
		return ErrSnapshotInvalid
	}
	defer os.RemoveAll(stage)
	if err := extractVerifiedSnapshot(ctx, snapshot.Path, manifest, stage); err != nil {
		return ErrSnapshotInvalid
	}
	return installRestoredSnapshot(ctx, paths, stage, manifest)
}

type snapshotInput struct{ source, name string }

func snapshotInputs(ctx context.Context, paths SnapshotPaths) ([]snapshotInput, error) {
	inputs := []snapshotInput{
		{paths.ServerExecutable, "install/bookmarksync.exe"},
		{paths.UpdaterExecutable, "install/bookmarksync-updater.exe"},
		{paths.EnvFile, "config/bookmarksync.env"},
		{paths.RcloneConfig, "rclone/rclone.conf"},
	}
	err := filepath.WalkDir(paths.DataDir, func(file string, entry os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ErrSnapshotInvalid
		}
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || rejectReparseAncestors(file) != nil {
			return ErrSnapshotInvalid
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return ErrSnapshotInvalid
		}
		rel, err := filepath.Rel(paths.DataDir, file)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ErrSnapshotInvalid
		}
		inputs = append(inputs, snapshotInput{file, "data/" + filepath.ToSlash(rel)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return inputs, nil
}

func hashSnapshotFile(ctx context.Context, source, name string) (snapshotEntry, error) {
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > 2<<30 {
		return snapshotEntry{}, ErrSnapshotInvalid
	}
	f, err := os.Open(source)
	if err != nil {
		return snapshotEntry{}, ErrSnapshotInvalid
	}
	defer f.Close()
	h := sha256.New()
	n, err := copyContext(ctx, h, f)
	if err != nil || n != info.Size() {
		return snapshotEntry{}, ErrSnapshotInvalid
	}
	return snapshotEntry{Name: name, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

func copyRegularFile(ctx context.Context, dst io.Writer, source string) error {
	f, err := os.Open(source)
	if err != nil {
		return ErrSnapshotInvalid
	}
	defer f.Close()
	_, err = copyContext(ctx, dst, f)
	return err
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buf := make([]byte, 64*1024)
	var total int64
	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			w, writeErr := dst.Write(buf[:n])
			total += int64(w)
			if writeErr != nil {
				return total, writeErr
			}
			if w != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
	}
}

func verifySnapshotArchive(ctx context.Context, archivePath, expectedManifestSHA string) (snapshotManifest, error) {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return snapshotManifest{}, ErrSnapshotInvalid
	}
	defer archive.Close()
	var manifestFile *zip.File
	seen := map[string]bool{}
	for _, file := range archive.File {
		if file.Name == "manifest.json" {
			if manifestFile != nil {
				return snapshotManifest{}, ErrSnapshotInvalid
			}
			manifestFile = file
			continue
		}
		if !safeArchiveName(file.Name) || seen[file.Name] {
			return snapshotManifest{}, ErrSnapshotInvalid
		}
		seen[file.Name] = true
	}
	if manifestFile == nil || manifestFile.UncompressedSize64 > 1<<20 {
		return snapshotManifest{}, ErrSnapshotInvalid
	}
	r, err := manifestFile.Open()
	if err != nil {
		return snapshotManifest{}, ErrSnapshotInvalid
	}
	data, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	_ = r.Close()
	if err != nil || len(data) > 1<<20 {
		return snapshotManifest{}, ErrSnapshotInvalid
	}
	if expectedManifestSHA != "" {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != expectedManifestSHA {
			return snapshotManifest{}, ErrSnapshotInvalid
		}
	}
	var manifest snapshotManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || len(manifest.Files) != len(seen) || updates.ValidateApprovedTag(manifest.Tag) != nil {
		return snapshotManifest{}, ErrSnapshotInvalid
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return snapshotManifest{}, ErrSnapshotInvalid
	}
	entries := make(map[string]snapshotEntry, len(manifest.Files))
	var totalSize int64
	for _, entry := range manifest.Files {
		if !safeArchiveName(entry.Name) || entries[entry.Name].Name != "" || len(entry.SHA256) != 64 || entry.Size < 0 || entry.Size > 2<<30 {
			return snapshotManifest{}, ErrSnapshotInvalid
		}
		entries[entry.Name] = entry
		totalSize += entry.Size
		if totalSize > 8<<30 {
			return snapshotManifest{}, ErrSnapshotInvalid
		}
	}
	for _, file := range archive.File {
		if file.Name == "manifest.json" {
			continue
		}
		entry, ok := entries[file.Name]
		if !ok || file.UncompressedSize64 != uint64(entry.Size) {
			return snapshotManifest{}, ErrSnapshotInvalid
		}
		f, err := file.Open()
		if err != nil {
			return snapshotManifest{}, ErrSnapshotInvalid
		}
		h := sha256.New()
		n, copyErr := copyContext(ctx, h, io.LimitReader(f, entry.Size+1))
		_ = f.Close()
		if copyErr != nil || n != entry.Size || hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
			return snapshotManifest{}, ErrSnapshotInvalid
		}
	}
	return manifest, nil
}

func extractVerifiedSnapshot(ctx context.Context, archivePath string, manifest snapshotManifest, stage string) error {
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		return ErrSnapshotInvalid
	}
	defer archive.Close()
	entries := make(map[string]snapshotEntry, len(manifest.Files))
	for _, e := range manifest.Files {
		entries[e.Name] = e
	}
	for _, file := range archive.File {
		if file.Name == "manifest.json" {
			continue
		}
		if ctx.Err() != nil {
			return ErrSnapshotInvalid
		}
		entry, ok := entries[file.Name]
		if !ok || !safeArchiveName(file.Name) {
			return ErrSnapshotInvalid
		}
		target := filepath.Join(stage, filepath.FromSlash(file.Name))
		if !within(stage, target) {
			return ErrSnapshotInvalid
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return ErrSnapshotInvalid
		}
		in, err := file.Open()
		if err != nil {
			return ErrSnapshotInvalid
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			_ = in.Close()
			return ErrSnapshotInvalid
		}
		h := sha256.New()
		n, copyErr := copyContext(ctx, io.MultiWriter(out, h), io.LimitReader(in, entry.Size+1))
		closeIn, closeOut := in.Close(), out.Close()
		if copyErr != nil || closeIn != nil || closeOut != nil || n != entry.Size || hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
			return ErrSnapshotInvalid
		}
	}
	return nil
}

func installRestoredSnapshot(ctx context.Context, paths SnapshotPaths, stage string, manifest snapshotManifest) error {
	if ctx.Err() != nil {
		return ErrSnapshotInvalid
	}
	dataStage := filepath.Join(stage, "data")
	if err := os.MkdirAll(dataStage, 0700); err != nil {
		return ErrSnapshotInvalid
	}
	needed := map[string]bool{
		"install/bookmarksync.exe":         true,
		"install/bookmarksync-updater.exe": true,
		"config/bookmarksync.env":          true,
		"rclone/rclone.conf":               true,
	}
	for _, file := range manifest.Files {
		if _, ok := needed[file.Name]; ok {
			delete(needed, file.Name)
			continue
		}
		if !strings.HasPrefix(file.Name, "data/") {
			return ErrSnapshotInvalid
		}
	}
	if len(needed) != 0 {
		return ErrSnapshotInvalid
	}
	// Stage each replacement beside its destination. A failed file restore rolls
	// back every earlier rename, while the verified archive remains untouched.
	replacements := []struct {
		from, to  string
		directory bool
	}{
		{from: filepath.Join(stage, "install", "bookmarksync.exe"), to: paths.ServerExecutable},
		{from: filepath.Join(stage, "install", "bookmarksync-updater.exe"), to: paths.UpdaterExecutable},
		{from: filepath.Join(stage, "config", "bookmarksync.env"), to: paths.EnvFile},
		{from: filepath.Join(stage, "rclone", "rclone.conf"), to: paths.RcloneConfig},
		{from: dataStage, to: paths.DataDir, directory: true},
	}
	var swaps []pathSwap
	for _, replacement := range replacements {
		if err := ctx.Err(); err != nil {
			rollbackSwaps(swaps)
			return ErrSnapshotInvalid
		}
		swap, err := stagePathReplacement(replacement.from, replacement.to, replacement.directory)
		if err != nil {
			if rollbackSwaps(swaps) != nil {
				return ErrSnapshotInvalid
			}
			return ErrSnapshotInvalid
		}
		swaps = append(swaps, swap)
	}
	for _, swap := range swaps {
		if swap.backup != "" {
			if err := os.RemoveAll(swap.backup); err != nil {
				return ErrSnapshotInvalid
			}
		}
	}
	return nil
}

type pathSwap struct {
	target, backup string
	directory      bool
	installed      bool
}

func stagePathReplacement(staged, target string, directory bool) (pathSwap, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return pathSwap{}, ErrSnapshotInvalid
	}
	stagedInfo, err := os.Lstat(staged)
	if err != nil || stagedInfo.Mode()&os.ModeSymlink != 0 || (directory && !stagedInfo.IsDir()) || (!directory && !stagedInfo.Mode().IsRegular()) {
		return pathSwap{}, ErrSnapshotInvalid
	}
	swap := pathSwap{target: target, directory: directory}
	targetInfo, err := os.Lstat(target)
	if err == nil {
		if targetInfo.Mode()&os.ModeSymlink != 0 || (directory && !targetInfo.IsDir()) || (!directory && !targetInfo.Mode().IsRegular()) {
			return pathSwap{}, ErrSnapshotInvalid
		}
		backupFile, err := os.CreateTemp(filepath.Dir(target), ".bookmarksync-rollback-*")
		if err != nil {
			return pathSwap{}, ErrSnapshotInvalid
		}
		backup := backupFile.Name()
		if backupFile.Close() != nil || os.Remove(backup) != nil {
			return pathSwap{}, ErrSnapshotInvalid
		}
		if err := os.Rename(target, backup); err != nil {
			return pathSwap{}, ErrSnapshotInvalid
		}
		swap.backup = backup
	} else if !errors.Is(err, os.ErrNotExist) {
		return pathSwap{}, ErrSnapshotInvalid
	}
	if err := os.Rename(staged, target); err != nil {
		if swap.backup != "" {
			_ = os.Rename(swap.backup, target)
		}
		return pathSwap{}, ErrSnapshotInvalid
	}
	swap.installed = true
	return swap, nil
}

func rollbackSwaps(swaps []pathSwap) error {
	var failed bool
	for i := len(swaps) - 1; i >= 0; i-- {
		swap := swaps[i]
		if swap.installed {
			var err error
			if swap.directory {
				err = os.RemoveAll(swap.target)
			} else {
				err = os.Remove(swap.target)
			}
			if err != nil {
				failed = true
				continue
			}
		}
		if swap.backup != "" && os.Rename(swap.backup, swap.target) != nil {
			failed = true
		}
	}
	if failed {
		return ErrSnapshotInvalid
	}
	return nil
}

func safeArchiveName(name string) bool {
	clean := path.Clean(name)
	return clean == name && !strings.HasPrefix(name, "../") && !strings.HasPrefix(name, "/") && !strings.Contains(name, `\`)
}

func validateSnapshotPaths(paths SnapshotPaths) error {
	for _, value := range []string{paths.InstallDir, paths.DataDir, paths.EnvFile, paths.RcloneConfig, paths.SnapshotDir, paths.ServerExecutable, paths.UpdaterExecutable} {
		if !filepath.IsAbs(value) {
			return ErrSnapshotInvalid
		}
	}
	if filepath.Dir(paths.ServerExecutable) != paths.InstallDir || filepath.Dir(paths.UpdaterExecutable) != paths.InstallDir ||
		overlaps(paths.InstallDir, paths.DataDir) || overlaps(paths.SnapshotDir, paths.DataDir) || overlaps(paths.SnapshotDir, paths.InstallDir) ||
		within(paths.DataDir, paths.EnvFile) || within(paths.DataDir, paths.RcloneConfig) {
		return ErrSnapshotInvalid
	}
	for _, dir := range []string{paths.InstallDir, paths.DataDir, filepath.Dir(paths.EnvFile), filepath.Dir(paths.RcloneConfig)} {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrSnapshotInvalid
		}
		if rejectReparseAncestors(dir) != nil {
			return ErrSnapshotInvalid
		}
	}
	if !filepath.IsAbs(paths.SnapshotDir) || rejectReparseAncestors(paths.SnapshotDir) != nil {
		return ErrSnapshotInvalid
	}
	return nil
}

func rejectReparseAncestors(candidate string) error {
	if !filepath.IsAbs(candidate) {
		return ErrSnapshotInvalid
	}
	for current := filepath.Clean(candidate); ; current = filepath.Dir(current) {
		if isReparsePoint(current) {
			return ErrSnapshotInvalid
		}
		if parent := filepath.Dir(current); parent == current {
			break
		}
	}
	return nil
}

func within(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func overlaps(a, b string) bool { return within(a, b) || within(b, a) }

func pruneSnapshots(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ErrSnapshotInvalid
	}
	type snapshotFile struct {
		path string
		mod  time.Time
	}
	var files []snapshotFile
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "snapshot-") || !strings.HasSuffix(entry.Name(), ".zip") {
			continue
		}
		if isReparsePoint(filepath.Join(dir, entry.Name())) {
			return ErrSnapshotInvalid
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return ErrSnapshotInvalid
		}
		files = append(files, snapshotFile{filepath.Join(dir, entry.Name()), info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	if len(files) > 3 {
		for _, file := range files[3:] {
			if err := os.Remove(file.path); err != nil {
				return ErrSnapshotInvalid
			}
		}
	}
	return nil
}

func updatesValidateTag(tag string) error { return updates.ValidateApprovedTag(tag) }
