import { useCallback, useEffect, useState } from 'react'
import { isCancelled, serverMessage, type Bookmark, type DuplicateGroup, type TreeNode, type WorkspaceApi } from '../api'
import { Busy } from '../components/common'
import { ConfirmDialog } from '../components/Dialog'
import { Workspace } from '../components/Workspace'
import { useApp } from '../context'
import { useScopes } from '../sections/useScopes'
import { folderPath, plural } from '../workspace'

/** Duplicate review: groups of records with the same exact URL. Nothing is removed without confirmation. */
export function DuplicatesPage() {
  return (
    <Workspace current="duplicates" title="Duplicates" subtitle="Review bookmarks that share the same address. Nothing is removed until you choose to.">
      <Review />
    </Workspace>
  )
}

function Review() {
  const { client } = useApp()
  const { scopes, active, setActive, api } = useScopes(client)
  return (
    <section className="ws-card glass dup">
      {scopes.length > 1 && (
        <label className="field scope-select">
          <span>Collection</span>
          <select value={active.key} onChange={(e) => setActive(e.target.value)}>
            {scopes.map((s) => <option key={s.key} value={s.key}>{s.label}</option>)}
          </select>
        </label>
      )}
      {active.readOnly && <p className="note" role="note">This shared folder is read-only, so duplicates can be reviewed here but not removed.</p>}
      <Groups key={active.key} api={api} canRemove={!active.readOnly} />
    </section>
  )
}

type State = { state: 'loading' } | { state: 'error'; message: string } | { state: 'ready'; count: number; groups: DuplicateGroup[]; hasMore: boolean }

function Groups({ api, canRemove }: { api: WorkspaceApi; canRemove: boolean }) {
  const [data, setData] = useState<State>({ state: 'loading' })
  const [roots, setRoots] = useState<TreeNode[]>([])
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [confirm, setConfirm] = useState<Bookmark | null>(null)
  const [busy, setBusy] = useState(false)
  const [confirmError, setConfirmError] = useState('')

  const load = useCallback(
    async (page: number) => {
      try {
        const d = await api.duplicates(page)
        setData((prev) => ({
          state: 'ready',
          count: d.count,
          groups: page > 0 && prev.state === 'ready' ? [...prev.groups, ...d.groups] : d.groups,
          hasMore: d.hasMore,
        }))
      } catch (err) {
        if (!isCancelled(err)) setData({ state: 'error', message: serverMessage(err) })
      }
    },
    [api],
  )
  useEffect(() => {
    void load(0)
    api.folders().then((d) => setRoots(d.folders ?? [])).catch(() => setRoots([]))
  }, [api, load])

  async function remove() {
    if (!confirm) return
    setBusy(true)
    setConfirmError('')
    try {
      await api.deleteBookmark(confirm.id)
      setNotice(`Moved to Trash: ${confirm.title}.`)
      setError('')
      setConfirm(null)
      await load(0)
    } catch (err) {
      if (!isCancelled(err)) setConfirmError(serverMessage(err))
      else setConfirm(null)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="stack">
      {notice && (
        <p className="notice" role="status">
          {notice}
          <button type="button" className="link-button" onClick={() => setNotice('')} aria-label="Dismiss message">Dismiss</button>
        </p>
      )}
      {error && <p className="error" role="alert">{error}</p>}
      {data.state === 'loading' && <Busy label="Looking for duplicates…" />}
      {data.state === 'error' && (
        <div className="error" role="alert">
          Duplicates could not be loaded: {data.message}{' '}
          <button type="button" className="btn outline small" onClick={() => void load(0)}>Try again</button>
        </div>
      )}
      {data.state === 'ready' && (
        <>
          <p className="muted">{data.count === 0 ? 'No duplicates to review.' : `${plural(data.count, 'duplicate')} to review. Removing one moves it to Trash, where you can restore it.`}</p>
          <ul className="list">
            {data.groups.map((g) => (
              <li key={g.url} className="dup-group">
                <p className="item-title"><code>{g.url}</code></p>
                <ul className="list">
                  {g.bookmarks.map((b) => (
                    <li key={b.id} className="list-item">
                      <div>
                        <p className="item-title">{b.title}</p>
                        <p className="small muted">{b.folders.length > 0 ? b.folders.map((f) => folderPath(roots, f)).join(' · ') : 'Not in a folder'}</p>
                      </div>
                      {canRemove && (
                        <button type="button" className="btn outline small" aria-label={`Remove ${b.title}`} onClick={() => setConfirm(b)}>Remove</button>
                      )}
                    </li>
                  ))}
                </ul>
              </li>
            ))}
          </ul>
          {data.hasMore && <button type="button" className="btn outline" onClick={() => void load(data.groups.length > 0 ? Math.ceil(data.groups.length / 50) : 1)}>Load more</button>}
        </>
      )}
      {confirm && (
        <ConfirmDialog
          title="Move to Trash?"
          message={`“${confirm.title}” will move to Trash. The other bookmark with this address stays. You can restore it for 30 days.`}
          confirm="Move to Trash"
          busy={busy}
          error={confirmError}
          onConfirm={remove}
          onCancel={() => {
            setConfirm(null)
            setConfirmError('')
          }}
        />
      )}
    </div>
  )
}
