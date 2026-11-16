// Helpers that drive the REAL floccus code inside native Firefox through WebDriver.
// Nothing here replaces a browser adapter or edits extension source:
//  - everything runs inside a genuine moz-extension:// page of the installed floccus add-on;
//  - account import writes the same `accounts` storage entry that
//    BrowserAccountStorage.setAccountData(data, null) writes (JSON string map);
//  - sync is triggered with the same runtime message Controller.syncAccount() posts on
//    Firefox (`browser.runtime.sendMessage({type:'syncAccount', params:[id, strategy, force]})`)
//    and completes on the background's `syncAccountResponse` message (runtime.onMessage).
// Secrets (app token) are accepted as arguments only and never logged or returned.
// Helpers take a selenium `driver`, so later server-sync and cross-vendor tests can reuse them.
import { withTimeout } from './harness.mjs'

export const EXTENSION_PAGE = 'dist/html/options.html'

/**
 * Run an async function inside the current page and return its result.
 * `fn` must be self-contained (it is serialised); args must be JSON-serialisable.
 */
export async function evalInPage(driver, fn, ...args) {
  const res = await driver.executeAsyncScript(
    `const done = arguments[arguments.length - 1];
     const args = Array.prototype.slice.call(arguments, 0, -1);
     Promise.resolve().then(() => (${fn.toString()}).apply(null, args))
       .then(v => done({ ok: v === undefined ? null : v }), e => done({ err: String((e && e.message) || e) }))`,
    ...args)
  if (res && 'err' in res) throw new Error(`page script failed: ${res.err}`)
  return res.ok
}

export async function openExtensionPage(driver, extensionUuid, page = EXTENSION_PAGE) {
  await driver.get(`moz-extension://${extensionUuid}/${page}`)
  return evalInPage(driver, async () => ({
    href: location.href, id: browser.runtime.id, version: browser.runtime.getManifest().version,
    name: browser.runtime.getManifest().name
  }))
}

/** Native root folders as Firefox reports them (records the real roots, ids and titles). */
export function getNativeRoots(driver) {
  return evalInPage(driver, async () =>
    (await browser.bookmarks.getTree())[0].children.map(c => ({ id: c.id, title: c.title, type: c.type ?? null })))
}

/** Create a sandbox folder via the real browser.bookmarks API; defaults to Other Bookmarks (`unfiled_____`). */
export function createSandboxFolder(driver, title, parentId = 'unfiled_____') {
  return evalInPage(driver, async (title, parentId) => (await browser.bookmarks.create({ parentId, title })).id, title, parentId)
}

export function readSubtree(driver, folderId) {
  return evalInPage(driver, async (id) => {
    const strip = n => ({ title: n.title, url: n.url, children: n.children?.map(strip) })
    const [t] = await browser.bookmarks.getSubTree(id)
    return strip(t)
  }, folderId)
}

export function removeSandboxFolder(driver, folderId) {
  return evalInPage(driver, async (id) => { await browser.bookmarks.removeTree(id) }, folderId)
}

/**
 * Import one floccus account (IAccountData as in floccus.export.json) into the profile.
 * `localRoot` must be an existing bookmark folder id. Returns the new account id.
 */
export function importAccount(driver, accountData) {
  return evalInPage(driver, async (data) => {
    const id = '' + Date.now() + Math.random() // same scheme as BrowserAccount.create
    const { accounts } = await browser.storage.local.get('accounts')
    let map = accounts
    while (typeof map === 'string') map = JSON.parse(map)
    map = map || {}
    map[id] = data
    await browser.storage.local.set({ accounts: JSON.stringify(map) })
    return id
  }, accountData)
}

/** Redacted view of a stored account: never returns password/passphrase/refreshToken. */
export function getAccountStatus(driver, accountId) {
  return evalInPage(driver, async (id) => {
    const { accounts } = await browser.storage.local.get('accounts')
    let map = accounts
    while (typeof map === 'string') map = JSON.parse(map)
    const a = (map || {})[id]
    if (!a) return null
    const { password, passphrase, refreshToken, ...rest } = a
    return rest
  }, accountId)
}

/**
 * Trigger a sync through the background RPC and wait (event-based, bounded) for
 * `syncAccountResponse`. strategy: undefined|'default'|'slave'|'overwrite'|'merge'.
 * The listener is registered before the message is sent, as Controller.syncAccount does.
 */
export async function syncAccount(driver, accountId, { strategy, force = true, timeoutMs = 60000 } = {}) {
  // WebDriver's async-script timeout must outlive the in-page wait (+5 s) so the page-side timer fires first.
  await driver.manage().setTimeouts({ script: timeoutMs + 5000 })
  const outcome = await withTimeout(evalInPage(driver, (accountId, strategy, force, timeoutMs) => new Promise((resolve) => {
    const timer = setTimeout(() => {
      browser.runtime.onMessage.removeListener(listener)
      resolve({ done: false, reason: `no syncAccountResponse within ${timeoutMs}ms` })
    }, timeoutMs)
    const listener = (data) => {
      if (data && data.type === 'syncAccountResponse') {
        clearTimeout(timer)
        browser.runtime.onMessage.removeListener(listener)
        resolve({ done: true })
      }
    }
    browser.runtime.onMessage.addListener(listener)
    browser.runtime.sendMessage({ type: 'syncAccount', params: [accountId, strategy, force] })
  }), accountId, strategy ?? null, force, timeoutMs), timeoutMs + 5000, 'syncAccount page script')
  const status = await getAccountStatus(driver, accountId)
  return { ...outcome, status }
}
