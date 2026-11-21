import { err, sessionState, type Call, type Reply, type Route } from './server'

export interface FakeBookmark {
  id: number
  url: string
  title: string
  tags: string[]
  folders: number[]
  favorite: boolean
}

interface FakeFolder {
  id: number
  title: string
  parent: number // -1 = root
}

type Child = { type: 'folder' | 'bookmark'; id: number }

/**
 * fakeWorkspace is a small stateful stand-in for the folder/bookmark API so organizer
 * tests exercise reload-after-mutation behavior. Individual routes can be overridden.
 */
export function fakeWorkspace(opts: { shares?: unknown[] } = {}) {
  const folders: FakeFolder[] = [
    { id: 10, title: 'Bookmarks Bar', parent: -1 },
    { id: 20, title: 'Other Bookmarks', parent: -1 },
    { id: 11, title: 'Work', parent: 10 },
    { id: 12, title: 'News', parent: 10 },
  ]
  const bookmarks: FakeBookmark[] = [
    { id: 1, url: 'https://github.com', title: 'GitHub', tags: ['dev'], folders: [11], favorite: false },
    { id: 2, url: 'https://debian.org', title: 'Debian', tags: ['infra'], folders: [11], favorite: false },
    { id: 3, url: 'https://proxmox.com', title: 'Proxmox', tags: ['infra'], folders: [11], favorite: false },
    { id: 4, url: 'https://news.example', title: 'Daily News', tags: [], folders: [12], favorite: false },
  ]
  const order = new Map<number, Child[]>()
  const childrenOf = (id: number): Child[] => {
    let list = order.get(id)
    if (!list) {
      list = [
        ...folders.filter((f) => f.parent === id).map((f) => ({ type: 'folder' as const, id: f.id })),
        ...bookmarks.filter((b) => b.folders.includes(id)).map((b) => ({ type: 'bookmark' as const, id: b.id })),
      ]
      order.set(id, list)
    }
    return list
  }
  const state = {
    folders,
    bookmarks,
    order,
    trash: [
      { type: 'bookmark', id: 9, title: 'Old link', url: 'https://old.example', deletedAt: '2026-11-10T09:00:00Z', purgeAt: '2026-12-10T09:00:00Z' },
      { type: 'bookmark', id: 10, title: 'Stale page', url: 'https://stale.example', deletedAt: '2026-11-11T09:00:00Z', purgeAt: '2026-12-11T09:00:00Z' },
    ] as { type: string; id: number; title: string; url: string; deletedAt: string; purgeAt: string }[],
    nextId: 100,
    failBulkMove: false,
  }
  const node = (f: FakeFolder): unknown => ({
    id: f.id,
    type: 'folder',
    title: f.title,
    loaded: true,
    children: childrenOf(f.id).map((c) =>
      c.type === 'folder'
        ? node(folders.find((x) => x.id === c.id)!)
        : (() => {
            const b = bookmarks.find((x) => x.id === c.id)!
            return { id: b.id, type: 'bookmark', title: b.title, url: b.url }
          })(),
    ),
  })
  const ok = (body: unknown, status = 200): Reply => ({ status, body })
  const bm = (id: number) => bookmarks.find((b) => b.id === id)

  const routes: Record<string, Route> = {
    'GET /api/v1/version': ok({ version: '1.0.0' }),
    'GET /api/v1/auth/session': ok(sessionState()),
    'GET /api/v1/shares/incoming': ok({ shares: opts.shares ?? [] }),
    'GET /api/v1/shares/owned': ok({ shares: [] }),
    'GET /api/v1/folders': () => ok({ folders: folders.filter((f) => f.parent === -1).map(node) }),
    'GET /api/v1/bookmarks': (c: Call) => {
      const folder = Number(c.query.get('folder') ?? 0)
      const q = (c.query.get('q') ?? '').toLowerCase()
      const tags = c.query.getAll('tag')
      const fav = c.query.get('favorite')
      const items = bookmarks.filter(
        (b) =>
          (folder === 0 || b.folders.includes(folder)) &&
          (!q || b.title.toLowerCase().includes(q) || b.url.includes(q)) &&
          tags.every((t) => b.tags.includes(t)) &&
          (fav === null || b.favorite === (fav === 'true')),
      )
      return ok({ items, page: 0, limit: 200, hasMore: false })
    },
    'GET /api/v1/tags': () => {
      const counts = new Map<string, number>()
      bookmarks.forEach((b) => b.tags.forEach((t) => counts.set(t, (counts.get(t) ?? 0) + 1)))
      return ok({ tags: [...counts].map(([name, count]) => ({ name, count })) })
    },
    'POST /api/v1/bookmarks': (c: Call) => {
      const b = c.body as Omit<FakeBookmark, 'id' | 'favorite'>
      const made = { id: state.nextId++, url: b.url, title: b.title, tags: b.tags ?? [], folders: b.folders, favorite: false }
      bookmarks.push(made)
      b.folders.forEach((f) => childrenOf(f).push({ type: 'bookmark', id: made.id }))
      return ok(made, 201)
    },
    'POST /api/v1/bookmarks/bulk-move': (c: Call) => {
      if (state.failBulkMove) return err(409, 'conflict', 'another bookmark with this URL is already in the destination folder')
      const { bookmarkIds, sourceFolderId, folderId } = c.body as { bookmarkIds: number[]; sourceFolderId: number; folderId: number }
      for (const id of bookmarkIds) {
        const b = bm(id)!
        b.folders = [...b.folders.filter((f) => f !== sourceFolderId), folderId]
        order.set(sourceFolderId, childrenOf(sourceFolderId).filter((x) => !(x.type === 'bookmark' && x.id === id)))
        childrenOf(folderId).push({ type: 'bookmark', id })
      }
      return { status: 204 }
    },
    'PUT /api/v1/folders/11/order': (c: Call) => applyOrder(11, c),
    'PUT /api/v1/folders/10/order': (c: Call) => applyOrder(10, c),
    'POST /api/v1/folders': (c: Call) => {
      const { parentId, title } = c.body as { parentId: number; title: string }
      const f = { id: state.nextId++, title, parent: parentId }
      folders.push(f)
      childrenOf(parentId).push({ type: 'folder', id: f.id })
      return ok({ id: f.id, parentId, title, position: 0, canonical: false }, 201)
    },
    'GET /api/v1/trash': () => ok({ items: state.trash, page: 0, limit: 100, hasMore: false }),
  }
  function applyOrder(id: number, c: Call): Reply {
    order.set(id, (c.body as { children: Child[] }).children)
    return { status: 204 }
  }
  // Per-id routes are matched by a catch-all installed on demand.
  for (const b of bookmarks) addBookmarkRoutes(b.id)
  function addBookmarkRoutes(id: number) {
    routes[`PATCH /api/v1/bookmarks/${id}`] = (c: Call) => {
      const b = bm(id)!
      const patch = c.body as Partial<FakeBookmark>
      Object.assign(b, patch)
      return ok(b)
    }
    routes[`DELETE /api/v1/bookmarks/${id}`] = () => {
      const i = bookmarks.findIndex((b) => b.id === id)
      const [gone] = bookmarks.splice(i, 1)
      state.trash.push({ type: 'bookmark', id, title: gone.title, url: gone.url, deletedAt: '2026-11-19T09:00:00Z', purgeAt: '2026-12-19T09:00:00Z' })
      order.forEach((list, k) => order.set(k, list.filter((x) => !(x.type === 'bookmark' && x.id === id))))
      return { status: 204 }
    }
    routes[`PUT /api/v1/bookmarks/${id}/favorite`] = () => {
      bm(id)!.favorite = true
      return { status: 204 }
    }
    routes[`DELETE /api/v1/bookmarks/${id}/favorite`] = () => {
      bm(id)!.favorite = false
      return { status: 204 }
    }
  }
  for (const t of [9, 10]) {
    routes[`POST /api/v1/trash/bookmark/${t}/restore`] = () => {
      state.trash = state.trash.filter((x) => x.id !== t)
      return ok({ item: { id: t, type: 'bookmark', title: 'Restored' } })
    }
    routes[`DELETE /api/v1/trash/bookmark/${t}`] = () => {
      state.trash = state.trash.filter((x) => x.id !== t)
      return { status: 204 }
    }
  }
  routes['DELETE /api/v1/trash'] = () => {
    state.trash = []
    return { status: 204 }
  }
  routes['POST /api/v1/import'] = ok({ imported: 3, folders: [] }, 201)
  routes['GET /api/v1/export'] = { status: 200, body: undefined, headers: { 'Content-Type': 'text/html', 'Content-Disposition': 'attachment; filename="bookmarks.html"' } }

  return { routes, state, addBookmarkRoutes }
}
