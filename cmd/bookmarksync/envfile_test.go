package main

import (
	"reflect"
	"testing"
)

func TestExtractServeEnvironmentFile(t *testing.T) {
	args, path, err := extractServeEnvironmentFile([]string{"--listen", "127.0.0.1:8080", "--env-file", "C:/ProgramData/BookmarksSync/bookmarksync.env", "--data-dir=data"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "C:/ProgramData/BookmarksSync/bookmarksync.env" {
		t.Fatalf("environment file path = %q", path)
	}
	want := []string{"--listen", "127.0.0.1:8080", "--data-dir=data"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("remaining args = %#v, want %#v", args, want)
	}
}

func TestExtractServeEnvironmentFileRejectsMissingAndDuplicatePaths(t *testing.T) {
	cases := [][]string{
		{"--env-file"},
		{"--env-file", "first.env", "--env-file", "second.env"},
		{"--env-file="},
	}
	for _, args := range cases {
		if _, _, err := extractServeEnvironmentFile(args); err == nil {
			t.Errorf("extractServeEnvironmentFile(%q) succeeded; want error", args)
		}
	}
}
