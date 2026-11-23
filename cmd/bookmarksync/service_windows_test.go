//go:build windows

package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows/svc"
)

func TestParseWindowsServiceArgsRequiresAbsoluteEnvironmentFile(t *testing.T) {
	got, err := parseWindowsServiceArgs([]string{"service", "run", "--env-file", `C:\ProgramData\BookmarksSync\bookmarksync.env`})
	if err != nil {
		t.Fatal(err)
	}
	if got != `C:\ProgramData\BookmarksSync\bookmarksync.env` {
		t.Fatalf("environment file path = %q", got)
	}
}

func TestParseWindowsServiceArgsRejectsRelativeOrUnexpectedArguments(t *testing.T) {
	for _, args := range [][]string{
		{"service", "run"},
		{"service", "run", "--env-file", `relative\bookmarksync.env`},
		{"service", "run", "--env-file", `C:\ProgramData\BookmarksSync\bookmarksync.env`, "--listen", "0.0.0.0:8080"},
	} {
		if _, err := parseWindowsServiceArgs(args); err == nil {
			t.Errorf("parseWindowsServiceArgs(%q) succeeded; want error", args)
		}
	}
}

func TestApplicationServiceReportsStartFailureAndSCMStates(t *testing.T) {
	handler := applicationService{
		envFile: filepath.Join(t.TempDir(), "missing.env"),
		stderr:  io.Discard,
		getenv:  os.Getenv,
	}
	statuses := make(chan svc.Status, 4)
	serviceSpecific, exitCode := handler.Execute(nil, make(chan svc.ChangeRequest), statuses)
	if serviceSpecific || exitCode == 0 {
		t.Fatalf("Execute() = (%v, %d), want ordinary startup failure", serviceSpecific, exitCode)
	}
	want := []svc.State{svc.StartPending, svc.Running, svc.Stopped}
	for _, state := range want {
		status := <-statuses
		if status.State != state {
			t.Fatalf("reported state %v, want %v", status.State, state)
		}
	}
}
