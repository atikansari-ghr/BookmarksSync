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
