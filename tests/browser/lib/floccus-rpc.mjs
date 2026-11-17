// Helpers that drive the REAL floccus extension code. Nothing here replaces a
// browser adapter or edits extension source:
//  - account import writes the same `accounts` storage entry that
//    BrowserAccountStorage.setAccountData(data, null) writes (JSON string map);
//  - sync is triggered with the same service-worker message Controller.syncAccount()
//    posts to the background (`{type:'syncAccount', params:[id, strategy, force]}`)
//    and completes on the background's `syncAccountResponse` event.
// Secrets (app token) are accepted as arguments only and never logged or returned.
import { withTimeout } from './harness.mjs'

export const EXTENSION_PAGE = 'dist/html/options.html'

export async function openExtensionPage(context, extensionId, page = EXTENSION_PAGE) {
  const p = await context.newPage()
  await p.goto(`chrome-extension://${extensionId}/${page}`, { waitUntil: 'domcontentloaded' })
  return p
}

/**
 * Import one floccus account (IAccountData as in floccus.export.json) into the
 * profile. `localRoot` must be an existing bookmark folder id. Returns the new account id.
 */
export async function importAccount(page, accountData) {
  return page.evaluate(async (data) => {
    const id = '' + Date.now() + Math.random() // same scheme as BrowserAccount.create
    const { accounts } = await chrome.storage.local.get('accounts')
    let map = accounts
    while (typeof map === 'string') map = JSON.parse(map)
    map = map || {}
    map[id] = data
    await chrome.storage.local.set({ accounts: JSON.stringify(map) })
    return id
  }, accountData)
}

/** Redacted view of a stored account: never returns password/passphrase. */
export async function getAccountStatus(page, accountId) {
  return page.evaluate(async (id) => {
    const { accounts } = await chrome.storage.local.get('accounts')
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
 */
export async function syncAccount(page, accountId, { strategy, force = true, timeoutMs = 60000 } = {}) {
  // Same transport as Controller.getWorker() on Chromium: the background is an MV3
  // service worker whose BrowserController listens for `message` events; replies go
  // back to the posting client (event.source.postMessage).
  const outcome = await withTimeout(page.evaluate(({ accountId, strategy, force, timeoutMs }) => new Promise(async (resolve) => {
    const sw = navigator.serviceWorker
    const timer = setTimeout(() => {
      sw.removeEventListener('message', listener)
      resolve({ done: false, reason: `no syncAccountResponse within ${timeoutMs}ms` })
    }, timeoutMs)
    const listener = (event) => {
      if (event.data && event.data.type === 'syncAccountResponse') {
        clearTimeout(timer)
        sw.removeEventListener('message', listener)
        resolve({ done: true })
      }
    }
    sw.addEventListener('message', listener)
    const registration = await sw.ready
    registration.active.postMessage({ type: 'syncAccount', params: [accountId, strategy, force] })
  }), { accountId, strategy, force, timeoutMs }), timeoutMs + 5000, 'syncAccount page.evaluate')
  const status = await getAccountStatus(page, accountId)
  return { ...outcome, status }
}

/** Create a sandbox folder via the real chrome.bookmarks API; returns its id. */
export async function createSandboxFolder(sw, title, parentId) {
  return sw.evaluate(async ({ title, parentId }) => {
    const [root] = await chrome.bookmarks.getTree()
    const other = parentId || root.children.find(c => c.folderType === 'other' || /other/i.test(c.title))?.id || root.children[1].id
    return (await chrome.bookmarks.create({ parentId: other, title })).id
  }, { title, parentId })
}

export async function readSubtree(sw, folderId) {
  return sw.evaluate(async (id) => {
    const strip = n => ({ title: n.title, url: n.url, children: n.children?.map(strip) })
    const [t] = await chrome.bookmarks.getSubTree(id)
    return strip(t)
  }, folderId)
}

export async function removeSandboxFolder(sw, folderId) {
  await sw.evaluate((id) => chrome.bookmarks.removeTree(id), folderId)
}
