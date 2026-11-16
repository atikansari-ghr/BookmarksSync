// Native Firefox harness for the stock, unmodified floccus extension (Firefox release XPI).
// Playwright's Firefox build has no add-on support, so this drives the installed
// Firefox through geckodriver (WebDriver/Marionette) via selenium-webdriver.
// Dev-only. Never touches a real user profile: every profile lives in a unique,
// marker-bearing directory under <repo>/.tools/browser-profiles (see harness.mjs).
// harness.mjs exports are used read-only; nothing here edits the Chrome harness.
import fs from 'node:fs'
import path from 'node:path'
import crypto from 'node:crypto'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { TOOLS_DIR, makeProfileDir, removeProfileDir, withTimeout } from './harness.mjs'

const here = path.dirname(fileURLToPath(import.meta.url))
export const FIREFOX_EXTENSION_ID = 'floccus@handmadeideas.org'
export const EXPECTED_EXTENSION_VERSION = '5.13.0'
export const GECKODRIVER_VERSION = '0.37.1'
export const GECKODRIVER_ZIP_SHA256 = 'dfed9315abe8d2fbc1b6161a2ee8002452e79cf05ee92fdc653a4e26bc35edd8'
export const XPI_SHA256 = 'f9aa443d5f64612c7d1c17bce2df8a9441ec756665c8a64b89f62d7d69ad1630'
export const XPI_URL = 'https://github.com/floccusaddon/floccus/releases/download/v5.13.0/floccus-build-v5.13.0.xpi'
export const GECKODRIVER_URL = `https://github.com/mozilla/geckodriver/releases/download/v${GECKODRIVER_VERSION}/geckodriver-v${GECKODRIVER_VERSION}-win64.zip`
export const XPI_PATH = path.resolve(process.env.BH_FIREFOX_XPI || path.join(TOOLS_DIR, 'floccus-5.13.0.xpi'))
export const GECKODRIVER_PATH = path.resolve(process.env.BH_GECKODRIVER ||
  path.join(TOOLS_DIR, 'geckodriver', process.platform === 'win32' ? 'geckodriver.exe' : 'geckodriver'))
// Default WebDriver async-script timeout; must exceed the longest RPC wait (60 s default sync).
export const SCRIPT_TIMEOUT_MS = 70000
const WIN_FIREFOX = ['C:/Program Files/Mozilla Firefox/firefox.exe', 'C:/Program Files (x86)/Mozilla Firefox/firefox.exe']

/** Thrown for any close/cleanup problem. `profileRetained` is the directory left on disk. */
export class FirefoxHarnessError extends Error {
  constructor(message, { profileRetained, cause } = {}) {
    super(message, cause ? { cause } : undefined)
    this.name = 'FirefoxHarnessError'
    this.profileRetained = profileRetained
  }
}

/** Throws unless the file's SHA-256 equals `expected`. Never prints file contents. */
export function verifySha256(file, expected) {
  const actual = crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')
  if (actual !== expected.toLowerCase()) {
    throw new Error(`sha256 mismatch for ${path.basename(file)}: expected ${expected} got ${actual}`)
  }
  return actual
}

export function resolveFirefox() {
  const candidates = process.env.BH_FIREFOX_EXE ? [process.env.BH_FIREFOX_EXE] : WIN_FIREFOX
  const exe = candidates.find(p => fs.existsSync(p))
  return exe ? { executablePath: exe } : { missing: true }
}

/** Uses tests/browser/node_modules (npm ci). */
export function loadSelenium() {
  const dir = path.join(here, '..', 'node_modules', 'selenium-webdriver')
  if (!fs.existsSync(path.join(dir, 'package.json'))) {
    throw Object.assign(new Error('selenium-webdriver not found. Run `npm ci` in tests/browser (see FIREFOX.md).'), { code: 'DEPENDENCY_MISSING' })
  }
  const req = createRequire(path.join(dir, 'package.json'))
  return {
    selenium: req(dir),
    firefox: req(path.join(dir, 'firefox')),
    version: JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8')).version
  }
}

/** Check the pinned XPI digest and that the driver exists. Returns facts for the report. */
export function verifyToolchain() {
  if (!fs.existsSync(XPI_PATH)) throw Object.assign(new Error(`floccus XPI not found at ${XPI_PATH}`), { code: 'TOOLCHAIN_MISSING' })
  if (!fs.existsSync(GECKODRIVER_PATH)) throw Object.assign(new Error(`geckodriver not found at ${GECKODRIVER_PATH}`), { code: 'TOOLCHAIN_MISSING' })
  return { xpiSha256: verifySha256(XPI_PATH, XPI_SHA256), geckodriverPath: GECKODRIVER_PATH }
}

export function pidAlive(pid) {
  try { process.kill(pid, 0); return true } catch (e) { return e.code === 'EPERM' }
}

/**
 * Quit the WebDriver session, prove the browser process is gone, and only then delete the
 * profile. Any problem throws FirefoxHarnessError carrying `profileRetained`. Messages hold
 * only the path and the cause, never profile contents (a profile may contain credentials).
 * `service` (the owned geckodriver service) is always killed best-effort, even when quit fails.
 * Only the pid this harness learned from its own session is ever probed; nothing is signalled.
 */
export async function closeAndCleanFirefox({ driver, profileDir, pid, service },
  { quitTimeoutMs = 30000, exitWaitMs = 15000, pollMs = 100, isAlive = pidAlive, remove = removeProfileDir } = {}) {
  const retained = (why, cause) => new FirefoxHarnessError(`profile RETAINED at ${profileDir}: ${why}`, { profileRetained: profileDir, cause })
  const killService = async () => {
    if (!service) return
    try { await withTimeout(Promise.resolve(service.kill()), quitTimeoutMs, 'service.kill') } catch { /* best effort */ }
  }
  if (driver) {
    try {
      await withTimeout(driver.quit(), quitTimeoutMs, 'driver.quit')
    } catch (e) {
      await killService()
      throw retained(`driver quit failed (${e.message}); browser may still be running`, e)
    }
  }
  await killService()
  if (!pid) throw retained('browser pid unknown, cannot prove the browser exited')
  const deadline = Date.now() + exitWaitMs
  while (isAlive(pid)) {
    if (Date.now() >= deadline) throw retained(`browser process ${pid} still running after quit`)
    await new Promise(r => setTimeout(r, pollMs))
  }
  let removed
  try { removed = remove(profileDir) } catch (e) { throw retained(`deletion failed (${e.message})`, e) }
  if (removed === false) throw retained('deletion incomplete')
  return true
}

const sessionPid = session => session.getCapabilities().get('moz:processID')

/**
 * Start a WebDriver session while keeping ownership of the driver and service handles.
 * `create()` must synchronously return { driver, service } (selenium's Driver.createSession
 * starts the session in the background; `driver.getSession()` is the startup promise).
 * On timeout or failure the handles are NOT lost: a late-arriving session is quit (bounded),
 * the owned service is killed, and the error carries `owned = { driver, service, pid }`
 * (pid only if our own session reported it). A session that arrives even later than
 * `lateWaitMs` is still shut down by a background handler. Nothing here signals any process.
 */
export async function startOwnedDriver({ create, startupTimeoutMs = 60000, lateWaitMs = 20000, quitTimeoutMs = 20000 }) {
  const { driver, service } = create()
  const sessionP = Promise.resolve(driver.getSession())
  const settled = sessionP.then(s => ({ s }), e => ({ e }))
  const killService = async () => {
    try { await withTimeout(Promise.resolve(service.kill()), quitTimeoutMs, 'service.kill') } catch { /* best effort */ }
  }
  let first
  try {
    first = await withTimeout(settled, startupTimeoutMs, 'start Firefox via geckodriver')
  } catch (timeoutErr) {
    // Startup is slow or hung. Keep ownership: give it a bounded grace period.
    let pid
    let late
    try { late = await withTimeout(settled, lateWaitMs, 'late startup grace period') } catch { /* still hung */ }
    if (late && late.s) {
      try { pid = sessionPid(late.s) } catch { /* unknown */ }
      try { await withTimeout(driver.quit(), quitTimeoutMs, 'late driver.quit') } catch { /* retained path reports it */ }
    } else {
      // Even later arrival: shut it down whenever it shows up, so no browser is orphaned.
      settled.then(r => { if (r.s) { Promise.resolve().then(() => driver.quit()).catch(() => {}).finally(() => Promise.resolve(service.kill()).catch(() => {})) } })
    }
    await killService()
    throw Object.assign(new FirefoxHarnessError(timeoutErr.message), { owned: { driver, service, pid } })
  }
  if (first.e) {
    await killService()
    throw Object.assign(first.e, { owned: { driver, service, pid: undefined } })
  }
  return { driver, service, pid: sessionPid(first.s) }
}

/**
 * Launch headless Firefox in a fresh isolated profile, temporarily install the unmodified
 * signed floccus XPI and locate its moz-extension UUID.
 * Returns { driver, service, profileDir, pid, addonId, extensionUuid, browserVersion, seleniumVersion }.
 * On any failure after launch the browser is quit and the profile cleaned (or retained and reported).
 */
export async function launchFirefoxWithFloccus({ headless = true, timeoutMs = 60000, scriptTimeoutMs = SCRIPT_TIMEOUT_MS } = {}) {
  const resolved = resolveFirefox()
  if (resolved.missing) throw Object.assign(new Error('Firefox executable not found'), { code: 'BROWSER_MISSING' })
  verifyToolchain()
  const { selenium, firefox, version: seleniumVersion } = loadSelenium()
  const profileDir = makeProfileDir('firefox')
  const options = new firefox.Options()
  options.setBinary(resolved.executablePath)
  const args = ['-profile', profileDir, '-no-remote']
  if (headless) args.unshift('-headless')
  options.addArguments(...args)
  // Prefs live in the disposable profile only (geckodriver writes them to its user.js).
  for (const [k, v] of Object.entries({
    'app.update.auto': false, 'app.update.enabled': false, 'browser.shell.checkDefaultBrowser': false,
    'datareporting.policy.dataSubmissionEnabled': false, 'toolkit.telemetry.reportingpolicy.firstRun': false,
    'browser.startup.homepage_override.mstone': 'ignore'
  })) options.setPreference(k, v)
  // Env is scoped to the geckodriver child process tree we own (not a global setting): it lets
  // Marionette read the add-on UUID pref in chrome context. The profile is disposable.
  const service = new firefox.ServiceBuilder(GECKODRIVER_PATH)
    .setEnvironment({ ...process.env, MOZ_REMOTE_ALLOW_SYSTEM_ACCESS: '1' })
    .build()
  let owned = { driver: undefined, service, pid: undefined }
  try {
    const started = await startOwnedDriver({
      create: () => ({ driver: firefox.Driver.createSession(options, service), service }),
      startupTimeoutMs: timeoutMs
    }).catch(e => { if (e.owned) owned = { ...e.owned, driver: undefined }; throw e })
    const { driver, pid } = started
    owned = { driver, service, pid }
    // Bound executeAsyncScript so a hung page script fails inside the RPC timeouts (45/60 s).
    await driver.manage().setTimeouts({ script: scriptTimeoutMs })
    const caps = await driver.getCapabilities()
    const browserVersion = caps.getBrowserVersion()
    // Temporary install of the official signed XPI: no signing/policy setting is changed.
    const addonId = await withTimeout(driver.installAddon(XPI_PATH, true), timeoutMs, 'installAddon')
    const extensionUuid = await locateExtensionUuid(driver, selenium, timeoutMs)
    return { driver, service, profileDir, pid, addonId, extensionUuid, browserVersion, seleniumVersion }
  } catch (err) {
    try {
      err.profileCleaned = await closeAndCleanFirefox({ ...owned, profileDir })
    } catch (cleanupErr) {
      err.profileCleaned = false
      err.profileRetained = cleanupErr.profileRetained
      err.cleanupError = cleanupErr.message
    }
    throw err
  }
}

/** Read the add-on's internal UUID from Firefox's own pref (chrome context, read-only). */
async function locateExtensionUuid(driver, selenium, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  await driver.setContext('chrome')
  try {
    for (;;) {
      const map = await driver.executeScript(
        'try { return Services.prefs.getStringPref("extensions.webextensions.uuids", "{}") } catch (e) { return "ERR:" + e.message }')
      if (typeof map === 'string' && map.startsWith('ERR:')) throw new Error(`cannot read extension UUID pref (${map})`)
      const uuid = JSON.parse(map)[FIREFOX_EXTENSION_ID]
      if (uuid) return uuid
      if (Date.now() > deadline) throw new Error('moz-extension UUID for floccus not found')
      await new Promise(r => setTimeout(r, 250))
    }
  } finally {
    await driver.setContext('content')
  }
}
