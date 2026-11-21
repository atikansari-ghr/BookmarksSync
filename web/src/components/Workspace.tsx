import { useEffect, useState, type ReactNode } from 'react'
import { ApiError, type SessionState } from '../api'
import { useApp } from '../context'
import { Footer } from './common'
import { BookmarkLogo } from './icons'

/**
 * The signed-in workspace frame: session bootstrap (a signed-out visitor is sent to
 * sign-in before any data request is made), workspace navigation, and the footer.
 */
export function Workspace({ current, title, subtitle, children }: { current: 'dashboard' | 'bookmarks' | 'duplicates' | 'admin'; title: string; subtitle: string; children: ReactNode }) {
  const { client, navigate, session, setSession } = useApp()
  const [ready, setReady] = useState(session !== null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (session) return
    let live = true
    client
      .request<SessionState>('GET', '/api/v1/auth/session', undefined, { reauth: false })
      .then((s) => {
        if (!live) return
        setSession(s)
        setReady(true)
      })
      .catch((err) => {
        if (!live) return
        if (err instanceof ApiError && err.status === 401) {
          setSession(null)
          navigate('/auth/login', { replace: true })
        } else setFailed(true)
      })
    return () => {
      live = false
    }
    // Check the server session once, on a fresh page load.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function signOut() {
    try {
      await client.request('POST', '/api/v1/auth/logout', undefined, { reauth: false })
    } catch {
      // Already signed out: leave either way.
    }
    client.clear()
    setSession(null)
    navigate('/auth/login', { replace: true })
  }

  const go = (to: string) => (e: React.MouseEvent) => {
    if (e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return
    e.preventDefault()
    navigate(to)
  }

  if (!ready || !session) {
    return (
      <div className="workspace">
        <main className="ws-main">
          {failed ? (
            <p className="error" role="alert">Your session could not be checked. Reload the page to try again.</p>
          ) : (
            <p className="muted" aria-busy="true">Loading your bookmarks…</p>
          )}
        </main>
      </div>
    )
  }

  const link = (to: string, key: typeof current | null, label: string) => (
    <a href={to} onClick={go(to)} aria-current={key === current ? 'page' : undefined} className={key === current ? 'ws-link active' : 'ws-link'}>{label}</a>
  )

  return (
    <div className="workspace">
      <a className="skip-link" href="#ws-content">Skip to content</a>
      <aside className="ws-side glass">
        <p className="brand small">
          <BookmarkLogo size={26} />
          <span className="brand-name">BookmarksSync</span>
        </p>
        <nav aria-label="Workspace">
          {link('/dashboard', 'dashboard', 'Dashboard')}
          {link('/bookmarks', 'bookmarks', 'My Bookmarks')}
          {link('/duplicates', 'duplicates', 'Duplicates')}
          {link('/auth/security', null, 'Account security')}
          {session.user.isAdmin && link('/admin', 'admin', 'Administration')}
        </nav>
        <div className="ws-user">
          <p className="small muted">Signed in as <strong>{session.user.username}</strong></p>
          <button type="button" className="btn ghost small" onClick={signOut}>Sign out</button>
        </div>
      </aside>
      <div className="ws-body">
        <header className="ws-head">
          <h1>{title}</h1>
          <p className="muted">{subtitle}</p>
        </header>
        <main id="ws-content" className="ws-main" tabIndex={-1}>{children}</main>
        <Footer />
      </div>
    </div>
  )
}
