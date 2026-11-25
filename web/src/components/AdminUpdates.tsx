import { useEffect, useState } from 'react'
import { ApiError } from '../api'
import { useApp } from '../context'

type Release = { tag: string; commit: string; publishedAt: string; url: string }
type Catalog = { installedCommit?: string; releases: Release[]; latest?: Release; updateAvailable: boolean; applyAvailable: boolean }

export function AdminUpdates() {
  const { client } = useApp()
  const [catalog, setCatalog] = useState<Catalog | null>(null)
  const [busy, setBusy] = useState(true)
  const [error, setError] = useState('')
  const [selectedTag, setSelectedTag] = useState('')
  const [approved, setApproved] = useState(false)
  const [notice, setNotice] = useState('')

  async function load() {
    setBusy(true)
    setError('')
    try {
      const result = await client.request<Catalog>('GET', '/api/v1/admin/updates')
      const releases = result.releases ?? []
      setCatalog({ ...result, releases })
      setSelectedTag((current) => releases.some((release) => release.tag === current) ? current : result.latest?.tag ?? '')
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Release information could not be loaded.')
    } finally {
      setBusy(false)
    }
  }

  useEffect(() => { void load() }, [client])

  async function approveUpdate() {
    setError(''); setNotice(''); setBusy(true)
    try {
      const result = await client.request<{ status: string; tag: string; job: string }>('POST', '/api/v1/admin/updates/approve', { tag: selectedTag })
      setNotice(`Update ${result.tag} was queued as ${result.job}. The server will restart during the update.`)
      setApproved(false)
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'The selected update could not be queued.')
    } finally { setBusy(false) }
  }

  return <section className="ws-card glass admin-section" aria-labelledby="updates-title">
    <div className="admin-section-head">
      <div><h2 id="updates-title">Software updates</h2><p className="muted small">Published main-branch builds are listed here. Installing a release always requires your approval.</p></div>
      <button className="btn secondary" type="button" onClick={() => void load()} disabled={busy}>Check now</button>
    </div>
    {busy && <p className="muted small" role="status">Checking the release catalog…</p>}
    {error && <p className="error" role="alert">{error}</p>}
    {!busy && !error && catalog && <>
      <p className="muted small">Installed commit: <code>{catalog.installedCommit || 'development build'}</code></p>
      {catalog.latest ? <div className="admin-update-release">
        <div><strong>{catalog.updateAvailable ? 'Update available' : 'Latest published build'}</strong><p className="muted small">{catalog.latest.tag} · published {new Date(catalog.latest.publishedAt).toLocaleString()}</p></div>
        <a className="btn secondary" href={catalog.latest.url} target="_blank" rel="noreferrer">View release</a>
      </div> : <p className="muted small">No eligible published Linux builds were found.</p>}
      {catalog.updateAvailable && catalog.applyAvailable && <div className="stack">
        <label className="field"><span>Release</span><select aria-label="Release to install" value={selectedTag} onChange={(e) => { setSelectedTag(e.target.value); setApproved(false) }}>{catalog.releases.map((release) => <option key={release.tag} value={release.tag}>{release.tag} · {new Date(release.publishedAt).toLocaleDateString()}</option>)}</select></label>
        <label className="check"><input type="checkbox" checked={approved} onChange={(e) => setApproved(e.target.checked)} /><span>I approve installing this release. The service will restart and create a rollback backup.</span></label>
        <div><button className="btn primary" type="button" disabled={busy || !approved || !selectedTag} onClick={() => void approveUpdate()}>{busy ? 'Queueing update…' : 'Install selected update'}</button></div>
      </div>}
      {catalog.updateAvailable && !catalog.applyAvailable && <p className="muted small">This host does not have the restricted browser updater installed. On Debian, apply a selected build with the documented `bookmarksync upgrade --tag` command; the installer verifies provenance and creates a rollback snapshot.</p>}
      {notice && <p className="success" role="status">{notice}</p>}
    </>}
  </section>
}
