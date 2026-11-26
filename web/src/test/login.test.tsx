import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import { err, go, mockServer, sessionState } from './server'

const base = () => ({
  'GET /api/v1/version': { status: 200, body: { version: '0.4.2', commit: 'abc', build_date: '' } },
  'GET /api/v1/auth/session': err(401, 'unauthenticated'),
  'GET /api/v1/auth/oidc/providers': { status: 200, body: { providers: [{ id: 'google', name: 'Google' }] } },
})

describe('login page', () => {
  beforeEach(() => go('/auth/login'))

  it('shows the approved identity, version and administrator guidance only', async () => {
    mockServer(base())
    render(<App />)
    expect(await screen.findByRole('heading', { name: 'Personal Bookmarks Server' })).toBeInTheDocument()
    expect(screen.getByText('Your bookmarks. Your browsers. Your control.')).toBeInTheDocument()
    expect(screen.getByText('A self-hosted home for your bookmarks.')).toBeInTheDocument()
    expect(screen.getByText('BookmarksSync', { selector: '.brand-name' })).toBeInTheDocument()
    expect(await screen.findByText(/v0\.4\.2/)).toBeInTheDocument()
    expect(screen.getByText(/© 2026 Atik Ansari/)).toBeInTheDocument()
    // Username (not email), no self-service reset, no "remember me", no theme picker.
    expect(screen.getByLabelText('Username')).toHaveAttribute('autocomplete', 'username')
    expect(screen.getByLabelText('Password')).toHaveAttribute('type', 'password')
    expect(screen.queryByText(/email/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /forgot/i })).not.toBeInTheDocument()
    expect(screen.queryByLabelText(/remember/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /theme|dark|light/i })).not.toBeInTheDocument()
    expect(screen.getByText(/ask your administrator to reset it/i)).toBeInTheDocument()
    expect(screen.getByText('Accounts are created by your administrator.')).toBeInTheDocument()
  })

  it('shows only the configured sign-in providers', async () => {
    mockServer(base())
    render(<App />)
    expect(await screen.findByRole('button', { name: 'Sign in with Google' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Sign in with Authentik' })).not.toBeInTheDocument()
  })

  it('shows no provider buttons when OIDC is not configured', async () => {
    mockServer({ ...base(), 'GET /api/v1/auth/oidc/providers': err(404, 'not_found') })
    render(<App />)
    await screen.findByRole('heading', { name: 'Personal Bookmarks Server' })
    await waitFor(() => expect(screen.queryByRole('button', { name: /Sign in with (Google|Authentik)/ })).not.toBeInTheDocument())
  })

  it('marks the decorative login background as hidden from assistive technology', async () => {
    mockServer({
      ...base(),
      'GET /api/v1/auth/presentation': { status: 200, body: { backgroundUrl: '/background.webp' } },
    })
    render(<App />)
    await screen.findByRole('heading', { name: 'Personal Bookmarks Server' })
    const background = document.querySelector('.login-background-image')
    expect(background).toHaveAttribute('aria-hidden', 'true')
    expect(background).not.toHaveAttribute('role')
  })

  it('toggles password visibility from the keyboard', async () => {
    mockServer(base())
    render(<App />)
    const user = userEvent.setup()
    const pw = await screen.findByLabelText('Password')
    await user.click(screen.getByRole('button', { name: 'Show password' }))
    expect(pw).toHaveAttribute('type', 'text')
    await user.click(screen.getByRole('button', { name: 'Hide password' }))
    expect(pw).toHaveAttribute('type', 'password')
  })

  it('signs in with username and password and opens the security page', async () => {
    const srv = mockServer({
      ...base(),
      'POST /api/v1/auth/login': { status: 200, body: sessionState() },
      'GET /api/v1/auth/sessions': { status: 200, body: { sessions: [] } },
    })
    render(<App />)
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Username'), 'alice')
    await user.type(screen.getByLabelText('Password'), 'correct horse battery')
    await user.click(screen.getByRole('button', { name: /^Sign in$/ }))
    await waitFor(() => expect(window.location.pathname).toBe('/auth/security'))
    expect(srv.find('POST', '/api/v1/auth/login')[0].body).toEqual({ username: 'alice', password: 'correct horse battery' })
    expect(await screen.findByText(/Signed in as/)).toHaveTextContent('alice')
  })

  it('shows a generic error for bad credentials and a wait message when rate limited', async () => {
    const srv = mockServer({ ...base(), 'POST /api/v1/auth/login': err(401, 'invalid_credentials', 'invalid username or password') })
    render(<App />)
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Username'), 'alice')
    await user.type(screen.getByLabelText('Password'), 'nope')
    await user.click(screen.getByRole('button', { name: /^Sign in$/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Incorrect username or password.')
    expect(screen.getByLabelText('Password')).toHaveValue('')
    srv.set('POST /api/v1/auth/login', { ...err(429, 'rate_limited'), headers: { 'Retry-After': '30' } })
    await user.type(screen.getByLabelText('Password'), 'again')
    await user.click(screen.getByRole('button', { name: /^Sign in$/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/Too many attempts/)
  })

  it('asks for the second factor when the account has TOTP, with a recovery-code option', async () => {
    const srv = mockServer({
      ...base(),
      'POST /api/v1/auth/login': { status: 202, body: { status: 'second_factor_required', flowId: 'flow-secret', methods: ['totp', 'recoveryCode'] } },
      'POST /api/v1/auth/login/second-factor': (c) =>
        (c.body as { code?: string }).code === '123456' ? { status: 200, body: sessionState() } : err(401, 'invalid_second_factor'),
      'GET /api/v1/auth/sessions': { status: 200, body: { sessions: [] } },
    })
    render(<App />)
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Username'), 'alice')
    await user.type(screen.getByLabelText('Password'), 'pw')
    await user.click(screen.getByRole('button', { name: /^Sign in$/ }))
    expect(await screen.findByRole('heading', { name: 'Two-step verification' })).toBeInTheDocument()
    const code = screen.getByLabelText('Authentication code')
    expect(code).toHaveAttribute('autocomplete', 'one-time-code')
    expect(code).toHaveAttribute('inputmode', 'numeric')
    expect(code).toHaveFocus()
    await user.type(code, '000000')
    await user.click(screen.getByRole('button', { name: 'Verify' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('That code did not work.')
    await user.click(screen.getByRole('button', { name: 'Use a recovery code instead' }))
    expect(screen.getByLabelText('Recovery code')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Use your authenticator app instead' }))
    await user.clear(screen.getByLabelText('Authentication code'))
    await user.type(screen.getByLabelText('Authentication code'), '123456')
    await user.click(screen.getByRole('button', { name: 'Verify' }))
    await waitFor(() => expect(window.location.pathname).toBe('/auth/security'))
    const calls = srv.find('POST', '/api/v1/auth/login/second-factor')
    expect(calls.at(-1)?.body).toEqual({ flowId: 'flow-secret', code: '123456' })
  })

  it('starts a provider sign-in by leaving for the provider', async () => {
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, assign, pathname: '/auth/login', search: '', hash: '', origin: 'http://bookmarks.test' })
    const srv = mockServer({ ...base(), 'POST /api/v1/auth/oidc/google/login': { status: 200, body: { authorizationUrl: 'https://accounts.example/auth?state=s' } } })
    render(<App />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Sign in with Google' }))
    await waitFor(() => expect(assign).toHaveBeenCalledWith('https://accounts.example/auth?state=s'))
    expect(srv.find('POST', '/api/v1/auth/oidc/google/login')).toHaveLength(1)
  })

  it('signs in with a passkey through the WebAuthn API', async () => {
    const get = vi.fn(async () => ({
      id: 'cred', rawId: new Uint8Array([1, 2]).buffer, type: 'public-key', authenticatorAttachment: 'platform',
      response: {
        authenticatorData: new Uint8Array([3]).buffer, clientDataJSON: new Uint8Array([4]).buffer,
        signature: new Uint8Array([5]).buffer, userHandle: new Uint8Array([6]).buffer,
      },
      getClientExtensionResults: () => ({}),
    }))
    vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
    Object.defineProperty(navigator, 'credentials', { value: { get, create: vi.fn() }, configurable: true })
    const srv = mockServer({
      ...base(),
      'POST /api/v1/auth/passkey/login/begin': { status: 200, body: { flowId: 'pk-flow', options: { publicKey: { challenge: 'AQID', rpId: 'bookmarks.test', userVerification: 'required' } } } },
      'POST /api/v1/auth/passkey/login/finish': { status: 200, body: sessionState() },
      'GET /api/v1/auth/sessions': { status: 200, body: { sessions: [] } },
    })
    render(<App />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Sign in with a passkey' }))
    await waitFor(() => expect(window.location.pathname).toBe('/auth/security'))
    const opts = (get.mock.calls[0] as unknown as [{ publicKey: { challenge: ArrayBuffer } }])[0]
    expect(new Uint8Array(opts.publicKey.challenge)).toEqual(new Uint8Array([1, 2, 3]))
    const finish = srv.find('POST', '/api/v1/auth/passkey/login/finish')[0].body as { flowId: string; credential: { rawId: string; response: { userHandle: string } } }
    expect(finish.flowId).toBe('pk-flow')
    expect(finish.credential.rawId).toBe('AQI')
    expect(finish.credential.response.userHandle).toBe('Bg')
  })

  it('hides passkey sign-in when the browser has no WebAuthn', async () => {
    vi.stubGlobal('PublicKeyCredential', undefined)
    mockServer(base())
    render(<App />)
    await screen.findByRole('heading', { name: 'Personal Bookmarks Server' })
    expect(screen.queryByRole('button', { name: 'Sign in with a passkey' })).not.toBeInTheDocument()
  })

  it('goes straight to the security page when already signed in', async () => {
    mockServer({ ...base(), 'GET /api/v1/auth/session': { status: 200, body: sessionState() }, 'GET /api/v1/auth/sessions': { status: 200, body: { sessions: [] } } })
    render(<App />)
    await waitFor(() => expect(window.location.pathname).toBe('/auth/security'))
    const main = await screen.findByRole('main')
    expect(within(main).getByRole('heading', { level: 1 })).toHaveTextContent('Account security')
  })
})
