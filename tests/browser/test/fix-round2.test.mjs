// Fix round 2 regressions: standalone order-trace ownership of a server that failed to start, and
// test-process hygiene (a failing assertion must never leak a child process).
import test, { after } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { spawn } from 'node:child_process'
import { runOrderTrace } from '../order-trace.mjs'
import * as SS from '../lib/server-sync.mjs'

const spawned = []
after(() => { for (const c of spawned) { try { c.kill() } catch {} } }) // backstop: never leave a test child behind
const GOOD_SECRET = 'bms1.3.' + 'A'.repeat(43)

/** Fake collaborators: no process, no browser, no network; the run directory and its data are real. */
function fakeDeps({ startupError }) {
  const seen = { started: false, openedBrowser: false, dataFile: null }
  return {
    seen,
    deps: {
      findGo: () => ({ exe: 'go-not-run' }),
      makeRunRoot: () => SS.makeRunRoot(), // real marked directory under .tools/server-sync
      pickLoopbackPort: async () => 45678,
      runCommand: async ({ args, cwd }) => {
        if (args[0] === 'user') { // the CLI "creates" the database: leave live data in the run directory
          const data = args[args.indexOf('--data-dir') + 1]
          fs.mkdirSync(data, { recursive: true })
          seen.dataFile = path.join(data, 'bookmarksync.db')
          fs.writeFileSync(seen.dataFile, 'LIVE USER DATA')
        }
        return { code: 0, stdout: args[0] === 'credential' ? GOOD_SECRET + '\n' : '', stderr: '' }
      },
      startProcessServer: async () => { seen.started = true; throw startupError },
      startRecordingProxy: async () => { throw new Error('proxy must not start after a server startup failure') },
      openBrowser: async () => { seen.openedBrowser = true; throw new Error('browser must not start after a server startup failure') }
    }
  }
}

test('order-trace: startup failure with a server that could not be stopped keeps the handle, refuses deletion and keeps run data', async () => {
  const err = Object.assign(new Error('server not ready within 10ms; server process 4242 could not be stopped (timeout)'), {
    processStillRunning: true, pid: 4242, stopError: 'timeout'
  })
  const { deps, seen } = fakeDeps({ startupError: err })
  let runDir
  try {
    const r = await runOrderTrace({ name: 'chrome', attempts: 1 }, deps)
    runDir = r.runDir
    assert.equal(seen.started, true)
    assert.equal(seen.openedBrowser, false)
    assert.equal(r.exitCode, 1)
    assert.equal(r.out.cleanup.clean, false)
    const f = r.out.cleanup.failures.find(x => x.step === 'remove run dir')
    assert.ok(f, 'the guarded run directory removal must be reported as a failure')
    assert.match(f.error, /RETAINED/)
    assert.ok(f.error.includes(runDir), 'the retained path is named')
    assert.match(f.error, /4242/, 'the uncertain pid is named')
    assert.ok(fs.existsSync(seen.dataFile), 'live server data must still exist')
    assert.match(r.out.error, /could not be stopped/)
  } finally {
    if (runDir) SS.removeRunRoot(runDir) // test-owned cleanup of the deliberately retained directory
  }
})

test('order-trace: a startup failure with a confirmed-stopped server still cleans the run directory', async () => {
  const err = Object.assign(new Error('server not ready within 10ms'), { processStillRunning: false })
  const { deps, seen } = fakeDeps({ startupError: err })
  const r = await runOrderTrace({ name: 'chrome', attempts: 1 }, deps)
  assert.equal(r.exitCode, 1) // the startup failure itself is still a failure
  assert.equal(r.out.cleanup.clean, true)
  assert.ok(!fs.existsSync(r.runDir), 'nothing retained when no server may be running')
  assert.ok(!fs.existsSync(seen.dataFile))
})

test('order-trace: missing go toolchain is NOT_RUN (exit 2) and creates nothing', async () => {
  const r = await runOrderTrace({}, { ...realDepsStub(), findGo: () => ({ missing: true }) })
  assert.equal(r.exitCode, 2)
  assert.equal(r.runDir, null)
})
function realDepsStub() { return { makeRunRoot: () => { throw new Error('must not be called') } } }

// ---- test-process hygiene ------------------------------------------------------------------------

test('withProcessCleanup kills and awaits the child even when the body throws', async () => {
  const child = spawn(process.execPath, ['-e', 'setInterval(()=>{},1000)'], { stdio: 'ignore' }); spawned.push(child)
  const pid = child.pid
  await assert.rejects(SS.withProcessCleanup(() => pid, async () => {
    assert.equal(1, 2, 'an assertion in the body fails')
  }), /assertion|equal/i)
  assert.throws(() => process.kill(pid, 0), 'the child must be gone once the helper returns')
})

test('withProcessCleanup also cleans up when the pid is only known later and when the body succeeds', async () => {
  const child = spawn(process.execPath, ['-e', 'setInterval(()=>{},1000)'], { stdio: 'ignore' }); spawned.push(child)
  let pid
  await SS.withProcessCleanup(() => pid, async () => { pid = child.pid })
  assert.throws(() => process.kill(child.pid, 0))
  // nothing to clean when no pid was ever produced
  await SS.withProcessCleanup(() => undefined, async () => {})
})
