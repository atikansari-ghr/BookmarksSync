// Session-level helpers for offline-sync.mjs. They run small self-contained functions inside the
// genuine floccus 5.13.0 background (via the browser-session `run` hook) and manage the marked run
// directory. Nothing here edits extension code, replaces a floccus adapter or simulates a sync.
import path from 'node:path'
import { TOOLS_DIR } from './harness.mjs'
import { makeRunRoot, guardedRunRemoval } from './server-sync.mjs'

export const OFFLINE_ROOT = path.join(TOOLS_DIR, 'offline-sync')

export const makeOfflineRunRoot = () => makeRunRoot({ root: OFFLINE_ROOT })
export const guardedOfflineRemoval = (dir, servers) => guardedRunRemoval(dir, servers, { root: OFFLINE_ROOT })

/**
 * Account settings a user can change in the floccus options page. Only these keys may be written,
 * and syncInterval must be one of the options page's own steps (5 minutes minimum in 5.13.0).
 */
export const UI_SYNC_INTERVAL_STEPS = [5, 10, 15, 20, 25, 30, 45, 60, 120, 180, 240, 360, 480, 720, 960, 1440]
const SETTABLE = new Set(['enabled', 'syncIntervalEnabled', 'syncInterval'])

export function validateAccountSettings(fields) {
  for (const [k, v] of Object.entries(fields ?? {})) {
    if (!SETTABLE.has(k)) throw new Error(`refusing to write account field ${k}`)
    if (k === 'syncInterval' && !UI_SYNC_INTERVAL_STEPS.includes(v)) throw new Error(`syncInterval ${v} is not an options-page step`)
    if (k !== 'syncInterval' && typeof v !== 'boolean') throw new Error(`${k} must be boolean`)
  }
  if (Object.keys(fields ?? {}).length === 0) throw new Error('no account fields to write')
  return fields
}

// In-page: merge settings into the same `accounts` storage map BrowserAccountStorage.setAccountData writes.
const fnSetFields = async ({ id, fields }) => {
  const api = typeof browser !== 'undefined' ? browser : chrome
  const { accounts } = await api.storage.local.get('accounts')
  let map = accounts
  while (typeof map === 'string') map = JSON.parse(map)
  if (!map || !map[id]) return false
  map[id] = { ...map[id], ...fields }
  await api.storage.local.set({ accounts: JSON.stringify(map) })
  return true
}

// In-page: storage key names (and value sizes) that mention the account id — evidence the profile
// still holds floccus's per-account cache/mappings. Values are not returned.
const fnCacheKeys = async (id) => {
  const api = typeof browser !== 'undefined' ? browser : chrome
  const all = await api.storage.local.get(null)
  return Object.keys(all).filter(k => k !== 'accounts' && k.includes(id))
    .map(k => ({ key: k.replace(id, '<account>'), bytes: JSON.stringify(all[k]).length }))
}

export async function setAccountSettings(session, id, fields) {
  validateAccountSettings(fields)
  const ok = await session.run(fnSetFields, { id, fields })
  if (!ok) throw new Error('account not found in profile storage')
  return true
}

export const cacheKeys = (session, id) => session.run(fnCacheKeys, id)
