import { useEffect, useState, type FormEvent } from 'react'
import { ApiError } from '../api'
import { useApp } from '../context'

type GeoLiteStatus = {
  configured: boolean
  enabled: boolean
  days: string[]
  startTime: string
  databaseAvailable: boolean
  databaseBuildTime: string | null
  lastAttempt: string | null
  lastResult: string
  lastErrorCode?: string
  running: boolean
  recoveryRequired: boolean
}

const weekdays = ['MON', 'TUE', 'WED', 'THU', 'FRI', 'SAT', 'SUN']

export function AdminGeoLite() {
  const { client } = useApp()
  const [status, setStatus] = useState<GeoLiteStatus | null>(null)
  const [days, setDays] = useState(['MON', 'THU'])
  const [startTime, setStartTime] = useState('04:00')
  const [accountId, setAccountId] = useState('')
  const [licenseKey, setLicenseKey] = useState('')
  const [enabled, setEnabled] = useState(false)
  const [loading, setLoading] = useState(true)
  const [working, setWorking] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  async function refresh() {
    const result = await client.request<{ status: GeoLiteStatus }>('GET', '/api/v1/admin/geolite')
    setStatus(result.status)
    setDays(result.status.days?.length ? result.status.days : ['MON', 'THU'])
    setStartTime(result.status.startTime || '04:00')
    setEnabled(result.status.enabled)
  }

  useEffect(() => {
    let live = true
    void client.request<{ status: GeoLiteStatus }>('GET', '/api/v1/admin/geolite').then((result) => {
      if (!live) return
      setStatus(result.status)
      setDays(result.status.days?.length ? result.status.days : ['MON', 'THU'])
      setStartTime(result.status.startTime || '04:00')
      setEnabled(result.status.enabled)
    }).catch((e: unknown) => {
      if (live) setError(e instanceof ApiError && e.status === 503 ? 'GeoLite updates are unavailable on this installation.' : 'GeoLite status could not be loaded.')
    }).finally(() => { if (live) setLoading(false) })
    return () => { live = false }
  }, [client])

  useEffect(() => {
    if (!status?.running) return
    const timer = window.setInterval(() => { void refresh().catch(() => undefined) }, 5000)
    return () => window.clearInterval(timer)
  }, [status?.running, client])

  async function save(e: FormEvent) {
    e.preventDefault(); setError(''); setNotice(''); setWorking(true)
    try {
      const result = await client.request<{ status: GeoLiteStatus }>('PUT', '/api/v1/admin/geolite', { accountId, licenseKey, enabled, days, startTime })
      setStatus(result.status); setAccountId(''); setLicenseKey(''); setNotice('GeoLite settings saved. Blank credentials keep the saved MaxMind account.');
    } catch (e) { setError(e instanceof ApiError ? e.message : 'GeoLite settings could not be saved.') }
    finally { setWorking(false) }
  }

  async function clear() {
    setError(''); setNotice(''); setWorking(true)
    try { const result = await client.request<{ status: GeoLiteStatus }>('DELETE', '/api/v1/admin/geolite'); setStatus(result.status); setAccountId(''); setLicenseKey(''); setNotice('Saved credentials and schedule removed. The active database is retained.') }
    catch (e) { setError(e instanceof ApiError ? e.message : 'GeoLite credentials could not be removed.') }
    finally { setWorking(false) }
  }

  async function runNow() {
    setError(''); setNotice(''); setWorking(true)
    try { const result = await client.request<{ job: string }>('POST', '/api/v1/admin/geolite/run'); setStatus((current) => current ? { ...current, running: true } : current); setNotice(`GeoLite update queued (${result.job}).`) }
    catch (e) { setError(e instanceof ApiError ? e.message : 'GeoLite update could not be queued.') }
    finally { setWorking(false) }
  }

  function toggleDay(day: string) { setDays((current) => current.includes(day) ? current.filter((d) => d !== day) : [...current, day].sort((a, b) => weekdays.indexOf(a) - weekdays.indexOf(b))) }

  return <section className="ws-card glass admin-section" aria-labelledby="geolite-title">
    <div className="admin-section-head"><div><h2 id="geolite-title">GeoLite country database</h2><p className="muted small">Configure MaxMind updates from this page. Credentials stay on the Windows host and are never shown again.</p></div><span className={status?.databaseAvailable ? 'health-pill ok' : 'health-pill bad'}>{loading ? 'Checking' : status?.databaseAvailable ? 'Database ready' : 'Database unavailable'}</span></div>
    {error && <p className="error" role="alert">{error}</p>}{notice && <p className="success" role="status">{notice}</p>}
    {loading ? <p className="muted">Loading GeoLite status…</p> : <>
      <div className="admin-health-grid">
        <Metric label="Credentials" value={status?.configured ? 'Configured' : 'Not configured'} />
        <Metric label="Schedule" value={status?.enabled ? 'Enabled' : 'Disabled'} />
        <Metric label="Database build" value={status?.databaseBuildTime ? new Date(status.databaseBuildTime).toLocaleString() : 'Unavailable'} />
        <Metric label="Last update" value={status?.lastAttempt ? `${new Date(status.lastAttempt).toLocaleString()} · ${status.lastResult || 'unknown'}` : 'Not run yet'} />
      </div>
      {status?.recoveryRequired && <p className="error" role="alert">Database recovery is pending. The next updater run will retry recovery before downloading.</p>}
      <form className="stack" onSubmit={save}>
        <div className="admin-brand-grid">
          <label className="field"><span>MaxMind Account ID <Hint text="Enter the Account ID from your MaxMind account. Leave both credential fields blank to keep the saved pair." /></span><input inputMode="numeric" autoComplete="off" value={accountId} onChange={(e) => setAccountId(e.target.value)} /></label>
          <label className="field"><span>MaxMind License Key <Hint text="License key is write-only here. A new ID and key must be entered together." /></span><input type="password" autoComplete="new-password" value={licenseKey} onChange={(e) => setLicenseKey(e.target.value)} /></label>
          <label className="field"><span>Local update time</span><input type="time" value={startTime} onChange={(e) => setStartTime(e.target.value)} /></label>
        </div>
        <fieldset className="field"><legend>Update days</legend><div className="admin-day-list">{weekdays.map((day) => <label key={day}><input type="checkbox" checked={days.includes(day)} onChange={() => toggleDay(day)} />{day}</label>)}</div><small className="muted">Choose at least two days. Times use the Windows host's local time.</small></fieldset>
        <label className="inline-check"><input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />Enable scheduled GeoLite updates</label>
        <p className="muted small">GeoLite Country is supplied by MaxMind. You must provide a valid Account ID and license key. Saving settings performs no MaxMind network request.</p>
        <div className="actions"><button className="btn primary" disabled={working || !status || days.length < 2} type="submit">{working ? 'Saving…' : 'Save GeoLite settings'}</button><button className="btn" disabled={working || !status?.configured} onClick={() => void clear()} type="button">Remove credentials</button><button className="btn" disabled={working || !status?.configured || status?.running} onClick={() => void runNow()} type="button">{status?.running ? 'Update running…' : 'Run update now'}</button></div>
      </form>
    </>}
  </section>
}

function Metric({ label, value }: { label: string; value: string }) { return <div className="admin-metric"><span className="muted small">{label}</span><strong>{value}</strong></div> }
function Hint({ text }: { text: string }) { return <span className="field-hint" title={text} aria-label={text} tabIndex={0}>ⓘ</span> }
