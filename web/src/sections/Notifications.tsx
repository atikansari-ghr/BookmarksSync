import { useEffect, useState } from 'react'
import { useApp } from '../context'

type Channel = { kind: 'email' | 'telegram'; destination: string; verifiedAt: string }
type Settings = { channels: Channel[]; securityAlerts: boolean; sharingAlerts: boolean }

export function NotificationsSection({ notify }: { notify: (message: string) => void }) {
  const { client } = useApp()
  const [settings, setSettings] = useState<Settings | null>(null)
  const [kind, setKind] = useState<'email' | 'telegram'>('email')
  const [destination, setDestination] = useState('')
  const [code, setCode] = useState('')
  const [pending, setPending] = useState(false)
  const [busy, setBusy] = useState(false)

  async function refresh() {
    setSettings(await client.request<Settings>('GET', '/api/v1/me/notifications'))
  }
  useEffect(() => { void refresh().catch(() => notify('Notification settings could not be loaded.')) }, [client, notify])

  async function begin() {
    setBusy(true)
    try {
      await client.request('POST', '/api/v1/me/notifications/bindings', { kind, destination })
      setPending(true)
      notify(`A verification code was sent to your ${kind === 'email' ? 'email address' : 'Telegram chat'}.`)
    } catch { notify('Verification could not be started. Check the address or chat ID and try again.') }
    finally { setBusy(false) }
  }
  async function confirm() {
    setBusy(true)
    try {
      setSettings(await client.request<Settings>('POST', '/api/v1/me/notifications/bindings/confirm', { kind, code }))
      setPending(false); setCode(''); setDestination('')
      notify('Notification channel verified.')
    } catch { notify('That verification code is invalid or expired.') }
    finally { setBusy(false) }
  }
  async function savePreferences(next: Pick<Settings, 'securityAlerts' | 'sharingAlerts'>) {
    try { setSettings(await client.request<Settings>('PATCH', '/api/v1/me/notifications', next)) }
    catch { notify('Notification preferences could not be saved.') }
  }
  async function remove(channel: Channel) {
    try { await client.request('DELETE', `/api/v1/me/notifications/bindings/${channel.kind}`); await refresh(); notify('Notification channel removed.') }
    catch { notify('Notification channel could not be removed.') }
  }

  return <section className="card settings-section" aria-labelledby="notification-settings-title">
    <h2 id="notification-settings-title">Security and sharing alerts</h2>
    <p className="muted">Add a verified email address or Telegram chat. Alerts are optional and never include bookmark names or links.</p>
    {settings === null ? <p className="muted" aria-busy="true">Loading notification settings…</p> : <>
      <ul className="connection-list">
        {settings.channels.map((channel) => <li key={channel.kind}>
          <span><strong>{channel.kind === 'email' ? 'Email' : 'Telegram'}</strong> · {channel.destination}</span>
          <button type="button" className="btn ghost" onClick={() => void remove(channel)}>Remove</button>
        </li>)}
      </ul>
      {!pending ? <div className="form-grid">
        <label>Channel<select value={kind} onChange={(e) => setKind(e.target.value as 'email' | 'telegram')}><option value="email">Email</option><option value="telegram">Telegram</option></select></label>
        <label>{kind === 'email' ? 'Email address' : 'Telegram chat ID'}<input value={destination} onChange={(e) => setDestination(e.target.value)} autoComplete="off" /></label>
        <button type="button" className="btn primary" disabled={busy || !destination} onClick={() => void begin()}>Send verification code</button>
      </div> : <div className="form-grid">
        <label>8-digit verification code<input inputMode="numeric" autoComplete="one-time-code" maxLength={8} value={code} onChange={(e) => setCode(e.target.value)} /></label>
        <button type="button" className="btn primary" disabled={busy || code.length !== 8} onClick={() => void confirm()}>Verify channel</button>
        <button type="button" className="btn ghost" disabled={busy} onClick={() => setPending(false)}>Cancel</button>
      </div>}
      <label className="check-row"><input type="checkbox" checked={settings.securityAlerts} onChange={(e) => void savePreferences({ securityAlerts: e.target.checked, sharingAlerts: settings.sharingAlerts })} /> Send account security alerts</label>
      <label className="check-row"><input type="checkbox" checked={settings.sharingAlerts} onChange={(e) => void savePreferences({ securityAlerts: settings.securityAlerts, sharingAlerts: e.target.checked })} /> Send sharing alerts</label>
      <p className="muted">Preferences start off. You can remove a channel or turn off alerts at any time.</p>
    </>}
  </section>
}
