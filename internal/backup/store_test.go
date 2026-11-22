package backup

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/database"
)

func TestBackupStoreSeedsApprovedScheduleDefaults(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.SQL(), func() time.Time { return time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC) })
	got, err := store.LoadSchedule(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultSchedule()
	if got != want {
		t.Fatalf("default schedule = %#v, want %#v", got, want)
	}
}

func TestBackupStorePersistsValidatedSchedule(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.SQL(), time.Now)
	want := DefaultSchedule()
	want.WeeklyEnabled = true
	want.MonthlyEnabled = true
	want.WeeklyDay = time.Monday
	want.WeeklyHour = 6
	want.WeeklyMinute = 15
	want.MonthlyHour = 7
	want.MonthlyMinute = 30
	want.WeeklyRetention = 4
	want.MonthlyRetention = 6
	if err := store.SaveSchedule(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.LoadSchedule(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("saved schedule = %#v, want %#v", got, want)
	}
}

func TestBackupStoreRejectsInvalidSchedule(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.SQL(), time.Now)
	invalid := []Schedule{
		func() Schedule { s := DefaultSchedule(); s.Timezone = "../../etc/passwd"; return s }(),
		func() Schedule { s := DefaultSchedule(); s.WeeklyDay = 7; return s }(),
		func() Schedule { s := DefaultSchedule(); s.WeeklyHour = 24; return s }(),
		func() Schedule { s := DefaultSchedule(); s.MonthlyDay = 29; return s }(),
		func() Schedule { s := DefaultSchedule(); s.WeeklyRetention = 0; return s }(),
	}
	for i, schedule := range invalid {
		if err := store.SaveSchedule(context.Background(), schedule); err == nil {
			t.Errorf("invalid schedule %d was accepted: %#v", i, schedule)
		}
	}
}

func TestBackupStorePersistsDestinationAndRejectsUnsafeReferences(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.SQL(), time.Now)
	destination := Destination{Label: "NAS weekly", Kind: DestinationLocal, Reference: filepath.Join(t.TempDir(), "backups"), Enabled: true}
	created, err := store.SaveDestination(context.Background(), destination)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Label != destination.Label || created.Kind != destination.Kind || created.Reference != destination.Reference || !created.Enabled {
		t.Fatalf("created destination = %#v", created)
	}
	items, err := store.ListDestinations(context.Background())
	if err != nil || len(items) != 1 || items[0] != created {
		t.Fatalf("ListDestinations = %#v, %v", items, err)
	}
	unsafe := []Destination{
		{Label: "", Kind: DestinationLocal, Reference: filepath.Join(t.TempDir(), "x")},
		{Label: "bad", Kind: DestinationLocal, Reference: "relative/path"},
		{Label: "bad remote", Kind: DestinationRclone, Reference: "../remote:/backup"},
		{Label: "bad target", Kind: DestinationRclone, Reference: "drive:../../outside"},
		{Label: "option remote", Kind: DestinationRclone, Reference: "-remote:backups"},
		{Label: "drive-letter remote", Kind: DestinationRclone, Reference: "C:backups"},
		{Label: "unknown kind", Kind: "shell", Reference: "echo hi"},
	}
	for _, item := range unsafe {
		if _, err := store.SaveDestination(context.Background(), item); err == nil {
			t.Errorf("unsafe destination accepted: %#v", item)
		}
	}
}

func TestBackupStorePreventsRetargetingDestinationWithHistory(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.SQL(), time.Now)
	dest, err := store.SaveDestination(context.Background(), Destination{Label: "Primary", Kind: DestinationRclone, Reference: "drive:backups", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.StartDestinationRun(context.Background(), "0123456789abcdef0123456789abcdef", TriggerManual, dest, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishDestinationRun(context.Background(), run.ID, RunSucceeded, 1, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	changed := dest
	changed.Reference = "other:backups"
	if _, err := store.SaveDestination(context.Background(), changed); err == nil {
		t.Fatal("destination with history was retargeted")
	}
	dest.Label = "Renamed"
	dest.Enabled = false
	if _, err := store.SaveDestination(context.Background(), dest); err != nil {
		t.Fatalf("label/enabled update rejected: %v", err)
	}
}

func TestBackupStoreRecoversInterruptedRuns(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.SQL(), time.Now)
	dest, err := store.SaveDestination(context.Background(), Destination{Label: "Local", Kind: DestinationLocal, Reference: filepath.Join(t.TempDir(), "backup")})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC)
	if _, err := store.StartDestinationRun(context.Background(), "0123456789abcdef0123456789abcdef", TriggerManual, dest, started); err != nil {
		t.Fatal(err)
	}
	finished := started.Add(time.Hour)
	count, err := store.RecoverInterrupted(context.Background(), finished)
	if err != nil || count != 1 {
		t.Fatalf("recovered=%d err=%v", count, err)
	}
	runs, err := store.ListRuns(context.Background(), 5)
	if err != nil || len(runs) != 1 || runs[0].State != RunFailed || runs[0].ErrorCode != "cancelled" || runs[0].FinishedAt == nil || !runs[0].FinishedAt.Equal(finished) {
		t.Fatalf("recovered runs=%#v err=%v", runs, err)
	}
}

func TestBackupStoreRecordsOnlySanitizedRunState(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.SQL(), time.Now)
	dest, err := store.SaveDestination(context.Background(), Destination{Label: "Cloud", Kind: DestinationRclone, Reference: "drive:bookmarksync", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	batchID := "0123456789abcdef0123456789abcdef"
	run, err := store.StartDestinationRun(context.Background(), batchID, TriggerManual, dest, started)
	if err != nil {
		t.Fatal(err)
	}
	if !validArchiveName(run.ArchiveName) {
		t.Fatalf("generated archive name is not a safe product name: %q", run.ArchiveName)
	}
	if err := store.FinishDestinationRun(context.Background(), run.ID, RunSucceeded, 1234, "", started.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishDestinationRun(context.Background(), run.ID, RunFailed, 0, "password=secret", started.Add(2*time.Minute)); err == nil {
		t.Fatal("terminal run was changed and raw error text accepted")
	}
	runs, err := store.ListRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns = %#v, %v", runs, err)
	}
	if runs[0].State != RunSucceeded || runs[0].SizeBytes != 1234 || runs[0].DestinationLabel != dest.Label || runs[0].FinishedAt == nil {
		t.Fatalf("recorded run = %#v", runs[0])
	}
	if _, err := store.StartDestinationRun(context.Background(), "abcdefabcdefabcdefabcdefabcdefab", "invalid trigger", dest, started); err == nil {
		t.Fatal("invalid trigger accepted")
	}
}

func TestBackupStoreReturnsOnlyOldSuccessfulRunsForRetention(t *testing.T) {
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "bookmarksync.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db.SQL(), time.Now)
	first, err := store.SaveDestination(context.Background(), Destination{Label: "First", Kind: DestinationLocal, Reference: filepath.Join(t.TempDir(), "one")})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.SaveDestination(context.Background(), Destination{Label: "Second", Kind: DestinationLocal, Reference: filepath.Join(t.TempDir(), "two")})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		batch := fmt.Sprintf("%032x", i+1)
		run, err := store.StartDestinationRun(context.Background(), batch, TriggerWeekly, first, base.Add(time.Duration(i)*7*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		state, code := RunSucceeded, ""
		if i == 2 {
			state, code = RunFailed, "transfer_failed"
		}
		if err := store.FinishDestinationRun(context.Background(), run.ID, state, 123, code, base.Add(time.Duration(i)*7*24*time.Hour+time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	monthly, err := store.StartDestinationRun(context.Background(), fmt.Sprintf("%032x", 10), TriggerMonthly, first, base.Add(100*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishDestinationRun(context.Background(), monthly.ID, RunSucceeded, 123, "", base.Add(100*24*time.Hour+time.Minute)); err != nil {
		t.Fatal(err)
	}
	otherDestination, err := store.StartDestinationRun(context.Background(), fmt.Sprintf("%032x", 11), TriggerWeekly, second, base.Add(110*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishDestinationRun(context.Background(), otherDestination.ID, RunSucceeded, 123, "", base.Add(110*24*time.Hour+time.Minute)); err != nil {
		t.Fatal(err)
	}
	stale, err := store.ListRunsToPrune(context.Background(), first.ID, TriggerWeekly, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].BatchID != fmt.Sprintf("%032x", 1) || !validArchiveName(stale[0].ArchiveName) {
		t.Fatalf("stale runs = %#v, want only the oldest successful weekly run", stale)
	}
	if _, err := store.ListRunsToPrune(context.Background(), first.ID, TriggerWeekly, 0); err == nil {
		t.Fatal("zero retention was accepted")
	}
}
