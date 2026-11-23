package windowsupdate

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestJobManagerSerializesAndDeduplicatesRequests(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	var calls atomic.Int32
	manager := NewJobManager(context.Background(), func(ctx context.Context, tag string) error {
		calls.Add(1)
		started <- tag
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	defer manager.Close()
	job1, err := manager.Submit(validTag)
	if err != nil || job1 == "" {
		t.Fatalf("first Submit = %q, %v", job1, err)
	}
	if got := <-started; got != validTag {
		t.Fatalf("started tag = %q", got)
	}
	duplicate, err := manager.Submit(validTag)
	if err != nil || duplicate != job1 {
		t.Fatalf("duplicate Submit = %q, %v; want same job %q", duplicate, err, job1)
	}
	if _, err := manager.Submit("build-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, ErrJobBusy) {
		t.Fatalf("conflicting Submit error = %v, want ErrJobBusy", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("executor called %d times, want one", calls.Load())
	}
	close(release)
	if err := manager.Wait(context.Background(), job1); err != nil {
		t.Fatal(err)
	}
}

func TestJobManagerRejectsInvalidAndCancelledSubmissions(t *testing.T) {
	manager := NewJobManager(context.Background(), func(context.Context, string) error { return nil })
	if _, err := manager.Submit("latest"); !errors.Is(err, ErrUpdateUnavailable) {
		t.Fatalf("invalid tag error = %v", err)
	}
	manager.Close()
	if _, err := manager.Submit(validTag); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("closed manager error = %v", err)
	}
}

func TestJobManagerAllowsRetryAfterFailedJob(t *testing.T) {
	var calls atomic.Int32
	manager := NewJobManager(context.Background(), func(context.Context, string) error {
		if calls.Add(1) == 1 {
			return errors.New("temporary failure")
		}
		return nil
	})
	defer manager.Close()
	first, err := manager.Submit(validTag)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Wait(context.Background(), first); err == nil {
		t.Fatal("first job unexpectedly passed")
	}
	second, err := manager.Submit(validTag)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Wait(context.Background(), second); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("executor called %d times, want retry", calls.Load())
	}
}

func TestJobManagerCloseCancelsAndWaitsForActiveWork(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	cancelled := make(chan struct{}, 1)
	release := make(chan struct{})
	manager := NewJobManager(context.Background(), func(ctx context.Context, _ string) error {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			cancelled <- struct{}{}
			return ctx.Err()
		}
		close(finished)
		return nil
	})
	job, err := manager.Submit(validTag)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}
	closed := make(chan struct{})
	go func() { manager.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("Close returned before active job finished")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case <-cancelled:
		t.Fatal("service stop cancelled an in-progress update")
	default:
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after active job completed")
	}
	select {
	case <-finished:
	default:
		t.Fatal("Close returned before active job finished")
	}
	if err := manager.Wait(context.Background(), job); err != nil {
		t.Fatalf("graceful stop changed completed job result: %v", err)
	}
}
