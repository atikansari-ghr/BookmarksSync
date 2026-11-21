import { useCallback, useEffect, useId, useState } from 'react'
import { isCancelled, serverMessage, type TrashItem, type WorkspaceApi } from '../api'
import { Busy, formatTime } from '../components/common'
import { ConfirmDialog } from '../components/Dialog'

/** Trash: restore, delete permanently, or empty. Purging asks for a recent sign-in via the shared client. */
export function TrashPanel({ api, onChanged, onNotice }: { api: WorkspaceApi; onChanged: () => void; onNotice: (m: string) => void }) {
  const id = useId()
  const [items, setItems] = useState<TrashItem[] | null>(null)
  const [loadError, setLoadError] = useState('')
  const [error, setError] = useState('')
  const [confirm, setConfirm] = useState<{ item?: TrashItem } | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    try {
      setItems((await api.trash()).items ?? [])
      setLoadError('')
    } catch (err) {
      setLoadError(serverMessage(err))
    }
  }, [api])
  useEffect(() => {
    void load()
  }, [load])

  async function restore(item: TrashItem) {
    setError('')
    try {
      await api.restore(item.type, item.id)
      onNotice(`Restored ${item.title || item.url}.`)
      await load()
      onChanged()
    } catch (err) {
      if (!isCancelled(err)) setError(serverMessage(err))
    }
  }

  async function purge() {
    if (!confirm) return
    setBusy(true)
    setError('')
    try {
      if (confirm.item) await api.purge(confirm.item.type, confirm.item.id)
      else await api.emptyTrash()
      onNotice(confirm.item ? `Deleted ${confirm.item.title || confirm.item.url} permanently.` : 'Trash emptied.')
      setConfirm(null)
      await load()
    } catch (err) {
      if (!isCancelled(err)) setError(serverMessage(err))
      else setConfirm(null)
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="trash" aria-labelledby={id}>
      <div className="trash-head">
        <h2 id={id}>Trash</h2>
        {items && items.length > 0 && (
          <button type="button" className="btn ghost small" onClick={() => setConfirm({})}>Empty Trash</button>
        )}
      </div>
      <p className="muted small">Deleted items stay here for 30 days, then are removed for good.</p>
      {loadError && (
        <div className="error" role="alert">
          Trash could not be loaded: {loadError}{' '}
          <button type="button" className="link-button" onClick={() => void load()}>Try again</button>
        </div>
      )}
      {error && <p className="error" role="alert">{error}</p>}
      {!items && !loadError && <Busy label="Loading Trash…" />}
      {items && items.length === 0 && <p className="muted">Trash is empty.</p>}
      {items && items.length > 0 && (
        <ul className="list">
          {items.map((t) => {
            const name = t.title || t.url
            return (
              <li key={`${t.type}-${t.id}`} className="list-item">
                <div>
                  <p className="item-title">{name} {t.type === 'folder' && <span className="badge">Folder</span>}</p>
                  <p className="small muted">Deleted {formatTime(t.deletedAt)} · removed for good {formatTime(t.purgeAt)}</p>
                </div>
                <div className="actions">
                  <button type="button" className="btn outline small" aria-label={`Restore ${name}`} onClick={() => restore(t)}>Restore</button>
                  <button type="button" className="btn ghost small" aria-label={`Delete ${name} permanently`} onClick={() => setConfirm({ item: t })}>Delete permanently</button>
                </div>
              </li>
            )
          })}
        </ul>
      )}
      {confirm && (
        <ConfirmDialog
          title="Delete permanently?"
          message={confirm.item ? `“${confirm.item.title || confirm.item.url}” will be removed for good. This cannot be undone.` : 'Everything in Trash will be removed for good. This cannot be undone.'}
          confirm="Delete permanently"
          busy={busy}
          error={error}
          onConfirm={purge}
          onCancel={() => {
            setConfirm(null)
            setError('')
          }}
        />
      )}
    </section>
  )
}
