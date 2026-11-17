import { useState } from 'react'
import { describe, isCancelled, type Provider } from '../api'
import { useApp } from '../context'
import { ConfirmDialog } from '../components/Dialog'
import { Busy, Section, formatTime } from '../components/common'
import { ProviderBadge } from '../components/icons'
import type { Notify } from '../pages/SecurityPage'
import { useLoad } from './useLoad'

interface Identity {
  id: number
  provider: string
  displayName: string
  createdAt: string
  lastLoginAt?: string | null
}

export function LinkedAccountsSection({ providers, notify }: { providers: Provider[]; notify: Notify }) {
  const { client, leave } = useApp()
  const [load, reload] = useLoad(() => client.request<{ identities: Identity[] }>('GET', '/api/v1/auth/oidc/identities').then((d) => d.identities ?? []))
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')
  const [unlinking, setUnlinking] = useState<Identity | null>(null)
  const nameOf = (id: string) => providers.find((p) => p.id === id)?.name ?? id

  async function link(p: Provider) {
    setError('')
    setBusy(p.id)
    try {
      const d = await client.request<{ authorizationUrl: string }>('POST', `/api/v1/auth/oidc/${encodeURIComponent(p.id)}/link`)
      leave(d.authorizationUrl)
    } catch (err) {
      setBusy('')
      if (!isCancelled(err)) setError(describe(err, `${p.name} is unavailable right now. Try again later.`))
    }
  }

  async function unlink(i: Identity) {
    try {
      await client.request('DELETE', `/api/v1/auth/oidc/identities/${i.id}`)
      setUnlinking(null)
      notify('Account unlinked. Your other sessions were signed out.')
      await reload()
    } catch (err) {
      setUnlinking(null)
      if (!isCancelled(err)) setError(describe(err, 'The account could not be unlinked.'))
    }
  }

  const linked = load.state === 'ready' ? load.data : []
  const unlinked = providers.filter((p) => !linked.some((i) => i.provider === p.id))

  return (
    <Section title="Linked accounts" intro="Sign in with an account you already use. Only your administrator can enable providers; accounts are never matched by email.">
      {error && <p className="error" role="alert">{error}</p>}
      {load.state === 'loading' && <Busy />}
      {load.state === 'error' && <p className="error" role="alert">Linked accounts could not be loaded.</p>}
      {load.state === 'ready' && (
        <>
          {linked.length === 0 && <p className="muted">No linked accounts.</p>}
          {linked.length > 0 && (
            <ul className="list">
              {linked.map((i) => (
                <li key={i.id} className="list-item">
                  <div className="with-badge">
                    <ProviderBadge name={nameOf(i.provider)} />
                    <div>
                      <p className="item-title">{i.displayName || nameOf(i.provider)}</p>
                      <p className="muted small">{nameOf(i.provider)} · linked {formatTime(i.createdAt)} · last sign-in {formatTime(i.lastLoginAt)}</p>
                    </div>
                  </div>
                  <button type="button" className="btn ghost small" aria-label={`Unlink ${nameOf(i.provider)}`} onClick={() => setUnlinking(i)}>Unlink</button>
                </li>
              ))}
            </ul>
          )}
          {unlinked.length > 0 && (
            <div className="actions start">
              {unlinked.map((p) => (
                <button key={p.id} type="button" className="btn outline" disabled={busy !== ''} onClick={() => link(p)}>
                  <ProviderBadge name={p.name} />
                  <span>{`Link ${p.name}`}</span>
                </button>
              ))}
            </div>
          )}
        </>
      )}
      {unlinking && (
        <ConfirmDialog
          title="Unlink this account?"
          message={`You will no longer be able to sign in with ${nameOf(unlinking.provider)}. Your other sessions will be signed out.`}
          confirm="Unlink account"
          onConfirm={() => unlink(unlinking)}
          onCancel={() => setUnlinking(null)}
        />
      )}
    </Section>
  )
}
