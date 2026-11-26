import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it } from 'vitest'
import { App } from '../App'
import { err, go, mockServer, sessionState } from './server'

const flow = 'a'.repeat(43)

describe('client connection consent', () => {
  beforeEach(() => { go(`/auth/connect?flow=${flow}`) })

  it('shows the account and private edit permission before explicit approval', async () => {
    const api = mockServer({
      'GET /api/v1/auth/session': { status: 200, body: sessionState() },
      'GET /api/v1/me/settings': { status: 200, body: { settings: {} } },
      'GET /api/v1/auth/connect': { status: 200, body: { status: 'pending' } },
      'POST /api/v1/auth/connect/approve': { status: 200, body: { status: 'approved' } },
    })
    render(<App />)
    const user = userEvent.setup()
    const approve = await screen.findByRole('button', { name: 'Approve connection' })
    expect(screen.getByText(/read, create, change and delete/)).toBeInTheDocument()
    expect(api.find('POST', '/api/v1/auth/connect/approve')).toHaveLength(0)
    await user.type(screen.getByLabelText('Connection name'), '  BookmarksSync-Home-Firefox  ')
    await user.click(approve)
    expect(await screen.findByRole('heading', { name: 'Connection approved' })).toBeInTheDocument()
    expect(api.find('POST', '/api/v1/auth/connect/approve')[0].body).toEqual({ flow, sessionId: 7, label: 'BookmarksSync-Home-Firefox' })
    expect(api.find('GET', '/api/v1/auth/connect')).toHaveLength(1)
  })

  it('asks for the Floccus profile name honestly and refuses a blank name', async () => {
    const api = mockServer({
      'GET /api/v1/auth/session': { status: 200, body: sessionState() },
      'GET /api/v1/me/settings': { status: 200, body: { settings: {} } },
      'GET /api/v1/auth/connect': { status: 200, body: { status: 'pending' } },
      'POST /api/v1/auth/connect/approve': { status: 200, body: { status: 'approved' } },
    })
    render(<App />)
    const user = userEvent.setup()
    const approve = await screen.findByRole('button', { name: 'Approve connection' })
    const name = screen.getByLabelText('Connection name') as HTMLInputElement
    expect(name.value).toBe('')
    expect(name).toBeRequired()
    expect(name.maxLength).toBe(200)
    expect(name).toHaveAccessibleDescription('Use the same name as your Floccus profile. Floccus does not send its label to the server.')
    await user.click(approve)
    expect(await screen.findByRole('alert')).toHaveTextContent('Enter a connection name')
    await user.type(name, '   ')
    await user.click(approve)
    expect(api.find('POST', '/api/v1/auth/connect/approve')).toHaveLength(0)
    expect(screen.queryByRole('heading', { name: 'Connection approved' })).not.toBeInTheDocument()
  })

  it('keeps the typed name and shows the error when approval fails', async () => {
    mockServer({
      'GET /api/v1/auth/session': { status: 200, body: sessionState() },
      'GET /api/v1/me/settings': { status: 200, body: { settings: {} } },
      'GET /api/v1/auth/connect': { status: 200, body: { status: 'pending' } },
      'POST /api/v1/auth/connect/approve': err(500, 'internal', 'boom'),
    })
    render(<App />)
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Connection name'), 'Work laptop')
    await user.click(screen.getByRole('button', { name: 'Approve connection' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('could not be approved')
    expect((screen.getByLabelText('Connection name') as HTMLInputElement).value).toBe('Work laptop')
  })

  it('continues password login to consent without approving automatically', async () => {
    const state = sessionState()
    const api = mockServer({
      'GET /api/v1/auth/session': err(401, 'unauthenticated', 'Sign in'),
      'GET /api/v1/auth/oidc/providers': { status: 200, body: { providers: [] } },
      'POST /api/v1/auth/login': { status: 200, body: state },
      'GET /api/v1/me/settings': { status: 200, body: { settings: {} } },
      'GET /api/v1/auth/connect': { status: 200, body: { status: 'pending' } },
    })
    render(<App />)
    const user = userEvent.setup()
    await user.type(await screen.findByLabelText('Username'), 'alice')
    await user.type(screen.getByLabelText('Password', { exact: true }), 'correct horse battery')
    await user.click(screen.getByRole('button', { name: /Sign in/ }))
    expect(await screen.findByRole('button', { name: 'Approve connection' })).toBeInTheDocument()
    expect(api.find('POST', '/api/v1/auth/connect/approve')).toHaveLength(0)
    expect(window.location.pathname).toBe('/auth/connect')
  })

  it('rejects expired links without showing an approval button', async () => {
    mockServer({
      'GET /api/v1/auth/session': { status: 200, body: sessionState() },
      'GET /api/v1/me/settings': { status: 200, body: { settings: {} } },
      'GET /api/v1/auth/connect': err(404, 'not_found', 'Expired'),
    })
    render(<App />)
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('expired'))
    expect(screen.queryByRole('button', { name: 'Approve connection' })).not.toBeInTheDocument()
  })
})
