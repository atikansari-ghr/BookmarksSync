package windowsupdate

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/updates"
)

var (
	ErrJobBusy       = errors.New("another update job is already active")
	ErrManagerClosed = errors.New("updater service is stopping")
)

type JobExecutor func(context.Context, string) error

type managedJob struct {
	tag  string
	id   string
	done chan struct{}
	err  error
}

type JobManager struct {
	mu     sync.Mutex
	ctx    context.Context
	exec   JobExecutor
	active *managedJob
	last   *managedJob
	jobs   map[string]*managedJob
	order  []string
	closed bool
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
	job := &managedJob{tag: tag, id: "bookmarksync-update-" + tag[len("build-"):], done: make(chan struct{})}
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
		if err := m.exec(ctx, job.tag); err != nil {
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
