// One uniform session over the four real browsers, built only from the existing harness
// launchers (Chromium family: harness.mjs; Firefox: firefox-harness.mjs). Everything runs
// inside the genuine, unmodified floccus 5.13.0 extension in a disposable profile.
import * as H from './harness.mjs'
import * as CRPC from './floccus-rpc.mjs'
import * as FH from './firefox-harness.mjs'
import * as FRPC from './firefox-rpc.mjs'

export const BROWSERS = ['chrome', 'edge', 'brave', 'firefox']

/** null when the browser can run, else the exact NOT_RUN reason. */
export function prerequisite(name) {
  if (name === 'firefox') {
    if (FH.resolveFirefox().missing) return 'Firefox executable not found'
    try { FH.verifyToolchain(); FH.loadSelenium() } catch (e) { return String(e.message).split('\n')[0] }
    return null
  }
  if (H.resolveBrowser(name).missing) return `${name} executable not found`
  try { H.verifyExtensionDir() } catch (e) { return e.message }
  try { H.loadPlaywright() } catch (e) { return e.message }
  return null
}

// ---- in-page functions (serialised into the extension; must be self-contained) --------

const fnRoots = async () => {
  const api = typeof browser !== 'undefined' ? browser : chrome
  const [t] = await api.bookmarks.getTree()
  return { rootId: t.id, children: t.children.map(c => ({ id: c.id, title: c.title, folderType: c.folderType ?? null })) }
}
const fnBuild = async (a) => {
  const api = typeof browser !== 'undefined' ? browser : chrome
  const make = async (pid, items) => {
    for (const it of items) {
      const n = await api.bookmarks.create(it.children ? { parentId: pid, title: it.title } : { parentId: pid, title: it.title, url: it.url })
      if (it.children) await make(n.id, it.children)
    }
  }
  await make(a.parentId, a.spec)
  return true
}
const fnSub = async (id) => {
  const api = typeof browser !== 'undefined' ? browser : chrome
  const strip = n => ({ id: n.id, title: n.title, url: n.url, children: n.children?.map(strip) })
  const [t] = await api.bookmarks.getSubTree(id)
  return strip(t)
}
// Walk titles from a folder id; returns the id of the node reached or null.
const fnFind = async (a) => {
  const api = typeof browser !== 'undefined' ? browser : chrome
  let [cur] = await api.bookmarks.getSubTree(a.from)
  for (const t of a.path) {
    cur = (cur.children || []).find(c => c.title === t)
    if (!cur) return null
  }
  return cur.id
}
const fnMove = async (a) => { const api = typeof browser !== 'undefined' ? browser : chrome; await api.bookmarks.move(a.id, a.dest); return true }
const fnUpdate = async (a) => { const api = typeof browser !== 'undefined' ? browser : chrome; await api.bookmarks.update(a.id, a.changes); return true }
const fnRemove = async (id) => { const api = typeof browser !== 'undefined' ? browser : chrome; await api.bookmarks.removeTree(id); return true }
const fnCreateFolder = async (a) => { const api = typeof browser !== 'undefined' ? browser : chrome; return (await api.bookmarks.create({ parentId: a.parentId, title: a.title })).id }

/** The real launchers; tests inject fakes through openBrowser's `deps`. */
export const realDeps = {
  launchGecko: o => FH.launchFirefoxWithFloccus(o),
  launchChromium: o => H.launchWithExtension(o),
  openGeckoPage: (driver, uuid) => FRPC.openExtensionPage(driver, uuid),
  openChromiumPage: (ctx, id) => CRPC.openExtensionPage(ctx, id),
  closeGecko: s => FH.closeAndCleanFirefox(s),
  closeChromium: s => H.closeAndClean(s)
}

/** Open a browser with stock floccus in a disposable profile. */
/** Init failed after a launch: close the owned session (retention rules apply) before rethrowing. */
async function abortInit(err, session, close) {
  try { await close(session); err.profileCleaned = true } catch (ce) {
    err.profileCleaned = false
    err.profileRetained = ce.profileRetained ?? session.profileDir
    err.cleanupError = ce.message
  }
  throw err
}

export async function openBrowser(name, { headless = true, deps = realDeps } = {}) {
  if (name === 'firefox') {
    const s = await deps.launchGecko({ headless })
    let info
    try { info = await deps.openGeckoPage(s.driver, s.extensionUuid) } catch (e) { return abortInit(e, s, deps.closeGecko) }
    const run = (fn, arg) => FRPC.evalInPage(s.driver, fn, arg)
    return wrap({
      name, family: 'gecko', version: s.browserVersion, extensionVersion: info.version, profileDir: s.profileDir,
      run, raw: s,
      importAccount: data => FRPC.importAccount(s.driver, data),
      syncRpc: (id, o) => FRPC.syncAccount(s.driver, id, o),
      statusRpc: id => FRPC.getAccountStatus(s.driver, id),
      close: () => deps.closeGecko(s)
    })
  }
  const s = await deps.launchChromium({ browser: name, headless })
  const run = (fn, arg) => s.serviceWorker.evaluate(fn, arg)
  let page, extensionVersion
  try {
    page = await deps.openChromiumPage(s.pageContext, s.extensionId)
    extensionVersion = await run(() => chrome.runtime.getManifest().version)
  } catch (e) { return abortInit(e, s, deps.closeChromium) }
  return wrap({
    name, family: 'chromium', version: s.browserVersion, extensionVersion, profileDir: s.profileDir, run, raw: s,
    importAccount: data => CRPC.importAccount(page, data),
    syncRpc: (id, o) => CRPC.syncAccount(page, id, o),
    statusRpc: id => CRPC.getAccountStatus(page, id),
    close: () => deps.closeChromium(s)
  })
}

function wrap(b) {
  const gecko = b.family === 'gecko'
  const native = {
    roots: () => b.run(fnRoots),
    build: (parentId, spec) => b.run(fnBuild, { parentId, spec }),
    sub: id => b.run(fnSub, id),
    find: (from, path) => b.run(fnFind, { from, path }),
    move: (id, dest) => b.run(fnMove, { id, dest }),
    update: (id, changes) => b.run(fnUpdate, { id, changes }),
    remove: id => b.run(fnRemove, id),
    createFolder: (parentId, title) => b.run(fnCreateFolder, { parentId, title })
  }
  return {
    ...b, native,
    /** Real native root ids: the absolute root plus the bar/other folders as this browser reports them. */
    async roots() {
      const r = await native.roots()
      const bar = gecko ? r.children.find(c => c.id === 'toolbar_____') : r.children.find(c => c.folderType === 'bookmarks-bar')
      const other = gecko ? r.children.find(c => c.id === 'unfiled_____') : r.children.find(c => c.folderType === 'other')
      if (!bar || !other) throw new Error(`cannot identify native roots: ${JSON.stringify(r.children)}`)
      return { absoluteRootId: r.rootId, barId: bar.id, otherId: other.id, barTitle: bar.title, otherTitle: other.title, all: r.children }
    },
    importAccount: b.importAccount,
    status: b.statusRpc,
    sync: (id, { strategy, force = true, timeoutMs = 90000 } = {}) => b.syncRpc(id, { strategy, force, timeoutMs }),
    close: b.close
  }
}
