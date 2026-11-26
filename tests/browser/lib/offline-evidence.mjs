// Pure evidence helpers for the offline/failsafe/restore acceptance run (offline-sync.mjs).
// Nothing here talks to a browser or a server; every function is unit-tested in
// test/offline-evidence.test.mjs. Verdict rules:
//  - a case is PASS only with concrete evidence (real requests + native/server tree checks);
//  - observations record what actually happened and never change the verdict;
//  - PENDING means the behaviour was not observed inside the bounded window (never a pass);
//  - one FAIL (including cleanup) fails the run.
import fs from 'node:fs'
import path from 'node:path'
import { sanitize } from './server-sync.mjs'
import { isContained } from './harness.mjs'
const isContainedPath = isContained

export const STATUSES = ['PASS', 'FAIL', 'BLOCKED', 'PENDING', 'NOT_RUN']
const CODES = { PASS: 0, FAIL: 1, NOT_RUN: 2, BLOCKED: 3, PENDING: 4 }

/** Overall status: FAIL > BLOCKED > PENDING > NOT_RUN > PASS; no cases is NOT_RUN. */
export function classifyRun(statuses) {
  for (const s of statuses) if (!STATUSES.includes(s)) throw new Error(`unknown status ${s}`)
  const order = ['FAIL', 'BLOCKED', 'PENDING', 'NOT_RUN']
  if (statuses.length === 0) return { status: 'NOT_RUN', code: CODES.NOT_RUN }
  for (const s of order) if (statuses.includes(s)) return { status: s, code: CODES[s] }
  return { status: 'PASS', code: CODES.PASS }
}

export class OfflineRecorder {
  constructor({ log = console.log, secrets = [] } = {}) {
    this.cases = []; this.observations = []; this.log = log; this.secrets = secrets
  }
  addSecret(s) { if (typeof s === 'string' && s.length >= 8) this.secrets.push(s) }
  record(name, status, detail = '') {
    if (!STATUSES.includes(status)) throw new Error(`unknown status ${status}`)
    const d = sanitize(typeof detail === 'string' ? detail : JSON.stringify(detail), this.secrets)
    this.cases.push({ name, status, detail: d })
    this.log(`${status.padEnd(8)} ${name}${d ? ' - ' + d : ''}`)
  }
  pass(n, d) { this.record(n, 'PASS', d) }
  fail(n, d) { this.record(n, 'FAIL', d) }
  blocked(n, d) { this.record(n, 'BLOCKED', d) }
  pending(n, d) { this.record(n, 'PENDING', d) }
  notRun(n, d) { this.record(n, 'NOT_RUN', d) }
  /** A sync PASS needs real server requests from the credential AND a native tree that was read. */
  passSync(name, evidence = {}, detail = '') {
    if (!(evidence.serverRequests > 0) || !evidence.nativeTree) {
      return this.fail(name, `no real sync evidence (serverRequests=${evidence.serverRequests ?? 0}, nativeTree=${!!evidence.nativeTree})`)
    }
    this.pass(name, detail)
  }
  /** Recorded fact, not a verdict. */
  observe(name, fact) {
    const f = sanitize(typeof fact === 'string' ? fact : JSON.stringify(fact), this.secrets)
    this.observations.push({ name, fact: f })
    this.log(`OBSERVED ${name} - ${f}`)
  }
  finish() { return classifyRun(this.cases.map(c => c.status)) }
}

// ---- floccus failsafe ------------------------------------------------------------

const FAILSAFE = {
  29: 'serverside-deletion', 43: 'serverside-addition', 49: 'clientside-addition', 50: 'clientside-deletion'
}

/** Parse a floccus 5.13.0 failsafe error ("E029: Failsafe: ... delete 75% ..."); null if not a failsafe error. */
export function parseFailsafeError(text) {
  const m = /\bE0(29|43|49|50)\b[^]*?Failsafe[^]*?(\d+)\s?%/.exec(String(text ?? ''))
  if (!m) return null
  return { code: Number(m[1]), kind: FAILSAFE[Number(m[1])], percent: Number(m[2]) }
}

/**
 * floccus 5.13.0 thresholds (non-tab profiles): failsafe trips when total > 5 and
 * changed/total > 0.2, or changed > 1000. Used to plan data so a case lands on the
 * intended side of the threshold; the actual verdict always comes from the real extension.
 */
export function wouldTripFailsafe({ total, changed }) {
  return (total > 5 && changed / total > 0.2) || changed > 1000
}

// ---- request log evidence ----------------------------------------------------------

const isLock = e => /\/lock$/.test(e.path) && (e.method === 'POST' || e.method === 'DELETE')
export const isWrite = e => e.method !== 'GET' && !isLock(e)
const ok = e => e.status >= 200 && e.status < 300

/** Bookmark id addressed by a request path (…/bookmark/<id>[/…]), else null. */
export function bookmarkIdOfPath(p) {
  const m = /\/bookmark\/(\d+)(?:$|[/?])/.exec(String(p))
  return m ? Number(m[1]) : null
}

/**
 * Last accepted (2xx) write that addressed one server bookmark, across credentials, in log order.
 * entries: parsed request log ([{method,path,credential,status}]) of ONE server process, in order.
 */
export function lastAcceptedWrite(entries, bookmarkId) {
  let last = null
  entries.forEach((e, i) => {
    if (isWrite(e) && ok(e) && bookmarkIdOfPath(e.path) === bookmarkId) last = { ...e, index: i }
  })
  return last
}

/** Accepted writes per credential, compact strings for the report. */
export function acceptedWrites(entries, credential, rest = '') {
  return entries.filter(e => e.credential === credential && isWrite(e) && ok(e))
    .map(e => `${e.method} ${e.path.replace(rest, '')} ${e.status}`)
}

/**
 * Automatic continuation verdict after a server restart. Needs, for every credential: at least one
 * request to the restarted server, a lock + unlock pair (a full floccus sync run), a lastSync after the
 * restart and no account error — and the harness must not have issued any sync RPC after the restart.
 */
export function assessAutomaticContinuation({ entries, credentials, statuses, restartAt, rpcCallsAfterRestart }) {
  const reasons = []
  const per = {}
  if (rpcCallsAfterRestart !== 0) reasons.push(`harness issued ${rpcCallsAfterRestart} sync RPC(s) after restart`)
  for (const [label, cred] of Object.entries(credentials)) {
    const mine = entries.filter(e => e.credential === cred)
    const locked = mine.some(e => e.method === 'POST' && /\/lock$/.test(e.path) && ok(e))
    const unlocked = mine.some(e => e.method === 'DELETE' && /\/lock$/.test(e.path) && ok(e))
    const st = statuses[label] ?? {}
    per[label] = { requests: mine.length, locked, unlocked, lastSync: st.lastSync ?? null, error: st.error ?? null, syncing: !!st.syncing }
    if (mine.length === 0) reasons.push(`${label}: no request to the restarted server`)
    else if (!locked || !unlocked) reasons.push(`${label}: no complete lock/unlock sync run`)
    if (!(st.lastSync > restartAt)) reasons.push(`${label}: lastSync not after restart`)
    if (st.error) reasons.push(`${label}: account error ${String(st.error).slice(0, 80)}`)
  }
  return { ok: reasons.length === 0, reasons, per }
}

// ---- trees -------------------------------------------------------------------------

/** Flat bookmark list [{title,url}] of a stripped/native/server tree (folders recursed, order kept). */
export function flatBookmarks(nodes) {
  const out = []
  const walk = list => { for (const n of list ?? []) { if (n.url) out.push({ title: n.title, url: n.url }); else walk(n.children) } }
  walk(Array.isArray(nodes) ? nodes : nodes?.children)
  return out
}

/** URLs that occur more than once. */
export function duplicateUrls(nodes) {
  const c = {}
  for (const b of flatBookmarks(nodes)) c[b.url] = (c[b.url] ?? 0) + 1
  return Object.keys(c).filter(u => c[u] > 1)
}

/** Unordered set comparison of {title,url}; returns {equal, onlyA, onlyB}. */
export function compareBookmarkSets(a, b) {
  const key = x => `${x.title}\u0000${x.url}`
  const A = flatBookmarks(a).map(key), B = flatBookmarks(b).map(key)
  const onlyA = A.filter(k => !B.includes(k)).map(k => k.replace('\u0000', ' '))
  const onlyB = B.filter(k => !A.includes(k)).map(k => k.replace('\u0000', ' '))
  return { equal: onlyA.length === 0 && onlyB.length === 0 && A.length === B.length, onlyA, onlyB }
}

/** Title for a URL in a tree (first match) or null. */
export function titleOf(nodes, url) {
  return flatBookmarks(nodes).find(b => b.url === url)?.title ?? null
}

// ---- bounded polling ---------------------------------------------------------------

/** Poll fn until it returns a truthy value or the bound passes; never throws for a timeout. */
export async function pollUntil(fn, { timeoutMs, intervalMs = 2000, now = Date.now, sleep = ms => new Promise(r => setTimeout(r, ms)) }) {
  const start = now()
  let value
  for (;;) {
    value = await fn()
    if (value) return { ok: true, value, elapsedMs: now() - start }
    if (now() - start >= timeoutMs) return { ok: false, value, elapsedMs: now() - start }
    await sleep(intervalMs)
  }
}

// ---- restore target guard ----------------------------------------------------------

/** A restore target must be absent and inside the marked run directory (restore never merges). */
export function assertRestoreTarget(target, runDir) {
  const t = path.resolve(target)
  if (!isContainedPath(runDir, t)) throw new Error(`restore target outside the run directory: ${t}`)
  if (fs.existsSync(t)) throw new Error(`restore target already exists: ${t}`)
  return t
}
