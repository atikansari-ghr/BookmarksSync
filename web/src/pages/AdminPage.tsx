import { useEffect, useState, type FormEvent } from 'react'
import { ApiError } from '../api'
import { Busy } from '../components/common'
import { Workspace } from '../components/Workspace'
import { AdminBackups } from '../components/AdminBackups'
import { useApp } from '../context'

type AdminUser = { id: number; username: string; active: boolean; isAdmin: boolean; bookmarkCount: number; folderCount: number; storageBytes: number; connectionCount: number; themeDefault?: string | null }
type Health = { cpuPercent: number | null; memory: { totalBytes: number; usedBytes: number; percent: number } | null; disk: { totalBytes: number; usedBytes: number; percent: number } | null; uptimeSeconds: number | null; unavailable: string[]; dbReady: boolean; syncErrors: number }
type Activity = { id: number; action: string; targetType: string; targetId: number | null; createdAt: string }
type Branding = Record<string, string>
type NetworkAccess = { mode: 'allow' | 'block'; countries: string[]; geoDatabaseAvailable: boolean; geoDatabaseBuildTime: string | null; trustedProxyCount: number }

const defaults: Branding = {
  'branding.app-title': 'BookmarksSync',
  'branding.signin-title': 'Personal Bookmarks Server',
  'branding.tagline': 'Your bookmarks. Your browsers. Your control.',
  'branding.supporting-line': 'A self-hosted home for your bookmarks.',
  'branding.primary-color': '#0f766e',
  'branding.accent-color': '#14b8a6',
  'branding.background-color': '#e6f4fb',
  'branding.login-layout': 'split',
}

export function AdminPage() {
  return <Workspace current="admin" title="Administration" subtitle="Manage accounts, server health, and login appearance."><Admin /></Workspace>
}

function Admin() {
  const { client, presentation, setPresentation } = useApp()
  const [users, setUsers] = useState<AdminUser[]>([])
  const [health, setHealth] = useState<Health | null>(null)
  const [activity, setActivity] = useState<Activity[]>([])
  const [branding, setBranding] = useState<Branding>(defaults)
  const [savedBranding, setSavedBranding] = useState<Branding>({})
  const [networkAccess, setNetworkAccess] = useState<NetworkAccess | null>(null)
  const [networkError, setNetworkError] = useState('')
  const [countryText, setCountryText] = useState('IN, SA')
  const [userDefaults, setUserDefaults] = useState<Record<number, string>>({})
  const [busy, setBusy] = useState(true)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  useEffect(() => {
    let live = true
    async function loadAllUsers() {
      const all: AdminUser[] = []
      for (let page = 0; page < 1000; page++) {
        const result = await client.request<{ users: AdminUser[]; hasMore: boolean }>('GET', `/api/v1/admin/users?page=${page}&limit=200`)
        all.push(...(result.users ?? []))
        if (!result.hasMore) return all
      }
      throw new Error('The account list exceeds the supported page count.')
    }
    void client.request<NetworkAccess>('GET', '/api/v1/admin/network-access').then((n) => {
      if (!live) return
      setNetworkAccess(n)
      setCountryText((n.countries ?? []).join(', '))
    }).catch(() => {
      if (live) setNetworkError('Country access status could not be loaded. Reload the page to try again.')
    })
    void Promise.all([
      loadAllUsers(),
      client.request<Health>('GET', '/api/v1/admin/health'),
      client.request<{ items: Activity[] }>('GET', '/api/v1/admin/activity'),
      client.request<{ settings: Branding }>('GET', '/api/v1/admin/settings'),
    ]).then(([u, h, a, b]) => {
      if (!live) return
      setUsers(u)
      setUserDefaults(Object.fromEntries(u.map((user) => [user.id, user.themeDefault ?? ''])))
      setHealth(h)
      setActivity(a.items ?? [])
      setBranding({ ...defaults, ...(b.settings ?? {}) })
      setSavedBranding(b.settings ?? {})
    }).catch((e: unknown) => {
      if (live) setError(e instanceof ApiError ? e.message : 'Administration data could not be loaded.')
    }).finally(() => { if (live) setBusy(false) })
    return () => { live = false }
  }, [client])

  function setBrand(key: string, value: string) { setBranding((prev) => ({ ...prev, [key]: value })); setNotice('') }
  async function saveBranding(e: FormEvent) {
    e.preventDefault()
    setError(''); setNotice('')
    const changes: Record<string, string | null> = {}
    for (const [key, value] of Object.entries(branding)) {
      if (value === defaults[key]) {
        if (savedBranding[key] !== undefined) changes[key] = null
      } else if (savedBranding[key] !== value) {
        changes[key] = value
      }
    }
    if (!Object.keys(changes).length) { setNotice('Branding is already saved.'); return }
    try {
      const result = await client.request<{ settings: Branding }>('PATCH', '/api/v1/admin/settings', changes)
      const saved = result.settings ?? {}
      const shown = { ...defaults, ...saved }
      setSavedBranding(saved)
      setBranding(shown)
      setPresentation({ ...presentation, appTitle: shown['branding.app-title'], signinTitle: shown['branding.signin-title'], tagline: shown['branding.tagline'], supportingLine: shown['branding.supporting-line'], primaryColor: shown['branding.primary-color'], accentColor: shown['branding.accent-color'], backgroundColor: shown['branding.background-color'], loginLayout: shown['branding.login-layout'] as 'split' | 'centered' | 'plain' })
      setNotice('Login appearance saved.')
    } catch (e) { setError(e instanceof ApiError ? e.message : 'Branding could not be saved.') }
  }
  async function setThemeDefault(user: AdminUser, theme: string) {
    const previous = userDefaults[user.id] ?? ''
    setError(''); setNotice('')
    setUserDefaults((prev) => ({ ...prev, [user.id]: theme }))
    try {
      await client.request('PATCH', `/api/v1/admin/users/${user.id}/settings`, { 'ui.theme': theme || null })
      setNotice(theme ? `Theme default saved for ${user.username}.` : `Theme default cleared for ${user.username}.`)
    } catch (e) {
      setUserDefaults((current) => current[user.id] === theme ? { ...current, [user.id]: previous } : current)
      setError(e instanceof ApiError ? e.message : 'User default could not be saved.')
    }
  }
  async function uploadAsset(kind: 'logo' | 'background', file?: File) {
    if (!file) return
    setError(''); setNotice('')
    const form = new FormData()
    form.set('kind', kind)
    form.set('asset', file)
    try {
      const result = await client.uploadForm<{ url: string }>(`/api/v1/admin/settings/assets`, form)
      setPresentation({ ...presentation, [kind === 'logo' ? 'logoUrl' : 'backgroundUrl']: result.url })
      setNotice(`${kind === 'logo' ? 'Logo' : 'Background'} uploaded.`)
    } catch (e) { setError(e instanceof ApiError ? e.message : 'Branding image could not be uploaded.') }
  }
  async function saveNetworkAccess(e: FormEvent) {
    e.preventDefault(); setError(''); setNotice('')
    const countries = countryText.split(',').map((code) => code.trim().toUpperCase()).filter(Boolean)
    try {
      const result = await client.request<NetworkAccess>('PATCH', '/api/v1/admin/network-access', { mode: networkAccess?.mode ?? 'allow', countries })
      setNetworkAccess(result); setCountryText(result.countries.join(', ')); setNotice('Network access policy saved.')
    } catch (e) { setError(e instanceof ApiError ? e.message : 'Network access policy could not be saved.') }
  }

  return <>
    {error && <p className="error" role="alert">{error}</p>}
    {notice && <p className="success" role="status">{notice}</p>}
    {busy ? <Busy label="Loading administration" /> : <>
      <section className="ws-card glass admin-section" aria-labelledby="health-title">
        <div className="admin-section-head"><div><h2 id="health-title">Server health</h2><p className="muted small">Host diagnostics. Bookmark titles and addresses are never shown here.</p></div><span className={health?.dbReady ? 'health-pill ok' : 'health-pill bad'}>{health?.dbReady ? 'Database ready' : 'Database unavailable'}</span></div>
        <div className="admin-health-grid">
          <Metric label="CPU" value={health?.cpuPercent == null ? 'Unavailable' : `${Math.round(health.cpuPercent)}%`} />
          <Metric label="Memory" value={health?.memory ? `${Math.round(health.memory.percent)}% used` : 'Unavailable'} />
          <Metric label="Disk" value={health?.disk ? `${Math.round(health.disk.percent)}% used` : 'Unavailable'} />
          <Metric label="Uptime" value={health?.uptimeSeconds == null ? 'Unavailable' : formatDuration(health.uptimeSeconds)} />
          <Metric label="Sync errors" value={`${health?.syncErrors ?? 0} server errors`} />
        </div>
        {!!health?.unavailable.length && <p className="muted small">Unavailable metrics: {health.unavailable.join(', ')}.</p>}
      </section>

      <AdminBackups />

      <section className="ws-card glass admin-section" aria-labelledby="network-access-title">
        <form className="stack" onSubmit={saveNetworkAccess}>
          <div><h2 id="network-access-title">Country access</h2><p className="muted small">Restrict visitors by country. Changes can block your current connection; keep local CLI access available for recovery.</p></div>
          {networkError && <p className="error" role="alert">{networkError}</p>}
          <div className="admin-brand-grid">
            <label className="field"><span>Country rule</span><select aria-label="Country rule" disabled={!networkAccess} value={networkAccess?.mode ?? 'allow'} onChange={(e) => setNetworkAccess((v) => v ? { ...v, mode: e.target.value as NetworkAccess['mode'] } : v)}><option value="allow">Allow only listed countries</option><option value="block">Block listed countries</option></select></label>
            <label className="field"><span>Country codes</span><input aria-label="Country codes" disabled={!networkAccess} value={countryText} onChange={(e) => setCountryText(e.target.value)} placeholder="IN, SA" /><span className="muted small">Comma-separated ISO country codes. Empty allow list blocks all public countries.</span></label>
          </div>
          <p className="muted small">GeoIP database: {networkAccess?.geoDatabaseAvailable ? `available${networkAccess.geoDatabaseBuildTime ? ` (built ${new Date(networkAccess.geoDatabaseBuildTime).toLocaleDateString()})` : ''}` : 'not available; public visitors will be denied'}. Trusted proxy entries: {networkAccess?.trustedProxyCount ?? 0}.</p>
          <div className="actions"><button className="btn primary" type="submit" disabled={!networkAccess}>Save country access</button></div>
        </form>
      </section>

      <section className="ws-card glass admin-section" aria-labelledby="accounts-title">
        <h2 id="accounts-title">Accounts</h2>
        <div className="admin-table-wrap"><table className="admin-table"><thead><tr><th>Account</th><th>Status</th><th>Bookmarks</th><th>Folders</th><th>Storage</th><th>Connections</th><th>Theme default</th></tr></thead>
          <tbody>{users.map((user) => <tr key={user.id}><th scope="row">{user.username}{user.isAdmin ? ' · Admin' : ''}</th><td>{user.active ? 'Active' : 'Suspended'}</td><td>{user.bookmarkCount}</td><td>{user.folderCount}</td><td>{formatBytes(user.storageBytes)}</td><td>{user.connectionCount}</td><td><select aria-label={`Theme default for ${user.username}`} value={userDefaults[user.id] ?? ''} onChange={(e) => void setThemeDefault(user, e.target.value)}><option value="">Use account default</option><option value="classic">Classic</option><option value="ocean-glass">Ocean Glass</option><option value="midnight-glass">Midnight Glass</option><option value="warm-glass">Warm Glass</option></select></td></tr>)}</tbody>
        </table></div>
      </section>

      <section className="ws-card glass admin-section" aria-labelledby="branding-title">
        <form className="stack" onSubmit={saveBranding}>
          <div><h2 id="branding-title">Login appearance</h2><p className="muted small">Text and colors shown on the sign-in page.</p></div>
          <div className="admin-brand-grid">
            <BrandText label="Application title" setting="branding.app-title" maxLength={80} value={branding} set={setBrand} />
            <BrandText label="Sign-in title" setting="branding.signin-title" maxLength={100} value={branding} set={setBrand} />
            <BrandText label="Tagline" setting="branding.tagline" maxLength={160} value={branding} set={setBrand} />
            <BrandText label="Supporting line" setting="branding.supporting-line" maxLength={240} value={branding} set={setBrand} />
            <BrandColor label="Primary color" setting="branding.primary-color" value={branding} set={setBrand} />
            <BrandColor label="Accent color" setting="branding.accent-color" value={branding} set={setBrand} />
            <BrandColor label="Background color" setting="branding.background-color" value={branding} set={setBrand} />
            <label className="field"><span>Login layout</span><select value={branding['branding.login-layout']} onChange={(e) => setBrand('branding.login-layout', e.target.value)}><option value="split">Split screen</option><option value="centered">Centered card</option><option value="plain">Plain</option></select></label>
            <label className="field"><span>Login logo (PNG, JPEG, or WebP; up to 2 MiB)</span><input aria-label="Login logo image" type="file" accept="image/png,image/jpeg,image/webp" onChange={(e) => void uploadAsset('logo', e.target.files?.[0])} /></label>
            <label className="field"><span>Login background image (PNG, JPEG, or WebP; up to 2 MiB)</span><input aria-label="Login background image" type="file" accept="image/png,image/jpeg,image/webp" onChange={(e) => void uploadAsset('background', e.target.files?.[0])} /></label>
          </div>
          {(presentation.logoUrl || presentation.backgroundUrl) && <div className="admin-brand-preview" aria-label="Current login images">{presentation.logoUrl && <img src={presentation.logoUrl} alt="Current login logo" />}{presentation.backgroundUrl && <img src={presentation.backgroundUrl} alt="Current login background" />}</div>}
          <div className="actions"><button className="btn primary" type="submit">Save branding</button></div>
        </form>
      </section>

      <section className="ws-card glass admin-section" aria-labelledby="activity-title">
        <h2 id="activity-title">Administrative activity</h2>
        {activity.length ? <ul className="admin-activity">{activity.map((v) => <li key={v.id}><span>{activityLabel(v.action)}</span><span className="muted">{v.targetType}{v.targetId ? ` #${v.targetId}` : ''} · {new Date(v.createdAt).toLocaleString()}</span></li>)}</ul> : <p className="muted">No recorded activity yet.</p>}
      </section>
    </>}
  </>
}

function Metric({ label, value }: { label: string; value: string }) { return <div className="admin-metric"><span className="muted small">{label}</span><strong>{value}</strong></div> }
function BrandText({ label, setting, maxLength, value, set }: { label: string; setting: string; maxLength: number; value: Branding; set: (k: string, v: string) => void }) { return <label className="field"><span>{label}</span><input maxLength={maxLength} value={value[setting] ?? ''} onChange={(e) => set(setting, e.target.value)} /></label> }
function BrandColor({ label, setting, value, set }: { label: string; setting: string; value: Branding; set: (k: string, v: string) => void }) { return <label className="field"><span>{label} (hex)</span><input aria-label={label} pattern="#[0-9a-fA-F]{6}" maxLength={7} value={value[setting] ?? ''} onChange={(e) => set(setting, e.target.value)} /></label> }
function formatBytes(value: number) { if (!value) return '0 B'; const n = Math.floor(Math.log(value) / Math.log(1024)); return `${(value / 1024 ** n).toLocaleString(undefined, { maximumFractionDigits: n ? 1 : 0 })} ${['B','KB','MB','GB','TB'][n]}` }
function formatDuration(seconds: number) { const days = Math.floor(seconds / 86400); const hours = Math.floor((seconds % 86400) / 3600); return days ? `${days}d ${hours}h` : `${hours}h ${Math.floor(seconds % 3600 / 60)}m` }
function activityLabel(action: string) { return ({ user_created: 'User created', user_updated: 'User updated', user_settings_updated: 'User defaults updated', system_settings_updated: 'System settings updated', branding_asset_uploaded: 'Branding image uploaded', connection_revoked: 'Browser connection revoked', password_reset: 'Password reset', factor_reset: 'Sign-in factors reset' } as Record<string, string>)[action] ?? 'Administrative event' }
