# Authentication and account security

This guide is for operators and users of BookmarksSync's browser sign-in, account security
pages, and `/api/v1` API. It describes what the current build does. Untested setups are
listed at the end. The approved design is [docs/design/identity-api.md](design/identity-api.md).
The API contract is [api/openapi.yaml](../api/openapi.yaml).

BookmarksSync has three independent kinds of credential:

| Credential | Used by | Created by | Stored as |
|---|---|---|---|
| Web session (`__Host-bookmarksync_session` cookie) | the sign-in and security pages, and the API from a browser | password, passkey or linked-provider sign-in (plus TOTP when enabled) | SHA-256 hash |
| Personal API token (`bmt1_…`) | scripts and automation, via `Authorization: Bearer` | the signed-in user | SHA-256 hash |
| Floccus connection / app credential (`bms1.<id>.<secret>`) | the floccus browser extension, via HTTP Basic | the CLI, or the user in the security page or API | SHA-256 hash |

Revoking one kind never revokes the others, except where stated below. Secrets are shown once,
when they are created, and cannot be shown again.

## 1. Enable browser sign-in

Browser sign-in, the `/api/v1` API and the pages under `/auth/` are off until you set a public
URL. Without one, the server still serves the health endpoints and the floccus API, and logs
`browser sign-in disabled`.

```sh
bookmarksync serve --listen 127.0.0.1:8080 --data-dir /var/lib/bookmarksync \
  --public-url https://bookmarks.example.com
# or: BOOKMARKSYNC_PUBLIC_URL=https://bookmarks.example.com
```

Rules for the public URL:

- **Exact origin only.** Give a scheme, host and optional port, with no path, query or fragment.
  It is normalized: lower case, and the default port is dropped. Serving under a path prefix
  (`https://example.com/bookmarks`) is not supported.
- **HTTPS is required.** Plain `http://` is accepted only for loopback development
  (`localhost`, `127.0.0.0/8`, `::1`). An invalid URL stops startup.
- **It is the only trusted origin.** Every unsafe request (POST, PATCH, PUT, DELETE), including
  login, must carry exactly one `Origin` header equal to this value. Passkeys and OpenID Connect
  redirects are built from it. The server never reads `Host`, `X-Forwarded-*` or `Forwarded`.
- **Changing it has consequences.** Changing the host invalidates every registered passkey (see
  §5) and requires re-registering provider redirect URIs (see §6).

### Reverse proxy requirements

The server speaks plain HTTP. Put a TLS-terminating reverse proxy (for example Nginx Proxy
Manager, Pangolin or Cloudflare Tunnel) in front of it, and:

1. Serve the site at the root of the public URL's host. Forward every path unchanged:
   `/api/v1/…`, `/auth/…`, `/`, the floccus paths `/index.php/apps/bookmarks/…` and
   `/ocs/v2.php/…`, and the health endpoints.
2. Pass the browser's `Origin` and `Cookie` headers through unchanged. Don't rewrite
   `Set-Cookie`. The session cookie uses the `__Host-` prefix, so it must stay `Secure` with
   `Path=/` and no `Domain`.
3. Keep `--listen` on loopback or a private interface that only the proxy can reach.
4. Don't cache responses. Auth and API responses already send `Cache-Control: no-store`.

**Known limitation: there is no trusted-proxy support yet** (planned for milestone 5). Rate
limits are keyed by the direct TCP peer, so behind a proxy all clients share the proxy's address.
The per-peer limits in §10 then apply to the whole site, while per-account limits still apply.
This can reduce availability during an attack, but forged forwarding headers can't bypass a
limit.

### Instance key (back it up)

When a public URL is set, the server creates `instance.key` in the data directory: 32 random
bytes, readable only by the service account. It encrypts TOTP seeds with AES-256-GCM.

- On Unix the file is mode `0600`. On Windows it has a protected DACL that grants access only to
  the creating user.
- Startup fails if the key is malformed, is a symlink, or is readable by others. The server never
  repairs or replaces an existing key.
- **Include `instance.key` in every full backup, together with the database.** If it is lost, the
  next start creates a new key, and existing TOTP seeds can no longer be decrypted. Affected users
  must then sign in with a recovery code, or an administrator resets their TOTP (§8).

## 2. Bootstrap the first administrator

There is no public registration. Create the first administrator on the server host with the CLI.
The password is read from standard input only, never from arguments, and must be 12–1024 bytes.

```sh
printf '%s\n' 'a long admin password' | \
  bookmarksync user create --username admin --password-stdin --admin --data-dir /var/lib/bookmarksync
bookmarksync user list --data-dir /var/lib/bookmarksync
```

Then open the public URL. `/` redirects to `/auth/login`, where you sign in with the username,
not an email address. Other accounts are created either by an administrator through the API
(`POST /api/v1/admin/users`, which needs a recent reauthentication) or with the same CLI command
without `--admin`. Administrators can also manage accounts and appearance in the **Administration**
workspace. See the [workspace and administration guide](workspace-guide.md).

Run CLI administration against the data directory of the running service, as the service account.

## 3. Sign-in methods

- **Password.** Usernames are case-insensitive. Unknown users, disabled users and wrong
  passwords all get the same `401 invalid_credentials` response after equivalent Argon2id work.
- **Passkey** (§5). A user-verified passkey sign-in counts as multi-factor and does not ask for
  TOTP.
- **Linked Google or Authentik identity** (§6). Accounts with TOTP enabled must still complete
  TOTP afterwards.
- **TOTP second step.** For an account with TOTP enabled, password or provider sign-in returns
  `202 second_factor_required` and sets no cookie. The pending step lasts 5 minutes and allows
  5 wrong attempts. Either a 6-digit code or one recovery code completes it.

There is no "forgot password" flow and no email. The login page tells users to ask their
administrator.

## 4. Sessions and CSRF

- Each session is opaque, random and stored only as a hash. It expires after **12 hours idle** or
  **30 days** from sign-in, whichever comes first.
- The cookie is `__Host-bookmarksync_session`: `Secure; HttpOnly; SameSite=Lax; Path=/`.
- Every sign-in creates a new session. Any session already presented by that browser is revoked,
  so a planted session can't be reused.
- **CSRF protection.** Every unsafe cookie-authenticated request needs:
  - an exact `Origin` header;
  - an `X-CSRF-Token` header.

  The login response and `GET /api/v1/auth/csrf` return the token. It is bound to the session and
  changes whenever the session rotates.
- **Recent reauthentication.** Sensitive actions require a password confirmation within the last
  5 minutes (`POST /api/v1/auth/reauthenticate`). Otherwise they return
  `403 reauthentication_required`. Reauthentication is password-only.

  These actions require it:
  - changing the password;
  - creating API tokens or Floccus connections;
  - TOTP enrollment, confirmation and removal, and new recovery codes;
  - adding or removing a passkey;
  - linking or unlinking a provider;
  - emptying the Trash or purging items;
  - every admin action that changes an account.

  OpenAPI marks these operations with `x-recent-auth: true`.
- **Managing sessions.** Users see their active sessions on the security page
  (`GET /api/v1/auth/sessions`). They can revoke any one of them
  (`DELETE /api/v1/auth/sessions/{id}`), or sign out (`POST /api/v1/auth/logout`).

Sessions are revoked automatically in these cases:

| Event | Effect |
|---|---|
| Logout, or revoking a session | That session |
| Changing the password | All other sessions plus all API tokens; the acting session gets a new secret |
| Adding or removing a passkey, any TOTP change, new recovery codes, linking or unlinking a provider | All other sessions; the acting session gets a new secret |
| Admin password reset or factor reset | All sessions and API tokens of that user |
| Suspending the account (`PATCH /api/v1/admin/users/{id}` with `active: false`, or `user set-active --active=false`) | All sessions, API tokens and Floccus connections, permanently. Reactivation doesn't restore them. |

## 5. Passkeys

- The relying-party ID is the **host of the public URL**, and the expected origin is the public URL
  itself. A passkey works only on exactly that host.
  - If the public URL's host is an IP address, passkeys are disabled and a warning is logged.
    Password and TOTP still work.
  - `http://localhost` works for development.
- Passkeys need a secure context, so the browser must reach the site over HTTPS, or `localhost`
  in development.
- New passkeys are discoverable credentials with user verification required, and no attestation
  is requested. An account can have at most 20.
- Users add, rename and remove passkeys on the security page. Adding or removing one needs a recent
  password confirmation. A passkey whose signature counter goes backwards is refused.
- **Changing the public host invalidates every passkey.** Users must then sign in another way and
  register new ones. Alternatively, an administrator resets their passkeys.

## 6. Google and Authentik (OpenID Connect)

Providers are configured by the operator, with **environment variables only**, so client secrets
never appear in process listings. They also need the public URL.

- A provider is enabled only when all of its variables are set.
- A partial set, a blank value, or OIDC without a public URL stops startup.
- Startup errors name the variable, never its value.

| Provider | Variables | Exact redirect URI to register |
|---|---|---|
| Google | `BOOKMARKSYNC_OIDC_GOOGLE_CLIENT_ID`, `BOOKMARKSYNC_OIDC_GOOGLE_CLIENT_SECRET` (issuer fixed to `https://accounts.google.com`) | `<public URL>/api/v1/auth/oidc/google/callback` |
| Authentik | `BOOKMARKSYNC_OIDC_AUTHENTIK_ISSUER`, `BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_ID`, `BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_SECRET` | `<public URL>/api/v1/auth/oidc/authentik/callback` |

For example, with `https://bookmarks.example.com` the Google redirect URI is
`https://bookmarks.example.com/api/v1/auth/oidc/google/callback`. At startup the server logs each
enabled provider's exact callback URL. It does not contact the provider until the first sign-in.

**Google:** create an OAuth client of type *Web application*. Add the redirect URI above exactly
(scheme, host, port and path) as an authorized redirect URI.

**Authentik:** create an OAuth2/OpenID provider and an application that uses it.

1. Set the client type to *Confidential* and register the redirect URI above as a strict match.
2. Choose an asymmetric signing key, so tokens are signed with RS256 or ES256.
3. Allow the `openid`, `profile` and `email` scopes.
4. Set `BOOKMARKSYNC_OIDC_AUTHENTIK_ISSUER` to the provider's issuer exactly as Authentik shows it,
   typically `https://authentik.example.com/application/o/<application-slug>/`, **including the
   trailing slash**. The value is used unchanged and must match the issuer in Authentik's discovery
   document. It must use HTTPS, except on loopback.

**How sign-in works:**

- The flow is an authorization-code flow with PKCE (S256), state and nonce, bound to the browser
  that started it by a short-lived `__Host-bookmarksync_oidc` cookie.
- The ID token is checked for signature, exact issuer, audience, `azp`, expiry and nonce.
- Flows expire after 10 minutes and can be used once.

**Account rules:**

- Accounts are **never** created from a provider sign-in, and are never matched by email.
- A user links an identity from the security page while signed in, after confirming their password
  within the last 5 minutes. Each account can link one identity per provider.
- A link is keyed by the verified issuer and subject. The same provider identity can't be linked
  to two accounts.
- Signing in with an identity that isn't linked, or whose account is disabled, returns `not_linked`.
- Unlinking requires a recent password confirmation.

Provider discovery results are cached for the life of the process, so restart after changing
provider settings. Signing-key rotation is handled automatically.

## 7. TOTP and recovery codes

- **Turning TOTP on.** Enable it on the security page (needs a recent password confirmation). Add
  the shown setup key or `otpauth://` link to an authenticator app; no QR code is shown. Then
  confirm with a current code.
- **TOTP parameters.** RFC 6238 with SHA-1, 6 digits, a 30-second period and ±1 step of clock skew.
  Each code is accepted once.
- **Recovery codes.** Confirming TOTP shows **10 recovery codes once**. Each code works once, only
  its hash is stored, and case, spaces and hyphens are ignored. Generating new codes replaces all
  old ones and needs a current TOTP code.
- **Turning TOTP off.** This needs a recent password confirmation plus a TOTP code or a recovery
  code.

## 8. Recovering an account or a lost factor

| Situation | What to do |
|---|---|
| Lost the authenticator device | At the second step, choose "Use a recovery code instead". Then turn TOTP off and set it up again, which also gives new recovery codes. |
| Lost the device and all recovery codes, or forgot the password | An administrator resets the account (below). |
| Lost a passkey | Sign in with the password, then remove the lost passkey. If needed, an administrator resets passkeys. |
| `instance.key` lost or replaced | TOTP codes fail for every user. Users sign in with recovery codes and set TOTP up again, or an administrator resets TOTP. |
| No administrator can sign in | On the server host, create a new administrator with `bookmarksync user create --admin …` (§2), sign in as that administrator, and reset the original account. The CLI has no password-reset command. |

Administrator resets need an active admin web session with a password confirmation from the last
5 minutes. Personal API tokens can never call admin routes.

- `POST /api/v1/admin/users/{id}/password-reset` with `{"newPassword": "…"}`. This sets a new
  password and revokes all of the user's sessions, API tokens and pending second-factor steps. If
  the user has TOTP enabled, it stays enabled.
- `POST /api/v1/admin/users/{id}/factor-reset` with any of `{"totp": true, "passkeys": true,
  "oidc": true}`. This removes the chosen factors (TOTP also removes its recovery codes) and
  revokes all of the user's sessions and API tokens.
- Floccus connections survive both resets. Revoke them separately if the device may be
  compromised (§9).
- `PATCH /api/v1/admin/users/{id}` with `{"active": false}` suspends an account, and
  `{"isAdmin": …}` changes the role. The last active administrator can't be demoted or suspended
  (`409 last_admin`).

## 9. Floccus connections

Each browser or device using the stock floccus extension gets its own app credential. Floccus
uses the account username as its username and the generated `bms1.…` secret as its password. Never
use the website password in floccus.

Create a connection in one of these ways:

- **Security page:** the "Floccus connections" section, which shows the server URL, username and
  app password once.
- **API:** `POST /api/v1/me/connections` with `{"label": "laptop", "mode": "edit"|"view",
  "shareId": 0}`. Use `shareId` to bind the connection to a folder shared with you. This needs a
  recent password confirmation.
- **CLI, on the host:** `bookmarksync credential create --username alice --mode edit --label laptop`.

Manage connections like this:

- **Users** list connections with `GET /api/v1/me/connections`, and revoke one with
  `DELETE /api/v1/me/connections/{id}`. Revoking takes effect on the next request and releases
  any sync lock the connection held.
- **Administrators** list a user's connections with `GET /api/v1/admin/users/{id}/connections`
  (no secrets) and revoke one with `DELETE …/connections/{cid}`.
- **Password changes and resets don't affect connections.** Only revoking the connection or
  suspending the account does.

Connections created by earlier releases keep working after an upgrade (see the README's upgrade
note). See [floccus-compatibility.md](floccus-compatibility.md) for the extension setup.

## 10. Personal API tokens and scopes

- **Creating a token.** A signed-in user creates tokens on the security page, or with
  `POST /api/v1/me/tokens` and `{"label": "backup script", "scopes": [...], "expiresAt": "2027-01-01T00:00:00Z"}`.
  This needs a recent password confirmation. `expiresAt` is optional; if you give it, it must be
  in the future. Without it, the token is valid until revoked.
- **Using a token.** The token (`bmt1_…`) is shown once. Send it as `Authorization: Bearer <token>`.
  Only one `Authorization` header is accepted; Basic auth and malformed headers get `401`.
- **Scopes match exactly.** `bookmarks:write` does not imply `bookmarks:read`, so grant both
  when a script needs both.

| Scope | Allows |
|---|---|
| `bookmarks:read` | list and read bookmarks and folders, tags, the Trash listing, export |
| `bookmarks:write` | create, update and delete bookmarks and folders, remove placements, reorder, restore from Trash, import |
| `shares:read` | list owned and incoming shares |
| `shares:manage` | grant, change, revoke or leave shares |

- Any valid token can read `GET /api/v1/me`.
- Tokens are **refused** (`403 token_not_allowed`) on routes that need a web session:
  - password and settings;
  - token and connection management;
  - emptying or purging the Trash;
  - every admin route.

  The `/api/v1/auth/*` routes ignore bearer tokens entirely; without a session cookie they answer
  `401`. So a token can't create other tokens or raise its own privileges.
- Users list and revoke tokens on the security page, or with `GET /api/v1/me/tokens` and
  `DELETE /api/v1/me/tokens/{id}`.
- Tokens are revoked when the password changes, on an admin password or factor reset, and when the
  account is suspended.

## 11. The `/api/v1` API

[api/openapi.yaml](../api/openapi.yaml) (OpenAPI 3.1) is the checked-in contract. Tests check it in
both directions against the registered routes: every route and method, the security scheme, the
token scope, and the admin and recent-reauthentication requirements.

- **Request bodies:** JSON only. Unknown fields are refused.
- **Errors:** `{"error": {"code": "...", "message": "..."}}`.
- **Pagination:** lists use `page` (zero-based) and `limit` (default 50, maximum 200), and return
  `hasMore`.
- **Shared folders:** add `?share=<id>` to bookmark and folder requests to work inside a folder
  shared with you. View-only shares refuse writes with `403 read_only`.
- **Floccus sync lock:** while floccus holds the lock on a collection, writes to it return
  `423 locked` with `Retry-After`.
- **Admin data:** admin routes show account metadata and aggregate bookmark counts and storage
  only. They never show another user's bookmarks.
- **Conditional routes:**
  - passkey routes exist only when the public host is a domain name;
  - `/api/v1/auth/oidc/…` routes exist only when at least one provider is configured;
  - all of `/api/v1/auth/*` and the rest of `/api/v1` (except `/api/v1/version`) need a public URL.

Example with a read-only token:

```sh
curl -H "Authorization: Bearer $TOKEN" "https://bookmarks.example.com/api/v1/bookmarks?limit=20&q=go"
```

## 12. Security limits

| Control | Current value |
|---|---|
| Failed password logins per username + direct peer | 10 per 15 minutes |
| Failed password logins per direct peer (all usernames) | 100 per 15 minutes |
| Failed second-factor attempts per account (all peers) | 10 per 15 minutes; then password login for that account also returns `429` |
| Wrong attempts on one pending second-factor step | 5 |
| Passkey sign-in starts per direct peer | 30 per 5 minutes; at most 10,000 pending passkey sign-ins |
| Provider sign-in starts per direct peer | 30 per 5 minutes; at most 10,000 pending flows per kind |
| Rate-limiter memory | at most 10,000 tracked keys per limiter, in memory, reset on restart |
| Body size | login 8 KiB; passkey ceremonies 64 KiB; JSON API 64 KiB; import 4 MiB; default 1 MiB |
| Session lifetime | 12 hours idle, 30 days absolute; recent reauthentication window 5 minutes |
| Server timeouts | read 30 s, write 60 s, idle 120 s; headers at most 64 KiB |
| Password length | 12–1024 bytes, hashed with Argon2id |

Rate limits never read forwarded headers. Distributed guessing from many addresses is limited by
the per-pair limits and Argon2id cost rather than by an account lockout. This prevents anyone from
remotely locking an account out.

Other protections:

- **Pages:** the sign-in pages send a strict Content-Security-Policy (no inline scripts or styles),
  `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer` and `Cache-Control: no-store`.
- **Browser storage:** the pages never put an auth secret in browser storage.
- **Logs:** audit events go to the server log (standard error) with user, session and peer IDs.
  They never include passwords, secrets, tokens, codes or provider subjects.
- **Privacy:** the operator, and anyone with the data directory or backups, can read stored
  bookmarks. BookmarksSync does not offer end-to-end encryption.

## 13. What has and hasn't been tested

**Tested:**

- Go unit and integration tests. The CI workflow runs them on Ubuntu and Windows.
- A real Chromium browser (Playwright) with a virtual authenticator, covering:
  - password and TOTP sign-in;
  - passkey registration and sign-in;
  - recovery codes;
  - session and token revocation;
  - linking and sign-in with a local fake OpenID provider.
- Floccus sync in Chrome, Edge, Brave and Firefox, verified in milestone 2.
- Upgrading a milestone-2 database, verified with the real milestone-2 binary.

**Not tested:**

- **Real providers:** no real Google or Authentik sign-in. The provider setup steps above follow
  the providers' documented OpenID Connect behavior, but haven't been run against a live tenant.
- **Other browsers and authenticators:** passkeys in Firefox and Safari/WebKit, and with real
  platform, roaming or synced authenticators.
- **Reverse proxies:** HTTPS through a real reverse proxy (NPM, Pangolin, Cloudflare Tunnel).
- **Platforms:**
  - macOS (no builds or tests);
  - the Unix instance-key permission tests run only where Go tests run on Linux (the CI Ubuntu
    job);
  - Linux/arm64 and other architectures.
