# BookmarksSync

A self-hosted bookmarks server, written in Go. **Early development: the floccus-compatible sync API has passed real-browser sync checks with stock floccus 5.13.0 in Chrome, Edge, Brave, and Firefox.**

## Status

Implemented:

- `bookmarksync serve` and `bookmarksync --version`; `--help` on every command
- Local SQLite storage (pure Go, no CGO) with versioned, transactional migrations, WAL, `synchronous=FULL`, foreign keys, immediate write transactions and a busy timeout
- `GET /healthz` (liveness), `GET /readyz` (reads `schema_migrations`), `GET /api/v1/version`
- JSON errors for unknown `/api` routes and wrong methods, request body/header limits, server timeouts, graceful shutdown
- Users (Argon2id password hashes), per-device revocable app credentials (256-bit secrets, only a hash is stored), shared-folder grants, administered with the CLI (`bookmarksync user|credential|share ...`)
- The bookmark domain: folders with canonical roots, ordered placements, tags, Trash (soft delete), shares, scope locks
- A Nextcloud-Bookmarks-compatible API for the stock floccus extension; see [docs/floccus-compatibility.md](docs/floccus-compatibility.md)

**Floccus compatibility is verified for the tested sync flows.** Two repeated real-server browser matrices passed in Chrome, Edge, Brave, and Firefox with stock floccus 5.13.0. The compatibility guide and browser report list the covered cases and remaining untested flows.

Not implemented yet: website login, passkeys/TOTP/OIDC, Login Flow v2 (the floccus "Connect" wizard; use profile import for now), `/api/v1` bookmark endpoints and OpenAPI, web UI, Trash restore and purge, backups, installers. See `docs/design.md` and `docs/implementation-plan.md`.

## Requirements

Go (the version in `go.mod`). No C compiler is needed. Node.js is only needed to regenerate the JS hash test vectors (`internal/bookmarks/testdata/genhashvectors.mjs`) or to run the optional browser harness.

## Run

```sh
go run ./cmd/bookmarksync serve --listen 127.0.0.1:8080 --data-dir ./data
```

Defaults: listen on `127.0.0.1:8080` (loopback only) and store data in `./data`. `BOOKMARKSYNC_LISTEN` and `BOOKMARKSYNC_DATA_DIR` set the defaults; flags override them. The data directory is created with mode `0700` and the database file `0600` on POSIX systems (Windows ignores these bits). The database must live on local disk.

Provision a user and an app credential (the password comes from standard input, the secret is printed once and never again):

```sh
echo "a long password" | go run ./cmd/bookmarksync user create --username alice --password-stdin --data-dir ./data
go run ./cmd/bookmarksync credential create --username alice --label laptop --mode edit --data-dir ./data
```

The compatibility guide covers share grants, view-only credentials and the floccus profile import.

```sh
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
curl http://127.0.0.1:8080/api/v1/version
```

## Test and build

```sh
gofmt -l cmd internal       # should print nothing
go vet ./...
go test ./...
```

Cross-builds with build information injected:

```sh
V=github.com/atikansari-ghr/BookmarksSync/internal/version
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "-s -w -X $V.Version=0.0.0-dev -X $V.Commit=$(git rev-parse --short HEAD)" -o dist/bookmarksync-linux-amd64 ./cmd/bookmarksync
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -X $V.Version=0.0.0-dev -X $V.Commit=$(git rev-parse --short HEAD)" -o dist/bookmarksync-windows-amd64.exe ./cmd/bookmarksync
```

## Layout

- `cmd/bookmarksync` — CLI entry point (`serve`, `user`, `credential`, `share`)
- `internal/config` — flag/environment parsing and validation
- `internal/database` — SQLite open, pragmas, embedded migrations (`migrations/NNNN_name.sql`)
- `internal/identity` — users, Argon2id password hashing, app credentials
- `internal/bookmarks` — SQL repositories and transactional bookmark/folder/share/lock operations (protocol-agnostic)
- `internal/floccus` — Nextcloud-Bookmarks wire adapter for floccus
- `internal/server` — HTTP handler, JSON helpers, graceful `Serve`
- `internal/version` — build information
- `docs/floccus-compatibility.md` — what is implemented and which evidence exists for it

## Limitations

- POSIX file-permission tests run only on Linux/macOS (CI Linux job); on Windows they are skipped.
- The server speaks plain HTTP and is intended to sit behind a TLS-terminating proxy.
- Floccus authentication is per request (HTTP Basic with an app secret). The `server.Options.APIMiddleware` hook is unused; website sessions arrive with the next milestone.
- Administration is CLI-only for now and must run on the server host.
- Browser behaviour of floccus against this server is unverified.

## License

MIT. Copyright (c) 2026 Atik Ansari.
