package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRunManagedServiceCancelsWorkAndWaitsForExit(t *testing.T) {
	stop := make(chan struct{})
	started := make(chan struct{})
	finished := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runManagedService(context.Background(), stop, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(finished)
			return nil
		})
	}()
	<-started
	close(stop)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runManagedService() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("runManagedService did not wait for the server to stop")
	}
	select {
	case <-finished:
	default:
		t.Fatal("runManagedService returned before server shutdown completed")
	}
}

func TestRunManagedServiceReturnsServerError(t *testing.T) {
	want := errors.New("listener failed")
	if err := runManagedService(context.Background(), make(chan struct{}), func(context.Context) error { return want }); !errors.Is(err, want) {
		t.Fatalf("runManagedService() error = %v, want %v", err, want)
	}
}
