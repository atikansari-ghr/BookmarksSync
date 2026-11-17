// Command bookmarksync is the BookmarksSync server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/auth"
	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/floccus"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
	"github.com/atikansari-ghr/BookmarksSync/internal/version"
)

const usage = `usage:
  bookmarksync --version
  bookmarksync --help
  bookmarksync serve [--listen host:port] [--data-dir dir] [--public-url URL]

administration (all take --data-dir dir; run "<command> --help" for details):
  bookmarksync user create --username NAME --password-stdin [--admin]
  bookmarksync user list
  bookmarksync user set-active --username NAME --active=true|false
  bookmarksync credential create --username NAME --mode view|edit [--label TEXT]
                                 [--share-owner OWNER --share-folder FOLDER_ID]
  bookmarksync credential list --username NAME
  bookmarksync credential revoke --id ID
  bookmarksync share grant --owner NAME --folder-id ID --user NAME --mode view|edit
  bookmarksync share revoke --owner NAME --folder-id ID --user NAME
  bookmarksync share list --owner NAME

Passwords are only ever read from standard input (--password-stdin), never from
arguments. A credential's secret is printed once on standard output and cannot be
shown again.
`

const serveUsage = `usage: bookmarksync serve [--listen host:port] [--data-dir dir] [--public-url URL]

  --listen      address to listen on (default 127.0.0.1:8080, env BOOKMARKSYNC_LISTEN)
  --data-dir    directory for the database (default ./data, env BOOKMARKSYNC_DATA_DIR)
  --public-url  browser origin users reach the server at, e.g. https://bookmarks.example.com
                (env BOOKMARKSYNC_PUBLIC_URL; https required except loopback). Enables
                browser sign-in under /api/v1/auth; unset leaves it disabled.

Serves the health endpoints and the floccus-compatible bookmarks API. Put a
TLS-terminating proxy in front of it for anything other than local testing.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "--version", "version":
		fmt.Fprintf(stdout, "bookmarksync %s\n", version.Current())
		return 0
	case "--help", "-h", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "serve":
		if err := serve(ctx, args[1:], stderr, getenv); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprint(stdout, serveUsage)
				return 0
			}
			fmt.Fprintf(stderr, "bookmarksync: %v\n", err)
			return 1
		}
		return 0
	case "user", "credential", "share":
		return admin(ctx, args, stdin, stdout, stderr, getenv)
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
	ids := identity.New(db.SQL())
	adapter := floccus.New(ids, bookmarks.New(db.SQL()), log)
	register := []func(server.Registrar){adapter.Register}
	if cfg.PublicOrigin != "" {
		authH, err := auth.New(auth.Options{Identity: ids, PublicOrigin: cfg.PublicOrigin, Logger: log})
		if err != nil {
			_ = ln.Close()
			return err
		}
		register = append(register, authH.Register)
	} else {
		log.Warn("browser sign-in disabled: set " + config.EnvPublicURL + " to enable /api/v1/auth")
	}
	h := server.NewHandler(server.Options{
		Version: version.Current(),
		Ready:   db,
		Register: func(r server.Registrar) {
			for _, reg := range register {
				reg(r)
			}
		},
		Logger: log,
	})
	log.Info("listening", "addr", ln.Addr().String(), "version", version.Current().Version)
	if err := server.Serve(ctx, ln, h, 15*time.Second); err != nil {
		return err
	}
	log.Info("shut down cleanly")
	return nil
}
