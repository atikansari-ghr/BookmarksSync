// Fix round 1 regressions for the server-sync harness: session init cleanup, live-server data
// protection, evidence-required sync verdicts, per-profile cleanup aggregation, detector wording.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import {
  startProcessServer, pickLoopbackPort, makeRunRoot, assessSync, guardedRunRemoval, mergeCleanup,
  detectIdNamespaceCollision, withProcessCleanup
} from '../lib/server-sync.mjs'
import { openBrowser } from '../lib/browser-session.mjs'

const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), 'bh-fr1-'))

// ---- 1. session initialisation failures must close the owned browser --------------------------

function fakes({ initThrows, closeThrows, family }) {
  const calls = []
  const session = {
    profileDir: '/profiles/p1', driver: {}, extensionUuid: 'u', pageContext: {}, extensionId: 'e', browserVersion: '1',
    serviceWorker: { evaluate: async () => { if (initThrows === 'version') throw new Error('sw evaluate failed'); return '5.13.0' } }
  }
  const deps = {
    launchGecko: async () => { calls.push('launch'); return session },
    launchChromium: async () => { calls.push('launch'); return session },
    openGeckoPage: async () => { calls.push('page'); if (initThrows === 'page') throw new Error('page init failed'); return { version: '5.13.0' } },
    openChromiumPage: async () => { calls.push('page'); if (initThrows === 'page') throw new Error('page init failed'); return {} },
    closeGecko: async s => { calls.push('close'); assert.equal(s, session); if (closeThrows) throw Object.assign(new Error('browser would not close'), { profileRetained: s.profileDir }) ; return true },
    closeChromium: async s => { calls.push('close'); assert.equal(s, session); if (closeThrows) throw Object.assign(new Error('browser would not close'), { profileRetained: s.profileDir }); return true }
  }
  return { deps, calls }
}

for (const [name, family] of [['chrome', 'chromium'], ['firefox', 'gecko']]) {
  const initCases = family === 'chromium' ? ['page', 'version'] : ['page']
  for (const what of initCases) {
    test(`${name}: failure initialising the extension (${what}) closes the owned session before rethrowing`, async () => {
      const { deps, calls } = fakes({ initThrows: what })
      await assert.rejects(openBrowser(name, { deps }), (e) => {
        assert.match(e.message, /page init failed|sw evaluate failed/)
        assert.equal(e.profileCleaned, true)
        return true
      })
      assert.deepEqual(calls.filter(c => c === 'close'), ['close'])
    })
    test(`${name}: failure initialising (${what}) with a failing close retains the profile and reports both errors`, async () => {
      const { deps } = fakes({ initThrows: what, closeThrows: true })
      await assert.rejects(openBrowser(name, { deps }), (e) => {
        assert.match(e.message, /page init failed|sw evaluate failed/)
        assert.equal(e.profileCleaned, false)
        assert.equal(e.profileRetained, '/profiles/p1')
        assert.match(e.cleanupError, /would not close/)
        return true
      })
    })
  }
  test(`${name}: a successful init does not close the session`, async () => {
    const { deps, calls } = fakes({})
    const s = await openBrowser(name, { deps })
    assert.ok(s)
    assert.ok(!calls.includes('close'))
  })
}

// ---- 2. never delete a run directory while a server may still be running --------------------

test('startup failure with an unkillable process reports it, keeps the pid and flags it as still running', async () => {
  const port = await pickLoopbackPort()
  let pid
  // the child is deliberately left running by the code under test; it is always killed and awaited here
  await withProcessCleanup(() => pid, async () => {
    await assert.rejects(startProcessServer({
      command: process.execPath, args: ['-e', 'setInterval(()=>{},1000)'], env: {}, readyUrl: `http://127.0.0.1:${port}/readyz`,
      startupTimeoutMs: 400, stopTimeoutMs: 300, terminate: () => {}, onSpawn: p => { pid = p }
    }), (e) => {
      assert.equal(e.processStillRunning, true)
      assert.equal(e.pid, pid)
      assert.match(e.stopError, /timeout/)
      assert.match(e.message, /could not be stopped/)
      return true
    })
  })
})

test('startup failure with a killable process says the process is gone', async () => {
  const port = await pickLoopbackPort()
  await assert.rejects(startProcessServer({
    command: process.execPath, args: ['-e', 'setInterval(()=>{},1000)'], env: {}, readyUrl: `http://127.0.0.1:${port}/readyz`, startupTimeoutMs: 400
  }), (e) => { assert.equal(e.processStillRunning, false); assert.equal(e.stopError, undefined); return true })
})

test('stop() rejects when the process does not exit', async () => {
  const port = await pickLoopbackPort()
  let srv
  await withProcessCleanup(() => srv?.pid, async () => {
    srv = await startProcessServer({
      command: process.execPath, args: ['-e', `require('http').createServer((q,s)=>s.end('ok')).listen(${port},'127.0.0.1')`], env: {},
      readyUrl: `http://127.0.0.1:${port}/readyz`, stopTimeoutMs: 300, terminate: () => {}
    })
    await assert.rejects(srv.stop(), /timeout/)
    assert.equal(srv.exited(), false)
  })
})

test('guardedRunRemoval keeps the run directory (and says where) while any server is not confirmed stopped', async () => {
  const base = tmp(); const root = path.join(base, 'server-sync')
  const dir = makeRunRoot({ root, toolsDir: base })
  fs.writeFileSync(path.join(dir, 'live.db'), 'live data')
  const step = guardedRunRemoval(dir, [{ pid: 4242, exited: () => true }, { pid: 4343, exited: () => false }], { root })
  await assert.rejects(Promise.resolve().then(step), (e) => {
    assert.match(e.message, /RETAINED/); assert.ok(e.message.includes(dir)); assert.match(e.message, /4343/)
    return true
  })
  assert.ok(fs.existsSync(path.join(dir, 'live.db')))
  const ok = guardedRunRemoval(dir, [{ pid: 1, exited: () => true }], { root })
  assert.equal(await ok(), true)
  assert.ok(!fs.existsSync(dir))
  fs.rmSync(base, { recursive: true, force: true })
})

// ---- 3. sync verdicts need request evidence for the credential and a completed RPC ----------

const E = (method, p, status) => ({ method, path: `/index.php/apps/bookmarks/public/rest/v2${p}`, credential: 7, status })
const done = { done: true, status: {} }

test('assessSync(success) needs a completed RPC, no floccus error and at least one request from the credential', () => {
  const ok = assessSync({ res: done, entries: [E('GET', '/folder/-1/hash', 200)], expect: 'success' })
  assert.equal(ok.ok, true)
  assert.equal(assessSync({ res: done, entries: [], expect: 'success' }).ok, false)
  assert.match(assessSync({ res: done, entries: [], expect: 'success' }).reasons.join(), /no request/)
  assert.equal(assessSync({ res: { done: false, reason: 'timeout' }, entries: [E('GET', '/x', 200)], expect: 'success' }).ok, false)
  assert.equal(assessSync({ res: { done: true, status: { error: 'E035' } }, entries: [E('GET', '/x', 200)], expect: 'success' }).ok, false)
  assert.equal(assessSync({ res: undefined, entries: [E('GET', '/x', 200)], expect: 'success' }).ok, false)
})

test('assessSync(rejected403) needs a real 403 plus a finished RPC; silence is not a rejection', () => {
  const withErr = { done: true, status: { error: 'E018: Couldn\'t authenticate' } }
  assert.equal(assessSync({ res: withErr, entries: [E('POST', '/folder', 403)], expect: 'rejected403' }).ok, true)
  assert.equal(assessSync({ res: done, entries: [E('POST', '/folder', 403)], expect: 'rejected403' }).ok, true)
  // no 403 observed (floccus never tried to write, or never ran): must not pass
  assert.equal(assessSync({ res: done, entries: [E('GET', '/folder/-1/hash', 200)], expect: 'rejected403' }).ok, false)
  assert.equal(assessSync({ res: withErr, entries: [], expect: 'rejected403' }).ok, false)
  assert.equal(assessSync({ res: { done: false }, entries: [E('POST', '/folder', 403)], expect: 'rejected403' }).ok, false)
  assert.match(assessSync({ res: done, entries: [E('GET', '/x', 200)], expect: 'rejected403' }).reasons.join(), /403/)
})

test('assessSync rejects an unknown expectation instead of defaulting to a pass', () => {
  assert.throws(() => assessSync({ res: done, entries: [E('GET', '/x', 200)], expect: 'whatever' }), /unknown expectation/)
})

// ---- 4. per-profile cleanup failures aggregate into the exit status ---------------------------

test('mergeCleanup is clean only when every part is clean and keeps every failure with its retained path', () => {
  assert.deepEqual(mergeCleanup([{ clean: true, failures: [] }, { clean: true, failures: [] }]), { clean: true, failures: [] })
  const m = mergeCleanup([
    { clean: true, failures: [] },
    { clean: false, failures: [{ step: 'close B2', error: 'profile RETAINED at /p/b2: browser still running' }] },
    { clean: false, failures: [{ step: 'stop server', error: 'timeout' }] }
  ])
  assert.equal(m.clean, false)
  assert.deepEqual(m.failures.map(f => f.step), ['close B2', 'stop server'])
  assert.deepEqual(mergeCleanup([]), { clean: true, failures: [] })
  assert.throws(() => mergeCleanup([undefined]), /missing cleanup result/)
})

// ---- minor: the detector must not claim CONFIRMED from equal numbers alone --------------------

const INSERT = "ReconcileReorders: Inserting created item into order { creation: { type: 'CREATE', payload: - #12[O1](https://o1.example/) parentId: 6, oldItem: - #7;7[O1](https://o1.example/) parentId: 7 }, reorder: { type: 'REORDER', oldItem: + #9[Folder F] parentId: 5, payload: + #6[Folder F] parentId: 5, order: [ [Object], [Object] ] } }"
const POLLUTED = "(local)ORDERFOLDER { id: '9', order: [ { type: 'folder', id: '13' }, { type: 'bookmark', id: '12' }, { type: 'bookmark', id: '14' } ] }"
const INNOCENT = "(local)ORDERFOLDER { id: '9', order: [ { type: 'bookmark', id: '14' }, { type: 'bookmark', id: '15' } ] }"

test('detector: same numeric ids alone is only a CANDIDATE', () => {
  const hits = detectIdNamespaceCollision([INSERT, INNOCENT])
  assert.equal(hits.length, 1)
  assert.equal(hits[0].status, 'CANDIDATE')
})

test('detector: CONFIRMED only when the created item appears in the order of a different folder', () => {
  const hits = detectIdNamespaceCollision([INSERT, POLLUTED])
  assert.equal(hits[0].status, 'CONFIRMED')
  assert.deepEqual(hits[0].contaminatedFolders, ['9'])
  // the created item ordered inside its own parent (6) is not contamination
  assert.equal(detectIdNamespaceCollision([INSERT, "(local)ORDERFOLDER { id: '6', order: [ { type: 'bookmark', id: '12' } ] }"])[0].status, 'CANDIDATE')
})
