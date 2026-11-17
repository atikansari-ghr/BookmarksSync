// Real browser + real server compatibility checks (replaces the old PENDING placeholder).
//
//   node server-sync.mjs [--browser=chrome,edge,brave,firefox] [--headed]
//
// For each browser this builds the real BookmarksSync server (once), starts it on a random
// loopback port with a fresh data directory, provisions disposable users and credentials with the
// real CLI (passwords only on child stdin, app secrets only in this process' memory), launches the
// stock floccus 5.13.0 extension in disposable profiles, and drives the genuine syncAccount RPC
// and the native bookmarks API. It then checks the server's request log, its API view and the
// actual browser trees, and finally stops the server and deletes only harness-owned paths.
//
// Exit codes: 0 every case PASS, 1 any FAIL (or cleanup failure), 2 NOT_RUN (a prerequisite is
// missing; never a pass), 3 BLOCKED. Nothing here is a stub: a sync case passes only with real
// server requests and a check of the real native tree.
import os from 'node:os'
import fs from 'node:fs'
import path from 'node:path'
import crypto from 'node:crypto'
import { REPO_ROOT, EXTENSION_DIR, EXPECTED_EXTENSION_VERSION, writeResult, TOOLS_DIR } from './lib/harness.mjs'

import {
  sanitizeDeep, sanitize, parseRequestLog, summarizeRequests, makeRunRoot, removeRunRoot, runCleanupSteps, Recorder,
  parseSecretFromCliStdout, assessSync, guardedRunRemoval, detectIdNamespaceCollision, startRecordingProxy, stripTree, pickLoopbackPort, findGo, startProcessServer, runCommand, treeEquals, apiClient, serverTree,
  dirContains, REST
} from './lib/server-sync.mjs'
import { BROWSERS, prerequisite, openBrowser } from './lib/browser-session.mjs'

const arg = (n, d) => (process.argv.find(a => a.startsWith(`--${n}=`)) || '').split('=')[1] ?? d
const wanted = arg('browser', BROWSERS.join(',')).split(',').filter(Boolean)
const headless = !process.argv.includes('--headed')
const BUMP = Number(arg('bump-ids', 0)) // experiment: push the server's folder/bookmark ids far away from the browser's small native ids
const TRACE = process.argv.includes('--trace') // route floccus through a recording proxy and record native bookmark events
const secrets = []
const liveServers = [] // handles { pid, exited() } of every server this run started
const rec = new Recorder({ secrets })
const report = {
  harness: 'server-sync', startedAt: new Date().toISOString(), node: process.version, os: `${os.type()} ${os.release()}`,
  browsers: {}, extension: {}, cleanup: {}, cases: rec.cases
}
const addSecret = s => { secrets.push(s); rec.addSecret(s) }
const sha256File = f => crypto.createHash('sha256').update(fs.readFileSync(f)).digest('hex')

const BAR_SPEC = [
  { title: 'Alpha', url: 'https://alpha.example/' },
  { title: 'Beta', url: 'https://beta.example/' },
  { title: 'Folder F', children: [{ title: 'G1', url: 'https://g1.example/' }, { title: 'G2', url: 'https://g2.example/' }] },
  { title: 'Zed', url: 'https://zed.example/' },
  { title: 'Dup', url: 'https://dup.example/' }
]
const OTHER_SPEC = [{ title: 'O1', url: 'https://o1.example/' }, { title: 'Sub', children: [{ title: 'O2', url: 'https://o2.example/' }] }]
const X = 'https://collide.example/x'

const accountData = ({ url, username, secret, localRoot, strategy = 'default', label }) => ({
  type: 'nextcloud-bookmarks', url: `${url}/`, username, password: secret, serverRoot: '', localRoot, strategy,
  failsafe: true, syncIntervalEnabled: false, enabled: true, includeCredentials: false, allowRedirects: false,
  nestedSync: false, label
})
const settleLog = async (requests, quietMs = 600, maxMs = 5000) => {
  const end = Date.now() + maxMs
  let n = requests().length, since = Date.now()
  while (Date.now() < end) {
    await new Promise(r => setTimeout(r, 100))
    if (requests().length !== n) { n = requests().length; since = Date.now() } else if (Date.now() - since >= quietMs) return
  }
}
const credId = secret => Number(secret.split('.')[1])
const synced = res => assessSync({ res, entries: res.evidence?.entries ?? [], expect: 'success' }).ok
const errText = res => String(res.status?.error ?? res.reason ?? (res.evidence?.entries?.length === 0 ? 'no request from this credential in the sync window (RPC returned without syncing)' : '')).slice(0, 160)
const at = (nodes, titles) => titles.reduce((cur, t) => (cur?.children ?? cur ?? []).find?.(n => n.title === t) ?? null, nodes)

async function buildServer(go, outFile) {
  const r = await runCommand({
    command: go, args: ['build', '-trimpath', '-o', outFile, './cmd/bookmarksync'], cwd: REPO_ROOT, timeoutMs: 300000,
    env: { CGO_ENABLED: '0', GOCACHE: path.join(TOOLS_DIR, 'go-cache') }
  })
  if (r.code !== 0) throw new Error(`go build failed: ${sanitize(r.stderr.slice(-400), secrets)}`)
}

async function runBrowser(name, ctx) {
  const pre = prerequisite(name)
  if (pre) { rec.notRun(`${name}: server sync (all cases)`, pre); report.browsers[name] = { status: 'NOT_RUN', reason: pre }; return }
  const tag = c => `${name}: ${c}`
  const dataDir = path.join(ctx.runDir, name, 'data')
  fs.mkdirSync(dataDir, { recursive: true })
  const cleanups = []
  const sessions = []
  let server
  const info = report.browsers[name] = { status: 'RUNNING' }
  try {
    // ---- server + disposable accounts (real CLI) -------------------------------------
    const port = await pickLoopbackPort()
    const base = `http://127.0.0.1:${port}`
    const cli = (args, stdin) => runCommand({ command: ctx.binary, args: [...args, '--data-dir', dataDir], stdin, timeoutMs: 60000 })
    const must = async (args, stdin) => {
      const r = await cli(args, stdin)
      if (r.code !== 0) throw new Error(`cli ${args.slice(0, 2).join(' ')} failed (${r.code}): ${sanitize(r.stderr.slice(-300), secrets)}`)
      return r
    }
    const mkCred = async (username, mode, extra = []) => {
      const r = await must(['credential', 'create', '--username', username, '--mode', mode, '--label', 'harness', ...extra])
      const s = parseSecretFromCliStdout(r.stdout)
      addSecret(s)
      return s
    }
    const password = crypto.randomBytes(18).toString('base64url') // local, never stored or used for floccus
    addSecret(password)
    for (const u of ['alice', 'bob']) await must(['user', 'create', '--username', u, '--password-stdin'], password + '\n')
    server = await startProcessServer({
      command: ctx.binary, args: ['serve', '--listen', `127.0.0.1:${port}`, '--data-dir', dataDir],
      readyUrl: `${base}/readyz`, startupTimeoutMs: 30000, secrets
    })
    liveServers.push(server)
    cleanups.push(['stop server', () => server.stop()])
    info.serverPid = server.pid; info.port = port
    const proxy = TRACE ? await startRecordingProxy({ targetPort: port }) : null
    if (proxy) cleanups.push(['stop recording proxy', () => proxy.stop()])
    const acctUrl = proxy ? proxy.url : base
    const nativeRecorder = async () => {
      const api = typeof browser !== 'undefined' ? browser : chrome
      self.__bh = []
      api.bookmarks.onCreated.addListener((id, n) => self.__bh.push(`created ${n.title ?? ''} parent=${n.parentId} index=${n.index}`))
      api.bookmarks.onMoved.addListener((id, m) => self.__bh.push(`moved ${id} parent=${m.parentId} index=${m.index} (was ${m.oldParentId}/${m.oldIndex})`))
      return true
    }

    const secA1 = await mkCred('alice', 'edit'), secA2 = await mkCred('alice', 'edit'), secA3 = await mkCred('alice', 'edit')
    const secView = await mkCred('alice', 'view')
    const alice = apiClient(base, 'alice', secA1)
    const requests = () => parseRequestLog(server.logs())
    // floccus also starts its own syncs on local bookmark changes, and its syncAccount() returns at once
    // when the account is already syncing. So: wait for idle, run the RPC, wait for idle and for the log to
    // settle, and if floccus did nothing at all (zero requests from this credential) repeat the RPC.
    const acctCred = new Map()
    info.rpcRetries = 0
    const importAcct = async (S, data) => { const id = await S.importAccount(data); acctCred.set(id, credId(data.password)); return id }
    const idle = async (S, id, ms = 60000) => {
      const end = Date.now() + ms
      while (Date.now() < end) { const st = await S.status(id); if (!st?.syncing) return true; await new Promise(r => setTimeout(r, 300)) }
      return false
    }
    // Returns the RPC result plus r.evidence.entries = this credential's requests in the (last) attempt's window.
    // Exhausted retries do NOT pass silently: the empty evidence makes every verdict fail.
    const doSync = async (S, id, o) => {
      let r, entries = []
      for (let attempt = 0; attempt < 3; attempt++) {
        await idle(S, id)
        const m = requests().length
        r = await S.sync(id, o)
        await idle(S, id)
        await settleLog(requests)
        entries = summarizeRequests(requests().slice(m), acctCred.get(id)).entries
        if (entries.length > 0) break
        info.rpcRetries++
      }
      r.evidence = { entries }
      return r
    }

    if (BUMP > 0) {
      const top0 = (await alice.get('/folder/-1/children?layers=1')).json.data
      const other0 = top0.find(n => n.title === 'Other Bookmarks').id
      for (let i = 0; i < BUMP; i++) {
        const f = (await alice.post('/folder', { parent_folder: other0, title: 'bump' + i })).json.item.id
        await alice.post('/bookmark', { url: 'https://bump.example/' + i, title: 'b', folders: [f] })
        await alice.del('/folder/' + f)
      }
      info.idBump = BUMP
    }

    // ---- case 0: capabilities and auth over the wire (plain HTTP, not a sync) ---------
    {
      const noAuth = await fetch(`${base}/index.php/ocs/v2.php/cloud/capabilities`, { redirect: 'manual' })
      const auth = await fetch(`${base}/index.php/ocs/v2.php/cloud/capabilities`, {
        redirect: 'manual', headers: { authorization: 'Basic ' + Buffer.from(`alice:${secA1}`).toString('base64') }
      })
      const caps = (await auth.json()).ocs?.data?.capabilities?.bookmarks
      if (noAuth.status === 401 && auth.status === 200 && caps?.['javascript-bookmarks'] === true && JSON.stringify(caps['hash-functions']) === '["sha256"]') rec.pass(tag('server up; capabilities require auth and advertise javascript-bookmarks + sha256'))
      else rec.fail(tag('server capabilities'), `noAuth=${noAuth.status} auth=${auth.status} caps=${JSON.stringify(caps)}`)
    }

    // ---- profiles ---------------------------------------------------------------------
    const open = async label => {
      const s = await openBrowser(name, { headless })
      sessions.push(s)
      cleanups.unshift([`close ${label}`, async () => { await s.close(); info[`${label}Cleaned`] = true }])
      return s
    }
    const A = await open('profileA')
    info.version = A.version; info.extensionVersion = A.extensionVersion; info.family = A.family
    if (A.extensionVersion !== EXPECTED_EXTENSION_VERSION) throw new Error(`extension version ${A.extensionVersion}`)
    const Ar = await A.roots()
    info.nativeRoots = Ar.all.map(c => ({ id: c.id, title: c.title, folderType: c.folderType }))

    // ---- case 1: native sandbox -> server -> second isolated profile ------------------
    const barSb = await A.native.createFolder(Ar.barId, 'bs-sandbox')
    await A.native.build(barSb, BAR_SPEC)
    const otherSb = await A.native.createFolder(Ar.otherId, 'bs-sandbox-other')
    await A.native.build(otherSb, OTHER_SPEC)
    const acctA = await importAcct(A, accountData({ url: acctUrl, username: 'alice', secret: secA1, localRoot: Ar.absoluteRootId, label: 'harness-A' }))
    const credA = credId(secA1)
    const before1 = requests().length
    const r1 = await doSync(A, acctA, { strategy: 'default' })
    const srvTop = (await alice.get('/folder/-1/children?layers=-1')).json?.data ?? []
    const sandboxOnServer = at(srvTop, ['Bookmarks Bar', 'bs-sandbox'])
    const otherOnServer = at(srvTop, ['Other Bookmarks', 'bs-sandbox-other'])
    const reqA = summarizeRequests(requests().slice(before1), credA)
    info.firstSyncRequests = reqA.entries.map(e => `${e.method} ${e.path.replace(REST, '')} ${e.status}`).slice(0, 60)
    if (synced(r1)) rec.pass(tag('profile A syncAccount (default) completed without a floccus error'))
    else rec.fail(tag('profile A syncAccount (default)'), errText(r1))
    {
      const titles = srvTop.map(n => n.title)
      const okRoots = titles.includes('Bookmarks Bar') && titles.includes('Other Bookmarks') &&
        !titles.some(t => /^(bookmarks bar|other bookmarks)$/.test(t) && t !== 'Bookmarks Bar' && t !== 'Other Bookmarks')
      if (okRoots) rec.pass(tag('both canonical roots on the server, no duplicate localized root'), JSON.stringify(titles))
      else rec.fail(tag('canonical roots on the server'), JSON.stringify(titles))
    }
    const treeOk = sandboxOnServer && otherOnServer &&
      JSON.stringify(serverTree(sandboxOnServer.children ?? [])) === JSON.stringify(BAR_SPEC.map(function norm(n) { return n.children ? { title: n.title, children: n.children.map(norm) } : n })) &&
      JSON.stringify(serverTree(otherOnServer.children ?? [])) === JSON.stringify(OTHER_SPEC.map(function norm(n) { return n.children ? { title: n.title, children: n.children.map(norm) } : n }))
    if (treeOk) rec.passSync(tag('A -> server: bar and other sandbox trees and order match'), { serverRequests: reqA.total, nativeTree: await A.native.sub(barSb) })
    else rec.fail(tag('A -> server tree/order'), JSON.stringify(serverTree(srvTop)).slice(0, 400))
    {
      const seen = new Set(reqA.entries.map(e => `${e.method} ${e.path.replace(REST, '').replace(/\/-?\d+/g, '/N').split('?')[0]}`))
      const need = ['GET /folder/N/children', 'POST /lock', 'DELETE /lock']
      const missing = need.filter(n => !seen.has(n))
      const caps = requests().slice(before1).some(e => e.credential === credA && e.path.endsWith('/capabilities') && e.status === 200)
      const orderCalls = [...seen].some(s => s.startsWith('PATCH /folder/N/childorder'))
      if (!missing.length && caps && orderCalls) rec.pass(tag('request log: capabilities, lock/unlock, folder reads and childorder PATCH'), [...seen].sort().join('; ').slice(0, 300))
      else rec.fail(tag('request log shape'), `missing=${missing.join(',')} caps=${caps} childorder=${orderCalls} seen=${[...seen].join('; ').slice(0, 300)}`)
    }

    const B = await open('profileB')
    const Br = await B.roots()
    const acctB = await importAcct(B, accountData({ url: acctUrl, username: 'alice', secret: secA2, localRoot: Br.absoluteRootId, label: 'harness-B' }))
    const credB = credId(secA2)
    const bBefore = requests().length
    if (TRACE) await B.run(nativeRecorder)
    const pBefore = proxy ? proxy.records().length : 0
    const r2 = await doSync(B, acctB, { strategy: 'default' })
    const bSbBar = await B.native.find(Br.barId, ['bs-sandbox'])
    const bSbOther = await B.native.find(Br.otherId, ['bs-sandbox-other'])
    const reqB = summarizeRequests(requests().slice(bBefore), credB)
    const bTreeBar = bSbBar && await B.native.sub(bSbBar)
    const bTreeOther = bSbOther && await B.native.sub(bSbOther)
    const aTreeBar = await A.native.sub(barSb), aTreeOther = await A.native.sub(otherSb)
    if (synced(r2) && bTreeBar && bTreeOther && treeEquals(aTreeBar, bTreeBar) && treeEquals(aTreeOther, bTreeOther)) {
      rec.passSync(tag('server -> second isolated profile B: identical native trees in both roots (order included)'), { serverRequests: reqB.total, nativeTree: bTreeBar })
    } else {
      // Diagnostics only (never changes the verdict): what did B ask for, what did the server hold, does a second B sync converge?
      const srvNow = (await alice.get('/folder/-1/children?layers=-1')).json?.data ?? []
      const traceB = proxy ? { proxyRequests: proxy.records().slice(pBefore).map(r => `${r.method} ${r.path.replace(REST, '')}${r.query} ${r.status}${r.method !== 'GET' ? ' body=' + r.requestBody.slice(0, 300) : ''}`), nativeEvents: await B.run(() => self.__bh), floccusLog: await B.run(async () => { const api = typeof browser !== 'undefined' ? browser : chrome; const all = await api.storage.local.get(null); const lines = []; for (const [k, v] of Object.entries(all)) { if (k === 'accounts') continue; let arr = null; try { arr = typeof v === 'string' ? JSON.parse(v) : null } catch {} const ls = Array.isArray(v) ? v.map(String) : Array.isArray(arr) ? arr.map(String) : String(typeof v === 'string' ? v : JSON.stringify(v)).split(String.fromCharCode(10)); for (const l of ls) if (/ORDERFOLDER|local.CREATE|REORDER|Executing reorder|local.MOVE|[Rr]eorders|ordering|Ordering/.test(l)) lines.push(k + ": " + l.slice(0, 3000)) } return lines.slice(-80) }) } : null
      const dg = info.orderDiag = {
        trace: traceB,
        knownIssue: traceB ? (() => { const h = detectIdNamespaceCollision(traceB.floccusLog); return h.some(x => x.status === 'CONFIRMED') ? 'floccus-5.13.0 ReconcileReorders id-namespace collision CONFIRMED (created item found in an unrelated folder order)' : h.length ? 'id-namespace collision CANDIDATE (same numeric ids; contamination not verified)' : 'collision signature not found in the extension log' })() : 'run with --trace to capture the extension log',
        localSandboxOtherId: String(bSbOther), serverFolderFId: String(at(srvNow, ['Bookmarks Bar', 'bs-sandbox', 'Folder F'])?.id),
        bRequests: reqB.entries.map(e => `${e.method} ${e.path.replace(REST, '')} ${e.status}`),
        serverFolderFAtBTime: JSON.stringify(serverTree(at(srvNow, ['Bookmarks Bar', 'bs-sandbox', 'Folder F'])?.children ?? []))
      }
      const m = requests().length
      const rx = await doSync(B, acctB, { strategy: 'default' })
      await settleLog(requests)
      const bx = bSbBar && await B.native.sub(bSbBar)
      const sx = (await alice.get('/folder/-1/children?layers=-1')).json?.data ?? []
      dg.secondSyncError = errText(rx)
      dg.secondSyncRequests = summarizeRequests(requests().slice(m), credB).entries.map(e => `${e.method} ${e.path.replace(REST, '')} ${e.status}`)
      dg.bFolderFAfterSecondSync = JSON.stringify(stripTree(bx ?? {title:''}).children?.find(c => c.title === 'Folder F')?.children?.map(c => c.title))
      dg.serverFolderFAfterSecondSync = JSON.stringify(serverTree(at(sx, ['Bookmarks Bar', 'bs-sandbox', 'Folder F'])?.children ?? []))
      rec.fail(tag('server -> profile B roundtrip'), `${errText(r2)} A=${JSON.stringify(stripTree(aTreeBar))} B=${JSON.stringify(bTreeBar && stripTree(bTreeBar))} Aother=${JSON.stringify(stripTree(aTreeOther))} Bother=${JSON.stringify(bTreeOther && stripTree(bTreeOther))} diag=${JSON.stringify(info.orderDiag)}`)
    }
    if (reqB.entries.some(e => e.method !== 'GET' && !e.path.endsWith('/lock') && e.status < 300) === false) rec.pass(tag('profile B download wrote nothing to the server'), `${reqB.total} requests, writes=0`)
    else rec.fail(tag('profile B download wrote to the server'), reqB.writes.map(w => `${w.method} ${w.path}`).join('; ').slice(0, 200))

    // ---- case 2: order / move / rename / delete in B, then back into A ----------------
    {
      const f = async (...p) => B.native.find(Br.barId, ['bs-sandbox', ...p])
      const alphaId = await f('Alpha'), betaId = await f('Beta'), folderF = await f('Folder F'), zedId = await f('Zed'), dupId = await f('Dup')
      await B.native.update(alphaId, { title: 'Alpha2' })
      await B.native.move(betaId, { parentId: folderF, index: 1 })
      await B.native.remove(zedId)
      await B.native.move(dupId, { parentId: bSbBar, index: 0 })
      const shape = n => ({ top: (n.children ?? []).map(c => c.title), f: (n.children ?? []).find(c => c.title === 'Folder F')?.children?.map(c => c.title) ?? [], urls: JSON.stringify(stripTree(n)).includes('zed.example') })
      // Expected edits regardless of the G1/G2 order case 1 judges: Dup first, Alpha renamed, Zed gone, Beta inside Folder F at index 1.
      const editsOk = n => { const x = shape(n); return JSON.stringify(x.top) === JSON.stringify(['Dup', 'Alpha2', 'Folder F']) && x.f.length === 3 && x.f[1] === 'Beta' && !x.urls }
      const expected = null
      const m0 = requests().length
      const r3 = await doSync(B, acctB, { strategy: 'default' })
      const srv = (await alice.get('/folder/-1/children?layers=-1')).json?.data ?? []
      const got = serverTree(at(srv, ['Bookmarks Bar', 'bs-sandbox'])?.children ?? [])
      const reqs = summarizeRequests(requests().slice(m0), credB)
      const bNow = await B.native.sub(bSbBar)
      if (synced(r3) && editsOk({ children: got }) && JSON.stringify(got) === JSON.stringify(stripTree(bNow).children)) rec.passSync(tag('B edits (rename, move, delete, reorder) reach the server exactly'), { serverRequests: reqs.total, nativeTree: await B.native.sub(bSbBar) })
      else rec.fail(tag('B edits -> server'), `${errText(r3)} got=${JSON.stringify(got).slice(0, 300)} native=${JSON.stringify(stripTree(bNow).children).slice(0, 300)}`)
      const r4 = await doSync(A, acctA, { strategy: 'default' })
      const aNow = await A.native.sub(barSb)
      if (synced(r4) && treeEquals(aNow, bNow)) rec.passSync(tag('server -> profile A: edits arrive natively (delete, move, rename, order)'), { serverRequests: summarizeRequests(requests(), credA).total, nativeTree: aNow })
      else rec.fail(tag('server -> A applies edits'), `${errText(r4)} native=${JSON.stringify(aNow.children?.map(c => c.title))}`)
    }

    // ---- case 3: lock contention (another credential holds the owner lock) ------------
    {
      const holder = apiClient(base, 'alice', secA3)
      const lk = await holder.post('/lock')
      const mark = requests().length
      const bar = await A.native.find(Ar.barId, ['bs-sandbox'])
      await A.native.build(bar, [{ title: 'LockedOut', url: 'https://locked.example/' }])
      const nativeBefore = JSON.stringify(stripTree(await A.native.sub(bar))).includes('LockedOut')
      const r = await doSync(A, acctA, { strategy: 'default', timeoutMs: 60000 })
      await settleLog(requests) // the server logs a request after it answered: let late lines land
      const rawEntries = requests().slice(mark)
      const mine = summarizeRequests(rawEntries, credA)
      const locked = mine.entries.some(e => e.path.endsWith('/lock') && e.status === 423)
      const wroteWhileLocked = mine.writes.some(w => w.status < 300)
      const srvHas = JSON.stringify((await alice.get('/folder/-1/children?layers=-1')).json).includes('LockedOut')
      info.lockContention = { holderLock: lk.status, nativeHadItemBeforeSync: nativeBefore, floccusDone: r.done, floccusError: errText(r), accountStatus: r.status,
        rawLogSinceMark: rawEntries.map(e => `cred${e.credential} ${e.method} ${e.path.replace(REST, '')} ${e.status}`), statuses: mine.entries.map(e => `${e.method} ${e.path.replace(REST, '')} ${e.status}`).slice(0, 12) }
      if (lk.status === 200 && locked && !wroteWhileLocked && !srvHas) rec.pass(tag('lock contention: server answered 423, no write landed; floccus result recorded'), `floccus error="${errText(r)}"`)
      else rec.fail(tag('lock contention'), JSON.stringify(info.lockContention).slice(0, 300))
      await holder.del('/lock')
      const r5 = await doSync(A, acctA, { strategy: 'default' })
      const srvAfter = JSON.stringify((await alice.get('/folder/-1/children?layers=-1')).json).includes('LockedOut')
      if (synced(r5) && srvAfter) rec.passSync(tag('after the lock is released, the pending local change syncs'), { serverRequests: summarizeRequests(requests(), credA).total, nativeTree: await A.native.sub(bar) })
      else rec.fail(tag('sync after lock release'), errText(r5))
    }

    // ---- case 4: read-only credential --------------------------------------------------
    {
      const ro = await B.native.createFolder(Br.otherId, 'ro-sandbox')
      const acctRo = await importAcct(B, accountData({ url: acctUrl, username: 'alice', secret: secView, localRoot: ro, strategy: 'slave', label: 'harness-ro' }))
      const credRo = credId(secView)
      const snap = async () => JSON.stringify((await alice.get('/folder/-1/children?layers=-1')).json)
      const s0 = await snap(); const m0 = requests().length
      const rr = await doSync(B, acctRo, { strategy: 'slave' })
      const reqRo = summarizeRequests(requests().slice(m0), credRo)
      const roTree = await B.native.sub(ro)
      const s1 = await snap()
      const tops = (roTree.children ?? []).map(c => c.title)
      if (synced(rr) && reqRo.total > 0 && reqRo.onlyReadsAndLock && s0 === s1 && tops.includes('Bookmarks Bar')) {
        rec.passSync(tag('read-only credential, download-only (slave) sync: only GET + lock verbs, server unchanged, tree downloaded'), { serverRequests: reqRo.total, nativeTree: roTree })
      } else rec.fail(tag('read-only download-only sync'), `${errText(rr)} writes=${reqRo.writes.map(w => w.method + ' ' + w.path).join(';')} unchanged=${s0 === s1} tops=${tops}`)
      // misconfiguration: a local edit + a merge strategy on a view credential
      await B.native.build(ro, [{ title: 'LocalOnlyEdit', url: 'https://local-only.example/' }])
      const m1 = requests().length
      const rm = await doSync(B, acctRo, { strategy: 'default' })
      const reqM = summarizeRequests(requests().slice(m1), credRo)
      const s2 = await snap()
      info.readOnlyMisconfig = { floccusDone: rm.done, floccusError: errText(rm), statuses: reqM.entries.map(e => `${e.method} ${e.path.replace(REST, '')} ${e.status}`).slice(0, 12) }
      const verdictRo = assessSync({ res: rm, entries: rm.evidence.entries, expect: 'rejected403' })
      if (verdictRo.ok && s2 === s0 && !reqM.writes.some(w => w.status < 300)) rec.pass(tag('read-only credential with a local edit and merge strategy: server answered 403, nothing written, RPC finished'), `floccus error="${errText(rm)}"`)
      else rec.fail(tag('read-only misconfiguration: expected a real 403 rejection'), `${verdictRo.reasons.join('; ')}; unchanged=${s2 === s0} ${JSON.stringify(info.readOnlyMisconfig).slice(0, 240)}`)
    }

    // ---- case 5: cross-scope same-URL collision + lookup-first 409 ----------------------
    {
      const top = (await alice.get('/folder/-1/children?layers=1')).json.data
      const srvBar = at(top, ['Bookmarks Bar']).id, srvOther = at(top, ['Other Bookmarks']).id
      const S = (await alice.post('/folder', { parent_folder: srvBar, title: 'h-shared' })).json.item.id
      const F1 = (await alice.post('/folder', { parent_folder: srvOther, title: 'h-hidden' })).json.item.id
      const priv = (await alice.post('/bookmark', { url: X, title: 'ALICE-PRIVATE-TITLE', folders: [F1], tags: ['alice-secret-tag'] })).json.item
      await must(['share', 'grant', '--owner', 'alice', '--folder-id', String(S), '--user', 'bob', '--mode', 'edit'])
      const secBob = await mkCred('bob', 'edit', ['--share-owner', 'alice', '--share-folder', String(S)])
      const bob = apiClient(base, 'bob', secBob)
      const bobFolder = await B.native.createFolder(Br.otherId, 'bob-share')
      await B.native.build(bobFolder, [{ title: 'BOB-TITLE', url: X }])
      const acctBob = await importAcct(B, accountData({ url: acctUrl, username: 'bob', secret: secBob, localRoot: bobFolder, label: 'harness-bob' }))
      const rb = await doSync(B, acctBob, { strategy: 'default' })
      const aliceX = (await alice.get(`/bookmark?page=-1&url=${encodeURIComponent(X)}`)).json?.data ?? []
      const bobX = (await bob.get(`/bookmark?page=-1&url=${encodeURIComponent(X)}`)).json?.data ?? []
      const bobTree = await B.native.sub(bobFolder)
      const leaked = JSON.stringify(bobTree).includes('ALICE-PRIVATE') || JSON.stringify(bobX).includes('ALICE-PRIVATE') || JSON.stringify(bobX).includes('alice-secret-tag')
      const privAfter = (await alice.get(`/bookmark/${priv.id}`)).json?.item
      if (synced(rb) && aliceX.length === 2 && bobX.length === 1 && !leaked && privAfter?.title === 'ALICE-PRIVATE-TITLE') {
        rec.passSync(tag('cross-scope same URL: share recipient syncs a separate record; private title/tags never reach or change'), { serverRequests: summarizeRequests(requests(), credId(secBob)).total, nativeTree: bobTree })
      } else rec.fail(tag('cross-scope collision'), `${errText(rb)} aliceRecords=${aliceX.length} bobRecords=${bobX.length} leaked=${leaked} privTitle=${privAfter?.title}`)

      // lookup-first flow on the wire: owner sees two records, asks one into the other's folder -> 409
      const recA = aliceX.find(r => r.title === 'ALICE-PRIVATE-TITLE'), recB = aliceX.find(r => r.title === 'BOB-TITLE')
      if (recA && recB) {
        const put = await alice.put(`/bookmark/${recA.id}`, { folders: [...recA.folders, S] })
        const after = (await alice.get(`/bookmark?page=-1&url=${encodeURIComponent(X)}`)).json?.data ?? []
        if (put.status === 409 && after.length === 2 && after.every(r => r.folders.length === 1) && !JSON.stringify(put.json).includes('BOB-TITLE')) rec.pass(tag('documented lookup-first PUT into the other record\'s folder: explicit 409, nothing merged or changed (real server over HTTP)'))
        else rec.fail(tag('lookup-first PUT 409'), `status=${put.status} records=${after.length}`)
      } else rec.fail(tag('lookup-first PUT 409'), 'precondition: both records not visible to the owner')

      // the same situation through the genuine extension (owner profile A)
      const mk = requests().length
      const ra = await doSync(A, acctA, { strategy: 'default' })
      const hiddenNative = await A.native.find(Ar.otherId, ['h-hidden'])
      const sharedNative = await A.native.find(Ar.barId, ['h-shared'])
      let moved = null
      if (hiddenNative && sharedNative) {
        const xNative = await A.native.find(hiddenNative, ['ALICE-PRIVATE-TITLE'])
        if (xNative) { await A.native.move(xNative, { parentId: sharedNative }); moved = xNative }
      }
      const rm = await doSync(A, acctA, { strategy: 'default' })
      const mine = summarizeRequests(requests().slice(mk), credA)
      const srvX = (await alice.get(`/bookmark?page=-1&url=${encodeURIComponent(X)}`)).json?.data ?? []
      const dupInFolder = (() => { const c = {}; for (const r of srvX) for (const f of r.folders) c[f] = (c[f] ?? 0) + 1; return Object.values(c).some(n => n > 1) })()
      info.extensionCollision = {
        preSyncError: errText(ra), movedNativeX: !!moved, postMoveDone: rm.done, postMoveError: errText(rm),
        owner409s: mine.entries.filter(e => e.status === 409).map(e => `${e.method} ${e.path.replace(REST, '')}`),
        serverRecordsForX: srvX.length, sameFolderDuplicate: dupInFolder
      }
      const postMove = assessSync({ res: rm, entries: rm.evidence.entries, expect: 'success' })
      if (moved && synced(ra) && postMove.ok && !dupInFolder) rec.pass(tag('owner profile A (stock floccus) moves one collided X next to the other: completed sync with request evidence, no same-folder duplicate on the server; behavior recorded'), JSON.stringify(info.extensionCollision).slice(0, 400))
      else rec.fail(tag('owner extension collision flow'), `moved=${!!moved} preSync=${synced(ra)} postMove=${postMove.reasons.join('; ') || 'ok'} ${JSON.stringify(info.extensionCollision).slice(0, 300)}`)
    }

    // ---- case 6: secrets are not stored in plaintext --------------------------------------
    {
      const leaked = secrets.filter(s => /^bms1\./.test(s)).some(s => dirContains(dataDir, s.split('.')[2]))
      const logLeak = secrets.some(s => s.length >= 8 && server.logs().includes(s))
      if (!leaked && !logLeak) rec.pass(tag('no app secret appears in the database files or server log'))
      else rec.fail(tag('secret exposure'), `db=${leaked} log=${logLeak}`)
    }
    info.requestLogLines = requests().length
    info.status = 'DONE'
  } catch (e) {
    if (e.processStillRunning) liveServers.push({ pid: e.pid, exited: () => false })
    info.status = 'ERROR'
    rec.fail(tag('run aborted'), String(e.message).split('\n')[0])
    info.stack = sanitize(String(e.stack).split('\n').slice(0, 4).join(' | '), secrets)
  } finally {
    const res = await runCleanupSteps(cleanups, { stepTimeoutMs: 60000 })
    info.cleanup = res
    if (!res.clean) rec.fail(tag('cleanup'), res.failures.map(f => `${f.step}: ${f.error}`).join('; '))
    else rec.pass(tag('cleanup: browsers closed, profiles deleted, server stopped'))
    info.serverStopped = server ? server.exited() : null
  }
}

let exit = 1
const finalSteps = []
let runDir
try {
  report.extension.manifestVersion = (() => { try { return JSON.parse(fs.readFileSync(path.join(EXTENSION_DIR, 'manifest.json'), 'utf8')).version } catch { return null } })()
  const zip = path.join(TOOLS_DIR, 'floccus-5.13.0-chrome.zip'), xpi = path.join(TOOLS_DIR, 'floccus-5.13.0.xpi')
  report.extension.chromeZipSha256 = fs.existsSync(zip) ? sha256File(zip) : null
  report.extension.firefoxXpiSha256 = fs.existsSync(xpi) ? sha256File(xpi) : null
  const go = findGo()
  if (go.missing) {
    for (const b of wanted) rec.notRun(`${b}: server sync (all cases)`, 'go toolchain not found (set BH_GO_EXE or install .tools/go)')
  } else {
    runDir = makeRunRoot()
    finalSteps.push(['remove run dir', guardedRunRemoval(runDir, liveServers)])
    const binary = path.join(runDir, process.platform === 'win32' ? 'bookmarksync.exe' : 'bookmarksync')
    let built = true
    try { await buildServer(go.exe, binary); report.serverBuild = 'go build ok (CGO_ENABLED=0)' } catch (e) {
      built = false
      for (const b of wanted) rec.fail(`${b}: build server`, e.message)
    }
    if (built) for (const b of wanted) { try { await runBrowser(b, { runDir, binary }) } catch (e) { rec.fail(`${b}: harness error`, e.message) } }
  }
} catch (e) {
  rec.fail('harness error', String(e.message).split('\n')[0])
} finally {
  const fin = await runCleanupSteps(finalSteps, { stepTimeoutMs: 60000 })
  report.cleanup = { ...fin, runDirRemoved: runDir ? !fs.existsSync(runDir) : null }
  if (!fin.clean) rec.fail('cleanup: run directory', fin.failures.map(f => `${f.step}: ${f.error}`).join('; '))
  const { status, code } = rec.finish()
  exit = code
  report.status = status
  report.finishedAt = new Date().toISOString()
  const file = writeResult('server-sync', sanitizeDeep(report, secrets))
  console.log(`\nRESULT server-sync: ${status}`)
  console.log(`run dir removed: ${report.cleanup.runDirRemoved}   result file: ${file}`)
  if (!fin.clean) console.error(`CLEANUP FAILED: ${JSON.stringify(sanitizeDeep(fin.failures, secrets))}`)
  process.exit(exit)
}
