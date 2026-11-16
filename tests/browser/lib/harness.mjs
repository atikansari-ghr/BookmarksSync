// Isolated-profile browser harness for the stock floccus extension.
// Dev-only. Never touches real user browser profiles: every profile lives in a
// fresh directory under <repo>/.tools/browser-profiles and is removed only after
// a containment + marker check.
import { createRequire } from 'node:module'
import { fileURLToPath, pathToFileURL } from 'node:url'
import path from 'node:path'
import fs from 'node:fs'
import crypto from 'node:crypto'

const here = path.dirname(fileURLToPath(import.meta.url))
export const REPO_ROOT = path.resolve(here, '..', '..', '..')
export const TOOLS_DIR = path.join(REPO_ROOT, '.tools')
export const PROFILES_ROOT = path.join(TOOLS_DIR, 'browser-profiles')
export const RESULTS_DIR = path.join(TOOLS_DIR, 'browser-results')
export const EXTENSION_DIR = path.resolve(process.env.BH_EXTENSION_DIR || path.join(TOOLS_DIR, 'floccus-5.13.0-chrome'))
export const EXPECTED_EXTENSION_VERSION = '5.13.0'
const PROFILE_MARKER = '.bookmarksync-harness-profile'

process.env.PLAYWRIGHT_BROWSERS_PATH ||= path.join(TOOLS_DIR, 'pw-browsers')

/** Prefer tests/browser/node_modules (npm ci); fall back to the pre-installed .tools copy. */
export function loadPlaywright() {
  const candidates = [
    path.join(here, '..', 'node_modules', 'playwright'),
    process.env.BH_PLAYWRIGHT_DIR,
    path.join(TOOLS_DIR, 'browser-harness', 'node_modules', 'playwright')
  ].filter(Boolean)
  for (const dir of candidates) {
    if (fs.existsSync(path.join(dir, 'package.json'))) {
      const req = createRequire(path.join(dir, 'package.json'))
      const pkg = JSON.parse(fs.readFileSync(path.join(dir, 'package.json'), 'utf8'))
      return { playwright: req(dir), version: pkg.version, dir }
    }
  }
  throw new Error('Playwright not found. Run `npm ci` in tests/browser (see README).')
}

const WIN_BROWSERS = {
  brave: ['C:/Program Files/BraveSoftware/Brave-Browser/Application/brave.exe'],
  chrome: ['C:/Program Files/Google/Chrome/Application/chrome.exe', 'C:/Program Files (x86)/Google/Chrome/Application/chrome.exe'],
  edge: ['C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe', 'C:/Program Files/Microsoft/Edge/Application/msedge.exe']
}

export function resolveBrowser(name) {
  if (name === 'chromium') return { name, branded: false, executablePath: undefined, channel: 'chromium' }
  const exe = (process.env[`BH_${name.toUpperCase()}_EXE`] ? [process.env[`BH_${name.toUpperCase()}_EXE`]] : WIN_BROWSERS[name] || []).find(p => fs.existsSync(p))
  if (!exe) return { name, missing: true }
  return { name, branded: name !== 'brave' ? true : false, executablePath: exe }
}

/** True when `child` is strictly inside `parent` after resolving both. */
export function isContained(parent, child) {
  const rel = path.relative(path.resolve(parent), path.resolve(child))
  return rel !== '' && !rel.startsWith('..') && !path.isAbsolute(rel)
}

export function makeProfileDir(label) {
  fs.mkdirSync(PROFILES_ROOT, { recursive: true })
  const dir = path.join(PROFILES_ROOT, `${label}-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`)
  if (!isContained(PROFILES_ROOT, dir)) throw new Error('profile path escaped root')
  fs.mkdirSync(dir)
  fs.writeFileSync(path.join(dir, PROFILE_MARKER), 'created by tests/browser harness\n')
  return dir
}

/** Remove only a directory this harness created: inside PROFILES_ROOT and carrying the marker. */
export function removeProfileDir(dir) {
  const abs = path.resolve(dir)
  if (!isContained(PROFILES_ROOT, abs)) throw new Error(`refusing to delete outside harness root: ${abs}`)
  if (!fs.existsSync(path.join(abs, PROFILE_MARKER))) throw new Error(`refusing to delete unmarked directory: ${abs}`)
  fs.rmSync(abs, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 })
  return !fs.existsSync(abs)
}

export function verifyExtensionDir() {
  const manifestPath = path.join(EXTENSION_DIR, 'manifest.json')
  if (!fs.existsSync(manifestPath)) throw new Error(`floccus extension not found at ${EXTENSION_DIR}`)
  const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'))
  if (manifest.version !== EXPECTED_EXTENSION_VERSION) {
    throw new Error(`unexpected floccus version ${manifest.version} (expected ${EXPECTED_EXTENSION_VERSION})`)
  }
  return manifest
}

export function withTimeout(promise, ms, what) {
  let t
  return Promise.race([
    promise,
    new Promise((_, rej) => { t = setTimeout(() => rej(new Error(`timeout after ${ms}ms: ${what}`)), ms) })
  ]).finally(() => clearTimeout(t))
}

/**
 * Launch a browser with an isolated profile and the unmodified extension.
 * mode 'flag' = --load-extension (works in Playwright chromium, usually Brave).
 * Returns { context, profileDir, serviceWorker, extensionId, browserVersion, loadMethod }.
 */
export async function launchWithExtension({ browser, headless = true, loadTimeoutMs = 30000 }) {
  const { playwright } = loadPlaywright()
  const resolved = resolveBrowser(browser)
  if (resolved.missing) throw Object.assign(new Error(`${browser} executable not found`), { code: 'BROWSER_MISSING' })
  verifyExtensionDir()
  const profileDir = makeProfileDir(browser)
  const args = [
    `--disable-extensions-except=${EXTENSION_DIR}`,
    `--load-extension=${EXTENSION_DIR}`,
    '--no-first-run', '--no-default-browser-check', '--disable-sync'
  ]
  let context
  try {
    context = await playwright.chromium.launchPersistentContext(profileDir, {
      headless,
      ...(resolved.channel ? { channel: resolved.channel } : { executablePath: resolved.executablePath }),
      args,
      ignoreDefaultArgs: ['--disable-extensions'],
      timeout: loadTimeoutMs
    })
    // Event-based discovery: take an already-registered worker, otherwise wait for the event.
    const isFloccus = w => w.url().startsWith('chrome-extension://') && w.url().endsWith('/dist/js/background-script.js')
    let serviceWorker = context.serviceWorkers().find(isFloccus)
    if (!serviceWorker) {
      serviceWorker = await context.waitForEvent('serviceworker', { predicate: isFloccus, timeout: loadTimeoutMs })
    }
    const extensionId = new URL(serviceWorker.url()).host
    const browserVersion = context.browser()?.version() ?? null
    return { context, profileDir, serviceWorker, extensionId, browserVersion, loadMethod: '--load-extension', resolved }
  } catch (err) {
    try {
      err.profileCleaned = await closeAndClean({ context, profileDir })
    } catch (cleanupErr) {
      err.profileCleaned = false
      err.profileRetained = cleanupErr.profileRetained
      err.cleanupError = cleanupErr.message
    }
    throw err
  }
}

/**
 * Shut the browser down, then delete its profile. Fails loudly: the profile is deleted
 * only after the browser is confirmed closed, and any close/delete problem throws an
 * Error carrying `profileRetained` (the path left on disk; it may contain credentials).
 * The message holds only the path and the cause, never profile contents.
 */
export async function closeAndClean({ context, profileDir }, { closeTimeoutMs = 20000, remove = removeProfileDir } = {}) {
  const retained = (why, cause) => Object.assign(
    new Error(`profile RETAINED at ${profileDir}: ${why}`, cause ? { cause } : undefined), { profileRetained: profileDir })
  if (context) {
    try {
      await withTimeout(context.close(), closeTimeoutMs, 'context.close')
    } catch (e) {
      throw retained(`browser close failed (${e.message}); it may still be running`, e)
    }
    if (context.browser()?.isConnected()) throw retained('browser still connected after close')
  }
  let removed
  try { removed = remove(profileDir) } catch (e) { throw retained(`deletion failed (${e.message})`, e) }
  if (removed === false) throw retained('deletion incomplete')
  return true
}

export function writeResult(name, data) {
  fs.mkdirSync(RESULTS_DIR, { recursive: true })
  const file = path.join(RESULTS_DIR, `${name}-${new Date().toISOString().replace(/[:.]/g, '-')}.json`)
  fs.writeFileSync(file, JSON.stringify(data, null, 2))
  return file
}

export { pathToFileURL }
