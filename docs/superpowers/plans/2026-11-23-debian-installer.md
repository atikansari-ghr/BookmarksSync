# Debian Installer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Install, upgrade, and repair BookmarksSync on Debian/LXC using provenance-verified builds and rollback snapshots.

**Architecture:** A root-only Debian control script installs the official GitHub CLI, resolves a published SHA release, downloads its assets, verifies checksum and attestation, and manages a hardened systemd service. Upgrade stops writes, saves a full backup and old binary, applies the selected release, and rolls back both binary and data if readiness fails.

**Tech Stack:** Bash, systemd, GitHub CLI, curl, Python 3, BookmarksSync CLI.

**Spec:** `docs/design.md`, Operations and Architecture and delivery; `docs/superpowers/specs/2026-11-23-debian-installation-design.md`.

## Global Constraints
- Support Debian amd64 only for this task; refuse other operating systems and architectures before mutation.
- Require release tags matching `build-[0-9a-f]{40}`; upgrades and repairs never choose a floating latest tag.
- Verify checksum and repository/workflow-scoped GitHub attestation before replacing any installed binary.
- Stop systemd service before standalone full backups; preserve at least three verified rollback snapshots.
- Keep runtime data on local disk at `/var/lib/bookmarksync`; default to preserving it.
- Do not push or publish from this branch.

## Review Focus
1. Invalid or missing release assets must fail before system changes.
2. Wrong checksum, repository, workflow, or subject must block replacement.
3. Interrupted/failed upgrade must retain old binary and recoverable snapshot.
4. Failed readiness after migration must restore old binary and pre-upgrade data.
5. Symlinks, writable managed paths, unsupported architecture, and missing systemd must fail closed.

## Files
- Create `scripts/debian/bookmarksync` for install/upgrade/repair commands and managed service configuration.
- Create `scripts/debian/test-bookmarksync.sh` for isolated fake-release/systemd integration tests.
- Update `docs/distribution.md`, `docs/network-access.md`, and `docs/implementation-plan.md` with supported commands and limits.

## Verification
- Run the installer shell suite with fake `curl`, `gh`, and `systemctl` tools.
- Run `bash -n`, ShellCheck when available, and the mocked lifecycle suite on the prepared Debian LXC.
- Independently confirm no release/push is attempted from this branch; live release download remains untestable until CI publishes the first `main` release.

## Completion record
- Debian 13 amd64 LXC mock lifecycle passed, including install, upgrade, readiness rollback, failed restore retention, protected environment permissions, and refusal to replace unmanaged service units.
- ShellCheck was unavailable on the host. A real release install remains pending the first published `main` artifact.
