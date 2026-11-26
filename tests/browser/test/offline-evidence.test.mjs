// Unit tests for the offline acceptance evidence/cleanup helpers (no browser, no server).
import { test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import {
  classifyRun, OfflineRecorder, parseFailsafeError, wouldTripFailsafe, bookmarkIdOfPath, lastAcceptedWrite, acceptedWrites,
  assessAutomaticContinuation, flatBookmarks, duplicateUrls, compareBookmarkSets, titleOf, pollUntil, assertRestoreTarget
} from '../lib/offline-evidence.mjs'
import { validateAccountSettings, makeOfflineRunRoot, guardedOfflineRemoval, OFFLINE_ROOT } from '../lib/offline-session.mjs'

const REST = '/index.php/apps/bookmarks/public/rest/v2'
const e = (method, p, credential, status = 200) => ({ method, path: REST + p, credential, status })

test('classifyRun orders FAIL > BLOCKED > PENDING > NOT_RUN > PASS and rejects unknown statuses', () => {
  assert.deepEqual(classifyRun([]), { status: 'NOT_RUN', code: 2 })
  assert.deepEqual(classifyRun(['PASS', 'PASS']), { status: 'PASS', code: 0 })
  assert.deepEqual(classifyRun(['PASS', 'PENDING']), { status: 'PENDING', code: 4 })
  assert.deepEqual(classifyRun(['PENDING', 'BLOCKED']), { status: 'BLOCKED', code: 3 })
  assert.deepEqual(classifyRun(['BLOCKED', 'FAIL', 'PASS']), { status: 'FAIL', code: 1 })
  assert.deepEqual(classifyRun(['PASS', 'NOT_RUN']), { status: 'NOT_RUN', code: 2 })
  assert.throws(() => classifyRun(['OK']), /unknown status/)
})

test('recorder: passSync without real evidence fails, observations never change the verdict, secrets redacted', () => {
  const lines = []
  const r = new OfflineRecorder({ log: l => lines.push(l) })
  r.addSecret('super-secret-value-123')
  r.passSync('no requests', { serverRequests: 0, nativeTree: {} })
  r.passSync('no tree', { serverRequests: 3 })
  assert.deepEqual(r.cases.map(c => c.status), ['FAIL', 'FAIL'])
  const ok = new OfflineRecorder({ log: () => {} })
  ok.passSync('real', { serverRequests: 2, nativeTree: { title: 'x' } })
  ok.observe('fact', 'token super-secret-value-123 and bms1.12.' + 'a'.repeat(43))
  assert.equal(ok.finish().status, 'PASS')
  assert.equal(ok.observations.length, 1)
  assert.doesNotMatch(ok.observations[0].fact, /bms1\.12\.a/)
  r.fail('leak?', 'value super-secret-value-123')
  assert.doesNotMatch(lines.join('\n'), /super-secret-value-123/)
  assert.throws(() => r.record('x', 'MAYBE'), /unknown status/)
})

test('parseFailsafeError reads the real floccus 5.13.0 failsafe messages and ignores other errors', () => {
  const e29 = 'E029: Failsafe: The current sync run would delete 75% of your links on the server. Refusing to execute. Disable this failsafe in the profile settings if you want to proceed anyway.'
  assert.deepEqual(parseFailsafeError(e29), { code: 29, kind: 'serverside-deletion', percent: 75 })
  assert.deepEqual(parseFailsafeError('E050: Failsafe: The current sync run would delete 40% of your local links in this profile.'), { code: 50, kind: 'clientside-deletion', percent: 40 })
  assert.equal(parseFailsafeError('E017: Network error: Check your network connection'), null)
  assert.equal(parseFailsafeError(undefined), null)
})

test('wouldTripFailsafe mirrors floccus 5.13.0 thresholds (total > 5 and > 20%, or > 1000)', () => {
  assert.equal(wouldTripFailsafe({ total: 8, changed: 1 }), false) // 12.5%
  assert.equal(wouldTripFailsafe({ total: 8, changed: 7 }), true)
  assert.equal(wouldTripFailsafe({ total: 5, changed: 5 }), false) // total not > 5
  assert.equal(wouldTripFailsafe({ total: 10, changed: 2 }), false) // exactly 20% is not above
  assert.equal(wouldTripFailsafe({ total: 100000, changed: 1001 }), true)
})

test('lastAcceptedWrite picks the last 2xx write addressing the bookmark, in log order, across credentials', () => {
  assert.equal(bookmarkIdOfPath(REST + '/bookmark/12'), 12)
  assert.equal(bookmarkIdOfPath(REST + '/folder/-3/bookmarks/12'), null)
  assert.equal(bookmarkIdOfPath(REST + '/bookmark/120'), 120)
  const log = [
    e('POST', '/lock', 2), e('PUT', '/bookmark/12', 2), e('DELETE', '/lock', 2),
    e('PUT', '/bookmark/12', 3, 409), e('GET', '/bookmark/12', 3), e('PUT', '/bookmark/120', 3), e('POST', '/lock', 3)
  ]
  const w = lastAcceptedWrite(log, 12)
  assert.equal(w.credential, 2)
  assert.equal(w.index, 1)
  log.push(e('PUT', '/bookmark/12', 3, 200))
  assert.equal(lastAcceptedWrite(log, 12).credential, 3)
  assert.equal(lastAcceptedWrite(log, 99), null)
  assert.deepEqual(acceptedWrites(log, 3, REST), ['PUT /bookmark/120 200', 'PUT /bookmark/12 200'])
})

test('assessAutomaticContinuation demands a full sync per credential, fresh lastSync, no error and zero RPCs', () => {
  const restartAt = 1000
  const full = c => [e('GET', '/folder/-1/children', c), e('POST', '/lock', c), e('DELETE', '/lock', c)]
  const okStatuses = { A: { lastSync: 2000 }, B: { lastSync: 3000 } }
  const good = assessAutomaticContinuation({ entries: [...full(2), ...full(3)], credentials: { A: 2, B: 3 }, statuses: okStatuses, restartAt, rpcCallsAfterRestart: 0 })
  assert.equal(good.ok, true, good.reasons.join('; '))
  const rpc = assessAutomaticContinuation({ entries: [...full(2), ...full(3)], credentials: { A: 2, B: 3 }, statuses: okStatuses, restartAt, rpcCallsAfterRestart: 1 })
  assert.equal(rpc.ok, false)
  assert.match(rpc.reasons.join(), /RPC/)
  const silent = assessAutomaticContinuation({ entries: full(2), credentials: { A: 2, B: 3 }, statuses: okStatuses, restartAt, rpcCallsAfterRestart: 0 })
  assert.match(silent.reasons.join(), /B: no request/)
  const noLock = assessAutomaticContinuation({ entries: [...full(2), e('GET', '/folder/-1/children', 3)], credentials: { A: 2, B: 3 }, statuses: okStatuses, restartAt, rpcCallsAfterRestart: 0 })
  assert.match(noLock.reasons.join(), /B: no complete lock\/unlock/)
  const stale = assessAutomaticContinuation({ entries: [...full(2), ...full(3)], credentials: { A: 2, B: 3 }, statuses: { A: { lastSync: 500 }, B: { lastSync: 3000, error: 'E029: Failsafe' } }, restartAt, rpcCallsAfterRestart: 0 })
  assert.match(stale.reasons.join(), /A: lastSync not after restart/)
  assert.match(stale.reasons.join(), /B: account error/)
})

test('tree helpers flatten, detect duplicates and compare unordered bookmark sets', () => {
  const t = { title: 'root', children: [{ title: 'a', url: 'u1' }, { title: 'f', children: [{ title: 'b', url: 'u2' }] }] }
  assert.deepEqual(flatBookmarks(t), [{ title: 'a', url: 'u1' }, { title: 'b', url: 'u2' }])
  assert.deepEqual(flatBookmarks(t.children), flatBookmarks(t))
  assert.equal(titleOf(t, 'u2'), 'b')
  assert.equal(titleOf(t, 'nope'), null)
  assert.deepEqual(duplicateUrls({ children: [{ title: 'a', url: 'u' }, { title: 'b', url: 'u' }] }), ['u'])
  const s = { children: [{ title: 'b', url: 'u2' }, { title: 'a', url: 'u1' }] }
  assert.equal(compareBookmarkSets(t, s).equal, true)
  const d = compareBookmarkSets(t, { children: [{ title: 'a2', url: 'u1' }] })
  assert.equal(d.equal, false)
  assert.deepEqual(d.onlyB, ['a2 u1'])
  assert.equal(compareBookmarkSets(null, { children: [] }).equal, true)
})

test('pollUntil is bounded and reports elapsed time without throwing', async () => {
  let now = 0
  const clock = { now: () => now, sleep: async ms => { now += ms } }
  let n = 0
  const hit = await pollUntil(async () => (++n === 3 ? 'v' : null), { timeoutMs: 10000, intervalMs: 1000, ...clock })
  assert.deepEqual(hit, { ok: true, value: 'v', elapsedMs: 2000 })
  now = 0
  const miss = await pollUntil(async () => null, { timeoutMs: 5000, intervalMs: 2000, ...clock })
  assert.equal(miss.ok, false)
  assert.ok(miss.elapsedMs >= 5000 && miss.elapsedMs <= 6000)
})

test('account settings writes are limited to options-page fields and steps', () => {
  assert.deepEqual(validateAccountSettings({ syncIntervalEnabled: true, syncInterval: 5 }), { syncIntervalEnabled: true, syncInterval: 5 })
  assert.throws(() => validateAccountSettings({ syncInterval: 1 }), /options-page step/)
  assert.throws(() => validateAccountSettings({ password: 'x' }), /refusing/)
  assert.throws(() => validateAccountSettings({ localRoot: '1' }), /refusing/)
  assert.throws(() => validateAccountSettings({ enabled: 'yes' }), /boolean/)
  assert.throws(() => validateAccountSettings({}), /no account fields/)
})

test('restore target must be absent and inside the marked run directory; run dir removal waits for servers', async () => {
  const dir = makeOfflineRunRoot()
  try {
    assert.ok(dir.startsWith(OFFLINE_ROOT))
    assert.equal(assertRestoreTarget(path.join(dir, 'restored'), dir), path.join(dir, 'restored'))
    fs.mkdirSync(path.join(dir, 'exists'))
    assert.throws(() => assertRestoreTarget(path.join(dir, 'exists'), dir), /already exists/)
    assert.throws(() => assertRestoreTarget(path.join(dir, '..', 'escape'), dir), /outside the run directory/)
    assert.throws(() => assertRestoreTarget(dir, dir), /outside the run directory/)
    const live = { pid: 4242, exited: () => false }
    await assert.rejects(guardedOfflineRemoval(dir, [live])(), /RETAINED/)
    assert.ok(fs.existsSync(dir))
    assert.equal(await guardedOfflineRemoval(dir, [{ pid: 4242, exited: () => true }])(), true)
    assert.equal(fs.existsSync(dir), false)
  } finally {
    if (fs.existsSync(dir)) fs.rmSync(dir, { recursive: true, force: true })
  }
})

test('run dir removal refuses an unmarked directory under the offline root', async () => {
  fs.mkdirSync(OFFLINE_ROOT, { recursive: true })
  const unmarked = fs.mkdtempSync(path.join(OFFLINE_ROOT, 'unmarked-'))
  try {
    await assert.rejects(guardedOfflineRemoval(unmarked, [])(), /unmarked/)
    assert.ok(fs.existsSync(unmarked))
  } finally { fs.rmSync(unmarked, { recursive: true, force: true }) }
})
