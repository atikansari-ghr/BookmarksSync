# BookmarksSync Implementation Plan

**Goal:** Implement docs/design.md through scoped tasks and independently verified milestones.
**Architecture:** Portable Go executable, embedded React/TypeScript UI, local SQLite, restricted optional maintenance helper.
**Spec:** docs/design.md is binding. Use mocscreenshots/ as visual references.

## Global constraints
- User identity/attribution and MIT per spec. Never commit social drafts.
- Work on build/bookmarksync in the user-selected workspace; do not force-push, mutate global settings, expose secrets, or deploy before tests.
- Do not represent unfinished features as working. No hardcoded demo authentication or fake successful operations.
- Existing floccus compatibility is a release gate. No substantial UI until compatibility decision.
- Tests cover behavior and failure paths; use failing regression tests before behavior fixes.

## Milestones and acceptance
- [x] 1 Foundation: connect remote preserving files; portable Go tooling; Go module, health/readiness/version endpoints, SQLite migrations/store, CLI serve, test/build CI, MIT, README. Verify persistence/reopen, graceful shutdown, JSON errors, Windows and Linux builds. Accepted at 9b90baa after independent review and checks; Ubuntu/Windows CI tests passed.
- [ ] 2 Compatibility: Nextcloud floccus capabilities/bookmark/folder/order/hash/lock APIs, per-connection credentials, roots, permissions and protocol contract tests. Then real browser floccus add/edit/move/delete/order/offline/read-only sharing proof. Stop if client constraints break design.
- [ ] 3 Identity/API: admin bootstrap, password/passkeys/TOTP/recovery, OIDC linking, private ownership/sharing, revocable sessions/tokens, /api/v1 + OpenAPI. Test isolation/revocation and auth failures.
- [ ] 4 Workspace/UI: native-style organizer and dashboards matching references; CRUD/multi-select/drag/drop/search/tags/favorites/import/export, duplicates, SSRF-safe link checks, Trash, persisted widgets/theme/branding. Browser/accessibility and negative authorization tests.
- [ ] 5 Operations: consistent snapshots, weekly/monthly destination backup/encryption/retention, full and user recovery, notifications, country access/trusted-proxy rules. Recovery to clean instance, corrupt archive, failed target, geo spoofing tests.
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
Backend: go test ./..., go vet ./..., focused integration suites, Linux/Windows cross-builds. Frontend: typecheck/unit/build and browser workflows. Real floccus separately. CI must not claim unavailable platform checks ran. LXC deployment awaits authorized SSH credentials; local independent work continues.
