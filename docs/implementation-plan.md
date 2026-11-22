# BookmarksSync Implementation Plan

**Goal:** Implement docs/design.md through scoped tasks and independently verified milestones.
**Architecture:** Portable Go executable, embedded React/TypeScript UI, local SQLite, restricted optional maintenance helper.
**Spec:** docs/design.md is binding. Use mocscreenshots/ as visual references.

## Global constraints
- User identity/attribution and MIT per spec. Never commit social drafts.
- Work on `build/bookmarksync` in the user-selected workspace; do not force-push, mutate global settings, or expose secrets. The user authorized isolated testing on LXC `192.168.10.226`; never deploy to production without separate authorization.
- Do not represent unfinished features as working. No hardcoded demo authentication or fake successful operations.
- Existing floccus compatibility is a release gate. No substantial UI until compatibility decision.
- Tests cover behavior and failure paths; use failing regression tests before behavior fixes.

## Milestones and acceptance
- [x] 1 Foundation: connect remote preserving files; portable Go tooling; Go module, health/readiness/version endpoints, SQLite migrations/store, CLI serve, test/build CI, MIT, README. Verify persistence/reopen, graceful shutdown, JSON errors, Windows and Linux builds. Accepted at 9b90baa after independent review and checks; Ubuntu/Windows CI tests passed.
- [x] 2 Compatibility: Nextcloud floccus capabilities/bookmark/folder/order/hash/lock APIs, per-connection credentials, roots, permissions and protocol tests. Negative wire folder IDs and the Floccus DELETE-only same-folder duplicate policy are implemented; two real-server browser matrices passed 72/72 each in Chrome, Edge, Brave, and Firefox. Completed in `8e884eb` through `b0eab92`; see `docs/floccus-compatibility.md`.
- [x] 3 Identity/API: admin bootstrap, password/passkeys/TOTP/recovery, OIDC linking, private ownership/sharing, revocable sessions/tokens, /api/v1 + OpenAPI. Implemented and committed through `fa78de8`; see `docs/design/identity-api.md`, `docs/superpowers/plans/2026-11-17-identity-api.md`, and `docs/authentication.md`.
  - Verified with Go tests/vet, Linux/Windows builds, real Chromium sign-in/security/admin smoke, and stock-Floccus browser sync. Real Google/Authentik credentials and public reverse-proxy OIDC/passkey checks depend on operator configuration; GitHub-hosted CI remains pending until an authorized push.
- [x] 4 Workspace/UI: organizer, dashboards, CRUD/multi-select/drag/drop/search/tags/favorites/import/export, duplicates, SSRF-safe link checks, Trash, persisted widgets/themes/branding. Implemented and committed through `a14bb00`; see `docs/superpowers/plans/2026-11-17-workspace-ui.md`, `docs/superpowers/plans/2026-11-18-bookmark-favorites.md`, and `docs/workspace-guide.md`.
  - Go/web tests, vet, typecheck/build, real UI smoke, and the current stock-Floccus Chrome/Edge/Brave/Firefox sync matrix passed. Live checks against operator-configured exclusions and public reverse-proxy workflows remain operator validation.
- [ ] 5 Operations: backup notifications, GeoLite provisioning and final country/proxy acceptance remain.
  - **Delivered:** manual compressed full-system archive, verification and clean-directory restore; additive user-only recovery of private bookmark roots, tags, favorites and user setting overrides; scheduled weekly/monthly runs, host-managed local/NFS/rclone destinations, admin controls, retention, and optional per-destination AES-256-GCM encryption with separately exported recovery keys. User recovery leaves identities, sessions, shares, system settings and other accounts intact. Details: `docs/backup-restore.md`, `docs/superpowers/plans/2026-11-22-scheduled-backups.md`, `docs/superpowers/plans/2026-11-22-user-recovery.md`, and `docs/superpowers/plans/2026-11-22-backup-encryption.md`.
  - **In progress:** request-wide country enforcement with trusted-proxy resolution, Admin policy controls, and local geo reset are implemented; Linux/Windows builds pass and an isolated LXC smoke passed. Public-country validation through the LXC's external web route remains unverified. Setup guide: `docs/network-access.md`.
  - **Verified:** full Go and web suites, vet, typecheck/build, Linux/Windows cross-builds, plus isolated LXC health/API/rclone archive verification and restore-policy checks. Existing LXC test data was backed up before updating its dedicated service.
  - **Still pending:** backup notifications, automated GeoLite provisioning, and final country/proxy acceptance. Do not mark Operations complete until these follow-ups are delivered.
- [ ] 6 Distribution: Debian/LXC/Docker/Windows installers and maintenance, successful-main CI packages with provenance/admin update confirmation/rollback; complete documentation. Each platform installs/upgrades/repairs/restores independently.

## Review focus
1. Offline browser cache following restore: test normal automatic continuation; document accepted risk.
2. Read-only shares: locally editable browser UI must never grant server writes or stall unrelated private profiles.
3. Equal URLs in different folders: preserve intentional placements; URL collision checks are limited to the caller-visible scope to protect private data. Hidden-scope collisions remain separate records and can surface in owner duplicate review. Explicit visible same-folder placement conflicts return 409. Stock floccus 5.13.0 instead resolves a same-folder move with a DELETE-only flow; when it removes the source's last placement and exactly one same-URL record remains visible, copy the source title/tags to that survivor before trashing the source. Do not merge hidden data or guess among multiple visible survivors; test this in all supported browsers.
4. Proxy chains/private IP classification: no remote visitor bypass through a tunnel's private socket.
5. Fresh-host restoration: include secrets/settings/manifest, do not revive obsolete sessions during user-only restore.

## Worker contract
Keep scoped briefs, reviews, and progress in ignored .superpowers/sdd/bookmarksync/. Implementers follow their brief without spawning extra workers, pushing, deploying, or changing unrelated settings. Report changed files, test commands/results, and concerns; independently inspect and run acceptance checks before advancement. Security prompts require session authorization review, never bypass modes.

## Verification
Backend: go test ./..., go vet ./..., focused integration suites, Linux/Windows cross-builds. Frontend: typecheck/unit/build and browser workflows. Real Floccus separately. CI must not claim unavailable platform checks ran. An isolated build is running on the authorized test LXC; only that instance may be changed without fresh production authorization.
