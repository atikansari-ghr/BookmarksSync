import { ROOT, type ChildRef, type TreeNode } from './api'

export interface FolderRow {
  node: TreeNode
  depth: number
  parentId: number
  hasChildren: boolean
}

export const folderChildren = (node: { children?: TreeNode[] }) => (node.children ?? []).filter((c) => c.type === 'folder')

/** The folders shown in the tree, in server order, descending only into expanded folders. */
export function visibleFolders(roots: TreeNode[], expanded: ReadonlySet<number>): FolderRow[] {
  const out: FolderRow[] = []
  const walk = (nodes: TreeNode[], depth: number, parentId: number) => {
    for (const node of nodes.filter((n) => n.type === 'folder')) {
      const kids = folderChildren(node)
      out.push({ node, depth, parentId, hasChildren: kids.length > 0 })
      if (kids.length > 0 && expanded.has(node.id)) walk(kids, depth + 1, node.id)
    }
  }
  walk(roots, 1, ROOT)
  return out
}

export function findFolder(roots: TreeNode[], id: number): { node: TreeNode; parentId: number; path: TreeNode[] } | undefined {
  const walk = (nodes: TreeNode[], parentId: number, path: TreeNode[]): ReturnType<typeof findFolder> => {
    for (const n of nodes) {
      if (n.type !== 'folder') continue
      if (n.id === id) return { node: n, parentId, path: [...path, n] }
      const hit = walk(n.children ?? [], n.id, [...path, n])
      if (hit) return hit
    }
    return undefined
  }
  return walk(roots, ROOT, [])
}

/** Ordered children (folders and bookmarks) of a folder, or of the collection root. */
export function childRefs(roots: TreeNode[], folderId: number): ChildRef[] {
  const list = folderId === ROOT ? roots : (findFolder(roots, folderId)?.node.children ?? [])
  return list.map((c) => ({ type: c.type, id: c.id }))
}

export function descendantIds(node: TreeNode): Set<number> {
  const out = new Set<number>()
  const walk = (n: TreeNode) => {
    for (const c of n.children ?? []) {
      if (c.type === 'folder') {
        out.add(c.id)
        walk(c)
      }
    }
  }
  walk(node)
  return out
}

export function folderPath(roots: TreeNode[], id: number): string {
  const hit = findFolder(roots, id)
  return hit ? hit.path.map((n) => n.title).join(' / ') : `Folder ${id}`
}

/** Places `moved` (in their given order) before or after `target`; the rest keep their order. */
export function placeRefs(children: ChildRef[], moved: ChildRef[], target: ChildRef, where: 'before' | 'after'): ChildRef[] {
  const same = (a: ChildRef, b: ChildRef) => a.type === b.type && a.id === b.id
  if (moved.some((m) => same(m, target))) return children
  const rest = children.filter((c) => !moved.some((m) => same(m, c)))
  const at = rest.findIndex((c) => same(c, target))
  if (at < 0) return children
  rest.splice(where === 'before' ? at : at + 1, 0, ...moved)
  return rest
}

export function sameOrder(a: ChildRef[], b: ChildRef[]) {
  return a.length === b.length && a.every((x, i) => x.type === b[i].type && x.id === b[i].id)
}

/** Only web links are rendered as anchors (never javascript: or data: bookmarks). */
export function safeHref(url: string): string | undefined {
  try {
    const u = new URL(url)
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : undefined
  } catch {
    return undefined
  }
}

export function parseTags(text: string): string[] {
  const seen = new Set<string>()
  for (const t of text.split(',')) {
    const v = t.trim()
    if (v) seen.add(v)
  }
  return [...seen]
}

export const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`
