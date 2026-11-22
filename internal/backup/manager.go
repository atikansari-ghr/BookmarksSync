package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/securefile"
	"github.com/atikansari-ghr/BookmarksSync/internal/version"
)

var ErrAlreadyRunning = errors.New("a backup is already running")
var ErrNoDestinations = errors.New("no enabled backup destinations are configured")

type Snapshot struct {
	Schedule     Schedule      `json:"schedule"`
	Destinations []Destination `json:"destinations"`
	Runs         []Run         `json:"runs"`
	Running      bool          `json:"running"`
}

type ManagerOptions struct {
	DB           *database.DB
	DataDir      string
	Build        version.Info
	Drivers      map[DestinationKind]DestinationDriver
	SnapshotLock sync.Locker
	Context      context.Context
	Now          func() time.Time
}

type Manager struct {
	db       *database.DB
	dataDir  string
	build    version.Info
	drivers  map[DestinationKind]DestinationDriver
	assetsMu sync.Locker
	ctx      context.Context
	store    *Store
	now      func() time.Time
	gate     chan struct{}
	changed  chan struct{}
	runWG    sync.WaitGroup
}

func NewManager(opts ManagerOptions) (*Manager, error) {
	if opts.DB == nil || opts.DataDir == "" {
		return nil, errors.New("backup manager requires database and data directory")
	}
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Build.Version == "" {
		opts.Build = version.Current()
	}
	if opts.Drivers == nil {
		opts.Drivers = map[DestinationKind]DestinationDriver{DestinationLocal: FilesystemDriver{}, DestinationNFS: FilesystemDriver{}, DestinationRclone: RcloneDriver{}}
	}
	return &Manager{db: opts.DB, dataDir: opts.DataDir, build: opts.Build, drivers: opts.Drivers,
		assetsMu: opts.SnapshotLock, ctx: opts.Context, store: NewStore(opts.DB.SQL(), opts.Now), now: opts.Now,
		gate: make(chan struct{}, 1), changed: make(chan struct{}, 1)}, nil
}

func (m *Manager) Snapshot(ctx context.Context) (Snapshot, error) {
	schedule, err := m.store.LoadSchedule(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	destinations, err := m.store.ListDestinations(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	for i := range destinations {
		destinations[i].RecoveryKeyAvailable = destinationRecoveryKeyAvailable(m.dataDir, destinations[i].ID)
	}
	runs, err := m.store.ListRuns(ctx, 50)
	if err != nil {
		return Snapshot{}, err
	}
	if destinations == nil {
		destinations = []Destination{}
	}
	if runs == nil {
		runs = []Run{}
	}
	return Snapshot{Schedule: schedule, Destinations: destinations, Runs: runs, Running: len(m.gate) > 0}, nil
}

func (m *Manager) UpdateSchedule(ctx context.Context, schedule Schedule) error {
	if err := m.store.SaveSchedule(ctx, schedule); err != nil {
		return err
	}
	select {
	case m.changed <- struct{}{}:
	default:
	}
	return nil
}

func (m *Manager) LoadSchedule(ctx context.Context) (Schedule, error) {
	return m.store.LoadSchedule(ctx)
}

func (m *Manager) RecoverInterrupted(ctx context.Context) (int64, error) {
	return m.store.RecoverInterrupted(ctx, m.now())
}

func (m *Manager) SaveDestination(ctx context.Context, destination Destination) (Destination, error) {
	if destination.ID != "" && destination.Encrypted {
		key, created, err := ensureDestinationKey(m.dataDir, destination.ID)
		if err != nil {
			return Destination{}, err
		}
		for i := range key {
			key[i] = 0
		}
		saved, err := m.store.SaveDestination(ctx, destination)
		if err != nil && created {
			_ = removeDestinationKey(m.dataDir, destination.ID)
		}
		saved.RecoveryKeyAvailable = err == nil && destinationRecoveryKeyAvailable(m.dataDir, saved.ID)
		return saved, err
	}
	saved, err := m.store.SaveDestination(ctx, destination)
	if err != nil || !saved.Encrypted {
		return saved, err
	}
	key, _, keyErr := ensureDestinationKey(m.dataDir, saved.ID)
	for i := range key {
		key[i] = 0
	}
	if keyErr != nil {
		_ = m.store.DeleteDestination(ctx, saved.ID)
		return Destination{}, keyErr
	}
	saved.RecoveryKeyAvailable = destinationRecoveryKeyAvailable(m.dataDir, saved.ID)
	return saved, nil
}

func (m *Manager) ExportDestinationRecoveryKey(ctx context.Context, id string) (string, error) {
	destinations, err := m.store.ListDestinations(ctx)
	if err != nil {
		return "", err
	}
	for _, d := range destinations {
		if d.ID == id {
			return ExportDestinationRecoveryKey(m.dataDir, id)
		}
	}
	return "", sql.ErrNoRows
}

func (m *Manager) DeleteDestination(ctx context.Context, id string) error {
	return m.store.DeleteDestination(ctx, id)
}

func (m *Manager) TestDestination(ctx context.Context, destination Destination) error {
	driver := m.drivers[destination.Kind]
	if driver == nil {
		return errors.New("backup destination type is unavailable")
	}
	return driver.Test(ctx, destination)
}

func (m *Manager) ScheduleChanges() <-chan struct{} { return m.changed }

func (m *Manager) StartManual() (string, error) {
	select {
	case m.gate <- struct{}{}:
	default:
		return "", ErrAlreadyRunning
	}
	destinations, err := m.store.ListDestinations(m.ctx)
	if err != nil {
		<-m.gate
		return "", errors.New("backup destinations could not be loaded")
	}
	hasEnabled := false
	for _, d := range destinations {
		hasEnabled = hasEnabled || d.Enabled
	}
	if !hasEnabled {
		<-m.gate
		return "", ErrNoDestinations
	}
	batch, err := randomHex(16)
	if err != nil {
		<-m.gate
		return "", err
	}
	m.runWG.Add(1)
	go func() {
		defer m.runWG.Done()
		defer func() { <-m.gate }()
		_ = m.runLocked(m.ctx, TriggerManual, batch)
	}()
	return batch, nil
}

func (m *Manager) Run(ctx context.Context, trigger Trigger) error {
	if trigger != TriggerManual && trigger != TriggerWeekly && trigger != TriggerMonthly {
		return errors.New("backup trigger is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.gate <- struct{}{}:
		defer func() { <-m.gate }()
	default:
		if trigger == TriggerWeekly || trigger == TriggerMonthly {
			_ = m.recordSkippedScheduledRun(ctx, trigger)
		}
		return ErrAlreadyRunning
	}
	m.runWG.Add(1)
	defer m.runWG.Done()
	batch, err := randomHex(16)
	if err != nil {
		return err
	}
	return m.runLocked(ctx, trigger, batch)
}

func (m *Manager) recordSkippedScheduledRun(ctx context.Context, trigger Trigger) error {
	destinations, err := m.store.ListDestinations(ctx)
	if err != nil {
		return err
	}
	batch, err := randomHex(16)
	if err != nil {
		return err
	}
	started := m.now().UTC()
	for _, destination := range destinations {
		if !destination.Enabled {
			continue
		}
		run, err := m.store.StartDestinationRun(ctx, batch, trigger, destination, started)
		if err != nil {
			return err
		}
		if err := m.store.FinishDestinationRun(ctx, run.ID, RunFailed, 0, "cancelled", started); err != nil {
			return err
		}
	}
	return nil
}

// Wait blocks until manual and scheduled backup runs have finished. Call after
// canceling the manager context and stopping the scheduler loop.
func (m *Manager) Wait() { m.runWG.Wait() }

func (m *Manager) runLocked(ctx context.Context, trigger Trigger, batch string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	destinations, err := m.store.ListDestinations(ctx)
	if err != nil {
		return err
	}
	enabled := make([]Destination, 0, len(destinations))
	for _, d := range destinations {
		if d.Enabled {
			enabled = append(enabled, d)
		}
	}
	if len(enabled) == 0 {
		return ErrNoDestinations
	}
	started := m.now().UTC()
	runs := make([]Run, 0, len(enabled))
	for _, d := range enabled {
		run, err := m.store.StartDestinationRun(ctx, batch, trigger, d, started)
		if err != nil {
			m.finishAll(runs, RunFailed, 0, "archive_failed")
			return errors.New("backup run could not be recorded")
		}
		runs = append(runs, run)
	}
	stage, err := securefile.CreateTempDir(os.TempDir(), "bookmarksync-scheduled-*")
	if err != nil {
		m.finishAll(runs, RunFailed, 0, "archive_failed")
		return errors.New("backup staging directory could not be created")
	}
	defer os.RemoveAll(stage)
	archivePath := filepath.Join(stage, "source.zip")
	_, err = Create(ctx, Options{DB: m.db, DataDir: m.dataDir, Output: archivePath, Build: m.build, SnapshotLock: m.assetsMu})
	if err == nil {
		_, err = Inspect(ctx, archivePath)
	}
	if err != nil {
		code := "archive_failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code = "cancelled"
		}
		m.finishAll(runs, RunFailed, 0, code)
		return errors.New("full-system backup could not be created or verified")
	}
	var runErr error
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			m.finishOne(run, RunFailed, 0, "cancelled")
			runErr = err
			continue
		}
		destination := findDestination(enabled, run.DestinationID)
		driver := m.drivers[destination.Kind]
		if driver == nil {
			m.finishOne(run, RunFailed, 0, "destination_unavailable")
			runErr = errors.New("one or more backup destinations are unavailable")
			continue
		}
		if err := driver.Test(ctx, destination); err != nil {
			code := "destination_unavailable"
			if ctx.Err() != nil {
				code = "cancelled"
			}
			m.finishOne(run, RunFailed, 0, code)
			if ctx.Err() != nil {
				runErr = ctx.Err()
				continue
			}
			runErr = errors.New("one or more backup destinations are unavailable")
			continue
		}
		publishPath := archivePath
		if destination.Encrypted {
			key, err := readDestinationKey(destinationKeyPath(m.dataDir, destination.ID))
			if err != nil {
				m.finishOne(run, RunFailed, 0, "transfer_failed")
				runErr = errors.New("one or more destination encryption keys are unavailable")
				continue
			}
			encryptedPath := filepath.Join(stage, run.ArchiveName)
			err = EncryptArchive(ctx, archivePath, encryptedPath, key)
			for i := range key {
				key[i] = 0
			}
			if err != nil {
				code := "transfer_failed"
				if ctx.Err() != nil {
					code = "cancelled"
				}
				m.finishOne(run, RunFailed, 0, code)
				runErr = errors.New("one or more destination archives could not be encrypted")
				continue
			}
			publishPath = encryptedPath
		}
		if err := driver.Publish(ctx, destination, publishPath, run.ArchiveName); err != nil {
			code := "transfer_failed"
			if strings.Contains(err.Error(), "verification") {
				code = "verification_failed"
			}
			if ctx.Err() != nil {
				code = "cancelled"
			}
			m.finishOne(run, RunFailed, 0, code)
			if ctx.Err() != nil {
				runErr = ctx.Err()
				continue
			}
			runErr = errors.New("one or more backup destinations could not be verified")
			continue
		}
		publishedInfo, statErr := os.Stat(publishPath)
		if statErr != nil || !publishedInfo.Mode().IsRegular() {
			m.finishOne(run, RunFailed, 0, "transfer_failed")
			runErr = errors.New("one or more backup archives could not be verified")
			continue
		}
		if err := m.finishOne(run, RunSucceeded, publishedInfo.Size(), ""); err != nil {
			runErr = err
			continue
		}
		if trigger == TriggerWeekly || trigger == TriggerMonthly {
			if err := m.prune(ctx, destination, trigger); err != nil {
				_ = m.store.SetRunWarning(context.Background(), run.ID, "retention_failed")
				runErr = errors.New("backup completed but retention cleanup failed")
			}
		}
	}
	return runErr
}

func findDestination(destinations []Destination, id string) Destination {
	for _, d := range destinations {
		if d.ID == id {
			return d
		}
	}
	return Destination{}
}

func (m *Manager) prune(ctx context.Context, destination Destination, trigger Trigger) error {
	schedule, err := m.store.LoadSchedule(ctx)
	if err != nil {
		return err
	}
	keep := schedule.WeeklyRetention
	if trigger == TriggerMonthly {
		keep = schedule.MonthlyRetention
	}
	old, err := m.store.ListRunsToPrune(ctx, destination.ID, trigger, keep)
	if err != nil {
		return err
	}
	driver := m.drivers[destination.Kind]
	var failures int
	for _, run := range old {
		if err := driver.Delete(ctx, destination, run.ArchiveName); err != nil {
			failures++
			continue
		}
		if err := m.store.MarkArchivePruned(ctx, run.ID); err != nil {
			failures++
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d old backup archives could not be pruned", failures)
	}
	return nil
}

func (m *Manager) finishAll(runs []Run, state RunState, size int64, code string) {
	for _, run := range runs {
		m.finishOne(run, state, size, code)
	}
}

func (m *Manager) finishOne(run Run, state RunState, size int64, code string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.store.FinishDestinationRun(ctx, run.ID, state, size, code, m.now()); err != nil {
		return fmt.Errorf("record backup result: %w", err)
	}
	return nil
}
