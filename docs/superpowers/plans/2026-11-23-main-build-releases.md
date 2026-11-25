# Main Build Releases Implementation Plan

> Historical record: native Windows server scope and Windows acceptance gates are superseded by the [2026-11-24 Linux-only decision](../specs/2026-11-24-linux-only-design.md). Historical implementation/test statements below are preserved; they do not create current Windows work.

> **For agentic workers:** This plan is executed by Codex in the current session.

**Goal:** Publish every fully verified `main` build as a SHA-addressed immutable-ready prerelease with binary provenance.

**Architecture:** Existing CI remains the release gate. Its Linux and Windows binaries become short-lived workflow artifacts; a least-privilege job assembles a draft release, adds checksums and build metadata, attests all files, then publishes it. Repository immutable-release protection locks assets and tags after publication.

**Tech Stack:** GitHub Actions, Go cross-builds, GitHub CLI, GitHub artifact attestations.

**Spec:** `docs/design.md`, Architecture and delivery; `docs/implementation-plan.md`, Milestone 6.

## Global Constraints
- Only successful pushes to `main` publish; pull requests never publish.
- Release tag includes the full commit SHA; no floating update artifact is created.
- Keep `contents:write`, OIDC, and attestations permissions only on the publisher job.
- Releases are drafts until every asset is attached; publishing makes them immutable when the repository setting is enabled.
- Do not push commits or deploy this workflow from the local branch.

## Review Focus
1. Partial matrix output must not create a release: publisher depends on the complete build matrix.
2. Duplicate/rerun SHA release must fail closed rather than replace existing assets.
3. Attestations and checksums must cover each shipped binary and manifest.
4. PRs and non-main pushes must never receive publication permissions or publish.
5. Draft assets must all exist before publishing so immutability protects a complete release.

## Files
- Modify `.github/workflows/ci.yml` for artifact upload, gated release assembly, provenance, and publish.
- Create `docs/distribution.md` for asset naming, verification, immutability, and remaining installer integration.
- Update `docs/implementation-plan.md` and `README.md` to report this slice accurately.

## Verification
- Validate workflow syntax and action pins.
- Run Go tests, vet, and Linux/Windows builds locally.
- Confirm repository immutable-release setting and validate workflow structure; an actual release requires a successful push to `main` and is not claimed from this branch.
