import { useRef, useState, type FormEvent } from 'react'
import { isCancelled, serverMessage, type Bookmark } from '../api'
import { Dialog } from '../components/Dialog'
import { parseTags } from '../workspace'

export interface FolderChoice {
  id: number
  path: string
}

function useSubmit(action: () => Promise<void>) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function submit(e: FormEvent) {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      await action()
    } catch (err) {
      if (!isCancelled(err)) setError(serverMessage(err))
    } finally {
      setBusy(false)
    }
  }
  return { busy, error, submit }
}

interface BookmarkValues {
  title: string
  url: string
  tags: string[]
  folder: number
}

/** Create or edit a bookmark. Placement changes happen by moving, not here. */
export function BookmarkDialog({
  bookmark,
  folders,
  initialFolder,
  onSave,
  onClose,
}: {
  bookmark?: Bookmark
  folders: FolderChoice[]
  initialFolder: number
  onSave: (v: BookmarkValues) => Promise<void>
  onClose: () => void
}) {
  const [title, setTitle] = useState(bookmark?.title ?? '')
  const [url, setUrl] = useState(bookmark?.url ?? '')
  const [tags, setTags] = useState(bookmark?.tags.join(', ') ?? '')
  const [folder, setFolder] = useState(initialFolder)
  const first = useRef<HTMLInputElement>(null)
  const { busy, error, submit } = useSubmit(() => onSave({ title: title.trim(), url: url.trim(), tags: parseTags(tags), folder }))
  return (
    <Dialog title={bookmark ? 'Edit bookmark' : 'New bookmark'} onClose={onClose} initialFocus={first}>
      <form className="stack compact" onSubmit={submit} noValidate>
        {error && <p className="error" role="alert">{error}</p>}
        <label className="field">
          <span>Title</span>
          <input ref={first} value={title} onChange={(e) => setTitle(e.target.value)} />
        </label>
        <label className="field">
          <span>URL</span>
          <input type="url" inputMode="url" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://example.com" />
        </label>
        {!bookmark && (
          <label className="field">
            <span>Folder</span>
            <select value={folder} onChange={(e) => setFolder(Number(e.target.value))}>
              {folders.map((f) => <option key={f.id} value={f.id}>{f.path}</option>)}
            </select>
          </label>
        )}
        <label className="field">
          <span>Tags</span>
          <input value={tags} onChange={(e) => setTags(e.target.value)} placeholder="Separate tags with commas" />
        </label>
        <div className="actions">
          <button type="button" className="btn ghost" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn primary" disabled={busy || !title.trim() || !url.trim() || (!bookmark && folder <= 0)}>Save</button>
        </div>
      </form>
    </Dialog>
  )
}

/** Create a folder or rename the open one. */
export function FolderDialog({ initial, onSave, onClose }: { initial?: string; onSave: (name: string) => Promise<void>; onClose: () => void }) {
  const [name, setName] = useState(initial ?? '')
  const first = useRef<HTMLInputElement>(null)
  const { busy, error, submit } = useSubmit(() => onSave(name.trim()))
  return (
    <Dialog title={initial === undefined ? 'New folder' : 'Rename folder'} onClose={onClose} initialFocus={first}>
      <form className="stack compact" onSubmit={submit} noValidate>
        {error && <p className="error" role="alert">{error}</p>}
        <label className="field">
          <span>Name</span>
          <input ref={first} value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <div className="actions">
          <button type="button" className="btn ghost" onClick={onClose}>Cancel</button>
          <button type="submit" className="btn primary" disabled={busy || !name.trim()}>Save</button>
        </div>
      </form>
    </Dialog>
  )
}
