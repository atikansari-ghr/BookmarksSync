# Connect a bookmarks app

BookmarksSync implements Nextcloud Login Flow v2 for compatible clients such as
floccus. It uses the normal website sign-in and a separate approval screen.
Your website password is never issued to the client.

1. Start the client's Nextcloud-style connection flow with your BookmarksSync
   public HTTPS origin.
2. Open the returned sign-in link, sign in to BookmarksSync, and confirm the
   account shown on **Connect your bookmarks app**.
3. Type a **Connection name**. Use the same name as your Floccus profile, for
   example `BookmarksSync-Home-Firefox`. The field starts empty and is required.
   Leading and trailing spaces are removed, and names can be up to 200 characters.
4. Select **Approve connection** only if you started this request yourself. The
   client receives a separate credential that can read and change your private
   bookmarks and folders. Closing the page or selecting Cancel grants no access.
5. Find the connection under that name in **Account security → Floccus
   connections**. Rename it or revoke it there when the device no longer needs
   access.

### Why you type the name yourself

Floccus does not send its profile label to the server. Its New Profile wizard
stores the label in the browser extension and starts Login Flow with only the
server URL; the source of floccus 5.14 (`NewAccount.runLoginFlow`) shows this.
BookmarksSync therefore cannot read, guess or synchronise the label, so the
consent page asks you to type it again. If you later rename the profile in
Floccus, rename the connection in BookmarksSync too, because nothing updates it
automatically.

### Renaming a connection

In **Account security → Floccus connections**, each connection has a **Rename**
button. Enter the **New connection name** and select **Save**, or select
**Cancel** to keep the current name. Renaming changes only the label shown in
BookmarksSync. The app password, access mode, shared-folder target and sync state
stay the same, so Floccus keeps syncing without changes. A rename does not ask for
your password again. If it fails, for example because the connection was revoked
in another tab, the dialog shows the error and keeps what you typed.
Older connections approved without a name keep the default label **Login Flow v2
connection** until you rename them.

Configure `BOOKMARKSYNC_PUBLIC_URL` to the HTTPS origin the browser and client
both reach. The normal country gate, trusted-proxy rules, password/passkey/TOTP
and provider sign-in requirements still apply. Provider sign-in can return to
the connection screen through a short-lived HttpOnly cookie. Real provider
and public reverse-proxy acceptance remain operator checks.

Approval requires a live session and recent authentication. It binds to the
account/session displayed in the consent page; switching sessions requires a
reload before approval. Requests expire after 20 minutes and can be redeemed
only once. Revocation or account suspension after consent prevents credential
issuance. The granted credential is a private edit connection; use the existing
security-page/API connection controls for view-only or shared-folder credentials.

## Protocol contract

- `POST /index.php/login/v2` returns independent browser and poll handles, a
  browser login URL, and the poll endpoint.
- `POST /login/v2/poll`, with a form-encoded `token`, returns HTTP 404 while a
  request is pending, expired or already consumed. After consent it returns
  `server`, `loginName`, and `appPassword` once.
- Browser consent uses the session/CSRF-protected `/api/v1/auth/connect`
  endpoints. The server stores only hashes of handles and credentials; no
  application password exists before successful one-time redemption.
- `POST /api/v1/auth/connect/approve` accepts `{flow, sessionId, label}`. The
  website always sends a trimmed, non-empty `label`; the server stores it with the
  pending request and uses it when polling mints the credential. Older callers that
  omit `label` still get the default label. `GET /api/v1/auth/connect` is unchanged.
- `PATCH /api/v1/me/connections/{id}` with `{label}` renames an active connection
  owned by the signed-in user and returns `{connection}` metadata. It does not
  reissue the secret or change access. Other users' or revoked connections return
  404.
- The Nextcloud Login Flow v2 protocol has no field for a client profile name.
  Stock Floccus sends only the server URL, so the name can come only from what the
  user types on the consent page.

Clients must treat every returned handle and app password as a secret. The
protocol responses use `Cache-Control: no-store`. User and administrator
connection-revocation controls continue to work with these credentials.

## Verification and remaining acceptance

Backend contract/security tests cover explicit approval, recent authentication,
CSRF/origin checks, expiry, session/account revocation, one-time concurrent
redemption, and usable/revocable private credentials. Component tests exercise
the consent screen and sign-in continuation.

Connection naming (2026-11-26): component tests cover the empty, required,
trimmed consent name; refusing a blank name; keeping the typed name after a failed
approval; and Rename with Save, Cancel and visible errors. All three smoke modes
now type a consent name before approval and check that `GET
/api/v1/me/connections` reports that label on the connection whose ID is in the
issued `bms1.` app password. In `--wizard` the name is the exact local profile
label `Wizard acceptance`, typed into both the stock wizard and the consent
page. Results of these runs are reported separately in the dated verification
notes. The [2026-11-26 verification](verification-2026-11-26.md) records passing
named approval, Settings rename with the original credential, stock login action
and the complete stock wizard with native bookmark upload.

The disposable real-Chromium smoke passed website password sign-in, explicit
consent, one-time polling and authentication to the actual Floccus folder API
with the issued credential. It also checked desktop/mobile layout and that
secrets were inaccessible through browser storage/script-readable cookies.
Run it with `cd tests/browser && npm run connect`; CI runs it after the existing
sign-in smoke with the same installed Chromium.

The optional `npm run connect -- --stock` check also passed with unmodified
floccus 5.13.0 in Chrome: the extension's own login action opened its consent tab,
polled the server and received a credential that authenticated to the actual
Floccus folder API. This requires the browser/extension assets described in the
browser harness README. It exercises the real extension login code, rather than
replacing its network implementation.

The optional `npm run connect -- --wizard` check uses the same unmodified
floccus 5.13.0 in a disposable headless Chrome profile. Independent verification
on 2026-11-25 exposed a step-transition race: two Continue buttons were briefly
visible. The corrected harness waits for a settled step and scopes controls to
that step. Two consecutive full functional reruns passed. Wizard screenshots are
optional (`--screenshots`), because capture can time out on the stock sync-options
screen even when the mandatory form interactions complete. It drives
only the extension's visible UI: Overview **Add Profile** (opens the wizard in a
new tab), the **Nextcloud Bookmarks** account type, **Profile label**, **Nextcloud
URL** → **Connect** → **Sign in with Nextcloud**, website password sign-in,
**Connection name** (the same profile label, typed again) and **Approve
connection** in the tab the extension opened, then **Bookmarks folder**
selection, the default sync-behaviour step and **Profile created**. No profile is
imported and no extension asset or sync code is replaced. The check then requires
exactly one saved `nextcloud-bookmarks` profile with the issued `bms1.` app
credential (never the website password), authenticates that credential to the
actual folder API, runs the extension's own background sync and confirms the
native test bookmark and its folder reached the server.

In the folder chooser select the browser's absolute root (the untitled top node,
shown as `/`). BookmarksSync keeps only the canonical bookmarks-bar and
other-bookmarks roots at the server top level, so a bookmark placed directly in a
chosen sub-folder is mapped to the server root and rejected (HTTP 400, floccus
error E035). This matches the root guidance in
[Floccus compatibility](floccus-compatibility.md).

The wizard check covers Chrome only; the connection flow in Edge, Brave and
Firefox was not driven. Existing four-browser stock sync matrices use profile
import, which remains available. Live provider sign-in and public HTTPS still
require operator configuration and acceptance.
