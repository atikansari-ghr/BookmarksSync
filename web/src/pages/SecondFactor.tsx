import { signInDestination } from '../connection'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { ApiError, describe, type SessionState } from '../api'
import { useApp } from '../context'

/**
 * Completes a pending password or provider sign-in with a TOTP code or a recovery
 * code. The flow handle stays in memory only.
 */
export function SecondFactorForm({ flowId, onBack }: { flowId: string; onBack?: () => void }) {
  const { client, navigate, setSession } = useApp()
  const [recovery, setRecovery] = useState(false)
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const input = useRef<HTMLInputElement>(null)

  useEffect(() => {
    input.current?.focus()
  }, [recovery])

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      const body = recovery ? { flowId, recoveryCode: value.trim() } : { flowId, code: value.trim() }
      const state = await client.request<SessionState>('POST', '/api/v1/auth/login/second-factor', body, { reauth: false })
      setSession(state)
      navigate(await signInDestination(), { replace: true })
    } catch (err) {
      setValue('')
      if (err instanceof ApiError && err.status === 401) {
        setError(recovery ? 'That recovery code did not work.' : 'That code did not work.')
      } else {
        setError(describe(err))
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <form className="stack" onSubmit={submit} noValidate>
      <h1>Two-step verification</h1>
      <p className="muted">
        {recovery ? 'Enter one of your saved recovery codes. Each code works once.' : 'Enter the 6-digit code from your authenticator app.'}
      </p>
      {error && <p className="error" role="alert">{error}</p>}
      <label className="field">
        <span>{recovery ? 'Recovery code' : 'Authentication code'}</span>
        <input
          ref={input}
          name={recovery ? 'recoveryCode' : 'code'}
          autoComplete={recovery ? 'off' : 'one-time-code'}
          inputMode={recovery ? 'text' : 'numeric'}
          spellCheck={false}
          maxLength={recovery ? 32 : 6}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          required
        />
      </label>
      <button type="submit" className="btn primary block" disabled={busy || value.trim() === ''}>
        {busy ? 'Verifying…' : 'Verify'}
      </button>
      <button type="button" className="link-button" onClick={() => { setRecovery(!recovery); setValue(''); setError('') }}>
        {recovery ? 'Use your authenticator app instead' : 'Use a recovery code instead'}
      </button>
      {onBack ? (
        <button type="button" className="link-button" onClick={onBack}>Back to sign in</button>
      ) : null}
    </form>
  )
}
