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
export async function startProcessServer({ command, args = [], env = {}, cwd, readyUrl, startupTimeoutMs = 30000, secrets = [], onSpawn }) {
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
    killTree(child)
    await withTimeout(exitP, 8000, 'server process exit')
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
    try { await stop() } catch {}
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
  for (const line of lines) {
    if (!String(line).includes('Inserting created item into order')) continue
    const c = /creation: \{[^}]*?payload: [-+] #[^\[]*\[([^\]]*)\][^]*?parentId: (\d+)/.exec(line)
    const r = /reorder: \{[^]*?payload: \+ #(\d+)\[([^\]]*)\]/.exec(line)
    if (!c || !r) continue
    const createdParentId = c[2], reorderPayloadId = r[1]
    if (createdParentId === reorderPayloadId) {
      hits.push({ createdParentId, reorderPayloadId, reorderPayloadTitle: r[2], createdTitle: c[1] })
    }
  }
  return hits
}
