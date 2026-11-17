# Repository Guidelines

## Project Structure & Module Organization

BookmarksSync is a Go service with local SQLite storage. The CLI lives in `cmd/bookmarksync/`; focused backend packages and embedded SQL migrations live in `internal/`. Go tests sit beside implementation files as `*_test.go`.

`tests/browser/` contains isolated stock-floccus extension tests. `docs/design.md` defines the approved behavior; `docs/implementation-plan.md` tracks milestones. `mocscreenshots/` contains UI references, not screenshots of a completed application. Keep local tools, databases, and worker scratch files in ignored directories.

## Build, Test, and Development Commands

- `go run ./cmd/bookmarksync serve --listen 127.0.0.1:8080 --data-dir ./data`: start locally.
- `go test ./...`: run backend tests against temporary databases.
- `go vet ./...`: check Go code; `gofmt -l cmd internal` should print nothing.
- `go build ./cmd/bookmarksync`: build the CLI/server.
- In `tests/browser/`, run `npm ci`, then `npm test` for harness unit tests. Follow its README for extension assets and real-browser smoke commands.

Use the Go version in `go.mod`. Current health endpoints are `/healthz`, `/readyz`, and `/api/v1/version`. Check the README for implemented features before assuming a planned feature works.

## Coding Style & Naming Conventions

Use `gofmt` for Go indentation and formatting. Keep feature packages focused and reuse domain services across protocol adapters. Follow existing JavaScript module style in browser tests. Avoid unrelated formatting changes.

## Testing Guidelines

Use Go's `testing`/`httptest` packages with real temporary SQLite databases. Cover authorization failures, persistence, and transactional behavior. Add reproducing regression tests before bug fixes. No arbitrary coverage percentage is configured. Browser tests must use disposable profiles; distinguish PASS, FAIL, BLOCKED, and PENDING. Protocol tests alone do not establish real floccus compatibility.

## Commit & Pull Request Guidelines

Follow the history's concise imperative subjects, such as `Add bookmark import validation`. Keep commits focused.

Pull requests should explain the change, link relevant issues, and report validation performed. Include screenshots for visible interface changes. Identify untested behavior explicitly.

## Attribution & Local Drafts

Use `atikansari <atikansari@ansariz.biz>` as the Git author and credit Atik Ansari alone in published material. Do not add assistant coauthor trailers or generation notices. License copyright lines must use `Copyright (c) <year> Atik Ansari`.

Keep social media drafts in `linkedin/`. Never commit or push them, and never add that directory to a repository's `.gitignore`; it is excluded globally.
