import { useCallback, useEffect, useState } from 'react'
import { ApiError, type Provider, type SessionState } from '../api'
import { useApp } from '../context'
import { Footer } from '../components/common'
import { BookmarkLogo } from '../components/icons'
import { ConnectionsSection } from '../sections/Connections'
import { LinkedAccountsSection } from '../sections/LinkedAccounts'
import { PasskeysSection } from '../sections/Passkeys'
import { SessionsSection } from '../sections/Sessions'
import { TokensSection } from '../sections/Tokens'
import { TotpSection } from '../sections/Totp'
import { NotificationsSection } from '../sections/Notifications'

export interface Notify {
  (message: string): void
}

/** Account security: passkeys, authenticator app, linked providers, sessions, tokens and Floccus connections. */
export function SecurityPage({ initialNotice }: { initialNotice?: string }) {
  const { client, navigate, session, setSession } = useApp()
  const [notice, setNotice] = useState(initialNotice ?? '')
  const [providers, setProviders] = useState<Provider[] | null>(null)
  const [ready, setReady] = useState(session !== null)

  useEffect(() => {
    let live = true
    if (!session) client
      .request<SessionState>('GET', '/api/v1/auth/session', undefined, { reauth: false })
      .then((s) => {
        if (!live) return
        client.setCSRF(s.csrfToken)
        setSession(s)
        setReady(true)
      })
      .catch((err) => {
        if (!live) return
        if (err instanceof ApiError && err.status === 401) {
          setSession(null)
          navigate('/auth/login', { replace: true })
        } else {
          setReady(true)
        }
      })
    client
      .request<{ providers: Provider[] }>('GET', '/api/v1/auth/oidc/providers')
      .then((d) => live && setProviders(d.providers ?? []))
      .catch(() => live && setProviders([]))
    return () => {
      live = false
    }
    // Re-check the server session only when none is known (fresh page load).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, navigate, setSession])

  const notify = useCallback<Notify>((m) => setNotice(m), [])

  async function signOut() {
    try {
      await client.request('POST', '/api/v1/auth/logout', undefined, { reauth: false })
    } catch {
      // Signed out or the session is already gone: leave either way.
    }
    client.clear()
    setSession(null)
    navigate('/auth/login', { replace: true })
  }

  if (!ready || !session) {
    return (
      <div className="app-shell">
        <main className="content">
          <p className="muted" aria-busy="true">Loading your account…</p>
        </main>
      </div>
    )
  }

  return (
    <div className="app-shell">
      <header className="topbar">
        <p className="brand small">
          <BookmarkLogo size={26} />
          <span className="brand-name">BookmarksSync</span>
        </p>
        <div className="whoami-bar">
          <p className="whoami">Signed in as <strong>{session.user.username}</strong></p>
          <button type="button" className="btn ghost" onClick={() => navigate('/dashboard')}>Dashboard</button>
          <button type="button" className="btn ghost" onClick={signOut}>Sign out</button>
        </div>
      </header>
      <main className="content">
        <h1>Account security</h1>
        <p className="muted">Manage how you sign in, where you are signed in, and how your apps and browsers connect.</p>
        {notice && (
          <p className="notice" role="status">
            {notice}
            <button type="button" className="link-button" onClick={() => setNotice('')} aria-label="Dismiss message">Dismiss</button>
          </p>
        )}
        <PasskeysSection notify={notify} />
        <TotpSection notify={notify} />
        {providers && providers.length > 0 && <LinkedAccountsSection providers={providers} notify={notify} />}
        <SessionsSection notify={notify} />
        <TokensSection notify={notify} />
        <ConnectionsSection notify={notify} username={session.user.username} />
        <NotificationsSection notify={notify} />
      </main>
      <Footer />
    </div>
  )
}
