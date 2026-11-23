# Windows Service and Approved Updates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Install BookmarksSync as a protected Windows service and allow recent-authenticated admins to approve verified Windows releases from the Admin page.

**Architecture:** The Go application runs as a dedicated Windows virtual service account. A separate LocalSystem updater receives a bounded request over a local, DACL-protected named pipe, independently verifies the selected release, and uses a temporary worker copy to replace the app and updater safely with backup and rollback.

**Tech Stack:** Go 1.27.4, `golang.org/x/sys/windows/svc`, Windows SCM and named pipes, PowerShell, GitHub CLI, rclone 1.76.0.

**Spec:** `docs/superpowers/specs/2026-11-23-windows-service-update-design.md`.

## Global Constraints

- Support only Windows amd64 in this slice; keep console mode working and preserve Linux builds.
- Service names are `BookmarksSync` and `BookmarksSyncUpdater`; the app identity is `NT SERVICE\BookmarksSync`, and the updater runs as LocalSystem.
- The updater accepts only a full `build-<40 lowercase hex>` tag; it never accepts commands, paths, or arbitrary process arguments from the web service.
- Require published-release membership, SHA256SUMS, matching BUILD_INFO, and GitHub attestation for both Windows executables before replacement.
- Stop the app before backup/replacement; retain three verified snapshots and recover the old binary, helper, configuration, rclone config, and application data on failed readiness.
- Store secrets under ProgramData with explicit administrator/SYSTEM/service-SID ACLs; do not put passwords in arguments or logs.
- Do not add firewall rules, run the app as LocalSystem, or update GeoLite automatically in this slice.

## Review Focus

1. A remote or unauthorized local process must not reach the privileged updater pipe; test its DACL and remote-client rejection.
2. Malformed, stale, or unpublished tags must be rejected before any service stops or files change.
3. Backup, replacement, restart, and rollback failures must retain recoverable data and report the retained path without secrets.
4. Environment-file parsing and ACL failures must stop installation without leaking OIDC secrets.
5. Junctions, symlinks, and unmanaged pre-existing Windows services/files must fail closed before mutation.

## Tasks

### Task 1: Add protected service configuration and SCM mode

**Files:** `internal/config/envfile.go`, `internal/config/envfile_test.go`, `internal/config/config.go`, `cmd/bookmarksync/main.go`, Windows/non-Windows service entry files under `cmd/bookmarksync/`.

**Interfaces:** Add `config.ReadEnvironmentFile(path string, inherited func(string) string) (func(string) string, error)`. The file accepts unique `BOOKMARKSYNC_* = value` entries only; the process environment overrides file values. Add `serve --env-file <path>` and a Windows `service run --env-file <path>` entry that dispatches `serve` through `svc.Run`; keep ordinary console invocation unchanged.

- [x] Write tests for file precedence, valid OIDC secrets, malformed/duplicate/unknown keys, oversized input, and secret-free errors; also cover env-file flag parsing and service cancellation/state transitions.
- [x] Run focused tests first; RED confirmed missing loader/service functions, then SCM state regression failed until `StartPending` was reported before `Running`.
- [x] Implement the strict environment-file loader and the Windows SCM handler with stop/shutdown context propagation.
- [x] Verify focused tests, full Go tests, vet, and Windows/Linux amd64 builds.
- [x] Commit as `Add native Windows service mode`.

### Task 2: Implement the restricted update-pipe protocol and app runner

**Files:** `internal/updateipc/protocol.go`, `protocol_test.go`, `pipe_windows.go`, `pipe_other.go`, `internal/updates/runner.go`, platform runner files and tests.

**Interfaces:** `updateipc.DecodeRequest(io.Reader) (Request, error)` and `EncodeRequest(io.Writer, string) error` use a 2-byte length prefix and a JSON body capped at 256 bytes. `EncodeResponse`/`DecodeResponse` carry an accepted flag and job id; one request and one response share a connection. `updates.InstalledHostRunner(...)` returns an `UpdateRunner` interface with `Start(context.Context, string) (string, error)`; Windows sends only the validated tag to `\\.\pipe\BookmarksSyncUpdater`, waits no longer than 10 seconds for acknowledgement, and returns the existing job identifier format.

- [x] Test invalid tags, unknown fields, trailing data, empty and oversized messages, and bounded reads.
- [x] Run focused protocol tests and record the expected RED before adding protocol code (initial compile failed on the missing protocol API).
- [x] Implement Windows pipe creation with an explicit DACL for SYSTEM, Administrators, and the app service SID, plus `PIPE_REJECT_REMOTE_CLIENTS`; add the Windows client with deadlines.
- [x] Test local request/reply, unauthorized identity denial, remote-client rejection, and missing updater behavior on Windows; keep non-Windows tests green.
- [x] Commit as `Add restricted Windows update pipe`.

### Task 3: Build the LocalSystem updater and transactional worker

**Files:** `cmd/bookmarksync-updater/`, `internal/windowsupdate/` service, release verification, worker, and tests; Windows updater-runner wiring.

**Interfaces:** The updater service accepts one `Request{Tag string}`, serializes jobs, validates the catalog and release before launching the downloaded updater executable as a temporary worker. `HostOperations` exposes `StopService(ctx, name) error`, `StartService(ctx, name) error`, `CreateVerifiedSnapshot(ctx, tag) (Snapshot, error)`, `ReplaceInstalledFiles(ctx, release VerifiedRelease) error`, `WaitReady(ctx, url) error`, and `RestoreSnapshot(ctx, Snapshot) error`; `ApplyUpdate(ctx, tag, ops HostOperations) error` owns ordering and rollback.

- [x] Write state-machine tests proving invalid/unavailable releases do not stop services, a verified snapshot exists before replacement, readiness failure restores prior files/data/config, and rollback failure retains the failed tree and snapshot.
- [x] Run focused tests to capture RED for each transition and error path.
- [x] Implement service stop/start handling, checksum/build metadata/attestation verification, compressed snapshot manifest, atomic executable replacement, local `/readyz` check, retention of three snapshots, and restart/rollback reporting.
- [x] Test serialization, duplicate requests, service stop during job, network/verification failures, low disk space, failed restore, and absence of secrets in logs.
- [x] Commit as `Add verified Windows update worker`.

### Task 4: Extend release assets and platform catalog

**Files:** `internal/updates/catalog.go`, `catalog_test.go`, `cmd/bookmarksync/main.go`, `cmd/bookmarksync-updater/`, `.github/workflows/ci.yml`.

**Interfaces:** Add `updates.RequiredReleaseAssets(goos, goarch string) []string`. Windows amd64 requires `bookmarksync-windows-amd64.exe`, `bookmarksync-updater-windows-amd64.exe`, `BUILD_INFO.txt`, and `SHA256SUMS`; Linux amd64 keeps its existing asset set.

- [ ] Test that each platform lists only releases containing all of its required assets and unsupported platforms expose no apply action.
- [ ] Run catalog tests and Windows/Linux command builds to confirm the new updater entry point is absent or safely stubbed on other operating systems.
- [ ] Extend the successful-main workflow to build and attest the updater executable, include it in SHA256SUMS, and publish it with the server binaries and metadata.
- [ ] Validate workflow syntax/asset names and `go test ./internal/updates ./cmd/bookmarksync/... ./cmd/bookmarksync-updater/...`.
- [ ] Commit as `Publish Windows updater asset`.

### Task 5: Add Windows install, upgrade, and repair commands

**Files:** `scripts/windows/bookmarksync.ps1`, `scripts/windows/BookmarksSync.Install.psm1`, `scripts/windows/tests/run.ps1`, `docs/distribution.md`, `THIRD_PARTY_NOTICES.md`, `third_party/rclone-COPYING`.

- [ ] Add isolated tests first for fresh install, explicit-tag upgrade, repair, catalog/checksum/attestation failures, existing unmanaged services, and path junction rejection. Tests use a temporary root and fake external commands; they must not touch host services or ProgramData.
- [ ] Run `powershell -NoProfile -File scripts/windows/tests/run.ps1` and record expected RED results.
- [ ] Implement `install`, `upgrade --tag`, and `repair --tag`; configure both services, virtual service SID, startup recovery, protected ProgramData paths and environment file, require or install GitHub CLI before host mutation, verify the pinned official Windows rclone archive, add loopback management CIDRs, and pipe first-admin input through stdin.
- [ ] Test verified download before mutation, data/config preservation, service and ACL state, forced readiness rollback, repeat repair, rclone license installation, and exact-path cleanup on Windows.
- [ ] Commit as `Add Windows service installer`.

### Task 6: Document and verify the Windows deployment

**Files:** `docs/distribution.md`, `docs/implementation-plan.md`, this plan, and installer help text.

- [ ] Document prerequisites, install/upgrade/repair commands, identities and paths, Admin-page approvals, firewall/reverse proxy, backup/restore, and GeoLite limitations.
- [ ] Run `gofmt -l cmd internal`, `go test ./... -count=1`, `go vet ./...`, Windows amd64 tests/build, Linux amd64 build, `powershell -NoProfile -File scripts/windows/tests/run.ps1`, workflow validation, and `git diff --check`.
- [ ] Run an elevated disposable Windows service lifecycle smoke: install, start, submit an approved update, force a readiness failure, verify rollback, and remove only test-owned services/files.
- [ ] Mark actual SCM lifecycle acceptance pending if an elevated Windows test host is unavailable; do not substitute the Linux LXC or claim service installation was tested.
- [ ] Commit as `Document Windows deployment lifecycle`.

## Completion Gate

Do not mark Windows distribution complete until the release contains both Windows executables with checksums and attestations, the platform catalog enables updates only when those assets exist, failure-path tests pass, and an elevated Windows SCM lifecycle passes. If the current non-admin workstation cannot run the final smoke, report that gate as pending and request an elevated Windows test session.

