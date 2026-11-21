import type { DragEvent } from 'react'
import { ROOT, type TreeNode } from '../api'
import { ChevronIcon, FolderIcon } from '../components/icons'
import { visibleFolders } from '../workspace'
import { dropWhere, type Drag, type Drop } from './dnd'

interface Props {
  roots: TreeNode[]
  expanded: ReadonlySet<number>
  /** 0 selects "All bookmarks". */
  selected: number
  onSelect: (id: number) => void
  onToggle: (id: number) => void
  /** Folders cannot be dragged in a read-only collection. */
  canEdit: boolean
  /** Whether top-level folders are the server's fixed roots (they cannot move). */
  fixedRoots: boolean
  drag: Drag | null
  onDragStart: (id: number, e: DragEvent) => void
  onDragEnd: () => void
  onDrop: (id: number, where: Drop) => void
  /** The folder a keyboard pick-up currently points at. */
  target: number | null
  carrying: number | null
}

/** The folder tree: an ARIA tree with pointer drag/drop targets; keys are handled by the organizer. */
export function FolderTree(p: Props) {
  const rows = visibleFolders(p.roots, p.expanded)
  const rovingId = p.selected === 0 ? 0 : (rows.find((r) => r.node.id === p.selected)?.node.id ?? 0)
  return (
    <ul role="tree" aria-label="Folders" className="tree">
      <li role="none">
        <div
          role="treeitem"
          aria-level={1}
          aria-selected={p.selected === 0}
          tabIndex={rovingId === 0 ? 0 : -1}
          data-folder-id="0"
          className={p.selected === 0 ? 'tree-item selected' : 'tree-item'}
          onClick={() => p.onSelect(0)}
        >
          <span className="tree-gap" aria-hidden="true" />
          <span className="tree-label">All bookmarks</span>
        </div>
      </li>
      {rows.map(({ node, depth, hasChildren, parentId }) => {
        const draggable = p.canEdit && !(p.fixedRoots && parentId === ROOT)
        const accepts = p.canEdit && p.drag !== null && !(p.drag.kind === 'folder' && p.drag.id === node.id)
        const cls = ['tree-item', p.selected === node.id && 'selected', p.target === node.id && 'drop-target', p.carrying === node.id && 'carried']
        return (
          <li role="none" key={node.id}>
            <div
              role="treeitem"
              aria-level={depth + 1}
              aria-selected={p.selected === node.id}
              aria-expanded={hasChildren ? p.expanded.has(node.id) : undefined}
              tabIndex={rovingId === node.id ? 0 : -1}
              data-folder-id={node.id}
              draggable={draggable}
              className={cls.filter(Boolean).join(' ')}
              onClick={() => p.onSelect(node.id)}
              onDragStart={(e) => draggable && p.onDragStart(node.id, e)}
              onDragEnd={p.onDragEnd}
              onDragOver={(e) => {
                if (accepts) {
                  e.preventDefault()
                  e.dataTransfer.dropEffect = 'move'
                }
              }}
              onDrop={(e) => {
                if (!accepts) return
                e.preventDefault()
                p.onDrop(node.id, p.drag?.kind === 'folder' ? dropWhere(e, true) : 'into')
              }}
            >
              <span className="tree-indent" aria-hidden="true">
                {Array.from({ length: depth - 1 }, (_, i) => <span key={i} className="tree-pad" />)}
              </span>
              {hasChildren ? (
                <span
                  className="tree-toggle"
                  aria-hidden="true"
                  onClick={(e) => {
                    e.stopPropagation()
                    p.onToggle(node.id)
                  }}
                >
                  <ChevronIcon open={p.expanded.has(node.id)} />
                </span>
              ) : (
                <span className="tree-gap" aria-hidden="true" />
              )}
              <span className="tree-icon" aria-hidden="true"><FolderIcon /></span>
              <span className="tree-label">{node.title}</span>
            </div>
          </li>
        )
      })}
    </ul>
  )
}
