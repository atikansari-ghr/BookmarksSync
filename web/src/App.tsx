import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { ApiError, Client, tooMany, type SessionState } from './api'
import { AppContext, useApp as useAppFromProvider, type AppContextValue } from './context'
import { Dialog } from './components/Dialog'
import { LoginPage } from './pages/LoginPage'
import { OidcComplete } from './pages/OidcComplete'
import { SecurityPage } from './pages/SecurityPage'

function currentPath() {
  return window.location.pathname.replace(/\/+$/, '') || '/'
}

/** The sign-in and account-security app served under /auth/. */
export function App() {
  const client = useMemo(() => new Client(), [])
  const [path, setPath] = useState(currentPath)
  const [notice, setNotice] = useState('')
  const [session, setSession] = useState<SessionState | null>(null)
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
      setSession(s)
    },
    [client],
  )

  const ctx: AppContextValue = useMemo(
    () => ({ client, navigate, session, setSession: updateSession, leave: (url: string) => window.location.assign(url) }),
    [client, navigate, session, updateSession],
  )

  let page
  switch (path) {
    case '/auth/security':
      page = <SecurityPage key={notice} initialNotice={notice} />
      break
    case '/auth/oidc/complete':
      page = <OidcComplete />
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

