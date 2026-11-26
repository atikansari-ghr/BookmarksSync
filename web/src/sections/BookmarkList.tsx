import type { DragEvent } from 'react'
import type { Bookmark } from '../api'
import { StarIcon } from '../components/icons'
import { safeHref } from '../workspace'
import { dropWhere, type Drop } from './dnd'

interface Props {
  items: Bookmark[]
  layout: 'list' | 'grid'
  canEdit: boolean
  /** Rows can be dragged (a folder is open, no search active). */
  canMove: boolean
  selection: ReadonlySet<number>
  onSelect: (id: number, on: boolean) => void
  onEdit: (b: Bookmark) => void
  onDelete: (b: Bookmark) => void
  onFavorite: (b: Bookmark) => void
  onDragStart: (id: number, e: DragEvent) => void
  onDragEnd: () => void
  onDrop: (id: number, where: Drop) => void
  dragging: boolean
  rovingId: number
  onRove: (id: number) => void
  carried: ReadonlySet<number>
}

/** Bookmarks of the open view, list-first with an optional grid layout. */
export function BookmarkList(p: Props) {
  return (
    <ul className={p.layout === 'grid' ? 'bm-list grid' : 'bm-list'} aria-label="Bookmarks">
      {p.items.map((b) => {
        const href = safeHref(b.url)
        const draggable = p.canEdit && p.canMove
        const cls = ['bm-item', p.selection.has(b.id) && 'selected', p.carried.has(b.id) && 'carried']
        return (
          <li
            key={b.id}
            data-bm-id={b.id}
            tabIndex={b.id === p.rovingId ? 0 : -1}
            draggable={draggable}
            className={cls.filter(Boolean).join(' ')}
            onFocus={(e) => {
              if (e.target === e.currentTarget) p.onRove(b.id)
            }}
            onDragStart={(e) => draggable && p.onDragStart(b.id, e)}
            onDragEnd={p.onDragEnd}
            onDragOver={(e) => {
              if (draggable && p.dragging) {
                e.preventDefault()
                e.dataTransfer.dropEffect = 'move'
              }
            }}
            onDrop={(e) => {
              if (!draggable || !p.dragging) return
              e.preventDefault()
              p.onDrop(b.id, dropWhere(e, false))
            }}
          >
            {p.canEdit && (
              <input
                type="checkbox"
                className="bm-check"
                aria-label={`Select ${b.title}`}
                checked={p.selection.has(b.id)}
                onChange={(e) => p.onSelect(b.id, e.target.checked)}
              />
            )}
            <div className="bm-main">
              {href ? (
                <a className="bm-title" href={href} target="_blank" rel="noopener noreferrer">{b.title}</a>
              ) : (
                <span className="bm-title">{b.title}</span>
              )}
              <span className="bm-url">{b.url}</span>
              {b.tags.length > 0 && (
                <span className="bm-tags">
                  {b.tags.map((t) => <span key={t} className="tag">{t}</span>)}
                </span>
              )}
            </div>
            <div className="bm-actions">
              <button
                type="button"
                className={b.favorite ? 'icon-btn on' : 'icon-btn'}
                aria-pressed={b.favorite}
                aria-label={b.favorite ? `Remove ${b.title} from favorites` : `Add ${b.title} to favorites`}
                onClick={() => p.onFavorite(b)}
              >
                <StarIcon filled={b.favorite} />
              </button>
              {p.canEdit && (
                <>
                  <button type="button" className="btn ghost small" aria-label={`Edit ${b.title}`} onClick={() => p.onEdit(b)}>Edit</button>
                  <button type="button" className="btn ghost small" aria-label={`Delete ${b.title}`} onClick={() => p.onDelete(b)}>Delete</button>
                </>
              )}
            </div>
          </li>
        )
      })}
    </ul>
  )
}
