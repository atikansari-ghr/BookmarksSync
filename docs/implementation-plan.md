# BookmarksSync Implementation Plan

**Goal:** Implement docs/design.md under Codex supervision with Claude Code/Herdr workers.
**Architecture:** Portable Go executable, embedded React/TypeScript UI, local SQLite, restricted optional maintenance helper.
**Spec:** docs/design.md is binding. Use mocscreenshots/ as visual references.

## Global constraints
- User identity/attribution and MIT per spec. Never commit social drafts.
- Work on build/bookmarksync in the user-selected workspace; do not force-push, mutate global settings, expose secrets, or deploy before tests.
- Do not represent unfinished features as working. No hardcoded demo authentication or fake successful operations.
- Existing floccus compatibility is a release gate. No substantial UI until compatibility decision.
- Tests cover behavior and failure paths; use failing regression tests before behavior fixes.

## Milestones and acceptance
- [ ] 1 Foundation: connect remote preserving files; portable Go tooling; Go module, health/readiness/version endpoints, SQLite migrations/store, CLI serve, test/build CI, MIT, README. Verify persistence/reopen, graceful shutdown, JSON errors, Windows and Linux builds.
- [ ] 2 Compatibility: Nextcloud floccus capabilities/bookmark/folder/order/hash/lock APIs, per-connection credentials, roots, permissions and protocol contract tests. Then real browser floccus add/edit/move/delete/order/offline/read-only sharing proof. Stop if client constraints break design.
- [ ] 3 Identity/API: admin bootstrap, password/passkeys/TOTP/recovery, OIDC linking, private ownership/sharing, revocable sessions/tokens, /api/v1 + OpenAPI. Test isolation/revocation and auth failures.
- [ ] 4 Workspace/UI: native-style organizer and dashboards matching references; CRUD/multi-select/drag/drop/search/tags/favorites/import/export, duplicates, SSRF-safe link checks, Trash, persisted widgets/theme/branding. Browser/accessibility and negative authorization tests.
- [ ] 5 Operations: consistent snapshots, weekly/monthly destination backup/encryption/retention, full and user recovery, notifications, country access/trusted-proxy rules. Recovery to clean instance, corrupt archive, failed target, geo spoofing tests.
- [ ] 6 Distribution: Debian/LXC/Docker/Windows installers and maintenance, successful-main CI packages with provenance/admin update confirmation/rollback; complete documentation. Each platform installs/upgrades/repairs/restores independently.

## Review focus
1. Offline browser cache following restore: test normal automatic continuation; document accepted risk.
2. Read-only shares: locally editable browser UI must never grant server writes or stall unrelated private profiles.
3. Equal URLs in different folders: preserve intentional placement, do not destructively deduplicate.
4. Proxy chains/private IP classification: no remote visitor bypass through a tunnel's private socket.
5. Fresh-host restoration: include secrets/settings/manifest, do not revive obsolete sessions during user-only restore.

## Worker contract
Codex writes scoped briefs/reviews/progress to ignored .superpowers/sdd/bookmarksync/. Workers implement only their brief, no subagents, no pushes or deployments, no unrelated settings. Report changed files, test commands/results, concerns; controller independently inspects/runs acceptance checks before advancement. Keep workers persistent through hc for steering. Security prompts require session authorization review, never bypass modes.

## Verification
Backend: go test ./..., go vet ./..., focused integration suites, Linux/Windows cross-builds. Frontend: typecheck/unit/build and browser workflows. Real floccus separately. CI must not claim unavailable platform checks ran. LXC deployment awaits authorized SSH credentials; local independent work continues.
