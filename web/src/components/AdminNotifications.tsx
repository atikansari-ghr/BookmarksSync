import { useEffect, useState, type FormEvent } from 'react'
import { ApiError } from '../api'
import { useApp } from '../context'

type Settings = {
  smtp: { host: string; port: number; tlsMode: 'tls' | 'starttls'; from: string; recipient: string; configured: boolean; authenticationConfigured: boolean }
  telegram: { configured: boolean; chatConfigured: boolean }
  adminAlerts: { backupSuccess: boolean; backupFailure: boolean; security: boolean }
  deliveryFailures: number
  userDeliveryFailures: number
}

const empty: Settings = {
  smtp: { host: '', port: 587, tlsMode: 'starttls', from: '', recipient: '', configured: false, authenticationConfigured: false },
  telegram: { configured: false, chatConfigured: false },
  adminAlerts: { backupSuccess: false, backupFailure: false, security: false },
  deliveryFailures: 0,
  userDeliveryFailures: 0,
}

export function AdminNotifications() {
  const { client } = useApp()
  const [settings, setSettings] = useState(empty)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [botToken, setBotToken] = useState('')
  const [chatId, setChatId] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  async function load() {
    try { setSettings(await client.request<Settings>('GET', '/api/v1/admin/notifications')) }
    catch (e) { setError(e instanceof ApiError ? e.message : 'Notification settings could not be loaded.') }
  }
  useEffect(() => { void load() }, [client])

  async function save(e: FormEvent) {
    e.preventDefault(); setBusy(true); setError(''); setNotice('')
    try {
      const result = await client.request<Settings>('PUT', '/api/v1/admin/notifications', {
        smtp: { host: settings.smtp.host, port: Number(settings.smtp.port), tlsMode: settings.smtp.tlsMode, username, password, from: settings.smtp.from, recipient: settings.smtp.recipient },
        telegram: { botToken, chatId },
        adminAlerts: settings.adminAlerts,
      })
      setSettings(result); setPassword(''); setBotToken(''); setUsername(''); setChatId('')
      setNotice('Notification provider settings saved. Secret fields were cleared from this form.')
    } catch (e) { setError(e instanceof ApiError ? e.message : 'Notification settings could not be saved.') }
    finally { setBusy(false) }
  }

  async function test(channel: 'smtp' | 'telegram') {
    setBusy(true); setError(''); setNotice('')
    try { await client.request('POST', '/api/v1/admin/notifications/test', { channel }); setNotice(`${channel === 'smtp' ? 'Email' : 'Telegram'} test notification sent.`) }
    catch (e) { setError(e instanceof ApiError ? e.message : 'Test notification could not be sent.') }
    finally { setBusy(false) }
  }

  async function clear(channel: 'smtp' | 'telegram') {
    if (!window.confirm(`Remove the saved ${channel === 'smtp' ? 'SMTP settings and credentials' : 'Telegram bot and chat settings'}?`)) return
    setBusy(true); setError(''); setNotice('')
    try {
      const update = channel === 'smtp' ? { smtp: { clear: true } } : { telegram: { clear: true } }
      setSettings(await client.request<Settings>('PUT', '/api/v1/admin/notifications', update))
      setNotice(`${channel === 'smtp' ? 'SMTP' : 'Telegram'} settings cleared.`)
    } catch (e) { setError(e instanceof ApiError ? e.message : 'Notification settings could not be cleared.') }
    finally { setBusy(false) }
  }

  return <section className="ws-card glass admin-section" aria-labelledby="notifications-title">
    <div><h2 id="notifications-title">Notifications</h2><p className="muted small">Configure optional email and Telegram delivery. Credentials are stored in the private server configuration and included in full-system backups.</p></div>
    {error && <p className="error" role="alert">{error}</p>}{notice && <p className="success" role="status">{notice}</p>}
    <form className="stack" onSubmit={(e) => void save(e)}>
      <h3>Email (SMTP)</h3>
      <p className="muted small">{settings.smtp.configured ? 'SMTP is configured.' : 'SMTP is not configured.'} Remote servers must use TLS. Leave the password empty to keep the saved password.</p>
      <div className="admin-brand-grid">
        <label className="field"><span>SMTP server</span><input autoComplete="off" maxLength={253} value={settings.smtp.host} onChange={(e) => setSettings({ ...settings, smtp: { ...settings.smtp, host: e.target.value } })} placeholder="smtp.example.com" /></label>
        <label className="field"><span>Port</span><input type="number" min={1} max={65535} value={settings.smtp.port || 587} onChange={(e) => setSettings({ ...settings, smtp: { ...settings.smtp, port: Number(e.target.value) } })} /></label>
        <label className="field"><span>Transport security</span><select value={settings.smtp.tlsMode || 'starttls'} onChange={(e) => setSettings({ ...settings, smtp: { ...settings.smtp, tlsMode: e.target.value as 'tls' | 'starttls' } })}><option value="starttls">STARTTLS</option><option value="tls">Implicit TLS</option></select></label>
        <label className="field"><span>Sender address</span><input type="email" maxLength={320} autoComplete="off" value={settings.smtp.from} onChange={(e) => setSettings({ ...settings, smtp: { ...settings.smtp, from: e.target.value } })} placeholder="bookmarks@example.com" /></label>
        <label className="field"><span>Administrator alert recipient</span><input type="email" maxLength={320} autoComplete="off" value={settings.smtp.recipient} onChange={(e) => setSettings({ ...settings, smtp: { ...settings.smtp, recipient: e.target.value } })} placeholder="admin@example.com" /></label>
        <label className="field"><span>SMTP username</span><input autoComplete="off" value={username} onChange={(e) => setUsername(e.target.value)} placeholder={settings.smtp.authenticationConfigured ? 'Saved; leave blank to keep' : 'Optional'} /></label>
        <label className="field"><span>SMTP password</span><input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder={settings.smtp.authenticationConfigured ? 'Saved; leave blank to keep' : 'Optional'} /></label>
      </div>
      <div className="actions"><button className="btn ghost" type="button" disabled={busy || !settings.smtp.configured} onClick={() => void test('smtp')}>Send email test</button><button className="btn ghost" type="button" disabled={busy || !settings.smtp.configured} onClick={() => void clear('smtp')}>Clear SMTP settings</button></div>

      <h3>Telegram</h3>
      <p className="muted small">Create a bot with BotFather, enter its token and your chat ID, then send a test message. The token is never shown again after saving.</p>
      <div className="admin-brand-grid">
        <label className="field"><span>Bot token</span><input type="password" autoComplete="new-password" value={botToken} onChange={(e) => setBotToken(e.target.value)} placeholder={settings.telegram.configured ? 'Saved; leave blank to keep' : '123456:bot-token'} /></label>
        <label className="field"><span>Administrator chat ID</span><input inputMode="numeric" autoComplete="off" value={chatId} onChange={(e) => setChatId(e.target.value)} placeholder={settings.telegram.chatConfigured ? 'Saved; leave blank to keep' : 'Your Telegram chat ID'} /></label>
      </div>
      <div className="actions"><button className="btn ghost" type="button" disabled={busy || !settings.telegram.configured || !settings.telegram.chatConfigured} onClick={() => void test('telegram')}>Send Telegram test</button><button className="btn ghost" type="button" disabled={busy || !settings.telegram.configured} onClick={() => void clear('telegram')}>Clear Telegram settings</button><button className="btn primary" type="submit" disabled={busy}>Save notification settings</button></div>
    </form>
    <div className="stack">
      <h3>Administrator alerts</h3>
      <p className="muted small">Alerts are sent only to configured administrator email and Telegram destinations. Messages contain event summaries, never bookmark titles, URLs, sign-in codes, or recovery secrets.</p>
      <label className="check"><input type="checkbox" checked={settings.adminAlerts.backupSuccess} onChange={(e) => setSettings({ ...settings, adminAlerts: { ...settings.adminAlerts, backupSuccess: e.target.checked } })} />Successful backups</label>
      <label className="check"><input type="checkbox" checked={settings.adminAlerts.backupFailure} onChange={(e) => setSettings({ ...settings, adminAlerts: { ...settings.adminAlerts, backupFailure: e.target.checked } })} />Failed backups</label>
      <label className="check"><input type="checkbox" checked={settings.adminAlerts.security} onChange={(e) => setSettings({ ...settings, adminAlerts: { ...settings.adminAlerts, security: e.target.checked } })} />Account and security administration changes</label>
      <button className="btn primary" type="button" disabled={busy} onClick={() => void client.request('PUT', '/api/v1/admin/notifications', { adminAlerts: settings.adminAlerts }).then(() => setNotice('Administrator alert preferences saved.')).catch((e: unknown) => setError(e instanceof ApiError ? e.message : 'Alert preferences could not be saved.'))}>Save alert preferences</button>
    </div>
    <p className="muted small">Saving provider credentials does not enable user notifications; users must verify a channel and opt into the alert categories they want.</p>
    <p className="muted small" role="status">Notification delivery failures since restart: {settings.deliveryFailures + settings.userDeliveryFailures}</p>
  </section>
}
