// Real-browser check of the sign-in, account-security, and admin branding UI. Builds the server
// (with the embedded UI) into a marked run directory, starts it on
// http://localhost:<port> with a local fake OpenID Connect issuer configured as
// "Authentik", creates a throw-away account through the CLI (password via stdin), and
// drives Playwright's Chromium through the real pages:
//   password sign-in -> API token create/revoke -> revoke another session ->
//   add a passkey (CDP virtual authenticator) -> passkey sign-in -> TOTP setup with
//   one-time recovery codes -> password + TOTP sign-in -> link the provider account
//   -> provider sign-in completed with a recovery code -> admin health and branding upload.
// After every step it asserts that localStorage and sessionStorage stay empty and no
// auth cookie is visible to scripts. Exit codes: 0 PASS, 1 FAIL, 2 NOT_RUN, 3 BLOCKED.
import crypto from 'node:crypto'
import fs from 'node:fs'
import http from 'node:http'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { loadPlaywright, REPO_ROOT, writeResult, withTimeout } from './lib/harness.mjs'
import { makeRunRoot, removeRunRoot, findGo, pickLoopbackPort, startProcessServer, runCommand, sanitize } from './lib/server-sync.mjs'

const SMOKE_VERSION = '0.6.0-uismoke'
const results = []
const record = (name, status, detail = '') => { results.push({ name, status, detail }); console.log(`${status.padEnd(7)} ${name}${detail ? ' - ' + detail : ''}`) }
class Fail extends Error {}
const must = (cond, name, detail = '') => { if (!cond) { record(name, 'FAIL', detail); throw new Fail(name) } record(name, 'PASS', detail) }

/**
 * The full, sanitized text of an unexpected error, including Playwright's multi-line call
 * log (which names the locator and why each click attempt was refused), bounded so a
 * runaway log cannot flood CI output. Secrets, session cookies and bearer tokens are redacted.
 */
export function formatFailure(err, secrets = [], { maxLines = 60, maxChars = 6000 } = {}) {
  let text = String(err?.message || err)
  if (err?.stack && !text.includes('\n    at ')) {
    const frames = String(err.stack).split('\n').filter((l) => /^\s+at /.test(l)).slice(0, 8)
    if (frames.length) text += '\nstack:\n' + frames.join('\n')
  }
  text = sanitize(text, secrets)
    .replace(/\x1b\[[0-9;]*m/g, '')
    .replace(/(bookmarksync[^=\s;]*=)[^;\s]+/gi, '$1[REDACTED]')
    .replace(/(Bearer\s+)\S+/gi, '$1[REDACTED]')
    .replace(/bmt1_[A-Za-z0-9_-]+/g, '[REDACTED]')
    .replace(/([?&](?:code|state|token|flowId)=)[^&\s"']+/gi, '$1[REDACTED]')
  let lines = text.split('\n')
  if (lines.length > maxLines) lines = [...lines.slice(0, maxLines), `... (${lines.length - maxLines} more lines omitted)`]
  text = lines.join('\n')
  return text.length > maxChars ? text.slice(0, maxChars) + `... (${text.length - maxChars} more chars omitted)` : text
}

/**
 * Page state at the moment of an unexpected failure: path (no query or fragment), viewport,
 * scroll position, document size, and the Sign out button's geometry, visibility, and the
 * element that would actually receive a click at its centre. Never reads storage or cookies.
 */
export async function capturePageState(page, { timeoutMs = 5000 } = {}) {
  const read = page.evaluate(() => {
    const rect = (el) => { if (!el) return null; const r = el.getBoundingClientRect(); return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height) } }
    const describe = (el) => el ? `${el.tagName.toLowerCase()}${el.id ? '#' + el.id : ''}${el.classList.length ? '.' + [...el.classList].join('.') : ''}` : null
    const buttons = [...document.querySelectorAll('button')].filter((b) => b.textContent.trim() === 'Sign out')
    const side = document.querySelector('.ws-side')
    const sideStyle = side ? getComputedStyle(side) : null
    return {
      path: location.pathname,
      viewport: { w: window.innerWidth, h: window.innerHeight },
      scroll: { x: Math.round(window.scrollX), y: Math.round(window.scrollY) },
      document: { w: document.documentElement.scrollWidth, h: document.documentElement.scrollHeight },
      activeElement: describe(document.activeElement),
      sidebar: side ? { rect: rect(side), position: sideStyle.position, top: sideStyle.top, minHeight: sideStyle.minHeight } : null,
      signOut: buttons.map((b) => {
        const r = b.getBoundingClientRect()
        const cx = r.x + r.width / 2, cy = r.y + r.height / 2
        const inViewport = cx >= 0 && cy >= 0 && cx < window.innerWidth && cy < window.innerHeight
        const hit = inViewport ? document.elementFromPoint(cx, cy) : null
        const style = getComputedStyle(b)
        return { rect: rect(b), disabled: b.disabled, display: style.display, visibility: style.visibility, opacity: style.opacity,
          inViewport, hitTarget: describe(hit), hitIsButton: hit === b || b.contains(hit) }
      }),
      dialogs: [...document.querySelectorAll('[role="dialog"], dialog[open]')].map(describe),
      headings: [...document.querySelectorAll('h1, h2')].slice(0, 8).map((h) => h.textContent.trim().slice(0, 60)),
    }
  })
  return withTimeout(read, timeoutMs, 'capture page state')
}

/** One viewport screenshot (not full page) into the ignored results folder; returns its path. */
export async function captureFailureScreenshot(page, name, { dir = path.join(REPO_ROOT, '.tools', 'browser-results', 'ui-failure'), timeoutMs = 10000 } = {}) {
  fs.mkdirSync(dir, { recursive: true })
  const file = path.join(dir, `${name.replace(/[^A-Za-z0-9_-]/g, '_').slice(0, 60)}.png`)
  await withTimeout(page.screenshot({ path: file, fullPage: false, timeout: timeoutMs }), timeoutMs + 1000, 'failure screenshot')
  return file
}

function totp(secretB32, stepOffset = 0) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let bits = ''
  for (const c of secretB32.replace(/=+$/, '')) bits += alphabet.indexOf(c).toString(2).padStart(5, '0')
  const seed = Buffer.from(bits.match(/.{8}/g).map((b) => parseInt(b, 2)))
  const msg = Buffer.alloc(8)
  msg.writeBigUInt64BE(BigInt(Math.floor(Date.now() / 1000 / 30) + stepOffset))
  const sum = crypto.createHmac('sha1', seed).update(msg).digest()
  const off = sum[sum.length - 1] & 0x0f
  return String((sum.readUInt32BE(off) & 0x7fffffff) % 1_000_000).padStart(6, '0')
}

const b64u = (buf) => Buffer.from(buf).toString('base64url')

/** A minimal OIDC issuer: discovery, JWKS, an auto-approving /auth and a PKCE-checking /token. */
async function startFakeIssuer({ clientId, clientSecret, subject }) {
  const { privateKey, publicKey } = crypto.generateKeyPairSync('rsa', { modulusLength: 2048 })
  const jwk = { ...publicKey.export({ format: 'jwk' }), kid: 'k1', alg: 'RS256', use: 'sig' }
  const codes = new Map()
  const port = await pickLoopbackPort()
  const issuer = `http://127.0.0.1:${port}`
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, issuer)
    const json = (status, body) => { res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }); res.end(JSON.stringify(body)) }
    if (url.pathname === '/.well-known/openid-configuration') {
      return json(200, { issuer, authorization_endpoint: `${issuer}/auth`, token_endpoint: `${issuer}/token`, jwks_uri: `${issuer}/keys`,
        response_types_supported: ['code'], subject_types_supported: ['public'], id_token_signing_alg_values_supported: ['RS256'] })
    }
    if (url.pathname === '/keys') return json(200, { keys: [jwk] })
    if (url.pathname === '/auth') {
      const q = url.searchParams
      if (q.get('client_id') !== clientId || q.get('code_challenge_method') !== 'S256') return json(400, { error: 'invalid_request' })
      const code = crypto.randomBytes(16).toString('base64url')
      codes.set(code, { nonce: q.get('nonce'), challenge: q.get('code_challenge'), redirect: q.get('redirect_uri') })
      const back = new URL(q.get('redirect_uri'))
      back.searchParams.set('code', code)
      back.searchParams.set('state', q.get('state'))
      res.writeHead(302, { Location: back.href })
      return res.end()
    }
    if (url.pathname === '/token' && req.method === 'POST') {
      let raw = ''
      req.on('data', (d) => { raw += d })
      req.on('end', () => {
        const form = new URLSearchParams(raw)
        let id = form.get('client_id'), secret = form.get('client_secret')
        const basic = /^Basic (.+)$/.exec(req.headers.authorization ?? '')
        if (basic) [id, secret] = Buffer.from(basic[1], 'base64').toString().split(':').map(decodeURIComponent)
        const grant = codes.get(form.get('code'))
        codes.delete(form.get('code'))
        const challenge = b64u(crypto.createHash('sha256').update(form.get('code_verifier') ?? '').digest())
        if (id !== clientId || secret !== clientSecret || !grant || grant.redirect !== form.get('redirect_uri') || challenge !== grant.challenge) return json(400, { error: 'invalid_grant' })
        const now = Math.floor(Date.now() / 1000)
        const head = b64u(JSON.stringify({ alg: 'RS256', kid: 'k1', typ: 'JWT' }))
        const body = b64u(JSON.stringify({ iss: issuer, sub: subject, aud: clientId, iat: now, exp: now + 300, nonce: grant.nonce, name: 'UI Smoke Person' }))
        const sig = b64u(crypto.sign('sha256', Buffer.from(`${head}.${body}`), privateKey))
        json(200, { access_token: crypto.randomBytes(16).toString('hex'), token_type: 'Bearer', expires_in: 300, id_token: `${head}.${body}.${sig}` })
      })
      return
    }
    json(404, { error: 'not_found' })
  })
  await new Promise((r) => server.listen(port, '127.0.0.1', r))
  return { issuer, close: () => new Promise((r) => server.close(r)) }
}

async function main() {
  const go = findGo()
  if (go.missing) { record('prerequisites', 'NOT_RUN', 'Go toolchain not found'); return 2 }
  let pw
  try { pw = loadPlaywright() } catch (e) { record('prerequisites', 'NOT_RUN', e.message); return 2 }
  const secrets = []
  const runRoot = makeRunRoot()
  let server, browser, issuer, page
  let step = 'setup'
  const consoleErrors = []
  try {
    const exe = path.join(runRoot, process.platform === 'win32' ? 'bookmarksync.exe' : 'bookmarksync')
    const build = await runCommand({ command: go.exe, args: ['build', '-ldflags', `-X github.com/atikansari-ghr/BookmarksSync/internal/version.Version=${SMOKE_VERSION}`, '-o', exe, './cmd/bookmarksync'], cwd: REPO_ROOT,
      env: { CGO_ENABLED: '0', GOCACHE: path.join(REPO_ROOT, '.tools', 'go-cache'), GOPATH: process.env.GOPATH ?? path.join(process.env.USERPROFILE ?? process.env.HOME ?? '', 'go') }, timeoutMs: 300000 })
    if (build.code !== 0) { record('build server', 'FAIL', build.stderr.slice(-400)); return 1 }
    const dataDir = path.join(runRoot, 'data')
    const port = await pickLoopbackPort()
    const origin = `http://localhost:${port}`
    const password = crypto.randomBytes(18).toString('base64url')
    const clientSecret = crypto.randomBytes(18).toString('base64url')
    secrets.push(password, clientSecret)
    const create = await runCommand({ command: exe, args: ['user', 'create', '--username', 'ui-smoke', '--password-stdin', '--admin', '--data-dir', dataDir], stdin: password + '\n' })
    if (create.code !== 0) { record('create user', 'FAIL', sanitize(create.stderr, secrets)); return 1 }
    issuer = await startFakeIssuer({ clientId: 'ui-smoke-client', clientSecret, subject: 'ui-smoke-subject' })
    server = await startProcessServer({ command: exe, args: ['serve', '--listen', `127.0.0.1:${port}`, '--data-dir', dataDir, '--public-url', origin],
      env: { BOOKMARKSYNC_OIDC_AUTHENTIK_ISSUER: issuer.issuer, BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_ID: 'ui-smoke-client', BOOKMARKSYNC_OIDC_AUTHENTIK_CLIENT_SECRET: clientSecret },
      readyUrl: `http://127.0.0.1:${port}/readyz`, secrets })

    const { chromium } = pw.playwright
    try { browser = await chromium.launch({ headless: true }) } catch (e) { record('launch chromium', 'NOT_RUN', String(e.message).split('\n')[0]); return 2 }
    const context = await browser.newContext()
    page = await context.newPage()
    const cdp = await context.newCDPSession(page)
    await cdp.send('WebAuthn.enable', { enableUI: false })
    const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', { options: { protocol: 'ctap2', ctap2Version: 'ctap2_1', transport: 'internal',
      hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true } })
    // Expected 401/404 probes are logged by the browser as "Failed to load resource";
    // anything else (script errors, CSP refusals) fails the run.
    page.on('console', (m) => { if (m.type() === 'error' && !/^Failed to load resource: the server responded with a status of (401|404)/.test(m.text())) consoleErrors.push(m.text()) })
    page.on('pageerror', (e) => consoleErrors.push(String(e?.stack || e)))

    // Optional screenshots (UI_SMOKE_SCREENSHOTS=1) go to the ignored results folder.
    const shoot = async (name, full = false) => {
      if (!process.env.UI_SMOKE_SCREENSHOTS) return
      const dir = path.join(REPO_ROOT, '.tools', 'browser-results', 'ui-screenshots')
      await import('node:fs').then((fs) => fs.mkdirSync(dir, { recursive: true }))
      await page.setViewportSize({ width: 1586, height: 992 })
      await page.screenshot({ path: path.join(dir, `${name}.png`), fullPage: full })
      await page.setViewportSize({ width: 390, height: 844 })
      await page.screenshot({ path: path.join(dir, `${name.replace('desktop', 'phone')}.png`), fullPage: full })
      await page.setViewportSize({ width: 1280, height: 720 })
    }
    const noStorage = async (step) => {
      const s = await page.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length, cookie: document.cookie }))
      must(s.local === 0 && s.session === 0 && !/bookmarksync/i.test(s.cookie), `no auth data in browser storage (${step})`, JSON.stringify(s))
    }
    const security = () => page.getByRole('heading', { level: 1, name: 'Account security' }).waitFor({ timeout: 15000 })
    const passwordLogin = async () => {
      await page.goto(`${origin}/auth/login`)
      await page.getByLabel('Username').fill('ui-smoke')
      await page.getByLabel('Password', { exact: true }).fill(password)
      await page.getByRole('button', { name: 'Sign in', exact: true }).click()
    }
    const signOut = async () => {
      await page.getByRole('button', { name: 'Sign out', exact: true }).click()
      await page.getByRole('heading', { name: 'Personal Bookmarks Server' }).waitFor()
    }

    step = 'login page'
    // 1. Login page identity and password sign-in.
    await page.goto(`${origin}/`)
    await page.getByRole('heading', { name: 'Personal Bookmarks Server' }).waitFor()
    must(page.url() === `${origin}/auth/login`, 'root redirects to the sign-in page', page.url())
    await page.getByText(`v${SMOKE_VERSION} · © 2026 Atik Ansari`).waitFor()
    await shoot('login-desktop')
    const body = await page.textContent('body')
    must(/Your control\./.test(body) && /ask your administrator to reset it/i.test(body) && !/forgot password\?|remember me|email/i.test(body), 'approved identity, runtime version, copyright and admin guidance')
    must(await page.getByRole('button', { name: 'Sign in with Authentik' }).isVisible() && !(await page.getByRole('button', { name: 'Sign in with Google' }).count()), 'only the configured provider is offered')
    await passwordLogin()
    await security()
    record('password sign-in through the UI', 'PASS')
    await shoot('security-desktop', true)
    await noStorage('after password sign-in')

    step = 'api token'
    // 2. Personal API token: create (secret once) and revoke.
    const tokens = page.getByRole('region', { name: 'Personal API tokens' })
    await tokens.getByLabel('Token name').fill('ui smoke token')
    await tokens.getByRole('checkbox', { name: 'bookmarks:read' }).check()
    await tokens.getByRole('button', { name: 'Create token' }).click()
    const tokenSecret = (await tokens.getByRole('group', { name: 'Copy your new token now' }).locator('code').last().textContent()).trim()
    secrets.push(tokenSecret)
    must(/^bmt1_/.test(tokenSecret), 'token secret shown once after creation')
    const tokenWorks = await page.evaluate(async (t) => (await fetch('/api/v1/me', { headers: { Authorization: `Bearer ${t}` } })).status, tokenSecret)
    must(tokenWorks === 200, 'the displayed token authenticates the API')
    await tokens.getByRole('button', { name: 'Done' }).click()
    must(!(await page.content()).includes(tokenSecret), 'token secret gone from the page after Done')
    await tokens.getByRole('button', { name: 'Revoke ui smoke token' }).click()
    await page.getByRole('button', { name: 'Revoke token' }).click()
    await tokens.getByText('You have no API tokens.').waitFor()
    const tokenAfter = await page.evaluate(async (t) => (await fetch('/api/v1/me', { headers: { Authorization: `Bearer ${t}` } })).status, tokenSecret)
    must(tokenAfter === 401, 'revoked token no longer authenticates', `status ${tokenAfter}`)
    await noStorage('after token create/revoke')

    step = 'revoke session'
    // 3. Revoke another session from the session list.
    const other = await browser.newContext({ userAgent: 'UI-Smoke Other Device' })
    const otherPage = await other.newPage()
    await otherPage.goto(`${origin}/auth/login`)
    await otherPage.getByLabel('Username').fill('ui-smoke')
    await otherPage.getByLabel('Password', { exact: true }).fill(password)
    await otherPage.getByRole('button', { name: 'Sign in', exact: true }).click()
    await otherPage.getByRole('heading', { level: 1, name: 'Account security' }).waitFor()
    await page.reload()
    await security()
    const sessions = page.getByRole('region', { name: 'Active sessions' })
    await sessions.getByRole('button', { name: 'Sign out UI-Smoke Other Device' }).click()
    await page.getByRole('status').filter({ hasText: 'Session signed out.' }).waitFor()
    const otherStatus = await otherPage.evaluate(async () => (await fetch('/api/v1/auth/session')).status)
    must(otherStatus === 401, 'revoked session is signed out on the other device', `status ${otherStatus}`)
    await other.close()

    step = 'passkey'
    // 4. Add a passkey, sign out, sign in with it.
    const passkeys = page.getByRole('region', { name: 'Passkeys' })
    await passkeys.getByLabel('Passkey name').fill('Virtual key')
    await passkeys.getByRole('button', { name: 'Add a passkey' }).click()
    await passkeys.getByText('Virtual key').waitFor()
    const { credentials } = await cdp.send('WebAuthn.getCredentials', { authenticatorId })
    must(credentials.length === 1 && credentials[0].isResidentCredential, 'passkey enrolled through the UI (resident credential on the virtual authenticator)')
    await signOut()
    await page.getByRole('button', { name: 'Sign in with a passkey' }).click()
    await security()
    record('passkey sign-in through the UI', 'PASS')
    await noStorage('after passkey sign-in')

    step = 'totp setup'
    // 5. TOTP setup with one-time recovery codes.
    const totpSec = page.getByRole('region', { name: 'Authenticator app' })
    await totpSec.getByRole('button', { name: 'Set up authenticator app' }).click()
    const setupKey = (await totpSec.locator('dt:has-text("Setup key") + dd code').textContent()).trim()
    secrets.push(setupKey)
    await totpSec.getByLabel('Authentication code').fill(totp(setupKey))
    await totpSec.getByRole('button', { name: 'Turn on' }).click()
    const codesGroup = totpSec.getByRole('group', { name: 'Save your recovery codes' })
    await codesGroup.waitFor()
    const recovery = (await codesGroup.locator('li code').allTextContents()).map((s) => s.trim())
    recovery.forEach((c) => secrets.push(c))
    must(recovery.length === 10, 'ten recovery codes shown once', `${recovery.length}`)
    await codesGroup.getByRole('button', { name: 'I have saved them' }).click()
    await totpSec.getByText(/is on/).waitFor()
    must(!(await page.content()).includes(recovery[0]), 'recovery codes gone from the page after saving')
    await noStorage('after TOTP setup')

    step = 'password totp sign-in'
    // 6. Password + TOTP sign-in.
    await signOut()
    await passwordLogin()
    await page.getByRole('heading', { name: 'Two-step verification' }).waitFor()
    await page.getByLabel('Authentication code').fill(totp(setupKey, 1))
    await page.getByRole('button', { name: 'Verify' }).click()
    await security()
    record('password + TOTP sign-in through the UI', 'PASS')

    step = 'oidc link'
    // 7. Link the provider account (OIDC) from the security page.
    const linked = page.getByRole('region', { name: 'Linked accounts' })
    await linked.getByRole('button', { name: 'Link Authentik' }).click()
    await page.getByRole('status').filter({ hasText: 'Account linked.' }).waitFor({ timeout: 20000 })
    await security()
    await linked.getByText('UI Smoke Person').waitFor()
    must(!page.url().includes('code=') && !page.url().includes('state='), 'provider callback parameters are not left in the address bar', page.url())
    record('OIDC account linking through the UI', 'PASS')
    await noStorage('after OIDC linking')

    step = 'oidc sign-in'
    // 8. Provider sign-in; the account has TOTP, so the second factor is required (recovery code).
    await signOut()
    await page.getByRole('button', { name: 'Sign in with Authentik' }).click()
    await page.getByRole('heading', { name: 'Two-step verification' }).waitFor({ timeout: 20000 })
    must(!page.url().includes('#') && !page.url().includes('flowId'), 'second-factor handle removed from the address bar', page.url())
    await page.getByRole('button', { name: 'Use a recovery code instead' }).click()
    await page.getByLabel('Recovery code').fill(recovery[0])
    await page.getByRole('button', { name: 'Verify' }).click()
    await security()
    record('OIDC sign-in completed with a recovery code', 'PASS')
    await noStorage('after OIDC sign-in')

    step = 'admin branding'
    // 9. Administrator health and branding upload. Build a real 1x1 PNG in the browser
    // canvas, so the smoke exercises the actual multipart upload and public image route.
    await page.goto(`${origin}/admin`)
    await page.getByRole('heading', { name: 'Server health' }).waitFor()
    must(await page.getByText('Database ready').isVisible(), 'administrator health page loads')
    const pngBytes = await page.evaluate(async () => {
      const canvas = document.createElement('canvas')
      canvas.width = 1; canvas.height = 1
      const ctx = canvas.getContext('2d')
      ctx.fillStyle = '#167f8b'; ctx.fillRect(0, 0, 1, 1)
      const blob = await new Promise((resolve) => canvas.toBlob(resolve, 'image/png'))
      return Array.from(new Uint8Array(await blob.arrayBuffer()))
    })
    await page.getByLabel('Login logo image').setInputFiles({ name: 'logo.png', mimeType: 'image/png', buffer: Buffer.from(pngBytes) })
    await page.getByText('Logo uploaded.').waitFor()
    await page.getByLabel('Login background image').setInputFiles({ name: 'background.png', mimeType: 'image/png', buffer: Buffer.from(pngBytes) })
    await page.getByText('Background uploaded.').waitFor()
    const presentation = await page.evaluate(async () => (await (await fetch('/api/v1/auth/presentation')).json()))
    const assetURL = /^\/api\/v1\/auth\/presentation\/assets\/[0-9a-f]{32}$/
    must(assetURL.test(presentation.logoUrl) && assetURL.test(presentation.backgroundUrl) && presentation.logoUrl !== presentation.backgroundUrl, 'uploaded login images have distinct generated public URLs')
    const images = await page.evaluate(async (urls) => Promise.all(urls.map(async (url) => {
      const response = await fetch(url)
      return { status: response.status, type: response.headers.get('content-type'), nosniff: response.headers.get('x-content-type-options') }
    })), [presentation.logoUrl, presentation.backgroundUrl])
    must(images.every((image) => image.status === 200 && image.type === 'image/png' && image.nosniff === 'nosniff'), 'public branding images are served with a safe image type')
    await shoot('admin-desktop', true)
    step = 'sign out after admin branding'
    await signOut()
    step = 'branded login page'
    const logo = page.locator('.login-logo')
    const logoReady = await logo.waitFor({ timeout: 5000 }).then(() => true, () => false)
    const loginState = await page.evaluate(async () => ({
      path: location.pathname,
      title: document.title,
      body: document.body.innerText.slice(0, 280),
      presentation: await (await fetch('/api/v1/auth/presentation')).json(),
    }))
    must(logoReady, 'branded login page renders the uploaded logo', JSON.stringify(loginState))
    const renderedBackground = await page.locator('.login-background-image').evaluate((element) => getComputedStyle(element).backgroundImage)
    must((await page.locator('.login-logo').getAttribute('src')) === presentation.logoUrl && renderedBackground.includes(presentation.backgroundUrl), 'uploaded images are applied to the login page')
    await shoot('branded-login-desktop', true)
    await noStorage('after admin branding upload')

    must(consoleErrors.length === 0, 'no script errors or CSP violations', consoleErrors.slice(0, 3).join(' | '))
    return 0
  } catch (e) {
    if (!(e instanceof Fail)) {
      const full = formatFailure(e, secrets)
      record('unexpected error', 'FAIL', `[step: ${step}] ${full.split('\n')[0]}`)
      const rest = full.split('\n').slice(1)
      if (rest.length) console.log('        call log:\n' + rest.map((l) => '        ' + l).join('\n'))
      if (consoleErrors.length) console.log('        page errors (' + consoleErrors.length + '):\n' + consoleErrors.slice(0, 5).map((m) => '        ' + formatFailure(m, secrets, { maxLines: 12, maxChars: 1500 }).split('\n').join('\n        ')).join('\n'))
      if (page && !page.isClosed()) {
        try { console.log('        page state: ' + sanitize(JSON.stringify(await capturePageState(page)), secrets)) } catch (s) { console.log('        page state unavailable: ' + formatFailure(s, secrets, { maxLines: 2 })) }
        try { console.log('        screenshot: ' + path.relative(REPO_ROOT, await captureFailureScreenshot(page, step))) } catch (s) { console.log('        screenshot unavailable: ' + formatFailure(s, secrets, { maxLines: 2 })) }
      }
    }
    return 1
  } finally {
    try { if (browser) await withTimeout(browser.close(), 20000, 'browser close') } catch (e) { record('cleanup browser', 'FAIL', e.message) }
    try { if (server) await server.stop() } catch (e) { record('cleanup server', 'FAIL', e.message) }
    try { if (issuer) await issuer.close() } catch (e) { record('cleanup issuer', 'FAIL', e.message) }
    try { if (!removeRunRoot(runRoot)) record('cleanup run dir', 'FAIL', runRoot) } catch (e) { record('cleanup run dir', 'FAIL', e.message) }
    writeResult('ui-smoke', JSON.parse(sanitize(JSON.stringify({ playwright: pw?.version, results }), secrets)))
  }
}

if (process.argv[1] && fs.realpathSync(process.argv[1]) === fs.realpathSync(fileURLToPath(import.meta.url))) main().then((code) => {
  const final = results.some((x) => x.status === 'FAIL') ? 1 : code
  console.log(final === 0 ? 'PASS' : final === 2 ? 'NOT_RUN' : final === 3 ? 'BLOCKED' : 'FAIL')
  process.exit(final)
}, (e) => { console.error('FAIL', e.message); process.exit(1) })
