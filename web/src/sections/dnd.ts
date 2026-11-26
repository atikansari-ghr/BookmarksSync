export type Drag = { kind: 'bookmarks'; ids: number[] } | { kind: 'folder'; id: number }
export type Drop = 'into' | 'before' | 'after'

/** Where on a target a pointer drop landed: the edges reorder, the middle moves into. */
export function dropWhere(e: { clientY: number; currentTarget: HTMLElement }, allowInto: boolean): Drop {
  const rect = e.currentTarget.getBoundingClientRect()
  if (!rect.height) return allowInto ? 'into' : 'before'
  const f = (e.clientY - rect.top) / rect.height
  if (allowInto) return f < 0.25 ? 'before' : f > 0.75 ? 'after' : 'into'
  return f < 0.5 ? 'before' : 'after'
}
