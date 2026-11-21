# Identity and API Milestone Design

**Status:** Approved 2026-11-17  
**Parent specification:** [Approved product design](../design.md)  
**Scope:** Implementation-plan milestone 3, with a minimal login and account-security interface.

## Goal

Let administrator-created users securely sign in, manage their own authentication methods and sessions, and use the same permissions through a documented `/api/v1` interface. Keep Floccus device credentials separate from website sessions and personal API tokens.

## Design

- Extend the existing Go `internal/identity` service and SQLite migrations. Preserve existing user IDs, Argon2id password hashes, Floccus credentials, share scopes, and bookmark data. Add server-side web sessions, WebAuthn credentials, encrypted TOTP seeds, single-use recovery-code hashes, linked OIDC identities, and personal API-token records. Store only hashes for session secrets, recovery codes, and API tokens. Encrypt TOTP seeds with an instance key kept in a private file in the data directory; include that key in full-system backups.
- Use opaque server-side sessions, rotated at authentication and revocable per device. Sessions expire after 12 hours idle or 30 days absolute. Cookies are `HttpOnly`, `Secure`, host-only, and `SameSite=Lax`; state-changing cookie-authenticated requests require CSRF protection and same-origin validation. Revoke sessions on logout, password/authenticator changes, account suspension, or admin reset. Do not put secrets in browser storage or logs.
- Support password and passkey sign-in; allow optional TOTP and one-time recovery codes. Admins create accounts; no public registration. Recovery codes are shown once, stored hashed, and consumed once. Admin account recovery requires an explicit password/authenticator reset and revokes active sessions and API tokens.
- Link Google and Authentik OIDC identities only from an authenticated account after reauthentication within five minutes. Do not create accounts automatically or match/link by email. Identify links by issuer plus subject. Use authorization-code flow with PKCE, state and nonce validation, exact registered callbacks, and verified ID tokens. Provider credentials and Authentik issuer settings are configured by the operator; provider secrets never appear in user pages or logs.
- Add `/api/v1` JSON routes for the signed-in user's profile, settings, bookmark/folder CRUD, search/tags/favorites, shares, Trash, and Floccus connection management; role-protected admin operations cover accounts, connections, and system settings. Favorites are per-user bookmark metadata that follows every placement; users may favorite bookmarks they can view in a shared folder, including view-only shares, without changing the owner's data or gaining write access. Favorites are stored in an additive `bookmark_favorites(user_id, bookmark_id)` relation (migration 0004), exposed as a `favorite` boolean on bookmark responses, filtered with `GET /api/v1/bookmarks?favorite=true|false`, and toggled with idempotent bodyless `PUT`/`DELETE /api/v1/bookmarks/{id}/favorite` (session+CSRF or a `bookmarks:write` token). The Floccus wire format and hashes are unchanged. Personal API tokens have `bookmarks:read`, `bookmarks:write`, `shares:read`, or `shares:manage` scopes and never authorize admin operations. Every handler derives the user and role from validated authentication, then scopes data in the service layer. Admin APIs expose aggregate counts/settings but never another user's private bookmarks. Keep OpenAPI as a versioned checked-in artifact that matches route behavior.
- Add a small React/TypeScript auth surface matching the split-screen login reference in `mocscreenshots/`: username/password, passkey login, optional second factor, passkey enrollment/removal, TOTP setup, recovery-code display, linked-provider management, active-session listing/revocation, and API-token creation/revocation. Show OIDC buttons only for configured providers. Use username (not email) for local sign-in, display the current version and Atik Ansari copyright, and replace an unavailable password-reset link with instructions to contact the administrator. Dashboard widgets, themes, branding controls, organizer, and full admin dashboard remain in milestone 4.

## Security and acceptance

Authentication endpoints use generic login errors, bounded per-account and per-peer rate limits, bounded request bodies, and structured audit events that exclude secrets. Tests cover login failures, inactive users, session rotation/revocation/expiry, CSRF and origin failures, WebAuthn challenge reuse and origin/RP mismatch, TOTP replay and recovery-code reuse, OIDC state/nonce/issuer/audience/subject validation, link conflicts, token scopes, account isolation, admin boundaries, and migration from the current database.

Use the Go WebAuthn implementation and OIDC/OAuth libraries only after pinning reviewed releases compatible with the repository's Go toolchain. Follow the [WebAuthn Go library](https://github.com/go-webauthn/webauthn), [go-oidc](https://github.com/coreos/go-oidc), and [OWASP Session Management guidance](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html). No external login or session is considered valid without server-side verification.

## Out of scope

Email-based password reset, self-registration, social-account auto-provisioning, dashboard/theme work, backup configuration, and deployment automation remain outside this milestone. Lost-factor recovery is through single-use recovery codes or an administrator reset.
