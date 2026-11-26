package backup

import (
	"archive/zip"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
)

type failPublishDriver struct{ publishCalls int }

func (d *failPublishDriver) Test(context.Context, Destination) error { return nil }
func (d *failPublishDriver) Publish(context.Context, Destination, string, string) error {
	d.publishCalls++
	return errors.New("private transport details")
}
func (d *failPublishDriver) Delete(context.Context, Destination, string) error { return nil }

type blockingTestDriver struct {
	entered chan struct{}
	release <-chan struct{}
}

func (d *blockingTestDriver) Test(context.Context, Destination) error {
	close(d.entered)
	<-d.release
	return nil
}
func (d *blockingTestDriver) Publish(ctx context.Context, _ Destination, source, name string) error {
	return nil
}
func (d *blockingTestDriver) Delete(context.Context, Destination, string) error { return nil }

type trackingLocker struct {
	locked         bool
	locks, unlocks int
}

func (l *trackingLocker) Lock()   { l.locked = true; l.locks++ }
func (l *trackingLocker) Unlock() { l.locked = false; l.unlocks++ }

func managerFixture(t *testing.T) (*database.DB, string, *Manager) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := config.SaveRuntime(config.RuntimePath(dir), config.RuntimeConfig{ListenAddr: "127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC) }
	m, err := NewManager(ManagerOptions{DB: db, DataDir: dir, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return db, dir, m
}

func addDestination(t *testing.T, m *Manager, label, path string) Destination {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := m.SaveDestination(context.Background(), Destination{Label: label, Kind: DestinationLocal, Reference: path, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestManagerCreatesOneVerifiedArchiveForEachEnabledDestination(t *testing.T) {
	_, dir, m := managerFixture(t)
	one := addDestination(t, m, "one", filepath.Join(dir, "one"))
	two := addDestination(t, m, "two", filepath.Join(dir, "two"))
	if err := m.Run(context.Background(), TriggerWeekly); err != nil {
		t.Fatal(err)
	}
	snapshot, err := m.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Runs) != 2 {
		t.Fatalf("run history has %d records, want 2: %#v", len(snapshot.Runs), snapshot.Runs)
	}
	if snapshot.Runs[0].BatchID != snapshot.Runs[1].BatchID || snapshot.Runs[0].State != RunSucceeded || snapshot.Runs[1].State != RunSucceeded {
		t.Fatalf("destinations did not share one successful batch: %#v", snapshot.Runs)
	}
	for _, d := range []Destination{one, two} {
		entries, err := os.ReadDir(d.Reference)
		if err != nil || len(entries) != 1 || !validArchiveName(entries[0].Name()) {
			t.Fatalf("destination %s has unexpected archive list %#v, err=%v", d.Label, entries, err)
		}
		if _, err := Inspect(context.Background(), filepath.Join(d.Reference, entries[0].Name())); err != nil {
			t.Fatalf("destination %s archive failed verification: %v", d.Label, err)
		}
	}
}

func TestManagerReportsGenericRunCompletionForSuccessAndFailure(t *testing.T) {
	_, dir, m := managerFixture(t)
	var calls int
	var gotTrigger Trigger
	var gotErr error
	m.onRunComplete = func(trigger Trigger, err error) { calls++; gotTrigger = trigger; gotErr = err }
	addDestination(t, m, "success", filepath.Join(dir, "success"))
	if err := m.Run(context.Background(), TriggerWeekly); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || gotTrigger != TriggerWeekly || gotErr != nil {
		t.Fatalf("success callback calls=%d trigger=%s err=%v", calls, gotTrigger, gotErr)
	}
	m.drivers[DestinationLocal] = &failPublishDriver{}
	if err := m.Run(context.Background(), TriggerManual); err == nil {
		t.Fatal("failed publish unexpectedly succeeded")
	}
	if calls != 2 || gotTrigger != TriggerManual || gotErr == nil {
		t.Fatalf("failure callback calls=%d trigger=%s err=%v", calls, gotTrigger, gotErr)
	}
}

func TestManagerEncryptsOnlyConfiguredDestinationAndKeepsRecoveryKeyOutsideArchive(t *testing.T) {
	_, dir, m := managerFixture(t)
	plain := addDestination(t, m, "plain", filepath.Join(dir, "plain"))
	encryptedPath := filepath.Join(dir, "encrypted")
	if err := os.MkdirAll(encryptedPath, 0o700); err != nil {
		t.Fatal(err)
	}
	encrypted, err := m.SaveDestination(context.Background(), Destination{Label: "encrypted", Kind: DestinationLocal, Reference: encryptedPath, Enabled: true, Encrypted: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := m.Snapshot(context.Background())
	if err != nil || len(snapshot.Destinations) != 2 {
		t.Fatalf("snapshot = %#v, %v", snapshot, err)
	}
	for _, destination := range snapshot.Destinations {
		if destination.ID == encrypted.ID && (!destination.Encrypted || !destination.RecoveryKeyAvailable) {
			t.Fatalf("encrypted destination lacks recovery key status: %#v", destination)
		}
	}
	if err := m.Run(context.Background(), TriggerManual); err != nil {
		t.Fatal(err)
	}
	plainEntries, err := os.ReadDir(plain.Reference)
	if err != nil || len(plainEntries) != 1 || filepath.Ext(plainEntries[0].Name()) != ".zip" {
		t.Fatalf("plain destination entries=%#v err=%v", plainEntries, err)
	}
	if _, err := Inspect(context.Background(), filepath.Join(plain.Reference, plainEntries[0].Name())); err != nil {
		t.Fatalf("plain archive failed verification: %v", err)
	}
	encryptedEntries, err := os.ReadDir(encrypted.Reference)
	if err != nil || len(encryptedEntries) != 1 || filepath.Ext(encryptedEntries[0].Name()) != ".enc" || !validArchiveName(encryptedEntries[0].Name()) {
		t.Fatalf("encrypted destination entries=%#v err=%v", encryptedEntries, err)
	}
	keyText, err := m.ExportDestinationRecoveryKey(context.Background(), encrypted.ID)
	if err != nil {
		t.Fatal(err)
	}
	key, err := hex.DecodeString(keyText)
	if err != nil {
		t.Fatal(err)
	}
	decrypted := filepath.Join(t.TempDir(), "restored.zip")
	if err := DecryptArchive(context.Background(), filepath.Join(encrypted.Reference, encryptedEntries[0].Name()), decrypted, key); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), decrypted); err != nil {
		t.Fatalf("decrypted encrypted-destination archive failed verification: %v", err)
	}
	zipReader, err := zip.OpenReader(filepath.Join(plain.Reference, plainEntries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer zipReader.Close()
	for _, file := range zipReader.File {
		if strings.HasPrefix(file.Name, "backup-keys/") {
			t.Fatalf("recovery key was included in archive entry %q", file.Name)
		}
	}
}

func TestManagerContinuesAfterOneDestinationFailsAndHidesDriverError(t *testing.T) {
	_, dir, m := managerFixture(t)
	failing := &failPublishDriver{}
	m.drivers[DestinationNFS] = failing
	first, err := m.SaveDestination(context.Background(), Destination{Label: "A failed", Kind: DestinationNFS, Reference: filepath.Join(dir, "unused"), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	second := addDestination(t, m, "B works", filepath.Join(dir, "works"))
	err = m.Run(context.Background(), TriggerManual)
	if err == nil || err.Error() != "one or more backup destinations could not be verified" || err.Error() == "private transport details" {
		t.Fatalf("Run returned unsafe or missing summary: %v", err)
	}
	if failing.publishCalls != 1 {
		t.Fatalf("failing destination publish calls = %d", failing.publishCalls)
	}
	runs, err := m.store.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}
	states := map[string]RunState{}
	for _, run := range runs {
		states[run.DestinationID] = run.State
	}
	if states[first.ID] != RunFailed || states[second.ID] != RunSucceeded {
		t.Fatalf("destination outcomes = %#v", states)
	}
}

func TestManagerRejectsRunWithoutEnabledDestinations(t *testing.T) {
	_, _, m := managerFixture(t)
	if err := m.Run(context.Background(), TriggerMonthly); err == nil {
		t.Fatal("Run accepted no configured destinations")
	}
	runs, err := m.store.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("runs=%#v err=%v", runs, err)
	}
}

func TestManagerRetentionRemovesOnlyOlderTrackedWeeklyArchives(t *testing.T) {
	_, dir, m := managerFixture(t)
	destination := addDestination(t, m, "retained", filepath.Join(dir, "retained"))
	schedule := DefaultSchedule()
	schedule.WeeklyRetention = 1
	if err := m.UpdateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := m.Run(context.Background(), TriggerWeekly); err != nil {
			t.Fatalf("weekly run %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(destination.Reference)
	if err != nil || len(entries) != 1 {
		t.Fatalf("retained archive count=%d, entries=%#v err=%v", len(entries), entries, err)
	}
	if !validArchiveName(entries[0].Name()) {
		t.Fatalf("unexpected remaining file %q", entries[0].Name())
	}
	runs, err := m.store.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 3 {
		t.Fatalf("history rows=%d err=%v, want all three history rows kept", len(runs), err)
	}
	old, err := m.store.ListRunsToPrune(context.Background(), destination.ID, TriggerWeekly, 1)
	if err != nil || len(old) != 0 {
		t.Fatalf("prune list=%#v err=%v, already-pruned archives should not be retried", old, err)
	}
}

func TestManagerSerializesManualRunAndRejectsOverlappingRun(t *testing.T) {
	_, dir, m := managerFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	blocker := &blockingTestDriver{entered: entered, release: release}
	m.drivers[DestinationNFS] = blocker
	if _, err := m.SaveDestination(context.Background(), Destination{Label: "slow", Kind: DestinationNFS, Reference: filepath.Join(dir, "unused"), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartManual(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first manual run did not reach destination test")
	}
	if _, err := m.StartManual(); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("overlapping run error = %v, want ErrAlreadyRunning", err)
	}
	if err := m.Run(context.Background(), TriggerWeekly); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("scheduled run overlapping a manual run error = %v, want ErrAlreadyRunning", err)
	}
	if runs, err := m.store.ListRuns(context.Background(), 10); err != nil || len(runs) != 2 || runs[0].State != RunFailed || runs[0].ErrorCode != "cancelled" {
		t.Fatalf("skipped scheduled run not recorded: %#v, %v", runs, err)
	}
	close(release)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if len(m.gate) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("manual backup did not finish")
}

type intermittentDeleteDriver struct {
	failures map[string]bool
	removed  []string
}

func (d *intermittentDeleteDriver) Test(context.Context, Destination) error { return nil }
func (d *intermittentDeleteDriver) Publish(context.Context, Destination, string, string) error {
	return nil
}
func (d *intermittentDeleteDriver) Delete(_ context.Context, _ Destination, name string) error {
	if d.failures[name] {
		return errors.New("delete failed")
	}
	d.removed = append(d.removed, name)
	return nil
}

func TestManagerRetentionContinuesAfterOneDeleteFailure(t *testing.T) {
	_, dir, m := managerFixture(t)
	schedule := DefaultSchedule()
	schedule.WeeklyRetention = 1
	if err := m.UpdateSchedule(context.Background(), schedule); err != nil {
		t.Fatal(err)
	}
	dest := addDestination(t, m, "remote", filepath.Join(dir, "archive-target"))
	driver := &intermittentDeleteDriver{failures: map[string]bool{}}
	m.drivers[DestinationLocal] = driver
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		run, err := m.store.StartDestinationRun(context.Background(), fmt.Sprintf("%032x", i+1), TriggerWeekly, dest, base.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if err := m.store.FinishDestinationRun(context.Background(), run.ID, RunSucceeded, 1, "", base.Add(time.Duration(i)*time.Hour+time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	old, err := m.store.ListRunsToPrune(context.Background(), dest.ID, TriggerWeekly, 1)
	if err != nil || len(old) != 3 {
		t.Fatalf("old=%d err=%v", len(old), err)
	}
	driver.failures[old[0].ArchiveName] = true
	if err := m.prune(context.Background(), dest, TriggerWeekly); err == nil {
		t.Fatal("retention failure was hidden")
	}
	old, err = m.store.ListRunsToPrune(context.Background(), dest.ID, TriggerWeekly, 1)
	if err != nil || len(old) != 1 || len(driver.removed) != 2 {
		t.Fatalf("remaining old=%#v removed=%v err=%v", old, driver.removed, err)
	}
}

func TestManagerCancellationDuringDestinationTestIsRecorded(t *testing.T) {
	_, dir, m := managerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	m.drivers[DestinationNFS] = cancelAwareDriver{entered: entered}
	if _, err := m.SaveDestination(context.Background(), Destination{Label: "slow", Kind: DestinationNFS, Reference: filepath.Join(dir, "unused"), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx, TriggerManual) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not reach destination test")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled run error = %v", err)
	}
	runs, err := m.store.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].State != RunFailed || runs[0].ErrorCode != "cancelled" {
		t.Fatalf("cancelled run record = %#v err=%v", runs, err)
	}
}

type cancelAwareDriver struct{ entered chan struct{} }

func (d cancelAwareDriver) Test(ctx context.Context, _ Destination) error {
	close(d.entered)
	<-ctx.Done()
	return ctx.Err()
}
func (d cancelAwareDriver) Publish(context.Context, Destination, string, string) error { return nil }
func (d cancelAwareDriver) Delete(context.Context, Destination, string) error          { return nil }

func TestManagerSnapshotLockCoversArchiveSnapshotAndReleases(t *testing.T) {
	db, dir, _ := managerFixture(t)
	lock := &trackingLocker{}
	m, err := NewManager(ManagerOptions{DB: db, DataDir: dir, SnapshotLock: lock})
	if err != nil {
		t.Fatal(err)
	}
	addDestination(t, m, "local", filepath.Join(dir, "locked"))
	if err := m.Run(context.Background(), TriggerManual); err != nil {
		t.Fatal(err)
	}
	if lock.locks != 1 || lock.unlocks != 1 || lock.locked {
		t.Fatalf("snapshot lock counts=%d/%d currentlyLocked=%v", lock.locks, lock.unlocks, lock.locked)
	}
}
