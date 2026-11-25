import { signInDestination } from '../connection'
import { useEffect, useState } from 'react'
import { useApp } from '../context'
import { AuthLayout } from './LoginPage'
import { SecondFactorForm } from './SecondFactor'

const messages: Record<string, string> = {
  not_linked: 'This provider account is not linked to a BookmarksSync account. Sign in with your password, then link it from your account security page.',
  access_denied: 'Sign-in with the provider was cancelled.',
  expired: 'The sign-in request expired or was already used. Please start again.',
  already_linked: 'That provider account is already linked to an account.',
  unavailable: 'The sign-in provider is unavailable right now. Try again later.',
  rate_limited: 'Too many verification attempts for this account. Try again later.',
}
const generic = 'Sign-in with the provider could not be completed. Please start again.'

/**
 * Reads the provider callback result once. A pending second-factor handle arrives
 * only in the URL fragment; it is removed from the address bar immediately and kept
 * in memory.
 */
function takeResult(): { result: string; error: string; flowId: string } {
  const params = new URLSearchParams(window.location.search)
  const fragment = new URLSearchParams(window.location.hash.replace(/^#/, ''))
  const flowId = fragment.get('flowId') ?? ''
  if (window.location.hash) {
    window.history.replaceState(null, '', window.location.pathname + window.location.search)
  }
  return { result: params.get('result') ?? '', error: params.get('error') ?? '', flowId }
}

export function OidcComplete() {
  const { navigate } = useApp()
  const [outcome] = useState(takeResult)

  useEffect(() => {
    if (outcome.result === 'signed_in') void signInDestination().then(to => navigate(to, { replace: true }))
    else if (outcome.result === 'linked') navigate('/auth/security', { replace: true, notice: 'Account linked.' })
  }, [outcome, navigate])

  if (outcome.result === 'second_factor_required' && outcome.flowId) {
    return (
      <AuthLayout>
        <SecondFactorForm flowId={outcome.flowId} />
      </AuthLayout>
    )
  }
  if (outcome.result === 'signed_in' || outcome.result === 'linked') {
    return (
      <AuthLayout>
        <p className="muted" aria-busy="true">Finishing sign-in…</p>
      </AuthLayout>
    )
  }
  const text = messages[outcome.error] ?? generic
  return (
    <AuthLayout>
      <h1>Sign-in not completed</h1>
      <p className="error" role="alert">{text}</p>
      <a className="btn primary block" href="/auth/login">Back to sign in</a>
    </AuthLayout>
  )
}
