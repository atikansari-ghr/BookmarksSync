import { useState } from 'react'
import { isCancelled, serverMessage, type WorkspaceApi } from '../api'
import { Dialog } from '../components/Dialog'

function readText(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(String(r.result ?? ''))
    r.onerror = () => reject(r.error ?? new Error('read failed'))
    r.readAsText(file)
  })
}

/** XBEL and other XML files are sent as XML; everything else as Netscape HTML. */
export function importType(text: string): string {
  const head = text.trimStart().slice(0, 400).toLowerCase()
  return head.startsWith('<?xml') || head.includes('<xbel') ? 'application/xml' : 'text/html'
}

export function ImportDialog({
  api,
  folder,
  folderName,
  onDone,
  onClose,
}: {
  api: WorkspaceApi
  folder: number
  folderName: string
  onDone: (imported: number) => void
  onClose: () => void
}) {
  const [file, setFile] = useState<File | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function run() {
    if (!file) return
    setBusy(true)
    setError('')
    try {
      const text = await readText(file)
      const r = await api.importFile(text, importType(text), folder > 0 ? folder : undefined)
      onDone(r.imported)
    } catch (err) {
      if (!isCancelled(err)) setError(serverMessage(err, 'The file could not be imported.'))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog title="Import bookmarks" onClose={onClose}>
      <div className="stack compact">
        <p className="muted">Import a Netscape HTML or XBEL export into <strong>{folderName}</strong>. Nothing is replaced; imported folders are added.</p>
        {error && <p className="error" role="alert">{error}</p>}
        <label className="field">
          <span>Bookmarks file</span>
          <input type="file" accept=".html,.htm,.xml,.xbel,text/html,application/xml,text/xml" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
        </label>
        <div className="actions">
          <button type="button" className="btn ghost" onClick={onClose}>Cancel</button>
          <button type="button" className="btn primary" disabled={!file || busy} onClick={run}>Import</button>
        </div>
      </div>
    </Dialog>
  )
}

export function ExportDialog({
  api,
  folder,
  folderName,
  onDone,
  onClose,
}: {
  api: WorkspaceApi
  folder: number
  folderName: string
  onDone: () => void
  onClose: () => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function run(format: 'netscape' | 'xbel') {
    setBusy(true)
    setError('')
    try {
      const text = await api.exportFile(format, folder > 0 ? folder : undefined)
      const type = format === 'xbel' ? 'application/xbel+xml' : 'text/html'
      const url = URL.createObjectURL(new Blob([text], { type }))
      const a = document.createElement('a')
      a.href = url
      a.download = format === 'xbel' ? 'bookmarks.xbel' : 'bookmarks.html'
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
      onDone()
    } catch (err) {
      if (!isCancelled(err)) setError(serverMessage(err, 'The export could not be created.'))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog title="Export bookmarks" onClose={onClose}>
      <div className="stack compact">
        <p className="muted">Exports <strong>{folderName}</strong> as a file you can import into any browser.</p>
        {error && <p className="error" role="alert">{error}</p>}
        <div className="actions start">
          <button type="button" className="btn outline" disabled={busy} onClick={() => run('netscape')}>Download Netscape HTML</button>
          <button type="button" className="btn outline" disabled={busy} onClick={() => run('xbel')}>Download XBEL</button>
        </div>
        <div className="actions">
          <button type="button" className="btn ghost" onClick={onClose}>Close</button>
        </div>
      </div>
    </Dialog>
  )
}
