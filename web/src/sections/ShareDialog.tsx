import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { isCancelled, serverMessage, type Share, type WorkspaceApi } from '../api'
import { Dialog } from '../components/Dialog'

/** Manage who a folder is shared with. Only the owner sees this (private collection). */
export function ShareDialog({ api, folderId, folderName, onChanged, onClose }: { api: WorkspaceApi; folderId: number; folderName: string; onChanged: (m: string) => void; onClose: () => void }) {
  const [shares, setShares] = useState<Share[] | null>(null)
  const [error, setError] = useState('')
  const [recipient, setRecipient] = useState('')
  const [mode, setMode] = useState<Share['mode']>('view')
  const [busy, setBusy] = useState(false)
  const input = useRef<HTMLInputElement>(null)

  const load = useCallback(async () => {
    try {
      const d = await api.ownedShares()
      setShares((d.shares ?? []).filter((s) => s.folderId === folderId))
    } catch (err) {
      setError(serverMessage(err))
    }
  }, [api, folderId])
  useEffect(() => {
    void load()
  }, [load])

  async function run(action: () => Promise<unknown>, done: string) {
    setBusy(true)
    setError('')
    try {
      await action()
      onChanged(done)
      await load()
    } catch (err) {
      if (!isCancelled(err)) setError(serverMessage(err))
    } finally {
      setBusy(false)
    }
  }

  function grant(e: FormEvent) {
    e.preventDefault()
    void run(async () => {
      await api.grantShare(folderId, recipient.trim(), mode)
      setRecipient('')
    }, `Shared ${folderName}.`)
  }

  return (
    <Dialog title={`Share “${folderName}”`} onClose={onClose} initialFocus={input}>
      <div className="stack compact">
        {error && <p className="error" role="alert">{error}</p>}
        <form className="inline-form" onSubmit={grant} noValidate>
          <label className="field grow">
            <span>Person (username or email)</span>
            <input ref={input} value={recipient} onChange={(e) => setRecipient(e.target.value)} />
          </label>
          <label className="field">
            <span>Access</span>
            <select value={mode} onChange={(e) => setMode(e.target.value as Share['mode'])}>
              <option value="view">View only</option>
              <option value="edit">Can edit</option>
            </select>
          </label>
          <button type="submit" className="btn primary" disabled={busy || !recipient.trim()}>Share</button>
        </form>
        {shares && shares.length === 0 && <p className="muted">Not shared with anyone yet.</p>}
        {shares && shares.length > 0 && (
          <ul className="list">
            {shares.map((s) => (
              <li key={s.id} className="list-item">
                <span className="item-title">User #{s.recipientId}</span>
                <div className="actions">
                  <select
                    aria-label={`Access for user ${s.recipientId}`}
                    value={s.mode}
                    disabled={busy}
                    onChange={(e) => void run(() => api.updateShare(s.id, e.target.value as Share['mode']), 'Access updated.')}
                  >
                    <option value="view">View only</option>
                    <option value="edit">Can edit</option>
                  </select>
                  <button type="button" className="btn ghost small" disabled={busy} aria-label={`Stop sharing with user ${s.recipientId}`} onClick={() => void run(() => api.revokeShare(s.id), 'Stopped sharing.')}>Stop sharing</button>
                </div>
              </li>
            ))}
          </ul>
        )}
        <div className="actions">
          <button type="button" className="btn ghost" onClick={onClose}>Close</button>
        </div>
      </div>
    </Dialog>
  )
}
