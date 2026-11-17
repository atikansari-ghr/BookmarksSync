import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { makeProfileDir, removeProfileDir } from '../lib/harness.mjs'
import { closeAndCleanFirefox, verifySha256, FirefoxHarnessError } from '../lib/firefox-harness.mjs'

const newProfile = () => makeProfileDir('unit-firefox-cleanup')
const forceRemove = d => { try { removeProfileDir(d) } catch {} }
const okDriver = () => ({ quit: async () => {} })
const fast = { quitTimeoutMs: 100, exitWaitMs: 100, pollMs: 10 }

test('success: quits driver, confirms browser gone, then deletes the profile', async () => {
  const dir = newProfile()
  const order = []
  const r = await closeAndCleanFirefox(
    { driver: { quit: async () => { order.push('quit') } }, profileDir: dir, pid: 4242 },
    { ...fast, isAlive: () => { order.push('alive?'); return false }, remove: d => { order.push('remove'); return removeProfileDir(d) } })
  assert.equal(r, true)
  assert.deepEqual(order, ['quit', 'alive?', 'remove'])
  assert.equal(fs.existsSync(dir), false)
})

test('driver.quit rejection fails; profile retained and path reported; no deletion attempted', async () => {
  const dir = newProfile()
  let removed = false
  try {
    await assert.rejects(
      closeAndCleanFirefox({ driver: { quit: async () => { throw new Error('boom') } }, profileDir: dir, pid: 1 },
        { ...fast, isAlive: () => false, remove: () => { removed = true; return true } }),
      e => e instanceof FirefoxHarnessError && e.profileRetained === dir && /boom/.test(e.message) && /retained/i.test(e.message))
    assert.equal(removed, false)
    assert.equal(fs.existsSync(dir), true)
  } finally { forceRemove(dir) }
})

test('driver.quit timeout fails; profile retained', async () => {
  const dir = newProfile()
  try {
    await assert.rejects(
      closeAndCleanFirefox({ driver: { quit: () => new Promise(() => {}) }, profileDir: dir, pid: 1 }, { ...fast, isAlive: () => false }),
      e => e.profileRetained === dir && /timeout/i.test(e.message))
    assert.equal(fs.existsSync(dir), true)
  } finally { forceRemove(dir) }
})

test('browser process still alive after quit: profile retained', async () => {
  const dir = newProfile()
  try {
    await assert.rejects(
      closeAndCleanFirefox({ driver: okDriver(), profileDir: dir, pid: 77 }, { ...fast, isAlive: () => true }),
      e => e.profileRetained === dir && /still running/i.test(e.message))
    assert.equal(fs.existsSync(dir), true)
  } finally { forceRemove(dir) }
})

test('browser that exits within the wait window is accepted', async () => {
  const dir = newProfile()
  let polls = 0
  const r = await closeAndCleanFirefox({ driver: okDriver(), profileDir: dir, pid: 77 },
    { ...fast, exitWaitMs: 1000, isAlive: () => ++polls < 3 })
  assert.equal(r, true)
  assert.ok(polls >= 3)
})

test('unknown pid (cannot prove browser exited) fails closed and retains profile', async () => {
  const dir = newProfile()
  try {
    await assert.rejects(
      closeAndCleanFirefox({ driver: okDriver(), profileDir: dir }, { ...fast, isAlive: () => false }),
      e => e.profileRetained === dir && /pid/i.test(e.message))
    assert.equal(fs.existsSync(dir), true)
  } finally { forceRemove(dir) }
})

test('launch failure with no driver but a known pid still checks the process before deleting', async () => {
  const dir = newProfile()
  try {
    await assert.rejects(
      closeAndCleanFirefox({ driver: null, profileDir: dir, pid: 9 }, { ...fast, isAlive: () => true }),
      e => e.profileRetained === dir)
  } finally { forceRemove(dir) }
})

test('deletion failure fails and reports the retained profile', async () => {
  const dir = newProfile()
  try {
    await assert.rejects(
      closeAndCleanFirefox({ driver: okDriver(), profileDir: dir, pid: 1 },
        { ...fast, isAlive: () => false, remove: () => { throw new Error('EBUSY') } }),
      e => e.profileRetained === dir && /EBUSY/.test(e.message))
  } finally { forceRemove(dir) }
})

test('deletion reporting incomplete removal fails', async () => {
  const dir = newProfile()
  try {
    await assert.rejects(
      closeAndCleanFirefox({ driver: okDriver(), profileDir: dir, pid: 1 }, { ...fast, isAlive: () => false, remove: () => false }),
      e => e.profileRetained === dir && /incomplete/i.test(e.message))
  } finally { forceRemove(dir) }
})

test('refuses to delete a directory outside the harness root even when the browser is gone', async () => {
  const outside = fs.mkdtempSync(path.join(os.tmpdir(), 'ff-outside-'))
  fs.writeFileSync(path.join(outside, 'keep.txt'), 'x')
  try {
    await assert.rejects(
      closeAndCleanFirefox({ driver: okDriver(), profileDir: outside, pid: 1 }, { ...fast, isAlive: () => false }),
      e => e.profileRetained === outside && /refusing/i.test(e.message))
    assert.equal(fs.existsSync(path.join(outside, 'keep.txt')), true)
  } finally { fs.rmSync(outside, { recursive: true, force: true }) }
})

test('error text never includes profile contents', async () => {
  const dir = newProfile()
  fs.writeFileSync(path.join(dir, 'prefs.js'), 'SECRET-PASSWORD-VALUE')
  try {
    await assert.rejects(
      closeAndCleanFirefox({ driver: okDriver(), profileDir: dir, pid: 1 }, { ...fast, isAlive: () => true }),
      e => !e.message.includes('SECRET-PASSWORD-VALUE') && !String(e.stack).includes('SECRET-PASSWORD-VALUE'))
  } finally { forceRemove(dir) }
})

test('verifySha256 accepts a matching digest and rejects a mismatch without echoing contents', () => {
  const f = path.join(os.tmpdir(), `ff-sha-${process.pid}.bin`)
  fs.writeFileSync(f, 'hello')
  try {
    const good = '2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824'
    assert.equal(verifySha256(f, good), good)
    assert.throws(() => verifySha256(f, '0'.repeat(64)), /sha256 mismatch/)
  } finally { fs.rmSync(f, { force: true }) }
})
