//go:build !windows

package main

import (
	"context"
	"fmt"
	"io"
)

func runServiceCommand(_ context.Context, args []string, stderr io.Writer, _ func(string) string) (bool, int) {
	if len(args) == 0 || args[0] != "service" {
		return false, 0
	}
	fmt.Fprintln(stderr, "bookmarksync: native service commands are available on Windows only")
	return true, 2
}
