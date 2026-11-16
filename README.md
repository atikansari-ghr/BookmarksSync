# BookmarksSync

A self-hosted bookmarks server, written in Go. **Early development: only the server foundation exists.**

## Status

Implemented:

- `bookmarksync serve` and `bookmarksync --version`
- Local SQLite storage (pure Go, no CGO) with versioned, transactional migrations, WAL, foreign keys and a busy timeout
- `GET /healthz` (liveness), `GET /readyz` (database check), `GET /api/v1/version`
- JSON errors for unknown `/api` routes and wrong methods, request body/header limits, server timeouts, graceful shutdown

Not implemented yet: bookmarks, accounts and authentication, sharing, floccus/Nextcloud Bookmarks API compatibility, web UI, backups, installers. Nothing here syncs bookmarks yet. See `docs/design.md` and `docs/implementation-plan.md`.

## Requirements

Go (the version in `go.mod`). No C compiler or Node.js is needed.

## Run

```sh
go run ./cmd/bookmarksync serve --listen 127.0.0.1:8080 --data-dir ./data
```

Defaults: listen on `127.0.0.1:8080` (loopback only) and store data in `./data`. `BOOKMARKSYNC_LISTEN` and `BOOKMARKSYNC_DATA_DIR` set the defaults; flags override them. The data directory is created with mode `0700` and the database file `0600` on POSIX systems (Windows ignores these bits). The database must live on local disk.

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

- `cmd/bookmarksync` — CLI entry point
- `internal/config` — flag/environment parsing and validation
- `internal/database` — SQLite open, pragmas, embedded migrations (`migrations/NNNN_name.sql`)
- `internal/server` — HTTP handler, JSON helpers, graceful `Serve`
- `internal/version` — build information

## Limitations

- POSIX file-permission tests run only on Linux/macOS (CI Linux job); on Windows they are skipped.
- The server speaks plain HTTP and is intended to sit behind a TLS-terminating proxy.
- The authentication hook (`server.Options.APIMiddleware`) is only an extension point; no authentication exists yet.

## License

MIT. Copyright (c) 2026 Atik Ansari.
