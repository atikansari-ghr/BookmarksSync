# Browser harness (stock floccus)

Dev-only harness that loads the **unmodified** official floccus extension into
isolated, throw-away browser profiles and drives it through real extension APIs.
It never uses a real user profile, signs in to anything, or edits application code.

## Prerequisites

- Node >= 20.
- Official floccus v5.13.0 Chrome build extracted at `.tools/floccus-5.13.0-chrome`
  (override with `BH_EXTENSION_DIR`). Official zip SHA256:
  `88ea0f8af37eafd585fccee07a2ff4ab55dcfc83039c80ba7dcc306425f97f91`.
- Playwright's full Chromium in `.tools/pw-browsers` (`PLAYWRIGHT_BROWSERS_PATH` defaults to it).
  If missing, from `tests/browser`:
  - PowerShell: `$env:PLAYWRIGHT_BROWSERS_PATH = "$PWD\..\..\.tools\pw-browsers"; npx playwright install chromium`
  - POSIX shell: `PLAYWRIGHT_BROWSERS_PATH=../../.tools/pw-browsers npx playwright install chromium`
- Branded browsers are used from their standard Windows install paths
  (override with `BH_BRAVE_EXE`, `BH_CHROME_EXE`, `BH_EDGE_EXE`).

## Commands (run from `tests/browser`)

```
npm ci                         # installs the pinned playwright 1.65.1
npm run smoke:chromium         # baseline: Playwright full Chromium, headless
npm run smoke:brave
npm run smoke:edge
npm run smoke:chrome           # branded Chrome, loaded over CDP (see "Native Chrome")
npm run server-sync            # real server + real browsers (see "Real server sync"); exit 0 PASS, 1 FAIL, 2 NOT_RUN, 3 BLOCKED
npm run factors                # real server + Chromium virtual authenticator: passkeys, TOTP (see "Authentication factors")
npm test                       # unit tests: profile cleanup + server-sync helpers (no browser needed)
```

Environment overrides use `$env:NAME = "value"` in PowerShell (e.g.
`$env:BH_EXTENSION_DIR = "D:\path\to\extension"; npm run smoke:chromium`) and
`NAME=value npm run ...` in POSIX shells.

Cleanup is strict: the profile is deleted only after the browser is confirmed closed.
If closing or deleting fails, the run exits 1 with `CLEANUP FAILED: profile retained at <path>`
(the profile may hold credentials; remove it by hand once the browser is gone).

`node smoke.mjs --browser=<name> [--headed] [--no-rpc]`. Exit codes: 0 pass,
1 fail, 2 browser not installed (NOT_RUN), 3 extension could not be loaded (BLOCKED).
Playwright is resolved from `tests/browser/node_modules`, then `BH_PLAYWRIGHT_DIR`,
then `.tools/browser-harness/node_modules`.

## What the smoke checks

1. Launch with a unique temporary `--user-data-dir` under `.tools/browser-profiles/`
   and load the stock extension (`--load-extension` for Chromium/Brave/Edge, CDP
   `Extensions.loadUnpacked` for branded Chrome; event-based service worker discovery).
2. Background service worker reports manifest version 5.13.0.
3. Real `chrome.bookmarks`: list roots, create/read/move/update/search/remove in a sandbox folder.
4. RPC plumbing: import a floccus account into extension storage and trigger a sync
   through the genuine background message RPC. The target is a local recorder that
   answers HTTP 500, so this proves only that real floccus code ran and made its first
   request (`GET /ocs/v2.php/cloud/capabilities`). It is **not** a protocol test.
5. Close the browser and delete only the harness-created profile (path must be inside
   `.tools/browser-profiles/` and contain the harness marker file).

Results (JSON, with versions and profile path) go to `.tools/browser-results/` (ignored).

## Helpers for later server tests (`lib/floccus-rpc.mjs`)

- `launchWithExtension({browser})` returns an isolated session.
- `importAccount(page, accountData)` writes the same `accounts` storage entry as
  `BrowserAccountStorage.setAccountData`. `accountData` has the `floccus.export.json` shape.
- `syncAccount(page, id, {strategy, force, timeoutMs})` posts the same service-worker
  message as `Controller.syncAccount` and waits for `syncAccountResponse` (bounded, no polling).
- `getAccountStatus` returns stored account data with password/passphrase removed.

`server-sync.mjs` generates its own disposable credentials; never put a real credential in a file or log.

## Browser support

| Browser | Status |
|---|---|
| Playwright full Chromium | supported (baseline) |
| Brave | supported via `--load-extension` |
| Edge | supported via `--load-extension` (as tested) |
| Chrome (branded) | supported via native launch + CDP `Extensions.loadUnpacked` (verified on Chrome 157.0.8103.51); see below |
| Firefox | not covered. Playwright's Firefox cannot load floccus (needs a WebExtension driver such as web-ext/Marionette); a pass there would not prove floccus behavior |

## Native Chrome (branded Chrome v157+)

Branded Chrome ignores `--load-extension`, and Playwright does not surface the
extension worker after `Extensions.loadUnpacked`. For `--browser=chrome` the harness
therefore does not use Playwright to launch:

- It spawns the installed `chrome.exe` itself with the disposable profile, `--remote-debugging-port=0`
  and `--enable-unsafe-extension-debugging`, reads `DevToolsActivePort`, and opens a raw CDP
  WebSocket. Those flags exist only on this throw-away profile; no real profile or
  global browser setting is touched, and the extension is not patched.
- `Extensions.loadUnpacked` loads the unmodified floccus directory. The harness waits for the
  real `chrome-extension://<id>/dist/js/background-script.js` `service_worker` target, attaches
  to it (`Target.attachToTarget`, flatten) and waits for `chrome.runtime`/`chrome.bookmarks`
  to be bound before use.
- `session.serviceWorker` is a CDP evaluator (`kind: 'cdp-service-worker-target'`) running
  `Runtime.evaluate` in that actual worker. It is not a Playwright Worker. The smoke report
  records `serviceWorkerKind` and `loadMethod`. Extension pages (options UI used for the
  sync RPC) are opened through Playwright `connectOverCDP` to the same browser, exposed as
  `session.pageContext`.
- Lifecycle is owned: close sends `Browser.close` and awaits process exit. A process that does not
  exit is killed and the run fails with the profile retained. The profile is deleted only after
  the process has exited, with the same containment and marker checks as the other browsers.
- Chrome is never replaced by Chrome for Testing or Chromium; if it is missing the run is NOT_RUN.

### Credentials

The floccus app token (`BOOKMARKSYNC_APP_TOKEN`) is passed in by the test process and stored by
the extension in `chrome.storage.local` inside the disposable profile only. The harness never
writes it to result JSON or logs, and the profile (with the token) is deleted on clean shutdown. If
cleanup fails the profile is retained and may still contain it; delete it by hand.

## Real server sync (`npm run server-sync`)

`node server-sync.mjs [--browser=chrome,edge,brave,firefox] [--headed] [--trace] [--bump-ids=N]`

Builds the real server (`go build`, needs `.tools/go` or `BH_GO_EXE`), starts it on a random loopback port
with a fresh data directory under `.tools/server-sync/run-*` (marked, deleted only after the checks), creates
disposable users and app credentials with the real CLI (passwords only on child stdin, app secrets only in this
process' memory, never written; results and logs are sanitised), launches **stock floccus 5.13.0** in disposable
profiles and drives the genuine `syncAccount` RPC and the native bookmarks API. Per browser it checks the
server request log, the server's API view and the real native trees: upload from a native sandbox, download into
a second isolated profile, both canonical roots, order/move/rename/delete, capabilities, lock and 423 contention,
a read-only credential, cross-scope same-URL isolation, and latest title/tag retention when floccus deletes one of two same-folder duplicates. A missing prerequisite
(browser, go, extension) is `NOT_RUN` (exit 2), never a pass; a sync case only passes with real server requests
and a check of the actual native tree.

`--trace` routes floccus through a harness-owned recording proxy and records native bookmark events plus
floccus's own debug log. `--bump-ids=N` is an experiment that pushes the server's ids far above the browser's.
`node order-trace.mjs` is the standalone version of the ordering diagnosis.

Resolved ordering issue: floccus 5.13.0 could reorder a downloaded folder when server and local folder ids
collided. The server now uses a negative wire namespace for concrete folder ids, and two repeated full
browser matrices passed. Historical failures and trace evidence remain in `.superpowers/sdd/bookmarksync/server-sync-report.md`.

## Authentication factors (`factors-smoke.mjs`)

`npm run factors` builds the server (CGO-free) into a marked run directory under
`.tools/server-sync`, creates a throw-away account through the CLI (password via stdin),
serves it on `http://localhost:<random port>` with `--public-url` set to that origin, and drives
Playwright's full Chromium in an ephemeral context. A CDP virtual authenticator
(`WebAuthn.addVirtualAuthenticator`: CTAP2, internal transport, resident keys, user
verification) answers the real `navigator.credentials.create()`/`get()` calls. It checks the
instance key is created, password login, passkey registration with a resident credential,
discoverable passkey sign-in, assertion replay refusal, TOTP enrollment/confirmation, the
pending second-factor response (no session), TOTP completion, and that nothing is left in
`localStorage`/`sessionStorage`/`document.cookie`. There is no UI yet (Task 6), so the API is
called with `fetch` from a same-origin page. Exit codes: 0 PASS, 1 FAIL, 2 NOT_RUN, 3 BLOCKED.
The run directory (database, instance key, binary) is removed afterwards. Firefox/WebKit and
real platform authenticators are not covered.
