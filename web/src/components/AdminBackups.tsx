import { useCallback, useEffect, useState } from 'react'
import { ApiError } from '../api'
import { useApp } from '../context'

type Schedule = { weeklyEnabled: boolean; monthlyEnabled: boolean; timezone: string; weeklyDay: number; weeklyHour: number; weeklyMinute: number; monthlyDay: number; monthlyHour: number; monthlyMinute: number; weeklyRetention: number; monthlyRetention: number }
type Destination = { id?: string; label: string; kind: 'local' | 'nfs' | 'rclone'; reference: string; enabled: boolean; encrypted: boolean; hasRecoveryKey?: boolean }
type BackupRun = { id: number; batchId: string; trigger: string; destinationId: string; destinationLabel: string; archiveName: string; state: string; sizeBytes: number; errorCode?: string; startedAt: string; finishedAt?: string }
type BackupData = { schedule: Schedule; destinations: Destination[]; runs: BackupRun[]; running: boolean }

const defaultSchedule: Schedule = { weeklyEnabled: false, monthlyEnabled: false, timezone: 'Asia/Riyadh', weeklyDay: 0, weeklyHour: 3, weeklyMinute: 0, monthlyDay: 1, monthlyHour: 4, monthlyMinute: 0, weeklyRetention: 8, monthlyRetention: 12 }
const emptyDestination: Destination = { label: '', kind: 'local', reference: '', enabled: true, encrypted: false }
const days = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

export function AdminBackups() {
  const { client } = useApp()
  const [data, setData] = useState<BackupData | null>(null)
  const [schedule, setSchedule] = useState(defaultSchedule)
  const [destination, setDestination] = useState<Destination>(emptyDestination)
  const [editing, setEditing] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')

  const load = useCallback(async (refreshSchedule = true) => {
    const response = await client.request<BackupData>('GET', '/api/v1/admin/backups')
    setData(response)
    if (refreshSchedule) setSchedule(response.schedule ?? defaultSchedule)
  }, [client])

  useEffect(() => { void load().catch((e: unknown) => setError(e instanceof ApiError ? e.message : 'Backup settings could not be loaded.')) }, [load])
  useEffect(() => {
    if (!data?.running) return
    const timer = window.setInterval(() => { void load(false).catch(() => undefined) }, 4000)
    return () => window.clearInterval(timer)
  }, [data?.running, load])

  async function saveSchedule() {
    setBusy(true); setError(''); setNotice('')
    try { await client.request('PUT', '/api/v1/admin/backups/schedule', schedule); setNotice('Backup schedule saved.'); await load() }
    catch (e) { setError(e instanceof ApiError ? e.message : 'Backup schedule could not be saved.') }
    finally { setBusy(false) }
  }

  async function testDestination() {
    setBusy(true); setError(''); setNotice('')
    try { await client.request('POST', '/api/v1/admin/backups/destinations/test', destination); setNotice('Destination test passed. Temporary test data was removed.') }
    catch (e) { setError(e instanceof ApiError ? e.message : 'Destination test failed.') }
    finally { setBusy(false) }
  }

  async function saveDestination() {
    setBusy(true); setError(''); setNotice('')
    try {
      const path = destination.id ? `/api/v1/admin/backups/destinations/${destination.id}` : '/api/v1/admin/backups/destinations'
      const { hasRecoveryKey: _hasRecoveryKey, ...request } = destination
      await client.request(destination.id ? 'PUT' : 'POST', path, request)
      setDestination(emptyDestination); setEditing(''); setNotice('Backup destination saved.'); await load()
    } catch (e) { setError(e instanceof ApiError ? e.message : 'Backup destination could not be saved.') }
    finally { setBusy(false) }
  }

  async function downloadRecoveryKey(id: string) {
    if (!window.confirm('Download this destination recovery key now and store it separately from the backup archives? Anyone with the key and an archive can read the backup.')) return
    setBusy(true); setError(''); setNotice('')
    try {
      const response = await client.request<{ recoveryKey: string }>('GET', `/api/v1/admin/backups/destinations/${id}/recovery-key`)
      const blob = new Blob([`${response.recoveryKey}\n`], { type: 'text/plain;charset=utf-8' })
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a'); anchor.href = url; anchor.download = `bookmarksync-${id}-recovery-key.txt`; anchor.click()
      window.setTimeout(() => URL.revokeObjectURL(url), 1000)
      setNotice('Recovery key downloaded. Store it separately from your backup files.')
    } catch (e) { setError(e instanceof ApiError ? e.message : 'Recovery key could not be downloaded.') }
    finally { setBusy(false) }
  }

  async function removeDestination(id: string) {
    if (!window.confirm('Remove this destination from BookmarksSync? Existing backup files and its host recovery key will be left in place. Download and store the recovery key before removing this destination; it will no longer be downloadable here.')) return
    setBusy(true); setError(''); setNotice('')
    try { await client.request('DELETE', `/api/v1/admin/backups/destinations/${id}`); setNotice('Backup destination removed.'); await load() }
    catch (e) { setError(e instanceof ApiError ? e.message : 'Backup destination could not be removed.') }
    finally { setBusy(false) }
  }

  async function startNow() {
    setBusy(true); setError(''); setNotice('')
    try { await client.request('POST', '/api/v1/admin/backups/run'); setNotice('Backup started. This page will refresh its status automatically.'); await load() }
    catch (e) { setError(e instanceof ApiError ? e.message : 'Backup could not be started.') }
    finally { setBusy(false) }
  }

  function editDestination(value: Destination) { setDestination(value); setEditing(value.id ?? 'new') }

  return <section className="ws-card glass admin-section" aria-labelledby="backups-title">
    <div className="admin-section-head"><div><h2 id="backups-title">System backups</h2><p className="muted small">Compressed full-system archives include server and user settings. Remote credentials stay on the host.</p></div><button className="btn primary" type="button" disabled={busy || data?.running || !data?.destinations.some((d) => d.enabled)} onClick={() => void startNow()}>{data?.running ? 'Backup running…' : 'Run backup now'}</button></div>
    {error && <p className="error" role="alert">{error}</p>}{notice && <p className="success" role="status">{notice}</p>}
    {data && <>
      <form className="stack" onSubmit={(e) => { e.preventDefault(); void saveSchedule() }}>
        <h3>Schedule</h3>
        <div className="admin-brand-grid">
          <label className="check"><input type="checkbox" checked={schedule.weeklyEnabled} onChange={(e) => setSchedule({ ...schedule, weeklyEnabled: e.target.checked })} />Weekly backups</label>
          <label className="field"><span>Day</span><select value={schedule.weeklyDay} onChange={(e) => setSchedule({ ...schedule, weeklyDay: Number(e.target.value) })}>{days.map((day, i) => <option key={day} value={i}>{day}</option>)}</select></label>
          <label className="field"><span>Time</span><input type="time" value={`${String(schedule.weeklyHour).padStart(2, '0')}:${String(schedule.weeklyMinute).padStart(2, '0')}`} onChange={(e) => { const [hour, minute] = e.target.value.split(':').map(Number); setSchedule({ ...schedule, weeklyHour: hour, weeklyMinute: minute }) }} /></label>
          <label className="field"><span>Keep weekly archives</span><input type="number" min={1} max={100} value={schedule.weeklyRetention} onChange={(e) => setSchedule({ ...schedule, weeklyRetention: Number(e.target.value) })} /></label>
          <label className="check"><input type="checkbox" checked={schedule.monthlyEnabled} onChange={(e) => setSchedule({ ...schedule, monthlyEnabled: e.target.checked })} />Monthly backups</label>
          <label className="field"><span>Day of month (1–28)</span><input type="number" min={1} max={28} value={schedule.monthlyDay} onChange={(e) => setSchedule({ ...schedule, monthlyDay: Number(e.target.value) })} /></label>
          <label className="field"><span>Time</span><input type="time" value={`${String(schedule.monthlyHour).padStart(2, '0')}:${String(schedule.monthlyMinute).padStart(2, '0')}`} onChange={(e) => { const [hour, minute] = e.target.value.split(':').map(Number); setSchedule({ ...schedule, monthlyHour: hour, monthlyMinute: minute }) }} /></label>
          <label className="field"><span>Keep monthly archives</span><input type="number" min={1} max={100} value={schedule.monthlyRetention} onChange={(e) => setSchedule({ ...schedule, monthlyRetention: Number(e.target.value) })} /></label>
          <label className="field"><span>Time zone (IANA)</span><input value={schedule.timezone} onChange={(e) => setSchedule({ ...schedule, timezone: e.target.value })} placeholder="Asia/Riyadh" /></label>
        </div>
        <p className="muted small">Missed schedule times are skipped after downtime. Weekly and monthly retention are managed separately.</p>
        <div className="actions"><button className="btn primary" type="submit" disabled={busy}>Save schedule</button></div>
      </form>

      <div className="stack">
        <h3>Destinations</h3>
        <p className="muted small">Local paths must exist. NFS requires an active NFS mount. Configure rclone remotes and credentials on the host, then enter a path such as <code>nas:bookmarks</code>. After a destination has backup history, its type and path are fixed; add a new destination to change where future archives go.</p>
        {data.destinations.length ? <ul className="admin-activity">{data.destinations.map((item) => <li key={item.id}><span><strong>{item.label}</strong> · {item.kind} · {item.reference} · {item.enabled ? 'Enabled' : 'Disabled'} · {item.encrypted ? 'Encrypted' : 'Plain'}{item.encrypted && !item.hasRecoveryKey ? ' · Recovery key unavailable' : ''}</span><span className="actions">{item.encrypted && item.hasRecoveryKey && item.id && <button className="btn ghost small" type="button" disabled={busy} onClick={() => void downloadRecoveryKey(item.id!)}>Download recovery key</button>}<button className="btn ghost small" type="button" onClick={() => editDestination(item)}>Edit</button><button className="btn ghost small" type="button" disabled={busy} onClick={() => item.id && void removeDestination(item.id)}>Remove</button></span></li>)}</ul> : <p className="muted">No backup destinations configured.</p>}
        <form className="stack" onSubmit={(e) => { e.preventDefault(); void saveDestination() }}>
          <h4>{editing ? 'Edit destination' : 'Add destination'}</h4>
          <div className="admin-brand-grid">
            <label className="field"><span>Label</span><input maxLength={80} value={destination.label} onChange={(e) => setDestination({ ...destination, label: e.target.value })} /></label>
            <label className="field"><span>Type</span><select value={destination.kind} onChange={(e) => setDestination({ ...destination, kind: e.target.value as Destination['kind'] })}><option value="local">Local folder</option><option value="nfs">NFS mount</option><option value="rclone">Configured rclone remote</option></select></label>
            <label className="field"><span>{destination.kind === 'rclone' ? 'Remote path' : 'Absolute host folder path'}</span><input value={destination.reference} onChange={(e) => setDestination({ ...destination, reference: e.target.value })} placeholder={destination.kind === 'rclone' ? 'nas:bookmarks' : '/mnt/backup/bookmarksync'} /></label>
            <label className="check"><input type="checkbox" checked={destination.enabled} onChange={(e) => setDestination({ ...destination, enabled: e.target.checked })} />Use for backups</label>
            <label className="check"><input type="checkbox" checked={destination.encrypted} onChange={(e) => setDestination({ ...destination, encrypted: e.target.checked })} />Encrypt backup archives at this destination</label>
          </div>
          <p className="muted small">Encryption is off by default. Download the recovery key and store it separately; losing it makes encrypted archives unrecoverable.</p>
          <div className="actions"><button className="btn ghost" type="button" disabled={busy} onClick={() => void testDestination()}>Test destination</button><button className="btn primary" type="submit" disabled={busy}>Save destination</button>{editing && <button className="btn ghost" type="button" onClick={() => { setDestination(emptyDestination); setEditing('') }}>Cancel</button>}</div>
        </form>
      </div>

      <div className="stack"><h3>Recent runs</h3>{data.runs.length ? <ul className="admin-activity">{data.runs.map((run) => <li key={run.id}><span><strong>{run.destinationLabel}</strong> · {run.trigger} · {run.state}{run.errorCode ? ` (${run.errorCode.replaceAll('_', ' ')})` : ''}</span><span className="muted">{new Date(run.startedAt).toLocaleString()} · {run.sizeBytes ? formatBytes(run.sizeBytes) : '—'}</span></li>)}</ul> : <p className="muted">No backup runs yet.</p>}</div>
    </>}
  </section>
}

function formatBytes(value: number) { if (!value) return '0 B'; const n = Math.floor(Math.log(value) / Math.log(1024)); return `${(value / 1024 ** n).toLocaleString(undefined, { maximumFractionDigits: n ? 1 : 0 })} ${['B', 'KB', 'MB', 'GB', 'TB'][n]}` }
