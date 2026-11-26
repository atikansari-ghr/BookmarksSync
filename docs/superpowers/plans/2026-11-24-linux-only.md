# Linux-only completion plan

Spec: `docs/superpowers/specs/2026-11-24-linux-only-design.md`.
Execution: parallel scoped implementation, integration and independent review. The existing user-selected `build/bookmarksync` branch is the work area. Changes remain local for review; no push, publication or production deployment is part of this plan.

## Task 1: Remove native Windows backend and UI
- [x] Remove `cmd/bookmarksync-updater`, `internal/windowsupdate`, `internal/updateipc`, native Windows service wiring and Windows-only GeoLite API/UI/model. Keep portable low-level filesystem adapters for local tests.
- [x] Restrict release catalog eligibility to Linux amd64, with a regression test proving Windows unsupported.
- [x] Preserve migrations, existing databases, browser client labels, Linux updater behavior, account and bookmark APIs.
- [x] Run focused Go tests, frontend typecheck/tests/build and Go build. Expected: pass; report environmental failures precisely.

## Task 2: Linux CI, release automation and documentation
- [x] Remove Windows CI/test/build/release artifacts; preserve Linux validation, provenance and SHA-addressed prereleases.
- [x] Prepare Linux amd64 Docker image publication on successful main CI, retaining least privilege and provenance. No live publication in this task.
- [x] Update current README/design/implementation/distribution/network/backup docs and AGENTS commands; mark historical Windows plans superseded.
- [x] Validate workflow syntax and packaging tests. Expected: no Windows artifacts; Linux binary and Docker flow intact.

## Task 3: Docker operations
- [x] Implement opt-in protected GeoLite scheduling for Docker without a Docker socket in the app. Use validated staged atomic publication and refresh the reader when the database changes.
- [x] Add Docker lifecycle helpers for safe maintenance; retain independent backups, operator-approved upgrades and rollback paths.
- [x] Test updater success, invalid/missing data, unchanged data and retry; test lifecycle argument handling with disposable/fake operations.
- [x] Document concrete configuration and runtime checks; do not claim live credential checks.
- [x] Add optional browser-approved Docker updates through a fixed host inbox and root-owned systemd worker; test tag validation, symlink rejection, bounded queue behavior and unavailable-host fallback. The app never controls Docker directly.

## Task 4: Debian maintenance and remaining application work
- [x] Add diagnostics and safe uninstall (retain application data, snapshots and backups), and command wrappers for backup/restore where appropriate. Add regression coverage for target validation and data preservation.
- [x] Implement and test Login Flow v2 against the stock Floccus contract if it can be safely completed; otherwise record a concrete blocker without speculative success claims.
- [x] Check public/operator acceptance dependencies and record exact pending items.

## Task 5: Integration and review
- [x] Independently review each task and resolve important findings, test first.
- [x] Run full Go/web/harness suites, vet/format, Linux cross-build, workflow/static checks and available isolated Linux runtime checks.
- [x] Reconcile the main project checklist with actual evidence and provide a concise report of delivered work and external acceptance gates.

## Verification record
All implementation and local/isolated acceptance tasks above are complete. Exact evidence and outstanding external/browser acceptance limits are recorded in [verification-2026-11-24.md](../../verification-2026-11-24.md). Operations and Distribution remain open in the main milestone plan until those external gates pass.
