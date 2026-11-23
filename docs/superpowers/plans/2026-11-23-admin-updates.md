# Admin Update Catalog and Approval Plan

**Goal:** Show eligible main-branch builds to administrators and let a recently reauthenticated admin explicitly approve a selected release.
**Architecture:** The web API presents only published SHA-addressed releases. Debian installation uses a dedicated host updater interface with a fixed command and strict tag validation; the web service never receives general root or Docker access. Updates always create a verified pre-update backup and roll back on failed readiness.
**Spec:** `docs/design.md` distribution requirements and `docs/distribution.md` release contract.

## Constraints
- Never auto-install. Require an explicit administrator action and recent reauthentication.
- Accept only published prereleases matching `build-<40 lowercase hex>` from the configured repository and require expected platform assets.
- Never trust release titles, body text, asset URLs, or admin-supplied tags as commands. Escape any rendered release notes.
- No arbitrary shell, Docker socket, or unrestricted privileged web endpoint.
- Keep the host updater unavailable on platforms where it has not been installed.

## Tasks
1. Add a bounded GitHub-release catalog client with injectable HTTP transport and tests for malformed, incomplete, draft, and non-SHA releases.
2. Add admin-only list and explicit approval routes, recent-auth gate, audit event, and OpenAPI contract tests.
3. Add an admin Settings card that shows installed/current build and available release metadata; require a checkbox confirmation and recent reauthentication to queue an update.
4. Add a Debian restricted updater bridge and installer-managed systemd/sudo policy; expose no general root command.
5. Verify Go/web suites and real UI authorization flows. Do not publish or push.

## Progress
- [x] Add bounded release catalog parsing and admin-only metadata endpoint with authorization tests.
- [x] Add explicit approval UI, recent-auth API route, sanitized audit event, and restricted Debian host runner.
- [ ] Publish a first `main` build to validate installation end to end.
