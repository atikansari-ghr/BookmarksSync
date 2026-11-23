package main

import "context"

func runManagedService(ctx context.Context, stop <-chan struct{}, serve func(context.Context) error) error {
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(serviceCtx) }()
	select {
	case err := <-done:
		return err
	case <-stop:
		cancel()
		return <-done
	case <-ctx.Done():
		cancel()
		return <-done
	}
}
