import { useState, type FormEvent } from 'react'
import { describe, isCancelled } from '../api'
import { useApp } from '../context'
import { ConfirmDialog } from '../components/Dialog'
import { Busy, CopyButton, Reveal, Section, formatTime } from '../components/common'
import type { Notify } from '../pages/SecurityPage'
import { useLoad } from './useLoad'

interface Connection {
  id: number
  label: string
  kind: string
  shareId?: number
  mode: string
  createdAt: string
  lastUsedAt?: string | null
  revoked?: boolean
}

interface IncomingShare {
  id: number
  folderTitle: string
  mode: string
}

export function ConnectionsSection({ notify, username }: { notify: Notify; username: string }) {
  const { client } = useApp()
  const [load, reload] = useLoad(() => client.request<{ connections: Connection[] }>('GET', '/api/v1/me/connections').then((d) => (d.connections ?? []).filter((c) => !c.revoked)))
  const [shares] = useLoad(() => client.request<{ shares: IncomingShare[] }>('GET', '/api/v1/shares/incoming').then((d) => d.shares ?? []))
  const [label, setLabel] = useState('')
  const [target, setTarget] = useState('')
  const [mode, setMode] = useState('edit')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [secret, setSecret] = useState('')
  const [revoking, setRevoking] = useState<Connection | null>(null)
  const shareList = shares.state === 'ready' ? shares.data : []
  const shareTitle = (id?: number) => shareList.find((s) => s.id === id)?.folderTitle
  // A view-only share can only back a read-only connection; the server refuses edit.
  const isViewOnly = (id: string) => shareList.find((s) => String(s.id) === id)?.mode === 'view'
  const viewOnly = isViewOnly(target)

  function chooseTarget(id: string) {
    setTarget(id)
    setMode(isViewOnly(id) ? 'view' : 'edit')
  }

  async function create(e: FormEvent) {
    e.preventDefault()
    setError('')
    if (!label.trim()) return setError('Enter a connection name, for example the browser or device.')
    const body: Record<string, unknown> = { label: label.trim(), mode }
    if (target) body.shareId = Number(target)
    setBusy(true)
    try {
      const d = await client.request<{ secret: string }>('POST', '/api/v1/me/connections', body)
      setSecret(d.secret)
      setLabel('')
      setTarget('')
      setMode('edit')
    } catch (err) {
      if (!isCancelled(err)) setError(describe(err, 'The connection could not be created.'))
    } finally {
      setBusy(false)
    }
  }

  async function revoke(c: Connection) {
    try {
      await client.request('DELETE', `/api/v1/me/connections/${c.id}`)
      setRevoking(null)
      notify('Connection revoked.')
      await reload()
    } catch (err) {
      setRevoking(null)
      setError(describe(err, 'The connection could not be revoked.'))
    }
  }

  return (
    <Section title="Floccus connections" intro="Each browser running Floccus gets its own app password, which you can revoke on its own. Your sign-in password is never used for sync.">
      {secret ? (
        <Reveal title="Set up Floccus now" onDone={() => { setSecret(''); void reload() }}>
          <p className="muted">In Floccus, add a “Nextcloud Bookmarks” account with these details. The app password will not be shown again.</p>
          <dl className="details">
            <dt>Server URL</dt>
            <dd><code>{window.location.origin}</code></dd>
            <dt>Username</dt>
            <dd><code>{username}</code></dd>
            <dt>App password</dt>
            <dd className="secret"><code>{secret}</code></dd>
          </dl>
          <CopyButton text={secret} label="Copy app password" />
        </Reveal>
      ) : (
        <form className="stack compact" onSubmit={create} noValidate>
          {error && <p className="error" role="alert">{error}</p>}
          <label className="field">
            <span>Connection name</span>
            <input value={label} maxLength={200} onChange={(e) => setLabel(e.target.value)} />
          </label>
          <label className="field">
            <span>Bookmarks</span>
            <select value={target} onChange={(e) => chooseTarget(e.target.value)}>
              <option value="">Your bookmarks</option>
              {shareList.map((s) => (
                <option key={s.id} value={String(s.id)}>{`${s.folderTitle} (shared, ${s.mode === 'edit' ? 'can edit' : 'view only'})`}</option>
              ))}
            </select>
          </label>
          <label className="field">
            <span>Access</span>
            <select value={mode} onChange={(e) => setMode(e.target.value)}>
              {!viewOnly && <option value="edit">Read and write</option>}
              <option value="view">Read only (download changes)</option>
            </select>
          </label>
          <button type="submit" className="btn primary" disabled={busy}>{busy ? 'Creating…' : 'Create connection'}</button>
        </form>
      )}
      {load.state === 'loading' && <Busy />}
      {load.state === 'error' && <p className="error" role="alert">Connections could not be loaded.</p>}
      {load.state === 'ready' && load.data.length === 0 && <p className="muted">No Floccus connections yet.</p>}
      {load.state === 'ready' && load.data.length > 0 && (
        <ul className="list">
          {load.data.map((c) => (
            <li key={c.id} className="list-item">
              <div>
                <p className="item-title">{c.label || 'Unnamed connection'}</p>
                <p className="muted small">
                  {c.kind === 'share' ? `Shared folder${shareTitle(c.shareId) ? ` “${shareTitle(c.shareId)}”` : ''}` : 'Your bookmarks'} · {c.mode === 'edit' ? 'read and write' : 'read only'} · last used {formatTime(c.lastUsedAt)}
                </p>
              </div>
              <button type="button" className="btn ghost small" aria-label={`Revoke ${c.label || 'Unnamed connection'}`} onClick={() => setRevoking(c)}>Revoke</button>
            </li>
          ))}
        </ul>
      )}
      {revoking && (
        <ConfirmDialog
          title="Revoke this connection?"
          message={`Floccus using “${revoking.label || 'Unnamed connection'}” will stop syncing immediately.`}
          confirm="Revoke connection"
          onConfirm={() => revoke(revoking)}
          onCancel={() => setRevoking(null)}
        />
      )}
    </Section>
  )
}
