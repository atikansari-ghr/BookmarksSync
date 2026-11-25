# GeoLite Provisioning Implementation Plan

> Historical record: native Windows server scope and Windows acceptance gates are superseded by the [2026-11-24 Linux-only decision](../specs/2026-11-24-linux-only-design.md). Historical implementation/test statements below are preserved; they do not create current Windows work.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provision and safely update the GeoLite2 Country MMDB on Debian/LXC.

**Architecture:** A pinned official `geoipupdate` executable runs from a root-only systemd service/timer. Its helper stages updates, validates them with the BookmarksSync CLI, atomically replaces the live database only when content changes, and restarts the service after a valid replacement. Setup defers that restart until systemd has loaded the new database path, then enables the timer.

**Tech Stack:** Go CLI, Bash, systemd, official MaxMind geoipupdate.

**Spec:** `docs/superpowers/specs/2026-11-23-geolite-provisioning.md`

## Global Constraints

- Preserve fail-closed country enforcement and the existing default `IN,SA` policy.
- Never bundle GeoLite data or publish MaxMind credentials.
- Store updater credentials in a root-only host config, not application runtime data.
- Verify the pinned upstream updater checksum before installing it.
- Do not update the production service; only the authorized test LXC may be changed.

## Review Focus

- Updater failure: preserve the known-good MMDB and do not restart the server.
- Invalid or wrong-edition MMDB: reject it before replacing the live file.
- Unchanged database: avoid needless server restart.
- Credential handling: no key in process arguments, output, or logs; config mode is `0600`.
- Path custody: the MMDB directory and config directory must not be replaceable by the unprivileged app account.
- Atomic replacement: never expose a partial live MMDB during update.

## File Map

- Modify `cmd/bookmarksync/main.go` and `admin_test.go` for a database-independent CLI validation subcommand; place its implementation in `admin.go`.
- Create `scripts/debian/bookmarksync-update-geoip` for staging, validation, atomic install, permissions, and conditional service restart.
- Create `scripts/debian/bookmarksync-geoip-setup` to install the verified updater and configure the protected credential file.
- Create `scripts/debian/bookmarksync-geoip-update.service` and `.timer` for root execution twice weekly.
- Create `scripts/debian/test-update-geoip.sh` with stub updater and fixture MMDB cases.
- Update `docs/network-access.md`, README, and `docs/implementation-plan.md` with setup, operation, and limitations.

## Task 1: MMDB validation CLI

- [x] Add CLI tests for a valid GeoLite/GeoIP Country database, a missing file, a corrupt file, and a City database; assert valid build time and nonzero exit on invalid input, without creating the data directory.
- [x] Run those tests and confirm the new command is absent or fails to dispatch before implementation (RED: unknown command).
- [x] Add `bookmarksync network validate-geo --file PATH`; dispatch it before the DB-backed part of `admin`, call `network.OpenCountryDatabase`, report only the UTC build time, close the reader, and reject extra arguments.
- [x] Run focused CLI and network tests, `gofmt`, and vet.

## Task 2: Host updater and systemd provisioning

- [x] Add shell integration cases using a fake updater and fixture MMDB: changed-valid installs/restarts; unchanged-valid avoids restart; deferred setup restart and failed-restart retry work; updater failure and invalid staging data preserve old bytes; insecure config is rejected.
- [x] Implement the helper with root-owned paths, same-filesystem staging, validation before install, mode `0644` for the MMDB, ownership matching the service user, atomic rename, and conditional restart.
- [x] Add setup script for Debian amd64/arm64 with pinned v8.0.0 release URLs and SHA-256 digests. Prompt for AccountID and license key without echo; write `/etc/bookmarksync/GeoIP.conf` mode `0600`, only `GeoLite2-Country`, and the configured staging directory. Refuse unsafe config and preserve valid credentials while narrowing the edition to Country.
- [x] Add a root oneshot service and twice-weekly persistent timer. The helper uses strict shell settings and avoids displaying credentials.
- [x] Run shell tests and `bash -n`; `shellcheck` was unavailable. Validate units with `systemd-analyze verify` on Debian/LXC.

## Task 3: Documentation and acceptance

- [x] Document MaxMind account prerequisites, credential handling, update schedule, failure behavior, proxy/firewall HTTPS needs, and manual validation/recovery commands.
- [x] Run Go tests/vet, Linux and Windows CGO-free builds, shell tests, unit verification, and `git diff --check`.
- [x] Preserve the LXC binary/data before deploying the validation CLI; validate fixtures, run the stub updater integration suite, stage the pinned upstream installer, check the interactive credential prompt with dummy values, verify systemd units, and confirm `/readyz`.
- [x] Update the Operations milestone to describe this Debian/LXC slice; leave real MaxMind credentials, live country enforcement, Docker/Windows scheduling, and external proxy country acceptance pending.
- [ ] Commit the focused change as Atik Ansari only; do not push.

## Validation record

- Go: `go test ./... -count=1`, `go vet ./...`, `gofmt`, and CGO-free Linux/amd64 + Windows/amd64 builds passed.
- Debian LXC: saved `/root/bookmarksync-geolite-rollback-20261123/` containing the previous binary and data archive; deployed binary hash matched the local build. `/readyz` remained ready. The Country fixture validated with build time `2026-10-22T18:52:57Z`; the City fixture was rejected.
- Shell integration: changed/unchanged fixture, deferred restart, restart retry, update failure, invalid MMDB, mode enforcement, and secret-redaction checks passed. Setup downloaded official geoipupdate 8.0.0, verified its pinned amd64 digest, staged a root-only config with dummy test values, and made no host service changes. Temporary `systemd-analyze verify` passed; no updater timer was enabled.
- Limits: no MaxMind account/license was configured, so no real database was downloaded and no live public visitor-country path was tested. ShellCheck is not installed on the LXC.
