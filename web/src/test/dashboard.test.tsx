import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it } from 'vitest'
import { App } from '../App'
import { err, go, mockServer, sessionState } from './server'

const dashboard = {
  totalBookmarks: 42,
  duplicateCount: 3,
  lastContactAt: '2026-11-19T09:00:00Z',
  recentChanges: { counts: { created: 2, updated: 1, deleted: 0 }, items: [{ type: 'created', title: 'Example', at: '2026-11-19T09:00:00Z' }] },
}

function routes() {
  return {
    'GET /api/v1/auth/session': { status: 200, body: sessionState() },
    'GET /api/v1/me/dashboard': { status: 200, body: dashboard },
    'GET /api/v1/me/settings': { status: 200, body: { settings: {} } },
    'PATCH /api/v1/me/settings': { status: 200, body: { settings: {} } },
  }
}

describe('user dashboard', () => {
  beforeEach(() => go('/dashboard'))

  it('DashboardReordersWidgetsByKeyboard and persists the new order', async () => {
    const server = mockServer(routes())
    render(<App />)
    const grid = await screen.findByRole('region', { name: 'Dashboard widgets' })
    const first = within(grid).getByRole('article', { name: 'Total Bookmarks' })
    fireEvent.keyDown(first, { key: 'ArrowDown', altKey: true })
    await waitFor(() => expect(server.find('PATCH', '/api/v1/me/settings')).toHaveLength(1))
    const labels = [...grid.querySelectorAll('article')].map((el) => el.getAttribute('aria-label'))
    expect(labels[0]).toBe('Recent Changes')
    expect((server.find('PATCH', '/api/v1/me/settings')[0].body as Record<string, string>)['dashboard.layout']).toContain('"id":"recent","visible":true,"order":0')
  })

  it('lets users hide and restore individual widgets', async () => {
    const server = mockServer(routes())
    render(<App />)
    const grid = await screen.findByRole('region', { name: 'Dashboard widgets' })
    const toggle = screen.getByRole('checkbox', { name: 'Show Total Bookmarks' })
    fireEvent.click(toggle)
    await waitFor(() => expect(within(grid).queryByRole('article', { name: 'Total Bookmarks' })).not.toBeInTheDocument())
    expect((server.find('PATCH', '/api/v1/me/settings')[0].body as Record<string, string>)['dashboard.layout']).toContain('"id":"total","visible":false')
    fireEvent.click(screen.getByRole('checkbox', { name: 'Show Total Bookmarks' }))
    await waitFor(() => expect(within(grid).getByRole('article', { name: 'Total Bookmarks' })).toBeInTheDocument())
  })

  it('keeps dashboard preferences disabled until saved settings load', async () => {
    let finishSettings!: (reply: { status: number; body: unknown }) => void
    const settings = new Promise<{ status: number; body: unknown }>((resolve) => { finishSettings = resolve })
    mockServer({
      ...routes(),
      'GET /api/v1/me/settings': () => settings,
    })
    render(<App />)
    expect(await screen.findByLabelText('Theme')).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Reset widgets' })).toBeDisabled()
    expect(screen.getByRole('checkbox', { name: 'Show Total Bookmarks' })).toBeDisabled()
    finishSettings({ status: 200, body: { settings: {} } })
    await waitFor(() => expect(screen.getByLabelText('Theme')).toBeEnabled())
    expect(screen.getByRole('button', { name: 'Reset widgets' })).toBeEnabled()
    expect(screen.getByRole('checkbox', { name: 'Show Total Bookmarks' })).toBeEnabled()
  })

  it('DashboardFallsBackAtMobileWidth', async () => {
    window.innerWidth = 390
    const server = mockServer(routes())
    render(<App />)
    const grid = await screen.findByRole('region', { name: 'Dashboard widgets' })
    expect(grid).toHaveClass('mobile')
    window.innerWidth = 1024
    fireEvent.resize(window)
    await waitFor(() => expect(grid).not.toHaveClass('mobile'))
    void server
  })

  it('saves the selected theme, color scheme, and transparency preference', async () => {
    const server = mockServer(routes())
    render(<App />)
    await screen.findByRole('region', { name: 'Dashboard widgets' })
    fireEvent.change(screen.getByLabelText('Theme'), { target: { value: 'warm-glass' } })
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe('warm-glass'))
    fireEvent.change(screen.getByLabelText('Colors'), { target: { value: 'dark' } })
    fireEvent.click(screen.getByRole('checkbox', { name: 'Reduce transparency' }))
    await waitFor(() => expect(document.documentElement.dataset.reducedTransparency).toBe('true'))
    const changes = server.find('PATCH', '/api/v1/me/settings').map((c) => c.body as Record<string, string>)
    expect(changes).toEqual([
      { 'ui.theme': 'warm-glass' },
      { 'ui.color-scheme': 'dark' },
      { 'ui.reduced-transparency': 'true' },
    ])
  })

  it('rolls back a theme that could not be saved and clears the error after a later success', async () => {
    const server = mockServer(routes())
    let writes = 0
    server.set('PATCH /api/v1/me/settings', () => {
      writes++
      return writes === 1 ? err(500, 'save_failed') : { status: 200, body: { settings: {} } }
    })
    render(<App />)
    await screen.findByRole('region', { name: 'Dashboard widgets' })
    const theme = screen.getByLabelText('Theme')
    fireEvent.change(theme, { target: { value: 'warm-glass' } })
    await screen.findByRole('alert')
    expect(theme).toHaveValue('ocean-glass')
    expect(document.documentElement.dataset.theme).toBe('ocean-glass')
    fireEvent.change(theme, { target: { value: 'midnight-glass' } })
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe('midnight-glass'))
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('does not let an older failed theme save undo a newer successful choice', async () => {
    let failFirst!: (reply: { status: number; body: unknown }) => void
    let writes = 0
    const server = mockServer({
      ...routes(),
      'PATCH /api/v1/me/settings': () => {
        writes++
        if (writes === 1) return new Promise<{ status: number; body: unknown }>((resolve) => { failFirst = resolve })
        return { status: 200, body: { settings: {} } }
      },
    })
    render(<App />)
    await screen.findByRole('region', { name: 'Dashboard widgets' })
    const theme = screen.getByLabelText('Theme')
    fireEvent.change(theme, { target: { value: 'warm-glass' } })
    fireEvent.change(theme, { target: { value: 'midnight-glass' } })
    await waitFor(() => expect(server.find('PATCH', '/api/v1/me/settings')).toHaveLength(2))
    await waitFor(() => expect(document.documentElement.dataset.theme).toBe('midnight-glass'))
    failFirst({ status: 500, body: { error: { code: 'save_failed', message: 'save failed' } } })
    await screen.findByRole('alert')
    expect(theme).toHaveValue('midnight-glass')
    expect(document.documentElement.dataset.theme).toBe('midnight-glass')
  })

  it('restores the previous widget order when saving a reorder fails', async () => {
    const server = mockServer({ ...routes(), 'PATCH /api/v1/me/settings': err(500, 'save_failed') })
    render(<App />)
    const grid = await screen.findByRole('region', { name: 'Dashboard widgets' })
    fireEvent.keyDown(within(grid).getByRole('article', { name: 'Total Bookmarks' }), { key: 'ArrowDown', altKey: true })
    await screen.findByRole('alert')
    const labels = [...grid.querySelectorAll('article')].map((el) => el.getAttribute('aria-label'))
    expect(labels[0]).toBe('Total Bookmarks')
    expect(server.find('PATCH', '/api/v1/me/settings')).toHaveLength(1)
  })
})
