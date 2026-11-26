import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import { err, go, mockServer, sessionState, type Route } from './server'

const now = '2026-11-17T09:00:00Z'

function base(extra: Record<string, Route> = {}): Record<string, Route> {
  return {
    'GET /api/v1/version': { status: 200, body: { version: '1.0.0' } },
    'GET /api/v1/auth/session': { status: 200, body: sessionState() },
    'GET /api/v1/auth/sessions': {
      status: 200,
      body: { sessions: [
        { id: 7, label: 'Firefox on Linux', createdAt: now, lastSeenAt: now, current: true },
        { id: 8, label: 'Chrome on Windows', createdAt: now, lastSeenAt: now, current: false },
      ] },
    },
    'GET /api/v1/auth/passkeys': { status: 200, body: { passkeys: [] } },
    'GET /api/v1/auth/totp': { status: 200, body: { enabled: false, recoveryCodesRemaining: 0 } },
    'GET /api/v1/auth/oidc/providers': { status: 200, body: { providers: [{ id: 'google', name: 'Google' }, { id: 'authentik', name: 'Authentik' }] } },
    'GET /api/v1/auth/oidc/identities': { status: 200, body: { identities: [] } },
    'GET /api/v1/me/tokens': { status: 200, body: { tokens: [] } },
    'GET /api/v1/me/connections': { status: 200, body: { connections: [] } },
    'GET /api/v1/shares/incoming': { status: 200, body: { shares: [] } },
    ...extra,
  }
}

const section = (name: string) => screen.getByRole('region', { name })

describe('security page', () => {
  beforeEach(() => go('/auth/security'))

  it('sends signed-out visitors to the login page', async () => {
    mockServer(base({ 'GET /api/v1/auth/session': err(401, 'unauthenticated'), 'GET /api/v1/auth/oidc/providers': err(404, 'not_found') }))
    render(<App />)
    await waitFor(() => expect(window.location.pathname).toBe('/auth/login'))
  })

  it('lists sessions, revokes another device and signs out the current one', async () => {
    let signedOut = false
    const srv = mockServer(base({
      'DELETE /api/v1/auth/sessions/8': { status: 204 },
      'POST /api/v1/auth/logout': () => { signedOut = true; return { status: 204 } },
      'GET /api/v1/auth/session': () => (signedOut ? err(401, 'unauthenticated') : { status: 200, body: sessionState() }),
      'GET /api/v1/auth/oidc/providers': err(404, 'not_found'),
    }))
    render(<App />)
    const user = userEvent.setup()
    expect(await screen.findByText('Signed in as')).toBeInTheDocument()
    const sessions = await screen.findByRole('region', { name: 'Active sessions' })
    expect(await within(sessions).findByText('Chrome on Windows')).toBeInTheDocument()
    expect(within(sessions).getByText('This device')).toBeInTheDocument()
    await user.click(within(sessions).getByRole('button', { name: 'Sign out Chrome on Windows' }))
    await waitFor(() => expect(srv.find('DELETE', '/api/v1/auth/sessions/8')).toHaveLength(1))
    expect(srv.find('DELETE', '/api/v1/auth/sessions/8')[0].headers.get('X-CSRF-Token')).toBe('csrf-1')
    expect(await screen.findByRole('status')).toHaveTextContent('Session signed out.')
    await user.click(screen.getByRole('button', { name: 'Sign out' }))
    await waitFor(() => expect(window.location.pathname).toBe('/auth/login'))
  })

  it('creates a personal API token, shows the secret once, then revokes it', async () => {
    let tokens: unknown[] = []
    const srv = mockServer(base({
      'GET /api/v1/me/tokens': () => ({ status: 200, body: { tokens } }),
      'POST /api/v1/me/tokens': (c) => {
        tokens = [{ id: 3, label: (c.body as { label: string }).label, scopes: ['bookmarks:read'], createdAt: now, expiresAt: null, lastUsedAt: null, revoked: false, expired: false }]
        return { status: 201, body: { token: tokens[0], secret: 'bmt1_SECRETVALUE' } }
      },
      'DELETE /api/v1/me/tokens/3': () => { tokens = []; return { status: 204 } },
    }))
    render(<App />)
    const user = userEvent.setup()
    const tok = await screen.findByRole('region', { name: 'Personal API tokens' })
    expect(await within(tok).findByText('You have no API tokens.')).toBeInTheDocument()
    await user.type(within(tok).getByLabelText('Token name'), 'backup script')
    await user.click(within(tok).getByRole('checkbox', { name: 'bookmarks:read' }))
    await user.click(within(tok).getByRole('button', { name: 'Create token' }))
    const reveal = await within(tok).findByRole('group', { name: 'Copy your new token now' })
    expect(within(reveal).getByText('bmt1_SECRETVALUE')).toBeInTheDocument()
    expect(within(reveal).getByText(/will not be shown again/)).toBeInTheDocument()
    expect(srv.find('POST', '/api/v1/me/tokens')[0].body).toEqual({ label: 'backup script', scopes: ['bookmarks:read'] })
    await user.click(within(reveal).getByRole('button', { name: 'Done' }))
    expect(within(tok).queryByText('bmt1_SECRETVALUE')).not.toBeInTheDocument()
    expect(await within(tok).findByText('backup script')).toBeInTheDocument()
    await user.click(within(tok).getByRole('button', { name: 'Revoke backup script' }))
    await user.click(await screen.findByRole('button', { name: 'Revoke token' }))
    await waitFor(() => expect(srv.find('DELETE', '/api/v1/me/tokens/3')).toHaveLength(1))
    expect(await within(tok).findByText('You have no API tokens.')).toBeInTheDocument()
  })

  it('requires choosing a scope before creating a token', async () => {
    const srv = mockServer(base())
    render(<App />)
    const user = userEvent.setup()
    const tok = await screen.findByRole('region', { name: 'Personal API tokens' })
    await user.type(within(tok).getByLabelText('Token name'), 'x')
    await user.click(within(tok).getByRole('button', { name: 'Create token' }))
    expect(await within(tok).findByRole('alert')).toHaveTextContent('Choose at least one permission.')
    expect(srv.find('POST', '/api/v1/me/tokens')).toHaveLength(0)
  })

  it('asks for the password again when the server requires recent sign-in, then retries', async () => {
    let attempts = 0
    const srv = mockServer(base({
      'POST /api/v1/me/tokens': (c) => {
        attempts++
        if (c.headers.get('X-CSRF-Token') !== 'csrf-2') return err(403, 'reauthentication_required')
        return { status: 201, body: { token: { id: 1, label: 'x', scopes: ['shares:read'], createdAt: now }, secret: 'bmt1_AFTERREAUTH' } }
      },
      'POST /api/v1/auth/reauthenticate': (c) => (c.body as { password: string }).password === 'right password'
        ? { status: 200, body: sessionState('alice', 'csrf-2') } : err(401, 'invalid_credentials'),
    }))
    render(<App />)
    const user = userEvent.setup()
    const tok = await screen.findByRole('region', { name: 'Personal API tokens' })
    await user.type(within(tok).getByLabelText('Token name'), 'x')
    await user.click(within(tok).getByRole('checkbox', { name: 'shares:read' }))
    await user.click(within(tok).getByRole('button', { name: 'Create token' }))
    const dialog = await screen.findByRole('dialog', { name: 'Confirm your password' })
    const pw = within(dialog).getByLabelText('Password')
    expect(pw).toHaveFocus()
    await user.type(pw, 'wrong')
    await user.click(within(dialog).getByRole('button', { name: 'Confirm' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Incorrect password.')
    await user.clear(pw)
    await user.type(pw, 'right password')
    await user.click(within(dialog).getByRole('button', { name: 'Confirm' }))
    expect(await within(tok).findByText('bmt1_AFTERREAUTH')).toBeInTheDocument()
    expect(attempts).toBe(2)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(srv.find('POST', '/api/v1/auth/reauthenticate').at(-1)?.headers.get('X-CSRF-Token')).toBe('csrf-1')
  })

  it('cancelling the password prompt abandons the action', async () => {
    const srv = mockServer(base({ 'POST /api/v1/me/tokens': err(403, 'reauthentication_required') }))
    render(<App />)
    const user = userEvent.setup()
    const tok = await screen.findByRole('region', { name: 'Personal API tokens' })
    await user.type(within(tok).getByLabelText('Token name'), 'x')
    await user.click(within(tok).getByRole('checkbox', { name: 'bookmarks:write' }))
    await user.click(within(tok).getByRole('button', { name: 'Create token' }))
    await screen.findByRole('dialog', { name: 'Confirm your password' })
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(within(tok).queryByText(/bmt1_/)).not.toBeInTheDocument()
    // Not retried and not reported as a failure: the user chose to stop.
    expect(srv.find('POST', '/api/v1/me/tokens')).toHaveLength(1)
    expect(srv.find('POST', '/api/v1/auth/reauthenticate')).toHaveLength(0)
    expect(within(tok).queryByRole('alert')).not.toBeInTheDocument()
  })

  it('creates a Floccus connection for a shared folder and shows setup details once', async () => {
    let conns: unknown[] = []
    const srv = mockServer(base({
      'GET /api/v1/shares/incoming': { status: 200, body: { shares: [{ id: 4, folderTitle: 'Team links', mode: 'view', ownerId: 2 }] } },
      'GET /api/v1/me/connections': () => ({ status: 200, body: { connections: conns } }),
      'POST /api/v1/me/connections': (c) => {
        conns = [{ id: 9, label: 'work laptop', kind: 'share', shareId: 4, mode: 'view', createdAt: now, lastUsedAt: null, revoked: false }]
        expect(c.body).toEqual({ label: 'work laptop', mode: 'view', shareId: 4 })
        return { status: 201, body: { connection: conns[0], secret: 'bms1.9.APPPASSWORD' } }
      },
      'DELETE /api/v1/me/connections/9': () => { conns = []; return { status: 204 } },
    }))
    render(<App />)
    const user = userEvent.setup()
    const sec = await screen.findByRole('region', { name: 'Floccus connections' })
    await user.type(within(sec).getByLabelText('Connection name'), 'work laptop')
    const access = within(sec).getByLabelText('Access') as HTMLSelectElement
    expect(access.value).toBe('edit')
    await user.selectOptions(within(sec).getByLabelText('Bookmarks'), 'Team links (shared, view only)')
    // A view-only share cannot back a read/write connection, so the form must not offer one.
    expect(access.value).toBe('view')
    expect(within(access).queryByRole('option', { name: 'Read and write' })).not.toBeInTheDocument()
    await user.click(within(sec).getByRole('button', { name: 'Create connection' }))
    const reveal = await within(sec).findByRole('group', { name: 'Set up Floccus now' })
    expect(within(reveal).getByText('bms1.9.APPPASSWORD')).toBeInTheDocument()
    expect(within(reveal).getByText(window.location.origin)).toBeInTheDocument()
    expect(within(reveal).getByText('alice')).toBeInTheDocument()
    await user.click(within(reveal).getByRole('button', { name: 'Done' }))
    expect(await within(sec).findByText('work laptop')).toBeInTheDocument()
    expect(within(sec).queryByText('bms1.9.APPPASSWORD')).not.toBeInTheDocument()
    await user.click(within(sec).getByRole('button', { name: 'Revoke work laptop' }))
    await user.click(await screen.findByRole('button', { name: 'Revoke connection' }))
    await waitFor(() => expect(srv.find('DELETE', '/api/v1/me/connections/9')).toHaveLength(1))
  })

  it('keeps read/write connections available for your bookmarks and editable shares', async () => {
    const posted: unknown[] = []
    const srv = mockServer(base({
      'GET /api/v1/shares/incoming': { status: 200, body: { shares: [
        { id: 4, folderTitle: 'Team links', mode: 'view', ownerId: 2 },
        { id: 5, folderTitle: 'Project', mode: 'edit', ownerId: 3 },
      ] } },
      'POST /api/v1/me/connections': (c) => {
        posted.push(c.body)
        return { status: 201, body: { connection: { id: posted.length }, secret: `bms1.${posted.length}.SECRET` } }
      },
    }))
    render(<App />)
    const user = userEvent.setup()
    const sec = await screen.findByRole('region', { name: 'Floccus connections' })
    const bookmarks = within(sec).getByLabelText('Bookmarks')
    await within(bookmarks).findByRole('option', { name: 'Project (shared, can edit)' })
    let access = within(sec).getByLabelText('Access') as HTMLSelectElement
    // Choosing a view-only share and then going back restores read/write as the default.
    await user.selectOptions(bookmarks, 'Team links (shared, view only)')
    expect(access.value).toBe('view')
    await user.selectOptions(bookmarks, 'Your bookmarks')
    expect(access.value).toBe('edit')
    expect(within(access).getByRole('option', { name: 'Read only (download changes)' })).toBeInTheDocument()
    await user.type(within(sec).getByLabelText('Connection name'), 'home')
    await user.click(within(sec).getByRole('button', { name: 'Create connection' }))
    await user.click(await within(sec).findByRole('button', { name: 'Done' }))

    await user.type(within(sec).getByLabelText('Connection name'), 'office')
    await user.selectOptions(within(sec).getByLabelText('Bookmarks'), 'Project (shared, can edit)')
    access = within(sec).getByLabelText('Access') as HTMLSelectElement
    expect(access.value).toBe('edit')
    expect(within(access).getByRole('option', { name: 'Read only (download changes)' })).toBeInTheDocument()
    await user.click(within(sec).getByRole('button', { name: 'Create connection' }))
    await within(sec).findByRole('group', { name: 'Set up Floccus now' })
    expect(posted).toEqual([{ label: 'home', mode: 'edit' }, { label: 'office', mode: 'edit', shareId: 5 }])
    expect(srv.find('POST', '/api/v1/me/connections')).toHaveLength(2)
  })

  it('renames a Floccus connection without touching its secret or access', async () => {
    let conns = [
      { id: 9, label: 'Login Flow v2 connection', kind: 'private', mode: 'edit', createdAt: now, lastUsedAt: null, revoked: false },
      { id: 10, label: 'office', kind: 'private', mode: 'view', createdAt: now, lastUsedAt: null, revoked: false },
    ]
    const srv = mockServer(base({
      'GET /api/v1/me/connections': () => ({ status: 200, body: { connections: conns } }),
      'PATCH /api/v1/me/connections/9': (c) => {
        conns = [{ ...conns[0], label: (c.body as { label: string }).label }, conns[1]]
        return { status: 200, body: { connection: conns[0] } }
      },
    }))
    render(<App />)
    const user = userEvent.setup()
    const sec = await screen.findByRole('region', { name: 'Floccus connections' })
    await user.click(await within(sec).findByRole('button', { name: 'Rename Login Flow v2 connection' }))
    const dialog = await screen.findByRole('dialog', { name: 'Rename connection' })
    const input = within(dialog).getByLabelText('New connection name') as HTMLInputElement
    expect(input.value).toBe('Login Flow v2 connection')
    expect(input.maxLength).toBe(200)
    await user.clear(input)
    await user.type(input, '  BookmarksSync-Home-Firefox ')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    const patches = srv.find('PATCH', '/api/v1/me/connections/9')
    expect(patches).toHaveLength(1)
    expect(patches[0].body).toEqual({ label: 'BookmarksSync-Home-Firefox' })
    expect(patches[0].headers.get('X-CSRF-Token')).toBe('csrf-1')
    expect(await within(sec).findByText('BookmarksSync-Home-Firefox')).toBeInTheDocument()
    expect(srv.find('POST', '/api/v1/auth/reauthenticate')).toHaveLength(0)
    expect(srv.find('POST', '/api/v1/me/connections')).toHaveLength(0)
    expect(srv.find('DELETE', '/api/v1/me/connections/9')).toHaveLength(0)
    // Create and Revoke stay available beside Rename.
    expect(within(sec).getByLabelText('Connection name')).toBeInTheDocument()
    expect(within(sec).getByRole('button', { name: 'Revoke BookmarksSync-Home-Firefox' })).toBeInTheDocument()
    expect(within(sec).getByRole('button', { name: 'Rename office' })).toBeInTheDocument()
  })

  it('cancelling a rename writes nothing and failed renames keep the typed name', async () => {
    const conns = [{ id: 9, label: 'laptop', kind: 'private', mode: 'edit', createdAt: now, lastUsedAt: null, revoked: false }]
    const srv = mockServer(base({
      'GET /api/v1/me/connections': { status: 200, body: { connections: conns } },
      'PATCH /api/v1/me/connections/9': err(404, 'not_found', 'connection not found'),
    }))
    render(<App />)
    const user = userEvent.setup()
    const sec = await screen.findByRole('region', { name: 'Floccus connections' })
    await user.click(await within(sec).findByRole('button', { name: 'Rename laptop' }))
    let dialog = await screen.findByRole('dialog', { name: 'Rename connection' })
    await user.type(within(dialog).getByLabelText('New connection name'), ' changed')
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(srv.find('PATCH', '/api/v1/me/connections/9')).toHaveLength(0)
    expect(within(sec).getByText('laptop')).toBeInTheDocument()

    await user.click(within(sec).getByRole('button', { name: 'Rename laptop' }))
    dialog = await screen.findByRole('dialog', { name: 'Rename connection' })
    const input = within(dialog).getByLabelText('New connection name') as HTMLInputElement
    await user.clear(input)
    await user.type(input, '   ')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('Enter a connection name')
    expect(srv.find('PATCH', '/api/v1/me/connections/9')).toHaveLength(0)

    await user.clear(input)
    await user.type(input, 'Home Firefox')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('This connection no longer exists')
    expect(screen.getByRole('dialog', { name: 'Rename connection' })).toBeInTheDocument()
    expect((within(dialog).getByLabelText('New connection name') as HTMLInputElement).value).toBe('Home Firefox')
    expect(srv.find('PATCH', '/api/v1/me/connections/9')).toHaveLength(1)
  })

  it('sets up TOTP, shows recovery codes once, and turns it off with a code', async () => {
    let enabled = false
    const srv = mockServer(base({
      'GET /api/v1/auth/totp': () => ({ status: 200, body: { enabled, recoveryCodesRemaining: enabled ? 10 : 0 } }),
      'POST /api/v1/auth/totp/enroll': { status: 200, body: { secret: 'JBSWY3DPEHPK3PXP', provisioningUri: 'otpauth://totp/BookmarksSync:alice?secret=JBSWY3DPEHPK3PXP' } },
      'POST /api/v1/auth/totp/confirm': (c) => {
        if ((c.body as { code: string }).code !== '111111') return err(401, 'invalid_code')
        enabled = true
        return { status: 200, body: { recoveryCodes: ['aaaa-bbbb-cccc-dddd', 'eeee-ffff-gggg-hhhh'], csrfToken: 'csrf-3' } }
      },
      'POST /api/v1/auth/totp/disable': (c) => {
        expect(c.headers.get('X-CSRF-Token')).toBe('csrf-3')
        expect(c.body).toEqual({ code: '222222' })
        enabled = false
        return { status: 200, body: { csrfToken: 'csrf-4' } }
      },
    }))
    render(<App />)
    const user = userEvent.setup()
    const sec = await screen.findByRole('region', { name: 'Authenticator app' })
    await user.click(await within(sec).findByRole('button', { name: 'Set up authenticator app' }))
    expect(await within(sec).findByText('JBSWY3DPEHPK3PXP')).toBeInTheDocument()
    expect(within(sec).getByText(/otpauth:\/\/totp/)).toBeInTheDocument()
    await user.type(within(sec).getByLabelText('Authentication code'), '000000')
    await user.click(within(sec).getByRole('button', { name: 'Turn on' }))
    expect(await within(sec).findByRole('alert')).toHaveTextContent('That code did not work.')
    await user.clear(within(sec).getByLabelText('Authentication code'))
    await user.type(within(sec).getByLabelText('Authentication code'), '111111')
    await user.click(within(sec).getByRole('button', { name: 'Turn on' }))
    const codes = await within(sec).findByRole('group', { name: 'Save your recovery codes' })
    expect(within(codes).getByText('aaaa-bbbb-cccc-dddd')).toBeInTheDocument()
    await user.click(within(codes).getByRole('button', { name: 'I have saved them' }))
    expect(within(sec).queryByText('aaaa-bbbb-cccc-dddd')).not.toBeInTheDocument()
    expect(await within(sec).findByText(/is on/)).toBeInTheDocument()
    expect(within(sec).getByText(/10 recovery codes left/)).toBeInTheDocument()
    await user.click(within(sec).getByRole('button', { name: 'Turn off' }))
    await user.type(await screen.findByLabelText('Code from your app or a recovery code'), '222222')
    await user.click(screen.getByRole('button', { name: 'Turn off authenticator app' }))
    await waitFor(() => expect(srv.find('POST', '/api/v1/auth/totp/disable')).toHaveLength(1))
    expect(await within(sec).findByRole('button', { name: 'Set up authenticator app' })).toBeInTheDocument()
  })

  it('adds, renames and removes a passkey', async () => {
    let passkeys: unknown[] = []
    const create = vi.fn(async () => ({
      id: 'k', rawId: new Uint8Array([9]).buffer, type: 'public-key', authenticatorAttachment: 'platform',
      response: { clientDataJSON: new Uint8Array([1]).buffer, attestationObject: new Uint8Array([2]).buffer, getTransports: () => ['internal'] },
      getClientExtensionResults: () => ({}),
    }))
    vi.stubGlobal('PublicKeyCredential', function PublicKeyCredential() {})
    Object.defineProperty(navigator, 'credentials', { value: { create, get: vi.fn() }, configurable: true })
    const srv = mockServer(base({
      'GET /api/v1/auth/passkeys': () => ({ status: 200, body: { passkeys } }),
      'POST /api/v1/auth/passkeys/register/begin': { status: 200, body: { flowId: 'reg', options: { publicKey: {
        challenge: 'AQID', rp: { id: 'bookmarks.test', name: 'BookmarksSync' }, user: { id: 'BAU', name: 'alice', displayName: 'alice' },
        pubKeyCredParams: [{ type: 'public-key', alg: -7 }], excludeCredentials: [{ type: 'public-key', id: 'Bwg' }],
        authenticatorSelection: { residentKey: 'required', userVerification: 'required' },
      } } } },
      'POST /api/v1/auth/passkeys/register/finish': (c) => {
        passkeys = [{ id: 5, name: (c.body as { name: string }).name, createdAt: now, lastUsedAt: null, backupEligible: true, backupState: true, transports: ['internal'] }]
        return { status: 201, body: { passkey: passkeys[0], csrfToken: 'csrf-5' } }
      },
      'PATCH /api/v1/auth/passkeys/5': (c) => { (passkeys[0] as { name: string }).name = (c.body as { name: string }).name; return { status: 204 } },
      'DELETE /api/v1/auth/passkeys/5': () => { passkeys = []; return { status: 200, body: { csrfToken: 'csrf-6' } } },
    }))
    render(<App />)
    const user = userEvent.setup()
    const sec = await screen.findByRole('region', { name: 'Passkeys' })
    expect(await within(sec).findByText('You have no passkeys yet.')).toBeInTheDocument()
    await user.type(within(sec).getByLabelText('Passkey name'), 'Laptop')
    await user.click(within(sec).getByRole('button', { name: 'Add a passkey' }))
    expect(await within(sec).findByText('Laptop')).toBeInTheDocument()
    const opts = (create.mock.calls[0] as unknown as [{ publicKey: { challenge: ArrayBuffer; user: { id: ArrayBuffer }; excludeCredentials: { id: ArrayBuffer }[] } }])[0].publicKey
    expect(new Uint8Array(opts.challenge)).toEqual(new Uint8Array([1, 2, 3]))
    expect(new Uint8Array(opts.user.id)).toEqual(new Uint8Array([4, 5]))
    expect(new Uint8Array(opts.excludeCredentials[0].id)).toEqual(new Uint8Array([7, 8]))
    const finish = srv.find('POST', '/api/v1/auth/passkeys/register/finish')[0]
    expect(finish.body).toMatchObject({ flowId: 'reg', name: 'Laptop', credential: { rawId: 'CQ', response: { attestationObject: 'Ag', transports: ['internal'] } } })
    await user.click(within(sec).getByRole('button', { name: 'Rename Laptop' }))
    const name = await within(sec).findByLabelText('New name for Laptop')
    await user.clear(name)
    await user.type(name, 'Work laptop{Enter}')
    expect(await within(sec).findByText('Work laptop')).toBeInTheDocument()
    expect(srv.find('PATCH', '/api/v1/auth/passkeys/5')[0].headers.get('X-CSRF-Token')).toBe('csrf-5')
    await user.click(within(sec).getByRole('button', { name: 'Remove Work laptop' }))
    await user.click(await screen.findByRole('button', { name: 'Remove passkey' }))
    expect(await within(sec).findByText('You have no passkeys yet.')).toBeInTheDocument()
  })

  it('reports when passkeys are not available on this server', async () => {
    mockServer(base({ 'GET /api/v1/auth/passkeys': err(404, 'not_found') }))
    render(<App />)
    const sec = await screen.findByRole('region', { name: 'Passkeys' })
    expect(await within(sec).findByText('Passkeys are not available on this server.')).toBeInTheDocument()
  })

  it('links and unlinks provider accounts, listing only configured providers', async () => {
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, assign, pathname: '/auth/security', search: '', hash: '', origin: 'http://bookmarks.test' })
    let ids: unknown[] = [{ id: 2, provider: 'authentik', displayName: 'Alice A.', createdAt: now, lastLoginAt: null }]
    const srv = mockServer(base({
      'GET /api/v1/auth/oidc/providers': { status: 200, body: { providers: [{ id: 'google', name: 'Google' }, { id: 'authentik', name: 'Authentik' }] } },
      'GET /api/v1/auth/oidc/identities': () => ({ status: 200, body: { identities: ids } }),
      'POST /api/v1/auth/oidc/google/link': { status: 200, body: { authorizationUrl: 'https://accounts.example/auth?state=link' } },
      'DELETE /api/v1/auth/oidc/identities/2': () => { ids = []; return { status: 200, body: { csrfToken: 'csrf-7' } } },
    }))
    render(<App />)
    const user = userEvent.setup()
    const sec = await screen.findByRole('region', { name: 'Linked accounts' })
    expect(await within(sec).findByText('Alice A.')).toBeInTheDocument()
    expect(within(sec).queryByRole('button', { name: 'Link Authentik' })).not.toBeInTheDocument()
    await user.click(within(sec).getByRole('button', { name: 'Link Google' }))
    await waitFor(() => expect(assign).toHaveBeenCalledWith('https://accounts.example/auth?state=link'))
    expect(srv.find('POST', '/api/v1/auth/oidc/google/link')[0].headers.get('X-CSRF-Token')).toBe('csrf-1')
    await user.click(within(sec).getByRole('button', { name: 'Unlink Authentik' }))
    await user.click(await screen.findByRole('button', { name: 'Unlink account' }))
    expect(await within(sec).findByRole('button', { name: 'Link Authentik' })).toBeInTheDocument()
  })

  it('hides linked accounts when no provider is configured', async () => {
    mockServer(base({ 'GET /api/v1/auth/oidc/providers': err(404, 'not_found') }))
    render(<App />)
    await screen.findByRole('region', { name: 'Personal API tokens' })
    expect(screen.queryByRole('region', { name: 'Linked accounts' })).not.toBeInTheDocument()
  })

  it('links to the dashboard but keeps workspace themes and admin controls there', async () => {
    mockServer(base())
    render(<App />)
    await screen.findByRole('region', { name: 'Personal API tokens' })
    expect(screen.getByRole('button', { name: 'Dashboard' })).toBeInTheDocument()
    expect(screen.queryByText(/admin/i, { selector: 'a,button,h2' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /theme|dark mode|light mode/i })).not.toBeInTheDocument()
    expect(section('Active sessions')).toBeInTheDocument()
  })
})

describe('device names', () => {
  it('summarises common user agents and keeps other labels', async () => {
    const { deviceName } = await import('../sections/Sessions')
    expect(deviceName('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/159.0 Safari/537.36')).toBe('Chrome on Windows')
    expect(deviceName('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/159.0 Safari/537.36 Edg/159.0')).toBe('Edge on Windows')
    expect(deviceName('Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0')).toBe('Firefox on Linux')
    expect(deviceName('Mozilla/5.0 (Macintosh; Intel Mac OS X 15_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15')).toBe('Safari on macOS')
    expect(deviceName('Work laptop')).toBe('Work laptop')
    expect(deviceName('')).toBe('Unknown browser')
  })
})
