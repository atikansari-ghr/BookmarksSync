import { useState, type FormEvent } from 'react'
import { describe, isCancelled } from '../api'
import { useApp } from '../context'
import { ConfirmDialog } from '../components/Dialog'
import { Busy, Section, formatTime } from '../components/common'
import type { Notify } from '../pages/SecurityPage'
import { attestationJSON, creationOptions, webauthnCancelled, webauthnSupported } from '../webauthn'
import { useLoad } from './useLoad'

interface Passkey {
  id: number
  name: string
  createdAt: string
  lastUsedAt?: string | null
  backupEligible?: boolean
  cloneWarning?: boolean
}

export function PasskeysSection({ notify }: { notify: Notify }) {
  const { client } = useApp()
  const [load, reload] = useLoad(() => client.request<{ passkeys: Passkey[] }>('GET', '/api/v1/auth/passkeys').then((d) => d.passkeys ?? []))
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [renaming, setRenaming] = useState<Passkey | null>(null)
  const [newName, setNewName] = useState('')
  const [removing, setRemoving] = useState<Passkey | null>(null)
  const supported = webauthnSupported()

  async function add(e: FormEvent) {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      const begin = await client.request<{ flowId: string; options: { publicKey: Record<string, unknown> } }>('POST', '/api/v1/auth/passkeys/register/begin')
      const cred = await navigator.credentials.create({ publicKey: creationOptions(begin.options.publicKey) })
      await client.request('POST', '/api/v1/auth/passkeys/register/finish', { flowId: begin.flowId, name: name.trim(), credential: attestationJSON(cred) })
      setName('')
      notify('Passkey added. Your other sessions were signed out.')
      await reload()
    } catch (err) {
      if (webauthnCancelled(err)) setError('Adding the passkey was cancelled.')
      else if (!isCancelled(err)) setError(describe(err, 'The passkey could not be added.'))
    } finally {
      setBusy(false)
    }
  }

  async function rename(e: FormEvent) {
    e.preventDefault()
    if (!renaming) return
    setError('')
    try {
      await client.request('PATCH', `/api/v1/auth/passkeys/${renaming.id}`, { name: newName.trim() })
      setRenaming(null)
      await reload()
    } catch (err) {
      setError(describe(err, 'Names must be 1–64 characters.'))
    }
  }

  async function remove(p: Passkey) {
    try {
      await client.request('DELETE', `/api/v1/auth/passkeys/${p.id}`)
      setRemoving(null)
      notify('Passkey removed. Your other sessions were signed out.')
      await reload()
    } catch (err) {
      setRemoving(null)
      if (!isCancelled(err)) setError(describe(err, 'The passkey could not be removed.'))
    }
  }

  return (
    <Section title="Passkeys" intro="Sign in with your fingerprint, face or device PIN instead of a password.">
      {error && <p className="error" role="alert">{error}</p>}
      {load.state === 'loading' && <Busy />}
      {load.state === 'unavailable' && <p className="muted">Passkeys are not available on this server.</p>}
      {load.state === 'error' && <p className="error" role="alert">Passkeys could not be loaded.</p>}
      {load.state === 'ready' && (
        <>
          {load.data.length === 0 ? (
            <p className="muted">You have no passkeys yet.</p>
          ) : (
            <ul className="list">
              {load.data.map((p) => (
                <li key={p.id} className="list-item">
                  {renaming?.id === p.id ? (
                    <form className="inline-form" onSubmit={rename}>
                      <label className="field">
                        <span className="visually-hidden">New name for {p.name}</span>
                        <input autoFocus value={newName} maxLength={64} onChange={(e) => setNewName(e.target.value)} />
                      </label>
                      <button type="submit" className="btn primary small">Save</button>
                      <button type="button" className="btn ghost small" onClick={() => setRenaming(null)}>Cancel</button>
                    </form>
                  ) : (
                    <>
                      <div>
                        <p className="item-title">{p.name}{p.backupEligible && <span className="badge">Synced</span>}</p>
                        <p className="muted small">Added {formatTime(p.createdAt)} · last used {formatTime(p.lastUsedAt)}</p>
                      </div>
                      <div className="actions">
                        <button type="button" className="btn ghost small" aria-label={`Rename ${p.name}`} onClick={() => { setRenaming(p); setNewName(p.name) }}>Rename</button>
                        <button type="button" className="btn ghost small" aria-label={`Remove ${p.name}`} onClick={() => setRemoving(p)}>Remove</button>
                      </div>
                    </>
                  )}
                </li>
              ))}
            </ul>
          )}
          {supported ? (
            <form className="inline-form" onSubmit={add}>
              <label className="field grow">
                <span>Passkey name</span>
                <input value={name} maxLength={64} placeholder="For example: Work laptop" onChange={(e) => setName(e.target.value)} />
              </label>
              <button type="submit" className="btn primary" disabled={busy}>{busy ? 'Waiting for your device…' : 'Add a passkey'}</button>
            </form>
          ) : (
            <p className="muted">This browser cannot create passkeys.</p>
          )}
        </>
      )}
      {removing && (
        <ConfirmDialog
          title="Remove this passkey?"
          message={`You will no longer be able to sign in with “${removing.name}”. Your other sessions will be signed out.`}
          confirm="Remove passkey"
          onConfirm={() => remove(removing)}
          onCancel={() => setRemoving(null)}
        />
      )}
    </Section>
  )
}
