// CI contract (Task 6): the real-browser sign-in/account-security flow (`npm run ui`) must run in CI
// on Linux and gate the Go build. Text-level checks only: no YAML dependency, no network, no browser.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const HERE = path.dirname(fileURLToPath(import.meta.url))
const REPO_ROOT = path.resolve(HERE, '..', '..', '..')
const WORKFLOW = fs.readFileSync(path.join(REPO_ROOT, '.github', 'workflows', 'ci.yml'), 'utf8').replace(/\r\n/g, '\n')

/** Splits the top-level `jobs:` map into { name: blockText } using the workflow's two-space indentation. */
function jobs(text) {
  const lines = text.split('\n')
  const start = lines.indexOf('jobs:')
  assert.notEqual(start, -1, 'workflow has a jobs: map')
  const out = {}
  let name = null
  for (const line of lines.slice(start + 1)) {
    const m = /^ {2}([A-Za-z0-9_-]+):\s*$/.exec(line)
    if (m) { name = m[1]; out[name] = []; continue }
    if (/^\S/.test(line)) break
    if (name) out[name].push(line)
  }
  return Object.fromEntries(Object.entries(out).map(([k, v]) => [k, v.join('\n')]))
}

const JOBS = jobs(WORKFLOW)
const uiJobName = Object.keys(JOBS).find(n => /^\s+run:\s*.*\bnpm run ui\b/m.test(JOBS[n]))
const uiJob = uiJobName ? JOBS[uiJobName] : ''

test('a CI job runs the real-browser UI smoke (npm run ui)', () => {
  assert.ok(uiJobName, 'no job has a run step invoking `npm run ui`')
})

test('the UI smoke job runs on Linux, not a matrix', () => {
  assert.match(uiJob, /^ {4}runs-on: ubuntu-latest\s*$/m)
})

test('the UI smoke job works in tests/browser and installs locked dependencies', () => {
  assert.match(uiJob, /working-directory: tests\/browser\s*$/m)
  assert.match(uiJob, /^\s+run: npm ci\s*$/m)
  assert.match(uiJob, /cache-dependency-path: tests\/browser\/package-lock\.json/)
})

test('the UI smoke job sets up Go from go.mod and hands it to the harness', () => {
  assert.match(uiJob, /uses: actions\/setup-go@/)
  assert.match(uiJob, /go-version-file: go\.mod/)
  // ui-smoke.mjs finds Go only through BH_GO_EXE (or the local .tools/go toolchain).
  assert.match(uiJob, /BH_GO_EXE=?"?\$\(command -v go\)/)
})

test('the UI smoke job uses Node 26', () => {
  assert.match(uiJob, /uses: actions\/setup-node@/)
  assert.match(uiJob, /node-version: "26"/)
})

test('the UI smoke job installs Playwright Chromium with system dependencies before running', () => {
  const install = uiJob.search(/run: npx playwright install --with-deps chromium\s*$/m)
  assert.notEqual(install, -1, 'missing `npx playwright install --with-deps chromium`')
  assert.ok(install < uiJob.search(/\bnpm run ui\b/), 'Chromium must be installed before `npm run ui`')
})

test('the UI smoke job pins actions to SHAs already used elsewhere in this workflow', () => {
  const uses = [...uiJob.matchAll(/uses: (\S+)/g)].map(m => m[1])
  assert.ok(uses.length >= 3, 'expected checkout, setup-go and setup-node')
  const elsewhere = Object.entries(JOBS).filter(([n]) => n !== uiJobName).map(([, b]) => b).join('\n')
  for (const ref of uses) {
    assert.match(ref, /^actions\/[a-z-]+@[0-9a-f]{40}$/, `${ref} is not pinned to a full commit SHA`)
    assert.ok(elsewhere.includes(`uses: ${ref}`), `${ref} is not a pin already used in this workflow`)
  }
  assert.match(uiJob, /persist-credentials: false/)
})

test('the Go build requires the UI smoke job', () => {
  const needs = /^ {4}needs: \[([^\]]*)\]/m.exec(JOBS.build ?? '')
  assert.ok(needs, 'build job has no needs list')
  assert.ok(needs[1].split(',').map(s => s.trim()).includes(uiJobName), `build does not need ${uiJobName}`)
})

test('the same Linux browser gate runs the real connection consent smoke', () => {
  assert.match(uiJob, /BH_GO_EXE=.*npm run connect/)
  assert.ok(uiJob.indexOf('npm run connect') > uiJob.indexOf('npm run ui'))
  const manifest = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, 'tests/browser/package.json'), 'utf8'))
  assert.equal(manifest.scripts.connect, 'node loginflow-smoke.mjs')
})

test('catalog-visible releases wait for the attested Docker image without a dependency cycle', () => {
  const dependencies = name => {
    const list = /^ {4}needs: \[([^\]]*)\]/m.exec(JOBS[name] ?? '')
    return list ? list[1].split(',').map(value => value.trim()).filter(Boolean) : []
  }
  assert.deepEqual(dependencies('publish-image'), ['build'])
  assert.deepEqual(dependencies('publish-build'), ['build', 'publish-image'])
  assert.match(JOBS['publish-image'], /uses: actions\/attest@[0-9a-f]{40}/)
  assert.match(JOBS['publish-image'], /push-to-registry: true/)
  for (const name of ['publish-image', 'publish-build']) {
    assert.match(JOBS[name], /if: github\.event_name == 'push' && github\.ref == 'refs\/heads\/main'/)
  }
  const visiting = new Set(), complete = new Set()
  const visit = name => {
    assert.ok(JOBS[name], `unknown dependency ${name}`)
    assert.ok(!visiting.has(name), `dependency cycle through ${name}`)
    if (complete.has(name)) return
    visiting.add(name)
    for (const dependency of dependencies(name)) visit(dependency)
    visiting.delete(name)
    complete.add(name)
  }
  for (const name of Object.keys(JOBS)) visit(name)
})

/** Returns the indented body of a top-level `key:` map, or null when absent. */
function topLevelBlock(text, key) {
  const lines = text.split('\n')
  const start = lines.indexOf(`${key}:`)
  if (start === -1) return null
  const body = []
  for (const line of lines.slice(start + 1)) {
    if (/^\S/.test(line)) break
    body.push(line)
  }
  return body.join('\n')
}

test('a newer push never cancels a running main publication; pull requests still cancel superseded runs', () => {
  const concurrency = topLevelBlock(WORKFLOW, 'concurrency')
  assert.ok(concurrency, 'workflow has a top-level concurrency block')
  // Grouping by ref keeps one PR's runs together so a new PR push supersedes the old run.
  assert.match(concurrency, /^ {2}group: ci-\$\{\{ github\.ref \}\}\s*$/m)
  const cancel = /^ {2}cancel-in-progress:\s*(.+?)\s*$/m.exec(concurrency)
  assert.ok(cancel, 'concurrency block sets cancel-in-progress')
  // Cancelling a running main run can strand a draft release or an unattested image
  // that a routine rerun cannot repair, so only pull-request runs may be cancelled.
  assert.equal(cancel[1], "${{ github.event_name == 'pull_request' }}")
  for (const [name, block] of Object.entries(JOBS)) {
    assert.doesNotMatch(block, /^\s+concurrency:/m, `${name} must not override workflow concurrency`)
    assert.doesNotMatch(block, /cancel-in-progress:/, `${name} must not set cancel-in-progress`)
  }
})

test('the UI smoke job installs and launches Chromium from the harness browser cache', () => {
  // lib/harness.mjs defaults PLAYWRIGHT_BROWSERS_PATH to <repo>/.tools/pw-browsers, so a bare
  // `npx playwright install` (default ~/.cache/ms-playwright) leaves the launch with no browser.
  const harness = fs.readFileSync(path.join(REPO_ROOT, 'tests/browser/lib/harness.mjs'), 'utf8')
  assert.match(harness, /process\.env\.PLAYWRIGHT_BROWSERS_PATH \|\|= path\.join\(TOOLS_DIR, 'pw-browsers'\)/)
  const env = /^ {4}env:\n((?: {6}.*\n)+)/m.exec(uiJob + '\n')
  assert.ok(env, 'UI smoke job needs a job-level env block shared by install and smoke steps')
  assert.match(env[1], /^ {6}PLAYWRIGHT_BROWSERS_PATH: \$\{\{ github\.workspace \}\}\/\.tools\/pw-browsers\s*$/m)
  assert.equal(topLevelBlock(WORKFLOW, 'env'), null, 'browser cache path must not leak into other jobs via workflow env')
  for (const [name, block] of Object.entries(JOBS)) {
    if (name !== uiJobName) assert.doesNotMatch(block, /PLAYWRIGHT_BROWSERS_PATH/, `${name} must not set PLAYWRIGHT_BROWSERS_PATH`)
  }
})

test('the Debian GeoLite fixture uses the existing nobody account instead of a host bookmarksync user', () => {
  const fixture = fs.readFileSync(path.join(REPO_ROOT, 'scripts/debian/test-update-geoip.sh'), 'utf8').replace(/\r\n/g, '\n')
  // Ubuntu CI runners have no bookmarksync account; the fixture must not depend on one or create one.
  assert.doesNotMatch(fixture, /BOOKMARKSYNC_SERVICE_USER=bookmarksync\b/)
  assert.doesNotMatch(fixture, /BOOKMARKSYNC_SERVICE_GROUP=bookmarksync\b/)
  assert.doesNotMatch(fixture, /\b(?:useradd|adduser|groupadd|addgroup|usermod)\b/)
  assert.match(fixture, /^export BOOKMARKSYNC_SERVICE_USER=nobody$/m)
  assert.match(fixture, /^export BOOKMARKSYNC_SERVICE_GROUP="\$\(id -gn nobody\)"$/m)
  // The installed database must carry the configured fixture ownership.
  assert.match(fixture, /stat -c '%U:%G' "\$BOOKMARKSYNC_GEOIP_FILE"\)" == "nobody:\$BOOKMARKSYNC_SERVICE_GROUP"/)
  // Production checks stay intact.
  const helper = fs.readFileSync(path.join(REPO_ROOT, 'scripts/debian/bookmarksync-update-geoip'), 'utf8')
  assert.match(helper, /id "\$service_user" >\/dev\/null 2>&1 \|\| fail "configured service user does not exist"/)
  assert.match(helper, /getent group "\$service_group" >\/dev\/null 2>&1 \|\| fail "configured service group does not exist"/)
  assert.match(helper, /service_user="\$\{BOOKMARKSYNC_SERVICE_USER:-bookmarksync\}"/)
})

test('Debian ownership and approval-queue fixtures run as root in Linux CI', () => {
  for (const script of ['test-bookmarksync.sh', 'test-update-queue.sh']) {
    assert.match(JOBS.docker, new RegExp('run: sudo --preserve-env=PATH bash scripts/debian/' + script.replaceAll('.', '\\.') + '(?:\\r?\\n|$)'))
  }
})
