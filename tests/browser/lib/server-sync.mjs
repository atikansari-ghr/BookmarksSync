// Helpers for the real server-sync command (server-sync.mjs). Dev-only.
// Rules enforced here:
//  - every path the harness creates lives under a MARKED run directory inside .tools/server-sync
//    and only such a directory is ever deleted;
//  - secrets (app credentials, passwords) live in process memory or child stdin; every captured
//    log/result passes through sanitize() before it is stored or printed;
//  - a run can only PASS when every case really passed: missing prerequisites are NOT_RUN (exit 2),
//    and a sync case without real server requests plus a native-tree check can never be a PASS.
import fs from 'node:fs'
import path from 'node:path'
import net from 'node:net'
import crypto from 'node:crypto'
import { spawn, spawnSync } from 'node:child_process'
import { TOOLS_DIR, REPO_ROOT, isContained, isPhysicallyContained, withTimeout } from './harness.mjs'

export const SYNC_ROOT = path.join(TOOLS_DIR, 'server-sync')
const RUN_MARKER = '.bookmarksync-server-sync-run'
export const REST = '/index.php/apps/bookmarks/public/rest/v2'

// The Floccus protocol puts concrete folder IDs in a negative namespace. CLI share
// provisioning uses the positive internal/database ID, so translate only at that boundary.
export function wireFolderIdToInternal(id) {
  if (!Number.isSafeInteger(id) || id >= -1) throw new TypeError('expected a concrete folder ID in the negative wire namespace')
  return -id - 1
}

// ---- sanitising ------------------------------------------------------------

/** Redact known secrets, any bms1 app token and Basic authorization values from text. */
export function sanitize(text, secrets = []) {
  let out = String(text)
  for (const s of secrets) {
    if (typeof s === 'string' && s.length >= 8) out = out.split(s).join('[REDACTED]')
  }
  out = out.replace(/bms1\.\d+\.[A-Za-z0-9_-]{43}/g, '[REDACTED]')
  out = out.replace(/(Basic\s+)[A-Za-z0-9+/=]{8,}/gi, '$1[REDACTED]')
  return out
}

export function sanitizeDeep(value, secrets = []) {
  if (typeof value === 'string') return sanitize(value, secrets)
  if (Array.isArray(value)) return value.map(v => sanitizeDeep(v, secrets))
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, sanitizeDeep(v, secrets)]))
  }
  return value
}

// ---- server request log ----------------------------------------------------

const LOG_RE = /msg="floccus request" method=(\S+) path=(\S+) credential=(\d+) status=(\d+)/

export function parseRequestLog(text) {
  const out = []
  for (const line of String(text).split('\n')) {
    const m = LOG_RE.exec(line)
    if (m) out.push({ method: m[1], path: m[2], credential: Number(m[3]), status: Number(m[4]) })
  }
  return out
}

/** Requests of one credential; "writes" are everything except GET and the lock verbs. */
export function summarizeRequests(entries, credential) {
  const mine = entries.filter(e => e.credential === credential)
  const isLock = e => e.path.endsWith('/lock') && (e.method === 'POST' || e.method === 'DELETE')
  const writes = mine.filter(e => e.method !== 'GET' && !isLock(e))
  return { total: mine.length, writes, onlyReadsAndLock: writes.length === 0, entries: mine }
}

// ---- marked run directories --------------------------------------------------

export function makeRunRoot({ root = SYNC_ROOT, toolsDir = TOOLS_DIR } = {}) {
  fs.mkdirSync(toolsDir, { recursive: true })
  fs.mkdirSync(root, { recursive: true })
  if (!isPhysicallyContained(toolsDir, root)) throw new Error(`refusing: server-sync root escapes tools dir: ${root}`)
  const dir = path.join(root, `run-${Date.now()}-${crypto.randomBytes(4).toString('hex')}`)
  if (!isContained(root, dir)) throw new Error('run path escaped root')
  fs.mkdirSync(dir)
  try {
    if (fs.lstatSync(dir).isSymbolicLink() || !isPhysicallyContained(root, dir)) throw new Error('run path escaped root (physical)')
    fs.writeFileSync(path.join(dir, RUN_MARKER), 'created by tests/browser server-sync harness\n')
  } catch (e) {
    try { fs.rmdirSync(dir) } catch {}
    throw e
  }
  return dir
}

export function removeRunRoot(dir, { root = SYNC_ROOT } = {}) {
  const abs = path.resolve(dir)
  if (!isContained(root, abs)) throw new Error(`refusing to delete outside harness root: ${abs}`)
  if (!fs.existsSync(abs)) return true
  if (fs.lstatSync(abs).isSymbolicLink()) throw new Error(`refusing to delete a link: ${abs}`)
  if (!isPhysicallyContained(root, abs)) throw new Error(`refusing to delete: resolves outside harness root: ${abs}`)
  if (!fs.existsSync(path.join(abs, RUN_MARKER))) throw new Error(`refusing to delete unmarked directory: ${abs}`)
  fs.rmSync(abs, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 })
  return !fs.existsSync(abs)
}

// ---- cleanup runner ----------------------------------------------------------

/** Run every cleanup step in order, each bounded; collect failures instead of stopping. */
export async function runCleanupSteps(steps, { stepTimeoutMs = 30000 } = {}) {
  const failures = []
  for (const [step, fn] of steps) {
    try {
      await withTimeout(Promise.resolve().then(fn), stepTimeoutMs, step)
    } catch (e) {
      failures.push({ step, error: String(e.message || e) })
    }
  }
  return { clean: failures.length === 0, failures }
}

// ---- results -----------------------------------------------------------------

const STATUSES = new Set(['PASS', 'FAIL', 'NOT_RUN', 'BLOCKED'])

export function classify(statuses) {
  for (const s of statuses) if (!STATUSES.has(s)) throw new Error(`unknown status ${s}`)
  if (statuses.length === 0) return { status: 'NOT_RUN', code: 2 }
  if (statuses.includes('FAIL')) return { status: 'FAIL', code: 1 }
  if (statuses.includes('BLOCKED')) return { status: 'BLOCKED', code: 3 }
  if (statuses.includes('NOT_RUN')) return { status: 'NOT_RUN', code: 2 }
  return { status: 'PASS', code: 0 }
}

export class Recorder {
  constructor({ log = console.log, secrets = [] } = {}) { this.cases = []; this.log = log; this.secrets = secrets }
  addSecret(s) { if (typeof s === 'string' && s.length >= 8) this.secrets.push(s) }
  record(name, status, detail = '') {
    const d = sanitize(detail, this.secrets)
    this.cases.push({ name, status, detail: d })
    this.log(`${status.padEnd(8)} ${name}${d ? ' - ' + d : ''}`)
  }
  pass(name, detail) { this.record(name, 'PASS', detail) }
  fail(name, detail) { this.record(name, 'FAIL', detail) }
  notRun(name, detail) { this.record(name, 'NOT_RUN', detail) }
  blocked(name, detail) { this.record(name, 'BLOCKED', detail) }
  /** A sync PASS needs real server requests AND a check of the actual native browser tree. */
  passSync(name, evidence = {}, detail = '') {
    if (!(evidence.serverRequests > 0) || !evidence.nativeTree) {
      return this.fail(name, `no real sync evidence (serverRequests=${evidence.serverRequests ?? 0}, nativeTree=${!!evidence.nativeTree})`)
    }
    this.pass(name, detail)
  }
  finish() { return classify(this.cases.map(c => c.status)) }
}

// ---- CLI / process helpers ---------------------------------------------------

export function parseSecretFromCliStdout(stdout) {
  const lines = String(stdout).split('\n').map(l => l.trim()).filter(Boolean)
  if (lines.length !== 1 || !/^bms1\.\d+\.[A-Za-z0-9_-]{43}$/.test(lines[0])) {
    throw new Error('credential create did not print exactly one app secret line')
  }
  return lines[0]
}

export async function pickLoopbackPort() {
  for (let i = 0; i < 30; i++) {
    const port = 20000 + crypto.randomInt(0, 40000)
    const ok = await new Promise(res => {
      const s = net.createServer().once('error', () => res(false)).listen(port, '127.0.0.1', () => s.close(() => res(true)))
    })
    if (ok) return port
  }
  throw new Error('no free loopback port found')
}

export function findGo({ env = process.env, exists = fs.existsSync, candidates } = {}) {
  const list = [env.BH_GO_EXE, ...(candidates ?? [path.join(REPO_ROOT, '.tools', 'go', 'bin', 'go.exe'), path.join(REPO_ROOT, '.tools', 'go', 'bin', 'go')])].filter(Boolean)
  const exe = list.find(p => exists(p))
  return exe ? { exe } : { missing: true }
}

function baseEnv() {
  const keep = ['PATH', 'Path', 'SystemRoot', 'TEMP', 'TMP', 'USERPROFILE', 'HOME', 'COMSPEC', 'PATHEXT', 'WINDIR']
  return Object.fromEntries(keep.filter(k => process.env[k] !== undefined).map(k => [k, process.env[k]]))
}

function killTree(child) {
  try { child.kill() } catch {}
  if (process.platform === 'win32' && child.pid) {
    try { spawnSync('taskkill', ['/PID', String(child.pid), '/T', '/F'], { stdio: 'ignore', windowsHide: true, timeout: 5000 }) } catch {}
  }
}

const LOG_CAP = 1 << 20

/**
 * Spawn a server process, keep its output in memory (never on disk), wait until readyUrl answers 2xx.
 * On any startup failure the child is killed and awaited before the rejection.
 */
export async function startProcessServer({ command, args = [], env = {}, cwd, readyUrl, startupTimeoutMs = 30000, secrets = [], onSpawn, terminate = killTree, stopTimeoutMs = 8000 }) {
  const child = spawn(command, args, { cwd, env: { ...baseEnv(), ...env }, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true })
  let buf = ''
  const take = d => { buf += d.toString(); if (buf.length > LOG_CAP) buf = buf.slice(-LOG_CAP) }
  child.stdout.on('data', take); child.stderr.on('data', take)
  let exited = child.exitCode !== null
  const exitP = exited ? Promise.resolve() : new Promise(r => child.once('exit', () => { exited = true; r() }))
  child.once('error', () => {})
  if (onSpawn) onSpawn(child.pid)
  const stop = async () => {
    if (exited) return
    terminate(child)
    await withTimeout(exitP, stopTimeoutMs, 'server process exit')
  }
  const deadline = Date.now() + startupTimeoutMs
  try {
    for (;;) {
      if (exited) throw new Error(`server process exited (code ${child.exitCode}) before ready: ${sanitize(buf.slice(-400), secrets)}`)
      try {
        const r = await withTimeout(fetch(readyUrl), 1500, 'ready probe')
        if (r.ok) break
      } catch {}
      if (Date.now() > deadline) throw new Error(`server not ready within ${startupTimeoutMs}ms`)
      await new Promise(r => setTimeout(r, 100))
    }
  } catch (e) {
    // Never swallow a failed stop: the caller must know a server may still be running (and holding its data).
    try { await stop(); e.processStillRunning = false } catch (stopErr) {
      e.processStillRunning = true
      e.pid = child.pid
      e.stopError = String(stopErr.message || stopErr)
      e.message += '; server process ' + child.pid + ' could not be stopped (' + e.stopError + ')'
    }
    throw e
  }
  return { pid: child.pid, logs: () => buf, stop, exited: () => exited }
}

/** Run a short-lived command, secrets only via stdin; returns { code, stdout, stderr } (unsanitised, in memory). */
export function runCommand({ command, args = [], env = {}, stdin, timeoutMs = 60000, cwd }) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, env: { ...baseEnv(), ...env }, stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true })
    let stdout = '', stderr = ''
    const timer = setTimeout(() => { killTree(child); reject(new Error(`timeout after ${timeoutMs}ms: ${path.basename(command)} ${args[0] ?? ''}`)) }, timeoutMs)
    child.stdout.on('data', d => { stdout += d })
    child.stderr.on('data', d => { stderr += d })
    child.once('error', e => { clearTimeout(timer); reject(e) })
    child.once('close', code => { clearTimeout(timer); resolve({ code, stdout, stderr }) })
    child.stdin.on('error', () => {})
    child.stdin.end(stdin ?? '')
  })
}

// ---- native tree comparison -------------------------------------------------------

export function stripTree(n) {
  const out = { title: n.title }
  if (n.url !== undefined && n.url !== null) out.url = n.url
  if (n.children) out.children = n.children.map(stripTree)
  return out
}

export const treeEquals = (a, b) => JSON.stringify(stripTree(a)) === JSON.stringify(stripTree(b))

// ---- HTTP oracle (server-side view through the same API, credential in memory) ------

export function apiClient(baseUrl, username, secret) {
  const auth = 'Basic ' + Buffer.from(`${username}:${secret}`).toString('base64')
  const call = async (method, p, body) => {
    const r = await fetch(baseUrl + REST + p, {
      method, redirect: 'manual',
      headers: { authorization: auth, ...(body ? { 'content-type': 'application/json' } : {}) },
      body: body ? JSON.stringify(body) : undefined
    })
    let json = null
    try { json = await r.json() } catch {}
    return { status: r.status, json }
  }
  return { get: p => call('GET', p), post: (p, b) => call('POST', p, b), put: (p, b) => call('PUT', p, b), del: p => call('DELETE', p) }
}

/** Normalise the server's folder children JSON into { title, url?, children? } (order preserved). */
export function serverTree(nodes) {
  return nodes.map(n => n.type === 'bookmark'
    ? { title: n.title, url: n.url }
    : { title: n.title, children: serverTree(n.children ?? []) })
}

/** True if any file under dir contains the byte string (used to prove secrets are not stored). */
export function dirContains(dir, needle) {
  const buf = Buffer.from(needle)
  for (const name of fs.readdirSync(dir)) {
    const f = path.join(dir, name)
    const st = fs.statSync(f)
    if (st.isDirectory()) { if (dirContains(f, needle)) return true; continue }
    if (st.size > 0 && fs.readFileSync(f).includes(buf)) return true
  }
  return false
}

// ---- recording proxy (diagnostics) ---------------------------------------------------
import http from 'node:http'

/**
 * A harness-owned loopback proxy in front of the real server. It forwards every request unchanged
 * (so the real server still authenticates) and records method, path, query, request/response
 * bodies (capped) and status in memory. Authorization and cookies are never recorded.
 */
export async function startRecordingProxy({ targetPort, bodyCap = 65536 }) {
  const records = []
  const srv = http.createServer((req, res) => {
    const chunks = []
    req.on('data', c => chunks.push(c))
    req.on('end', () => {
      const body = Buffer.concat(chunks)
      const u = new URL(req.url, 'http://x')
      const headers = { ...req.headers, host: `127.0.0.1:${targetPort}` }
      const up = http.request({ host: '127.0.0.1', port: targetPort, method: req.method, path: req.url, headers }, (ur) => {
        const rc = []
        ur.on('data', c => rc.push(c))
        ur.on('end', () => {
          const rb = Buffer.concat(rc)
          records.push({
            method: req.method, path: u.pathname, query: u.search, status: ur.statusCode,
            requestBody: sanitize(body.subarray(0, bodyCap).toString('utf8')), responseBody: sanitize(rb.subarray(0, bodyCap).toString('utf8'))
          })
          const h = { ...ur.headers }
          res.writeHead(ur.statusCode, h)
          res.end(rb)
        })
      })
      up.on('error', () => { res.writeHead(502); res.end() })
      up.end(body)
    })
  })
  await new Promise((r, j) => srv.once('error', j).listen(0, '127.0.0.1', r))
  const port = srv.address().port
  return {
    url: `http://127.0.0.1:${port}`, port, records: () => records,
    stop: () => new Promise(r => { srv.closeAllConnections?.(); srv.close(() => r()) })
  }
}

// ---- known floccus 5.13.0 limitation: id-namespace collision in ReconcileReorders ----------
/**
 * Floccus's debug log line "ReconcileReorders: Inserting created item into order" prints the created
 * item (a LOCAL parentId) and the REORDER it was inserted into (payload = a SERVER folder id). When the
 * numbers are equal for unrelated folders, a created item pollutes the other folder's order (floccus
 * compares ids across the two namespaces). Returns one entry per such collision.
 */
export function detectIdNamespaceCollision(lines) {
  const hits = []
  const orderRecords = []
  for (const line of lines) {
    const o = /\(local\)ORDERFOLDER \{ id: '(\d+)', order: \[([^\]]*)\]/.exec(String(line))
    if (o) orderRecords.push({ folder: o[1], ids: [...o[2].matchAll(/id: '(\d+)'/g)].map(m => m[1]) })
  }
  for (const line of lines) {
    if (!String(line).includes('Inserting created item into order')) continue
    const c = /creation: \{[^}]*?payload: [-+] #(\d+)(?:;\d+)?\[([^\]]*)\][^]*?parentId: (\d+)/.exec(line)
    const r = /reorder: \{[^]*?payload: \+ #(\d+)\[([^\]]*)\]/.exec(line)
    if (!c || !r) continue
    const createdId = c[1], createdParentId = c[3], reorderPayloadId = r[1]
    if (createdParentId !== reorderPayloadId) continue
    // Same numbers alone are only a candidate. CONFIRMED needs the created item to show up in the
    // order of a folder that is NOT its own parent (unrelated order contamination).
    const contaminatedFolders = orderRecords.filter(x => x.folder !== createdParentId && x.ids.includes(createdId)).map(x => x.folder)
    hits.push({
      createdParentId, reorderPayloadId, reorderPayloadTitle: r[2], createdTitle: c[2],
      status: contaminatedFolders.length ? 'CONFIRMED' : 'CANDIDATE', contaminatedFolders
    })
  }
  return hits
}


// ---- fix round 1 -----------------------------------------------------------------------------

/**
 * Cleanup step that removes the run directory only when every server process using it is confirmed
 * exited. A live server's data directory is never deleted: the step fails and names the retained path.
 */
export function guardedRunRemoval(dir, servers, { root = SYNC_ROOT } = {}) {
  return async () => {
    const alive = servers.filter(sv => !sv.exited())
    if (alive.length) {
      throw new Error('run directory RETAINED at ' + dir + ': server process ' + alive.map(a => a.pid).join(', ') + ' not confirmed stopped')
    }
    return removeRunRoot(dir, { root })
  }
}

/** Combine cleanup results (outer run plus every per-profile cleanup); one failure makes the whole unclean. */
export function mergeCleanup(results) {
  const failures = []
  for (const r of results) {
    if (!r || typeof r.clean !== 'boolean' || !Array.isArray(r.failures)) throw new Error('missing cleanup result')
    failures.push(...r.failures)
  }
  return { clean: failures.length === 0, failures }
}

/**
 * Verdict for one floccus sync. Needs evidence: the RPC completed, and the credential made requests
 * in this sync's window. 'success' also requires no floccus error. 'rejected403' (an expected read-only
 * rejection) requires a real 403 from the server for the credential; silence is not a rejection.
 */
export function assessSync({ res, entries, expect }) {
  if (expect !== 'success' && expect !== 'rejected403') throw new Error('unknown expectation ' + expect)
  const reasons = []
  if (!res || res.done !== true) reasons.push('RPC did not complete (' + (res?.reason ?? 'no result') + ')')
  if (!entries || entries.length === 0) reasons.push('no request from this credential in the sync window')
  if (expect === 'success' && res?.status?.error) reasons.push('floccus error: ' + String(res.status.error).slice(0, 120))
  if (expect === 'rejected403' && !(entries ?? []).some(e => e.status === 403)) reasons.push('no 403 observed from the server')
  return { ok: reasons.length === 0, reasons }
}

// ---- fix round 2 -----------------------------------------------------------------------------

/**
 * Start a server through `start` and register its handle. If startup fails with a process that could not
 * be stopped (processStillRunning), an always-alive pseudo handle is registered before rethrowing, so
 * guardedRunRemoval refuses to delete data that process may still be using.
 */
export async function startRegisteredServer(start, liveServers) {
  try {
    const srv = await start()
    liveServers.push(srv)
    return srv
  } catch (e) {
    if (e && e.processStillRunning) liveServers.push({ pid: e.pid, exited: () => false })
    throw e
  }
}

/** Kill a process by pid and wait until it is really gone (throws if it is still alive after timeoutMs). */
export async function killPidAndWait(pid, { timeoutMs = 5000 } = {}) {
  try { process.kill(pid) } catch (e) { if (e.code === 'ESRCH') return }
  const end = Date.now() + timeoutMs
  for (;;) {
    try { process.kill(pid, 0) } catch (e) { if (e.code === 'ESRCH') return }
    if (Date.now() > end) throw new Error('process ' + pid + ' still alive after ' + timeoutMs + 'ms')
    await new Promise(r => setTimeout(r, 50))
  }
}

/**
 * Run body; whatever happens (including a failing assertion) kill and await the child whose pid
 * getPid() returns. Used by tests that deliberately leave a process running.
 */
export async function withProcessCleanup(getPid, body) {
  let bodyError
  try { return await body() } catch (e) { bodyError = e; throw e } finally {
    const pid = getPid()
    if (pid) {
      try { await killPidAndWait(pid) } catch (ce) {
        if (bodyError) bodyError.cleanupError = ce.message
        else throw ce
      }
    }
  }
}
