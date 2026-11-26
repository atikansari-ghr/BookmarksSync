import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ApiError } from '../api'
import { Busy } from '../components/common'
import { Workspace } from '../components/Workspace'
import { useApp } from '../context'

type WidgetID = 'total' | 'recent' | 'sync' | 'duplicates'
type Widget = { id: WidgetID; visible: boolean; order: number; size: 'small' | 'medium' | 'large' }
type Layout = { widgets: Widget[] }
const DEFAULT_LAYOUT: Layout = { widgets: [
  { id: 'total', visible: true, order: 0, size: 'small' },
  { id: 'recent', visible: true, order: 1, size: 'medium' },
  { id: 'sync', visible: true, order: 2, size: 'small' },
  { id: 'duplicates', visible: true, order: 3, size: 'small' },
] }
const names: Record<WidgetID, string> = { total: 'Total Bookmarks', recent: 'Recent Changes', sync: 'Sync Status', duplicates: 'Duplicate Count' }
type Dashboard = {
  totalBookmarks: number
  duplicateCount: number
  lastContactAt: string | null
  recentChanges: { counts: { created: number; updated: number; deleted: number }; items: { type: string; title: string; at: string }[] }
}

function parseLayout(raw?: string): Layout {
  if (!raw) return DEFAULT_LAYOUT
  try {
    const value = JSON.parse(raw) as Layout
    if (!Array.isArray(value.widgets) || value.widgets.length !== 4) return DEFAULT_LAYOUT
    return { widgets: [...value.widgets].sort((a, b) => a.order - b.order) }
  } catch { return DEFAULT_LAYOUT }
}

export function DashboardPage() {
  return <Workspace current="dashboard" title="Dashboard" subtitle="A clear view of your bookmarks and sync activity."><Dashboard /></Workspace>
}

function Dashboard() {
  const { client, navigate, theme, setTheme } = useApp()
  const [data, setData] = useState<Dashboard | null>(null)
  const [layout, setLayout] = useState(DEFAULT_LAYOUT)
  const [busy, setBusy] = useState(true)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [mobile, setMobile] = useState(window.innerWidth <= 560)
  const themeChangeVersion = useRef(0)

  useEffect(() => {
    const resize = () => setMobile(window.innerWidth <= 560)
    window.addEventListener('resize', resize)
    return () => window.removeEventListener('resize', resize)
  }, [])

  useEffect(() => {
    let live = true
    void Promise.all([
      client.request<Dashboard>('GET', '/api/v1/me/dashboard'),
      client.request<{ settings: Record<string, string> }>('GET', '/api/v1/me/settings'),
    ]).then(([d, prefs]) => {
      if (!live) return
      setData(d)
      setLayout(parseLayout(prefs.settings['dashboard.layout']))
    }).catch((e: unknown) => {
      if (live) setError(e instanceof ApiError ? e.message : 'Dashboard could not be loaded.')
    }).finally(() => { if (live) setBusy(false) })
    return () => { live = false }
  }, [client])

  const updateSetting = useCallback(async (key: string, value: string): Promise<boolean> => {
    setSaved(false)
    setError('')
    try {
      await client.request('PATCH', '/api/v1/me/settings', { [key]: value })
      setSaved(true)
      return true
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Your setting could not be saved.')
      return false
    }
  }, [client])

  const ordered = useMemo(() => [...layout.widgets].sort((a, b) => a.order - b.order), [layout])

  async function persistLayout(next: Layout) {
    const previous = layout
    setLayout(next)
    if (!(await updateSetting('dashboard.layout', JSON.stringify(next)))) {
      setLayout((current) => current === next ? previous : current)
    }
  }
  function reorder(id: WidgetID, delta: number) {
    const list = [...ordered]
    const from = list.findIndex((w) => w.id === id)
    const to = Math.max(0, Math.min(list.length - 1, from + delta))
    if (to === from) return
    const [item] = list.splice(from, 1)
    list.splice(to, 0, item)
    void persistLayout({ widgets: list.map((w, order) => ({ ...w, order })) })
  }
  function keyMove(e: React.KeyboardEvent, id: WidgetID) {
    if (e.altKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
      e.preventDefault()
      reorder(id, e.key === 'ArrowUp' ? -1 : 1)
    }
  }
  async function chooseTheme(name: string) {
    const previous = theme
    const next = { ...theme, name }
    const version = ++themeChangeVersion.current
    setTheme(next)
    if (!(await updateSetting('ui.theme', name)) && themeChangeVersion.current === version) setTheme(previous)
  }
  async function chooseScheme(scheme: 'light' | 'dark') {
    const previous = theme
    const next = { ...theme, scheme }
    const version = ++themeChangeVersion.current
    setTheme(next)
    if (!(await updateSetting('ui.color-scheme', scheme)) && themeChangeVersion.current === version) setTheme(previous)
  }
  async function chooseTransparency(reducedTransparency: boolean) {
    const previous = theme
    const next = { ...theme, reducedTransparency }
    const version = ++themeChangeVersion.current
    setTheme(next)
    if (!(await updateSetting('ui.reduced-transparency', String(reducedTransparency))) && themeChangeVersion.current === version) setTheme(previous)
  }

  return <>
    {error && <p className="error" role="alert">{error}</p>}
    {saved && <p className="success" role="status">Preferences saved.</p>}
    <section className="ws-card glass dashboard-preferences" aria-label="Dashboard preferences">
      <div><h2>Appearance</h2><p className="muted small">Choose a look and adjust how much glass effect you see.</p></div>
      <label className="field"><span>Theme</span><select disabled={busy} value={theme.name} onChange={(e) => void chooseTheme(e.target.value)}>
        <option value="classic">Classic</option><option value="ocean-glass">Ocean Glass</option><option value="midnight-glass">Midnight Glass</option><option value="warm-glass">Warm Glass</option>
      </select></label>
      <label className="field"><span>Colors</span><select disabled={busy} value={theme.scheme} onChange={(e) => void chooseScheme(e.target.value as 'light' | 'dark')}><option value="light">Light</option><option value="dark">Dark</option></select></label>
      <label className="check"><input type="checkbox" disabled={busy} checked={theme.reducedTransparency} onChange={(e) => void chooseTransparency(e.target.checked)} /> Reduce transparency</label>
    </section>
    <div className="dashboard-tools">
      <h2>Overview</h2><button type="button" className="btn ghost small" disabled={busy} onClick={() => void persistLayout(DEFAULT_LAYOUT)}>Reset widgets</button>
    </div>
    <fieldset className="widget-visibility ws-card glass" disabled={busy}>
      <legend>Visible widgets</legend>
      {ordered.map((w) => <label className="check" key={w.id}><input type="checkbox" aria-label={`Show ${names[w.id]}`} checked={w.visible} onChange={(e) => void persistLayout({ widgets: layout.widgets.map((x) => x.id === w.id ? { ...x, visible: e.target.checked } : x) })} />{names[w.id]}</label>)}
    </fieldset>
    {busy ? <Busy label="Loading dashboard" /> : !data ? <p className="muted">Dashboard data is unavailable.</p> : <section className={`dashboard-grid${mobile ? ' mobile' : ''}`} aria-label="Dashboard widgets">
      {ordered.map((w) => w.visible && <article key={w.id} className={`dashboard-widget glass size-${w.size}`} aria-label={names[w.id]} tabIndex={0} onKeyDown={(e) => keyMove(e, w.id)}>
        <div className="widget-heading"><h3>{names[w.id]}</h3><span className="widget-actions">
          <button className="btn ghost small" type="button" aria-label={`Move ${names[w.id]} up`} onClick={() => reorder(w.id, -1)}>↑</button>
          <button className="btn ghost small" type="button" aria-label={`Move ${names[w.id]} down`} onClick={() => reorder(w.id, 1)}>↓</button>
          <label className="visually-hidden" htmlFor={`size-${w.id}`}>{names[w.id]} size</label>
          <select id={`size-${w.id}`} aria-label={`${names[w.id]} size`} value={w.size} onChange={(e) => void persistLayout({ widgets: layout.widgets.map((x) => x.id === w.id ? { ...x, size: e.target.value as Widget['size'] } : x) })}>
            <option value="small">Small</option><option value="medium">Medium</option><option value="large">Large</option>
          </select>
        </span></div>
        {w.id === 'total' && <p className="widget-value">{data.totalBookmarks}</p>}
        {w.id === 'duplicates' && <><p className="widget-value">{data.duplicateCount}</p><button type="button" className="btn ghost" onClick={() => navigate('/duplicates')}>Review duplicates</button></>}
        {w.id === 'sync' && <p>{data.lastContactAt ? `Last synced ${new Date(data.lastContactAt).toLocaleString()}` : 'No browser sync has been recorded yet.'}</p>}
        {w.id === 'recent' && <><p className="muted">Last 7 days: {data.recentChanges.counts.created} added · {data.recentChanges.counts.updated} updated · {data.recentChanges.counts.deleted} removed</p><ul>{data.recentChanges.items.slice(0, 5).map((item, i) => <li key={`${item.at}-${i}`}>{item.title} <span className="muted">· {item.type}</span></li>)}</ul></>}
      </article>)}
    </section>}
  </>
}
