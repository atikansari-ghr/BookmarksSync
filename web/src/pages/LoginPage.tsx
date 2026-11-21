import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { ApiError, describe, tooMany, type Provider, type SessionState } from '../api'
import { useApp } from '../context'
import { Footer } from '../components/common'
import { ArrowIcon, BookmarkLogo, EyeIcon, HeroArt, PasskeyIcon, ProviderBadge } from '../components/icons'
import { assertionJSON, requestOptions, webauthnCancelled, webauthnSupported } from '../webauthn'
import { SecondFactorForm } from './SecondFactor'

/** The split-screen layout shared by the sign-in pages. */
export function AuthLayout({ children }: { children: ReactNode }) {
  const { presentation } = useApp()
  return (
    <div className={`auth-layout auth-layout-${presentation.loginLayout}`}>
      <aside className="hero" aria-label={presentation.appTitle}>
        <p className="brand">
          {presentation.logoUrl ? <img className="login-logo" src={presentation.logoUrl} alt="" /> : <BookmarkLogo size={34} />}
          <span className="brand-name">{presentation.appTitle}</span>
        </p>
        <p className="tagline">{presentation.tagline}</p>
        <p className="supporting">{presentation.supportingLine}</p>
        {presentation.backgroundUrl ? <div className="login-background-image" aria-hidden="true" style={{ backgroundImage: `linear-gradient(135deg, color-mix(in srgb, var(--login-background) 35%, transparent), color-mix(in srgb, var(--login-primary) 48%, transparent)), url("${presentation.backgroundUrl}")` }} /> : <HeroArt />}
      </aside>
      <main className="auth-main">
        <div className="card glass">{children}</div>
        <Footer />
      </main>
    </div>
  )
}

export function LoginPage() {
  const { client, navigate, setSession, leave, presentation } = useApp()
  const [providers, setProviders] = useState<Provider[]>([])
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)
  const [busy, setBusy] = useState<'' | 'password' | 'passkey' | string>('')
  const [error, setError] = useState('')
  const [flowId, setFlowId] = useState('')
  const passwordRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    let live = true
    client
      .request<SessionState>('GET', '/api/v1/auth/session', undefined, { reauth: false })
      .then((s) => {
        if (!live) return
        setSession(s)
        navigate('/auth/security', { replace: true })
      })
      .catch(() => {})
    client
      .request<{ providers: Provider[] }>('GET', '/api/v1/auth/oidc/providers')
      .then((d) => live && setProviders(d.providers ?? []))
      .catch(() => live && setProviders([]))
    return () => {
      live = false
    }
  }, [client, navigate, setSession])

  async function signIn(e: FormEvent) {
    e.preventDefault()
    setError('')
    if (!username.trim() || !password) {
      setError('Enter your username and password.')
      return
    }
    setBusy('password')
    try {
      const res = await fetch('/api/v1/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ username, password }),
        credentials: 'same-origin',
        cache: 'no-store',
      })
      const data = await res.json().catch(() => ({}))
      if (res.status === 202 && data.flowId) {
        setPassword('')
        setFlowId(data.flowId)
        return
      }
      if (res.ok) {
        setPassword('')
        client.setCSRF(data.csrfToken)
        setSession(data as SessionState)
        navigate('/auth/security', { replace: true })
        return
      }
      const ra = Number(res.headers.get('Retry-After'))
      throw new ApiError(res.status, data?.error?.code ?? 'error', data?.error?.message ?? '', Number.isFinite(ra) && ra > 0 ? ra : undefined)
    } catch (err) {
      setPassword('')
      if (err instanceof ApiError && err.status === 401) setError('Incorrect username or password.')
      else if (err instanceof ApiError && err.status === 429) setError(tooMany(err.retryAfter))
      else setError(describe(err))
      passwordRef.current?.focus()
    } finally {
      setBusy('')
    }
  }

  async function passkey() {
    setError('')
    setBusy('passkey')
    try {
      const begin = await client.request<{ flowId: string; options: { publicKey: Record<string, unknown> } }>('POST', '/api/v1/auth/passkey/login/begin', undefined, { reauth: false })
      const cred = await navigator.credentials.get({ publicKey: requestOptions(begin.options.publicKey) })
      const state = await client.request<SessionState>('POST', '/api/v1/auth/passkey/login/finish', { flowId: begin.flowId, credential: assertionJSON(cred) }, { reauth: false })
      setSession(state)
      navigate('/auth/security', { replace: true })
    } catch (err) {
      if (webauthnCancelled(err)) setError('Passkey sign-in was cancelled.')
      else if (err instanceof ApiError && err.status === 401) setError('That passkey could not sign you in.')
      else setError(describe(err))
    } finally {
      setBusy('')
    }
  }

  async function provider(p: Provider) {
    setError('')
    setBusy(p.id)
    try {
      const d = await client.request<{ authorizationUrl: string }>('POST', `/api/v1/auth/oidc/${encodeURIComponent(p.id)}/login`, undefined, { reauth: false })
      leave(d.authorizationUrl)
    } catch (err) {
      setError(err instanceof ApiError && err.code === 'provider_unavailable' ? `${p.name} is unavailable right now. Try again later.` : describe(err))
      setBusy('')
    }
  }

  if (flowId) {
    return (
      <AuthLayout>
        <SecondFactorForm flowId={flowId} onBack={() => setFlowId('')} />
      </AuthLayout>
    )
  }

  return (
    <AuthLayout>
      <h1>{presentation.signinTitle}</h1>
      <p className="muted center">Sign in to your account</p>
      {error && <p className="error" role="alert">{error}</p>}
      {providers.length > 0 && (
        <div className="providers">
          {providers.map((p) => (
            <button key={p.id} type="button" className="btn outline" aria-label={`Sign in with ${p.name}`} disabled={busy !== ''} onClick={() => provider(p)}>
              <ProviderBadge name={p.name} />
              <span>{p.name}</span>
            </button>
          ))}
        </div>
      )}
      {webauthnSupported() && (
        <button type="button" className="btn outline teal block" disabled={busy !== ''} onClick={passkey}>
          <PasskeyIcon />
          <span>{busy === 'passkey' ? 'Waiting for your passkey…' : 'Sign in with a passkey'}</span>
        </button>
      )}
      <p className="divider"><span>or use your password</span></p>
      <form className="stack" onSubmit={signIn} noValidate>
        <label className="field">
          <span>Username</span>
          <input name="username" autoComplete="username" autoCapitalize="none" spellCheck={false} value={username} onChange={(e) => setUsername(e.target.value)} required />
        </label>
        <div className="field">
          <label htmlFor="password">Password</label>
          <div className="input-with-button">
            <input
              id="password"
              ref={passwordRef}
              name="password"
              type={showPassword ? 'text' : 'password'}
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
            />
            <button type="button" className="icon-button" aria-label={showPassword ? 'Hide password' : 'Show password'} aria-pressed={showPassword} onClick={() => setShowPassword(!showPassword)}>
              <EyeIcon open={!showPassword} />
            </button>
          </div>
        </div>
        <button type="submit" className="btn primary block" disabled={busy !== ''}>
          <span>{busy === 'password' ? 'Signing in…' : 'Sign in'}</span>
          {busy !== 'password' && <ArrowIcon />}
        </button>
      </form>
      <p className="muted center small">Accounts are created by your administrator.</p>
      <p className="muted center small">Forgot your password? Ask your administrator to reset it.</p>
    </AuthLayout>
  )
}
