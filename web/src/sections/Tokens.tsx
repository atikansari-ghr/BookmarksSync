import { useState, type FormEvent } from 'react'
import { describe, isCancelled } from '../api'
import { useApp } from '../context'
import { ConfirmDialog } from '../components/Dialog'
import { Busy, CopyButton, Reveal, Section, formatTime } from '../components/common'
import type { Notify } from '../pages/SecurityPage'
import { useLoad } from './useLoad'

interface Token {
  id: number
  label: string
  scopes: string[]
  createdAt: string
  expiresAt?: string | null
  lastUsedAt?: string | null
  revoked?: boolean
  expired?: boolean
}

const SCOPES: { id: string; help: string }[] = [
  { id: 'bookmarks:read', help: 'Read bookmarks, folders, tags and exports' },
  { id: 'bookmarks:write', help: 'Create, change, import and delete bookmarks' },
  { id: 'shares:read', help: 'See folders shared with or by you' },
  { id: 'shares:manage', help: 'Share folders and change or end shares' },
]

export function TokensSection({ notify }: { notify: Notify }) {
  const { client } = useApp()
  const [load, reload] = useLoad(() => client.request<{ tokens: Token[] }>('GET', '/api/v1/me/tokens').then((d) => (d.tokens ?? []).filter((t) => !t.revoked)))
  const [label, setLabel] = useState('')
  const [scopes, setScopes] = useState<string[]>([])
  const [expires, setExpires] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [secret, setSecret] = useState('')
  const [revoking, setRevoking] = useState<Token | null>(null)

  async function create(e: FormEvent) {
    e.preventDefault()
    setError('')
    if (!label.trim()) return setError('Enter a token name.')
    if (scopes.length === 0) return setError('Choose at least one permission.')
    const body: Record<string, unknown> = { label: label.trim(), scopes: SCOPES.map((s) => s.id).filter((s) => scopes.includes(s)) }
    if (expires) body.expiresAt = new Date(`${expires}T23:59:59`).toISOString()
    setBusy(true)
    try {
      const d = await client.request<{ secret: string }>('POST', '/api/v1/me/tokens', body)
      setSecret(d.secret)
      setLabel('')
      setScopes([])
      setExpires('')
    } catch (err) {
      if (!isCancelled(err)) setError(describe(err, 'The token could not be created. Check the name and expiry date.'))
    } finally {
      setBusy(false)
    }
  }

  async function revoke(t: Token) {
    try {
      await client.request('DELETE', `/api/v1/me/tokens/${t.id}`)
      setRevoking(null)
      notify('Token revoked.')
      await reload()
    } catch (err) {
      setRevoking(null)
      setError(describe(err, 'The token could not be revoked.'))
    }
  }

  return (
    <Section title="Personal API tokens" intro="Tokens let scripts use the BookmarksSync API with only the permissions you choose. They never grant administrator access.">
      {secret ? (
        <Reveal title="Copy your new token now" onDone={() => { setSecret(''); void reload() }}>
          <p className="muted">It will not be shown again. Send it as <code>Authorization: Bearer …</code>.</p>
          <p className="secret"><code>{secret}</code></p>
          <CopyButton text={secret} />
        </Reveal>
      ) : (
        <form className="stack compact" onSubmit={create} noValidate>
          {error && <p className="error" role="alert">{error}</p>}
          <label className="field">
            <span>Token name</span>
            <input value={label} maxLength={200} onChange={(e) => setLabel(e.target.value)} />
          </label>
          <fieldset className="field">
            <legend>Permissions</legend>
            {SCOPES.map((s) => (
              <div key={s.id} className="check">
                <input
                  id={`scope-${s.id}`}
                  type="checkbox"
                  checked={scopes.includes(s.id)}
                  aria-describedby={`scope-help-${s.id}`}
                  onChange={(e) => setScopes(e.target.checked ? [...scopes, s.id] : scopes.filter((x) => x !== s.id))}
                />
                <label htmlFor={`scope-${s.id}`}>{s.id}</label>
                <small id={`scope-help-${s.id}`} className="muted">{s.help}</small>
              </div>
            ))}
          </fieldset>
          <label className="field">
            <span>Expires on (optional)</span>
            <input type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
          </label>
          <button type="submit" className="btn primary" disabled={busy}>{busy ? 'Creating…' : 'Create token'}</button>
        </form>
      )}
      {load.state === 'loading' && <Busy />}
      {load.state === 'error' && <p className="error" role="alert">Tokens could not be loaded.</p>}
      {load.state === 'ready' && load.data.length === 0 && <p className="muted">You have no API tokens.</p>}
      {load.state === 'ready' && load.data.length > 0 && (
        <ul className="list">
          {load.data.map((t) => (
            <li key={t.id} className="list-item">
              <div>
                <p className="item-title">{t.label || 'Unnamed token'}{t.expired && <span className="badge warn">Expired</span>}</p>
                <p className="muted small">{t.scopes.join(', ')} · created {formatTime(t.createdAt)} · last used {formatTime(t.lastUsedAt)}{t.expiresAt ? ` · expires ${formatTime(t.expiresAt)}` : ''}</p>
              </div>
              <button type="button" className="btn ghost small" aria-label={`Revoke ${t.label || 'Unnamed token'}`} onClick={() => setRevoking(t)}>Revoke</button>
            </li>
          ))}
        </ul>
      )}
      {revoking && (
        <ConfirmDialog
          title="Revoke this token?"
          message={`Scripts using “${revoking.label || 'Unnamed token'}” will stop working immediately.`}
          confirm="Revoke token"
          onConfirm={() => revoke(revoking)}
          onCancel={() => setRevoking(null)}
        />
      )}
    </Section>
  )
}
