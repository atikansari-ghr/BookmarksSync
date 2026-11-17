import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import { err, go, mockServer, sessionState } from './server'

const base = {
  'GET /api/v1/version': { status: 200, body: { version: '1.0.0' } },
  'GET /api/v1/auth/session': err(401, 'unauthenticated'),
  'GET /api/v1/auth/oidc/providers': { status: 200, body: { providers: [{ id: 'authentik', name: 'Authentik' }] } },
  'GET /api/v1/auth/sessions': { status: 200, body: { sessions: [] } },
}

describe('OIDC completion page', () => {
  it('removes the second-factor handle from the address bar before anything else and completes TOTP', async () => {
    go('/auth/oidc/complete?result=second_factor_required#flowId=oidc-flow-handle')
    const replace = vi.spyOn(window.history, 'replaceState')
    const srv = mockServer({ ...base, 'POST /api/v1/auth/login/second-factor': { status: 200, body: sessionState() } })
    render(<App />)
    expect(await screen.findByRole('heading', { name: 'Two-step verification' })).toBeInTheDocument()
    expect(window.location.hash).toBe('')
    expect(window.location.href).not.toContain('oidc-flow-handle')
    expect(replace).toHaveBeenCalled()
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('Authentication code'), '654321')
    await user.click(screen.getByRole('button', { name: 'Verify' }))
    await waitFor(() => expect(window.location.pathname).toBe('/auth/security'))
    expect(srv.find('POST', '/api/v1/auth/login/second-factor')[0].body).toEqual({ flowId: 'oidc-flow-handle', code: '654321' })
  })

  it('opens the security page after a provider sign-in', async () => {
    go('/auth/oidc/complete?result=signed_in')
    mockServer({ ...base, 'GET /api/v1/auth/session': { status: 200, body: sessionState() } })
    render(<App />)
    await waitFor(() => expect(window.location.pathname).toBe('/auth/security'))
  })

  it('confirms a newly linked account on the security page', async () => {
    go('/auth/oidc/complete?result=linked')
    mockServer({ ...base, 'GET /api/v1/auth/session': { status: 200, body: sessionState() } })
    render(<App />)
    await waitFor(() => expect(window.location.pathname).toBe('/auth/security'))
    expect(await screen.findByRole('status')).toHaveTextContent('Account linked.')
  })

  it.each([
    ['not_linked', /not linked to a BookmarksSync account/],
    ['access_denied', /cancelled/],
    ['expired', /expired/],
    ['already_linked', /already linked/],
    ['unavailable', /unavailable/],
    ['invalid_request', /could not be completed/],
    ['something_new', /could not be completed/],
  ])('explains error %s with a way back to sign in', async (code, text) => {
    go(`/auth/oidc/complete?error=${code}`)
    mockServer(base)
    render(<App />)
    expect(await screen.findByRole('alert')).toHaveTextContent(text)
    expect(screen.getByRole('link', { name: 'Back to sign in' })).toHaveAttribute('href', '/auth/login')
    expect(document.body.textContent).not.toContain('something_new<')
  })
})
