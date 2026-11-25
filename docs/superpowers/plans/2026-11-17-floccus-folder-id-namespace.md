# Floccus Folder ID Namespace Implementation Plan

> Historical record: native Windows server scope and Windows acceptance gates are superseded by the [2026-11-24 Linux-only decision](../specs/2026-11-24-linux-only-design.md). Historical implementation/test statements below are preserved; they do not create current Windows work.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Work test-first in the shared feature branch; do not edit the Floccus extension.

**Goal:** Prevent stock floccus 5.13.0 from confusing local browser folder IDs with server folder IDs during reorder reconciliation.

**Architecture:** Keep positive SQLite folder IDs internal. Encode every real server folder ID as a negative wire ID (`wire = -(internal + 1)`); reserve `-1` for the existing virtual root. Translate all folder IDs at the Floccus HTTP boundary, including paths, request bodies, and responses. Bookmark IDs and the website/domain API remain unchanged.

**Tech Stack:** Go, SQLite, Go `testing`/`httptest`, existing Node browser harness, stock floccus 5.13.0.

**Spec:** `docs/design.md`; compatibility acceptance findings in `.superpowers/sdd/bookmarksync/server-sync-report.md`.

## Global Constraints

- Never modify or patch floccus.
- Keep database and domain-service folder IDs positive; only the Floccus protocol uses the negative namespace.
- Preserve `-1` as the virtual root; internal folder `1` is wire `-2`, internal folder `2` is wire `-3`.
- Reject `-1` where a concrete folder is required; reject malformed, overflowing, or wrong-type IDs without mutating data.
- Preserve account/share isolation, bookmark IDs, root titles, ordering, and existing endpoint behavior apart from folder wire IDs.
- Every browser result must be genuine sync with isolated profiles and the real server; missing prerequisites are `NOT_RUN`, never `PASS`.

## Review Focus

1. Canonical and nested folders round-trip through every folder-valued route while no positive concrete folder ID leaks onto the Floccus wire.
2. Folder references in bookmarks and child-order payloads map by item type; bookmark IDs stay positive.
3. `-1`, malformed negatives, overflow, and invalid folder/bookmark IDs fail safely with no partial writes.
4. Share-scoped folder IDs remain isolated after translation.
5. All four browsers remain stable when a local native folder ID numerically equals the underlying positive SQLite folder ID.

## Tasks

### Task 1: Define and test wire ID translation

- Add failing unit tests in `internal/floccus` for internal-to-wire and wire-to-internal conversion: `1 ↔ -2`, `2 ↔ -3`, `-1` root sentinel, invalid zero/positive concrete wire IDs, and overflow.
- Run the focused Go tests and confirm the expected failures.
- Implement typed helpers that distinguish concrete folder IDs, virtual root IDs, and bookmark IDs; do not broaden bookmark path parsing.
- Run `gofmt` and the focused tests.

### Task 2: Translate every Floccus folder field

- Add failing route tests for folder tree/children/hash/create/update/delete/import/childorder and bookmark `folders[]` input/output. Assert canonical roots serialize as `-2`/`-3`, nested folders are negative, bookmark IDs stay positive, and service calls still address the original positive SQLite rows.
- Map folder path/query IDs and folder-valued JSON fields at the handler boundary. Convert child-order folder IDs only when `type == "folder"`; retain bookmark IDs unchanged.
- Verify invalid folder IDs return the documented 400/404 response and never partially mutate.
- Run `go test -count=1 ./internal/floccus ./internal/bookmarks`, `go vet ./...`, and Linux/Windows `CGO_ENABLED=0` builds.

### Task 3: Preserve the latest visible duplicate metadata

- When Floccus removes the last placement of a bookmark and exactly one same-URL bookmark remains visible in the caller's scope, copy the removed record's title and tags to that survivor before trashing the orphan.
- Do not merge if the source still has any live placements, there is no visible duplicate, more than one visible survivor exists, or the candidate is visible only outside the caller's scope. Preserve existing behavior in those cases.
- Add service tests first for the unambiguous visible duplicate case, hidden-scope isolation, and non-orphan/multiple-survivor cases. Verify the remaining record keeps its id and placements; the removed source is no longer visible.
- Run the focused package tests, full Go suite, vet, and Linux/Windows CGO-free builds.

### Task 4: Prove the namespace in real browsers

- Extend the existing real-server harness only if needed to force local/server numeric ID collisions in fresh profiles.
- Add route assertions for virtual and share roots that pin `id: -1` and `parent_folder: 0` (the scope root has no concrete parent).
- Update `docs/floccus-compatibility.md` to distinguish negative Floccus wire folder IDs from positive internal/database IDs; keep bookmark wire IDs positive.
- Update the server-sync harness CLI bridge to translate a negative wire folder ID to its positive internal ID before provisioning shares, if that path uses one.
- Run `npm test`, `npm run test:firefox`, and the genuine server sync in Chrome, Edge, Brave, and Firefox. Require stable nested order on download, edit propagation, root mapping, share/read-only, lock, and cleanup cases.
- Verify the real sync retains the latest moved bookmark's title and tags when Floccus emits the delete-only duplicate resolution.
- Record exact browser versions, outcomes, and cleanup evidence; ordering passes only when no wrong-folder native moves or reversed `childorder` writes occur in repeated runs.
- Commit the reviewed change with sole author Atik Ansari.
