package windowsupdate

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/geolite"
	"github.com/atikansari-ghr/BookmarksSync/internal/updateipc"
	"github.com/atikansari-ghr/BookmarksSync/internal/updates"
)

var (
	ErrJobBusy       = errors.New("another update job is already active")
	ErrManagerClosed = errors.New("updater service is stopping")
)

type JobExecutor func(context.Context, string) error

type managedJob struct {
	tag     string
	id      string
	done    chan struct{}
	err     error
	execute func(context.Context) error
}

type JobManager struct {
	mu      sync.Mutex
	ctx     context.Context
	exec    JobExecutor
	active  *managedJob
	last    *managedJob
	jobs    map[string]*managedJob
	order   []string
	closed  bool
	geoLite *GeoLiteManager
}

func (m *JobManager) SetGeoLiteManager(manager *GeoLiteManager) {
	m.mu.Lock()
	m.geoLite = manager
	m.mu.Unlock()
}

func (m *JobManager) GeoLiteStatus(ctx context.Context) (geolite.Status, error) {
	m.mu.Lock()
	manager := m.geoLite
	m.mu.Unlock()
	if manager == nil {
		return geolite.Status{}, ErrUpdateUnavailable
	}
	status, err := manager.Status(ctx)
	if err != nil {
		return status, err
	}
	m.mu.Lock()
	if m.active != nil && strings.HasPrefix(m.active.id, "bookmarksync-geolite-") {
		status.Running = true
	}
	m.mu.Unlock()
	return status, nil
}

func (m *JobManager) SubmitGeoLite(ctx context.Context, request updateipc.GeoLiteRequest) (updateipc.GeoLiteResponse, error) {
	m.mu.Lock()
	manager := m.geoLite
	if m.closed || manager == nil {
		m.mu.Unlock()
		return updateipc.GeoLiteResponse{}, ErrManagerClosed
	}
	if request.Operation == updateipc.GeoLiteOperationStatus {
		m.mu.Unlock()
		status, err := manager.Status(ctx)
		if err != nil {
			return updateipc.GeoLiteResponse{OK: false, ErrorCode: "unavailable"}, err
		}
		return updateipc.GeoLiteResponse{OK: true, Status: &status}, nil
	}
	if m.active != nil {
		m.mu.Unlock()
		return updateipc.GeoLiteResponse{OK: false, ErrorCode: "busy"}, ErrJobBusy
	}
	if request.Operation == updateipc.GeoLiteOperationConfigure || request.Operation == updateipc.GeoLiteOperationClear {
		if request.Operation == updateipc.GeoLiteOperationConfigure && request.Config == nil {
			m.mu.Unlock()
			return updateipc.GeoLiteResponse{OK: false, ErrorCode: "invalid_request"}, ErrUpdateUnavailable
		}
		job := &managedJob{id: "bookmarksync-geolite-config", done: make(chan struct{})}
		m.active = job
		m.mu.Unlock()
		var err error
		if request.Operation == updateipc.GeoLiteOperationConfigure {
			err = manager.Configure(ctx, *request.Config)
		} else {
			err = manager.Clear(ctx)
		}
		job.err = err
		close(job.done)
		m.mu.Lock()
		if m.active == job {
			m.active = nil
		}
		m.mu.Unlock()
		if err != nil {
			code := "unavailable"
			if errors.Is(err, ErrJobBusy) {
				code = "busy"
			}
			if errors.Is(err, geolite.ErrInvalidConfig) {
				code = "invalid_request"
			}
			return updateipc.GeoLiteResponse{OK: false, ErrorCode: code}, err
		}
		return updateipc.GeoLiteResponse{OK: true}, nil
	}
	if request.Operation != updateipc.GeoLiteOperationRun {
		m.mu.Unlock()
		return updateipc.GeoLiteResponse{OK: false, ErrorCode: "invalid_request"}, ErrUpdateUnavailable
	}
	if m.ctx.Err() != nil {
		m.mu.Unlock()
		return updateipc.GeoLiteResponse{OK: false, ErrorCode: "unavailable"}, ErrManagerClosed
	}
	id := "bookmarksync-geolite-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	job := &managedJob{id: id, done: make(chan struct{}), execute: func(ctx context.Context) error { _, err := manager.Run(ctx); return err }}
	m.active, m.last, m.jobs[id] = job, job, job
	m.order = append(m.order, id)
	if len(m.order) > 32 {
		delete(m.jobs, m.order[0])
		m.order = m.order[1:]
	}
	m.mu.Unlock()
	go m.run(job)
	return updateipc.GeoLiteResponse{OK: true, Job: id}, nil
}

func NewJobManager(parent context.Context, execute JobExecutor) *JobManager {
	if parent == nil {
		parent = context.Background()
	}
	return &JobManager{ctx: context.WithoutCancel(parent), exec: execute, jobs: make(map[string]*managedJob)}
}

func (m *JobManager) Submit(tag string) (string, error) {
	if updates.ValidateApprovedTag(tag) != nil || m.exec == nil {
		return "", ErrUpdateUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return "", ErrManagerClosed
	}
	if m.active != nil {
		if m.active.tag == tag {
			return m.active.id, nil
		}
		return "", ErrJobBusy
	}
	if m.last != nil && m.last.tag == tag {
		select {
		case <-m.last.done:
			if m.last.err == nil {
				return m.last.id, nil
			}
		default:
			return m.last.id, nil
		}
	}
	job := &managedJob{tag: tag, id: "bookmarksync-update-" + tag[len("build-"):], done: make(chan struct{}), execute: func(ctx context.Context) error { return m.exec(ctx, tag) }}
	m.active, m.last, m.jobs[job.id] = job, job, job
	knownID := false
	for _, id := range m.order {
		if id == job.id {
			knownID = true
			break
		}
	}
	if !knownID {
		m.order = append(m.order, job.id)
		if len(m.order) > 32 {
			delete(m.jobs, m.order[0])
			m.order = m.order[1:]
		}
	}
	go m.run(job)
	return job.id, nil
}

func (m *JobManager) run(job *managedJob) {
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Minute)
	defer cancel()
	func() {
		defer func() {
			if recover() != nil {
				job.err = ErrUpdateUnavailable
			}
		}()
		if job.execute == nil || job.execute(ctx) != nil {
			job.err = ErrUpdateUnavailable
		}
	}()
	close(job.done)
	m.mu.Lock()
	if m.active == job {
		m.active = nil
	}
	m.mu.Unlock()
}

func (m *JobManager) Wait(ctx context.Context, id string) error {
	m.mu.Lock()
	job := m.jobs[id]
	m.mu.Unlock()
	if job == nil {
		return ErrUpdateUnavailable
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-job.done:
		return job.err
	}
}

func (m *JobManager) Close() {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
	}
	job := m.active
	m.mu.Unlock()
	if job != nil {
		<-job.done
	}
}
