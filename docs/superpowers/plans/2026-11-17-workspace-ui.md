# Workspace and Dashboard Implementation Plan

> Historical record: native Windows server scope and Windows acceptance gates are superseded by the [2026-11-24 Linux-only decision](../specs/2026-11-24-linux-only-design.md). Historical implementation/test statements below are preserved; they do not create current Windows work.

> **For agentic workers:** Use Claude Code through Herd for scoped implementation; Codex coordinates, reviews each diff, and runs acceptance checks. Do not push, merge, or deploy during implementation.

**Goal:** Deliver the bookmark organizer, configurable user dashboard, admin dashboard and health view described in the approved product design.

**Architecture:** Extend the current embedded React/TypeScript application and Go `/api/v1` service. Persist user preferences and system branding in SQLite; collect platform health through a small Go service. Reuse existing bookmark/share/trash services and never bypass their scoping.

**Tech Stack:** Go 1.27.4, SQLite (`modernc.org/sqlite`), React/TypeScript/Vite, Playwright.

**Spec:** `docs/design.md`; existing API/auth contracts in `docs/design/identity-api.md` and `api/openapi.yaml`. A Claude read-only requirement map was completed for this plan on 2026-11-17.

## Approaches

1. **Extend the existing embedded React app and JSON API (selected).** This reuses the established cookie/CSRF client, shared-folder scoping, build pipeline, and SQLite domain services.
2. Build a second standalone frontend. Rejected because it duplicates authentication, packaging, and versioning.
3. Server-render the workspace in Go. Rejected because the shipped sign-in/security interface already uses React and the workspace needs interactive drag/drop and configurable widgets.

## Global Constraints

- Preserve Floccus protocol behavior, hashes, credentials, folder ordering, and share isolation.
- User and share data must always be scoped through `internal/bookmarks`; never add handler-level SQL that bypasses services.
- Admin responses may contain aggregates and operational diagnostics, never bookmark titles, URLs, tags, or folder names.
- User actions require CSRF and correct token scopes; read-only shares must remain read-only.
- Do not show placeholder success or dashboard widgets for backup/update services before those services exist in later milestones.
- Link checks may contact public destinations only; exclusions can narrow checks, never allow private destinations.
- Country restrictions and trusted-proxy policy remain in Operations (milestone 5), not this workspace milestone.
- Keep applied migrations immutable; Identity/API Favorites now uses migration 0004. This plan adds migration 0005 for link checks and 0006 for system settings/activity.
- Keep Linux and Windows builds working. Do not deploy to `192.168.10.226` in this milestone.
- Favorites behavior is approved and implemented in the Identity/API milestone as per-user bookmark metadata across placements. This workspace milestone may add its UI using the existing favorites API; do not add a second Favorites schema.

## Review Focus

1. A shared-folder recipient must not read another user's duplicate, recent-change, link-check, or dashboard data.
2. Bulk moves that encounter a lock, revoked share, or invalid item must not partially modify the collection.
3. Link checking must reject private, loopback, link-local, mapped IPv4, rebinding, and redirect-to-private targets.
4. Admin health and activity output must not expose private bookmark content or secrets.
5. Browser UI errors, interrupted requests, and concurrent Floccus sync must leave the bookmark tree recoverable and correctly ordered.

---

### Task 1: User dashboard summary and recent changes

**Files:** `internal/bookmarks/dashboard.go`, `internal/bookmarks/dashboard_test.go`, `internal/api/api.go`, `internal/api/api_account_handlers.go`, `internal/api/dashboard_test.go`, `api/openapi.yaml`.

**Interfaces:** Add `Service.Dashboard(ctx, privateScope, since, page, limit) (DashboardSummary, error)` in `internal/bookmarks` and `GET /api/v1/me/dashboard` in `internal/api/api_account_handlers.go` (requires `bookmarks:read`, always uses the user's private scope). `DashboardSummary` contains `totalBookmarks` (distinct live records) and `recentChanges` (counts plus paginated items with type, item ID, title, URL, and timestamp). The seven-day window uses existing creation/update/deletion timestamps and reports current item state, not a full event history. Placement-only edits are not represented because they do not update bookmark timestamps; restoring Trash appears as an update. The API handler adds `lastContactAt` from the most recently used active Floccus credential (including share connections) and `duplicateCount` from Task 2; keep identity data out of the bookmarks service.

- [x] Add domain/API tests for summary counts, seven-day boundaries, current-state change types, pagination, empty results, private-only visibility, read token authorization, duplicate count, and latest active Floccus contact. Revoked connections do not count as current sync status.
- [x] Run focused tests and capture RED for the missing service/API; add a RED regression for newer revoked credentials and confirm it fails before filtering them.
- [x] Implement the service/API and document its schema in OpenAPI.
- [x] Run focused and full Go suites, vet, formatting, OpenAPI lint, and CGO-free Linux/Windows builds.
- [ ] Commit the scoped change with Atik Ansari as sole author.

### Task 2: Duplicate review and safe removal

**Files:** new `internal/bookmarks/duplicates.go`, `internal/bookmarks/duplicates_test.go`, `internal/api/api_resource_handlers.go`, API policy/OpenAPI, domain and HTTP tests.

**Interfaces:** Add caller-scoped duplicate groups and a count to `Service.ListDuplicates(ctx, scope, page, limit)`. Count excess distinct bookmark records per exact URL; multiple folder placements of one record are intentional and do not count. `GET /api/v1/duplicates` returns `{ "count": n, "groups": [...] }`; each group identifies the URL and its visible bookmark records/folders. Remove a selected record through the existing bookmark DELETE/Trash API. Never auto-delete.

- [x] Add domain/API tests for distinct records versus placements, exact URLs, caller-scope isolation, pagination, read-only/token boundaries, non-destructive review, Trash deletion and restore.
- [x] Run failing domain/API tests, then implement through service methods.
- [x] Add OpenAPI and run full Go tests, vet, formatting, lint, and CGO-free Linux/Windows builds.
- [ ] Commit the scoped change.

### Task 3: Atomic multi-bookmark moves

**Files:** new `internal/bookmarks/bulk.go`, `internal/bookmarks/bulk_test.go`, `internal/api/api.go`, `internal/api/api_resource_handlers.go`, OpenAPI and API tests.

**Interfaces:** Add `Service.MoveBookmarks(ctx, scope, ids, sourceFolderID, destinationFolderID) error` and `POST /api/v1/bookmarks/bulk-move` with `{ "bookmarkIds": [1, 2], "sourceFolderId": 4, "folderId": 3 }` (204 on success); accept 1–100 unique positive IDs. Only the placement each record has in the source folder moves; placements in other folders the caller can see are kept. The source folder, every bookmark (with a live placement in the source) and the destination must be visible in the caller's scope, and source and destination must be distinct real folders (not the private root). The submitted ID order is the resulting order among moved bookmarks; append them after existing destination children. A record already placed in the destination is not duplicated: its source placement is removed and it takes its submitted place at the end. Validate everything inside one transaction; either move all items and update order, or change none. Enforce the same lock, share, and duplicate-placement rules as single-item writes: another record with the same exact URL in the destination is refused with 409 `conflict`. `ErrConflict` maps to HTTP 409 in the shared domain-error writer.

- [x] Add domain/API tests for ordering, preserved other placements, existing destination placements, input validation, atomic rejection, Floccus locks, share downgrade/revocation, URL conflicts, CSRF and token scopes.
- [x] Run focused tests and capture RED before implementing the service/API.
- [x] Implement the transactional service, `sourceFolderId` request field, route policy, OpenAPI entry and 409 mapping for `ErrConflict`.
- [x] Run full Go tests, vet, formatting, OpenAPI lint and CGO-free Linux/Windows builds.
- [ ] Commit the scoped change.

### Task 4: Workspace navigation and bookmark organizer

**Files:** `internal/webui/webui.go` and tests, `web/src/App.tsx`, `web/src/api.ts`, new `web/src/pages/OrganizerPage.tsx` and `web/src/pages/DuplicatesPage.tsx`, focused components under `web/src/sections/`, `web/src/styles.css`, and web tests. Register `/bookmarks` and `/duplicates` as supported application routes without exposing unknown paths.

**Interfaces:** Add typed client calls for folder/bookmark/search/tag/share/import/export/Trash/duplicate APIs, `POST /api/v1/bookmarks/bulk-move`, and existing `PUT /api/v1/folders/{id}/order`. Build a folder tree and list-first bookmark view with optional grid mode. Support create/edit/delete, placement-aware moves, pointer drag/drop and keyboard rearrangement for both folders and bookmarks, multi-select, search, tags, shares, duplicate review/removal, import/export, and Trash restore/purge. Preserve server ordering and show errors without false success.

- [x] Add `OrganizerLoadsAndShowsServerErrors`, `OrganizerSupportsCRUDSearchAndTags`, `OrganizerMovesSelectionByPointerDrop`, `OrganizerMovesSelectionWithKeyboard`, `OrganizerHonorsReadOnlyShare`, `DuplicatesAreReviewedBeforeRemoval`, and `OrganizerImportsExportsAndRestoresTrash`; verify keyboard-only use and server-side rollback.
- [x] Run the new organizer tests first and confirm they fail because the pages/components are missing.
- [x] Implement the organizer and responsive/accessibility behavior.
- [x] Run web typecheck/tests/build and verify embedded assets are deterministic.
- [x] Run a Chromium smoke against the production-built UI with a mocked API; pointer drag/drop and keyboard move both sent the placement-aware request.
- [ ] Verify organizer edits against the real API and stock Floccus in supported browsers; organizer layout has been visually checked at desktop and mobile widths against the approved reference.
- [ ] Commit the scoped change.

### Task 5: Public-only link checker and exclusions

**Files:** new `internal/linkcheck/checker.go`, `internal/linkcheck/worker.go`, `internal/linkcheck/checker_test.go`, additive migration `internal/database/migrations/0005_link_checks.sql`, user settings validation, API routes/OpenAPI, organizer UI and focused tests.

**Interfaces:** Add `Service.SubmitCheck(ctx, scope, bookmarkIDs) (Job, error)` and `Service.CheckStatus(ctx, scope, jobID) (Job, error)`. `POST /api/v1/link-checks` requires `bookmarks:write`, accepts up to 50 bookmark IDs and returns a job ID; `GET /api/v1/link-checks/{id}` requires `bookmarks:read` and returns only that user's results. Persist jobs/results in migration `0005_link_checks.sql` and resume queued work after restart. Check HTTP(S) URLs only; report other schemes as skipped. Store per-user domain and IP/CIDR exclusions using validated `linkcheck.exclusions` user settings; exclusions match exact domains/subdomains or addresses inside a CIDR and only skip checks. Bound each URL to 10 seconds, 1 MiB, and five redirects. Resolve and validate every DNS answer, dial only a validated public IP, and revalidate every redirect target to block rebinding and private-address redirects.

- [x] Add `TestCheckerRejectsPrivateAndMappedAddresses`, `TestCheckerRejectsDNSRebinding`, `TestCheckerRevalidatesRedirectTargets`, `TestCheckerBoundsTimeAndResponseSize`, and `TestExclusionsOnlyNarrowChecks`; include cross-user result isolation.
- [x] Add `TestQueuedLinkCheckResumesAfterRestart` and verify completed jobs do not retry unexpectedly.
- [x] Capture RED for the SSRF cases before implementation; Codex added regression tests for mapped public IPv4, mixed public-IP exclusions, revoked/downgraded shares, and transient worker storage failures, observed RED, then fixed them.
- [x] Implement injectable DNS/dial dependencies and bounded request/result storage; add per-user exclusions and selection-based organizer controls.
- [x] Run focused and full Go tests, vet, gofmt, OpenAPI contract tests, web typecheck/tests/build, deterministic embedded builds, and CGO-free Linux/Windows builds.
- [ ] Add and run a real-browser link-check UI smoke against the real API; decide on job/result retention before production deployment.
- [ ] Commit the scoped change.

### Task 6: User widgets and theme preferences

**Files:** user-setting validators/storage, dashboard API, `web/src/pages/DashboardPage.tsx`, new widget components, and theme tokens/styles/tests.

**Interfaces:** Show top widgets for Total Bookmarks, Recent Changes, Sync Status (last contact), and Duplicate Count. The Duplicate Count widget opens `/duplicates` for review. Persist a validated `dashboard.layout` JSON object per account containing widget IDs, visibility, order, and `small|medium|large` size; reset restores the four-widget default. Provide Classic, Ocean Glass, Midnight Glass, and Warm Glass; light/dark and reduced-transparency modes. Admin-selected per-user defaults apply until a user override exists.

- [x] Add `TestWidgetSettingsPersistPerUserAndReset`, `TestThemeSettingsRejectUnknownValues`, `DashboardReordersWidgetsByKeyboard`, and `DashboardFallsBackAtMobileWidth`; settings are isolated per account.
- [ ] Add `TestUserThemeOverrideTakesPrecedenceOverAdminDefault` with Task 7's admin defaults.
- [x] Run the failing Go setting tests before implementation. Frontend tests cover keyboard ordering and mobile layout selection but were added after implementation (no frontend RED was captured).
- [x] Implement the dashboard and theme controls against the existing APIs.
- [x] Run web typecheck/tests/build, full Go tests/vet, gofmt, diff check, and a native Windows build. Real browser smoke remains pending.
- [ ] Commit the scoped change.

### Task 7: Admin dashboard, health, and branding

**Files:** new `internal/health/collector.go` with Linux/Windows collectors and tests, additive migration `internal/database/migrations/0006_workspace_admin.sql`, system-settings persistence/API, per-user admin defaults, sanitized admin activity service/API, `web/src/pages/AdminPage.tsx`, admin components and tests.

**Interfaces:** Add admin-only `GET /api/v1/admin/health`, `GET/PATCH /api/v1/admin/settings`, `POST /api/v1/admin/settings/assets`, `GET/PATCH /api/v1/admin/users/{id}/settings`, `GET /api/v1/admin/activity`, and public `GET /api/v1/auth/presentation` exposing only safe login appearance values. `GET /api/v1/admin/health` reports host CPU, memory, disk, uptime, DB readiness, and sync errors. Show users, per-user counts/storage, health and sanitized operational activity. Add configurable logo/background/colors/copy and split-screen, centered-card, and plain login layouts. Keep actual app version and release-year copyright visible. Save system settings and sanitized admin events in `0006_workspace_admin.sql`; serve uploaded PNG/JPEG/WebP images (up to 2 MiB each) only from generated paths under the private data directory. Keep backup/update/alert widgets absent until their later milestones deliver real data.

- [x] Add admin health, sanitization, unavailable-metric, per-user defaults, and branding validation/upload tests.
- [x] Verify account overrides take precedence over admin defaults.
- [x] Run failing Go and UI tests before implementation; both server and upload UI regressions were observed before fixes.
- [x] Implement cross-platform collection, admin settings, login presentation, and generated-path image upload/serving.
- [x] Run full Go tests and vet, full web tests, web typecheck/build, and Linux/Windows builds.
- [x] Run the real-browser login/security/admin health and branding upload smoke; review desktop and phone screenshots.
- [ ] Commit the scoped change.

### Favorites decision (resolved for Identity/API)

- [x] Favorites are per-user metadata on the bookmark record, follow every placement, can be set on visible view-only shares without changing owner data, are not sent to Floccus, survive Trash/restore, and transfer to the surviving record when duplicates merge. Migration 0004 and the Identity/API routes are tracked in `docs/superpowers/plans/2026-11-18-bookmark-favorites.md`.

### Final acceptance

- [x] Run full Go tests/vet, web typecheck/tests/build, browser harness tests, and Firefox harness tests.
- [x] Run the real Chromium login/security/admin branding smoke and review admin desktop/phone screenshots.
- [ ] Run the stock-Floccus organizer matrix; verify accessibility, mobile dashboard layout, and top-widget order/resize against the approved references.
- [ ] Have an independent reviewer inspect the milestone diff and fix Critical/Important findings test-first.
- [x] Update README, OpenAPI and implementation-plan status accurately. Do not push, merge or deploy.
- [x] Refresh the end-user guide for the new workspace and admin settings (`docs/workspace-guide.md`).
