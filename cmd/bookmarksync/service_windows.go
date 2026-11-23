//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/debug"
)

const windowsServerServiceName = "BookmarksSync"

func parseWindowsServiceArgs(args []string) (string, error) {
	if len(args) < 2 || args[0] != "service" || args[1] != "run" {
		return "", fmt.Errorf("usage: bookmarksync service run --env-file PATH")
	}
	remaining, envFile, err := extractServeEnvironmentFile(args[2:])
	if err != nil {
		return "", err
	}
	if len(remaining) != 0 {
		return "", fmt.Errorf("unexpected Windows service argument %q", remaining[0])
	}
	if envFile == "" || !filepath.IsAbs(envFile) {
		return "", fmt.Errorf("Windows service mode requires an absolute --env-file path")
	}
	return envFile, nil
}

func runServiceCommand(_ context.Context, args []string, stderr io.Writer, getenv func(string) string) (bool, int) {
	if len(args) == 0 || args[0] != "service" {
		return false, 0
	}
	envFile, err := parseWindowsServiceArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "bookmarksync: %v\n", err)
		return true, 2
	}
	handler := applicationService{envFile: envFile, stderr: stderr, getenv: getenv}
	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintf(stderr, "bookmarksync: detect Windows service context: %v\n", err)
		return true, 1
	}
	if isService {
		err = svc.Run(windowsServerServiceName, handler)
	} else {
		err = debug.Run(windowsServerServiceName, handler)
	}
	if err != nil {
		fmt.Fprintf(stderr, "bookmarksync: Windows service stopped: %v\n", err)
		return true, 1
	}
	return true, 0
}

type applicationService struct {
	envFile string
	stderr  io.Writer
	getenv  func(string) string
}

func (s applicationService) Execute(_ []string, requests <-chan svc.ChangeRequest, statuses chan<- svc.Status) (bool, uint32) {
	stop := make(chan struct{})
	done := make(chan error, 1)
	serveArgs := []string{"--env-file", s.envFile}
	statuses <- svc.Status{State: svc.StartPending, WaitHint: 10000}
	go func() {
		done <- runManagedService(context.Background(), stop, func(ctx context.Context) error {
			return serve(ctx, serveArgs, s.stderr, s.getenv)
		})
	}()
	current := svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	statuses <- current
	for {
		select {
		case request, ok := <-requests:
			if !ok {
				close(stop)
				err := <-done
				statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: serviceExitCode(err)}
				return false, serviceExitCode(err)
			}
			switch request.Cmd {
			case svc.Interrogate:
				statuses <- current
			case svc.Stop, svc.Shutdown:
				statuses <- svc.Status{State: svc.StopPending, WaitHint: 30000}
				close(stop)
				err := <-done
				code := serviceExitCode(err)
				statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: code}
				return false, code
			}
		case err := <-done:
			code := serviceExitCode(err)
			statuses <- svc.Status{State: svc.Stopped, Win32ExitCode: code}
			return false, code
		}
	}
}

func serviceExitCode(err error) uint32 {
	if err == nil {
		return 0
	}
	return 1
}
