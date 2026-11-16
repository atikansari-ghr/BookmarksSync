// Command bookmarksync is the BookmarksSync server.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
	"github.com/atikansari-ghr/BookmarksSync/internal/version"
)

const usage = `usage:
  bookmarksync --version
  bookmarksync serve [--listen host:port] [--data-dir dir]
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "--version", "version":
		fmt.Fprintf(stdout, "bookmarksync %s\n", version.Current())
		return 0
	case "serve":
		if err := serve(ctx, args[1:], stderr, getenv); err != nil {
			fmt.Fprintf(stderr, "bookmarksync: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(stderr, "bookmarksync: unknown command %q\n%s", args[0], usage)
		return 2
	}
}

func serve(ctx context.Context, args []string, stderr io.Writer, getenv func(string) string) error {
	cfg, err := config.Parse(args, getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))

	db, err := database.Open(ctx, cfg.DatabasePath())
	if err != nil {
		return err
	}
	defer db.Close()

	ln, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	h := server.NewHandler(server.Options{
		Version: version.Current(),
		Ready:   db,
		Logger:  log,
	})
	log.Info("listening", "addr", ln.Addr().String(), "version", version.Current().Version)
	if err := server.Serve(ctx, ln, h, 15*time.Second); err != nil {
		return err
	}
	log.Info("shut down cleanly")
	return nil
}
