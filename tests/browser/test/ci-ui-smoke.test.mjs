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
