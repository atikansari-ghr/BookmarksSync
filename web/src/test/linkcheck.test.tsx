import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import { App } from '../App'
import { err, go, mockServer } from './server'
import { fakeWorkspace } from './workspace'

async function openWork(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByRole('tree', { name: 'Folders' })
  await user.click(await screen.findByRole('treeitem', { name: 'Work' }))
  await screen.findByText('GitHub', { selector: '.bm-title' })
}

describe('link checks in the organizer', () => {
  beforeEach(() => go('/bookmarks'))

  it('submits only the selected bookmarks in the active collection and shows completed results', async () => {
    const ws = fakeWorkspace()
    const srv = mockServer({
      ...ws.routes,
      'POST /api/v1/link-checks': () => ({ status: 202, body: {
        id: 9, status: 'queued', total: 1, done: 0, results: [{ bookmarkId: 1, url: 'https://github.com', status: 'pending' }],
      } }),
      'GET /api/v1/link-checks/9': () => ({ status: 200, body: {
        id: 9, status: 'completed', total: 1, done: 1, results: [{ bookmarkId: 1, url: 'https://github.com', status: 'ok', httpStatus: 200, detail: '', redirects: 0 }],
      } }),
    })
    render(<App />)
    const user = userEvent.setup()
    await openWork(user)
    await user.click(screen.getByRole('checkbox', { name: 'Select GitHub' }))
    await user.click(screen.getByRole('button', { name: 'Check links (1)' }))

    await waitFor(() => expect(srv.find('POST', '/api/v1/link-checks')).toHaveLength(1))
    expect(srv.find('POST', '/api/v1/link-checks')[0].body).toEqual({ bookmarkIds: [1] })
    await screen.findByText('https://github.com', { selector: '.link-check-result a' })
    await waitFor(() => expect(srv.find('GET', '/api/v1/link-checks/9').length).toBeGreaterThanOrEqual(1), { timeout: 4000 })
    await waitFor(() => expect(screen.getByText('OK (200)')).toBeInTheDocument(), { timeout: 4000 })
  })

  it('retries status polling after a temporary server error', async () => {
    const ws = fakeWorkspace()
    let polls = 0
    const srv = mockServer({
      ...ws.routes,
      'POST /api/v1/link-checks': { status: 202, body: { id: 12, status: 'queued', total: 1, done: 0, results: [] } },
      'GET /api/v1/link-checks/12': () => {
        polls++
        if (polls === 1) return err(503, 'temporarily_unavailable')
        return { status: 200, body: { id: 12, status: 'completed', total: 1, done: 1, results: [{ bookmarkId: 1, url: 'https://github.com', status: 'ok', httpStatus: 200, detail: '', redirects: 0 }] } }
      },
    })
    render(<App />)
    const user = userEvent.setup()
    await openWork(user)
    await user.click(screen.getByRole('checkbox', { name: 'Select GitHub' }))
    await user.click(screen.getByRole('button', { name: 'Check links (1)' }))
    await waitFor(() => expect(screen.getByText('OK (200)')).toBeInTheDocument(), { timeout: 6500 })
    expect(polls).toBeGreaterThanOrEqual(2)
    expect(srv.find('GET', '/api/v1/link-checks/12')).toHaveLength(polls)
  }, 7500)

  it('edits the signed-in user exclusion list without changing other settings', async () => {
    const ws = fakeWorkspace()
    const srv = mockServer({
      ...ws.routes,
      'GET /api/v1/me/settings': { status: 200, body: { settings: { 'linkcheck.exclusions': '["intranet.example"]', 'ui.timezone': 'Asia/Riyadh' } } },
      'PATCH /api/v1/me/settings': { status: 200, body: { settings: { 'linkcheck.exclusions': '["intranet.example","10.0.0.0/8"]', 'ui.timezone': 'Asia/Riyadh' } } },
    })
    render(<App />)
    const user = userEvent.setup()
    await openWork(user)
    await user.click(screen.getByRole('button', { name: 'Link check exclusions' }))
    await screen.findByRole('dialog', { name: 'Link check exclusions' })
    const input = screen.getByRole('textbox', { name: 'Domains, IP addresses or CIDR ranges' })
    expect(input).toHaveValue('intranet.example')
    await user.clear(input)
    await user.type(input, 'intranet.example{Enter}10.0.0.0/8')
    await user.click(screen.getByRole('button', { name: 'Save exclusions' }))
    await waitFor(() => expect(srv.find('PATCH', '/api/v1/me/settings')).toHaveLength(1))
    expect(srv.find('PATCH', '/api/v1/me/settings')[0].body).toEqual({ 'linkcheck.exclusions': '["intranet.example","10.0.0.0/8"]' })
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Link check exclusions' })).not.toBeInTheDocument())
    expect(screen.getByRole('status')).toHaveTextContent('Link check exclusions saved.')
  })

  it('keeps unsaved exclusion edits after a save failure without offering a destructive reload', async () => {
    const ws = fakeWorkspace()
    const srv = mockServer({
      ...ws.routes,
      'GET /api/v1/me/settings': { status: 200, body: { settings: { 'linkcheck.exclusions': '["intranet.example"]' } } },
      'PATCH /api/v1/me/settings': err(500, 'save_failed'),
    })
    render(<App />)
    const user = userEvent.setup()
    await openWork(user)
    await user.click(screen.getByRole('button', { name: 'Link check exclusions' }))
    const input = await screen.findByRole('textbox', { name: 'Domains, IP addresses or CIDR ranges' })
    await user.clear(input)
    await user.type(input, 'custom.example')
    await user.click(screen.getByRole('button', { name: 'Save exclusions' }))
    await screen.findByRole('alert')
    expect(input).toHaveValue('custom.example')
    expect(screen.queryByRole('button', { name: 'Retry' })).not.toBeInTheDocument()
    expect(srv.find('PATCH', '/api/v1/me/settings')).toHaveLength(1)
  })

  it('does not offer link checks for a view-only share', async () => {
    const ws = fakeWorkspace({ shares: [{ id: 5, ownerId: 2, folderId: 31, recipientId: 1, mode: 'view', folderTitle: 'Family' }] })
    const srv = mockServer({
      ...ws.routes,
      'GET /api/v1/folders': (c) => {
        if (c.query.get('share') === '5') return { status: 200, body: { folders: [{ id: 31, type: 'folder', title: 'Family', loaded: true, children: [{ id: 7, type: 'bookmark', title: 'Soup', url: 'https://soup.example' }] }] } }
        const route = ws.routes['GET /api/v1/folders']
        return typeof route === 'function' ? route(c) : route
      },
      'GET /api/v1/bookmarks': (c) => (c.query.get('share') === '5'
        ? { status: 200, body: { items: [{ id: 7, url: 'https://soup.example', title: 'Soup', tags: [], folders: [31], favorite: false }], page: 0, limit: 200, hasMore: false } }
        : { status: 200, body: { items: [], page: 0, limit: 200, hasMore: false } }),
    })
    render(<App />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /Family/ }))
    await user.click(await screen.findByRole('treeitem', { name: 'Family' }))
    await screen.findByText('Soup', { selector: '.bm-title' })
    expect(screen.queryByRole('button', { name: /Check links/ })).not.toBeInTheDocument()
    expect(srv.find('POST', '/api/v1/link-checks')).toHaveLength(0)
  })
})
