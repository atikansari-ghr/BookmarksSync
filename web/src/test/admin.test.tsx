import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import { App } from '../App'
import { err, go, mockServer, sessionState, type Route } from './server'

function adminSession() {
  const s = sessionState()
  s.user.isAdmin = true
  return s
}

function adminRoutes(): Record<string, Route> {
  return {
    'GET /api/v1/auth/session': { status: 200, body: adminSession() },
    'GET /api/v1/me/settings': { status: 200, body: { settings: {} } },
    'GET /api/v1/admin/users': { status: 200, body: { users: [{ id: 2, username: 'sam', active: true, isAdmin: false, bookmarkCount: 12, folderCount: 4, storageBytes: 2048, connectionCount: 1 }], page: 0, limit: 50, hasMore: false } },
    'GET /api/v1/admin/health': { status: 200, body: { cpuPercent: 12, memory: { totalBytes: 1000, usedBytes: 400, percent: 40 }, disk: null, uptimeSeconds: 1000, unavailable: ['disk'], dbReady: true, syncErrors: 2 } },
    'GET /api/v1/admin/settings': { status: 200, body: { settings: { 'branding.app-title': 'BookmarksSync' } } },
    'GET /api/v1/admin/activity': { status: 200, body: { items: [{ id: 1, actorId: 1, action: 'user_created', targetType: 'user', targetId: 2, createdAt: '2026-11-20T12:00:00Z' }] } },
    'GET /api/v1/admin/backups': { status: 200, body: { schedule: { weeklyEnabled: false, monthlyEnabled: false, timezone: 'Asia/Riyadh', weeklyDay: 0, weeklyHour: 3, weeklyMinute: 0, monthlyDay: 1, monthlyHour: 4, monthlyMinute: 0, weeklyRetention: 8, monthlyRetention: 12 }, destinations: [{ id: '0123456789abcdef0123456789abcdef', label: 'NAS', kind: 'rclone', reference: 'nas:bookmarks', enabled: true }], runs: [], running: false } },
    'PUT /api/v1/admin/backups/schedule': { status: 200, body: { schedule: { weeklyEnabled: true, monthlyEnabled: false, timezone: 'Asia/Riyadh', weeklyDay: 0, weeklyHour: 3, weeklyMinute: 0, monthlyDay: 1, monthlyHour: 4, monthlyMinute: 0, weeklyRetention: 8, monthlyRetention: 12 } } },
    'POST /api/v1/admin/backups/destinations/test': { status: 200, body: { status: 'ok' } },
    'POST /api/v1/admin/backups/run': { status: 202, body: { batchId: '0123456789abcdef0123456789abcdef', status: 'accepted' } },
    'PATCH /api/v1/admin/settings': { status: 200, body: { settings: {} } },
    'POST /api/v1/admin/settings/assets': { status: 201, body: { url: '/api/v1/auth/presentation/assets/0123456789abcdef0123456789abcdef' } },
    'GET /api/v1/admin/users/2/settings': { status: 200, body: { settings: {} } },
    'PATCH /api/v1/admin/users/2/settings': { status: 200, body: { settings: { 'ui.theme': 'midnight-glass' } } },
  }
}

describe('admin workspace', () => {
  beforeEach(() => go('/admin'))

  it('shows account usage, host health, and sanitized activity', async () => {
    mockServer(adminRoutes())
    render(<App />)
    expect(await screen.findByRole('heading', { name: 'Server health' })).toBeInTheDocument()
    await screen.findByText('No backup runs yet.')
    expect(screen.getByText('12%')).toBeInTheDocument()
    expect(screen.getByText('2 server errors')).toBeInTheDocument()
    expect(screen.getByText('sam')).toBeInTheDocument()
    expect(screen.getByText('2 KB')).toBeInTheDocument()
    expect(screen.getByText('User created')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'System backups' })).toBeInTheDocument()
    expect(screen.getAllByText(/nas:bookmarks/)).toHaveLength(2)
  })

  it('saves the backup schedule, tests a destination, and starts a manual run', async () => {
    const api = mockServer(adminRoutes())
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'System backups' })
    await screen.findByText('NAS')
    await user.click(screen.getByLabelText('Weekly backups'))
    await user.click(screen.getByRole('button', { name: 'Save schedule' }))
    await waitFor(() => expect(api.find('PUT', '/api/v1/admin/backups/schedule')).toHaveLength(1))
    expect(api.find('PUT', '/api/v1/admin/backups/schedule')[0].body).toMatchObject({ weeklyEnabled: true, timezone: 'Asia/Riyadh' })
    await user.click(screen.getByRole('button', { name: 'Test destination' }))
    await waitFor(() => expect(api.find('POST', '/api/v1/admin/backups/destinations/test')).toHaveLength(1))
    await user.click(screen.getByRole('button', { name: 'Run backup now' }))
    await waitFor(() => expect(api.find('POST', '/api/v1/admin/backups/run')).toHaveLength(1))
  })

  it('limits branding text fields to the server accepted lengths', async () => {
    mockServer(adminRoutes())
    render(<App />)
    await screen.findByRole('heading', { name: 'Server health' })
    expect(screen.getByLabelText('Application title')).toHaveAttribute('maxLength', '80')
    expect(screen.getByLabelText('Sign-in title')).toHaveAttribute('maxLength', '100')
    expect(screen.getByLabelText('Tagline')).toHaveAttribute('maxLength', '160')
    expect(screen.getByLabelText('Supporting line')).toHaveAttribute('maxLength', '240')
  })

  it('loads every administrator user page instead of silently truncating the account list', async () => {
    const routes = adminRoutes()
    routes['GET /api/v1/admin/users'] = (call) => call.query.get('page') === '1'
      ? { status: 200, body: { users: [{ id: 3, username: 'lee', active: true, isAdmin: false, bookmarkCount: 0, folderCount: 2, storageBytes: 0, connectionCount: 0 }], page: 1, limit: 200, hasMore: false } }
      : { status: 200, body: { users: [{ id: 2, username: 'sam', active: true, isAdmin: false, bookmarkCount: 12, folderCount: 4, storageBytes: 2048, connectionCount: 1 }], page: 0, limit: 200, hasMore: true } }
    const api = mockServer(routes)
    render(<App />)
    await screen.findByRole('heading', { name: 'Server health' })
    expect(await screen.findByText('lee')).toBeInTheDocument()
    expect(api.find('GET', '/api/v1/admin/users').map((call) => call.query.get('page'))).toEqual(['0', '1'])
  })

  it('saves branding preferences and per-user theme defaults', async () => {
    const api = mockServer(adminRoutes())
    render(<App />)
    await screen.findByRole('heading', { name: 'Server health' })
    fireEvent.change(screen.getByLabelText('Application title'), { target: { value: 'My Bookmarks' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save branding' }))
    await waitFor(() => expect(api.find('PATCH', '/api/v1/admin/settings')).toHaveLength(1))
    expect(api.find('PATCH', '/api/v1/admin/settings')[0].body).toEqual({ 'branding.app-title': 'My Bookmarks' })
    fireEvent.change(screen.getByLabelText('Theme default for sam'), { target: { value: 'midnight-glass' } })
    await waitFor(() => expect(api.find('PATCH', '/api/v1/admin/users/2/settings')).toHaveLength(1))
  })

  it('keeps an unset user theme default selectable and rolls back failed saves', async () => {
    const routes = adminRoutes()
    routes['PATCH /api/v1/admin/users/2/settings'] = err(500, 'server_error')
    const api = mockServer(routes)
    render(<App />)
    const user = userEvent.setup()
    await screen.findByRole('heading', { name: 'Server health' })
    const theme = screen.getByLabelText('Theme default for sam')
    expect(theme).toHaveValue('')
    await user.selectOptions(theme, 'ocean-glass')
    await waitFor(() => expect(api.find('PATCH', '/api/v1/admin/users/2/settings')).toHaveLength(1))
    expect(theme).toHaveValue('')
    expect(await screen.findByRole('alert')).toHaveTextContent('server_error')
  })

  it('clears a saved branding override when the admin restores the built-in default', async () => {
    const routes = adminRoutes()
    routes['GET /api/v1/admin/settings'] = { status: 200, body: { settings: { 'branding.tagline': 'Custom tagline' } } }
    const api = mockServer(routes)
    render(<App />)
    const user = userEvent.setup()
    await screen.findByRole('heading', { name: 'Server health' })
    const tagline = screen.getByLabelText('Tagline')
    await user.clear(tagline)
    await user.type(tagline, 'Your bookmarks. Your browsers. Your control.')
    await user.click(screen.getByRole('button', { name: 'Save branding' }))
    await waitFor(() => expect(api.find('PATCH', '/api/v1/admin/settings')).toHaveLength(1))
    expect(api.find('PATCH', '/api/v1/admin/settings')[0].body).toEqual({ 'branding.tagline': null })
  })

  it('uploads a logo as multipart data and shows the new preview', async () => {
    const api = mockServer(adminRoutes())
    render(<App />)
    await screen.findByRole('heading', { name: 'Server health' })
    const file = new File(['small image'], 'logo.png', { type: 'image/png' })
    fireEvent.change(screen.getByLabelText('Login logo image'), { target: { files: [file] } })
    await waitFor(() => expect(api.find('POST', '/api/v1/admin/settings/assets')).toHaveLength(1))
    const call = api.find('POST', '/api/v1/admin/settings/assets')[0]
    expect(call.body).toBeInstanceOf(FormData)
    expect((call.body as FormData).get('kind')).toBe('logo')
    expect((call.body as FormData).get('asset')).toBe(file)
    expect(call.headers.has('content-type')).toBe(false)
    expect(await screen.findByAltText('Current login logo')).toHaveAttribute('src', '/api/v1/auth/presentation/assets/0123456789abcdef0123456789abcdef')
  })

  it('reauthenticates and retries a branding image upload when the recent sign-in window expires', async () => {
    let uploads = 0
    const api = mockServer(adminRoutes())
    api.set('POST /api/v1/admin/settings/assets', (call) => {
      uploads++
      if (call.headers.get('X-CSRF-Token') !== 'csrf-2') return err(403, 'reauthentication_required')
      return { status: 201, body: { url: '/api/v1/auth/presentation/assets/0123456789abcdef0123456789abcdef' } }
    })
    api.set('POST /api/v1/auth/reauthenticate', (call) => {
      if ((call.body as { password: string }).password !== 'right password') return err(401, 'invalid_credentials')
      const refreshed = adminSession()
      refreshed.csrfToken = 'csrf-2'
      return { status: 200, body: refreshed }
    })
    render(<App />)
    const user = userEvent.setup()
    await screen.findByRole('heading', { name: 'Server health' })
    fireEvent.change(screen.getByLabelText('Login logo image'), { target: { files: [new File(['image'], 'logo.png', { type: 'image/png' })] } })
    const dialog = await screen.findByRole('dialog', { name: 'Confirm your password' })
    await user.type(screen.getByLabelText('Password'), 'right password')
    await user.click(within(dialog).getByRole('button', { name: 'Confirm' }))
    await screen.findByAltText('Current login logo')
    expect(uploads).toBe(2)
    expect(api.find('POST', '/api/v1/auth/reauthenticate')).toHaveLength(1)
    expect(api.find('POST', '/api/v1/admin/settings/assets')[1].headers.get('X-CSRF-Token')).toBe('csrf-2')
  })
})
