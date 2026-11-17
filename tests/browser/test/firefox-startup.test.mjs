import test from 'node:test'
import assert from 'node:assert/strict'
import { startOwnedDriver, FirefoxHarnessError } from '../lib/firefox-harness.mjs'

const session = pid => ({ getCapabilities: () => ({ get: k => (k === 'moz:processID' ? pid : undefined) }) })
const sleep = ms => new Promise(r => setTimeout(r, ms))
const opts = { startupTimeoutMs: 60, lateWaitMs: 150, quitTimeoutMs: 100 }
function fake({ delay = 0, reject = false, pid = 4242, quitHangs = false } = {}) {
  const calls = { quit: 0, kill: 0 }
  const driver = {
    getSession: () => new Promise((res, rej) => setTimeout(() => (reject ? rej(new Error('start failed')) : res(session(pid))), delay)),
    quit: () => { calls.quit++; return quitHangs ? new Promise(() => {}) : Promise.resolve() }
  }
  const service = { kill: () => { calls.kill++; return Promise.resolve() } }
  return { create: () => ({ driver, service }), calls }
}
// No test may deliver a real signal to any process: only signal 0 (liveness probes) is allowed.
const guardKill = () => {
  const real = process.kill, sent = []
  process.kill = (pid, sig) => { if (sig !== 0) sent.push([pid, sig]); return real.call(process, pid, sig) }
  return { sent, restore: () => { process.kill = real } }
}

test('prompt startup returns the owned driver, service and pid', async () => {
  const f = fake({ delay: 5 })
  const r = await startOwnedDriver({ create: f.create, ...opts })
  assert.equal(r.pid, 4242)
  assert.equal(f.calls.quit, 0)
})

test('delayed startup past the timeout: late session is quit, service killed, pid preserved on the error', async () => {
  const g = guardKill()
  try {
    const f = fake({ delay: 100, pid: 5151 })
    await assert.rejects(startOwnedDriver({ create: f.create, ...opts }),
      e => e instanceof FirefoxHarnessError && /timeout/i.test(e.message) && e.owned.pid === 5151)
    assert.equal(f.calls.quit, 1)
    assert.ok(f.calls.kill >= 1)
    assert.deepEqual(g.sent, [])
  } finally { g.restore() }
})

test('hung startup: bounded, service killed, no pid guessed, no process signalled', async () => {
  const g = guardKill()
  try {
    const f = fake({ delay: 10_000 })
    const t0 = Date.now()
    await assert.rejects(startOwnedDriver({ create: f.create, ...opts }), e => e.owned.pid === undefined)
    assert.ok(Date.now() - t0 < 1500)
    assert.ok(f.calls.kill >= 1)
    assert.deepEqual(g.sent, [])
  } finally { g.restore() }
})

test('startup that completes only after we gave up is still shut down (no orphaned browser)', async () => {
  const f = fake({ delay: 260 })
  await assert.rejects(startOwnedDriver({ create: f.create, ...opts }))
  await sleep(400)
  assert.equal(f.calls.quit, 1)
  assert.ok(f.calls.kill >= 1)
})

test('startup rejection kills the service and does not quit a session that never existed', async () => {
  const f = fake({ delay: 5, reject: true })
  await assert.rejects(startOwnedDriver({ create: f.create, ...opts }), /start failed/)
  assert.equal(f.calls.quit, 0)
  assert.ok(f.calls.kill >= 1)
})

test('late quit that hangs is bounded and service is still killed', async () => {
  const f = fake({ delay: 100, quitHangs: true })
  const t0 = Date.now()
  await assert.rejects(startOwnedDriver({ create: f.create, ...opts }))
  assert.ok(Date.now() - t0 < 1500)
  assert.ok(f.calls.kill >= 1)
})

// --- round 2: a failed late quit must survive into cleanup even if the browser later exits ---
import fs from 'node:fs'
import { makeProfileDir, removeProfileDir } from '../lib/harness.mjs'
import { cleanupAfterFailedStart } from '../lib/firefox-harness.mjs'

test('late quit rejection is recorded on the startup error', async () => {
  const f = fake({ delay: 100 })
  f.create().driver // shape check only
  const calls = f.calls
  const create = () => { const c = f.create(); c.driver.quit = () => { calls.quit++; return Promise.reject(new Error('quit boom')) }; return c }
  await assert.rejects(startOwnedDriver({ create, ...opts }), e => /quit boom/.test(e.owned.quitError?.message))
})

test('late quit timeout is recorded on the startup error', async () => {
  const f = fake({ delay: 100, quitHangs: true })
  await assert.rejects(startOwnedDriver({ create: f.create, ...opts }), e => /timeout/i.test(e.owned.quitError?.message))
})

test('failed late quit + browser that later exits: profile RETAINED and cleanup failure reported', async () => {
  const dir = makeProfileDir('unit-late-quit')
  let removed = false
  try {
    const owned = { driver: undefined, service: { kill: async () => {} }, pid: 5151, quitError: new Error('quit boom') }
    await assert.rejects(
      cleanupAfterFailedStart({ owned, profileDir: dir }, { exitWaitMs: 50, pollMs: 5, isAlive: () => false, remove: d => { removed = true; return removeProfileDir(d) } }),
      e => e instanceof FirefoxHarnessError && e.profileRetained === dir && /quit boom/.test(e.message) && /retained/i.test(e.message))
    assert.equal(removed, false)
    assert.equal(fs.existsSync(dir), true)
  } finally { try { removeProfileDir(dir) } catch {} }
})

test('clean failed start (no quit error, browser gone) still deletes the profile', async () => {
  const dir = makeProfileDir('unit-clean-fail')
  const owned = { driver: undefined, service: { kill: async () => {} }, pid: 5151 }
  assert.equal(await cleanupAfterFailedStart({ owned, profileDir: dir }, { exitWaitMs: 50, pollMs: 5, isAlive: () => false }), true)
  assert.equal(fs.existsSync(dir), false)
})
