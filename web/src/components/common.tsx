import { useEffect, useId, useState, type ReactNode } from 'react'

export const COPYRIGHT = '© 2026 Atik Ansari'

/** Runtime version and copyright, from the public /api/v1/version endpoint. */
export function Footer() {
  const [version, setVersion] = useState('')
  useEffect(() => {
    let live = true
    fetch('/api/v1/version', { credentials: 'same-origin', cache: 'no-store' })
      .then((r) => (r.ok ? r.json() : null))
      .then((d: { version?: string } | null) => {
        if (live && d?.version) setVersion(d.version)
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [])
  return (
    <footer className="site-footer">
      <p>{version ? `${/^\d/.test(version) ? 'v' : ''}${version} · ${COPYRIGHT}` : COPYRIGHT}</p>
    </footer>
  )
}

/** Copies text to the clipboard when the browser allows it. */
export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'failed'>('idle')
  return (
    <button
      type="button"
      className="btn ghost small"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text)
          setState('copied')
        } catch {
          setState('failed')
        }
      }}
    >
      {state === 'copied' ? 'Copied' : state === 'failed' ? 'Copy failed: select and copy' : label}
    </button>
  )
}

/** A one-time secret display: shown until the user confirms they saved it. */
export function Reveal({ title, children, onDone, doneLabel = 'Done' }: { title: string; children: ReactNode; onDone: () => void; doneLabel?: string }) {
  const id = useId()
  return (
    <div className="reveal" role="group" aria-labelledby={id}>
      <h3 id={id}>{title}</h3>
      {children}
      <button type="button" className="btn primary" onClick={onDone}>{doneLabel}</button>
    </div>
  )
}

export function Section({ title, children, intro }: { title: string; intro?: ReactNode; children: ReactNode }) {
  const id = useId()
  return (
    <section className="panel glass" aria-labelledby={id}>
      <h2 id={id}>{title}</h2>
      {intro && <p className="muted">{intro}</p>}
      {children}
    </section>
  )
}

export function formatTime(iso?: string | null): string {
  if (!iso) return 'never'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? 'unknown' : d.toLocaleString()
}

export function Busy({ label = 'Loading…' }: { label?: string }) {
  return <p className="muted" aria-busy="true">{label}</p>
}
