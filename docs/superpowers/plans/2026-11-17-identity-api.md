# Identity and API Implementation Plan

> **For agentic workers:** Use Claude Code through Herd for the scoped implementation tasks; Codex coordinates, independently reviews each task, runs acceptance checks, and integrates. Do not push or deploy during implementation.

**Goal:** Deliver administrator-provisioned browser accounts, password/passkey/TOTP authentication, linked Google/Authentik login, revocable sessions and scoped API tokens, the versioned user/admin API with OpenAPI, and a minimal usable login/security interface.

**Architecture:** Extend the current Go identity service and SQLite store. Put interactive authentication behind opaque server-side sessions and CSRF-protected same-origin requests; keep Floccus app credentials separate. Add `/api/v1` handlers that derive identity and scope from verified sessions or narrowly scoped personal API tokens and reuse existing bookmark services.

**Tech Stack:** Go 1.27.4, `modernc.org/sqlite`, React/TypeScript/Vite, Playwright. WebAuthn: `github.com/go-webauthn/webauthn` v0.18.2; OIDC: `github.com/coreos/go-oidc/v3` v3.15.0 (plus its OAuth2 dependency), pinned in `go.mod` and covered by CI.

**Spec:** `docs/design.md`; approved milestone design: `docs/design/identity-api.md`.

## Global Constraints

- Admin-created accounts only; never create or link accounts by email equality.
- A linked OIDC identity is keyed by verified issuer and subject; linking requires an authenticated account and recent reauthentication.
- Store hashes, not raw web-session secrets, recovery codes, or personal API tokens.
- Floccus credentials remain independent from interactive sessions and `/api/v1` tokens.
- Admin APIs expose aggregate/system information, never another user's private bookmarks.
- User sessions: 12-hour idle expiry, 30-day absolute expiry; logout, account suspension, password/authenticator change, and admin reset revoke sessions.
- Admin actions that change account/security state require reauthentication within five minutes.
- Browser auth uses HttpOnly, Secure, host-only SameSite=Lax cookies, CSRF tokens, and origin validation. Never store auth tokens in browser storage or logs.
- Passkey and OIDC relying-party origins come only from explicit `BOOKMARKSYNC_PUBLIC_URL` (origin only, HTTPS except loopback development), never an untrusted Host or forwarded header.
- TOTP seeds are encrypted with an instance key in a private file in the data directory; system recovery must preserve that key.
- Preserve current schema/data and all Linux/Windows builds. Do not deploy to the LXC during this milestone.

## Review Focus

1. Unknown, disabled, and wrong-password accounts return indistinguishable public login responses and comparable password-hash work; pin with password-auth tests.
2. Session fixation, idle/absolute expiry, and concurrent revocation cannot leave a usable session; pin with identity and middleware tests.
3. CSRF, Origin, cookie, and reverse-proxy inputs cannot authenticate a cross-site mutation or alter the WebAuthn/OIDC origin; pin with HTTP integration tests.
4. WebAuthn challenges, TOTP steps, recovery codes, OIDC state/nonce, and OAuth codes are one-use and bound to the expected account/flow; pin replay tests in each factor/provider task.
5. User, share, API-token-scope, and admin boundaries hold across every `/api/v1` handler; pin cross-account, read-only, and role tests before adding each route family.

---

### Task 1: Identity security schema, web sessions, and personal API tokens

**Files:** Create `internal/database/migrations/0003_identity_api.sql`, `internal/identity/sessions.go`, `internal/identity/tokens.go`, and their tests. Modify identity account activation and migration fixtures.

**Interfaces:** Add service methods for creating, resolving, listing, revoking, and expiring sessions and personal API tokens. Session/token secrets are generated from cryptographic randomness and stored only as SHA-256 hashes. Token scopes are `bookmarks:read`, `bookmarks:write`, `shares:read`, and `shares:manage`; personal tokens never authorize admin endpoints. Migration 0003 creates the complete milestone schema up front: sessions, API tokens, WebAuthn credentials, encrypted TOTP state, recovery codes, linked OIDC subjects, short-lived auth flows, and whitelisted user settings. Later tasks consume this schema without editing an applied migration.

- [x] Write tests for opaque secret generation, hash-only persistence, session idle/absolute expiry, rotation, per-session revoke, user-wide revocation, token scopes, and one-time token display.
- [x] Run focused identity tests and confirm they fail for missing schema and methods.
- [x] Add the migration and service methods. Make deactivation revoke web sessions and API tokens in the same transaction as existing credential revocation.
- [x] Run focused tests, `go test -count=1 ./...`, `go vet ./...`, and `gofmt -l cmd internal`.
- [x] Commit the task and provide Claude's change summary and test output for Codex review.

### Task 2: Browser password login, sessions, CSRF, and reauthentication

**Files:** Create `internal/auth/` session middleware, handlers, and tests. Modify `internal/config/config.go`, `cmd/bookmarksync/main.go`, and server registration/wiring.

**Interfaces:** Register `/api/v1/auth/*` JSON endpoints for login, logout, current session, CSRF bootstrap, session listing/revocation, and recent reauthentication. Reserve `/auth/*` for the login/security pages added in Task 6; do not add placeholder browser pages here. Set a `__Host-` session cookie with `Secure; HttpOnly; SameSite=Lax; Path=/`; add a session-bound CSRF token for mutations. Add explicit `BOOKMARKSYNC_PUBLIC_URL` (origin only, HTTPS except loopback development); require exact same-origin checks and do not trust forwarded headers. Password login returns a generic error for every invalid account state. Bound rate-limit storage by normalized username and direct peer address, with a fixed maximum number of tracked keys; do not parse forwarded headers.

- [x] Test successful login, wrong/unknown/inactive users, session rotation, logout, cookie attributes, idle/absolute expiry, immediate revoke, CSRF/origin rejection, and reauthentication age.
- [x] Run focused HTTP tests and confirm they fail before implementation.
- [x] Implement password login, logout, current-session and session-list/revoke routes, CSRF bootstrap, and reauthentication with existing Argon2id verification.
- [x] Run auth integration tests, `go test -count=1 ./...`, `go vet ./...`, formatting, and Linux/Windows cross-builds.
- [x] Commit and require independent review before factor work begins.

### Task 3: Passkeys, TOTP, and recovery codes

**Files:** Create `internal/identity/webauthn.go`, `internal/identity/totp.go`, `internal/identity/secretbox.go`, `internal/auth/factors.go`, and focused tests. Consume the WebAuthn, TOTP, recovery-code, and auth-flow tables from migration `0003`. Modify `cmd/bookmarksync/main.go` to load the instance key from the data directory and pass it to the identity service.

**Interfaces:** Support discoverable passkey login and authenticated add/rename/remove flows. Use WebAuthn relying-party ID/origin from `BOOKMARKSYNC_PUBLIC_URL`. TOTP enrollment displays a provisioning URI and confirmation code; recovery codes are displayed once and consumed once. A pending password+TOTP challenge is short-lived and cannot access normal routes.

- [x] Write tests for WebAuthn origin/RP/challenge binding, challenge reuse, credential ownership, counter handling, TOTP enrollment/verification/replay, recovery-code single use, encrypted TOTP ciphertext, and instance-key creation/permissions on Unix and Windows. (Unix key-permission tests run only on Linux; executed by the CI Ubuntu job, not locally.)
- [x] Run factor tests and confirm expected failures.
- [x] Implement ceremonies using pinned `go-webauthn/webauthn` v0.18.2; implement standards-based TOTP with encrypted cryptographic seeds and hashed recovery codes. Generate/load an instance AES-GCM key in a private file, apply a restrictive Windows ACL, and fail closed if the key cannot be secured or used.
- [x] Exercise passkeys in Playwright with a virtual authenticator; test all service-level replay and mismatch cases.
- [x] Run focused, full Go, formatting, vet, and cross-build checks; commit for review.

### Task 4: Google and Authentik OIDC linking and sign-in

**Files:** Create `internal/identity/oidc.go`, `internal/auth/oidc.go`, and tests. Consume the OIDC identity and auth-flow tables from migration `0003`; modify config, startup wiring, and operator documentation.

**Interfaces:** Operator config supplies Google client ID/secret and Authentik issuer/client ID/secret through environment variables. Expose begin/callback/unlink routes. Use authorization-code flow with PKCE, state, nonce, exact redirect URI, issuer/audience/signature/expiry checks. Persist only provider issuer, verified subject, and non-sensitive display fields. No auto-signup or email-based linking.

- [x] Write tests with a local fake OIDC provider for state, nonce, PKCE, signature, issuer, audience, expiry, missing/changed subject, duplicate link, unlinked login, reauthentication-required unlink, bounded discovery/callback timeouts, and provider secrets absent from logs.
- [x] Run the provider tests and confirm they fail before implementation.
- [x] Implement provider discovery and validation using pinned `go-oidc` v3.15.0 and OAuth2 PKCE support. Bind every transaction to a short-lived server-side flow record.
- [x] Run focused and full Go tests, vet, formatting, and cross-builds; commit for review.

### Task 5: `/api/v1` user and administrator API with OpenAPI

**Files:** Create `internal/api/` router, auth context, handlers, and tests; create `api/openapi.yaml`; modify server registration and reuse services in `internal/bookmarks` and `internal/identity`.

**Interfaces:** Implement self/profile/settings, bookmarks/folders/search/tags/favorites, shares, Trash, safe Netscape/XBEL import/export, session/token management, and per-user Floccus connection create/list/revoke routes. Admin routes manage accounts, Floccus connections, and supported system settings. Session auth is cookie+CSRF; personal-token auth requires the exact scope for each operation and cannot call admin routes. Admin routes require an active admin web session plus recent reauthentication. Return consistent JSON errors and pagination; show each generated secret only once.

- [x] Write table-driven route tests for method/path/schema errors, pagination limits, each token scope, ownership, share view/edit permissions, admin role, last-admin protection, deactivated users, and no private data in admin responses.
- [x] Add a migration fixture test proving existing users, credentials, bookmarks, and shares remain intact.
- [x] Implement handlers through domain services; do not add SQL that bypasses service scoping. Favorites are deferred until the user rules on their storage (no migration 0004 yet).
- [x] Validate OpenAPI syntax and add a test that every registered `/api/v1` route/method is represented.
- [x] Run API integration suite, full Go tests, vet, formatting, builds, and mutation checks for authorization boundaries; commit for review.

### Task 6: Minimal login and account-security interface

**Files:** Create `web/` React/TypeScript/Vite app and `internal/webui/` embed/static handler. Modify CI and server wiring to build/embed the UI.

**Interfaces:** Pages: split-screen login matching `mocscreenshots/exec-d71e4627-7ef3-4ae3-8bf0-501e95813482.png`, TOTP challenge, passkey sign-in/enrollment, TOTP enrollment, one-time recovery-code display, linked-provider management, active sessions, personal API tokens, and Floccus connection credential management. Use same-origin APIs and CSRF bootstrap; local login uses username, not email; render provider buttons only when configured; use approved app title/tagline, show build version and `© <year> Atik Ansari`; provide administrator-reset guidance instead of a password-reset flow. No dashboard, theme picker, branding editor, or admin dashboard in this task.

- [x] Write frontend component/flow tests and Playwright tests for login, TOTP, passkey using virtual authenticator, session revoke, OIDC link UI, and token create/revoke. Assert no auth secret reaches `localStorage` or `sessionStorage`.
- [x] Run tests to identify missing accessible form states and API/UI contracts.
- [x] Implement responsive accessible screens with keyboard navigation and clear factor setup/removal states; build static assets into the Go embed package.
- [x] Add CI Node setup, locked dependency install, typecheck, unit/browser tests, and frontend build before Go packaging.
- [x] Run frontend checks, Go suite, Linux/Windows builds, local end-to-end auth flow, and commit for review. (Independent Task 6 review still pending; see progress ledger.)

### Task 7: Security documentation and whole-milestone acceptance

**Files:** Update `README.md`, `docs/design.md`, `docs/implementation-plan.md`, add `docs/authentication.md`, update OpenAPI and CI evidence.

- [x] Document CLI admin bootstrap, HTTPS/public URL configuration, Google/Authentik provider setup, account/factor recovery, API tokens/scopes, session controls, and security limits. (`docs/authentication.md`, README, design and implementation-plan updates.)
- [x] Test a fresh database and an upgraded milestone-2 database; prove existing Floccus credentials and sync remain operational. (Go regression over the Floccus wire with the preexisting `bms1` credential; real `b0eab92` binary upgraded to the current binary; fresh-database CLI bootstrap.)
- [ ] Run all CI workflows, Go/Node suites, browser workflows, Linux/Windows builds, and `govulncheck`; record exact results and untested platform cases. Local CI-equivalents all pass after the go-jose v4.1.4 security bump, as do the UI and factor smokes and the Floccus 4-browser matrix (72/72). Still open: the race detector (NOT_RUN: no local C compiler; CI runs it) and GitHub-hosted CI (NOT_RUN: branch not pushed). See `task-7-report.md`.
- [ ] Ask a fresh reviewer to inspect the full milestone diff for auth bypasses, cross-user access, migration safety, and mismatch between OpenAPI and handlers. Fix all Critical/Important findings and rerun affected checks. Implementer self-review is done: it fixed the OpenAPI recent-auth drift and the govulncheck finding. A fresh independent review is still open; this task was told not to start extra agents.
- [ ] Commit only after the review and acceptance checks are clean; do not merge or deploy. Task 7 is committed for controller review, but not merged, pushed or deployed. Milestone acceptance waits for the fresh review and CI.
