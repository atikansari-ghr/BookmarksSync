package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/securefile"
	"github.com/atikansari-ghr/BookmarksSync/internal/version"
	_ "modernc.org/sqlite"
)

type Options struct {
	DB      *database.DB
	DataDir string
	Output  string
	Build   version.Info
}

type Result struct {
	Path     string
	Size     int64
	Manifest Manifest
}

func Create(ctx context.Context, opts Options) (Result, error) {
	if opts.DB == nil || opts.DataDir == "" || opts.Output == "" {
		return Result{}, errors.New("backup requires database, data directory, and output path")
	}
	if opts.Build.Version == "" {
		opts.Build = version.Current()
	}
	output, err := filepath.Abs(opts.Output)
	if err != nil {
		return Result{}, errors.New("invalid backup output path")
	}
	if _, err := os.Lstat(output); err == nil {
		return Result{}, errors.New("backup output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, errors.New("backup output cannot be checked")
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return Result{}, fmt.Errorf("create backup output directory: %w", err)
	}
	stage, err := securefile.CreateTempDir(os.TempDir(), "bookmarksync-backup-stage-*")
	if err != nil {
		return Result{}, fmt.Errorf("create local backup staging directory: %w", err)
	}
	defer os.RemoveAll(stage)

	if err := opts.DB.BackupTo(ctx, filepath.Join(stage, databaseName)); err != nil {
		return Result{}, err
	}
	runtimePath := config.RuntimePath(opts.DataDir)
	if err := copyPrivateFile(runtimePath, filepath.Join(stage, runtimeName), maxRuntime); err != nil {
		return Result{}, fmt.Errorf("stage runtime configuration: %w", err)
	}
	runtimeCfg, err := config.LoadRuntime(filepath.Join(stage, filepath.FromSlash(runtimeName)))
	if err != nil {
		return Result{}, errors.New("valid runtime configuration is required for a full backup")
	}
	keyPath := filepath.Join(opts.DataDir, "instance.key")
	if _, err := os.Lstat(keyPath); err == nil {
		key, err := identity.LoadOrCreateInstanceKey(opts.DataDir)
		if err != nil {
			return Result{}, errors.New("instance key is invalid or not private")
		}
		for i := range key {
			key[i] = 0
		}
		if err := copyPrivateFile(keyPath, filepath.Join(stage, keyName), 4096); err != nil {
			return Result{}, fmt.Errorf("stage instance key: %w", err)
		}
	} else if errors.Is(err, os.ErrNotExist) && runtimeCfg.PublicOrigin != "" {
		return Result{}, errors.New("browser sign-in backup is missing its instance key")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("inspect instance key: %w", err)
	}
	settings, err := snapshotBrandingSettings(ctx, filepath.Join(stage, filepath.FromSlash(databaseName)))
	if err != nil {
		return Result{}, fmt.Errorf("read branding settings: %w", err)
	}
	for _, key := range []string{"branding.logo-id", "branding.background-id"} {
		id := settings[key]
		if id == "" {
			continue
		}
		if !validAssetID(id) {
			return Result{}, errors.New("branding setting contains an invalid asset reference")
		}
		found := false
		for _, ext := range []string{"png", "jpg", "webp"} {
			name := "branding/" + id + "." + ext
			source := filepath.Join(opts.DataDir, "branding", id+"."+ext)
			if _, err := os.Lstat(source); err == nil {
				if !brandingName.MatchString(name) {
					return Result{}, errors.New("branding setting contains an invalid asset reference")
				}
				if err := copyPrivateFile(source, filepath.Join(stage, filepath.FromSlash(name)), 2<<20); err != nil {
					return Result{}, errors.New("referenced branding asset could not be staged")
				}
				found = true
			}
		}
		if !found {
			return Result{}, errors.New("referenced branding asset is missing")
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	manifest := Manifest{FormatVersion: FormatVersion, CreatedAt: time.Now().UTC(), Application: opts.Build}
	manifest.PublicOrigin = runtimeCfg.PublicOrigin
	for _, provider := range runtimeCfg.OIDCProviders {
		manifest.OIDCProviderIDs = append(manifest.OIDCProviderIDs, provider.ID)
	}
	manifest.SchemaVersion, err = readDatabaseSchemaVersion(filepath.Join(stage, filepath.FromSlash(databaseName)))
	if err != nil {
		return Result{}, err
	}
	var names []string
	err = filepath.WalkDir(stage, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(stage, path)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("list staged backup files: %w", err)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		e, err := fileEntry(filepath.Join(stage, filepath.FromSlash(name)), name)
		if err != nil {
			return Result{}, err
		}
		manifest.Entries = append(manifest.Entries, e)
	}
	manifestBytes, err := encodeManifest(manifest)
	if err != nil {
		return Result{}, err
	}
	tmp, err := securefile.CreateTemp(filepath.Dir(output), ".bookmarksync-backup-*.tmp")
	if err != nil {
		return Result{}, fmt.Errorf("create temporary backup archive: %w", err)
	}
	tmpPath := tmp.Name()
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(tmpPath)
		}
	}()
	w := zip.NewWriter(tmp)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			w.Close()
			tmp.Close()
			return Result{}, err
		}
		if err := addZipFile(w, filepath.Join(stage, filepath.FromSlash(name)), name); err != nil {
			w.Close()
			tmp.Close()
			return Result{}, err
		}
	}
	if err := addZipBytes(w, manifestName, manifestBytes); err != nil {
		w.Close()
		tmp.Close()
		return Result{}, err
	}
	if err := w.Close(); err != nil {
		tmp.Close()
		return Result{}, fmt.Errorf("finish backup archive: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return Result{}, fmt.Errorf("sync backup archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Result{}, fmt.Errorf("close backup archive: %w", err)
	}
	if err := securefile.PublishFile(tmpPath, output); err != nil {
		return Result{}, fmt.Errorf("publish backup archive: %w", err)
	}
	complete = true
	_ = os.Remove(tmpPath)
	if err := securefile.SyncDir(filepath.Dir(output)); err != nil {
		return Result{}, fmt.Errorf("sync backup output directory: %w", err)
	}
	info, err := os.Stat(output)
	if err != nil {
		return Result{}, fmt.Errorf("stat published backup: %w", err)
	}
	return Result{Path: output, Size: info.Size(), Manifest: manifest}, nil
}

func Inspect(ctx context.Context, path string) (Manifest, error) {
	stage, err := securefile.CreateTempDir(os.TempDir(), "bookmarksync-verify-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("create verification staging directory: %w", err)
	}
	defer os.RemoveAll(stage)
	manifest, err := verifyToStage(ctx, path, stage)
	if err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func verifyToStage(ctx context.Context, path, stage string) (Manifest, error) {
	if err := preflightZip(path); err != nil {
		return Manifest{}, err
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return Manifest{}, errors.New("backup archive is invalid")
	}
	defer zr.Close()
	if len(zr.File) < 3 || len(zr.File) > maxFiles+1 {
		return Manifest{}, errors.New("backup archive entry count is invalid")
	}
	seen := map[string]bool{}
	var manifestFile *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.Mode()&os.ModeSymlink != 0 || seen[f.Name] {
			return Manifest{}, errors.New("backup archive contains an unsafe or duplicate entry")
		}
		seen[f.Name] = true
		if f.Name == manifestName {
			manifestFile = f
			continue
		}
		if !allowedName(f.Name) {
			return Manifest{}, errors.New("backup archive contains an unsupported entry")
		}
	}
	if manifestFile == nil || manifestFile.UncompressedSize64 > maxManifest {
		return Manifest{}, errors.New("backup manifest is missing or too large")
	}
	manifestData, err := readZipFile(manifestFile, maxManifest)
	if err != nil {
		return Manifest{}, errors.New("backup manifest is invalid")
	}
	var manifest Manifest
	dec := json.NewDecoder(strings.NewReader(string(manifestData)))
	dec.DisallowUnknownFields()
	if dec.Decode(&manifest) != nil || dec.Decode(new(any)) != io.EOF {
		return Manifest{}, errors.New("backup manifest is invalid")
	}
	if err := manifest.validate(); err != nil {
		return Manifest{}, err
	}
	entries := make(map[string]Entry, len(manifest.Entries))
	for _, e := range manifest.Entries {
		entries[e.Name] = e
	}
	if len(seen) != len(entries)+1 {
		return Manifest{}, errors.New("backup entries do not match the manifest")
	}
	for _, f := range zr.File {
		if f.Name == manifestName {
			continue
		}
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
		expected, ok := entries[f.Name]
		if !ok {
			return Manifest{}, errors.New("backup entries do not match the manifest")
		}
		limit, ok := entrySizeLimit(f.Name)
		if !ok || f.UncompressedSize64 > uint64(limit) || f.UncompressedSize64 != uint64(expected.Size) {
			return Manifest{}, errors.New("backup entry size does not match its manifest")
		}
		if err := verifyZipEntry(ctx, f, expected, filepath.Join(stage, filepath.FromSlash(f.Name))); err != nil {
			return Manifest{}, err
		}
	}
	if err := validateDatabase(filepath.Join(stage, filepath.FromSlash(databaseName)), manifest.SchemaVersion); err != nil {
		return Manifest{}, err
	}
	runtimeCfg, err := config.LoadRuntime(filepath.Join(stage, filepath.FromSlash(runtimeName)))
	if err != nil {
		return Manifest{}, errors.New("backup runtime configuration is invalid")
	}
	if runtimeCfg.PublicOrigin != manifest.PublicOrigin {
		return Manifest{}, errors.New("backup public origin does not match its runtime configuration")
	}
	providerIDs := make([]string, 0, len(runtimeCfg.OIDCProviders))
	for _, provider := range runtimeCfg.OIDCProviders {
		providerIDs = append(providerIDs, provider.ID)
	}
	if !sameStrings(providerIDs, manifest.OIDCProviderIDs) {
		return Manifest{}, errors.New("backup provider list does not match its runtime configuration")
	}
	return manifest, nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// preflightZip caps central-directory parsing before archive/zip allocates one
// File record per entry. This rejects oversized and multi-disk containers early.
func preflightZip(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 22 || info.Size() > maxArchiveSize {
		return errors.New("backup archive file is invalid or too large")
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("backup archive cannot be opened")
	}
	defer f.Close()
	windowSize := int64(22 + 65535)
	if info.Size() < windowSize {
		windowSize = info.Size()
	}
	window := make([]byte, windowSize)
	if _, err := f.ReadAt(window, info.Size()-windowSize); err != nil {
		return errors.New("backup archive directory is invalid")
	}
	search := window
	for {
		idx := bytes.LastIndex(search, []byte{'P', 'K', 5, 6})
		if idx < 0 || len(search)-idx < 22 {
			return errors.New("backup archive directory is invalid")
		}
		eocd := search[idx:]
		commentLen := int(binary.LittleEndian.Uint16(eocd[20:22]))
		if len(eocd) == 22+commentLen {
			if binary.LittleEndian.Uint16(eocd[4:6]) != 0 || binary.LittleEndian.Uint16(eocd[6:8]) != 0 {
				return errors.New("multi-disk backup archives are unsupported")
			}
			if binary.LittleEndian.Uint16(eocd[8:10]) != binary.LittleEndian.Uint16(eocd[10:12]) {
				return errors.New("multi-disk backup archives are unsupported")
			}
			count := uint64(binary.LittleEndian.Uint16(eocd[10:12]))
			directorySize := uint64(binary.LittleEndian.Uint32(eocd[12:16]))
			directoryOffset := uint64(binary.LittleEndian.Uint32(eocd[16:20]))
			if count == 0xffff || directorySize == 0xffffffff || directoryOffset == 0xffffffff {
				absoluteEOCD := info.Size() - windowSize + int64(idx)
				if absoluteEOCD < 20 {
					return errors.New("backup ZIP64 directory is invalid")
				}
				locator := make([]byte, 20)
				if _, err := f.ReadAt(locator, absoluteEOCD-20); err != nil || !bytes.Equal(locator[:4], []byte{'P', 'K', 6, 7}) {
					return errors.New("backup ZIP64 directory is invalid")
				}
				if binary.LittleEndian.Uint32(locator[4:8]) != 0 {
					return errors.New("multi-disk backup archives are unsupported")
				}
				if binary.LittleEndian.Uint32(locator[16:20]) != 1 {
					return errors.New("multi-disk backup archives are unsupported")
				}
				zip64Offset := binary.LittleEndian.Uint64(locator[8:16])
				if zip64Offset > uint64(info.Size()) || uint64(info.Size())-zip64Offset < 56 {
					return errors.New("backup ZIP64 directory is invalid")
				}
				zip64 := make([]byte, 56)
				if _, err := f.ReadAt(zip64, int64(zip64Offset)); err != nil || !bytes.Equal(zip64[:4], []byte{'P', 'K', 6, 6}) {
					return errors.New("backup ZIP64 directory is invalid")
				}
				count = binary.LittleEndian.Uint64(zip64[32:40])
				if binary.LittleEndian.Uint32(zip64[16:20]) != 0 || binary.LittleEndian.Uint32(zip64[20:24]) != 0 || binary.LittleEndian.Uint64(zip64[24:32]) != count {
					return errors.New("multi-disk backup archives are unsupported")
				}
				directorySize = binary.LittleEndian.Uint64(zip64[40:48])
				directoryOffset = binary.LittleEndian.Uint64(zip64[48:56])
			}
			if count > uint64(maxFiles+1) || directorySize > maxCentralDirectory || directoryOffset > uint64(info.Size()) || directorySize > uint64(info.Size())-directoryOffset {
				return errors.New("backup archive directory exceeds supported limits")
			}
			return nil
		}
		search = search[:idx]
	}
}

func verifyZipEntry(ctx context.Context, f *zip.File, expected Entry, output string) error {
	r, err := f.Open()
	if err != nil {
		return errors.New("backup entry cannot be read")
	}
	defer r.Close()
	if err := securefile.MkdirAll(filepath.Dir(output)); err != nil {
		return fmt.Errorf("create verification staging path: %w", err)
	}
	out, err := securefile.Create(output)
	if err != nil {
		return fmt.Errorf("create verification staging file: %w", err)
	}
	h := sha256.New()
	limit, ok := entrySizeLimit(expected.Name)
	if !ok {
		_ = out.Close()
		_ = os.Remove(output)
		return errors.New("backup entry name is unsupported")
	}
	count, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(&contextReader{ctx: ctx, r: r}, limit+1))
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("backup entry could not be extracted")
	}
	if count != expected.Size || hex.EncodeToString(h.Sum(nil)) != expected.SHA256 {
		return errors.New("backup entry checksum or size does not match")
	}
	return nil
}

func validateDatabase(path string, expectedSchema int) error {
	schema, err := readDatabaseSchemaVersion(path)
	if err != nil {
		return err
	}
	if schema != expectedSchema {
		return errors.New("backup database schema does not match its manifest")
	}
	if schema > database.LatestSchemaVersion {
		return errors.New("backup database requires a newer application version")
	}
	return nil
}

func readDatabaseSchemaVersion(path string) (int, error) {
	db, err := openSnapshotDB(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var check string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		return 0, errors.New("backup database integrity check failed")
	}
	var schema int
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&schema); err != nil {
		return 0, errors.New("backup database schema is invalid")
	}
	return schema, nil
}

func openSnapshotDB(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("backup database path is invalid")
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro&immutable=1"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errors.New("backup database is invalid")
	}
	return db, nil
}

func snapshotBrandingSettings(ctx context.Context, path string) (map[string]string, error) {
	db, err := openSnapshotDB(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT key,value FROM system_settings WHERE key IN ('branding.logo-id','branding.background-id')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return settings, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func readZipFile(f *zip.File, limit int64) ([]byte, error) {
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, limit+1))
}

func copyPrivateFile(source, destination string, max int64) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > max {
		return errors.New("file is not a supported regular file")
	}
	if err := securefile.MkdirAll(filepath.Dir(destination)); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := securefile.Create(destination)
	if err != nil {
		return err
	}
	count, copyErr := io.Copy(out, io.LimitReader(in, max+1))
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || count > max || count != info.Size() {
		_ = os.Remove(destination)
		return errors.New("file copy failed")
	}
	return nil
}

func fileEntry(path, name string) (Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Entry{}, fmt.Errorf("hash backup file %s: %w", name, err)
	}
	return Entry{Name: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func addZipFile(w *zip.Writer, source, name string) error {
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = name
	header.Method = zip.Deflate
	header.SetMode(0o600)
	dst, err := w.CreateHeader(header)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, file); err != nil {
		return fmt.Errorf("write backup archive entry: %w", err)
	}
	return nil
}

func addZipBytes(w *zip.Writer, name string, data []byte) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0o600)
	dst, err := w.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = dst.Write(data)
	return err
}
