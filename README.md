# BookmarksSync

A self-hosted bookmarks server, written in Go.

**Early development.**
- The floccus-compatible sync API has passed real-browser checks with stock floccus 5.13.0 in Chrome, Edge, Brave, and Firefox; organizer and collision cases are still under acceptance review.
- Browser sign-in, account security, bookmark workspace, dashboard, and admin interface are implemented on the development branch and await acceptance review.

## Status

Implemented:

- `bookmarksync serve` and `bookmarksync --version`; `--help` on every command
- Local SQLite storage (pure Go, no CGO) with versioned, transactional migrations, WAL, `synchronous=FULL`, foreign keys, immediate write transactions and a busy timeout
- `GET /healthz` (liveness), `GET /readyz` (reads `schema_migrations`), `GET /api/v1/version`
- JSON errors for unknown `/api` routes and wrong methods, request body/header limits, server timeouts, graceful shutdown
- Users (Argon2id password hashes), per-device revocable app credentials (256-bit secrets, only a hash is stored), shared-folder grants, administered with the CLI (`bookmarksync user|credential|share ...`)
- The bookmark domain: folders with canonical roots, ordered placements, tags, Trash (soft delete), shares, scope locks
- A Nextcloud-Bookmarks-compatible API for the stock floccus extension; see [docs/floccus-compatibility.md](docs/floccus-compatibility.md)
- Browser sign-in, enabled by `--public-url`; see [docs/authentication.md](docs/authentication.md):
  - username and password, passkeys, optional TOTP with one-time recovery codes, and linked Google/Authentik identities;
  - accounts created by an administrator only;
  - server-side sessions, revocable per device (12 hours idle, 30 days absolute);
  - CSRF and exact-origin checks, and recent reauthentication for sensitive changes.
- A country access gate applies to every HTTP route, including Floccus and health checks. Provision a MaxMind Country database or explicitly configure management CIDRs: without either, all requests (including loopback) are denied. See [docs/network-access.md](docs/network-access.md).
- Sign-in and account-security pages under `/auth/`, built with React/TypeScript and embedded in the binary. They cover passkeys, the authenticator app, recovery codes, linked accounts, active sessions, personal API tokens, Floccus connections, and optional verified email/Telegram alerts.
- Bookmark workspace pages at `/dashboard`, `/bookmarks`, `/duplicates`, and `/admin`; these include configurable dashboard widgets and themes, folder/bookmark organization, duplicate review, and public-only link checks.
- Admin health and account usage views, per-account theme defaults, sanitized activity, editable login appearance, and private PNG/JPEG/WebP logo/background uploads (up to 2 MiB).
- Compressed full-system backups with admin-managed weekly/monthly schedules, local/NFS/rclone destinations, verified publishing, per-schedule retention, optional per-destination encryption, and backup notifications; manual verify/restore tools remain available. See [docs/backup-restore.md](docs/backup-restore.md).
- Admin-managed SMTP/Telegram provider settings and backup/security alerts; users can verify their own channels and opt into account-security and sharing alerts. See [docs/notifications.md](docs/notifications.md).
- User instructions for the bookmark workspace and admin pages: [docs/workspace-guide.md](docs/workspace-guide.md).
- The versioned `/api/v1` JSON API, documented in [api/openapi.yaml](api/openapi.yaml):
  - profile, password and settings;
  - bookmarks, folders, tags, search and per-user Favorites across folder placements;
  - Trash list, restore and purge;
  - Netscape HTML and XBEL import/export;
  - shares;
  - personal API tokens with `bookmarks:read|write` and `shares:read|manage` scopes;
  - per-device Floccus connections;
  - administrator routes for accounts, resets, connections, health, settings, sanitized activity and aggregate system data, with no access to other users' bookmarks.

**Floccus compatibility is verified for the tested sync flows.** Two repeated real-server browser matrices passed in Chrome, Edge, Brave, and Firefox with stock floccus 5.13.0. The compatibility guide and browser report list the covered cases and remaining untested flows.

Still planned or awaiting acceptance:
- Login Flow v2 (the floccus "Connect" wizard; use profile import for now);
- trusted-proxy and country rules, per-destination backup encryption, and per-user recovery;
- production single-command installers and admin-triggered updates;
- automated GeoLite provisioning on Debian/LXC; final public country/proxy acceptance and Docker/Windows GeoLite scheduling remain.

See `docs/design.md` and `docs/implementation-plan.md`.

## Requirements

Go (the version in `go.mod`). No C compiler is needed. The built sign-in UI is committed under `internal/webui/static`, so Go builds and tests never need Node.js.

Node.js is only needed to:
- change the UI in `web/` (`npm ci && npm run build`, which regenerates `internal/webui/static`);
- regenerate the JS hash test vectors (`internal/bookmarks/testdata/genhashvectors.mjs`);
- run the browser harness.

## Run

```sh
BOOKMARKSYNC_MANAGEMENT_CIDRS=127.0.0.0/8,::1/128 go run ./cmd/bookmarksync serve --listen 127.0.0.1:8080 --data-dir ./data
```

Defaults: listen on `127.0.0.1:8080` (loopback only) and store data in `./data`. `BOOKMARKSYNC_LISTEN` and `BOOKMARKSYNC_DATA_DIR` set the defaults; flags override them. The data directory is created with mode `0700` and the database file `0600` on POSIX systems (Windows ignores these bits). The database must live on local disk.

**Upgrade note:** country gating is active immediately. Before upgrading, provision `BOOKMARKSYNC_GEOIP_DB` or set `BOOKMARKSYNC_MANAGEMENT_CIDRS` to the exact local/proxy client ranges that must remain reachable. An absent database with no management range blocks every request; private and loopback clients are not exempt automatically.

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

### Browser sign-in and the API

Set the public HTTPS origin that users reach (loopback `http://` is accepted only for local development), then bootstrap an administrator:

```sh
echo "a long admin password" | go run ./cmd/bookmarksync user create --username admin --password-stdin --admin --data-dir ./data
go run ./cmd/bookmarksync serve --data-dir ./data --public-url https://bookmarks.example.com
```

- **Without `--public-url`** (or `BOOKMARKSYNC_PUBLIC_URL`), only the health endpoints and the floccus API are served.
- **With it**, `/` redirects to the sign-in page at `/auth/login`, and the private `instance.key` that encrypts TOTP seeds is created in the data directory. Back it up together with the database.
- **Google and Authentik** are configured through environment variables only. Register the exact redirect URI `<public URL>/api/v1/auth/oidc/<google|authentik>/callback`.

[docs/authentication.md](docs/authentication.md) covers:
- reverse-proxy requirements;
- trusted proxy and country access setup in [docs/network-access.md](docs/network-access.md);
- provider setup;
- passkey domain rules;
- TOTP and recovery codes;
- account and factor recovery;
- sessions, API tokens and scopes;
- Floccus connections;
- the security limits.

### Upgrading from milestone 2

Migrations run automatically at startup, in a transaction. Upgrading a milestone-2 database keeps users, passwords, bookmarks, shares and existing floccus app credentials. Existing floccus profiles keep syncing with their current `bms1.…` secret; no reconfiguration is needed. Back up the data directory before upgrading.

## Test and build

```sh
gofmt -l cmd internal       # should print nothing
go vet ./...
go test ./...
go test -race ./...         # needs cgo and a C compiler (CI runs it on Ubuntu)
govulncheck ./...           # golang.org/x/vuln/cmd/govulncheck@v1.9.0, as pinned in CI
```

Sign-in UI (`web/`) and browser harness (`tests/browser/`, see its README):

```sh
(cd web && npm ci && npm run typecheck && npm test && npm run build)   # the build must leave internal/webui/static unchanged
(cd tests/browser && npm ci && npm test && npm run ui)                 # npm run ui: real-Chromium sign-in smoke
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
- `internal/identity` — users, Argon2id password hashing, app credentials, web sessions, API tokens, passkeys, TOTP and recovery codes, linked OIDC identities, the instance key
- `internal/auth` — browser sign-in, session/CSRF/origin middleware, factor and OpenID Connect routes, rate limits
- `internal/api` — the `/api/v1` user and administrator API (contract: `api/openapi.yaml`)
- `internal/interchange` — bounded Netscape HTML and XBEL import/export
- `internal/webui` — embedded sign-in and account-security pages (source in `web/`)
- `internal/bookmarks` — SQL repositories and transactional bookmark/folder/share/lock operations (protocol-agnostic)
- `internal/floccus` — Nextcloud-Bookmarks wire adapter for floccus
- `internal/backup` — compressed full-system snapshots, destination drivers, schedule and retention manager
- `internal/network` — trusted-proxy client resolution, offline country checks, and request-wide access policy
- `internal/server` — HTTP handler, JSON helpers, graceful `Serve`
- `internal/version` — build information
- `docs/floccus-compatibility.md` — what is implemented and which evidence exists for it
- `docs/authentication.md` — operator and user guide for sign-in, recovery, tokens and security limits

## Limitations

- POSIX file-permission tests run only on Linux/macOS (CI Linux job); on Windows they are skipped.
- The server speaks plain HTTP and is intended to sit behind a TLS-terminating proxy.
- Trusted proxies are supported when their CIDRs are explicitly configured; only their `X-Forwarded-For` chain is used. See [docs/network-access.md](docs/network-access.md).
- Floccus authentication is per request (HTTP Basic with a per-device app secret) and is independent of website sessions and API tokens.
- Administration is available through the web interface and CLI. Password/factor resets and emergency geo-policy recovery remain host-side operations.
- Floccus browser compatibility is verified only for the flows listed in the compatibility guide.
- Passkeys and provider sign-in were tested in Chromium with a virtual authenticator and a local fake OpenID provider only. See the untested list in `docs/authentication.md`.

## License

MIT. Copyright (c) 2026 Atik Ansari.
