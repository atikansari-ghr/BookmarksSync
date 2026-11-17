import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { makeProfileDir, removeProfileDir } from '../lib/harness.mjs'

const MARKER = '.bookmarksync-harness-profile'
const link = (target, p) => fs.symlinkSync(target, p, 'junction') // junction on Windows, symlink elsewhere
const sandbox = () => fs.mkdtempSync(path.join(os.tmpdir(), 'bs-contain-'))

test('removeProfileDir refuses a marked entry that is a junction escaping the profiles root', () => {
  const tmp = sandbox()
  try {
    const tools = path.join(tmp, 'tools'), root = path.join(tools, 'browser-profiles'), outside = path.join(tmp, 'outside')
    fs.mkdirSync(root, { recursive: true }); fs.mkdirSync(outside)
    fs.writeFileSync(path.join(outside, MARKER), 'm'); fs.writeFileSync(path.join(outside, 'precious.txt'), 'keep')
    const entry = path.join(root, 'firefox-1-aa'); link(outside, entry)
    assert.throws(() => removeProfileDir(entry, { root }), /refusing/)
    assert.equal(fs.readFileSync(path.join(outside, 'precious.txt'), 'utf8'), 'keep')
  } finally { fs.rmSync(tmp, { recursive: true, force: true }) }
})

test('removeProfileDir refuses when an ancestor of the target is a junction out of the root', () => {
  const tmp = sandbox()
  try {
    const root = path.join(tmp, 'tools', 'browser-profiles'), outside = path.join(tmp, 'outside')
    fs.mkdirSync(root, { recursive: true }); fs.mkdirSync(path.join(outside, 'p'), { recursive: true })
    fs.writeFileSync(path.join(outside, 'p', MARKER), 'm'); fs.writeFileSync(path.join(outside, 'p', 'precious.txt'), 'keep')
    link(outside, path.join(root, 'hop'))
    assert.throws(() => removeProfileDir(path.join(root, 'hop', 'p'), { root }), /refusing/)
    assert.equal(fs.existsSync(path.join(outside, 'p', 'precious.txt')), true)
  } finally { fs.rmSync(tmp, { recursive: true, force: true }) }
})

test('removeProfileDir still deletes a genuine marked profile (junction inside it is unlinked, not followed)', () => {
  const tmp = sandbox()
  try {
    const root = path.join(tmp, 'tools', 'browser-profiles'), outside = path.join(tmp, 'outside')
    fs.mkdirSync(root, { recursive: true }); fs.mkdirSync(outside); fs.writeFileSync(path.join(outside, 'precious.txt'), 'keep')
    const dir = path.join(root, 'firefox-2-bb'); fs.mkdirSync(dir); fs.writeFileSync(path.join(dir, MARKER), 'm')
    link(outside, path.join(dir, 'inner-link'))
    assert.equal(removeProfileDir(dir, { root }), true)
    assert.equal(fs.existsSync(dir), false)
    assert.equal(fs.existsSync(path.join(outside, 'precious.txt')), true)
  } finally { fs.rmSync(tmp, { recursive: true, force: true }) }
})

test('makeProfileDir refuses to create when the profiles root is a junction leaving the tools dir', () => {
  const tmp = sandbox()
  try {
    const tools = path.join(tmp, 'tools'), root = path.join(tools, 'browser-profiles'), outside = path.join(tmp, 'outside')
    fs.mkdirSync(tools, { recursive: true }); fs.mkdirSync(outside); link(outside, root)
    assert.throws(() => makeProfileDir('firefox', { root, toolsDir: tools }), /escap|refusing/)
    assert.deepEqual(fs.readdirSync(outside), [])
  } finally { fs.rmSync(tmp, { recursive: true, force: true }) }
})

test('makeProfileDir creates a contained, marked profile under a normal root', () => {
  const tmp = sandbox()
  try {
    const tools = path.join(tmp, 'tools'), root = path.join(tools, 'browser-profiles')
    const dir = makeProfileDir('firefox', { root, toolsDir: tools })
    assert.equal(fs.existsSync(path.join(dir, MARKER)), true)
    assert.equal(path.dirname(fs.realpathSync(dir)), fs.realpathSync(root))
  } finally { fs.rmSync(tmp, { recursive: true, force: true }) }
})
