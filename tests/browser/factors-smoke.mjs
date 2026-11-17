// Real-browser check of the Task 3 authentication-factor API (no UI exists yet; Task 6
// owns pages). Builds and starts a disposable server on http://localhost:<port>, creates
// a throw-away account through the CLI (password via stdin), and drives Playwright's
// Chromium with a CDP virtual authenticator:
//   1. password login, passkey registration (navigator.credentials.create)
//   2. logout, discoverable passkey sign-in (navigator.credentials.get), replay refused
//   3. TOTP enrollment/confirmation, password login -> pending challenge, TOTP completion
//   4. no auth secret in localStorage/sessionStorage/document.cookie
// Exit codes: 0 PASS, 1 FAIL, 2 NOT_RUN (prerequisite missing), 3 BLOCKED.
// The run directory (server data, instance key, binary) is marked and removed afterwards;
// the browser uses Playwright's ephemeral context (no persistent profile).
import crypto from 'node:crypto'
import path from 'node:path'
import fs from 'node:fs'
import { loadPlaywright, REPO_ROOT, writeResult, withTimeout } from './lib/harness.mjs'
import { makeRunRoot, removeRunRoot, findGo, pickLoopbackPort, startProcessServer, runCommand, sanitize } from './lib/server-sync.mjs'

function totp(secretB32, stepOffset = 0) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = ''
  for (const c of secretB32.replace(/=+$/, '')) bits += alphabet.indexOf(c).toString(2).padStart(5, '0')
  const seed = Buffer.from(bits.match(/.{8}/g).map(b => parseInt(b, 2)))
  const msg = Buffer.alloc(8)
  msg.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 1000 / 30) + stepOffset))
  const sum = crypto.createHmac('sha1', seed).update(msg).digest()
  const off = sum[sum.length - 1] & 0x0f
  return String((sum.readUInt32BE(off) & 0x7fffffff) % 1_000_000).padStart(6, '0')
}

const results = []
const record = (name, status, detail = '') => { results.push({ name, status, detail }); console.log(`${status.padEnd(7)} ${name}${detail ? ' - ' + detail : ''}`) }

async function main() {
  const go = findGo()
  if (go.missing) { record('prerequisites', 'NOT_RUN', 'Go toolchain not found (.tools/go or BH_GO_EXE)'); return 2 }
  let pw
  try { pw = loadPlaywright() } catch (e) { record('prerequisites', 'NOT_RUN', e.message); return 2 }

  const secrets = []
  const runRoot = makeRunRoot()
  let server, browser
  try {
    const exe = path.join(runRoot, process.platform === 'win32' ? 'bookmarksync.exe' : 'bookmarksync')
    const build = await runCommand({ command: go.exe, args: ['build', '-o', exe, './cmd/bookmarksync'], cwd: REPO_ROOT, env: { CGO_ENABLED: '0', GOCACHE: path.join(REPO_ROOT, '.tools', 'go-cache'), GOPATH: process.env.GOPATH ?? path.join(process.env.USERPROFILE ?? process.env.HOME ?? '', 'go') }, timeoutMs: 300000 })
    if (build.code !== 0) { record('build server', 'FAIL', build.stderr.slice(-400)); return 1 }
    const dataDir = path.join(runRoot, 'data')
    const port = await pickLoopbackPort()
    const origin = `http://localhost:${port}`
    const password = crypto.randomBytes(18).toString('base64url')
    secrets.push(password)
    const create = await runCommand({ command: exe, args: ['user', 'create', '--username', 'factor-smoke', '--password-stdin', '--data-dir', dataDir], stdin: password + '\n' })
    if (create.code !== 0) { record('create user', 'FAIL', sanitize(create.stderr, secrets)); return 1 }

    server = await startProcessServer({ command: exe, args: ['serve', '--listen', `127.0.0.1:${port}`, '--data-dir', dataDir, '--public-url', origin], readyUrl: `http://127.0.0.1:${port}/readyz`, secrets })
    if (!fs.existsSync(path.join(dataDir, 'instance.key'))) { record('instance key created', 'FAIL'); return 1 }
    record('instance key created', 'PASS')

    const { chromium } = pw.playwright
    try {
      browser = await chromium.launch({ headless: true })
    } catch (e) { record('launch chromium', 'NOT_RUN', String(e.message).split('\n')[0]); return 2 }
    const context = await browser.newContext()
    const page = await context.newPage()
    await page.goto(`${origin}/healthz`)
    const cdp = await context.newCDPSession(page)
    await cdp.send('WebAuthn.enable', { enableUI: false })
    const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', { options: {
      protocol: 'ctap2', ctap2Version: 'ctap2_1', transport: 'internal', hasResidentKey: true,
      hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } })

    // All API calls run inside the page, so the browser itself supplies Origin, cookies and WebAuthn.
    const api = (method, url, body, csrf) => page.evaluate(async ([method, url, body, csrf]) => {
      const headers = { 'Content-Type': 'application/json' }
      if (csrf) headers['X-CSRF-Token'] = csrf
      const r = await fetch(url, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), credentials: 'same-origin' })
      const text = await r.text()
      let json = null
      try { json = JSON.parse(text) } catch {}
      return { status: r.status, json }
    }, [method, url, body, csrf])

    let r = await api('POST', '/api/v1/auth/login', { username: 'factor-smoke', password })
    if (r.status !== 200) { record('password login', 'FAIL', `status ${r.status}`); return 1 }
    let csrf = r.json.csrfToken
    record('password login (Secure __Host- cookie on localhost)', 'PASS')

    r = await api('POST', '/api/v1/auth/passkeys/register/begin', undefined, csrf)
    if (r.status !== 200) { record('passkey registration begin', 'FAIL', `status ${r.status}`); return 1 }
    const reg = await page.evaluate(async ({ flowId, options }) => {
      try {
        const publicKey = PublicKeyCredential.parseCreationOptionsFromJSON(options.publicKey)
        const cred = await navigator.credentials.create({ publicKey })
        return { flowId, credential: cred.toJSON() }
      } catch (e) { return { error: String(e) } }
    }, r.json)
    if (reg.error) { record('navigator.credentials.create', 'FAIL', reg.error); return 1 }
    r = await api('POST', '/api/v1/auth/passkeys/register/finish', { flowId: reg.flowId, name: 'Virtual', credential: reg.credential }, csrf)
    if (r.status !== 201) { record('passkey registration finish', 'FAIL', `status ${r.status} ${JSON.stringify(r.json)}`); return 1 }
    const { credentials } = await cdp.send('WebAuthn.getCredentials', { authenticatorId })
    if (credentials.length !== 1 || !credentials[0].isResidentCredential) { record('resident credential stored', 'FAIL'); return 1 }
    record('passkey registration (virtual authenticator, resident key, UV)', 'PASS')

    r = await api('POST', '/api/v1/auth/logout', undefined, csrf)
    if (r.status !== 204 || (await api('GET', '/api/v1/auth/session')).status !== 401) { record('logout', 'FAIL'); return 1 }

    r = await api('POST', '/api/v1/auth/passkey/login/begin')
    if (r.status !== 200) { record('passkey login begin', 'FAIL', `status ${r.status}`); return 1 }
    const asr = await page.evaluate(async ({ flowId, options }) => {
      try {
        const publicKey = PublicKeyCredential.parseRequestOptionsFromJSON(options.publicKey)
        const cred = await navigator.credentials.get({ publicKey })
        return { flowId, credential: cred.toJSON() }
      } catch (e) { return { error: String(e) } }
    }, r.json)
    if (asr.error) { record('navigator.credentials.get', 'FAIL', asr.error); return 1 }
    r = await api('POST', '/api/v1/auth/passkey/login/finish', asr)
    const who = await api('GET', '/api/v1/auth/session')
    if (r.status !== 200 || who.status !== 200 || who.json.user.username !== 'factor-smoke') { record('passkey sign-in', 'FAIL', `finish ${r.status}, session ${who.status}`); return 1 }
    csrf = r.json.csrfToken
    record('discoverable passkey sign-in', 'PASS')
    r = await api('POST', '/api/v1/auth/passkey/login/finish', asr)
    record('passkey assertion replay refused', r.status === 401 ? 'PASS' : 'FAIL', `status ${r.status}`)

    r = await api('POST', '/api/v1/auth/totp/enroll', undefined, csrf)
    if (r.status !== 200) { record('totp enroll', 'FAIL', `status ${r.status}`); return 1 }
    const secret = r.json.secret
    secrets.push(secret)
    r = await api('POST', '/api/v1/auth/totp/confirm', { code: totp(secret) }, csrf)
    if (r.status !== 200 || r.json.recoveryCodes.length !== 10) { record('totp confirm', 'FAIL', `status ${r.status}`); return 1 }
    r.json.recoveryCodes.forEach(c => secrets.push(c))
    record('totp enrollment and confirmation', 'PASS')
    await api('POST', '/api/v1/auth/logout', undefined, csrf)

    r = await api('POST', '/api/v1/auth/login', { username: 'factor-smoke', password })
    const noSession = (await api('GET', '/api/v1/auth/session')).status === 401
    if (r.status !== 202 || r.json.status !== 'second_factor_required' || !noSession) { record('pending second factor', 'FAIL', `status ${r.status}`); return 1 }
    record('password login returns pending challenge without a session', 'PASS')
    // The confirmation used the current step; the next step is within the accepted skew.
    const done = await api('POST', '/api/v1/auth/login/second-factor', { flowId: r.json.flowId, code: totp(secret, 1) })
    const after = await api('GET', '/api/v1/auth/session')
    record('totp second factor completes sign-in', done.status === 200 && after.status === 200 ? 'PASS' : 'FAIL', `complete ${done.status}, session ${after.status}`)

    const storage = await page.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length, cookie: document.cookie }))
    record('no secrets in browser storage or script-visible cookies', storage.local === 0 && storage.session === 0 && !storage.cookie.includes('bookmarksync') ? 'PASS' : 'FAIL', JSON.stringify(storage))
    return results.some(x => x.status === 'FAIL') ? 1 : 0
  } finally {
    try { if (browser) await withTimeout(browser.close(), 20000, 'browser close') } catch (e) { record('cleanup browser', 'FAIL', e.message) }
    try { if (server) await server.stop() } catch (e) { record('cleanup server', 'FAIL', e.message) }
    try { if (!removeRunRoot(runRoot)) record('cleanup run dir', 'FAIL', runRoot) } catch (e) { record('cleanup run dir', 'FAIL', e.message) }
    writeResult('factors-smoke', JSON.parse(sanitize(JSON.stringify({ playwright: pw?.version, results }), secrets)))
  }
}

main().then(code => {
  const final = results.some(x => x.status === 'FAIL') ? 1 : code
  console.log(final === 0 ? 'PASS' : final === 2 ? 'NOT_RUN' : final === 3 ? 'BLOCKED' : 'FAIL')
  process.exit(final)
}, e => { console.error('FAIL', e.message); process.exit(1) })
