import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import { closeAndClean, makeProfileDir, removeProfileDir } from '../lib/harness.mjs'

const fakeContext = (close, connected = false) => ({ close, browser: () => ({ isConnected: () => connected }) })
const newProfile = () => makeProfileDir('unit-cleanup')
const forceRemove = d => { try { removeProfileDir(d) } catch {} }

test('success: closes then deletes the profile', async () => {
  const dir = newProfile()
  const r = await closeAndClean({ context: fakeContext(async () => {}), profileDir: dir })
  assert.equal(r, true)
  assert.equal(fs.existsSync(dir), false)
})

test('close rejection fails, profile is retained and reported', async () => {
  const dir = newProfile()
  try {
    await assert.rejects(
      closeAndClean({ context: fakeContext(async () => { throw new Error('boom') }), profileDir: dir }),
      e => e.profileRetained === dir && /boom/.test(e.message) && /retained/i.test(e.message))
    assert.equal(fs.existsSync(dir), true)
  } finally { forceRemove(dir) }
})

test('close timeout fails, profile is retained', async () => {
  const dir = newProfile()
  try {
    await assert.rejects(
      closeAndClean({ context: fakeContext(() => new Promise(() => {})), profileDir: dir }, { closeTimeoutMs: 50 }),
      e => e.profileRetained === dir && /timeout/i.test(e.message))
    assert.equal(fs.existsSync(dir), true)
  } finally { forceRemove(dir) }
})

test('browser still connected after close resolves: profile is retained', async () => {
  const dir = newProfile()
  try {
    await assert.rejects(
      closeAndClean({ context: fakeContext(async () => {}, true), profileDir: dir }),
      e => e.profileRetained === dir && /still connected/i.test(e.message))
    assert.equal(fs.existsSync(dir), true)
  } finally { forceRemove(dir) }
})

test('deletion failure fails and reports the retained profile', async () => {
  const dir = newProfile()
  try {
    await assert.rejects(
      closeAndClean({ context: fakeContext(async () => {}), profileDir: dir }, { remove: () => { throw new Error('EBUSY') } }),
      e => e.profileRetained === dir && /EBUSY/.test(e.message))
  } finally { forceRemove(dir) }
})

test('deletion reporting incomplete removal fails', async () => {
  const dir = newProfile()
  try {
    await assert.rejects(
      closeAndClean({ context: fakeContext(async () => {}), profileDir: dir }, { remove: () => false }),
      e => e.profileRetained === dir)
  } finally { forceRemove(dir) }
})

// ---- native Chrome (raw CDP) helpers ----
import { EventEmitter } from 'node:events'
import {
  buildChromeNativeArgs, createCdpClient, createServiceWorkerEvaluator, waitForServiceWorkerTarget,
  createOwnedBrowserHandle, parseDevToolsActivePort
} from '../lib/harness.mjs'

class FakeSocket extends EventTarget {
  sent = []
  send(s) { this.sent.push(JSON.parse(s)) }
  close() { this.dispatchEvent(new Event('close')) }
  reply(m) { this.dispatchEvent(new MessageEvent('message', { data: JSON.stringify(m) })) }
}

test('buildChromeNativeArgs: disposable profile flags, loadUnpacked prerequisites, no --load-extension', () => {
  const a = buildChromeNativeArgs({ profileDir: 'P', headless: true })
  assert.ok(a.includes('--user-data-dir=P'))
  assert.ok(a.includes('--remote-debugging-port=0'))
  assert.ok(a.includes('--enable-unsafe-extension-debugging'))
  assert.ok(a.includes('--headless=new'))
  assert.ok(!a.some(x => x.startsWith('--load-extension')))
  assert.ok(!buildChromeNativeArgs({ profileDir: 'P', headless: false }).includes('--headless=new'))
})

test('parseDevToolsActivePort reads the port and ws path, rejects junk', () => {
  assert.deepEqual(parseDevToolsActivePort('1234\n/devtools/browser/abc\n'), { port: 1234, path: '/devtools/browser/abc' })
  assert.equal(parseDevToolsActivePort('x'), null)
  assert.equal(parseDevToolsActivePort('0\n/p'), null)
})

test('cdp client correlates replies, surfaces errors, dispatches events, rejects on close', async () => {
  const s = new FakeSocket()
  const c = createCdpClient(s)
  const seen = []
  c.on('Target.targetCreated', p => seen.push(p))
  const ok = c.send('A.b', { x: 1 }, 'sess')
  assert.deepEqual(s.sent[0], { id: 1, method: 'A.b', params: { x: 1 }, sessionId: 'sess' })
  s.reply({ id: 1, result: { v: 2 } })
  assert.deepEqual(await ok, { v: 2 })
  const bad = c.send('A.c')
  s.reply({ id: 2, error: { message: 'nope' } })
  await assert.rejects(bad, /nope/)
  s.reply({ method: 'Target.targetCreated', params: { n: 1 } })
  assert.deepEqual(seen, [{ n: 1 }])
  const pending = c.send('A.d')
  s.close()
  await assert.rejects(pending, /closed/)
})

test('service worker evaluator runs fn(arg) in the attached session and reports honest metadata', async () => {
  const calls = []
  const client = { send: async (m, p, sid) => { calls.push({ m, p, sid }); return { result: { value: 42 } } } }
  const sw = createServiceWorkerEvaluator({ client, sessionId: 'S', url: 'chrome-extension://abc/dist/js/background-script.js' })
  assert.equal(sw.kind, 'cdp-service-worker-target')
  assert.equal(sw.url(), 'chrome-extension://abc/dist/js/background-script.js')
  assert.equal(await sw.evaluate((a) => a + 1, 41), 42)
  assert.equal(calls[0].m, 'Runtime.evaluate')
  assert.equal(calls[0].sid, 'S')
  assert.equal(calls[0].p.awaitPromise, true)
  assert.equal(calls[0].p.returnByValue, true)
  assert.match(calls[0].p.expression, /\(41\)|41/)
})

test('service worker evaluator throws on exceptionDetails', async () => {
  const client = { send: async () => ({ exceptionDetails: { text: 'Uncaught', exception: { description: 'Error: kaboom' } } }) }
  const sw = createServiceWorkerEvaluator({ client, sessionId: 'S', url: 'u' })
  await assert.rejects(sw.evaluate(() => 1), /kaboom/)
})

test('waitForServiceWorkerTarget: finds an existing target, waits for an event, times out', async () => {
  const mk = (url, type = 'service_worker') => ({ targetId: url, type, url })
  const ee = new EventEmitter()
  const client = {
    on: (m, h) => { ee.on(m, h); return () => ee.off(m, h) },
    send: async () => ({ targetInfos: [mk('chrome-extension://other/background.js'), mk('chrome-extension://abc/dist/js/background-script.js', 'page')] })
  }
  const isF = t => t.type === 'service_worker' && t.url.startsWith('chrome-extension://abc/') && t.url.endsWith('/dist/js/background-script.js')
  setTimeout(() => ee.emit('Target.targetCreated', { targetInfo: mk('chrome-extension://abc/dist/js/background-script.js') }), 20)
  const t = await waitForServiceWorkerTarget(client, isF, 2000)
  assert.equal(t.type, 'service_worker')
  const pre = { on: () => () => {}, send: async () => ({ targetInfos: [mk('chrome-extension://abc/dist/js/background-script.js')] }) }
  assert.equal((await waitForServiceWorkerTarget(pre, isF, 50)).type, 'service_worker')
  const none = { on: () => () => {}, send: async () => ({ targetInfos: [] }) }
  await assert.rejects(waitForServiceWorkerTarget(none, isF, 50), /timeout/i)
})

function fakeChild({ exitOnClose = true } = {}) {
  const c = new EventEmitter()
  c.exitCode = null
  c.killed = false
  c.kill = () => { c.killed = true }
  c.exit = (code = 0) => { c.exitCode = code; c.emit('exit', code) }
  return Object.assign(c, { exitOnClose })
}

test('owned browser handle: close awaits process exit; not connected afterwards', async () => {
  const child = fakeChild()
  const client = { send: async (m) => { if (m === 'Browser.close') setTimeout(() => child.exit(0), 10); return {} }, close() {} }
  const h = createOwnedBrowserHandle({ child, client })
  assert.equal(h.browser().isConnected(), true)
  await h.close()
  assert.equal(h.browser().isConnected(), false)
})

test('owned browser handle: process that never exits fails close (and is killed), profile kept via closeAndClean', async () => {
  const dir = newProfile()
  const child = fakeChild()
  const client = { send: async () => ({}), close() {} }
  const h = createOwnedBrowserHandle({ child, client }, { exitTimeoutMs: 50 })
  try {
    await assert.rejects(closeAndClean({ context: h, profileDir: dir }),
      e => e.profileRetained === dir && /did not exit|timeout/i.test(e.message))
    assert.equal(fs.existsSync(dir), true)
    assert.equal(child.killed, true)
  } finally { forceRemove(dir) }
})

test('owned browser handle: Browser.close rejection still waits/kills and fails when process stays up', async () => {
  const child = fakeChild()
  const client = { send: async () => { throw new Error('ws gone') }, close() {} }
  const h = createOwnedBrowserHandle({ child, client }, { exitTimeoutMs: 50 })
  await assert.rejects(h.close(), /did not exit|timeout|ws gone/i)
  assert.equal(h.browser().isConnected(), true)
})

import { waitUntilReady } from '../lib/harness.mjs'

test('waitUntilReady retries thrown/falsy probes until ready, then times out with the last error', async () => {
  let n = 0
  await waitUntilReady(async () => { if (++n < 3) throw new Error('chrome is not defined'); return true }, 2000, 'x', 5)
  assert.equal(n, 3)
  await assert.rejects(waitUntilReady(async () => { throw new Error('still no') }, 40, 'apis', 5), /timeout.*apis.*still no/)
})
