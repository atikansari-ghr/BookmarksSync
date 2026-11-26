package linkcheck

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck/exclude"
)

// Bounds of the queue.
const (
	MaxBookmarksPerJob = 50
	MaxActiveJobs      = 5 // queued or running jobs one user may have at a time
	workerCount        = 4

	jobQueued     = "queued"
	jobRunning    = "running"
	jobCompleted  = "completed"
	resultPending = "pending"
	idlePoll      = 30 * time.Second
)

// ErrTooManyJobs is returned when a user already has MaxActiveJobs unfinished jobs.
var ErrTooManyJobs = errors.New("too many active link checks")

// URLChecker checks one URL under a user's exclusions. *Checker implements it.
type URLChecker interface {
	Check(ctx context.Context, rawURL string, ex exclude.Set) Result
}

// SettingsReader reads a user's settings (implemented by identity.Service).
type SettingsReader interface {
	Settings(ctx context.Context, userID int64) (map[string]string, error)
}

// JobResult is the outcome for one bookmark of a job. Status is "pending" until checked.
type JobResult struct {
	BookmarkID int64
	URL        string
	Status     string
	HTTPStatus int
	Detail     string
	Redirects  int
	CheckedAt  time.Time
}

// Job is a submitted batch of checks. Done counts results that are no longer pending.
type Job struct {
	ID         int64
	Status     string
	Total      int
	Done       int
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	Results    []JobResult
}

// Service queues, runs and reports link checks. Every job is scoped to the submitting user
// and the collection or share it was submitted in.
type Service struct {
	db       *sql.DB
	bm       *bookmarks.Service
	settings SettingsReader
	checker  URLChecker
	now      func() time.Time
	log      *slog.Logger
	wake     chan struct{}
}

// Option customises a Service.
type Option func(*Service)

// WithClock sets the time source.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithLogger sets the logger used by Run.
func WithLogger(l *slog.Logger) Option { return func(s *Service) { s.log = l } }

// NewService builds a Service over db. The bookmarks service decides what a scope may see.
func NewService(db *sql.DB, bm *bookmarks.Service, settings SettingsReader, checker URLChecker, opts ...Option) *Service {
	s := &Service{db: db, bm: bm, settings: settings, checker: checker, now: time.Now, log: slog.Default(), wake: make(chan struct{}, workerCount)}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Service) ms() int64 { return s.now().UnixMilli() }

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", bookmarks.ErrInvalid, fmt.Sprintf(format, args...))
}

// authorize re-derives from committed state what the scope may do: its actor must be
// active and, for a share scope, the live share must still grant the actor (edit when
// write is true). Anything else is indistinguishable from a missing job or bookmark.
func (s *Service) authorize(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sc bookmarks.Scope, write bool) error {
	if sc.Actor == 0 {
		return fmt.Errorf("%w: link checks need a signed-in user", bookmarks.ErrForbidden)
	}
	var one int
	var err error
	if sc.RootFolderID == 0 {
		if sc.OwnerID != sc.Actor {
			return bookmarks.ErrNotFound
		}
		err = q.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id = ? AND active = 1`, sc.Actor).Scan(&one)
	} else {
		err = q.QueryRowContext(ctx, `SELECT 1 FROM shares sh
			JOIN folders f ON f.id = sh.folder_id JOIN users u ON u.id = sh.recipient_id
			JOIN users owner ON owner.id = sh.owner_id AND owner.active = 1
			WHERE sh.owner_id = ? AND sh.folder_id = ? AND sh.recipient_id = ? AND f.deleted_at IS NULL AND u.active = 1
			AND (? = 0 OR sh.mode = 'edit')`, sc.OwnerID, sc.RootFolderID, sc.Actor, boolInt(write)).Scan(&one)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return bookmarks.ErrNotFound
	}
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SubmitCheck queues a check of up to MaxBookmarksPerJob bookmarks visible in sc. It needs
// write authority like any other bookmark operation (a read-only scope, including a
// view-only share, is refused), and refuses the whole batch if any bookmark is not visible.
func (s *Service) SubmitCheck(ctx context.Context, sc bookmarks.Scope, bookmarkIDs []int64) (Job, error) {
	if sc.Actor == 0 || !sc.Writable {
		return Job{}, fmt.Errorf("%w: link checks need write access to the collection", bookmarks.ErrForbidden)
	}
	if len(bookmarkIDs) == 0 || len(bookmarkIDs) > MaxBookmarksPerJob {
		return Job{}, invalid("choose between 1 and %d bookmarks", MaxBookmarksPerJob)
	}
	seen := make(map[int64]bool, len(bookmarkIDs))
	for _, id := range bookmarkIDs {
		if id <= 0 || seen[id] {
			return Job{}, invalid("bookmark ids must be unique positive numbers")
		}
		seen[id] = true
	}
	urls := make([]string, len(bookmarkIDs))
	for i, id := range bookmarkIDs {
		b, err := s.bm.GetBookmark(ctx, sc, id)
		if err != nil {
			return Job{}, err
		}
		urls[i] = b.URL
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	if err := s.authorize(ctx, tx, sc, true); err != nil {
		return Job{}, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM link_check_jobs WHERE user_id = ? AND status IN ('queued', 'running')`, sc.Actor).Scan(&active); err != nil {
		return Job{}, err
	}
	if active >= MaxActiveJobs {
		return Job{}, ErrTooManyJobs
	}
	now := s.ms()
	res, err := tx.ExecContext(ctx, `INSERT INTO link_check_jobs(user_id, owner_id, share_folder_id, status, created_at) VALUES (?, ?, ?, 'queued', ?)`,
		sc.Actor, sc.OwnerID, sc.RootFolderID, now)
	if err != nil {
		return Job{}, err
	}
	jobID, err := res.LastInsertId()
	if err != nil {
		return Job{}, err
	}
	for i, id := range bookmarkIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO link_check_results(job_id, position, bookmark_id, url, status) VALUES (?, ?, ?, ?, 'pending')`,
			jobID, i, id, urls[i]); err != nil {
			return Job{}, err
		}
	}
	job, err := loadJob(ctx, tx, jobID)
	if err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
wakeWorkers:
	for i := 0; i < workerCount; i++ {
		select {
		case s.wake <- struct{}{}:
		default:
			break wakeWorkers
		}
	}
	return job, nil
}

// CheckStatus returns the job and its results if it belongs to the scope's actor in this
// exact collection or share; otherwise bookmarks.ErrNotFound.
func (s *Service) CheckStatus(ctx context.Context, sc bookmarks.Scope, jobID int64) (Job, error) {
	var job Job
	err := s.readTx(ctx, func(q rowQueryer) error {
		if err := s.authorize(ctx, q, sc, false); err != nil {
			return err
		}
		var one int
		err := q.QueryRowContext(ctx, `SELECT 1 FROM link_check_jobs WHERE id = ? AND user_id = ? AND owner_id = ? AND share_folder_id = ?`,
			jobID, sc.Actor, sc.OwnerID, sc.RootFolderID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return bookmarks.ErrNotFound
		}
		if err != nil {
			return err
		}
		job, err = loadJob(ctx, q, jobID)
		return err
	})
	return job, err
}

type rowQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Service) readTx(ctx context.Context, fn func(q rowQueryer) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return fn(tx)
}

func loadJob(ctx context.Context, q rowQueryer, id int64) (Job, error) {
	var j Job
	var created int64
	var started, finished sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT id, status, created_at, started_at, finished_at FROM link_check_jobs WHERE id = ?`, id).
		Scan(&j.ID, &j.Status, &created, &started, &finished)
	if err != nil {
		return Job{}, err
	}
	j.CreatedAt = time.UnixMilli(created).UTC()
	if started.Valid {
		j.StartedAt = time.UnixMilli(started.Int64).UTC()
	}
	if finished.Valid {
		j.FinishedAt = time.UnixMilli(finished.Int64).UTC()
	}
	rows, err := q.QueryContext(ctx, `SELECT bookmark_id, url, status, http_status, detail, redirects, checked_at
		FROM link_check_results WHERE job_id = ? ORDER BY position`, id)
	if err != nil {
		return Job{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var r JobResult
		var httpStatus, checked sql.NullInt64
		if err := rows.Scan(&r.BookmarkID, &r.URL, &r.Status, &httpStatus, &r.Detail, &r.Redirects, &checked); err != nil {
			return Job{}, err
		}
		r.HTTPStatus = int(httpStatus.Int64)
		if checked.Valid {
			r.CheckedAt = time.UnixMilli(checked.Int64).UTC()
		}
		if r.Status != resultPending {
			j.Done++
		}
		j.Results = append(j.Results, r)
	}
	j.Total = len(j.Results)
	return j, rows.Err()
}

// Resume re-queues jobs that were running when the process stopped. Call it once at
// start-up, before Run. Results already stored stay; only pending ones are checked again.
func (s *Service) Resume(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE link_check_jobs SET status = 'queued', started_at = NULL WHERE status = 'running'`)
	return err
}

// ProcessNext claims the oldest queued job and checks its pending results one by one,
// storing each as it finishes. It reports whether a job was claimed. If ctx ends part way,
// the interrupted URL is not recorded and the job stays 'running' for Resume to re-queue.
func (s *Service) ProcessNext(ctx context.Context) (bool, error) {
	var jobID, userID int64
	err := s.db.QueryRowContext(ctx, `UPDATE link_check_jobs SET status = 'running', started_at = ?
		WHERE id = (SELECT queued.id FROM link_check_jobs queued WHERE queued.status = 'queued'
			AND NOT EXISTS (SELECT 1 FROM link_check_jobs running WHERE running.user_id = queued.user_id AND running.status = 'running')
			ORDER BY queued.id LIMIT 1)
		RETURNING id, user_id`, s.ms()).Scan(&jobID, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = s.runJob(ctx, jobID, userID)
	if err != nil && ctx.Err() == nil {
		// A normal process error is transient from the queue's perspective. Put the
		// job back so Run's poll can retry it; completed result rows remain untouched.
		if _, resetErr := s.db.ExecContext(ctx, `UPDATE link_check_jobs SET status = 'queued', started_at = NULL WHERE id = ? AND status = 'running'`, jobID); resetErr != nil {
			return true, errors.Join(err, fmt.Errorf("requeue link-check job: %w", resetErr))
		}
	}
	return true, err
}

func (s *Service) runJob(ctx context.Context, jobID, userID int64) error {
	var ownerID, shareFolderID int64
	if err := s.db.QueryRowContext(ctx, `SELECT owner_id, share_folder_id FROM link_check_jobs WHERE id = ?`, jobID).Scan(&ownerID, &shareFolderID); err != nil {
		return err
	}
	sc := bookmarks.Scope{OwnerID: ownerID, RootFolderID: shareFolderID, Actor: userID, Writable: true}
	var active int
	if err := s.db.QueryRowContext(ctx, `SELECT active FROM users WHERE id = ?`, userID).Scan(&active); err != nil {
		return err
	}
	ex := s.exclusions(ctx, userID)
	rows, err := s.db.QueryContext(ctx, `SELECT position, url FROM link_check_results WHERE job_id = ? AND status = 'pending' ORDER BY position`, jobID)
	if err != nil {
		return err
	}
	type item struct {
		pos int
		url string
	}
	var todo []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.pos, &it.url); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, it)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, it := range todo {
		if err := ctx.Err(); err != nil {
			return err
		}
		res := Result{Status: StatusError, Detail: "user_inactive"}
		if active == 1 {
			if err := s.authorize(ctx, s.db, sc, true); err != nil {
				if errors.Is(err, bookmarks.ErrNotFound) || errors.Is(err, bookmarks.ErrForbidden) {
					return s.finishRevokedJob(ctx, jobID)
				}
				return err
			}
			res = s.checker.Check(ctx, it.url, ex)
		}
		if err := ctx.Err(); err != nil {
			return err // interrupted: do not record a cancellation as an answer
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE link_check_results SET status = ?, http_status = ?, detail = ?, redirects = ?, checked_at = ?
			WHERE job_id = ? AND position = ? AND status = 'pending'`,
			res.Status, nullInt(res.HTTPStatus), res.Detail, res.Redirects, s.ms(), jobID, it.pos); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE link_check_jobs SET status = 'completed', finished_at = ? WHERE id = ?`, s.ms(), jobID)
	return err
}

// finishRevokedJob discards pending network checks when the actor or share has lost
// write access since submission. Already completed results stay intact.
func (s *Service) finishRevokedJob(ctx context.Context, jobID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.ms()
	if _, err := tx.ExecContext(ctx, `UPDATE link_check_results SET status = 'blocked', detail = 'scope_revoked', checked_at = ?
		WHERE job_id = ? AND status = 'pending'`, now, jobID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE link_check_jobs SET status = 'completed', finished_at = ? WHERE id = ?`, now, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// exclusions loads the user's validated exclusions. An unreadable or invalid stored value
// means no exclusions (every URL is simply checked), never an error and never a wider net.
func (s *Service) exclusions(ctx context.Context, userID int64) exclude.Set {
	m, err := s.settings.Settings(ctx, userID)
	if err != nil {
		return exclude.Set{}
	}
	set, err := exclude.Parse(m[exclude.SettingKey])
	if err != nil {
		return exclude.Set{}
	}
	return set
}

// Run processes queued jobs until ctx ends, waking on new submissions and polling as a
// safety net. Call Resume first.
func (s *Service) Run(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer workers.Done()
			s.runWorker(ctx)
		}()
	}
	workers.Wait()
}

func (s *Service) runWorker(ctx context.Context) {
	ticker := time.NewTicker(idlePoll)
	defer ticker.Stop()
	for ctx.Err() == nil {
		did, err := s.ProcessNext(ctx)
		if err != nil && ctx.Err() == nil {
			s.log.Error("link check failed", "error", err)
		}
		if did && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}
