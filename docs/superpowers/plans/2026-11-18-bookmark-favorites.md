# Bookmark Favorites Implementation Plan

> Historical record: native Windows server scope and Windows acceptance gates are superseded by the [2026-11-24 Linux-only decision](../specs/2026-11-24-linux-only-design.md). Historical implementation/test statements below are preserved; they do not create current Windows work.

> **For agentic workers:** Use test-first implementation, then independently review and verify. Do not push, merge, deploy, or commit until the controller accepts the work.

**Goal:** Add per-user favorites to the Identity/API milestone, with one favorite per bookmark across all of its folder placements.

**Architecture:** Store favorites in an additive SQLite relation keyed by `(user_id, bookmark_id)`. The bookmark service resolves favorite state for the current `Scope.Actor`, filters visible bookmarks, and changes only the actor's relation. A user may star a bookmark visible through a read-only share without changing the owner's bookmark or share permissions.

**Tech Stack:** Go, SQLite migrations, existing bookmark service and `/api/v1` router, OpenAPI 3.1.

**Spec:** `docs/design/identity-api.md` and `docs/design.md`.

## Global Constraints

- Preserve existing Floccus sync and bookmark hashes; Favorites are API metadata and do not enter the Nextcloud wire format.
- Keep SQL in `internal/bookmarks`; API handlers must call domain services.
- Derive the favorite owner from the authenticated API actor; never trust a user ID from the request.
- A share viewer's favorite is private and must not grant bookmark write access or modify owner data.
- Preserve `0001` through `0003`; add migration `0004`.
- Do not push, merge, deploy, or commit during implementation.

## Review Focus

1. A view-only share recipient can star a visible bookmark without changing shared data or gaining edit access.
2. A user cannot read or change another user's favorite relation through bookmark IDs or token scopes.
3. Favorite state follows a bookmark across folder moves and is distinct per user.
4. Removed or inaccessible placements do not make a favorite filter reveal a bookmark outside the active scope.
5. Existing databases migrate without losing users, bookmarks, shares, credentials, or settings.

---

### Task 1: Domain storage and scope-aware favorite state

**Files:** Create `internal/database/migrations/0004_bookmark_favorites.sql`; modify `internal/bookmarks/items.go`, `internal/bookmarks/api_support.go`, migration tests, and bookmark domain tests.

**Interfaces:** Add `Favorite bool` to `Bookmark`, `Favorite *bool` to `ListQuery`, and `SetFavorite(context.Context, Scope, int64, bool) error` to `Service`. Resolve and filter favorites by `Scope.Actor`; actorless Floccus scopes continue to see `false` and never write favorites. Favorite writes validate the active actor and currently visible bookmark inside the transaction, but do not require collection edit permission or acquire a Floccus collection lock.

- [x] Write failing tests for fresh/upgrade migration, per-user isolation, shared-viewer favorites, favorite filtering within scope, persistence across placements, and preserving the user's star when duplicate records merge.
- [x] Run focused tests and confirm expected failure before implementation.
- [x] Add migration 0004 with foreign keys and a composite primary key; hydrate/filter state and implement idempotent set/unset through the domain service.
- [x] Run focused domain/database tests and confirm pass.

**Duplicate-merge favorite transfer (review fix, 2026-11-18).** Favorites are private per user and follow a bookmark across all placements. When `RemovePlacement` merges the just-orphaned source record into its sole visible same-URL survivor (`mergeIntoSoleVisibleDuplicate`), each source favorite is copied to the survivor for that same user (`INSERT ... SELECT user_id ... ON CONFLICT DO NOTHING`). One user's favorite never becomes another user's. The source favorite rows are kept, so a Trash restore of the source keeps its favorite state.

- [x] Regression test `TestMergeTransfersSourceFavoritesPerUserAndKeepsSourceRow` in `internal/bookmarks/duplicate_merge_test.go`, using API scopes because Floccus scopes are actorless. Alice favorites her private source, bob creates the survivor through an edit share, and alice removes the source's last placement. The test checks that alice sees the survivor as a favorite, bob does not, bob has no favorite rows, alice's source row remains, and the restored source is still a favorite.
- [x] RED before the fix: `duplicate_merge_test.go:222: alice's favorite on the merged source did not follow to the survivor`. The source favorite stayed only on the trashed source.
- [x] GREEN after the fix: the focused test passes, and `go test -count=1 ./internal/bookmarks` passes (`ok`). `go vet ./internal/bookmarks` and `gofmt -l internal/bookmarks` are clean. `go test -count=1 ./...` passes too.
- [x] Floccus wire regression `TestFloccusDeleteDuplicateTransfersFavoriteWithoutWireLeak` verifies an authenticated protocol DELETE triggers the merge, the owner favorite follows to the survivor, and Floccus GET list/item responses omit a `favorite` field. Focused test and full `go test -count=1 ./...` pass.

### Task 2: API routes and OpenAPI contract

**Files:** Modify `internal/api/api.go`, `internal/api/api_test.go`, `internal/api/openapi_test.go`, and `api/openapi.yaml`.

**Interfaces:** Include `favorite` in bookmark responses; support `GET /api/v1/bookmarks?favorite=true|false`; add idempotent `PUT` and `DELETE /api/v1/bookmarks/{id}/favorite` with no body and 204 success. Favorite mutation resolves a read-only bookmark scope so a viewer can update personal metadata; token authorization still requires `bookmarks:write`.

- [x] Add failing HTTP tests for favorite list/filter, PUT/DELETE, idempotence, CSRF, token scopes, hidden IDs, and read-only share isolation.
- [x] Run focused API tests and confirm expected failures.
- [x] Implement routes through the bookmark service and document parameters, schemas, responses, and token scope in OpenAPI.
- [x] Run focused API and OpenAPI tests and confirm pass.

### Task 3: Documentation and acceptance

**Files:** Modify `README.md`, `docs/design.md`, `docs/design/identity-api.md`, `docs/implementation-plan.md`, `docs/superpowers/plans/2026-11-17-identity-api.md`, and the dependent workspace plan `docs/superpowers/plans/2026-11-17-workspace-ui.md`.

- [x] Document per-user bookmark-level favorites and read-only shared-folder behavior; remove the pending-decision note and shift future migration numbers.
- [x] Run `go test -count=1 ./...`, `go vet ./...`, `gofmt -l cmd internal`, `git diff --check`, and CGO-free Linux/Windows amd64 builds.
- [x] Independently review authorization, migration, OpenAPI parity, and Floccus compatibility; fix the duplicate-merge favorite transfer finding and rerun affected checks.
- [x] Leave local changes uncommitted pending the milestone's remaining acceptance gates.

**Acceptance caveat:** Local Go tests, race tests, vet, formatting, cross-builds, `govulncheck@v1.9.0`, and the configured Node audits pass. Redocly validates the updated OpenAPI with 72 warnings and no errors. GitHub Actions run 44037891729 passed only on the remote base `b0eab923`; there is no run for current local HEAD `2b40496`, and this task forbids pushing. The Identity/API milestone remains unaccepted until its current-head CI gate is resolved.
