// Isolated-profile browser harness for the stock floccus extension.
// Dev-only. Never touches real user browser profiles: every profile lives in a
// fresh directory under <repo>/.tools/browser-profiles and is removed only after
// a containment + marker check.
import { createRequire } from 'node:module'
import { fileURLToPath, pathToFileURL } from 'node:url'
import path from 'node:path'
import fs from 'node:fs'
import crypto from 'node:crypto'
import { spawn, spawnSync } from 'node:child_process'

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
  // Branded Chrome ignores --load-extension, so it is launched natively and loaded over CDP.
  return { name, branded: name !== 'brave' ? true : false, executablePath: exe, nativeCdp: name === 'chrome' }
}

/** True when `child` is strictly inside `parent` after resolving both. */
export function isContained(parent, child) {
  const rel = path.relative(path.resolve(parent), path.resolve(child))
  return rel !== '' && !rel.startsWith('..') && !path.isAbsolute(rel)
}

/** Like isContained, but on physical paths: resolves junctions/symlinks in both. */
export function isPhysicallyContained(parent, child) {
  return isContained(fs.realpathSync.native(parent), fs.realpathSync.native(child))
}

export function makeProfileDir(label, { root = PROFILES_ROOT, toolsDir = TOOLS_DIR } = {}) {
  fs.mkdirSync(toolsDir, { recursive: true })
  fs.mkdirSync(root, { recursive: true })
  // The root must physically live inside the tools dir (a junction here would redirect every profile).
  if (!isPhysicallyContained(toolsDir, root)) throw new Error(`refusing: profiles root escapes tools dir: ${root}`)
  const dir = path.join(root, `${label}-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`)
  if (!isContained(root, dir)) throw new Error('profile path escaped root')
  fs.mkdirSync(dir)
  try {
    if (fs.lstatSync(dir).isSymbolicLink() || !isPhysicallyContained(root, dir)) throw new Error('profile path escaped root (physical)')
    fs.writeFileSync(path.join(dir, PROFILE_MARKER), 'created by tests/browser harness\n')
  } catch (e) {
    try { fs.rmdirSync(dir) } catch {}
    throw e
  }
  return dir
}

/**
 * Remove only a directory this harness created: lexically AND physically (realpath) inside the
 * profiles root, not itself a link, and carrying the marker.
 */
export function removeProfileDir(dir, { root = PROFILES_ROOT } = {}) {
  const abs = path.resolve(dir)
  if (!isContained(root, abs)) throw new Error(`refusing to delete outside harness root: ${abs}`)
  if (!fs.existsSync(abs)) return true
  if (fs.lstatSync(abs).isSymbolicLink()) throw new Error(`refusing to delete a link: ${abs}`)
  if (!isPhysicallyContained(root, abs)) throw new Error(`refusing to delete: resolves outside harness root: ${abs}`)
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
  const resolved = resolveBrowser(browser)
  if (resolved.missing) throw Object.assign(new Error(`${browser} executable not found`), { code: 'BROWSER_MISSING' })
  verifyExtensionDir()
  if (resolved.nativeCdp) return launchChromeNative({ resolved, headless, loadTimeoutMs })
  const { playwright } = loadPlaywright()
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
    return { context, pageContext: context, profileDir, serviceWorker, extensionId, browserVersion, loadMethod: '--load-extension', resolved }
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

// ---------------------------------------------------------------------------
// Native branded Chrome: launched by this harness (not Playwright) with a
// disposable profile; the extension is loaded through CDP Extensions.loadUnpacked.
// The Chrome flags below are for this throw-away profile only.
// ---------------------------------------------------------------------------

export function buildChromeNativeArgs({ profileDir, headless = true }) {
  return [
    ...(headless ? ['--headless=new'] : []),
    `--user-data-dir=${profileDir}`,
    '--remote-debugging-port=0',
    '--enable-unsafe-extension-debugging', // required by Extensions.loadUnpacked; never used on a real profile
    '--no-first-run', '--no-default-browser-check', '--disable-sync',
    'about:blank'
  ]
}

/** DevToolsActivePort is "<port>\n<browser ws path>\n". Returns null until complete and valid. */
export function parseDevToolsActivePort(text) {
  const [portLine, wsPath] = String(text).split('\n')
  const port = Number(portLine)
  if (!Number.isInteger(port) || port <= 0 || !wsPath || !wsPath.startsWith('/')) return null
  return { port, path: wsPath.trim() }
}

/** Minimal CDP client over a WebSocket-like object (send / addEventListener). */
export function createCdpClient(socket, { requestTimeoutMs = 15000 } = {}) {
  let nextId = 0
  let closed = false
  const pending = new Map()
  const listeners = new Map()
  socket.addEventListener('message', (ev) => {
    let m
    try { m = JSON.parse(typeof ev.data === 'string' ? ev.data : String(ev.data)) } catch { return }
    if (m.id !== undefined && pending.has(m.id)) {
      const { resolve, reject } = pending.get(m.id)
      pending.delete(m.id)
      if (m.error) reject(new Error(`${m.error.message}${m.error.data ? ` (${m.error.data})` : ''}`))
      else resolve(m.result ?? {})
    } else if (m.method) {
      for (const h of [...(listeners.get(m.method) || [])]) h(m.params, m.sessionId)
    }
  })
  socket.addEventListener('close', () => {
    closed = true
    for (const { reject } of pending.values()) reject(new Error('CDP connection closed'))
    pending.clear()
  })
  return {
    send(method, params = {}, sessionId) {
      if (closed) return Promise.reject(new Error('CDP connection closed'))
      const id = ++nextId
      return new Promise((resolve, reject) => {
        // Every request has a deadline: a response that never arrives must not hang the run.
        const timer = setTimeout(() => {
          pending.delete(id)
          reject(new Error(`timeout after ${requestTimeoutMs}ms: CDP ${method}`))
        }, requestTimeoutMs)
        pending.set(id, {
          resolve: v => { clearTimeout(timer); resolve(v) },
          reject: e => { clearTimeout(timer); reject(e) }
        })
        try {
          socket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }))
        } catch (e) { pending.get(id)?.reject(e); pending.delete(id) }
      })
    },
    on(method, handler) {
      if (!listeners.has(method)) listeners.set(method, new Set())
      listeners.get(method).add(handler)
      return () => listeners.get(method)?.delete(handler)
    },
    close() { try { socket.close() } catch {} }
  }
}

/**
 * Evaluator bound to a CDP-attached extension service worker target. It runs code in the
 * real background worker (Runtime.evaluate). It is NOT a Playwright Worker and NOT an
 * extension page, hence the explicit `kind`.
 */
export function createServiceWorkerEvaluator({ client, sessionId, url }) {
  return {
    kind: 'cdp-service-worker-target',
    url: () => url,
    async evaluate(fn, arg) {
      const expression = `(${fn.toString()})(${arg === undefined ? '' : JSON.stringify(arg)})`
      const res = await client.send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true }, sessionId)
      if (res.exceptionDetails) {
        const d = res.exceptionDetails
        throw new Error(`service worker evaluate failed: ${d.exception?.description || d.text}`)
      }
      return res.result?.value
    }
  }
}

/** Resolve the first target matching `predicate`, already present or announced via Target.targetCreated. */
export async function waitForServiceWorkerTarget(client, predicate, timeoutMs) {
  let off
  const created = new Promise((resolve) => {
    off = client.on('Target.targetCreated', ({ targetInfo }) => { if (predicate(targetInfo)) resolve(targetInfo) })
  })
  try {
    const { targetInfos = [] } = await client.send('Target.getTargets')
    const existing = targetInfos.find(predicate)
    if (existing) return existing
    return await withTimeout(created, timeoutMs, 'extension service worker target')
  } finally { off?.() }
}

/**
 * Close-able handle for a browser process this harness spawned, shaped like the part of a
 * Playwright context that closeAndClean uses. close() asks the browser to quit, then AWAITS
 * process exit; a process that stays alive is killed and close() fails (profile retained).
 */
export function createOwnedBrowserHandle({ child, client }, { exitTimeoutMs = 8000, killTimeoutMs = 5000 } = {}) {
  let exited = child.exitCode !== null && child.exitCode !== undefined
  const exitP = exited ? Promise.resolve() : new Promise(r => child.once('exit', () => { exited = true; r() }))
  return {
    browser: () => ({ isConnected: () => !exited }),
    // Whole shutdown is bounded by exitTimeoutMs + killTimeoutMs (kept below closeAndClean's own limit).
    async close() {
      let sendErr
      // Browser.close is fire-and-forget: its response may never come (or the socket drops as Chrome quits).
      client.send('Browser.close').catch(e => { sendErr = e })
      try {
        await withTimeout(exitP, exitTimeoutMs, 'browser process exit')
        client.close()
        return
      } catch (e) {
        const graceful = e.message
        try {
          terminateProcess(child)
          await withTimeout(exitP, killTimeoutMs, 'forced termination')
        } catch (killErr) {
          client.close()
          throw new Error(`browser process did not exit, even after forced kill (${graceful}; ${killErr.message}${sendErr ? `; Browser.close: ${sendErr.message}` : ''})`)
        }
        client.close()
        throw new Error(`browser process did not exit gracefully and was force-killed (${graceful}${sendErr ? `; Browser.close: ${sendErr.message}` : ''})`)
      }
    }
  }
}

/** Force-terminate a process this harness spawned (and only it, with its own children on Windows). */
function terminateProcess(child) {
  try { child.kill() } catch {}
  if (process.platform === 'win32' && child.pid) {
    try { spawnSync('taskkill', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore', windowsHide: true, timeout: 5000 }) } catch {}
  }
}

/** Retry `probe` (which may throw or return falsy) until truthy; bounded, fails with the last error. */
export async function waitUntilReady(probe, timeoutMs, what, intervalMs = 50) {
  const deadline = Date.now() + timeoutMs
  let last
  while (Date.now() < deadline) {
    // Each attempt is itself bounded by the time left, so a probe that never settles cannot hang us.
    try { if (await withTimeout(Promise.resolve().then(probe), Math.max(1, deadline - Date.now()), what)) return } catch (e) { last = e }
    await new Promise(r => setTimeout(r, intervalMs))
  }
  throw new Error(`timeout after ${timeoutMs}ms: ${what}${last ? ` (${last.message.split('\n')[0]})` : ''}`)
}

async function readDevToolsPort(profileDir, child, timeoutMs) {
  const file = path.join(profileDir, 'DevToolsActivePort')
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (child.exitCode !== null && child.exitCode !== undefined) throw new Error(`Chrome exited early (code ${child.exitCode})`)
    try {
      const info = parseDevToolsActivePort(fs.readFileSync(file, 'utf8'))
      if (info) return info
    } catch {}
    await new Promise(r => setTimeout(r, 100))
  }
  throw new Error(`timeout after ${timeoutMs}ms: DevToolsActivePort`)
}

async function launchChromeNative({ resolved, headless, loadTimeoutMs }) {
  const profileDir = makeProfileDir('chrome')
  let child, client, handle
  try {
    child = spawn(resolved.executablePath, buildChromeNativeArgs({ profileDir, headless }), { stdio: 'ignore', windowsHide: true })
    child.once('error', () => {})
    const { port, path: wsPath } = await readDevToolsPort(profileDir, child, loadTimeoutMs)
    const ws = new WebSocket(`ws://127.0.0.1:${port}${wsPath}`)
    await withTimeout(new Promise((res, rej) => {
      ws.addEventListener('open', res)
      ws.addEventListener('error', () => rej(new Error('CDP websocket error')))
    }), loadTimeoutMs, 'CDP websocket open')
    client = createCdpClient(ws, { requestTimeoutMs: loadTimeoutMs })
    handle = createOwnedBrowserHandle({ child, client })
    const browserVersion = (await client.send('Browser.getVersion')).product.replace(/^Chrome\//, '')
    await client.send('Target.setDiscoverTargets', { discover: true })
    const { id: extensionId } = await client.send('Extensions.loadUnpacked', { path: EXTENSION_DIR })
    const swInfo = await waitForServiceWorkerTarget(client,
      t => t.type === 'service_worker' && t.url === `chrome-extension://${extensionId}/dist/js/background-script.js`, loadTimeoutMs)
    const { sessionId } = await client.send('Target.attachToTarget', { targetId: swInfo.targetId, flatten: true })
    const serviceWorker = createServiceWorkerEvaluator({ client, sessionId, url: swInfo.url })
    // The worker target is announced before its chrome.* bindings exist; wait (bounded) for the real APIs.
    await waitUntilReady(() => serviceWorker.evaluate(() => typeof chrome !== 'undefined' && !!chrome.runtime?.id && !!chrome.bookmarks),
      loadTimeoutMs, 'extension APIs in service worker')
    // Extension pages (options UI that talks to the worker) go through Playwright attached to the same browser.
    const { playwright } = loadPlaywright()
    const pwBrowser = await playwright.chromium.connectOverCDP(`http://127.0.0.1:${port}`, { timeout: loadTimeoutMs })
    const pageContext = pwBrowser.contexts()[0]
    if (!pageContext) throw new Error('no default browser context over CDP')
    return {
      context: handle, pageContext, profileDir, serviceWorker, extensionId, browserVersion,
      loadMethod: 'CDP Extensions.loadUnpacked (--enable-unsafe-extension-debugging)', resolved
    }
  } catch (err) {
    try {
      if (!handle && child) {
        // Failed before the CDP handle existed: kill and await the process we own.
        handle = {
          browser: () => ({ isConnected: () => child.exitCode === null }),
          close: async () => {
            terminateProcess(child)
            await withTimeout(new Promise(r => (child.exitCode !== null ? r() : child.once('exit', r))), 8000, 'forced termination')
          }
        }
      }
      err.profileCleaned = await closeAndClean({ context: handle, profileDir })
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
