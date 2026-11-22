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
	"path/filepath"
	"syscall"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/api"
	"github.com/atikansari-ghr/BookmarksSync/internal/auth"
	"github.com/atikansari-ghr/BookmarksSync/internal/bookmarks"
	"github.com/atikansari-ghr/BookmarksSync/internal/config"
	"github.com/atikansari-ghr/BookmarksSync/internal/database"
	"github.com/atikansari-ghr/BookmarksSync/internal/floccus"
	"github.com/atikansari-ghr/BookmarksSync/internal/health"
	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/linkcheck"
	"github.com/atikansari-ghr/BookmarksSync/internal/server"
	"github.com/atikansari-ghr/BookmarksSync/internal/version"
	"github.com/atikansari-ghr/BookmarksSync/internal/webui"
)

const usage = `usage:
  bookmarksync --version
  bookmarksync --help
  bookmarksync serve [--listen host:port] [--data-dir dir] [--public-url URL]
  bookmarksync backup create --data-dir dir --output archive.zip
  bookmarksync backup verify --file archive.zip
  bookmarksync backup restore --file archive.zip --target-dir new-data-dir

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
              [--disable-public-url] [--disable-oidc-google] [--disable-oidc-authentik]

  --listen      address to listen on (default 127.0.0.1:8080, env BOOKMARKSYNC_LISTEN)
  --data-dir    directory for the database (default ./data, env BOOKMARKSYNC_DATA_DIR)
  --public-url  browser origin users reach the server at, e.g. https://bookmarks.example.com
                (env BOOKMARKSYNC_PUBLIC_URL; https required except loopback). Enables
                browser sign-in under /api/v1/auth (password, TOTP, and passkeys when
                the host is a domain name) and the sign-in pages under /auth/;
                unset leaves both disabled.
  --disable-public-url       clear a saved public origin and saved OIDC settings
  --disable-oidc-google       remove the saved Google provider on startup
  --disable-oidc-authentik    remove the saved Authentik provider on startup

OpenID Connect sign-in (requires --public-url; environment variables only, so client
secrets never appear in process listings):
  Google:    BOOKMARKSYNC_OIDC_GOOGLE_CLIENT_ID, BOOKMARKSYNC_OIDC_GOOGLE_CLIENT_SECRET
  Authentik: BOOKMARKSYNC_OIDC_AUTHENTIK_ISSUER, BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_ID,
             BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_SECRET
Register the exact redirect URI <public-url>/api/v1/auth/oidc/<google|authentik>/callback
with the provider. Users link an identity from their account after reauthenticating;
accounts are never created or matched by email.

With --public-url set, a private instance key (instance.key) is created in the data
directory to encrypt TOTP seeds. Include it in every full backup; without it users
must re-enroll their authenticator apps. Startup fails if it is malformed or not private.

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
	case "backup":
		return backupCommand(ctx, args[1:], stdout, stderr)
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
	if err := config.SaveRuntime(config.RuntimePath(cfg.DataDir), config.RuntimeConfig{ListenAddr: cfg.ListenAddr, PublicOrigin: cfg.PublicOrigin, OIDCProviders: cfg.OIDCProviders}); err != nil {
		return fmt.Errorf("save runtime configuration: %w", err)
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
	var idOpts []identity.Option
	passkeysEnabled := false
	if cfg.PublicOrigin != "" {
		// The instance key encrypts TOTP seeds. It must be private and is never
		// replaced: a malformed or exposed key stops startup (fail closed). Full
		// backups must include it.
		key, err := identity.LoadOrCreateInstanceKey(cfg.DataDir)
		if err != nil {
			_ = ln.Close()
			return err
		}
		idOpts = append(idOpts, identity.WithInstanceKey(key))
		if rp, err := identity.NewRelyingParty(cfg.PublicOrigin); err != nil {
			log.Warn("passkeys disabled", "reason", err.Error())
		} else {
			idOpts = append(idOpts, identity.WithRelyingParty(rp))
			passkeysEnabled = true
		}
	}
	ids := identity.New(db.SQL(), idOpts...)
	bm := bookmarks.New(db.SQL())
	adapter := floccus.New(ids, bm, log)
	register := []func(server.Registrar){adapter.Register}
	if cfg.PublicOrigin != "" {
		var providers []auth.OIDCProvider
		for _, p := range cfg.OIDCProviders {
			providers = append(providers, auth.OIDCProvider{ID: p.ID, Name: p.Name, Issuer: p.Issuer, ClientID: p.ClientID, ClientSecret: p.ClientSecret})
			log.Info("OpenID Connect provider enabled", "provider", p.ID, "callback", cfg.PublicOrigin+"/api/v1/auth/oidc/"+p.ID+"/callback")
		}
		authH, err := auth.New(auth.Options{Identity: ids, Factors: ids, PublicOrigin: cfg.PublicOrigin, Logger: log,
			OIDC: auth.OIDCOptions{Providers: providers, Store: ids}})
		if err != nil {
			_ = ln.Close()
			return err
		}
		// Link checks resolve stored URLs, so they run only with the browser API that can
		// submit them. Jobs interrupted by a restart are re-queued before the worker starts.
		lc := linkcheck.NewService(db.SQL(), bm, ids, linkcheck.NewChecker(linkcheck.Config{}), linkcheck.WithLogger(log))
		if err := lc.Resume(ctx); err != nil {
			_ = ln.Close()
			return err
		}
		go lc.Run(ctx)
		apiH, err := api.New(api.Options{Auth: authH, Identity: ids, Bookmarks: bm, LinkChecks: lc, Logger: log, Health: health.New(cfg.DataDir), Readiness: db, SyncErrors: adapter.SyncErrors, AssetDir: filepath.Join(cfg.DataDir, "branding"),
			System: api.SystemInfo{Version: version.Current().Version, PublicOrigin: cfg.PublicOrigin,
				PasskeysEnabled: passkeysEnabled, OIDCProviders: configuredOIDCNames(cfg.OIDCProviders)}})
		if err != nil {
			_ = ln.Close()
			return err
		}
		ui, err := webui.New(webui.Files())
		if err != nil {
			_ = ln.Close()
			return err
		}
		register = append(register, authH.Register, apiH.Register, ui.Register)
	} else {
		log.Warn("browser sign-in disabled: set " + config.EnvPublicURL + " to enable /api/v1/auth")
	}
	go purgeTrashLoop(ctx, bm, log)
	h := server.NewHandler(server.Options{
		Version:   version.Current(),
		Ready:     db,
		BodyLimit: api.BodyLimit,
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

func configuredOIDCNames(providers []config.OIDCProvider) []string {
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.ID)
	}
	return names
}

func purgeTrashLoop(ctx context.Context, bm *bookmarks.Service, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	purgeExpiredTrash(ctx, bm, log)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			purgeExpiredTrash(ctx, bm, log)
		}
	}
}

func purgeExpiredTrash(ctx context.Context, bm *bookmarks.Service, log *slog.Logger) {
	count, err := bm.PurgeExpiredTrash(ctx)
	if err != nil {
		log.Error("automatic trash cleanup failed", "error", err)
	} else if count > 0 {
		log.Info("expired trash purged", "owners", count)
	}
}
