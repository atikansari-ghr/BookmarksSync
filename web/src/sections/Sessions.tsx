import { useState } from 'react'
import { describe, type SessionInfo } from '../api'
import { useApp } from '../context'
import { Busy, Section, formatTime } from '../components/common'
import type { Notify } from '../pages/SecurityPage'
import { useLoad } from './useLoad'

/** A readable "Browser on OS" summary of a user-agent label; other labels pass through. */
export function deviceName(label: string): string {
  if (!label) return 'Unknown browser'
  const browser = /Edg\//.test(label) ? 'Edge' : /Firefox\//.test(label) ? 'Firefox' : /Chrome\//.test(label) ? 'Chrome' : /Version\/[\d.]+ .*Safari\//.test(label) ? 'Safari' : ''
  if (!browser) return label
  const os = /Windows/.test(label) ? 'Windows' : /Android/.test(label) ? 'Android' : /iPhone|iPad/.test(label) ? 'iOS' : /Mac OS X/.test(label) ? 'macOS' : /Linux/.test(label) ? 'Linux' : ''
  return os ? `${browser} on ${os}` : browser
}

export function SessionsSection({ notify }: { notify: Notify }) {
  const { client } = useApp()
  const [load, reload] = useLoad(() => client.request<{ sessions: SessionInfo[] }>('GET', '/api/v1/auth/sessions').then((d) => d.sessions ?? []))
  const [busy, setBusy] = useState(0)
  const [error, setError] = useState('')

  async function revoke(s: SessionInfo) {
    setError('')
    setBusy(s.id)
    try {
      await client.request('DELETE', `/api/v1/auth/sessions/${s.id}`)
      notify('Session signed out.')
      await reload()
    } catch (err) {
      setError(describe(err, 'That session could not be signed out.'))
    } finally {
      setBusy(0)
    }
  }

  return (
    <Section title="Active sessions" intro="Browsers where you are signed in. Sign out any you do not recognise.">
      {error && <p className="error" role="alert">{error}</p>}
      {load.state === 'loading' && <Busy />}
      {load.state === 'error' && <p className="error" role="alert">Sessions could not be loaded.</p>}
      {load.state === 'ready' && (
        <ul className="list">
          {load.data.map((s) => (
            <li key={s.id} className="list-item">
              <div>
                <p className="item-title">
                  {deviceName(s.label)}
                  {s.current && <span className="badge">This device</span>}
                </p>
                <p className="muted small">Last active {formatTime(s.lastSeenAt)} · signed in {formatTime(s.createdAt)}</p>
              </div>
              {!s.current && (
                <button type="button" className="btn ghost small" disabled={busy === s.id} aria-label={`Sign out ${deviceName(s.label)}`} title={s.label} onClick={() => revoke(s)}>
                  Sign out
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </Section>
  )
}
