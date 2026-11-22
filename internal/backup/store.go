package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type DestinationKind string

const (
	DestinationLocal  DestinationKind = "local"
	DestinationNFS    DestinationKind = "nfs"
	DestinationRclone DestinationKind = "rclone"
)

type Trigger string

const (
	TriggerManual  Trigger = "manual"
	TriggerWeekly  Trigger = "weekly"
	TriggerMonthly Trigger = "monthly"
)

type RunState string

const (
	RunRunning   RunState = "running"
	RunSucceeded RunState = "succeeded"
	RunFailed    RunState = "failed"
)

type Schedule struct {
	WeeklyEnabled    bool         `json:"weeklyEnabled"`
	MonthlyEnabled   bool         `json:"monthlyEnabled"`
	Timezone         string       `json:"timezone"`
	WeeklyDay        time.Weekday `json:"weeklyDay"`
	WeeklyHour       int          `json:"weeklyHour"`
	WeeklyMinute     int          `json:"weeklyMinute"`
	MonthlyDay       int          `json:"monthlyDay"`
	MonthlyHour      int          `json:"monthlyHour"`
	MonthlyMinute    int          `json:"monthlyMinute"`
	WeeklyRetention  int          `json:"weeklyRetention"`
	MonthlyRetention int          `json:"monthlyRetention"`
}

func DefaultSchedule() Schedule {
	return Schedule{
		Timezone: "Asia/Riyadh", WeeklyDay: time.Sunday, WeeklyHour: 3,
		MonthlyDay: 1, MonthlyHour: 4, WeeklyRetention: 8, MonthlyRetention: 12,
	}
}

type Destination struct {
	ID        string          `json:"id,omitempty"`
	Label     string          `json:"label"`
	Kind      DestinationKind `json:"kind"`
	Reference string          `json:"reference"`
	Enabled   bool            `json:"enabled"`
}

type Run struct {
	ID               int64      `json:"id"`
	BatchID          string     `json:"batchId"`
	Trigger          Trigger    `json:"trigger"`
	DestinationID    string     `json:"destinationId"`
	DestinationLabel string     `json:"destinationLabel"`
	ArchiveName      string     `json:"archiveName"`
	State            RunState   `json:"state"`
	SizeBytes        int64      `json:"sizeBytes"`
	ErrorCode        string     `json:"errorCode,omitempty"`
	StartedAt        time.Time  `json:"startedAt"`
	FinishedAt       *time.Time `json:"finishedAt,omitempty"`
}

type Store struct {
	db  *sql.DB
	now func() time.Time
}

var (
	ErrInvalidSchedule     = errors.New("backup schedule is invalid")
	ErrInvalidDestination  = errors.New("backup destination is invalid")
	ErrDestinationConflict = errors.New("backup destination conflicts with an existing destination or has backup history")
)

func NewStore(db *sql.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

func (s *Store) LoadSchedule(ctx context.Context) (Schedule, error) {
	var out Schedule
	var weekly, monthly, weekday int
	err := s.db.QueryRowContext(ctx, `SELECT weekly_enabled,monthly_enabled,timezone,weekly_day,weekly_hour,weekly_minute,monthly_day,monthly_hour,monthly_minute,weekly_retention,monthly_retention FROM backup_schedule WHERE id=1`).Scan(
		&weekly, &monthly, &out.Timezone, &weekday, &out.WeeklyHour, &out.WeeklyMinute,
		&out.MonthlyDay, &out.MonthlyHour, &out.MonthlyMinute, &out.WeeklyRetention, &out.MonthlyRetention)
	if err != nil {
		return Schedule{}, err
	}
	out.WeeklyEnabled, out.MonthlyEnabled, out.WeeklyDay = weekly != 0, monthly != 0, time.Weekday(weekday)
	return out, nil
}

func (s *Store) SaveSchedule(ctx context.Context, value Schedule) error {
	if err := validateSchedule(value); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSchedule, err)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE backup_schedule SET weekly_enabled=?,monthly_enabled=?,timezone=?,weekly_day=?,weekly_hour=?,weekly_minute=?,monthly_day=?,monthly_hour=?,monthly_minute=?,weekly_retention=?,monthly_retention=?,updated_at=? WHERE id=1`,
		boolInt(value.WeeklyEnabled), boolInt(value.MonthlyEnabled), value.Timezone, int(value.WeeklyDay), value.WeeklyHour, value.WeeklyMinute,
		value.MonthlyDay, value.MonthlyHour, value.MonthlyMinute, value.WeeklyRetention, value.MonthlyRetention, s.now().UnixMilli())
	return err
}

func validateSchedule(v Schedule) error {
	if strings.TrimSpace(v.Timezone) != v.Timezone || v.Timezone == "" {
		return errors.New("backup timezone is invalid")
	}
	if _, err := time.LoadLocation(v.Timezone); err != nil {
		return errors.New("backup timezone is invalid")
	}
	if v.WeeklyDay < time.Sunday || v.WeeklyDay > time.Saturday || v.WeeklyHour < 0 || v.WeeklyHour > 23 || v.WeeklyMinute < 0 || v.WeeklyMinute > 59 {
		return errors.New("weekly backup schedule is invalid")
	}
	if v.MonthlyDay < 1 || v.MonthlyDay > 28 || v.MonthlyHour < 0 || v.MonthlyHour > 23 || v.MonthlyMinute < 0 || v.MonthlyMinute > 59 {
		return errors.New("monthly backup schedule is invalid")
	}
	if v.WeeklyRetention < 1 || v.WeeklyRetention > 100 || v.MonthlyRetention < 1 || v.MonthlyRetention > 100 {
		return errors.New("backup retention must be between 1 and 100")
	}
	return nil
}

func (s *Store) SaveDestination(ctx context.Context, value Destination) (Destination, error) {
	value.Label = strings.TrimSpace(value.Label)
	if err := validateDestination(value); err != nil {
		return Destination{}, fmt.Errorf("%w: %v", ErrInvalidDestination, err)
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Destination{}, err
	}
	defer tx.Rollback()
	var oldKind, oldReference string
	if value.ID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT kind,reference FROM backup_destinations WHERE id=?`, value.ID).Scan(&oldKind, &oldReference); err != nil {
			return Destination{}, err
		}
	}
	var duplicate int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM backup_destinations WHERE kind=? AND reference=? AND id<>?)`, string(value.Kind), value.Reference, value.ID).Scan(&duplicate); err != nil {
		return Destination{}, err
	}
	if duplicate != 0 {
		return Destination{}, ErrDestinationConflict
	}
	if value.ID == "" {
		id, err := randomHex(16)
		if err != nil {
			return Destination{}, err
		}
		value.ID = id
		_, err = tx.ExecContext(ctx, `INSERT INTO backup_destinations(id,label,kind,reference,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
			value.ID, value.Label, string(value.Kind), value.Reference, boolInt(value.Enabled), now, now)
		if err != nil {
			return Destination{}, err
		}
		if err := tx.Commit(); err != nil {
			return Destination{}, err
		}
		return value, nil
	}
	if oldKind != string(value.Kind) || oldReference != value.Reference {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM backup_runs WHERE destination_id=?`, value.ID).Scan(&count); err != nil {
			return Destination{}, err
		}
		if count > 0 {
			return Destination{}, ErrDestinationConflict
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE backup_destinations SET label=?,kind=?,reference=?,enabled=?,updated_at=? WHERE id=?`,
		value.Label, string(value.Kind), value.Reference, boolInt(value.Enabled), now, value.ID)
	if err != nil {
		return Destination{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Destination{}, err
	}
	if count == 0 {
		return Destination{}, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return Destination{}, err
	}
	return value, nil
}

func validateDestination(v Destination) error {
	if v.Label == "" || len([]rune(v.Label)) > 80 || strings.IndexFunc(v.Label, unicode.IsControl) >= 0 || len(v.Reference) == 0 || len(v.Reference) > 1024 || strings.IndexByte(v.Reference, 0) >= 0 {
		return errors.New("backup destination label or reference is invalid")
	}
	switch v.Kind {
	case DestinationLocal, DestinationNFS:
		if !filepath.IsAbs(v.Reference) || filepath.Clean(v.Reference) != v.Reference {
			return errors.New("local backup destination must be a clean absolute path")
		}
	case DestinationRclone:
		if err := validateRcloneReference(v.Reference); err != nil {
			return err
		}
	default:
		return errors.New("unknown backup destination type")
	}
	if v.ID != "" && (len(v.ID) < 16 || len(v.ID) > 64 || !hexString.MatchString(v.ID)) {
		return errors.New("backup destination id is invalid")
	}
	return nil
}

func validateRcloneReference(reference string) error {
	remote, remotePath, ok := strings.Cut(reference, ":")
	if !ok || !rcloneRemoteName.MatchString(remote) || strings.HasPrefix(remote, "-") || len(remote) < 2 || (len(remote) == 1 && strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", rune(remote[0]))) || strings.HasPrefix(remotePath, "/") || strings.TrimSpace(remotePath) != remotePath || strings.IndexByte(reference, 0) >= 0 {
		return errors.New("rclone destination must use a configured remote and relative path")
	}
	for _, part := range strings.Split(strings.ReplaceAll(remotePath, `\`, "/"), "/") {
		if part == ".." {
			return errors.New("rclone destination path cannot traverse parent directories")
		}
	}
	return nil
}

var (
	rcloneRemoteName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	hexString        = regexp.MustCompile(`^[0-9a-f]+$`)
)

func (s *Store) ListDestinations(ctx context.Context) ([]Destination, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,label,kind,reference,enabled FROM backup_destinations ORDER BY label,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Destination
	for rows.Next() {
		var d Destination
		var enabled int
		if err := rows.Scan(&d.ID, &d.Label, &d.Kind, &d.Reference, &enabled); err != nil {
			return nil, err
		}
		d.Enabled = enabled != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) DeleteDestination(ctx context.Context, id string) error {
	if id == "" || !hexString.MatchString(id) || len(id) < 16 || len(id) > 64 {
		return errors.New("backup destination id is invalid")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM backup_destinations WHERE id=?`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) StartDestinationRun(ctx context.Context, batchID string, trigger Trigger, destination Destination, started time.Time) (Run, error) {
	if len(batchID) < 16 || len(batchID) > 64 || !hexString.MatchString(batchID) {
		return Run{}, errors.New("backup run id is invalid")
	}
	if trigger != TriggerManual && trigger != TriggerWeekly && trigger != TriggerMonthly {
		return Run{}, errors.New("backup run trigger is invalid")
	}
	if err := validateDestination(destination); err != nil || destination.ID == "" {
		return Run{}, errors.New("backup run destination is invalid")
	}
	started = started.UTC()
	archiveName := backupArchiveName(trigger, started, batchID)
	if archiveName == "" {
		return Run{}, errors.New("backup archive name is invalid")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO backup_runs(batch_id,trigger_kind,destination_id,destination_label,archive_name,state,started_at) VALUES(?,?,?,?,?,?,?)`,
		batchID, string(trigger), destination.ID, destination.Label, archiveName, string(RunRunning), started.UnixMilli())
	if err != nil {
		return Run{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Run{}, err
	}
	return Run{ID: id, BatchID: batchID, Trigger: trigger, DestinationID: destination.ID, DestinationLabel: destination.Label, ArchiveName: archiveName, State: RunRunning, StartedAt: started}, nil
}

var runErrorCodes = map[string]bool{
	"archive_failed": true, "destination_unavailable": true, "transfer_failed": true,
	"verification_failed": true, "retention_failed": true, "cancelled": true,
}

func (s *Store) FinishDestinationRun(ctx context.Context, id int64, state RunState, size int64, errorCode string, finished time.Time) error {
	if id <= 0 || (state != RunSucceeded && state != RunFailed) || size < 0 ||
		(state == RunSucceeded && errorCode != "" && errorCode != "retention_failed") || (state == RunFailed && !runErrorCodes[errorCode]) {
		return errors.New("backup run result is invalid")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE backup_runs SET state=?,size_bytes=?,error_code=?,finished_at=? WHERE id=? AND state='running'`, string(state), size, errorCode, finished.UTC().UnixMilli(), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("backup run is missing or already finished")
	}
	return nil
}

func (s *Store) RecoverInterrupted(ctx context.Context, finished time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE backup_runs SET state='failed',error_code='cancelled',finished_at=? WHERE state='running'`, finished.UTC().UnixMilli())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("backup run limit is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,batch_id,trigger_kind,destination_id,destination_label,archive_name,state,size_bytes,error_code,started_at,finished_at FROM backup_runs ORDER BY started_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var trigger, state string
		var started, finished sql.NullInt64
		if err := rows.Scan(&r.ID, &r.BatchID, &trigger, &r.DestinationID, &r.DestinationLabel, &r.ArchiveName, &state, &r.SizeBytes, &r.ErrorCode, &started, &finished); err != nil {
			return nil, err
		}
		r.Trigger, r.State = Trigger(trigger), RunState(state)
		r.StartedAt = time.UnixMilli(started.Int64).UTC()
		if finished.Valid {
			t := time.UnixMilli(finished.Int64).UTC()
			r.FinishedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRunsToPrune returns the successful archives older than the newest keep
// count for this destination and backup class. Only names recorded after a
// successful transfer are eligible for deletion.
func (s *Store) ListRunsToPrune(ctx context.Context, destinationID string, trigger Trigger, keep int) ([]Run, error) {
	if destinationID == "" || !hexString.MatchString(destinationID) || len(destinationID) < 16 || len(destinationID) > 64 ||
		(trigger != TriggerWeekly && trigger != TriggerMonthly) || keep < 1 || keep > 100 {
		return nil, errors.New("backup retention query is invalid")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,batch_id,trigger_kind,destination_id,destination_label,archive_name,state,size_bytes,error_code,started_at,finished_at FROM backup_runs WHERE destination_id=? AND trigger_kind=? AND state='succeeded' AND archive_pruned=0 ORDER BY started_at DESC,id DESC LIMIT -1 OFFSET ?`, destinationID, string(trigger), keep)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var triggerValue, state string
		var started, finished sql.NullInt64
		if err := rows.Scan(&r.ID, &r.BatchID, &triggerValue, &r.DestinationID, &r.DestinationLabel, &r.ArchiveName, &state, &r.SizeBytes, &r.ErrorCode, &started, &finished); err != nil {
			return nil, err
		}
		r.Trigger, r.State = Trigger(triggerValue), RunState(state)
		r.StartedAt = time.UnixMilli(started.Int64).UTC()
		if finished.Valid {
			t := time.UnixMilli(finished.Int64).UTC()
			r.FinishedAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) MarkArchivePruned(ctx context.Context, runID int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE backup_runs SET archive_pruned=1 WHERE id=? AND state='succeeded' AND archive_pruned=0`, runID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("backup archive is missing or already pruned")
	}
	return nil
}

func (s *Store) SetRunWarning(ctx context.Context, runID int64, warning string) error {
	if warning != "retention_failed" {
		return errors.New("backup warning is invalid")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE backup_runs SET error_code=? WHERE id=? AND state='succeeded'`, warning, runID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return errors.New("backup run is missing or not successful")
	}
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate backup identifier: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
