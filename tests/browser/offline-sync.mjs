// Real stock floccus 5.13.0 offline / conflict / failsafe / full-restore continuation acceptance.
//
//   node tests/browser/offline-sync.mjs [--browser=chrome] [--headed] [--auto-wait-ms=240000]
//
// One self-contained slice on top of the server-sync building blocks: builds the real server,
// provisions a test-only user and two app credentials with the real CLI (password on child stdin,
// app secrets only in this process' memory), and drives two disposable profiles of the unmodified
// extension through its real syncAccount RPC and the native bookmarks API. The server is stopped and
// restarted on the same loopback origin to make edits genuinely offline. The full-system backup is
// created/verified/restored with the real CLI into an absent directory, and the restored server is
// restarted on the same origin while both browser profiles (and their floccus caches) stay open.
// After that restart the harness issues NO sync RPC: continuation must come from floccus's own
// background alarm (interval set to the options page's 5-minute step before the restore).
//
// Exit codes: 0 PASS, 1 FAIL (incl. cleanup), 2 NOT_RUN, 3 BLOCKED, 4 PENDING.
import os from 'node:os'
import fs from 'node:fs'
import path from 'node:path'
import crypto from 'node:crypto'
import { REPO_ROOT, EXPECTED_EXTENSION_VERSION, writeResult, TOOLS_DIR } from './lib/harness.mjs'
import {
  sanitize, sanitizeDeep, parseRequestLog, summarizeRequests, runCleanupSteps, parseSecretFromCliStdout, assessSync,
  stripTree, pickLoopbackPort, findGo, startProcessServer, startRegisteredServer, runCommand, apiClient, serverTree, dirContains, REST
} from './lib/server-sync.mjs'
import { prerequisite, openBrowser } from './lib/browser-session.mjs'
import {
  OfflineRecorder, parseFailsafeError, wouldTripFailsafe, lastAcceptedWrite, acceptedWrites, assessAutomaticContinuation,
  flatBookmarks, duplicateUrls, compareBookmarkSets, titleOf, pollUntil, assertRestoreTarget
} from './lib/offline-evidence.mjs'
import { makeOfflineRunRoot, guardedOfflineRemoval, setAccountSettings, cacheKeys } from './lib/offline-session.mjs'

const arg = (n, d) => (process.argv.find(a => a.startsWith(`--${n}=`)) || '').split('=')[1] ?? d
const browserName = arg('browser', 'chrome')
const headless = !process.argv.includes('--headed')
const AUTO_WAIT_MS = Math.min(Number(arg('auto-wait-ms', 240000)), 300000) // hard cap 5 min
const INTERVAL_MIN = 5 // smallest syncInterval step in the floccus 5.13.0 options page
const PRE_RESTART_AGE_MS = 3 * 60000 // restart ~3 min after the last sync so the interval falls due ~2-3 min later

const secrets = []
const liveServers = []
const rec = new OfflineRecorder({ secrets })
const report = {
  harness: 'offline-sync', startedAt: new Date().toISOString(), node: process.version, os: `${os.type()} ${os.release()}`,
  browser: browserName, phases: {}, cleanup: {}, cases: rec.cases, observations: rec.observations
}
const addSecret = s => { secrets.push(s); rec.addSecret(s) }
const sleep = ms => new Promise(r => setTimeout(r, ms))
const U = k => `https://${k}.offline.example/`
const SANDBOX = 'off-sandbox'
const SEED = ['lw', 'ed1', 'ed2', 'k1', 'k2', 'k3', 'k4', 'k5'].map(k => ({ title: k.toUpperCase(), url: U(k) }))
const credId = secret => Number(secret.split('.')[1])
const at = (nodes, titles) => titles.reduce((cur, t) => (cur?.children ?? cur ?? []).find?.(n => n.title === t) ?? null, nodes)
const errText = r => String(r?.status?.error ?? r?.reason ?? '').slice(0, 200)

const accountData = ({ url, secret, localRoot, label }) => ({
  type: 'nextcloud-bookmarks', url: `${url}/`, username: 'alice', password: secret, serverRoot: '', localRoot, strategy: 'default',
  // Automatic triggers off until the restore phase so the offline/reconnect ordering is controlled by the harness.
  failsafe: true, enabled: false, syncIntervalEnabled: false, includeCredentials: false, allowRedirects: false, nestedSync: false, label
})

async function main(ctx) {
  const pre = prerequisite(browserName)
  if (pre) { rec.notRun(`${browserName}: offline acceptance`, pre); return }
  const dataDir = path.join(ctx.runDir, 'data')
  fs.mkdirSync(dataDir)
  const cleanups = []
  const P = report.phases
  const port = await pickLoopbackPort()
  const base = `http://127.0.0.1:${port}`
  const cur = { server: null }
  let rpcCount = 0
  try {
    const cli = (args, stdin, dir = dataDir) => runCommand({ command: ctx.binary, args: [...args, '--data-dir', dir], stdin, timeoutMs: 120000 })
    const must = async (args, stdin) => {
      const r = await cli(args, stdin)
      if (r.code !== 0) throw new Error(`cli ${args.slice(0, 2).join(' ')} failed (${r.code}): ${sanitize(r.stderr.slice(-300), secrets)}`)
      return r
    }
    const runPlain = async (args) => {
      const r = await runCommand({ command: ctx.binary, args, timeoutMs: 180000 })
      return { code: r.code, out: sanitize((r.stdout + r.stderr).trim().slice(-400), secrets) }
    }
    const startServer = async dir => {
      cur.server = await startRegisteredServer(() => startProcessServer({
        command: ctx.binary, args: ['serve', '--listen', `127.0.0.1:${port}`, '--data-dir', dir],
        readyUrl: `${base}/readyz`, startupTimeoutMs: 30000, secrets
      }), liveServers)
      return cur.server
    }
    const stopServer = async () => {
      await cur.server.stop()
      if (!cur.server.exited()) throw new Error('server did not exit')
      let refused = false
      try { await fetch(`${base}/readyz`, { signal: AbortSignal.timeout(2000) }) } catch { refused = true }
      if (!refused) throw new Error('origin still answering after server stop')
      return true
    }
    cleanups.push(['stop servers', async () => { for (const s of liveServers) if (!s.exited()) await s.stop() }])

    const password = crypto.randomBytes(18).toString('base64url')
    addSecret(password)
    await must(['user', 'create', '--username', 'alice', '--password-stdin'], password + '\n')
    const mkCred = async () => {
      const r = await must(['credential', 'create', '--username', 'alice', '--mode', 'edit', '--label', 'offline-harness'])
      const s = parseSecretFromCliStdout(r.stdout); addSecret(s); return s
    }
    const secA = await mkCred(), secB = await mkCred(), secO = await mkCred()
    const credA = credId(secA), credB = credId(secB)
    const credLabel = { [credA]: 'A', [credB]: 'B' }
    await startServer(dataDir)
    P.server = { port, pid: cur.server.pid }
    const oracle = apiClient(base, 'alice', secO)
    const requests = () => parseRequestLog(cur.server.logs())
    const settleLog = async (quietMs = 700, maxMs = 6000) => {
      const end = Date.now() + maxMs
      let n = requests().length, since = Date.now()
      while (Date.now() < end) { await sleep(100); if (requests().length !== n) { n = requests().length; since = Date.now() } else if (Date.now() - since >= quietMs) return }
    }

    // ---- profiles ---------------------------------------------------------------
    const sessions = {}
    const open = async label => {
      const s = await openBrowser(browserName, { headless })
      sessions[label] = s
      cleanups.unshift([`close profile ${label}`, async () => { await s.close(); P[`profile${label}Closed`] = true }])
      return s
    }
    const A = await open('A'), B = await open('B')
    P.browserVersion = A.version; P.extensionVersion = A.extensionVersion
    if (A.extensionVersion !== EXPECTED_EXTENSION_VERSION || B.extensionVersion !== EXPECTED_EXTENSION_VERSION) throw new Error(`extension version ${A.extensionVersion}/${B.extensionVersion}`)
    const Ar = await A.roots(), Br = await B.roots()

    const acct = {}
    const idle = async (S, id, ms = 60000) => {
      const end = Date.now() + ms
      while (Date.now() < end) { const st = await S.status(id); if (!st?.syncing) return true; await sleep(300) }
      return false
    }
    // Real RPC sync; retried only when floccus ignored the RPC (zero requests from this credential).
    const doSync = async (label, o = { strategy: 'default' }) => {
      const S = sessions[label], id = acct[label].id, cred = acct[label].cred
      let r, entries = []
      for (let attempt = 0; attempt < 3; attempt++) {
        await idle(S, id)
        const m = requests().length
        rpcCount++
        r = await S.sync(id, o)
        await idle(S, id)
        await settleLog()
        entries = summarizeRequests(requests().slice(m), cred).entries
        if (entries.length > 0) break
      }
      r.evidence = { entries }
      r.ok = assessSync({ res: r, entries, expect: 'success' }).ok
      return r
    }
    const sandbox = {}
    const nativeTree = async label => stripTree(await sessions[label].native.sub(sandbox[label]))
    const nativeIdByUrl = async (label, url) => {
      const t = await sessions[label].native.sub(sandbox[label])
      const hit = (function walk(n) { if (n.url === url) return n; for (const c of n.children ?? []) { const h = walk(c); if (h) return h } return null })(t)
      return hit?.id ?? null
    }
    const serverRaw = async () => at((await oracle.get('/folder/-1/children?layers=-1')).json?.data ?? [], ['Bookmarks Bar', SANDBOX])
    const serverSandbox = async () => { const r = await serverRaw(); return r ? { children: serverTree(r.children ?? []) } : null }
    const serverIdByUrl = async url => (function walk(list) { for (const n of list ?? []) { if (n.type === 'bookmark' && n.url === url) return n.id; const h = walk(n.children); if (h) return h } return null })((await serverRaw())?.children)
    const snapshot = async () => ({ server: await serverSandbox(), A: await nativeTree('A'), B: await nativeTree('B') })
    const converged = s => s.server && compareBookmarkSets(s.server, s.A).equal && compareBookmarkSets(s.server, s.B).equal &&
      !duplicateUrls(s.server).length && !duplicateUrls(s.A).length && !duplicateUrls(s.B).length
    const brief = s => ({ server: flatBookmarks(s.server).map(b => b.title), A: flatBookmarks(s.A).map(b => b.title), B: flatBookmarks(s.B).map(b => b.title) })
    const converge = async (order, maxRounds = 3) => {
      const runs = []
      let s = await snapshot()
      for (let i = 0; i < maxRounds && !converged(s); i++) {
        for (const l of order) { const r = await doSync(l); runs.push({ profile: l, ok: r.ok, error: errText(r), requests: r.evidence.entries.length }) }
        s = await snapshot()
      }
      return { converged: converged(s), runs, snap: s }
    }

    // ---- phase 1: seed A, download into B --------------------------------------------
    sandbox.A = await A.native.createFolder(Ar.barId, SANDBOX)
    await A.native.build(sandbox.A, SEED)
    acct.A = { id: await A.importAccount(accountData({ url: base, secret: secA, localRoot: Ar.absoluteRootId, label: 'offline-A' })), cred: credA }
    const r1 = await doSync('A')
    const s1 = await serverSandbox()
    if (r1.ok && s1 && JSON.stringify(s1.children) === JSON.stringify(SEED)) rec.passSync('A seeds 8 bookmarks to the server (order kept)', { serverRequests: r1.evidence.entries.length, nativeTree: await nativeTree('A') })
    else rec.fail('A seed sync', `${errText(r1)} server=${JSON.stringify(s1)}`)
    acct.B = { id: await B.importAccount(accountData({ url: base, secret: secB, localRoot: Br.absoluteRootId, label: 'offline-B' })), cred: credB }
    const r2 = await doSync('B')
    sandbox.B = await B.native.find(Br.barId, [SANDBOX])
    const bTree = sandbox.B ? await nativeTree('B') : null
    if (r2.ok && bTree && JSON.stringify(bTree.children) === JSON.stringify(SEED)) rec.passSync('B downloads the identical tree', { serverRequests: r2.evidence.entries.length, nativeTree: bTree })
    else { rec.fail('B initial download', `${errText(r2)} B=${JSON.stringify(bTree)}`); throw new Error('baseline not established') }
    const lwServerId = await serverIdByUrl(U('lw'))
    P.cacheKeysBeforeOffline = { A: await cacheKeys(A, acct.A.id), B: await cacheKeys(B, acct.B.id) }

    // ---- phase 2: offline round 1 (edit/edit on LW, edit/delete on ED1), B reconnects first ----
    {
      await stopServer()
      const tA = Date.now()
      const titleA = 'LW edited offline in A (earlier)', titleB = 'LW edited offline in B (later)'
      await A.native.update(await nativeIdByUrl('A', U('lw')), { title: titleA })
      await A.native.update(await nativeIdByUrl('A', U('ed1')), { title: 'ED1 edited offline in A' })
      await sleep(1500)
      const tB = Date.now()
      await B.native.update(await nativeIdByUrl('B', U('lw')), { title: titleB })
      await B.native.remove(await nativeIdByUrl('B', U('ed1')))
      P.round1 = { editChronology: { A: new Date(tA).toISOString(), B: new Date(tB).toISOString() } }
      // a sync while the origin is down: genuine floccus failure, local edits must stay
      await idle(A, acct.A.id); rpcCount++
      const off = await A.sync(acct.A.id, { strategy: 'default', timeoutMs: 60000 })
      await idle(A, acct.A.id)
      const aAfterOff = await nativeTree('A')
      P.round1.offlineAttempt = { done: off.done, error: errText(off) }
      if (off.done && off.status?.error && titleOf(aAfterOff, U('lw')) === titleA) rec.pass('offline: A sync against the stopped origin fails with a floccus error and keeps local edits', `error="${errText(off)}"`)
      else rec.fail('offline sync attempt', JSON.stringify(P.round1.offlineAttempt))

      await startServer(dataDir)
      const order = []
      for (const l of ['B', 'A']) { const r = await doSync(l); order.push({ profile: l, ok: r.ok, error: errText(r), accepted: acceptedWrites(r.evidence.entries, acct[l].cred, REST) }) }
      const c = await converge(['B', 'A'])
      const entries = requests()
      const law = lastAcceptedWrite(entries, lwServerId)
      const finalLw = titleOf(c.snap.server, U('lw'))
      const winner = law ? credLabel[law.credential] : null
      const expected = winner === 'A' ? titleA : winner === 'B' ? titleB : null
      P.round1.reconnect = { order, extraRounds: c.runs, lwServerId, lastAcceptedLwWrite: law && `${winner} ${law.method} ${law.path.replace(REST, '')} ${law.status}`, finalLw, final: brief(c.snap) }
      rec.observe('round 1 LW edit/edit', `edits: A earlier, B later; reconnect order B then A; last accepted LW write by ${winner ?? 'none'}; final title "${finalLw}"`)
      if (law && c.converged && finalLw === expected) rec.passSync('last accepted write wins (server keeps the title of the last accepted LW write, all three trees agree)', { serverRequests: entries.length, nativeTree: c.snap.A }, `winner=${winner} (${winner === 'A' ? 'the EARLIER offline edit: chronology not used' : 'the later edit; A did not overwrite'})`)
      else rec.fail('last accepted write (LW)', JSON.stringify(P.round1.reconnect).slice(0, 600))
      const ed1 = { server: titleOf(c.snap.server, U('ed1')), A: titleOf(c.snap.A, U('ed1')), B: titleOf(c.snap.B, U('ed1')) }
      P.round1.ed1 = ed1
      rec.observe('round 1 edit-vs-delete (delete accepted first, then the edit reconnects)', `ED1 final: server=${JSON.stringify(ed1.server)} A=${JSON.stringify(ed1.A)} B=${JSON.stringify(ed1.B)}`)
      const allOk = order.every(o => o.ok) && c.runs.every(o => o.ok)
      if (c.converged && allOk) rec.passSync('edit vs delete (delete first): converged, no duplicates, no floccus error', { serverRequests: entries.length, nativeTree: c.snap.B })
      else rec.fail('edit vs delete (delete first)', JSON.stringify({ converged: c.converged, order, runs: c.runs, final: brief(c.snap) }).slice(0, 600))
    }

    // ---- phase 3: offline round 2 (edit/delete on ED2), the editor reconnects first -----
    {
      await stopServer()
      await A.native.update(await nativeIdByUrl('A', U('ed2')), { title: 'ED2 edited offline in A' })
      await B.native.remove(await nativeIdByUrl('B', U('ed2')))
      await startServer(dataDir)
      const order = []
      for (const l of ['A', 'B']) { const r = await doSync(l); order.push({ profile: l, ok: r.ok, error: errText(r), accepted: acceptedWrites(r.evidence.entries, acct[l].cred, REST) }) }
      const c = await converge(['A', 'B'])
      const ed2 = { server: titleOf(c.snap.server, U('ed2')), A: titleOf(c.snap.A, U('ed2')), B: titleOf(c.snap.B, U('ed2')) }
      P.round2 = { order, extraRounds: c.runs, ed2, final: brief(c.snap) }
      rec.observe('round 2 edit-vs-delete (edit accepted first, then the delete reconnects)', `ED2 final: server=${JSON.stringify(ed2.server)} A=${JSON.stringify(ed2.A)} B=${JSON.stringify(ed2.B)}`)
      if (c.converged && order.every(o => o.ok) && c.runs.every(o => o.ok)) rec.passSync('edit vs delete (edit first): converged, no duplicates, no floccus error', { serverRequests: requests().length, nativeTree: c.snap.A })
      else rec.fail('edit vs delete (edit first)', JSON.stringify(P.round2).slice(0, 600))
    }

    // ---- phase 4: mass deletion above the failsafe threshold ---------------------------
    {
      let n = flatBookmarks(await nativeTree('A')).length
      const fillers = []
      if (n < 6) { // top up while the server total is <= 5 (the addition failsafe cannot trip then)
        for (let i = 0; n + i < 6; i++) fillers.push({ title: `FILL${i}`, url: U(`fill${i}`) })
        await A.native.build(sandbox.A, fillers)
        const rf = await doSync('A'); await doSync('B')
        if (!rf.ok) rec.fail('failsafe precondition top-up', errText(rf))
        n = flatBookmarks(await nativeTree('A')).length
      }
      const before = await snapshot()
      const keep = flatBookmarks(before.A)[0]
      const del = flatBookmarks(before.A).slice(1)
      P.failsafe = { total: n, deleting: del.length, plannedToTrip: wouldTripFailsafe({ total: n, changed: del.length }), fillers: fillers.length }
      for (const b of del) await A.native.remove(await nativeIdByUrl('A', b.url))
      const rf = await doSync('A')
      const fs1 = parseFailsafeError(rf.status?.error)
      const mine = rf.evidence.entries
      const after = await serverSandbox()
      const deletesAccepted = mine.filter(e => e.method === 'DELETE' && !/\/lock$/.test(e.path) && e.status < 300).length
      P.failsafe.extension = { done: rf.done, error: errText(rf), parsed: fs1, requests: mine.length, deletesAccepted }
      if (rf.done && fs1?.code === 29 && mine.length > 0 && deletesAccepted === 0 && compareBookmarkSets(after, before.server).equal) {
        rec.pass('mass local deletion above the failsafe threshold: real E029 safety error, no DELETE accepted, server unchanged', `E0${fs1.code} ${fs1.percent}% (${del.length}/${n})`)
      } else rec.fail('failsafe', JSON.stringify(P.failsafe).slice(0, 500))
      const rb = await doSync('B')
      const bNow = await nativeTree('B')
      if (rb.ok && compareBookmarkSets(bNow, before.B).equal && compareBookmarkSets(await serverSandbox(), before.server).equal) rec.passSync('after the refused run, profile B syncs and is unchanged (no propagated damage)', { serverRequests: rb.evidence.entries.length, nativeTree: bNow })
      else rec.fail('profile B after failsafe', `${errText(rb)} ${JSON.stringify(brief({ server: await serverSandbox(), A: await nativeTree('A'), B: bNow }))}`)
      rec.observe('profile A after the refused run', `A keeps ${flatBookmarks(await nativeTree('A')).length} local bookmark(s) (kept "${keep.title}"); floccus did not revert the local deletion by itself`)
      const rs = await doSync('A', { strategy: 'slave' })
      const rd = await doSync('A')
      const s = await snapshot()
      P.failsafe.recovery = { slave: { ok: rs.ok, error: errText(rs) }, followUp: { ok: rd.ok, error: errText(rd), accepted: acceptedWrites(rd.evidence.entries, credA, REST) } }
      if (rs.ok && rd.ok && converged(s) && compareBookmarkSets(s.server, before.server).equal) rec.passSync('download-only (slave) sync restores A from the server; a following default sync is clean', { serverRequests: rs.evidence.entries.length, nativeTree: s.A })
      else rec.fail('failsafe recovery', JSON.stringify({ ...P.failsafe.recovery, final: brief(s) }).slice(0, 500))
    }

    // ---- phase 5: full-system backup -> rollback restore -> automatic continuation -----
    {
      const R = P.restore = {}
      const preBackup = await snapshot()
      await stopServer()
      const archiveDir = path.join(ctx.runDir, 'archive'); fs.mkdirSync(archiveDir)
      const archive = path.join(archiveDir, 'bookmarksync-offline.zip')
      const bc = await runPlain(['backup', 'create', '--data-dir', dataDir, '--output', archive])
      const bv = await runPlain(['backup', 'verify', '--file', archive])
      R.backupCreate = bc; R.backupVerify = bv
      if (bc.code === 0 && bv.code === 0 && /^Backup verified/m.test(bv.out)) rec.pass('real backup create + verify (server stopped)', bv.out.split('\n')[0])
      else { rec.fail('backup create/verify', JSON.stringify({ bc, bv })); throw new Error('no verified archive') }
      await startServer(dataDir)

      // post-backup changes that the rollback will remove from the server
      const victim = flatBookmarks(preBackup.A).find(b => /^K\d$/.test(b.title)) ?? flatBookmarks(preBackup.A)[1]
      await A.native.build(sandbox.A, [{ title: 'POST-BACKUP-A', url: U('post-backup') }])
      await A.native.remove(await nativeIdByUrl('A', victim.url))
      const ra = await doSync('A'), rb = await doSync('B')
      const postBackup = await snapshot()
      R.postBackup = { added: 'POST-BACKUP-A', removed: victim.title, syncA: ra.ok, syncB: rb.ok, converged: converged(postBackup), final: brief(postBackup) }
      if (!(ra.ok && rb.ok && converged(postBackup))) rec.fail('post-backup changes reach both profiles', JSON.stringify(R.postBackup).slice(0, 400))

      // user setting as in the options page: interval sync on, 5-minute step (UI minimum)
      for (const l of ['A', 'B']) await setAccountSettings(sessions[l], acct[l].id, { syncIntervalEnabled: true, syncInterval: INTERVAL_MIN })
      const st0 = { A: await A.status(acct.A.id), B: await B.status(acct.B.id) }
      const lastSyncs = [st0.A.lastSync, st0.B.lastSync]
      R.intervalConfigured = { syncInterval: INTERVAL_MIN, lastSyncA: new Date(st0.A.lastSync).toISOString(), lastSyncB: new Date(st0.B.lastSync).toISOString() }
      const waitUntil = Math.min(...lastSyncs) + PRE_RESTART_AGE_MS
      if (waitUntil > Date.now()) await sleep(waitUntil - Date.now())
      const cacheBefore = { A: await cacheKeys(A, acct.A.id), B: await cacheKeys(B, acct.B.id) }

      await stopServer()
      const restored = assertRestoreTarget(path.join(ctx.runDir, 'restored-data'), ctx.runDir)
      const rr = await runPlain(['backup', 'restore', '--file', archive, '--target-dir', restored])
      R.restoreCli = rr
      if (rr.code !== 0) { rec.fail('backup restore into an absent directory', rr.out); throw new Error('restore failed') }
      await startServer(restored)
      const restartAt = Date.now()
      const rpcAtRestart = rpcCount
      R.restartAt = new Date(restartAt).toISOString()
      const restoredServer = await serverSandbox()
      if (compareBookmarkSets(restoredServer, preBackup.server).equal) rec.pass('restored server on the same origin/credentials holds exactly the pre-backup tree', `${flatBookmarks(restoredServer).length} bookmarks`)
      else rec.fail('restored server content', JSON.stringify({ restored: flatBookmarks(restoredServer).map(b => b.title), preBackup: flatBookmarks(preBackup.server).map(b => b.title) }))

      // no RPC from here: wait (bounded) for floccus's own alarm-driven syncs
      const statuses = async () => ({ A: await A.status(acct.A.id), B: await B.status(acct.B.id) })
      const bound = Math.min(AUTO_WAIT_MS, Math.max(60000, Math.max(...lastSyncs) + (INTERVAL_MIN + 1.5) * 60000 - restartAt))
      R.autoWaitBoundMs = bound
      const waited = await pollUntil(async () => {
        const st = await statuses()
        return ['A', 'B'].every(l => st[l]?.lastSync > restartAt && !st[l]?.syncing) ? st : null
      }, { timeoutMs: bound, intervalMs: 3000 })
      await settleLog(1500, 10000)
      const stAfter = await statuses()
      const entries = requests()
      const auto = assessAutomaticContinuation({
        entries, credentials: { A: credA, B: credB }, statuses: stAfter, restartAt, rpcCallsAfterRestart: rpcCount - rpcAtRestart
      })
      const afterAuto = await snapshot()
      R.automatic = {
        waitedMs: waited.elapsedMs, observedWithinBound: waited.ok, verdict: auto, rpcCallsAfterRestart: rpcCount - rpcAtRestart,
        firstSyncAfterRestartMs: Object.fromEntries(['A', 'B'].map(l => [l, stAfter[l]?.lastSync > restartAt ? stAfter[l].lastSync - restartAt : null])),
        requestsA: summarizeRequests(entries, credA).entries.map(e => `${e.method} ${e.path.replace(REST, '')} ${e.status}`).slice(0, 40),
        requestsB: summarizeRequests(entries, credB).entries.map(e => `${e.method} ${e.path.replace(REST, '')} ${e.status}`).slice(0, 40),
        final: brief(afterAuto), cacheKeysBeforeRestore: cacheBefore
      }
      const post = { server: titleOf(afterAuto.server, U('post-backup')), A: titleOf(afterAuto.A, U('post-backup')), B: titleOf(afterAuto.B, U('post-backup')) }
      const vic = { server: titleOf(afterAuto.server, victim.url), A: titleOf(afterAuto.A, victim.url), B: titleOf(afterAuto.B, victim.url) }
      R.rollbackOutcome = { postBackupItem: post, removedAfterBackup: { title: victim.title, ...vic } }
      if (auto.ok) {
        rec.passSync('automatic background (alarm/interval) sync resumed against the restored server, no RPC after restart', { serverRequests: entries.length, nativeTree: afterAuto.A },
          `A after ${Math.round(R.automatic.firstSyncAfterRestartMs.A / 1000)}s, B after ${Math.round(R.automatic.firstSyncAfterRestartMs.B / 1000)}s`)
        rec.observe('rollback outcome after automatic continuation', `post-backup bookmark: server=${JSON.stringify(post.server)} A=${JSON.stringify(post.A)} B=${JSON.stringify(post.B)}; bookmark deleted after backup ("${victim.title}"): server=${JSON.stringify(vic.server)} A=${JSON.stringify(vic.A)} B=${JSON.stringify(vic.B)}`)
        if (converged(afterAuto)) rec.passSync('after automatic continuation server, A and B agree, no duplicates', { serverRequests: entries.length, nativeTree: afterAuto.B })
        else rec.pending('convergence after automatic continuation within the bound', JSON.stringify(brief(afterAuto)))
      } else {
        rec.pending('automatic background sync after restore within the bound', `${Math.round(bound / 1000)}s: ${auto.reasons.join('; ')}`)
      }
      const unchanged = flatBookmarks(preBackup.server).filter(b => b.url !== victim.url)
      const intact = t => unchanged.every(b => titleOf(t, b.url) === b.title)

      // manual continuation is reported separately and never counted as automatic evidence
      if (!(auto.ok && converged(afterAuto))) {
        const c = await converge(['A', 'B'])
        R.manual = { runs: c.runs, converged: c.converged, final: brief(c.snap) }
        if (c.converged) rec.passSync('MANUAL continuation (RPC) after restore converges server, A and B', { serverRequests: requests().length, nativeTree: c.snap.A })
        else rec.fail('manual continuation after restore', JSON.stringify(R.manual).slice(0, 500))
        if (intact(c.snap.server) && intact(c.snap.A) && intact(c.snap.B)) rec.pass('no unexpected damage: every bookmark untouched since before the backup is intact everywhere')
        else rec.fail('unexpected damage after restore', JSON.stringify(brief(c.snap)))
      } else if (intact(afterAuto.server) && intact(afterAuto.A) && intact(afterAuto.B)) rec.pass('no unexpected damage: every bookmark untouched since before the backup is intact everywhere')
      else rec.fail('unexpected damage after restore', JSON.stringify(brief(afterAuto)))
    }

    // ---- secrets -------------------------------------------------------------------
    {
      const tokens = secrets.filter(s => /^bms1\./.test(s)).map(s => s.split('.')[2])
      const dbLeak = tokens.some(t => dirContains(dataDir, t) || (fs.existsSync(path.join(ctx.runDir, 'restored-data')) && dirContains(path.join(ctx.runDir, 'restored-data'), t)))
      const logLeak = liveServers.some(s => secrets.some(x => x.length >= 8 && s.logs?.().includes(x)))
      if (!dbLeak && !logLeak) rec.pass('no app secret or password in the data directories or any server log')
      else rec.fail('secret exposure', `db=${dbLeak} log=${logLeak}`)
    }
    P.serverProcesses = liveServers.length
  } catch (e) {
    rec.fail('run aborted', sanitize(String(e.message).split('\n')[0], secrets))
    report.stack = sanitize(String(e.stack).split('\n').slice(0, 4).join(' | '), secrets)
  } finally {
    const res = await runCleanupSteps(cleanups, { stepTimeoutMs: 60000 })
    report.cleanup.session = res
    if (!res.clean) rec.fail('cleanup: profiles/servers', res.failures.map(f => `${f.step}: ${f.error}`).join('; '))
    else rec.pass('cleanup: both browsers closed and profiles deleted, every server process exited')
  }
}

let runDir
const finalSteps = []
try {
  const go = findGo()
  if (go.missing) rec.notRun('offline acceptance', 'go toolchain not found (.tools/go)')
  else {
    runDir = makeOfflineRunRoot()
    finalSteps.push(['remove run dir', guardedOfflineRemoval(runDir, liveServers)])
    const binary = path.join(runDir, process.platform === 'win32' ? 'bookmarksync.exe' : 'bookmarksync')
    const b = await runCommand({
      command: go.exe, args: ['build', '-trimpath', '-o', binary, './cmd/bookmarksync'], cwd: REPO_ROOT, timeoutMs: 300000,
      env: { CGO_ENABLED: '0', GOCACHE: path.join(TOOLS_DIR, 'go-cache') }
    })
    if (b.code !== 0) rec.fail('build server', sanitize(b.stderr.slice(-400), secrets))
    else { report.serverBuild = 'go build ok (CGO_ENABLED=0)'; await main({ runDir, binary }) }
  }
} catch (e) {
  rec.fail('harness error', sanitize(String(e.message).split('\n')[0], secrets))
} finally {
  const fin = await runCleanupSteps(finalSteps, { stepTimeoutMs: 60000 })
  report.cleanup.runDir = { ...fin, removed: runDir ? !fs.existsSync(runDir) : null }
  if (!fin.clean) rec.fail('cleanup: run directory', fin.failures.map(f => `${f.step}: ${f.error}`).join('; '))
  const { status, code } = rec.finish()
  report.status = status
  report.finishedAt = new Date().toISOString()
  const file = writeResult('offline-sync', sanitizeDeep(report, secrets))
  console.log(`\nRESULT offline-sync: ${status}`)
  console.log(`run dir removed: ${report.cleanup.runDir.removed}   result file: ${file}`)
  process.exit(code)
}
