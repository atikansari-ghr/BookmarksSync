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
