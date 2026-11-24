# Windows GeoLite Admin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Configure and run safe Windows GeoLite2 Country updates through Admin Settings without shell-based routine setup.

**Architecture:** Add a Windows GeoLite manager behind the existing LocalSystem updater service and its restricted named pipe. The Admin API validates and authorizes GUI operations, while the updater owns protected credentials, a fixed SYSTEM scheduled task, staging, validation, atomic replacement, and service restart. Keep the UI status sanitized and never send stored secrets back to the browser.

**Tech Stack:** Go, Windows SCM and named pipes, Task Scheduler, React/TypeScript, existing SQLite-backed Admin API, MaxMind geoipupdate.

**Spec:** `docs/superpowers/specs/2026-11-24-windows-geolite-admin-design.md`

## Global Constraints

- Admin Settings is the only routine configuration interface; no GeoLite setup CLI is required. A missing database continues to fail closed.
- Keep the updater pipe limited to bounded, fixed GeoLite operations; never accept arbitrary commands or paths.
- Store MaxMind credentials outside the application database and full-system backups, readable only by Administrators and SYSTEM.
- Download only `GeoLite2-Country`; validate before replacement and preserve the active database on every failure.
- Restart BookmarksSync only after a valid changed database is installed.
- Preserve country policy and default `IN,SA`; make no firewall changes; Docker scheduling remains out of scope.
- Use author `atikansari <atikansari@ansariz.biz>` for commits, with no co-author trailers.

## Review Focus

- Malformed, duplicate, oversized, or unknown pipe operations must be rejected before any host mutation.
- Secrets must not appear in API responses, pipe responses, logs, task arguments, error messages, or snapshots.
- Invalid credentials, denied task registration, updater failure, invalid MMDB, replacement failure, and restart failure must not corrupt or remove the last valid database.
- Concurrent manual and scheduled runs must serialize; a second request must report busy without changing status or files.
- A failed UI/API response must not falsely show a saved schedule or a completed update.

## File Map

- `internal/updateipc/protocol.go`, `protocol_test.go`, `pipe_windows.go`: bounded command union and named-pipe transport.
- `internal/geolite/model.go`, `validator.go`: platform-neutral GeoLite config/status and strict MMDB validation shared by the pipe, server, and updater.
- `internal/windowsupdate/geolite*.go`, Windows tests, and `cmd/bookmarksync-updater/`: updater-owned state, task, credential, and execution behavior.
- `internal/api/api.go`, new `internal/api/admin_geolite.go`, API tests, `api/openapi.yaml`: admin routes, auth gates, DTOs, and API contract.
- `web/src/pages/AdminPage.tsx`, `web/src/test/admin.test.tsx`, and shared API types: Admin Settings interface and tests.
- `internal/updates/catalog.go`, `.github/workflows/ci.yml`, and `scripts/windows`: require and publish the pinned updater binary with the server release, and install it safely.
- `docs/distribution.md`, `docs/network-access.md`, and `THIRD_PARTY_NOTICES.md`: Windows operations, recovery, limitations, and upstream license notice.

## Interfaces

Use these shared contracts across tasks:

- Shared model: `geolite.Config{AccountID string, LicenseKey string, Enabled bool, Days []string, StartTime string}` and `geolite.Status{Configured bool, Enabled bool, Days []string, StartTime string, DatabaseAvailable bool, DatabaseBuildTime *time.Time, LastAttempt *time.Time, LastResult string, LastErrorCode string, Running bool}`. Status has no credential fields.
- Enabled schedules require two to seven unique weekdays and valid local `HH:MM` time; defaults are Monday/Thursday at 04:00 local, matching Debian. Both credential fields blank preserve both current values; one supplied credential is invalid; first configuration requires both.
- Keep the existing release pipe types unchanged: `Request{Tag string}` and `Response{Accepted bool, Job string}`. Add a separate `GeoLiteRequest{Operation string, Config *geolite.Config}` and `GeoLiteResponse{OK bool, Job string, ErrorCode string, Status *geolite.Status}`. `Job` is present only for an accepted asynchronous run; `ErrorCode` comes from a fixed allowlist.
- Admin API: `GET /api/v1/admin/geolite` returns `geolite.Status`; `PUT /api/v1/admin/geolite` accepts `accountId`, `licenseKey`, `enabled`, `days`, `startTime`; `DELETE /api/v1/admin/geolite` clears the secret and managed task; `POST /api/v1/admin/geolite/run` requests a manual run and returns an accepted job/status. GET never returns either credential.
- The updater resolves all paths from `ProgramFiles` and `ProgramData` defaults and the installed service layout; callers cannot override them.

---

### Task 1: Lock down the GeoLite pipe protocol and authorization boundary

**Files:** Create `internal/geolite/model.go` and tests; modify `internal/updateipc/protocol.go`, `protocol_test.go`, `pipe_windows.go`, `pipe_windows_test.go`, `internal/windowsupdate/pipe.go`, and its tests.

**Interfaces:** Define `geolite.Config`, `geolite.Status`, `ValidateConfig(Config) error`, and `DefaultConfig() Config`. Add the closed operation request/response types above using `EncodeGeoLiteRequest`, `DecodeGeoLiteRequest`, `EncodeGeoLiteResponse`, and `DecodeGeoLiteResponse`; leave the existing release-tag request/response unchanged.

- [ ] Add tests for each allowed operation, unexpected fields, duplicate keys, oversize frames, missing/extra fields, invalid schedule shapes, and legacy update compatibility.
- [ ] Run `go test ./internal/updateipc ./internal/windowsupdate` and confirm the new rejection tests fail against the current protocol.
- [ ] Implement distinct bounded GeoLite request/response envelopes while keeping release-tag messages byte-compatible; do not include executable paths, task names, or arbitrary commands.
- [ ] Run the focused tests and `go test ./internal/updateipc ./internal/windowsupdate`.
- [ ] Cross-build the Windows updater and server with `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/bookmarksync ./cmd/bookmarksync-updater`.
- [ ] Commit as `Add bounded Windows GeoLite pipe protocol`.

### Task 2: Implement the Windows GeoLite task manager and safe updater worker

**Files:** Create `internal/geolite/validator.go` and tests; focused `internal/windowsupdate/geolite.go`, platform-specific task/ACL/atomic-replace files and tests; extend `cmd/bookmarksync-updater` dispatch; pin geoipupdate v8.0.0 and its digest.

**Interfaces:** `GeoLiteManager.Status(context.Context) (geolite.Status, error)`, `Configure(context.Context, geolite.Config) error`, `Clear(context.Context) error`, and `Run(context.Context) error`; inject filesystem, Task Scheduler XML command runner, process runner, service control, and atomic replace operations for tests. `Run` is called only from the updater service's serialized job manager. The scheduled task invokes fixed `geolite-run` mode, which requires LocalSystem and sends a `run` request to that service through the pipe.

- [ ] Add Windows tests proving fixed managed paths, no reparse traversal, protected secret/state/stage/backup ACLs before file writes, app SID read-only access to the active MMDB, and task XML for SYSTEM/highest privileges/StartWhenAvailable/IgnoreNew/PT1H/no battery restrictions/local-time boundaries.
- [ ] Add worker tests for changed/unchanged DB, failed download, invalid/wrong-edition/oversize/downgrade MMDB, replacement error, restart ordering, rollback/recovery, stale stage cleanup under serialization, and secret-safe process output. Use existing MMDB fixtures and stub executables.
- [ ] Add `network.OpenGeoLiteCountryDatabase(path)` and tests: accept exactly `GeoLite2-Country`, cap at 64 MiB, reject invalid/future build dates, and expose its edition/build time. Retain the existing general validator behavior for manually supplied GeoIP2 Country files; the update manager compares candidate time against the current GeoLite build and rejects downgrades.
- [ ] Run focused tests and capture expected failures before implementation.
- [ ] Create the credential/state directory with protected SYSTEM/Administrators ACL before creating secret files; atomically replace credentials only after validating the paired account/key and schedule. Do not make a network request during Configure. Delete the managed task before clearing credentials; retain secrets if task deletion fails, and always retain the last valid MMDB.
- [ ] Package the official `geoipupdate_8.0.0_windows_amd64.zip` with SHA-256 `1aadec5283389670729b1332ca938e3cf12a1413853c30a2193cfe9d3fc8bbea`. Before each run verify the installed executable against the BookmarksSync manifest; invoke it only with `-f <protected-config> -d <fixed-stage>`, discard stdout/stderr, enforce a one-hour limit, and return fixed error categories.
- [ ] Never truncate a mapped MMDB. Preserve the old file in a protected backup, atomically replace on the same volume, restart BookmarksSync through SCM, and wait for `/readyz`; clear recovery state/backup only after readiness. On activation failure restore the prior DB and readiness; preserve a recovery marker and backup if rollback cannot finish, and resume recovery before any later download.
- [ ] Run focused updater tests, Windows package tests, `go vet ./...`, `go test ./... -count=1`, and both Windows/Linux CGO-free builds.
- [ ] Commit as `Add protected Windows GeoLite updater`.

### Task 3: Connect the restricted updater service to GeoLite operations

**Files:** Modify `internal/windowsupdate/jobs.go`, `pipe.go`, host/service wiring, `internal/updates/runner_windows.go`, and related tests.

**Interfaces:** Extend the existing shared updater job manager so one active job may be either an approved release apply or GeoLite update. Add `SubmitGeoLite(operation string, config *geolite.Config) (jobID string, err error)`, `Status(context.Context) (geolite.Status, error)`, and `Wait(context.Context, jobID string) error`. Configure/clear/status are bounded host operations serialized against release/update jobs; configure/clear return busy while another job runs. Manual and scheduled runs return a tracked job ID.

- [ ] Test unauthorized caller denial, authorized requests, unknown operation rejection, release-versus-manual-versus-scheduled job serialization, configure/clear busy behavior, status retrieval, LocalSystem-only `geolite-run`, and updater service shutdown during a job.
- [ ] Run focused Windows updater and update IPC tests to see the failing assertions first.
- [ ] Implement service dispatch so the pipe routes only the explicit GeoLite operations and keeps existing approved-release update behavior intact.
- [ ] Add bounded client methods in `internal/updates/runner_windows.go`; map unavailability and busy state to safe domain errors.
- [ ] Run focused tests, full Go tests, vet, and Windows amd64 build.
- [ ] Commit as `Connect GeoLite operations to Windows updater service`.

### Task 4: Add admin-only GeoLite API routes and OpenAPI contract

**Files:** Modify `internal/api/api.go`, create `internal/api/admin_geolite.go`, extend `internal/api` tests and `api/openapi.yaml` plus its contract tests.

**Interfaces:** Add optional `GeoLiteRunner` dependency with `Status(context.Context) (geolite.Status, error)`, `Configure(context.Context, geolite.Config) error`, `Clear(context.Context) error`, and `Run(context.Context) (string, error)`. Register GET, PUT, DELETE, and POST routes with Admin authorization; require recent auth on mutations and manual run. Validate Account ID, license-key pattern `^[A-Za-z0-9_]{1,64}$`, paired credentials, two to seven unique weekdays, and 24-hour time before sending to host. Preserve both credentials when both fields are blank; clear only through DELETE.

- [ ] Add HTTP tests: regular user forbidden; admin without recent auth denied for writes; valid config accepted; invalid days/time/credential format/pair rejected; both blank preserves existing credentials; one blank rejects; DELETE clears; GET and all errors never contain submitted credentials; run returns accepted/busy safely; non-Windows host responds with a clear unavailable status.
- [ ] Run those tests and confirm expected failures on the current server.
- [ ] Implement API routes and sanitized response mapping without persisting the MaxMind key in system settings or activity logs.
- [ ] Add exact request/response schemas and route security to OpenAPI; assert no secret property exists in response schemas.
- [ ] Run `go test ./internal/api ./internal/updates ./internal/windowsupdate -count=1`, `go vet ./...`, and `go test ./... -count=1`.
- [ ] Commit as `Add Admin API for Windows GeoLite settings`.

### Task 5: Add GeoLite controls to Admin Settings

**Files:** Modify `web/src/pages/AdminPage.tsx`, its API types if shared, `web/src/test/admin.test.tsx`, and help text/styles only if needed.

- [ ] Add UI tests for write-only paired credentials, configured indicator, blank-pair preserve, explicit remove, enabled state, selected weekdays/time (default Monday/Thursday 04:00 local), database build and last-run status, run-now busy/result, validation errors, inaccessible/error status, and no secret reflected from the API.
- [ ] Run focused frontend tests and verify they fail before UI implementation.
- [ ] Add a GeoLite management card next to Country access with clear tooltips, MaxMind account/license note, twice-weekly defaults, save/clear controls, manual run, sanitized status/error text, and accessible loading/disabled feedback.
- [ ] From `web/`, run `npm test -- src/test/admin.test.tsx`, then `npm test`, `npm run typecheck`, and `npm run build`.
- [ ] Commit as `Add GeoLite controls to Admin Settings`.

### Task 6: Package, install, and document the Windows workflow

**Files:** Modify `internal/updates/catalog.go` and tests, `.github/workflows/ci.yml`, `scripts/windows/BookmarksSync.Install.psm1`, `scripts/windows/tests/run.ps1`, `THIRD_PARTY_NOTICES.md`, `docs/distribution.md`, `docs/network-access.md`, and `README.md` where appropriate.

- [ ] Test that the Windows release catalog requires `geoipupdate-windows-amd64.exe` and tests reject a release missing that asset.
- [ ] Update CI packaging to download the official v8.0.0 Windows archive, verify archive SHA-256 `1aadec5283389670729b1332ca938e3cf12a1413853c30a2193cfe9d3fc8bbea`, extract only `geoipupdate.exe` as `geoipupdate-windows-amd64.exe`, preserve its applicable license notice, include the executable in release SHA256SUMS, and attest/publish it with the Windows build.
- [ ] Test install, upgrade, and repair stage and verify `geoipupdate-windows-amd64.exe`, preserve its license, set `BOOKMARKSYNC_GEOIP_DB` to `%ProgramData%\BookmarksSync\geolite\database\GeoLite2-Country.mmdb`, grant the app service read-only database access, and do not create or enable a scheduled task before GUI configuration.
- [ ] Implement install/upgrade/repair handling so existing GeoLite credentials/task state survive application upgrades, pinned updater assets are validated, and task repair does not drop the last valid database.
- [ ] Document GUI steps, key handling, default schedule, task/status behavior, update-now feedback, recovery when no valid database exists, MaxMind licensing/account requirements, proxy limitations, and Windows versus Docker scope.
- [ ] Run PowerShell Pester tests, actionlint/static packaging checks, documentation link checks used by the repo, frontend suite, full Go suite/vet, and clean diff check.
- [ ] Commit as `Package and document Windows GeoLite updates`.

## End-to-end verification

- [ ] Test the flow locally with a fake MaxMind endpoint and fixture MMDB: configure through Admin Settings, verify protected credential and task state, run once, validate database, verify status/build time, confirm app restart follows a changed database, and confirm unchanged or invalid updates do not restart or alter the active file.
- [ ] Verify non-elevated test runs do not create real Windows services/tasks or touch ProgramData; use temporary roots and injected scheduler/process operations.
- [ ] Keep the user's separately deferred elevated live Windows service test pending; do not perform it as part of this plan.
- [ ] Review the final diff and commit focused changes using the configured Atik Ansari identity; do not push or publish without a separate explicit request.
