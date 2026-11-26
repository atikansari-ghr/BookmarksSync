// Unit tests for the UI smoke failure diagnostics: the full Playwright call log must survive,
// secrets must not, and output stays bounded. No browser, server, or network.
import test from 'node:test'
import assert from 'node:assert/strict'
import { formatFailure } from '../ui-smoke.mjs'

const playwrightTimeout = () => {
  const e = new Error([
    'locator.click: Timeout 30000ms exceeded.',
    'Call log:',
    "  - waiting for getByRole('button', { name: 'Sign out', exact: true })",
  ].join('\n'))
  e.stack = e.message + '\n    at signOut (ui-smoke.mjs:1:1)'
  return e
}

test('keeps the Playwright call log beyond the first line', () => {
  const out = formatFailure(playwrightTimeout())
  assert.match(out, /^locator\.click: Timeout 30000ms exceeded\./)
  assert.match(out, /Call log:/)
  assert.match(out, /waiting for getByRole\('button', \{ name: 'Sign out'/)
})

test('appends stack frames when the message carries none', () => {
  const e = new Error('TypeError: x is null')
  e.stack = 'Error: TypeError: x is null\n    at main (ui-smoke.mjs:10:5)\n    at run (ui-smoke.mjs:20:1)'
  const out = formatFailure(e)
  assert.match(out, /stack:\n\s+at main \(ui-smoke\.mjs:10:5\)/)
})

test('redacts known secrets, session cookies, bearer and API tokens, and callback parameters', () => {
  const password = 'Pa55word-very-secret-value'
  const msg = [
    `fill("${password}")`,
    'cookie: bookmarksync_session=bms1.1.' + 'A'.repeat(43),
    'cookie: bookmarksync_csrf=abcdef0123456789',
    'Authorization: Bearer bmt1_abcdefghijklmnop',
    'token bmt1_ZYXWVUTSRQ_9876',
    'http://localhost:1/auth/oidc/callback?code=abc123&state=xyz789',
  ].join('\n')
  const out = formatFailure(new Error(msg), [password])
  for (const leaked of [password, 'bms1.1.', 'abcdef0123456789', 'bmt1_', 'abc123', 'xyz789']) assert.ok(!out.includes(leaked), `leaked ${leaked}: ${out}`)
  assert.match(out, /\[REDACTED\]/)
})

test('bounds the number of lines and characters', () => {
  const long = new Error(Array.from({ length: 500 }, (_, i) => `line ${i}`).join('\n'))
  const byLines = formatFailure(long, [], { maxLines: 10, maxChars: 100000 })
  assert.equal(byLines.split('\n').length, 11)
  assert.match(byLines, /more lines omitted/)
  const byChars = formatFailure(new Error('x'.repeat(10000)), [], { maxChars: 200 })
  assert.ok(byChars.length < 260)
  assert.match(byChars, /more chars omitted/)
})

test('accepts non-Error values', () => {
  assert.equal(formatFailure('plain text'), 'plain text')
})
