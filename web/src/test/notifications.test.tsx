import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, beforeEach } from 'vitest'
import { App } from '../App'
import { go, mockServer, sessionState } from './server'

describe('account notification settings', () => {
  beforeEach(() => go('/auth/security'))

  it('verifies a channel and saves opt-in preferences', async () => {
    const initial = { channels: [], securityAlerts: false, sharingAlerts: false }
    const verified = { channels: [{ kind: 'email', destination: 'alice@example.test', verifiedAt: '2026-11-23T12:00:00Z' }], securityAlerts: false, sharingAlerts: false }
    const srv = mockServer({
      'GET /api/v1/auth/session': { status: 200, body: sessionState() },
      'GET /api/v1/auth/oidc/providers': { status: 200, body: { providers: [] } },
      'GET /api/v1/me/notifications': { status: 200, body: initial },
      'POST /api/v1/me/notifications/bindings': { status: 202, body: { status: 'verification_sent' } },
      'POST /api/v1/me/notifications/bindings/confirm': { status: 200, body: verified },
      'PATCH /api/v1/me/notifications': { status: 200, body: { ...verified, securityAlerts: true } },
    })
    render(<App />)
    const user = userEvent.setup()
    await screen.findByRole('heading', { name: 'Security and sharing alerts' })
    await user.type(await screen.findByLabelText('Email address'), 'alice@example.test')
    await user.click(screen.getByRole('button', { name: 'Send verification code' }))
    expect(srv.find('POST', '/api/v1/me/notifications/bindings')[0].body).toEqual({ kind: 'email', destination: 'alice@example.test' })
    await user.type(await screen.findByLabelText('8-digit verification code'), '12345678')
    await user.click(screen.getByRole('button', { name: 'Verify channel' }))
    expect(await screen.findByText(/alice@example.test/)).toBeInTheDocument()
    await user.click(screen.getByRole('checkbox', { name: 'Send account security alerts' }))
    await waitFor(() => expect(srv.find('PATCH', '/api/v1/me/notifications')).toHaveLength(1))
    expect(srv.find('PATCH', '/api/v1/me/notifications')[0].body).toEqual({ securityAlerts: true, sharingAlerts: false })
  })
})
