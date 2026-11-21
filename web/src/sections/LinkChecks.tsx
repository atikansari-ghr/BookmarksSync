import { useEffect, useState } from 'react'
import { ApiError, isCancelled, serverMessage, type Client, type LinkCheckJob, type WorkspaceApi } from '../api'
import { Dialog } from '../components/Dialog'
import { safeHref } from '../workspace'

const EXCLUSION_KEY = 'linkcheck.exclusions'
const MAX_SELECTED = 50

/** Actions and live status for a selection of bookmarks in one private/share scope. */
export function LinkChecks({ api, client, bookmarkIds, canSubmit, onNotice }: {
  api: WorkspaceApi
  client: Client
  bookmarkIds: number[]
  canSubmit: boolean
  onNotice: (message: string) => void
}) {
  const [job, setJob] = useState<LinkCheckJob | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [showExclusions, setShowExclusions] = useState(false)

  useEffect(() => {
    if (!job || job.status === 'completed') return
    let live = true
    const timer = window.setTimeout(() => {
      api.linkCheckStatus(job.id).then((next) => {
        if (live) {
          setError('')
          setJob(next)
        }
      }).catch((err) => {
        if (live && !isCancelled(err)) {
          setError(serverMessage(err, 'The check status could not be loaded.'))
          // Updating the job restarts this effect's bounded polling delay after a
          // transient network/server failure. The job itself remains unchanged.
          setJob((current) => current ? { ...current } : current)
        }
      })
    }, 1200)
    return () => {
      live = false
      window.clearTimeout(timer)
    }
  }, [api, job])

  async function submit() {
    if (!canSubmit || bookmarkIds.length === 0 || bookmarkIds.length > MAX_SELECTED) return
    setBusy(true)
    setError('')
    setJob(null)
    try {
      setJob(await api.submitLinkCheck(bookmarkIds))
      onNotice('Link check queued.')
    } catch (err) {
      if (!isCancelled(err)) setError(serverMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="link-check-panel" aria-label="Link checks">
      {canSubmit && (
        <button type="button" className="btn ghost small" disabled={busy || bookmarkIds.length === 0 || bookmarkIds.length > MAX_SELECTED} onClick={submit}>
          {busy ? 'Queuing link check…' : `Check links${bookmarkIds.length > 0 ? ` (${bookmarkIds.length})` : ''}`}
        </button>
      )}
      <button type="button" className="btn ghost small" onClick={() => setShowExclusions(true)}>Link check exclusions</button>
      {!canSubmit && <span className="muted small">Link checks need edit access to this collection.</span>}
      {bookmarkIds.length > MAX_SELECTED && <span className="error small" role="status">Select no more than {MAX_SELECTED} bookmarks for one check.</span>}
      {error && <p className="error small" role="alert">{error}</p>}
      {job && (
        <div className="link-check-results" aria-live="polite">
          <p className="small muted">Job #{job.id}: {job.status === 'completed' ? 'Complete' : `Checking ${job.done} of ${job.total}`}</p>
          <ul className="list">
            {job.results.map((r) => {
              const href = safeHref(r.url)
              return (
                <li className="link-check-result" key={r.bookmarkId}>
                  {href ? <a href={href} target="_blank" rel="noopener noreferrer">{r.url}</a> : <span>{r.url}</span>}
                  <strong className={`check-${r.status}`}>{resultLabel(r.status, r.httpStatus)}</strong>
                </li>
              )
            })}
          </ul>
        </div>
      )}
      {showExclusions && <ExclusionsDialog client={client} onClose={() => setShowExclusions(false)} onSaved={() => onNotice('Link check exclusions saved.')} />}
    </section>
  )
}

function resultLabel(status: string, httpStatus?: number | null) {
  if (status === 'ok') return httpStatus ? `OK (${httpStatus})` : 'OK'
  if (status === 'broken') return httpStatus ? `Broken (${httpStatus})` : 'Broken'
  if (status === 'excluded') return 'Excluded'
  if (status === 'blocked') return 'Blocked'
  if (status === 'skipped') return 'Skipped'
  if (status === 'pending') return 'Pending'
  return 'Could not check'
}

function ExclusionsDialog({ client, onClose, onSaved }: { client: Client; onClose: () => void; onSaved: () => void }) {
  const [value, setValue] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [loadFailed, setLoadFailed] = useState(false)

  async function load() {
    setError('')
    setLoadFailed(false)
    try {
      const res = await client.request<{ settings: Record<string, string> }>('GET', '/api/v1/me/settings')
      const raw = res.settings[EXCLUSION_KEY] ?? '[]'
      const entries = JSON.parse(raw)
      if (!Array.isArray(entries) || entries.some((x) => typeof x !== 'string')) throw new Error('invalid stored list')
      setValue(entries.join('\n'))
    } catch (err) {
      setLoadFailed(true)
      setError(err instanceof ApiError ? serverMessage(err) : 'The saved exclusions could not be read.')
    }
  }

  useEffect(() => { void load() }, [])

  async function save() {
    if (value === null) return
    const entries = value.split(/\r?\n/).map((x) => x.trim()).filter(Boolean)
    if (entries.length > 100) {
      setError('Enter at most 100 exclusions.')
      return
    }
    setError('')
    setLoadFailed(false)
    setBusy(true)
    try {
      await client.request('PATCH', '/api/v1/me/settings', { [EXCLUSION_KEY]: JSON.stringify(entries) })
      onSaved()
      onClose()
    } catch (err) {
      if (!isCancelled(err)) setError(serverMessage(err, 'The exclusions could not be saved.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title="Link check exclusions" onClose={onClose}>
      <div className="stack compact">
        <p className="muted">Enter one domain, IP address, or CIDR range per line. Domains also exclude their subdomains. These entries skip checks; they never make a private address safe.</p>
      {error && <p role="alert" className="error">{error}{loadFailed && <button type="button" className="link-button" onClick={() => void load()}>Retry</button>}</p>}
        <label className="field">
          <span>Domains, IP addresses or CIDR ranges</span>
          <textarea rows={6} value={value ?? ''} disabled={value === null || busy} onChange={(e) => setValue(e.target.value)} />
        </label>
        <p className="small muted">Examples: <code>intranet.example</code>, <code>10.0.0.0/8</code></p>
        <div className="actions">
          <button type="button" className="btn ghost" onClick={onClose}>Cancel</button>
          <button type="button" className="btn primary" disabled={value === null || busy} onClick={save}>Save exclusions</button>
        </div>
      </div>
    </Dialog>
  )
}

