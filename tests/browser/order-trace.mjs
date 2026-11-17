// Diagnostic (not part of the acceptance run): trace why a downloaded nested folder can end up
// in a different order than the server sent. Records, for one real browser + the real server:
//   - the exact request/response bodies floccus exchanged (through a harness-owned recording proxy),
//   - the browser's native create/move events with indexes while the download runs.
//   node order-trace.mjs --browser=chrome [--attempts=6] [--full] [--direct]
// Exit codes: 0 ok, 1 failure (including any cleanup failure), 2 NOT_RUN (missing prerequisite).
import path from 'node:path'
import crypto from 'node:crypto'
import { fileURLToPath } from 'node:url'
import { REPO_ROOT, TOOLS_DIR } from './lib/harness.mjs'
import * as SS from './lib/server-sync.mjs'
import { openBrowser, prerequisite } from './lib/browser-session.mjs'

const { guardedRunRemoval, mergeCleanup, runCleanupSteps, sanitize, parseSecretFromCliStdout, REST } = SS

/** Real collaborators; the entry-point test injects fakes for the ones that touch processes. */
export const realDeps = {
  findGo: SS.findGo,
  makeRunRoot: SS.makeRunRoot,
  runCommand: SS.runCommand,
  startProcessServer: SS.startProcessServer,
  startRecordingProxy: SS.startRecordingProxy,
  pickLoopbackPort: SS.pickLoopbackPort,
  openBrowser
}

/**
 * Runs the trace. Never calls process.exit; returns { out, exitCode, runDir, secrets }.
 * Every server this run started (or could not stop) is registered in liveServers so the run
 * directory is only deleted when no server may still be using it.
 */
export async function runOrderTrace({ name = 'chrome', attempts = 6, full = false, direct = false } = {}, deps = realDeps) {
  const go = deps.findGo()
  const secrets = []
  const out = { browser: name, attempts: [] }
  if (go.missing) return { out: { ...out, notRun: 'go toolchain missing' }, exitCode: 2, runDir: null, secrets }

  const cleanups = []
  const attemptCleanups = [] // one cleanup result per profile B, merged into the exit status
  const liveServers = []
  const runDir = deps.makeRunRoot()
  out.runDir = runDir
  try {
    const bin = path.join(runDir, 'b.exe'), data = path.join(runDir, 'data')
    const b = await deps.runCommand({ command: go.exe, args: ['build', '-trimpath', '-o', bin, './cmd/bookmarksync'], cwd: REPO_ROOT, timeoutMs: 300000, env: { CGO_ENABLED: '0', GOCACHE: path.join(TOOLS_DIR, 'go-cache') } })
    if (b.code) throw new Error(b.stderr.slice(-300))
    const cli = (a, s) => deps.runCommand({ command: bin, args: [...a, '--data-dir', data], stdin: s })
    await cli(['user', 'create', '--username', 'alice', '--password-stdin'], crypto.randomBytes(12).toString('base64url') + 'xx\n')
    const mk = async () => { const s = parseSecretFromCliStdout((await cli(['credential', 'create', '--username', 'alice', '--mode', 'edit'])).stdout); secrets.push(s); return s }
    const port = await deps.pickLoopbackPort()
    // Registers the handle on success AND an uncertain pid if startup fails with a server that could not be stopped.
    const srv = await SS.startRegisteredServer(() => deps.startProcessServer({ command: bin, args: ['serve', '--listen', `127.0.0.1:${port}`, '--data-dir', data], readyUrl: `http://127.0.0.1:${port}/readyz`, secrets }), liveServers)
    cleanups.push(['stop server', () => srv.stop()])
    const proxy = await deps.startRecordingProxy({ targetPort: port })
    cleanups.push(['stop proxy', () => proxy.stop()])
    const acct = (secret, localRoot, label) => ({ type: 'nextcloud-bookmarks', url: `${direct ? `http://127.0.0.1:${port}` : proxy.url}/`, username: 'alice', password: secret, serverRoot: '', localRoot, strategy: 'default', failsafe: true, syncIntervalEnabled: false, enabled: true, includeCredentials: false, allowRedirects: false, nestedSync: false, label })
    const A = await deps.openBrowser(name); cleanups.unshift(['close A', () => A.close()])
    const Ar = await A.roots()
    const sb = await A.native.createFolder(Ar.barId, 'bs-sandbox')
    const u = (t) => ({ title: t, url: `https://${t.toLowerCase()}.example/` })
    const F = { title: 'Folder F', children: [u('G1'), u('G2')] }
    // --full reproduces the acceptance scenario's content (same siblings, both roots)
    await A.native.build(sb, full ? [u('Alpha'), u('Beta'), F, u('Zed'), u('Dup')] : [F])
    if (full) {
      const so = await A.native.createFolder(Ar.otherId, 'bs-sandbox-other')
      await A.native.build(so, [u('O1'), { title: 'Sub', children: [u('O2')] }])
    }
    const mark0 = proxy.records().length
    const ra = await A.sync(await A.importAccount(acct(await mk(), Ar.absoluteRootId, 'A')), { strategy: 'default' })
    out.upload = {
      error: ra.status?.error ?? null,
      requests: proxy.records().slice(mark0).map(r => `${r.method} ${r.path.replace(REST, '')}${r.query} ${r.status}${r.method !== 'GET' && r.requestBody ? ' body=' + r.requestBody.slice(0, 300) : ''}`)
    }
    const recorder = async () => {
      const api = typeof browser !== 'undefined' ? browser : chrome
      self.__bh = []
      api.bookmarks.onCreated.addListener((id, n) => self.__bh.push(`created ${n.title ?? ''} parent=${n.parentId} index=${n.index}`))
      api.bookmarks.onMoved.addListener((id, m) => self.__bh.push(`moved ${id} parent=${m.parentId} index=${m.index} (was ${m.oldParentId}/${m.oldIndex})`))
      return true
    }
    for (let i = 1; i <= attempts; i++) {
      const B = await deps.openBrowser(name)
      try {
        const Br = await B.roots()
        await B.run(recorder)
        const m = proxy.records().length
        const rb = await B.sync(await B.importAccount(acct(await mk(), Br.absoluteRootId, 'B')), { strategy: 'default' })
        const recs = proxy.records().slice(m)
        const bf = await B.native.find(Br.barId, ['bs-sandbox', 'Folder F'])
        const order = bf ? (await B.native.sub(bf)).children.map(c => c.title) : null
        const events = await B.run(() => self.__bh)
        out.attempts.push({
          n: i, error: rb.status?.error ?? null, nativeOrderF: order,
          nativeEvents: events.filter(e => /G[12]|Folder F/.test(e) || /^moved/.test(e)),
          requests: recs.map(r => `${r.method} ${r.path.replace(REST, '')}${r.query} ${r.status}${r.method !== 'GET' && r.requestBody ? ' body=' + r.requestBody.slice(0, 300) : ''}`),
          childrenResponses: recs.filter(r => /children/.test(r.path)).map(r => r.responseBody.replace(/"url":"[^"]*",?/g, '').slice(0, 700))
        })
      } finally {
        attemptCleanups.push(await runCleanupSteps([[`close B${i}`, () => B.close()]], { stepTimeoutMs: 60000 }))
      }
    }
  } catch (e) {
    out.error = sanitize(String(e.message || e).split('\n')[0], secrets)
  } finally {
    const r = await runCleanupSteps([...cleanups, ['remove run dir', guardedRunRemoval(runDir, liveServers)]], { stepTimeoutMs: 60000 })
    out.cleanup = mergeCleanup([r, ...attemptCleanups])
  }
  return { out, exitCode: out.cleanup.clean && !out.error ? 0 : 1, runDir, secrets }
}

const isMain = process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)
if (isMain) {
  const arg = (n, d) => (process.argv.find(a => a.startsWith(`--${n}=`)) || '').split('=')[1] ?? d
  const name = arg('browser', 'chrome')
  const pre = prerequisite(name)
  if (pre) { console.log(`NOT_RUN ${pre}`); process.exit(2) }
  const { out, exitCode, secrets } = await runOrderTrace({
    name, attempts: Number(arg('attempts', 6)), full: process.argv.includes('--full'), direct: process.argv.includes('--direct')
  })
  console.log(sanitize(JSON.stringify(out, null, 1), secrets))
  process.exit(exitCode)
}
