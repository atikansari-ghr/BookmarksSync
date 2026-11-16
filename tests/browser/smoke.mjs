// Harness smoke: launch an isolated profile with stock floccus, verify the
// extension and the real chrome.bookmarks API, then the RPC plumbing.
//   node smoke.mjs [--browser=chromium|brave|chrome|edge] [--headed] [--no-rpc]
import os from 'node:os'
import http from 'node:http'
import {
  launchWithExtension, closeAndClean, writeResult, loadPlaywright, verifyExtensionDir,
  EXPECTED_EXTENSION_VERSION, EXTENSION_DIR, resolveBrowser
} from './lib/harness.mjs'
import {
  openExtensionPage, importAccount, syncAccount, createSandboxFolder, readSubtree
} from './lib/floccus-rpc.mjs'

const arg = (n, d) => (process.argv.find(a => a.startsWith(`--${n}=`)) || '').split('=')[1] ?? d
const browser = arg('browser', 'chromium')
const headless = !process.argv.includes('--headed')

const checks = []
const check = (name, ok, detail) => {
  checks.push({ name, ok: !!ok, detail })
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? ' - ' + detail : ''}`)
  if (!ok) throw Object.assign(new Error(`check failed: ${name}`), { checkFailed: true })
}

const report = {
  harness: 'smoke', browser, headless, startedAt: new Date().toISOString(),
  node: process.version, os: `${os.type()} ${os.release()}`, extensionDir: EXTENSION_DIR,
  playwright: loadPlaywright().version, checks, status: 'FAIL'
}

function stubServer() {
  // NOT a protocol server: a request recorder returning 500, used only to prove the
  // sync RPC reaches real floccus code that issues its first capabilities request.
  const requests = []
  const srv = http.createServer((req, res) => {
    requests.push(`${req.method} ${req.url}`)
    res.writeHead(500, {
      'content-type': 'application/json',
      'access-control-allow-origin': '*',
      'access-control-allow-headers': '*'
    })
    res.end('{"status":"error"}')
  })
  return new Promise(r => srv.listen(0, '127.0.0.1', () => r({ srv, requests, port: srv.address().port })))
}

let session
let exitCode = 1
try {
  const manifest = verifyExtensionDir()
  report.extensionManifestVersion = manifest.version
  const resolved = resolveBrowser(browser)
  if (resolved.missing) {
    report.status = 'NOT_RUN'
    report.reason = `${browser} executable not found`
    throw Object.assign(new Error(report.reason), { skip: true })
  }
  try {
    session = await launchWithExtension({ browser, headless })
  } catch (e) {
    // Never substitute another browser: report the limitation explicitly.
    report.status = 'BLOCKED'
    report.reason = `could not load extension in ${browser}:${e.message.split('\n')[0]}`
    report.profileCleaned = e.profileCleaned
    report.profileRetained = e.profileRetained
    report.cleanupError = e.cleanupError
    throw Object.assign(new Error(report.reason), { skip: true })
  }
  const { pageContext, serviceWorker: sw, extensionId, browserVersion, profileDir } = session
  report.serviceWorkerKind = sw.kind || 'playwright-worker'
  report.browserVersion = browserVersion
  report.loadMethod = session.loadMethod
  report.profileDir = profileDir
  report.extensionId = extensionId

  const rtVersion = await sw.evaluate(() => chrome.runtime.getManifest().version)
  check('service worker reports manifest version', rtVersion === EXPECTED_EXTENSION_VERSION, rtVersion)

  const roots = await sw.evaluate(async () =>
    (await chrome.bookmarks.getTree())[0].children.map(c => ({ id: c.id, title: c.title, folderType: c.folderType ?? null })))
  report.bookmarkRoots = roots
  check('browser exposes bookmark roots', roots.length >= 2, JSON.stringify(roots))

  // Real chrome.bookmarks CRUD in a sandbox folder (own folder, removed at the end).
  const crud = await sw.evaluate(async () => {
    const out = {}
    const kids = (await chrome.bookmarks.getTree())[0].children
    const other = kids.find(c => c.folderType === 'other') ?? kids[1]
    const sandbox = await chrome.bookmarks.create({ parentId: other.id, title: 'bookmarksync-harness-sandbox' })
    const a = await chrome.bookmarks.create({ parentId: sandbox.id, title: 'A', url: 'https://example.com/a' })
    const sub = await chrome.bookmarks.create({ parentId: sandbox.id, title: 'sub' })
    out.created = (await chrome.bookmarks.get(a.id))[0].url === 'https://example.com/a'
    await chrome.bookmarks.move(a.id, { parentId: sub.id })
    out.moved = (await chrome.bookmarks.get(a.id))[0].parentId === sub.id
    await chrome.bookmarks.update(a.id, { title: 'A2' })
    out.updated = (await chrome.bookmarks.get(a.id))[0].title === 'A2'
    out.found = (await chrome.bookmarks.search({ url: 'https://example.com/a' })).length === 1
    await chrome.bookmarks.remove(a.id)
    out.removed = (await chrome.bookmarks.search({ url: 'https://example.com/a' })).length === 0
    await chrome.bookmarks.removeTree(sandbox.id)
    out.sandboxGone = await chrome.bookmarks.get(sandbox.id).then(() => false, () => true)
    return out
  })
  for (const [k, v] of Object.entries(crud)) check(`chrome.bookmarks ${k}`, v)

  // RPC plumbing: import an account (token via env, never printed) and trigger a real sync.
  if (!process.argv.includes('--no-rpc')) {
    const { srv, requests, port } = await stubServer()
    try {
      const folderId = await createSandboxFolder(sw, 'bookmarksync-harness-rpc')
      const page = await openExtensionPage(pageContext, extensionId)
      const accountId = await importAccount(page, {
        type: 'nextcloud-bookmarks', url: `http://127.0.0.1:${port}/`, username: 'harness',
        password: process.env.BOOKMARKSYNC_APP_TOKEN || 'harness-placeholder-not-a-secret',
        serverRoot: '', localRoot: folderId, strategy: 'slave', failsafe: true,
        syncIntervalEnabled: false, enabled: true, includeCredentials: false, allowRedirects: false, label: 'harness-stub'
      })
      const res = await syncAccount(page, accountId, { strategy: 'slave', force: true, timeoutMs: 45000 })
      check('syncAccount RPC completed (syncAccountResponse event)', res.done, res.reason)
      check('real floccus issued its first request to the stub (capabilities GET)',
        requests[0] && requests[0].startsWith('GET /ocs/v2.php/cloud/capabilities'), requests.slice(0, 3).join(' | '))
      check('floccus recorded an error on the account (stub returns 500)', !!res.status?.error, String(res.status?.error).slice(0, 80))
      report.rpc = {
        requests: requests.slice(0, 5),
        note: 'stub returns HTTP 500; plumbing only, NOT a protocol test',
        subtree: await readSubtree(sw, folderId)
      }
    } finally { srv.close() }
  }

  report.status = 'PASS'
  exitCode = 0
} catch (e) {
  report.error = report.error || e.message
  if (!e.checkFailed && !e.skip) report.stack = e.stack
  if (e.skip) exitCode = report.status === 'NOT_RUN' ? 2 : 3
} finally {
  if (session) {
    try {
      report.profileCleaned = await closeAndClean(session)
    } catch (e) {
      report.profileCleaned = false
      report.profileRetained = e.profileRetained ?? session.profileDir
      report.cleanupError = e.message
    }
  }
  if (report.profileCleaned === false) {
    // A leftover browser/profile (may hold credentials) is a failure, whatever else happened.
    report.status = 'FAIL'
    report.error = report.cleanupError || 'cleanup failed'
    exitCode = 1
  }
  report.finishedAt = new Date().toISOString()
  const file = writeResult(`smoke-${browser}`, report)
  console.log(`\nRESULT ${browser}: ${report.status}${report.reason ? ' (' + report.reason + ')' : ''}${report.error && report.status === 'FAIL' ? ' - ' + report.error : ''}`)
  console.log(`profile cleaned: ${report.profileCleaned}   result file: ${file}`)
  if (report.profileRetained) console.error(`CLEANUP FAILED: profile retained at ${report.profileRetained} - ${report.cleanupError}`)
  process.exit(exitCode)
}
