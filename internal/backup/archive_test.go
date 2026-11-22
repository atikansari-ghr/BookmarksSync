package backup

import (
	"archive/zip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
)

func makeTestBackup(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.SQL().Exec(`INSERT INTO app_metadata(key,value) VALUES('archive-test','preserved')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`INSERT INTO system_settings(key,value,updated_at) VALUES('network.geo-mode','block',1),('network.geo-countries','US',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.LoadOrCreateInstanceKey(dir); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveRuntime(config.RuntimePath(dir), config.RuntimeConfig{ListenAddr: "127.0.0.1:8080", PublicOrigin: "https://bookmarks.example.org", OIDCProviders: []config.OIDCProvider{{ID: "google", Name: "Google", Issuer: config.GoogleIssuer, ClientID: "client-id", ClientSecret: "secret-value"}}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "system.zip")
	if _, err := Create(context.Background(), Options{DB: db, DataDir: dir, Output: path}); err != nil {
		t.Fatal(err)
	}
	return path, dir
}

func TestCreateAndInspectFullSystemArchive(t *testing.T) {
	path, _ := makeTestBackup(t)
	m, err := Inspect(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if m.FormatVersion != FormatVersion || m.SchemaVersion != database.LatestSchemaVersion || len(m.Entries) != 3 || len(m.OIDCProviderIDs) != 1 || m.OIDCProviderIDs[0] != "google" {
		t.Fatalf("unexpected manifest: %#v", m)
	}
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, f := range reader.File {
		if f.Name == manifestName {
			continue
		}
		if f.Method != zip.Deflate {
			t.Errorf("%s compression method = %d", f.Name, f.Method)
		}
	}
}

func TestCreateDoesNotReplaceExistingOutput(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := config.SaveRuntime(config.RuntimePath(dir), config.RuntimeConfig{ListenAddr: "127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "existing.zip")
	if err := os.WriteFile(output, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), Options{DB: db, DataDir: dir, Output: output}); err == nil {
		t.Fatal("Create replaced an existing archive")
	}
	if got, err := os.ReadFile(output); err != nil || string(got) != "keep" {
		t.Fatalf("existing archive changed: %q (%v)", got, err)
	}
}

func TestCreateSnapshotsReferencedBrandingFromDatabaseSnapshot(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := config.SaveRuntime(config.RuntimePath(dir), config.RuntimeConfig{ListenAddr: "127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	id := "abcdef0123456789abcdef0123456789"
	brandingDir := filepath.Join(dir, "branding")
	if err := os.Mkdir(brandingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brandingDir, id+".png"), []byte("logo-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brandingDir, "ffffffffffffffffffffffffffffffff.jpg"), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`INSERT INTO system_settings(key,value,updated_at) VALUES('branding.logo-id',?,1)`, id); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "branding.zip")
	result, err := Create(context.Background(), Options{DB: db, DataDir: dir, Output: archive})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Manifest.Entries) != 3 {
		t.Fatalf("entries = %#v; expected only the referenced image", result.Manifest.Entries)
	}
	if _, err := Inspect(context.Background(), archive); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := Restore(context.Background(), archive, restored); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(restored, "branding", id+".png")); err != nil || string(got) != "logo-bytes" {
		t.Fatalf("restored branding = %q (%v)", got, err)
	}
	if _, err := os.Stat(filepath.Join(restored, "branding", "ffffffffffffffffffffffffffffffff.jpg")); !os.IsNotExist(err) {
		t.Fatalf("orphan branding was included: %v", err)
	}
}

func TestInspectRejectsFutureDatabaseSchema(t *testing.T) {
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.SQL().Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(9999,'future')`); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveRuntime(config.RuntimePath(dir), config.RuntimeConfig{ListenAddr: "127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "future.zip")
	if _, err := Create(context.Background(), Options{DB: db, DataDir: dir, Output: archive}); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), archive); err == nil {
		t.Fatal("Inspect accepted a database schema newer than this binary")
	}
}

func TestRestoreCreatesNewDataDirectory(t *testing.T) {
	path, _ := makeTestBackup(t)
	target := filepath.Join(t.TempDir(), "restored")
	if err := Restore(context.Background(), path, target); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(context.Background(), filepath.Join(target, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got string
	if err := db.SQL().QueryRow(`SELECT value FROM app_metadata WHERE key='archive-test'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "preserved" {
		t.Fatalf("restored data = %q", got)
	}
	var geoMode, countries string
	if err := db.SQL().QueryRow(`SELECT value FROM system_settings WHERE key='network.geo-mode'`).Scan(&geoMode); err != nil {
		t.Fatal(err)
	}
	if err := db.SQL().QueryRow(`SELECT value FROM system_settings WHERE key='network.geo-countries'`).Scan(&countries); err != nil {
		t.Fatal(err)
	}
	if geoMode != "block" || countries != "US" {
		t.Fatalf("restored geo policy = %q/%q", geoMode, countries)
	}
	if _, err := config.LoadRuntime(config.RuntimePath(target)); err != nil {
		t.Fatal(err)
	}
	restoredRuntime, err := config.LoadRuntime(config.RuntimePath(target))
	if err != nil || len(restoredRuntime.OIDCProviders) != 1 || restoredRuntime.OIDCProviders[0].ClientSecret != "secret-value" {
		t.Fatalf("restored provider credentials missing: %#v (%v)", restoredRuntime, err)
	}
	if _, err := identity.LoadOrCreateInstanceKey(target); err != nil {
		t.Fatalf("restored instance key: %v", err)
	}
}

func TestRestoreDisablesBackupTargetsAndFinishesInterruptedRuns(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := database.Open(ctx, filepath.Join(dir, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`UPDATE backup_schedule SET weekly_enabled=1, monthly_enabled=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`INSERT INTO backup_destinations(id,label,kind,reference,enabled,created_at,updated_at) VALUES('0123456789abcdef','Remote','rclone','drive:backups',1,1,1)`); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db.SQL(), time.Now)
	dest, err := store.ListDestinations(ctx)
	if err != nil || len(dest) != 1 {
		t.Fatalf("destinations=%#v err=%v", dest, err)
	}
	started := time.Now().UTC()
	if _, err := store.StartDestinationRun(ctx, "0123456789abcdef0123456789abcdef", TriggerManual, dest[0], started); err != nil {
		t.Fatal(err)
	}
	if _, err := identity.LoadOrCreateInstanceKey(dir); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveRuntime(config.RuntimePath(dir), config.RuntimeConfig{ListenAddr: "127.0.0.1:8080", PublicOrigin: "https://bookmarks.example.org"}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := Create(ctx, Options{DB: db, DataDir: dir, Output: archive}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err := Restore(ctx, archive, target); err != nil {
		t.Fatal(err)
	}
	restored, err := database.Open(ctx, filepath.Join(target, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var weekly, monthly, enabled int
	if err := restored.SQL().QueryRow(`SELECT weekly_enabled,monthly_enabled FROM backup_schedule WHERE id=1`).Scan(&weekly, &monthly); err != nil {
		t.Fatal(err)
	}
	if err := restored.SQL().QueryRow(`SELECT enabled FROM backup_destinations WHERE id='0123456789abcdef'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	var state, code string
	var finished sql.NullInt64
	if err := restored.SQL().QueryRow(`SELECT state,error_code,finished_at FROM backup_runs`).Scan(&state, &code, &finished); err != nil {
		t.Fatal(err)
	}
	if weekly != 0 || monthly != 0 || enabled != 0 || state != "failed" || code != "cancelled" || !finished.Valid {
		t.Fatalf("restored external backup settings active or run unfinished: schedule=%d/%d destination=%d run=%s/%s/%v", weekly, monthly, enabled, state, code, finished)
	}
}

func TestRestoreRefusesExistingTarget(t *testing.T) {
	path, _ := makeTestBackup(t)
	target := t.TempDir()
	marker := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(marker, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), path, target); err == nil {
		t.Fatal("Restore replaced an existing directory")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "untouched" {
		t.Fatalf("existing target changed: %q %v", data, err)
	}
}

func TestInspectRejectsCorruptArchive(t *testing.T) {
	path, _ := makeTestBackup(t)
	if err := os.WriteFile(path, []byte("not a zip archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), path); err == nil {
		t.Fatal("Inspect accepted a corrupt archive")
	}
}

func TestInspectRejectsTraversalEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unsafe.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for _, name := range []string{manifestName, "../escape", keyName} {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), path); err == nil {
		t.Fatal("Inspect accepted a traversal path")
	}
}

func TestInspectRejectsDuplicateAndSymlinkEntries(t *testing.T) {
	for name, headers := range map[string][]*zip.FileHeader{
		"duplicate": {{Name: manifestName}, {Name: keyName}, {Name: keyName}},
		"symlink":   {{Name: manifestName}, {Name: keyName}},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name+".zip")
			f, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			w := zip.NewWriter(f)
			for _, h := range headers {
				if h.Name == "" {
					t.Fatal("empty test entry name")
				}
				if h.Method == 0 {
					h.Method = zip.Deflate
				}
				if name == "symlink" && h.Name == keyName {
					h.SetMode(os.ModeSymlink | 0o777)
				} else if h.Name == keyName {
					h.SetMode(0o600)
				}
				entry, err := w.CreateHeader(h)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := entry.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := Inspect(context.Background(), path); err == nil {
				t.Fatalf("Inspect accepted %s entry", name)
			}
		})
	}
}

func TestInspectRejectsTooManyEntriesBeforeParsing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "many.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for i := 0; i < maxFiles+2; i++ {
		entry, err := w.Create(fmt.Sprintf("entry-%03d", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), path); err == nil {
		t.Fatal("Inspect accepted too many archive entries")
	}
}

func TestInspectRejectsChecksumMismatch(t *testing.T) {
	path, _ := makeTestBackup(t)
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := path + ".tampered"
	out, err := os.Create(tampered)
	if err != nil {
		reader.Close()
		t.Fatal(err)
	}
	w := zip.NewWriter(out)
	for _, f := range reader.File {
		entry, err := w.CreateHeader(&f.FileHeader)
		if err != nil {
			t.Fatal(err)
		}
		if f.Name == runtimeName {
			_, err = entry.Write([]byte("tampered runtime config"))
		} else {
			var src io.ReadCloser
			src, err = f.Open()
			if err == nil {
				_, err = io.Copy(entry, src)
				src.Close()
			}
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), tampered); err == nil {
		t.Fatal("Inspect accepted an entry with a bad checksum")
	}
}
