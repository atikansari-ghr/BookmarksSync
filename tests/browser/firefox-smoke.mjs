// Native Firefox smoke: installed Firefox + geckodriver + official floccus v5.13.0 XPI in a
// disposable profile. Proves plumbing only (extension running, native bookmarks, background
// RPC reaching real floccus code); NOT protocol-server compatibility.
//   node firefox-smoke.mjs [--headed] [--no-rpc]
// Exit codes: 0 PASS, 1 FAIL, 2 NOT_RUN (prerequisite missing), 3 BLOCKED (could not start/install).
import os from 'node:os'
import http from 'node:http'
import { writeResult } from './lib/harness.mjs'
import {
  launchFirefoxWithFloccus, closeAndCleanFirefox, resolveFirefox, verifyToolchain, loadSelenium,
  EXPECTED_EXTENSION_VERSION, FIREFOX_EXTENSION_ID, XPI_PATH, GECKODRIVER_PATH, GECKODRIVER_VERSION
} from './lib/firefox-harness.mjs'
import {
  openExtensionPage, importAccount, syncAccount, createSandboxFolder, readSubtree, getNativeRoots, evalInPage
} from './lib/firefox-rpc.mjs'

const headless = !process.argv.includes('--headed')
const checks = []
const check = (name, ok, detail) => {
  checks.push({ name, ok: !!ok, detail })
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? ' - ' + detail : ''}`)
  if (!ok) throw Object.assign(new Error(`check failed: ${name}`), { checkFailed: true })
}

const report = {
  harness: 'firefox-smoke', browser: 'firefox', headless, startedAt: new Date().toISOString(),
  node: process.version, os: `${os.type()} ${os.release()}`, xpi: XPI_PATH,
  geckodriver: GECKODRIVER_PATH, geckodriverVersion: GECKODRIVER_VERSION, checks, status: 'FAIL'
}

function recorderServer() {
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

const skip = (status, reason) => {
  report.status = status
  report.reason = reason
  return Object.assign(new Error(reason), { skip: true })
}

let session
let exitCode = 1
try {
  if (resolveFirefox().missing) throw skip('NOT_RUN', 'Firefox executable not found')
  try {
    report.toolchain = verifyToolchain()
    report.selenium = loadSelenium().version
  } catch (e) {
    throw skip('NOT_RUN', e.message)
  }
  try {
    session = await launchFirefoxWithFloccus({ headless })
  } catch (e) {
    // Never substitute another browser or a simulated adapter: report the limitation.
    report.profileCleaned = e.profileCleaned
    report.profileRetained = e.profileRetained
    report.cleanupError = e.cleanupError
    throw skip('BLOCKED', `could not start Firefox / install floccus: ${String(e.message).split('\n')[0]}`)
  }
  const { driver, extensionUuid, browserVersion, profileDir, addonId } = session
  report.browserVersion = browserVersion
  report.addonId = addonId
  report.extensionUuid = extensionUuid
  report.profileDir = profileDir
  report.loadMethod = 'WebDriver installAddon(temporary=true), official signed XPI'

  check('Firefox reports a version', /^\d+\./.test(browserVersion), browserVersion)
  check('install returned the floccus add-on id', addonId === FIREFOX_EXTENSION_ID, addonId)
  check('moz-extension UUID located', /^[0-9a-f-]{36}$/.test(extensionUuid), extensionUuid)

  const page = await openExtensionPage(driver, extensionUuid)
  report.extensionPage = { href: page.href, id: page.id, version: page.version, name: page.name }
  check('genuine extension page is running (runtime.id matches)', page.id === FIREFOX_EXTENSION_ID, page.id)
  check('extension reports manifest version', page.version === EXPECTED_EXTENSION_VERSION, page.version)
  const hasUi = await evalInPage(driver, async () => !!document.querySelector('#app') && document.title.length > 0)
  check('floccus options UI mounted', hasUi)

  const roots = await getNativeRoots(driver)
  report.bookmarkRoots = roots
  check('Firefox exposes native bookmark roots', roots.length >= 3, JSON.stringify(roots))

  // Real browser.bookmarks CRUD in a sandbox folder (own folder, removed at the end).
  const crud = await evalInPage(driver, async () => {
    const out = {}
    const sandbox = await browser.bookmarks.create({ parentId: 'unfiled_____', title: 'bookmarksync-harness-sandbox' })
    const a = await browser.bookmarks.create({ parentId: sandbox.id, title: 'A', url: 'https://example.com/a' })
    const sub = await browser.bookmarks.create({ parentId: sandbox.id, title: 'sub' })
    out.created = (await browser.bookmarks.get(a.id))[0].url === 'https://example.com/a'
    await browser.bookmarks.move(a.id, { parentId: sub.id })
    out.moved = (await browser.bookmarks.get(a.id))[0].parentId === sub.id
    await browser.bookmarks.update(a.id, { title: 'A2' })
    out.updated = (await browser.bookmarks.get(a.id))[0].title === 'A2'
    out.found = (await browser.bookmarks.search({ url: 'https://example.com/a' })).length === 1
    await browser.bookmarks.remove(a.id)
    out.removed = (await browser.bookmarks.search({ url: 'https://example.com/a' })).length === 0
    await browser.bookmarks.removeTree(sandbox.id)
    out.sandboxGone = await browser.bookmarks.get(sandbox.id).then(() => false, () => true)
    return out
  })
  for (const [k, v] of Object.entries(crud)) check(`browser.bookmarks ${k}`, v)

  if (!process.argv.includes('--no-rpc')) {
    const { srv, requests, port } = await recorderServer()
    try {
      const folderId = await createSandboxFolder(driver, 'bookmarksync-harness-rpc')
      const accountId = await importAccount(driver, {
        type: 'nextcloud-bookmarks', url: `http://127.0.0.1:${port}/`, username: 'harness',
        password: 'harness-placeholder-not-a-secret', // fixed placeholder only; no environment secrets are read
        serverRoot: '', localRoot: folderId, strategy: 'slave', failsafe: true,
        syncIntervalEnabled: false, enabled: true, includeCredentials: false, allowRedirects: false, label: 'harness-recorder'
      })
      const res = await syncAccount(driver, accountId, { strategy: 'slave', force: true, timeoutMs: 45000 })
      check('syncAccount RPC completed (syncAccountResponse message)', res.done, res.reason)
      check('real floccus issued its first request to the recorder (capabilities GET)',
        requests[0] && requests[0].startsWith('GET /ocs/v2.php/cloud/capabilities'), requests.slice(0, 3).join(' | '))
      check('floccus recorded an error on the account (recorder returns 500)', !!res.status?.error, String(res.status?.error).slice(0, 80))
      report.rpc = {
        requests: requests.slice(0, 5),
        accountError: String(res.status?.error).slice(0, 120),
        note: 'recorder returns HTTP 500; plumbing only, NOT a protocol test',
        subtree: await readSubtree(driver, folderId)
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
      report.profileCleaned = await closeAndCleanFirefox(session)
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
  const file = writeResult('smoke-firefox', report)
  console.log(`\nRESULT firefox: ${report.status}${report.reason ? ' (' + report.reason + ')' : ''}${report.error && report.status === 'FAIL' ? ' - ' + report.error : ''}`)
  console.log(`profile cleaned: ${report.profileCleaned}   result file: ${file}`)
  if (report.profileRetained) console.error(`CLEANUP FAILED: profile retained at ${report.profileRetained} - ${report.cleanupError}`)
  process.exit(exitCode)
}
