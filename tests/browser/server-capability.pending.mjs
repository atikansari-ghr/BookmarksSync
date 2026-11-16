// Server capability tests against the real BookmarksSync server.
// STATUS: PENDING - the protocol server does not exist yet. This file exits 0 but
// reports every check as PENDING; it must never be read as a pass.
//
// When the server exists, each case will: start the server on a loopback port with a
// fresh data dir, launch a separate isolated profile (launchWithExtension), create a
// sandbox folder, importAccount() with the app credential from the environment
// (BOOKMARKSYNC_APP_TOKEN, BOOKMARKSYNC_URL), syncAccount() via the genuine background
// RPC, then assert actual chrome.bookmarks contents and the server request log.
import { writeResult } from './lib/harness.mjs'

const cases = [
  'capabilities + lock + download-only sync issues no writes (server request log)',
  'merge sync uploads a sandbox folder tree and order via childorder',
  'move/rename/delete round-trip between two isolated profiles',
  'duplicate URL placements across folders',
  'native roots map to Bookmarks Bar / Other Bookmarks on each browser',
  'read-only share credential: writes rejected with 403, download-only works',
  'lock contention returns 423 and floccus reschedules'
]
const missing = ['BOOKMARKSYNC_URL', 'BOOKMARKSYNC_APP_TOKEN'].filter(k => !process.env[k])
for (const c of cases) console.log(`PENDING  ${c}`)
console.log(`\n${cases.length} cases PENDING (no server implementation). Env not set: ${missing.join(', ') || 'none'}`)
console.log(`result file: ${writeResult('server-capability', { status: 'PENDING', cases: cases.map(name => ({ name, status: 'PENDING' })) })}`)
