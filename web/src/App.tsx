import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { ApiError, Client, tooMany, type PublicPresentation, type SessionState } from './api'
import { AppContext, useApp as useAppFromProvider, type AppContextValue } from './context'
import { Dialog } from './components/Dialog'
import { DuplicatesPage } from './pages/DuplicatesPage'
import { DashboardPage } from './pages/DashboardPage'
import { AdminPage } from './pages/AdminPage'
import { LoginPage } from './pages/LoginPage'
import { OidcComplete } from './pages/OidcComplete'
import { OrganizerPage } from './pages/OrganizerPage'
import { SecurityPage } from './pages/SecurityPage'

function currentPath() {
  return window.location.pathname.replace(/\/+$/, '') || '/'
}

const defaultPresentation: PublicPresentation = {
  appTitle: 'BookmarksSync', signinTitle: 'Personal Bookmarks Server',
  tagline: 'Your bookmarks. Your browsers. Your control.',
  supportingLine: 'A self-hosted home for your bookmarks.', loginLayout: 'split',
  primaryColor: '#0f766e', accentColor: '#14b8a6', backgroundColor: '#e6f4fb', version: '', copyright: '© 2026 Atik Ansari',
}

/** The sign-in and account-security app (/auth/) and the bookmark workspace (/bookmarks, /duplicates). */
export function App() {
  const client = useMemo(() => new Client(), [])
  const [path, setPath] = useState(currentPath)
  const [notice, setNotice] = useState('')
  const [session, setSession] = useState<SessionState | null>(null)
  const [theme, setTheme] = useState<AppContextValue['theme']>({ name: 'ocean-glass', scheme: 'light', reducedTransparency: false })
  const [presentation, setPresentation] = useState<PublicPresentation>(defaultPresentation)
  const themeTouched = useRef(false)
  const themeOwner = useRef<number | null>(null)
  const [reauth, setReauth] = useState<((ok: boolean) => void) | null>(null)

  useEffect(() => {
    const onPop = () => setPath(currentPath())
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  // Expose the password prompt to the API client: requests that need a recent sign-in
  // wait for the user, then retry once.
  useEffect(() => {
    client.onReauth = () => new Promise<boolean>((resolve) => setReauth(() => resolve))
    return () => {
      client.onReauth = null
    }
  }, [client])

  const navigate = useCallback<AppContextValue['navigate']>((to, opts) => {
    if (opts?.replace) window.history.replaceState(null, '', to)
    else window.history.pushState(null, '', to)
    setNotice(opts?.notice ?? '')
    setPath(to)
  }, [])

  const updateSession = useCallback(
    (s: SessionState | null) => {
      if (s) client.setCSRF(s.csrfToken)
      if (!s) {
        themeTouched.current = false
        themeOwner.current = null
        setTheme({ name: 'ocean-glass', scheme: 'light', reducedTransparency: false })
      }
      setSession(s)
    },
    [client],
  )

  useEffect(() => {
    let live = true
    void client.request<PublicPresentation>('GET', '/api/v1/auth/presentation', undefined, { reauth: false }).then((p) => { if (live) setPresentation((old) => ({ ...old, ...p })) }).catch(() => undefined)
    return () => { live = false }
  }, [client])

  useEffect(() => {
    document.documentElement.style.setProperty('--login-primary', presentation.primaryColor)
    document.documentElement.style.setProperty('--login-accent', presentation.accentColor)
    document.documentElement.style.setProperty('--login-background', presentation.backgroundColor)
  }, [presentation])

  useEffect(() => {
    if (!session) return
    if (themeOwner.current !== session.user.id) {
      themeOwner.current = session.user.id
      themeTouched.current = false
    }
    let live = true
    void client.request<{ settings: Record<string, string> }>('GET', '/api/v1/me/settings', undefined, { reauth: false }).then(({ settings }) => {
      if (!live) return
      if (themeTouched.current) return
      setTheme({
        name: settings['ui.theme'] || 'ocean-glass',
        scheme: settings['ui.color-scheme'] === 'dark' ? 'dark' : 'light',
        reducedTransparency: settings['ui.reduced-transparency'] === 'true',
      })
    }).catch(() => undefined)
    return () => { live = false }
  }, [client, session?.user.id])

  const updateTheme = useCallback<AppContextValue['setTheme']>((v) => {
    themeTouched.current = true
    setTheme(v)
  }, [])

  useEffect(() => {
    document.documentElement.dataset.theme = theme.name
    document.documentElement.dataset.scheme = theme.scheme
    document.documentElement.dataset.reducedTransparency = String(theme.reducedTransparency)
  }, [theme])

  const ctx: AppContextValue = useMemo(
    () => ({ client, navigate, session, setSession: updateSession, leave: (url: string) => window.location.assign(url), theme, setTheme: updateTheme, presentation, setPresentation }),
    [client, navigate, session, updateSession, theme, updateTheme, presentation],
  )

  let page
  switch (path) {
    case '/dashboard':
      page = <DashboardPage />
      break
    case '/admin':
      page = <AdminPage />
      break
    case '/auth/security':
      page = <SecurityPage key={notice} initialNotice={notice} />
      break
    case '/auth/oidc/complete':
      page = <OidcComplete />
      break
    case '/bookmarks':
      page = <OrganizerPage />
      break
    case '/duplicates':
      page = <DuplicatesPage />
      break
    default:
      page = <LoginPage />
  }

  return (
    <AppContext.Provider value={ctx}>
      {page}
      {reauth && (
        <ReauthDialog
          onDone={(ok) => {
            reauth(ok)
            setReauth(null)
          }}
        />
      )}
    </AppContext.Provider>
  )
}

function ReauthDialog({ onDone }: { onDone: (ok: boolean) => void }) {
  const { client, session, setSession } = useAppFromProvider()
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const input = useRef<HTMLInputElement>(null)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      const s = await client.request<SessionState>('POST', '/api/v1/auth/reauthenticate', { password }, { reauth: false })
      setPassword('')
      setSession(s)
      onDone(true)
    } catch (err) {
      setPassword('')
      if (err instanceof ApiError && err.status === 401) setError('Incorrect password.')
      else if (err instanceof ApiError && err.status === 429) setError(tooMany(err.retryAfter))
      else setError('Your password could not be checked. Try again.')
      input.current?.focus()
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title="Confirm your password" onClose={() => onDone(false)} initialFocus={input}>
      <form className="stack compact" onSubmit={submit} noValidate>
        <p className="muted">For your security, confirm it is you before changing how you sign in{session ? `, ${session.user.username}` : ''}.</p>
        {error && <p className="error" role="alert">{error}</p>}
        <input type="text" name="username" autoComplete="username" value={session?.user.username ?? ''} readOnly hidden />
        <label className="field">
          <span>Password</span>
          <input ref={input} type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
        <div className="actions">
          <button type="button" className="btn ghost" onClick={() => onDone(false)}>Cancel</button>
          <button type="submit" className="btn primary" disabled={busy || !password}>Confirm</button>
        </div>
      </form>
    </Dialog>
  )
}

