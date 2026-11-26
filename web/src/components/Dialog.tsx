import { useEffect, useId, useRef, type ReactNode } from 'react'

interface DialogProps {
  title: string
  onClose: () => void
  children: ReactNode
  /** Element to focus first; defaults to the first focusable control. */
  initialFocus?: React.RefObject<HTMLElement | null>
}

const focusable = 'button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href]'

/** A modal dialog: labelled, focus-trapped, closed with Escape, focus restored on close. */
export function Dialog({ title, onClose, children, initialFocus }: DialogProps) {
  const id = useId()
  const ref = useRef<HTMLDivElement>(null)
  const closeRef = useRef(onClose)
  closeRef.current = onClose

  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null
    const first = initialFocus?.current ?? ref.current?.querySelector<HTMLElement>(focusable)
    first?.focus()
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        closeRef.current()
      } else if (e.key === 'Tab' && ref.current) {
        const items = Array.from(ref.current.querySelectorAll<HTMLElement>(focusable))
        if (items.length === 0) return
        const [head, tail] = [items[0], items[items.length - 1]]
        if (e.shiftKey && document.activeElement === head) {
          e.preventDefault()
          tail.focus()
        } else if (!e.shiftKey && document.activeElement === tail) {
          e.preventDefault()
          head.focus()
        }
      }
    }
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('keydown', onKey)
      previous?.focus?.()
    }
  }, [initialFocus])

  return (
    <div className="dialog-backdrop">
      <div ref={ref} className="dialog glass" role="dialog" aria-modal="true" aria-labelledby={id}>
        <h2 id={id}>{title}</h2>
        {children}
      </div>
    </div>
  )
}

interface ConfirmProps {
  title: string
  message: ReactNode
  confirm: string
  busy?: boolean
  error?: string
  onConfirm: () => void
  onCancel: () => void
}

/** A destructive-action confirmation. Focus starts on Cancel. */
export function ConfirmDialog({ title, message, confirm, busy, error, onConfirm, onCancel }: ConfirmProps) {
  const cancel = useRef<HTMLButtonElement>(null)
  return (
    <Dialog title={title} onClose={onCancel} initialFocus={cancel}>
      <p>{message}</p>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="actions">
        <button type="button" ref={cancel} className="btn ghost" onClick={onCancel}>Cancel</button>
        <button type="button" className="btn danger" disabled={busy} onClick={onConfirm}>{confirm}</button>
      </div>
    </Dialog>
  )
}
