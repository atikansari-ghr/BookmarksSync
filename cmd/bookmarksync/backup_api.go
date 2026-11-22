package main

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/api"
	"github.com/atikansari-ghr/BookmarksSync/internal/backup"
)

type backupAPI struct{ manager *backup.Manager }

func (b backupAPI) Snapshot(ctx context.Context) (api.BackupSnapshot, error) {
	snapshot, err := b.manager.Snapshot(ctx)
	if err != nil {
		return api.BackupSnapshot{}, err
	}
	out := api.BackupSnapshot{Schedule: apiSchedule(snapshot.Schedule), Destinations: make([]api.BackupDestination, 0, len(snapshot.Destinations)), Runs: make([]api.BackupRun, 0, len(snapshot.Runs)), Running: snapshot.Running}
	for _, value := range snapshot.Destinations {
		out.Destinations = append(out.Destinations, apiDestination(value))
	}
	for _, value := range snapshot.Runs {
		out.Runs = append(out.Runs, apiRun(value))
	}
	return out, nil
}

func (b backupAPI) UpdateSchedule(ctx context.Context, value api.BackupSchedule) error {
	err := b.manager.UpdateSchedule(ctx, backup.Schedule{WeeklyEnabled: value.WeeklyEnabled, MonthlyEnabled: value.MonthlyEnabled,
		Timezone: value.Timezone, WeeklyDay: time.Weekday(value.WeeklyDay), WeeklyHour: value.WeeklyHour, WeeklyMinute: value.WeeklyMinute,
		MonthlyDay: value.MonthlyDay, MonthlyHour: value.MonthlyHour, MonthlyMinute: value.MonthlyMinute,
		WeeklyRetention: value.WeeklyRetention, MonthlyRetention: value.MonthlyRetention})
	if errors.Is(err, backup.ErrInvalidSchedule) {
		return api.ErrBackupInvalidSchedule
	}
	return err
}

func (b backupAPI) SaveDestination(ctx context.Context, value api.BackupDestination) (api.BackupDestination, error) {
	saved, err := b.manager.SaveDestination(ctx, backup.Destination{ID: value.ID, Label: value.Label, Kind: backup.DestinationKind(value.Kind), Reference: value.Reference, Enabled: value.Enabled, Encrypted: value.Encrypted})
	if err != nil {
		return api.BackupDestination{}, mapBackupDestinationError(err)
	}
	return apiDestination(saved), nil
}

func (b backupAPI) DeleteDestination(ctx context.Context, id string) error {
	err := b.manager.DeleteDestination(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return api.ErrBackupDestinationNotFound
	}
	return err
}

func (b backupAPI) ExportDestinationRecoveryKey(ctx context.Context, id string) (string, error) {
	key, err := b.manager.ExportDestinationRecoveryKey(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", api.ErrBackupDestinationNotFound
	}
	return key, err
}

func mapBackupDestinationError(err error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return api.ErrBackupDestinationNotFound
	case errors.Is(err, backup.ErrInvalidDestination):
		return api.ErrBackupInvalidDestination
	case errors.Is(err, backup.ErrDestinationConflict):
		return api.ErrBackupDestinationConflict
	default:
		return err
	}
}

func (b backupAPI) TestDestination(ctx context.Context, value api.BackupDestination) error {
	return b.manager.TestDestination(ctx, backup.Destination{ID: value.ID, Label: value.Label, Kind: backup.DestinationKind(value.Kind), Reference: value.Reference, Enabled: value.Enabled, Encrypted: value.Encrypted})
}

func (b backupAPI) StartManual() (string, error) {
	batch, err := b.manager.StartManual()
	switch {
	case errors.Is(err, backup.ErrAlreadyRunning):
		return "", api.ErrBackupAlreadyRunning
	case errors.Is(err, backup.ErrNoDestinations):
		return "", api.ErrBackupNoDestinations
	default:
		return batch, err
	}
}

func apiSchedule(value backup.Schedule) api.BackupSchedule {
	return api.BackupSchedule{WeeklyEnabled: value.WeeklyEnabled, MonthlyEnabled: value.MonthlyEnabled, Timezone: value.Timezone,
		WeeklyDay: int(value.WeeklyDay), WeeklyHour: value.WeeklyHour, WeeklyMinute: value.WeeklyMinute, MonthlyDay: value.MonthlyDay,
		MonthlyHour: value.MonthlyHour, MonthlyMinute: value.MonthlyMinute, WeeklyRetention: value.WeeklyRetention, MonthlyRetention: value.MonthlyRetention}
}

func apiDestination(value backup.Destination) api.BackupDestination {
	return api.BackupDestination{ID: value.ID, Label: value.Label, Kind: string(value.Kind), Reference: value.Reference, Enabled: value.Enabled,
		Encrypted: value.Encrypted, RecoveryKeyAvailable: value.RecoveryKeyAvailable}
}

func apiRun(value backup.Run) api.BackupRun {
	return api.BackupRun{ID: value.ID, BatchID: value.BatchID, Trigger: string(value.Trigger), DestinationID: value.DestinationID,
		DestinationLabel: value.DestinationLabel, ArchiveName: value.ArchiveName, State: string(value.State), SizeBytes: value.SizeBytes,
		ErrorCode: value.ErrorCode, StartedAt: value.StartedAt, FinishedAt: value.FinishedAt}
}
