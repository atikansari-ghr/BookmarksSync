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
npm run smoke:chrome           # expected BLOCKED, see below
npm run pending                # server capability placeholders, all PENDING
npm test                       # unit tests for profile cleanup failure handling
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
   and `--load-extension` of the stock extension (event-based service worker discovery).
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

The app credential comes from the environment (`BOOKMARKSYNC_APP_TOKEN`); never put it in a file or log.

## Browser support

| Browser | Status |
|---|---|
| Playwright full Chromium | supported (baseline) |
| Brave | supported via `--load-extension` |
| Edge | supported via `--load-extension` (as tested) |
| Chrome (branded) | blocked: `--load-extension` is ignored; no service worker appears |
| Firefox | not covered. Playwright's Firefox cannot load floccus (needs a WebExtension driver such as web-ext/Marionette); a pass there would not prove floccus behavior |
