// Real server and disposable Chromium context: explicit consent, one-time polling,
// usable Floccus credential, and desktop/mobile screenshots. No real user profile.
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import fs from 'node:fs'
import path from 'node:path'
import { loadPlaywright, REPO_ROOT, RESULTS_DIR, writeResult, launchWithExtension, closeAndClean } from './lib/harness.mjs'
import { openExtensionPage, createSandboxFolder, syncAccount } from './lib/floccus-rpc.mjs'
import { makeRunRoot, removeRunRoot, findGo, pickLoopbackPort, startProcessServer, runCommand, sanitize } from './lib/server-sync.mjs'

const results = []
const wizardScreenshots = process.argv.includes('--screenshots')
let stock
const secrets = []
const runRoot = makeRunRoot()
let server, browser, diagPage
let exitCode = 1
try {
  const go = findGo()
  if (go.missing) throw new Error('Go toolchain missing')
  const exe = path.join(runRoot, process.platform === 'win32' ? 'bookmarksync.exe' : 'bookmarksync')
  const build = await runCommand({ command: go.exe, args: ['build', '-buildvcs=false', '-o', exe, './cmd/bookmarksync'], cwd: REPO_ROOT, env: { GOCACHE: path.join(REPO_ROOT, '.tools', 'go-cache') }, timeoutMs: 300000 })
  assert.equal(build.code, 0, sanitize(build.stderr, secrets))
  const dataDir = path.join(runRoot, 'data')
  const password = crypto.randomBytes(24).toString('base64url')
  secrets.push(password)
  const create = await runCommand({ command: exe, args: ['user', 'create', '--username', 'connect-smoke', '--password-stdin', '--data-dir', dataDir], stdin: password + '\n' })
  assert.equal(create.code, 0, sanitize(create.stderr, secrets))
  const port = await pickLoopbackPort()
  const origin = `http://localhost:${port}`
  server = await startProcessServer({ command: exe, args: ['serve', '--listen', `127.0.0.1:${port}`, '--data-dir', dataDir, '--public-url', origin], env: { BOOKMARKSYNC_MANAGEMENT_CIDRS: '127.0.0.0/8,::1/128' }, readyUrl: `http://127.0.0.1:${port}/readyz`, secrets })
  const { playwright } = loadPlaywright()
  browser = await playwright.chromium.launch({ headless: true })
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
  const page = await context.newPage()
  const start = await fetch(origin + '/index.php/login/v2', { method: 'POST' })
  assert.equal(start.status, 200)
  const flow = await start.json()
  secrets.push(flow.poll.token, new URL(flow.login).searchParams.get('flow'))
  const poll = () => fetch(flow.poll.endpoint, { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body: new URLSearchParams({ token: flow.poll.token }) })
  assert.equal((await poll()).status, 404)
  await page.goto(flow.login)
  await page.getByLabel('Username', { exact: true }).fill('connect-smoke')
  await page.getByLabel('Password', { exact: true }).fill(password)
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
  await page.getByRole('button', { name: 'Approve connection' }).waitFor()
  assert.equal((await poll()).status, 404)
  fs.mkdirSync(RESULTS_DIR, { recursive: true })
  for (const [name, viewport] of [['desktop', { width: 1440, height: 1000 }], ['mobile', { width: 390, height: 844 }]]) {
    await page.setViewportSize(viewport)
    await page.screenshot({ path: path.join(RESULTS_DIR, `loginflow-${name}.png`), fullPage: true })
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), true, `${name} horizontal overflow`)
  }
  await page.getByRole('button', { name: 'Approve connection' }).click()
  await page.getByRole('heading', { name: 'Connection approved' }).waitFor()
  const approved = await poll()
  assert.equal(approved.status, 200)
  const credential = await approved.json()
  secrets.push(credential.appPassword)
  assert.equal(credential.server, origin)
  assert.equal(credential.loginName, 'connect-smoke')
  assert.equal(approved.headers.get('cache-control'), 'no-store')
  const rest = await fetch(origin + '/index.php/apps/bookmarks/public/rest/v2/folder', { headers: { Authorization: 'Basic ' + Buffer.from(credential.loginName + ':' + credential.appPassword).toString('base64') } })
  assert.equal(rest.status, 200, 'issued credential must authenticate to actual Floccus route')
  assert.equal((await poll()).status, 404)
  const storage = await page.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length, cookie: document.cookie }))
  assert.equal(storage.local, 0)
  assert.equal(storage.session, 0)
  assert.equal(storage.cookie.includes('bookmarksync'), false)
  results.push({ name: 'real password login → explicit consent → one-time app credential → Floccus folder API', status: 'PASS' })
  results.push({ name: 'desktop/mobile layout and script-inaccessible secrets', status: 'PASS' })
  if (process.argv.includes('--stock') || process.argv.includes('--wizard')) {
    stock = await launchWithExtension({ browser: 'chrome', headless: true })
    const extensionPage = await openExtensionPage(stock.pageContext, stock.extensionId)
    if (process.argv.includes('--wizard')) {
      // Set up only native test bookmarks. Profile credentials/configuration are
      // created entirely through the unmodified extension's visible wizard.
      const folderTitle = 'Login Flow Wizard Smoke'
      const testFolder = await createSandboxFolder(stock.serviceWorker, folderTitle)
      await stock.serviceWorker.evaluate(async id => {
        await chrome.bookmarks.create({ parentId: id, title: 'Wizard acceptance bookmark', url: 'https://example.com/login-flow-wizard' })
      }, testFolder)
      const { parentTitle, browserRoot } = await stock.serviceWorker.evaluate(async id => {
        const [folder] = await chrome.bookmarks.get(id)
        const [root] = await chrome.bookmarks.getTree()
        return { parentTitle: (await chrome.bookmarks.get(folder.parentId))[0].title, browserRoot: root.id }
      }, testFolder)
      // Labels/roles follow the stock 5.13.0 sources (options.js.map) and en locale:
      // Overview's "Add Profile" is a router link with target=_blank to #/new.
      diagPage = extensionPage
      const wizardOpened = stock.pageContext.waitForEvent('page', { timeout: 15000 })
      await extensionPage.getByRole('link', { name: 'Add Profile' }).click()
      const wizard = await wizardOpened
      diagPage = wizard
      await wizard.waitForLoadState('domcontentloaded')
      assert.match(wizard.url(), /options\.html#\/new$/)
      const exact = text => new RegExp('^\\s*' + text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '\\s*$')
      // Stock NewAccount.vue is a Vuetify 2 v-stepper: every step is a
      // .v-stepper__content pane (v-show + tab-transition) with its own Continue
      // button. During a step change the leaving and entering panes are both
      // displayed, so wait until exactly one pane is displayed, it carries no
      // enter/leave transition class, and its headline is the expected step; then
      // scope every control to that pane.
      const activeStep = async heading => {
        await wizard.waitForFunction(text => {
          const shown = [...document.querySelectorAll('.v-stepper__items > .v-stepper__content')].filter(p => getComputedStyle(p).display !== 'none')
          if (shown.length !== 1 || /transition-(enter|leave)/.test(shown[0].className)) return false
          const headline = shown[0].querySelector('.v-stepper__wrapper > [role="heading"]')?.cloneNode(true)
          headline?.querySelectorAll('.v-icon').forEach(icon => icon.remove())
          return !!headline && headline.textContent.trim() === text
        }, heading, { timeout: 30000 })
        return wizard.locator('.v-stepper__items > .v-stepper__content').filter({ has: wizard.getByRole('heading', { name: exact(heading) }) })
      }
      const stepButton = (step, name) => step.getByRole('button', { name, exact: true }).filter({ visible: true })
      // Step 0: "How do you want to sync?" — choose the account type by its visible label.
      let step = await activeStep('How do you want to sync?')
      await step.getByText('Nextcloud Bookmarks', { exact: true }).click()
      assert.equal(await step.getByRole('radio', { name: 'Nextcloud Bookmarks', exact: true }).isChecked(), true)
      await stepButton(step, 'Continue').click()
      // Step 1: profile label.
      step = await activeStep('Profile label')
      await step.getByLabel('Profile label', { exact: true }).fill('Wizard acceptance')
      await stepButton(step, 'Continue').click()
      // Step 2: server URL → Connect (stock server test) → Sign in with Nextcloud.
      step = await activeStep('Which server do you want to sync to?')
      await step.getByLabel('Nextcloud URL', { exact: true }).fill(origin)
      await stepButton(step, 'Connect').click()
      const pagesBefore = new Set(stock.pageContext.pages())
      await stepButton(step, 'Sign in with Nextcloud').click()
      let consentPage
      for (let i = 0; i < 150 && !consentPage; i++) {
        consentPage = stock.pageContext.pages().find(p => !pagesBefore.has(p) && p.url().startsWith(origin + '/auth/connect'))
        if (!consentPage) await new Promise(resolve => setTimeout(resolve, 100))
      }
      assert.ok(consentPage, 'wizard sign-in must open the server consent tab')
      diagPage = consentPage
      secrets.push(new URL(consentPage.url()).searchParams.get('flow'))
      await consentPage.getByLabel('Username', { exact: true }).fill('connect-smoke')
      await consentPage.getByLabel('Password', { exact: true }).fill(password)
      await consentPage.getByRole('button', { name: 'Sign in', exact: true }).click()
      await consentPage.getByRole('button', { name: 'Approve connection' }).click()
      await consentPage.getByRole('heading', { name: 'Connection approved' }).waitFor()
      diagPage = wizard
      // Step 3: the wizard advances after its own poll receives credentials.
      step = await activeStep('Which Folders do you want to sync?')
      // BookmarksSync exposes only canonical bar/other roots at the server top level,
      // so (as documented in floccus-compatibility.md) choose the browser's absolute
      // root: the stock chooser's untitled top tree node (chrome root id "0").
      await step.getByRole('textbox', { name: 'Bookmarks folder', exact: true }).click()
      // The stock OptionSyncFolder chooser is a detached Vuetify v-dialog.
      const finder = wizard.locator('.v-dialog.v-dialog--active')
      await finder.locator('.v-treeview-node__label').filter({ hasText: exact(parentTitle) }).waitFor()
      // The untitled absolute root is, by definition, the single top-level tree node.
      const rootNode = finder.locator('.v-treeview > .v-treeview-node > .v-treeview-node__root')
      assert.equal(await rootNode.count(), 1, 'chooser must show one absolute browser root')
      await rootNode.click()
      await finder.locator('.v-treeview > .v-treeview-node > .v-treeview-node__root.v-treeview-node--active').waitFor()
      await finder.getByRole('button', { name: 'Save', exact: true }).click()
      await finder.waitFor({ state: 'hidden' })
      // Optional diagnostics: stock extension capture can stall independently
      // of form interaction. All functional assertions run without this flag.
      if (wizardScreenshots) await wizard.screenshot({ path: path.join(RESULTS_DIR, 'loginflow-wizard-folder.png'), animations: 'disabled' })
      await stepButton(step, 'Continue').click()
      // Step 4: sync behaviour (stock defaults) → Continue runs the stock CREATE_ACCOUNT.
      step = await activeStep('How do you want syncing to work?')
      if (wizardScreenshots) await wizard.screenshot({ path: path.join(RESULTS_DIR, 'loginflow-wizard-sync-options.png'), animations: 'disabled' })
      await stepButton(step, 'Continue').click()
      await activeStep('Profile created')
      if (wizardScreenshots) await wizard.screenshot({ path: path.join(RESULTS_DIR, 'loginflow-wizard-created.png'), animations: 'disabled' })
      diagPage = undefined
      const saved = await wizard.evaluate(async () => {
        let { accounts } = await chrome.storage.local.get('accounts')
        while (typeof accounts === 'string') accounts = JSON.parse(accounts)
        return Object.entries(accounts || {}).map(([id, data]) => ({ id, ...data }))
      })
      for (const account of saved) if (account.password) secrets.push(account.password)
      assert.equal(saved.length, 1, 'wizard must create exactly one profile')
      const account = saved[0]
      assert.equal(account.type, 'nextcloud-bookmarks')
      assert.equal(account.url, origin)
      assert.equal(account.username, 'connect-smoke')
      assert.equal(account.localRoot, browserRoot, 'chooser must save the absolute browser root')
      assert.equal(account.label, 'Wizard acceptance')
      assert.match(account.password, /^bms1\.\d+\.[A-Za-z0-9_-]{43}$/)
      const headers = { Authorization: 'Basic ' + Buffer.from(account.username + ':' + account.password).toString('base64') }
      assert.equal((await fetch(origin + '/index.php/apps/bookmarks/public/rest/v2/folder', { headers })).status, 200)
      // Stock background sync (same message the stock UI posts); the default
      // auto-sync may already be running, so wait for the remote result too.
      const synced = await syncAccount(wizard, account.id)
      assert.equal(synced.done, true, 'real extension sync must complete')
      assert.ok(!synced.status?.error, 'wizard-created profile must sync without errors: ' + sanitize(String(synced.status?.error), secrets))
      let uploaded = false
      for (let i = 0; i < 60 && !uploaded; i++) {
        const remote = await fetch(origin + '/index.php/apps/bookmarks/public/rest/v2/bookmark', { headers })
        assert.equal(remote.status, 200)
        uploaded = JSON.stringify(await remote.json()).includes('https://example.com/login-flow-wizard')
        if (!uploaded) await new Promise(resolve => setTimeout(resolve, 500))
      }
      assert.ok(uploaded, 'native test bookmark must reach server')
      const tree = await fetch(origin + '/index.php/apps/bookmarks/public/rest/v2/folder', { headers })
      assert.equal(tree.status, 200)
      assert.ok(JSON.stringify(await tree.json()).includes(folderTitle), 'native test folder must reach server folder tree')
      const after = await wizard.evaluate(async () => {
        let { accounts } = await chrome.storage.local.get('accounts')
        while (typeof accounts === 'string') accounts = JSON.parse(accounts)
        return Object.values(accounts || {}).map(a => a.type)
      })
      assert.deepEqual(after, ['nextcloud-bookmarks'], 'still exactly one saved profile after sync')
      results.push({ name: 'stock floccus 5.13.0 Chrome wizard: type/URL → consent → saved profile → native bookmark upload', status: 'PASS' })
    } else {
    await extensionPage.waitForFunction(() => {
      for (const element of document.querySelectorAll('*')) {
        const store = element.__vue_app__?.config.globalProperties.$store || element.__vue__?.$store || element.__vueParentComponent?.proxy?.$store
        if (store) { window.__stockStore = store; return true }
      }
      return false
    }, undefined, { timeout: 10000 })
    const actionNames = await extensionPage.evaluate(() => Object.keys(window.__stockStore._actions).filter(k => /START_LOGIN_FLOW/.test(k)))
    assert.equal(actionNames.length, 1, 'stock login action must exist')
    await extensionPage.evaluate(({ origin, action }) => {
      const store = window.__stockStore
      store.dispatch(action, origin).then(credentials => { window.__connectionCheck = { credentials } }).catch(error => { window.__connectionCheck = { error: String(error) } })
    }, { origin, action: actionNames[0] })
    let consentPage
    for (let i = 0; i < 100 && !consentPage; i++) {
      consentPage = stock.pageContext.pages().find(p => p.url().startsWith(origin + '/auth/connect'))
      if (!consentPage) await new Promise(resolve => setTimeout(resolve, 100))
    }
    assert.ok(consentPage, 'unmodified extension must open its own consent tab')
    secrets.push(new URL(consentPage.url()).searchParams.get('flow'))
    await consentPage.getByLabel('Username', { exact: true }).fill('connect-smoke')
    await consentPage.getByLabel('Password', { exact: true }).fill(password)
    await consentPage.getByRole('button', { name: 'Sign in', exact: true }).click()
    await consentPage.getByRole('button', { name: 'Approve connection' }).click()
    await extensionPage.waitForFunction(() => !!window.__connectionCheck, undefined, { timeout: 15000 })
    const returned = await extensionPage.evaluate(() => window.__connectionCheck)
    if (returned.credentials?.password) secrets.push(returned.credentials.password)
    assert.ok(!returned.error, returned.error)
    assert.equal(returned.credentials.username, 'connect-smoke')
    const usable = await fetch(origin + '/index.php/apps/bookmarks/public/rest/v2/folder', { headers: { Authorization: 'Basic ' + Buffer.from(returned.credentials.username + ':' + returned.credentials.password).toString('base64') } })
    assert.equal(usable.status, 200, 'stock extension must receive a usable application credential')
    results.push({ name: 'stock floccus 5.13.0 login action opens consent and receives usable credentials', status: 'PASS' })
    }
  }
  exitCode = 0
} catch (error) {
  const serverTail = server && process.argv.includes('--wizard') ? '\nserver log tail:\n' + server.logs().slice(-4000) : ''
  results.push({ name: 'login flow browser smoke', status: 'FAIL', detail: sanitize(error.stack + serverTail, secrets) })
  if (diagPage) await diagPage.screenshot({ path: path.join(RESULTS_DIR, 'loginflow-wizard-failure.png'), fullPage: true }).catch(() => {})
} finally {
  try { if (stock) await closeAndClean(stock) } catch (error) { results.push({ name: 'stock browser cleanup', status: 'FAIL', detail: sanitize(error.message, secrets) }); exitCode = 1 }
  try { if (browser) await browser.close() } catch (error) { results.push({ name: 'browser cleanup', status: 'FAIL', detail: sanitize(error.message, secrets) }); exitCode = 1 }
  try { if (server) await server.stop() } catch (error) { results.push({ name: 'server cleanup', status: 'FAIL', detail: sanitize(error.message, secrets) }); exitCode = 1 }
  try { if (!removeRunRoot(runRoot)) throw new Error('marked disposable run retained') } catch (error) { results.push({ name: 'run cleanup', status: 'FAIL', detail: error.message }); exitCode = 1 }
  writeResult('loginflow-smoke', { results })
  for (const result of results) console.log(result.status, result.name, result.detail || '')
}
process.exitCode = exitCode
