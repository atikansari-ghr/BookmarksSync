// Unit tests for the helpers behind the real server-sync command (lib/server-sync.mjs).
// They never start a browser. Process helpers are exercised with throw-away `node` children.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import net from 'node:net'
import * as serverSync from '../lib/server-sync.mjs'
import {
  sanitize, sanitizeDeep, parseRequestLog, summarizeRequests, makeRunRoot, removeRunRoot,
  runCleanupSteps, classify, Recorder, parseSecretFromCliStdout, pickLoopbackPort, findGo,
  startProcessServer, treeEquals, stripTree
} from '../lib/server-sync.mjs'

const SECRET = 'bms1.7.' + 'A'.repeat(43)
const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), 'bh-ss-'))

test('disposable servers explicitly allow only loopback management unless a test overrides the policy', async () => {
  for (const [env, expected] of [[{}, '127.0.0.0/8,::1/128'], [{ BOOKMARKSYNC_MANAGEMENT_CIDRS: '' }, '']]) {
    const port = await pickLoopbackPort()
    const script = `require('node:http').createServer((req,res)=>{res.writeHead(process.env.BOOKMARKSYNC_MANAGEMENT_CIDRS===process.argv[1]?200:403);res.end()}).listen(${port},'127.0.0.1')`
    let server
    try {
      server = await startProcessServer({ command: process.execPath, args: ['-e', script, expected], env, readyUrl: `http://127.0.0.1:${port}/readyz`, startupTimeoutMs: 2500 })
      assert.ok(server.pid)
    } finally {
      if (server) await server.stop()
    }
  }
})

test('wire folder IDs convert to positive CLI IDs without accepting virtual or invalid IDs', () => {
  const convert = serverSync.wireFolderIdToInternal
  assert.equal(typeof convert, 'function')
  assert.equal(convert(-2), 1)
  assert.equal(convert(-3), 2)
  assert.throws(() => convert(-1), /concrete folder/i)
  for (const value of [0, 1, -0, -1.5, NaN, Infinity, Number.MIN_SAFE_INTEGER - 1]) {
    assert.throws(() => convert(value), /negative wire namespace/i)
  }
})

test('sanitize removes known secrets, bms1 tokens and Basic authorization values', () => {
  const secret2 = 'p@ss-word-value'
  const txt = `x ${SECRET} y ${secret2} Authorization: Basic YWxpY2U6c2VjcmV0 z bms1.12.${'B'.repeat(43)}`
  const out = sanitize(txt, [secret2])
  assert.ok(!out.includes(SECRET) && !out.includes(secret2) && !out.includes('YWxpY2U6c2VjcmV0'))
  assert.ok(!/bms1\.\d+\.[A-Za-z0-9_-]{43}/.test(out))
  assert.match(out, /\[REDACTED\]/)
})

test('sanitize ignores empty/short secrets instead of mangling the text', () => {
  assert.equal(sanitize('hello world', ['', null, undefined, 'ab']), 'hello world')
})

test('sanitizeDeep cleans nested strings, arrays and error messages', () => {
  const obj = { a: [`t ${SECRET}`], b: { c: new Error(`boom ${SECRET}`).message }, n: 5, ok: true }
  const out = JSON.stringify(sanitizeDeep(obj, []))
  assert.ok(!out.includes(SECRET))
  assert.equal(sanitizeDeep(obj, []).n, 5)
})

test('parseRequestLog reads slog text lines and ignores everything else', () => {
  const log = [
    'time=2026-11-17T10:00:00Z level=INFO msg="floccus request" method=GET path=/index.php/ocs/v2.php/cloud/capabilities credential=3 status=200',
    'time=2026-11-17T10:00:01Z level=INFO msg="server listening" addr=127.0.0.1:1',
    'time=2026-11-17T10:00:02Z level=INFO msg="floccus request" method=POST path=/index.php/apps/bookmarks/public/rest/v2/lock credential=3 status=423',
    'garbage'
  ].join('\n')
  const e = parseRequestLog(log)
  assert.equal(e.length, 2)
  assert.deepEqual(e[1], { method: 'POST', path: '/index.php/apps/bookmarks/public/rest/v2/lock', credential: 3, status: 423 })
})

test('summarizeRequests filters by credential and classifies writes vs reads/lock verbs', () => {
  const R = '/index.php/apps/bookmarks/public/rest/v2'
  const entries = [
    { method: 'GET', path: `${R}/folder/-1/children`, credential: 3, status: 200 },
    { method: 'POST', path: `${R}/lock`, credential: 3, status: 200 },
    { method: 'DELETE', path: `${R}/lock`, credential: 3, status: 200 },
    { method: 'POST', path: `${R}/folder`, credential: 3, status: 200 },
    { method: 'PATCH', path: `${R}/folder/5/childorder`, credential: 3, status: 200 },
    { method: 'POST', path: `${R}/lock`, credential: 9, status: 200 }
  ]
  const s = summarizeRequests(entries, 3)
  assert.equal(s.total, 5)
  assert.deepEqual(s.writes.map(w => `${w.method} ${w.path.slice(R.length)}`), ['POST /folder', 'PATCH /folder/5/childorder'])
  assert.equal(s.onlyReadsAndLock, false)
  assert.equal(summarizeRequests(entries.slice(0, 3), 3).onlyReadsAndLock, true)
})

test('makeRunRoot creates a marked dir inside the harness root; removeRunRoot deletes only that', () => {
  const base = tmp(); const root = path.join(base, 'server-sync')
  const dir = makeRunRoot({ root, toolsDir: base })
  assert.ok(fs.existsSync(path.join(dir, '.bookmarksync-server-sync-run')))
  fs.writeFileSync(path.join(dir, 'x.db'), 'data')
  assert.equal(removeRunRoot(dir, { root }), true)
  assert.ok(!fs.existsSync(dir))
  fs.rmSync(base, { recursive: true, force: true })
})

test('removeRunRoot refuses outside, unmarked and link targets', () => {
  const base = tmp(); const root = path.join(base, 'server-sync'); fs.mkdirSync(root)
  const outside = path.join(base, 'outside'); fs.mkdirSync(outside)
  fs.writeFileSync(path.join(outside, '.bookmarksync-server-sync-run'), '')
  assert.throws(() => removeRunRoot(outside, { root }), /outside/)
  const unmarked = path.join(root, 'u'); fs.mkdirSync(unmarked)
  assert.throws(() => removeRunRoot(unmarked, { root }), /unmarked/)
  assert.ok(fs.existsSync(unmarked))
  const link = path.join(root, 'l')
  try { fs.symlinkSync(outside, link, 'junction') } catch { fs.rmSync(base, { recursive: true, force: true }); return }
  assert.throws(() => removeRunRoot(link, { root }), /link|outside|unmarked/)
  assert.ok(fs.existsSync(path.join(outside, '.bookmarksync-server-sync-run')))
  fs.rmSync(base, { recursive: true, force: true })
})

test('runCleanupSteps runs every step even when earlier ones fail and reports all failures', async () => {
  const ran = []
  const res = await runCleanupSteps([
    ['stop server', async () => { ran.push('a'); throw new Error('kill failed') }],
    ['close browser A', async () => { ran.push('b') }],
    ['remove data', async () => { ran.push('c'); throw new Error('rm failed') }],
    ['close browser B', () => { ran.push('d') }]
  ])
  assert.deepEqual(ran, ['a', 'b', 'c', 'd'])
  assert.equal(res.clean, false)
  assert.deepEqual(res.failures.map(f => f.step), ['stop server', 'remove data'])
  assert.match(res.failures[0].error, /kill failed/)
})

test('runCleanupSteps bounds a step that never settles', async () => {
  const res = await runCleanupSteps([['hang', () => new Promise(() => {})], ['after', async () => {}]], { stepTimeoutMs: 50 })
  assert.equal(res.clean, false)
  assert.match(res.failures[0].error, /timeout/)
})

test('runCleanupSteps is clean when everything succeeds', async () => {
  assert.deepEqual(await runCleanupSteps([['x', async () => {}]]), { clean: true, failures: [] })
})

test('classify never reports PASS without a real PASS, and maps NOT_RUN to exit 2', () => {
  assert.deepEqual(classify([]), { status: 'NOT_RUN', code: 2 })
  assert.deepEqual(classify(['PASS', 'PASS']), { status: 'PASS', code: 0 })
  assert.deepEqual(classify(['PASS', 'NOT_RUN']), { status: 'NOT_RUN', code: 2 })
  assert.deepEqual(classify(['PASS', 'FAIL', 'NOT_RUN']), { status: 'FAIL', code: 1 })
  assert.deepEqual(classify(['PASS', 'BLOCKED']), { status: 'BLOCKED', code: 3 })
  assert.deepEqual(classify(['NOT_RUN', 'BLOCKED']), { status: 'BLOCKED', code: 3 })
  assert.throws(() => classify(['PASS', 'PENDING']), /unknown status/)
})

test('Recorder.passSync refuses a sync PASS with no real server requests or no native tree evidence', () => {
  const r = new Recorder({ log: () => {} })
  r.passSync('stub-like', { serverRequests: 0, nativeTree: true })
  r.passSync('no tree', { serverRequests: 4 })
  r.passSync('real', { serverRequests: 4, nativeTree: { children: [] } })
  assert.deepEqual(r.cases.map(c => c.status), ['FAIL', 'FAIL', 'PASS'])
  assert.equal(r.finish().code, 1)
})

test('Recorder sanitizes details and lets NOT_RUN keep the run from passing', () => {
  const r = new Recorder({ log: () => {}, secrets: ['hunter2-hunter2'] })
  r.pass('a', 'ok hunter2-hunter2')
  r.notRun('firefox', 'geckodriver missing')
  assert.ok(!JSON.stringify(r.cases).includes('hunter2-hunter2'))
  assert.deepEqual(r.finish(), { status: 'NOT_RUN', code: 2 })
})

test('parseSecretFromCliStdout accepts exactly one bms1 secret line and never echoes bad output', () => {
  assert.equal(parseSecretFromCliStdout(`${SECRET}\n`), SECRET)
  assert.throws(() => parseSecretFromCliStdout('oops sensitive text\n'), (e) => !e.message.includes('sensitive'))
  assert.throws(() => parseSecretFromCliStdout(`${SECRET}\n${SECRET}\n`))
})

test('pickLoopbackPort returns a bindable unprivileged port', async () => {
  const p = await pickLoopbackPort()
  assert.ok(p >= 1024 && p <= 65535)
  await new Promise((res, rej) => { const s = net.createServer().once('error', rej).listen(p, '127.0.0.1', () => s.close(res)) })
})

test('findGo reports missing instead of guessing', () => {
  assert.deepEqual(findGo({ env: {}, exists: () => false, candidates: ['/nope/go'] }), { missing: true })
  assert.equal(findGo({ env: { BH_GO_EXE: '/x/go' }, exists: p => p === '/x/go', candidates: [] }).exe, '/x/go')
})

const fakeServer = (body) => ['-e', body]
const READY = `require('http').createServer((q,s)=>{console.log('floccus request method=GET path=/readyz');s.end('ok')}).listen(Number(process.env.PORT),'127.0.0.1')`

test('startProcessServer waits for readiness, captures logs in memory, and stop() ends the process', async () => {
  const port = await pickLoopbackPort()
  const srv = await startProcessServer({
    command: process.execPath, args: fakeServer(READY), env: { PORT: String(port) },
    readyUrl: `http://127.0.0.1:${port}/readyz`, startupTimeoutMs: 15000
  })
  const pid = srv.pid
  assert.ok(pid > 0)
  await fetch(`http://127.0.0.1:${port}/readyz`)
  await new Promise(r => setTimeout(r, 100))
  assert.match(srv.logs(), /floccus request/)
  await srv.stop()
  assert.throws(() => process.kill(pid, 0))
  await srv.stop() // idempotent
})

test('startProcessServer kills the child and rejects when it never becomes ready', async () => {
  const port = await pickLoopbackPort()
  let pid
  await assert.rejects(startProcessServer({
    command: process.execPath, args: fakeServer('setInterval(()=>{},1000)'), env: {},
    readyUrl: `http://127.0.0.1:${port}/readyz`, startupTimeoutMs: 600, onSpawn: p => { pid = p }
  }), /not ready|timeout/)
  assert.ok(pid > 0)
  await new Promise(r => setTimeout(r, 300))
  assert.throws(() => process.kill(pid, 0))
})

test('startProcessServer rejects when the child exits early, with its sanitized output', async () => {
  const port = await pickLoopbackPort()
  await assert.rejects(startProcessServer({
    command: process.execPath, args: fakeServer(`console.error('fatal ${SECRET}');process.exit(3)`), env: {},
    readyUrl: `http://127.0.0.1:${port}/readyz`, startupTimeoutMs: 5000, secrets: []
  }), (e) => /exited/.test(e.message) && !e.message.includes(SECRET))
})

test('stripTree/treeEquals compare native trees by title/url/order only', () => {
  const a = { id: '5', parentId: '1', index: 0, title: 'S', children: [{ id: '6', title: 'a', url: 'https://a/' }, { id: '7', title: 'f', children: [] }] }
  const b = { id: '9', parentId: '2', index: 3, title: 'S', children: [{ id: '10', title: 'a', url: 'https://a/' }, { id: '11', title: 'f', children: [] }] }
  assert.deepEqual(stripTree(a), { title: 'S', children: [{ title: 'a', url: 'https://a/' }, { title: 'f', children: [] }] })
  assert.ok(treeEquals(a, b))
  b.children.reverse()
  assert.ok(!treeEquals(a, b))
})

test('startRecordingProxy forwards requests, records method/path/body/response, and never stores Authorization', async () => {
  const http = await import('node:http')
  const startRecordingProxy = (await import('../lib/server-sync.mjs')).startRecordingProxy ?? (() => { throw new Error('startRecordingProxy is not implemented') })
  const up = http.createServer((q, s) => { let b = ''; q.on('data', d => { b += d }); q.on('end', () => { s.writeHead(200, { 'content-type': 'application/json' }); s.end(JSON.stringify({ echo: b, auth: q.headers.authorization ?? null })) }) })
  await new Promise(r => up.listen(0, '127.0.0.1', r))
  let proxy
  try {
  proxy = await startRecordingProxy({ targetPort: up.address().port })
  const r = await fetch(`${proxy.url}/rest/folder/6/childorder?x=1`, { method: 'PATCH', headers: { authorization: 'Basic c2VjcmV0c2VjcmV0', 'content-type': 'application/json' }, body: '{"data":[{"id":"2","type":"bookmark"}]}' })
  const j = await r.json()
  assert.equal(j.echo, '{"data":[{"id":"2","type":"bookmark"}]}')
  assert.match(j.auth, /^Basic /) // forwarded upstream so the real server still authenticates
  const rec = proxy.records()
  assert.equal(rec.length, 1)
  assert.equal(rec[0].method, 'PATCH'); assert.equal(rec[0].path, '/rest/folder/6/childorder'); assert.equal(rec[0].status, 200)
  assert.equal(rec[0].requestBody, '{"data":[{"id":"2","type":"bookmark"}]}')
  assert.ok(!JSON.stringify(rec).includes('c2VjcmV0'))
  } finally { await proxy?.stop(); up.close() }
})

test('stripTree treats a null url (Firefox folders) like an absent url', () => {
  const ff = { id: 'x', title: 'F', url: null, children: [{ id: 'y', title: 'a', url: 'https://a/' }] }
  const other = { title: 'F', children: [{ title: 'a', url: 'https://a/' }] }
  assert.deepEqual(stripTree(ff), { title: 'F', children: [{ title: 'a', url: 'https://a/' }] })
  assert.ok(treeEquals(ff, other))
})

// Real floccus 5.13.0 log excerpt (Chrome, failing run) condensed to the two relevant lines.
const COLLISION_LOG = [
  "ReconcileReorders: Inserting created item into order { creation: { type: 'CREATE', payload: - #12[O1](https://o1.example/) parentId: 6, oldItem: - #7;7[O1](https://o1.example/) parentId: 7 }, reorder: { type: 'REORDER', oldItem: + #9[Folder F] parentId: 5, hash: undefined - #3;6[G1](https://g1.example/) parentId: 9, payload: + #6[Folder F] parentId: 5, hash: undefined - #3;6[G1](https://g1.example/) parentId: 6, order: [ [Object], [Object] ] } }",
  "(local)ORDERFOLDER { id: '9', order: [ { type: 'folder', id: '13' }, { type: 'bookmark', id: '12' }, { type: 'bookmark', id: '14' }, { type: 'bookmark', id: '15' } ] }"
]
const CLEAN_LOG = [
  "ReconcileReorders: Inserting created item into order { creation: { type: 'CREATE', payload: - #12[O1](https://o1.example/) parentId: 6, oldItem: - #7;7[O1](https://o1.example/) parentId: 7 }, reorder: { type: 'REORDER', oldItem: + #7[Other] parentId: 5, payload: + #7[Other] parentId: 5, order: [ [Object] ] } }"
]

test('detectIdNamespaceCollision finds a created item inserted into the order of an unrelated folder with the same numeric id', async () => {
  const { detectIdNamespaceCollision } = await import('../lib/server-sync.mjs')
  const hits = detectIdNamespaceCollision(COLLISION_LOG)
  assert.equal(hits.length, 1)
  assert.deepEqual(hits[0], { createdParentId: '6', reorderPayloadId: '6', reorderPayloadTitle: 'Folder F', createdTitle: 'O1', status: 'CONFIRMED', contaminatedFolders: ['9'] })
  assert.deepEqual(detectIdNamespaceCollision(CLEAN_LOG), [])
  assert.deepEqual(detectIdNamespaceCollision([]), [])
})
