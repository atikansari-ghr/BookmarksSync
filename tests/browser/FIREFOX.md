# Native Firefox harness

Drives the **installed Firefox** with the **official, unmodified floccus v5.13.0 Firefox release XPI** in a
disposable profile, using geckodriver (WebDriver/Marionette) through `selenium-webdriver`. Playwright's
Firefox build has no add-on support, so it is not used here. The Chrome-family harness (`README.md`,
`smoke.mjs`) is separate and untouched.

This proves **plumbing only**: the extension runs, native `browser.bookmarks` works, and the background
RPC reaches real floccus code (first request to a local recorder that returns HTTP 500). It does **not**
prove server protocol compatibility. Server sync and cross-vendor root assertions are not implemented.

## Pinned versions

| Item | Version | Source / SHA-256 |
|---|---|---|
| Firefox | 160.0 (system install, not modified) | `C:\Program Files\Mozilla Firefox\firefox.exe` or `BH_FIREFOX_EXE` |
| floccus XPI | v5.13.0, add-on id `floccus@handmadeideas.org` | https://github.com/floccusaddon/floccus/releases/download/v5.13.0/floccus-build-v5.13.0.xpi, `f9aa443d5f64612c7d1c17bce2df8a9441ec756665c8a64b89f62d7d69ad1630` |
| geckodriver | 0.37.1 win64 | https://github.com/mozilla/geckodriver/releases/download/v0.37.1/geckodriver-v0.37.1-win64.zip, zip `dfed9315abe8d2fbc1b6161a2ee8002452e79cf05ee92fdc653a4e26bc35edd8` (matches the GitHub release digest) |
| selenium-webdriver | 4.52.0 (exact, in `package-lock.json`) | npm |

The XPI digest is re-checked on every run. The geckodriver zip digest is checked at setup time (below).
Nothing is rebuilt or patched, and no signing or policy setting is changed: the signed XPI is installed
with WebDriver's temporary add-on install into the owned profile.

## Setup (once)

Paths: `.tools/` is git-ignored scratch under the repo root. Override with `BH_GECKODRIVER` (driver exe),
`BH_FIREFOX_XPI` (XPI file), `BH_FIREFOX_EXE` (Firefox exe).

PowerShell (from the repo root):

```powershell
Set-Location tests/browser; npm ci; Set-Location ../..
New-Item -ItemType Directory -Force .tools/geckodriver | Out-Null
curl.exe --fail --location --silent --show-error -o .tools/geckodriver/geckodriver.zip https://github.com/mozilla/geckodriver/releases/download/v0.37.1/geckodriver-v0.37.1-win64.zip
if ((Get-FileHash .tools/geckodriver/geckodriver.zip -Algorithm SHA256).Hash.ToLowerInvariant() -ne 'dfed9315abe8d2fbc1b6161a2ee8002452e79cf05ee92fdc653a4e26bc35edd8') { throw 'geckodriver checksum mismatch' }
Expand-Archive -Force .tools/geckodriver/geckodriver.zip .tools/geckodriver
curl.exe --fail --location --silent --show-error -o .tools/floccus-5.13.0.xpi https://github.com/floccusaddon/floccus/releases/download/v5.13.0/floccus-build-v5.13.0.xpi
if ((Get-FileHash .tools/floccus-5.13.0.xpi -Algorithm SHA256).Hash.ToLowerInvariant() -ne 'f9aa443d5f64612c7d1c17bce2df8a9441ec756665c8a64b89f62d7d69ad1630') { throw 'floccus checksum mismatch' }
```

POSIX shell (Git Bash on Windows; use `unzip` where `tar` cannot read zip; on Linux/macOS use the matching
geckodriver archive from the same release and set `BH_GECKODRIVER`, `BH_FIREFOX_EXE`):

```sh
(cd tests/browser && npm ci)
mkdir -p .tools/geckodriver
curl -fsSL -o .tools/geckodriver/geckodriver.zip https://github.com/mozilla/geckodriver/releases/download/v0.37.1/geckodriver-v0.37.1-win64.zip
echo "dfed9315abe8d2fbc1b6161a2ee8002452e79cf05ee92fdc653a4e26bc35edd8  .tools/geckodriver/geckodriver.zip" | sha256sum -c -
unzip -o .tools/geckodriver/geckodriver.zip -d .tools/geckodriver
curl -fsSL -o .tools/floccus-5.13.0.xpi https://github.com/floccusaddon/floccus/releases/download/v5.13.0/floccus-build-v5.13.0.xpi
echo "f9aa443d5f64612c7d1c17bce2df8a9441ec756665c8a64b89f62d7d69ad1630  .tools/floccus-5.13.0.xpi" | sha256sum -c -
```

## Run

From `tests/browser`:

```powershell
npm run test:firefox      # unit tests: cleanup/error helpers, digest check (no browser needed)
npm run smoke:firefox     # native smoke (headless); add `-- --headed` or `-- --no-rpc`
```

POSIX is identical (`npm run test:firefox`, `npm run smoke:firefox`). Optional: `BOOKMARKSYNC_APP_TOKEN`
supplies the account credential; otherwise a non-secret placeholder is used. It is never printed or written.

## Result statuses and exit codes

| Status | Exit | Meaning |
|---|---|---|
| PASS | 0 | every check passed and the profile was deleted |
| FAIL | 1 | a check failed, or cleanup failed (profile retained) |
| NOT_RUN | 2 | prerequisite missing (Firefox, geckodriver, XPI, selenium) |
| BLOCKED | 3 | Firefox started or was attempted but the add-on could not be installed or found |

There is no placeholder green. A NOT_RUN or BLOCKED mandatory path exits non-zero. No other browser or
simulated adapter is ever substituted. A JSON result is written to `.tools/browser-results/smoke-firefox-*.json`.

## What the smoke checks

Firefox version recorded, add-on id from install, moz-extension UUID read from Firefox's
`extensions.webextensions.uuids` pref, a genuine `moz-extension://<uuid>/dist/html/options.html` page whose
`browser.runtime.id` and manifest version match, the options UI mounted, native roots recorded
(`menu________`, `toolbar_____`, `unfiled_____`, `mobile______`), native `browser.bookmarks` create, move,
update, search, remove and removeTree inside a sandbox folder, import of a floccus account into
`storage.local` (same entry as `BrowserAccountStorage.setAccountData`), then `runtime.sendMessage` with
`{type:'syncAccount', params:[id, strategy, force]}` exactly as `Controller.ts` does on Firefox, waiting for
`syncAccountResponse`. The first request seen by the recorder must be
`GET /ocs/v2.php/cloud/capabilities?format=json` and floccus must store an account error (E019, HTTP 500).

## Isolation and cleanup

- Each run uses a unique profile `.tools/browser-profiles/firefox-<ts>-<rand>` with its own marker file,
  passed to Firefox via `-profile`. Real user profiles are never used. Profile prefs go to that profile only.
- `MOZ_REMOTE_ALLOW_SYSTEM_ACCESS=1` is set in the environment of the geckodriver child process only. It is
  needed to read the UUID pref in chrome context. It is not a global setting.
- Deletion happens only after `driver.quit()` completes (bounded timeout) and the browser PID is confirmed
  gone, and only after the existing `removeProfileDir` containment and marker check passes.
- Quit rejection or timeout, a still-running browser, an unknown PID, or failed or incomplete deletion
  yields status FAIL, exit 1, and `CLEANUP FAILED: profile retained at <path>`. Output holds only the path and
  cause, never profile contents.
- Tests never touch user bookmarks: all bookmark writes are in a sandbox folder in the throwaway profile.

## Limitations

- Headless Windows run only, against the installed Firefox 160. Other OSes need driver and path overrides.
- The add-on is installed temporarily per run, not persisted.
- No real server sync and no cross-vendor native-root assertions yet (helpers in `lib/firefox-rpc.mjs`
  take a `driver` so they can be reused for those later).
