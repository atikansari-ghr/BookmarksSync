import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent, type KeyboardEvent } from 'react'
import { isCancelled, ROOT, serverMessage, type Bookmark, type ChildRef, type Tag, type TreeNode } from '../api'
import { Busy } from '../components/common'
import { ConfirmDialog } from '../components/Dialog'
import { GridIcon, ListIcon } from '../components/icons'
import { Workspace } from '../components/Workspace'
import { useApp } from '../context'
import { BookmarkList } from '../sections/BookmarkList'
import { type Drag, type Drop } from '../sections/dnd'
import { BookmarkDialog, FolderDialog } from '../sections/EditDialogs'
import { FolderTree } from '../sections/FolderTree'
import { LinkChecks } from '../sections/LinkChecks'
import { ShareDialog } from '../sections/ShareDialog'
import { ExportDialog, ImportDialog } from '../sections/TransferDialogs'
import { TrashPanel } from '../sections/TrashPanel'
import { useScopes } from '../sections/useScopes'
import { childRefs, descendantIds, findFolder, folderChildren, folderPath, placeRefs, plural, sameOrder, visibleFolders } from '../workspace'

type Load<T> = { state: 'loading' } | { state: 'ready'; data: T } | { state: 'error'; message: string }

interface Carry {
  drag: Drag
  targets: number[]
  index: number
}

type Confirm = { kind: 'bookmark'; bookmark: Bookmark } | { kind: 'folder'; id: number; title: string }
type Dialogs =
  | { kind: 'new-bookmark' }
  | { kind: 'edit-bookmark'; bookmark: Bookmark }
  | { kind: 'new-folder' }
  | { kind: 'rename-folder' }
  | { kind: 'import' }
  | { kind: 'export' }
  | { kind: 'share' }

const HELP = 'Keyboard: Space picks up the selected bookmarks or the focused folder, arrow keys choose a destination folder, Enter drops, Escape cancels. Alt+Up and Alt+Down reorder.'

/** The bookmark organizer: folder tree, bookmark list, search, tags, shares, import/export and Trash. */
export function OrganizerPage() {
  return (
    <Workspace current="bookmarks" title="My Bookmarks" subtitle="Your bookmarks. Your browsers. Your control.">
      <Organizer />
    </Workspace>
  )
}

function Organizer() {
  const { client } = useApp()
  const { scopes, active, setActive, api, shareError } = useScopes(client)
  // A collection switch starts from a clean view.
  return <Collection key={active.key} scopes={scopes} active={active} setActive={setActive} api={api} shareError={shareError} />
}

function Collection({ scopes, active, setActive, api, shareError }: Omit<ReturnType<typeof useScopes>, never>) {
  const { client } = useApp()
  const canWrite = !active.readOnly
  const isPrivate = active.share === undefined

  const [tree, setTree] = useState<Load<TreeNode[]>>({ state: 'loading' })
  const [expanded, setExpanded] = useState<Set<number>>(new Set())
  const [selectedId, setSelectedId] = useState(0)
  const [qInput, setQInput] = useState('')
  const [q, setQ] = useState('')
  const [activeTags, setActiveTags] = useState<string[]>([])
  const [favOnly, setFavOnly] = useState(false)
  const [layout, setLayout] = useState<'list' | 'grid'>('list')
  const [tags, setTags] = useState<Tag[]>([])
  const [list, setList] = useState<Load<{ items: Bookmark[]; hasMore: boolean }>>({ state: 'loading' })
  const [rev, setRev] = useState(0)
  const [selection, setSelection] = useState<Set<number>>(new Set())
  const [view, setView] = useState<'bookmarks' | 'trash'>('bookmarks')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const [dialog, setDialog] = useState<Dialogs | null>(null)
  const [confirm, setConfirm] = useState<Confirm | null>(null)
  const [confirmBusy, setConfirmBusy] = useState(false)
  const [confirmError, setConfirmError] = useState('')
  const [drag, setDrag] = useState<Drag | null>(null)
  const [carry, setCarry] = useState<Carry | null>(null)
  const [announce, setAnnounce] = useState('')
  const [roving, setRoving] = useState(0)
  const seq = useRef(0)
  const initialised = useRef(false)

  const roots = tree.state === 'ready' ? tree.data : []
  const selected = selectedId === 0 || findFolder(roots, selectedId) ? selectedId : 0
  const selectedFolder = selected > 0 ? findFolder(roots, selected) : undefined
  const searching = q !== ''
  const canMove = canWrite && selected > 0 && !searching

  const loadTree = useCallback(async () => {
    try {
      const d = await api.folders()
      const nodes = d.folders ?? []
      setTree({ state: 'ready', data: nodes })
      if (!initialised.current) {
        initialised.current = true
        setExpanded(new Set(nodes.filter((n) => n.type === 'folder').map((n) => n.id)))
      }
    } catch (err) {
      if (isCancelled(err)) return
      setTree((prev) => {
        if (prev.state === 'ready') {
          setError(`The folder tree could not be refreshed: ${serverMessage(err)}`)
          return prev
        }
        return { state: 'error', message: serverMessage(err) }
      })
    }
  }, [api])

  const loadTags = useCallback(async () => {
    try {
      setTags((await api.tags()).tags ?? [])
    } catch {
      // Tag chips are optional; the list and search still work without them.
    }
  }, [api])

  const refresh = useCallback(() => {
    void loadTree()
    void loadTags()
    setRev((r) => r + 1)
  }, [loadTree, loadTags])

  useEffect(() => {
    void loadTree()
    void loadTags()
  }, [loadTree, loadTags])

  // Debounce the search box.
  useEffect(() => {
    const t = setTimeout(() => setQ(qInput.trim()), 250)
    return () => clearTimeout(t)
  }, [qInput])

  const treeReady = tree.state === 'ready'
  const tagKey = activeTags.join('\u0000')
  const loadList = useCallback(
    async (page: number) => {
      const mine = ++seq.current
      try {
        const r = await api.listBookmarks({ folder: searching ? 0 : selected, q, tags: tagKey ? tagKey.split('\u0000') : undefined, favorite: favOnly ? true : undefined, page })
        if (mine !== seq.current) return
        setList((prev) => ({
          state: 'ready',
          data: { items: page > 0 && prev.state === 'ready' ? [...prev.data.items, ...r.items] : r.items, hasMore: r.hasMore },
        }))
      } catch (err) {
        if (mine === seq.current && !isCancelled(err)) setList({ state: 'error', message: serverMessage(err) })
      }
    },
    [api, searching, selected, q, tagKey, favOnly],
  )
  const page = useRef(0)
  useEffect(() => {
    if (!treeReady) return
    page.current = 0
    void loadList(0)
  }, [treeReady, loadList, rev])

  // The server's folder order is the display order.
  const shown = useMemo(() => {
    if (list.state !== 'ready') return []
    if (selected <= 0 || searching) return list.data.items
    const index = new Map<number, number>()
    childRefs(roots, selected).forEach((c, i) => c.type === 'bookmark' && index.set(c.id, i))
    return [...list.data.items].sort((a, b) => (index.get(a.id) ?? 1e9) - (index.get(b.id) ?? 1e9))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [list, selected, searching, tree])

  // Drop selections that are no longer on screen.
  useEffect(() => {
    setSelection((prev) => {
      const present = new Set(shown.map((b) => b.id))
      const next = new Set([...prev].filter((id) => present.has(id)))
      return next.size === prev.size ? prev : next
    })
  }, [shown])

  const folderChoices = useMemo(
    () => visibleFolders(roots, new Set(allIds(roots))).map((r) => ({ id: r.node.id, path: folderPath(roots, r.node.id) })),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [tree],
  )

  async function act(fn: () => Promise<string | void>) {
    setError('')
    setNotice('')
    try {
      const done = await fn()
      if (done) setNotice(done)
    } catch (err) {
      if (!isCancelled(err)) setError(serverMessage(err))
    } finally {
      // Always show what the server holds now: a rejected change leaves the view as it was.
      refresh()
    }
  }

  const titleOf = (id: number) => findFolder(roots, id)?.node.title ?? 'the folder'

  function moveBookmarks(ids: number[], dest: number) {
    if (!canMove || ids.length === 0 || dest === selected) return
    return act(async () => {
      await api.bulkMove(ids, selected, dest)
      setSelection(new Set())
      return `Moved ${plural(ids.length, 'bookmark')} to ${titleOf(dest)}.`
    })
  }

  function moveFolder(id: number, dest: number) {
    const me = findFolder(roots, id)
    if (!canWrite || !me || id === dest || descendantIds(me.node).has(dest)) return
    return act(async () => {
      await api.updateFolder(id, { parentId: dest })
      setExpanded((e) => new Set(e).add(dest))
      return `Moved ${me.node.title} into ${titleOf(dest)}.`
    })
  }

  function reorder(parent: number, moved: ChildRef[], target: ChildRef, where: 'before' | 'after') {
    const current = childRefs(roots, parent)
    const next = placeRefs(current, moved, target, where)
    if (sameOrder(current, next)) return
    return act(async () => {
      await api.reorder(parent, next)
      return 'Order saved.'
    })
  }

  function placeFolderNear(id: number, targetId: number, where: 'before' | 'after') {
    const me = findFolder(roots, id)
    const t = findFolder(roots, targetId)
    if (!me || !t || id === targetId || descendantIds(me.node).has(targetId)) return
    if (me.parentId === t.parentId) return reorder(t.parentId, [{ type: 'folder', id }], { type: 'folder', id: targetId }, where)
    return act(async () => {
      await api.updateFolder(id, { parentId: t.parentId })
      const base = childRefs(roots, t.parentId)
      const mine: ChildRef = { type: 'folder', id }
      await api.reorder(t.parentId, placeRefs([...base, mine], [mine], { type: 'folder', id: targetId }, where))
      return `Moved ${me.node.title}.`
    })
  }

  // ---- pointer drag and drop ----
  function dragBookmark(id: number, e: DragEvent) {
    const ids = selection.has(id) ? shown.filter((b) => selection.has(b.id)).map((b) => b.id) : [id]
    setDrag({ kind: 'bookmarks', ids })
    e.dataTransfer?.setData('text/plain', ids.join(','))
    if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
  }
  function dragFolder(id: number, e: DragEvent) {
    setDrag({ kind: 'folder', id })
    e.dataTransfer?.setData('text/plain', `folder:${id}`)
    if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move'
  }
  function dropOnFolder(id: number, where: Drop) {
    const d = drag
    setDrag(null)
    if (!d) return
    if (d.kind === 'bookmarks') return void moveBookmarks(d.ids, id)
    if (where === 'into') return void moveFolder(d.id, id)
    void placeFolderNear(d.id, id, where)
  }
  function dropOnBookmark(id: number, where: Drop) {
    const d = drag
    setDrag(null)
    if (!d || d.kind !== 'bookmarks' || !canMove || where === 'into') return
    void reorder(selected, d.ids.map((i) => ({ type: 'bookmark', id: i })), { type: 'bookmark', id }, where)
  }

  // ---- keyboard: pick up, choose a folder, drop; Alt+arrows reorder ----
  function pickUp(drag: Drag, targets: number[], what: string) {
    if (targets.length === 0) return setAnnounce('There is no folder to move to.')
    setCarry({ drag, targets, index: -1 })
    setAnnounce(`Picked up ${what}. Use the arrow keys to choose a folder, Enter to drop, Escape to cancel.`)
  }

  function startBookmarkPickUp(id: number) {
    if (!canWrite) return
    if (!canMove) return setAnnounce(searching ? 'Clear the search to move bookmarks.' : 'Open a folder to move bookmarks.')
    const ids = selection.has(id) ? shown.filter((b) => selection.has(b.id)).map((b) => b.id) : [id]
    const targets = visibleFolders(roots, expanded).filter((r) => r.node.id !== selected).map((r) => r.node.id)
    pickUp({ kind: 'bookmarks', ids }, targets, plural(ids.length, 'bookmark'))
  }

  function startFolderPickUp(id: number) {
    const me = findFolder(roots, id)
    if (!canWrite || !me) return
    if (isPrivate && me.parentId === ROOT) return setAnnounce('This folder cannot be moved.')
    const banned = descendantIds(me.node).add(id)
    const targets = visibleFolders(roots, expanded).filter((r) => !banned.has(r.node.id) && r.node.id !== me.parentId).map((r) => r.node.id)
    pickUp({ kind: 'folder', id }, targets, `folder ${me.node.title}`)
  }

  function carryKey(e: KeyboardEvent) {
    if (!carry) return
    const n = carry.targets.length
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const index = e.key === 'ArrowDown' ? (carry.index + 1) % n : carry.index <= 0 ? n - 1 : carry.index - 1
      setCarry({ ...carry, index })
      setAnnounce(`Move to ${titleOf(carry.targets[index])}. Press Enter to drop, Escape to cancel.`)
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (carry.index < 0) return setAnnounce('Choose a destination folder with the arrow keys first.')
      const dest = carry.targets[carry.index]
      const d = carry.drag
      setCarry(null)
      setAnnounce('')
      if (d.kind === 'bookmarks') void moveBookmarks(d.ids, dest)
      else void moveFolder(d.id, dest)
    } else if (e.key === 'Escape' || e.key === 'Tab') {
      if (e.key === 'Escape') e.preventDefault()
      setCarry(null)
      setAnnounce('Move cancelled.')
    }
  }

  function focusSibling(current: HTMLElement, selector: string, step: number) {
    const all = Array.from(current.closest('[data-keynav]')?.querySelectorAll<HTMLElement>(selector) ?? [])
    const next = all[all.indexOf(current) + step]
    next?.focus()
  }

  function onKeyDown(e: KeyboardEvent<HTMLElement>) {
    if (carry) return carryKey(e)
    const t = e.target as HTMLElement
    const row = t.closest<HTMLElement>('[data-bm-id]')
    const item = t.closest<HTMLElement>('[data-folder-id]')
    if (row && t === row) {
      const id = Number(row.dataset.bmId)
      if (e.key === ' ') {
        e.preventDefault()
        startBookmarkPickUp(id)
      } else if (e.altKey && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
        e.preventDefault()
        if (!canMove) return
        const at = shown.findIndex((b) => b.id === id)
        const other = shown[at + (e.key === 'ArrowDown' ? 1 : -1)]
        if (other) void reorder(selected, [{ type: 'bookmark', id }], { type: 'bookmark', id: other.id }, e.key === 'ArrowDown' ? 'after' : 'before')
      } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault()
        focusSibling(row, '[data-bm-id]', e.key === 'ArrowDown' ? 1 : -1)
      }
    } else if (item && t === item) {
      const id = Number(item.dataset.folderId)
      const me = id > 0 ? findFolder(roots, id) : undefined
      if (e.altKey && (e.key === 'ArrowDown' || e.key === 'ArrowUp')) {
        e.preventDefault()
        if (!canWrite || !me) return
        const parent = me.parentId === ROOT ? roots : (findFolder(roots, me.parentId)?.node.children ?? [])
        const sibs = folderChildren({ children: parent })
        const other = sibs[sibs.findIndex((s) => s.id === id) + (e.key === 'ArrowDown' ? 1 : -1)]
        if (other) void reorder(me.parentId, [{ type: 'folder', id }], { type: 'folder', id: other.id }, e.key === 'ArrowDown' ? 'after' : 'before')
      } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
        e.preventDefault()
        focusSibling(item, '[data-folder-id]', e.key === 'ArrowDown' ? 1 : -1)
      } else if (e.key === 'ArrowRight' && me && folderChildren(me.node).length > 0 && !expanded.has(id)) {
        e.preventDefault()
        toggle(id)
      } else if (e.key === 'ArrowLeft' && me && expanded.has(id)) {
        e.preventDefault()
        toggle(id)
      } else if (e.key === 'Enter') {
        e.preventDefault()
        selectFolder(id)
      } else if (e.key === ' ') {
        e.preventDefault()
        if (id > 0) startFolderPickUp(id)
      }
    }
  }

  function toggle(id: number) {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function selectFolder(id: number) {
    setSelectedId(id)
    setSelection(new Set())
    setView('bookmarks')
  }

  async function runConfirm() {
    if (!confirm) return
    setConfirmBusy(true)
    setConfirmError('')
    try {
      if (confirm.kind === 'bookmark') {
        await api.deleteBookmark(confirm.bookmark.id)
        setNotice(`Moved “${confirm.bookmark.title}” to Trash.`)
      } else {
        await api.deleteFolder(confirm.id)
        if (selectedId === confirm.id) setSelectedId(0)
        setNotice(`Moved folder “${confirm.title}” to Trash.`)
      }
      setError('')
      setConfirm(null)
      refresh()
    } catch (err) {
      if (!isCancelled(err)) setConfirmError(serverMessage(err))
      else setConfirm(null)
    } finally {
      setConfirmBusy(false)
    }
  }

  const canEditFolder = canWrite && selectedFolder !== undefined && !(isPrivate && selectedFolder.parentId === ROOT)
  const heading = searching ? 'Search results' : selectedFolder ? selectedFolder.node.title : 'All bookmarks'
  const target = carry && carry.index >= 0 ? carry.targets[carry.index] : null
  const carriedBookmarks = new Set(carry?.drag.kind === 'bookmarks' ? carry.drag.ids : [])
  const items = shown
  const rovingId = items.some((b) => b.id === roving) ? roving : (items[0]?.id ?? 0)
  const exportName = selectedFolder ? selectedFolder.node.title : 'all bookmarks'

  return (
    <div className="organizer" onKeyDown={onKeyDown}>
      <section className="scopes" aria-label="Collections">
        {scopes.map((s) => (
          <button key={s.key} type="button" className={s.key === active.key ? 'scope active' : 'scope'} aria-pressed={s.key === active.key} onClick={() => setActive(s.key)}>{s.label}</button>
        ))}
        {shareError && <span className="muted small">{shareError}</span>}
      </section>

      {notice && (
        <p className="notice" role="status">
          {notice}
          <button type="button" className="link-button" onClick={() => setNotice('')} aria-label="Dismiss message">Dismiss</button>
        </p>
      )}
      {error && (
        <p className="error" role="alert">
          {error}{' '}
          <button type="button" className="link-button" onClick={() => setError('')} aria-label="Dismiss error">Dismiss</button>
        </p>
      )}
      {active.readOnly && <p className="note" role="note">This shared folder is view-only. You can browse, search, export and mark favorites, but nothing here can be changed.</p>}

      {tree.state === 'loading' && <Busy label="Loading your folders…" />}
      {tree.state === 'error' && (
        <div className="error" role="alert">
          Your folders could not be loaded: {tree.message}{' '}
          <button type="button" className="btn outline small" onClick={refresh}>Try again</button>
        </div>
      )}

      {tree.state === 'ready' && (
        <div className="ws-card glass">
          <div className="ws-toolbar">
            <input
              type="search"
              className="ws-search"
              aria-label="Search bookmarks"
              placeholder="Search bookmarks…"
              value={qInput}
              onChange={(e) => setQInput(e.target.value)}
            />
            <div className="seg" role="group" aria-label="Layout">
              <button type="button" className="icon-btn" aria-label="List view" aria-pressed={layout === 'list'} onClick={() => setLayout('list')}><ListIcon /></button>
              <button type="button" className="icon-btn" aria-label="Grid view" aria-pressed={layout === 'grid'} onClick={() => setLayout('grid')}><GridIcon /></button>
            </div>
            {canWrite && <button type="button" className="btn outline" onClick={() => setDialog({ kind: 'new-folder' })}>New folder</button>}
            {canWrite && <button type="button" className="btn primary" onClick={() => setDialog({ kind: 'new-bookmark' })}>New bookmark</button>}
          </div>
          <div className="ws-subtools">
            <button type="button" className="btn ghost small" aria-pressed={favOnly} onClick={() => setFavOnly((v) => !v)}>Favorites only</button>
            {canWrite && <button type="button" className="btn ghost small" onClick={() => setDialog({ kind: 'import' })}>Import</button>}
            <button type="button" className="btn ghost small" onClick={() => setDialog({ kind: 'export' })}>Export</button>
            {isPrivate && (
              <button type="button" className="btn ghost small" aria-pressed={view === 'trash'} onClick={() => setView(view === 'trash' ? 'bookmarks' : 'trash')}>
                {view === 'trash' ? 'Back to bookmarks' : 'Trash'}
              </button>
            )}
            {canEditFolder && <button type="button" className="btn ghost small" onClick={() => setDialog({ kind: 'rename-folder' })}>Rename folder</button>}
            {canEditFolder && <button type="button" className="btn ghost small" onClick={() => setConfirm({ kind: 'folder', id: selected, title: selectedFolder!.node.title })}>Delete folder</button>}
            {canEditFolder && isPrivate && <button type="button" className="btn ghost small" onClick={() => setDialog({ kind: 'share' })}>Share folder</button>}
          </div>
          <LinkChecks
            api={api}
            client={client}
            bookmarkIds={shown.filter((b) => selection.has(b.id)).map((b) => b.id)}
            canSubmit={canWrite}
            onNotice={setNotice}
          />

          {view === 'trash' ? (
            <TrashPanel api={api} onChanged={refresh} onNotice={setNotice} />
          ) : (
            <div className="ws-panes" data-keynav>
              <div className="pane-tree">
                <FolderTree
                  roots={roots}
                  expanded={expanded}
                  selected={selected}
                  onSelect={selectFolder}
                  onToggle={toggle}
                  canEdit={canWrite}
                  fixedRoots={isPrivate}
                  drag={drag}
                  onDragStart={dragFolder}
                  onDragEnd={() => setDrag(null)}
                  onDrop={dropOnFolder}
                  target={target}
                  carrying={carry?.drag.kind === 'folder' ? carry.drag.id : null}
                />
              </div>
              <section className="pane-list" aria-label={heading}>
                <div className="pane-head">
                  <h2>{heading}</h2>
                  {list.state === 'ready' && <p className="muted small">{plural(items.length, 'bookmark')}{list.data.hasMore ? ' loaded' : ''}</p>}
                </div>
                {tags.length > 0 && (
                  <div className="tag-bar" role="group" aria-label="Filter by tag">
                    {tags.map((t) => (
                      <button
                        key={t.name}
                        type="button"
                        className="tag-chip"
                        aria-pressed={activeTags.includes(t.name)}
                        onClick={() => setActiveTags((cur) => (cur.includes(t.name) ? cur.filter((x) => x !== t.name) : [...cur, t.name]))}
                      >
                        {t.name} <span className="count">{t.count}</span>
                      </button>
                    ))}
                  </div>
                )}
                {selection.size > 0 && (
                  <p className="selection-bar">
                    {plural(selection.size, 'bookmark')} selected
                    <button type="button" className="link-button" onClick={() => setSelection(new Set())}>Clear selection</button>
                  </p>
                )}
                {list.state === 'loading' && <Busy label="Loading bookmarks…" />}
                {list.state === 'error' && (
                  <div className="error" role="alert">
                    Bookmarks could not be loaded: {list.message}{' '}
                    <button type="button" className="btn outline small" onClick={() => void loadList(0)}>Try again</button>
                  </div>
                )}
                {list.state === 'ready' && items.length === 0 && (
                  <p className="muted">{searching || activeTags.length > 0 || favOnly ? 'No bookmarks match.' : 'No bookmarks here yet.'}</p>
                )}
                {list.state === 'ready' && items.length > 0 && (
                  <BookmarkList
                    items={items}
                    layout={layout}
                    canEdit={canWrite}
                    canMove={canMove}
                    selection={selection}
                    onSelect={(id, on) => setSelection((cur) => {
                      const next = new Set(cur)
                      if (on) next.add(id)
                      else next.delete(id)
                      return next
                    })}
                    onEdit={(b) => setDialog({ kind: 'edit-bookmark', bookmark: b })}
                    onDelete={(b) => setConfirm({ kind: 'bookmark', bookmark: b })}
                    onFavorite={(b) => void act(async () => { await api.setFavorite(b.id, !b.favorite) })}
                    onDragStart={dragBookmark}
                    onDragEnd={() => setDrag(null)}
                    onDrop={dropOnBookmark}
                    dragging={drag?.kind === 'bookmarks'}
                    rovingId={rovingId}
                    onRove={setRoving}
                    carried={carriedBookmarks}
                  />
                )}
                {list.state === 'ready' && list.data.hasMore && (
                  <button type="button" className="btn outline" onClick={() => { page.current += 1; void loadList(page.current) }}>Load more</button>
                )}
                {canWrite && <p className="muted small help">{HELP}</p>}
              </section>
            </div>
          )}
        </div>
      )}
      <p className="visually-hidden" aria-live="polite" data-testid="announcer">{announce}</p>

      {dialog?.kind === 'new-bookmark' && (
        <BookmarkDialog
          folders={folderChoices}
          initialFolder={selected > 0 ? selected : (folderChoices[0]?.id ?? 0)}
          onClose={() => setDialog(null)}
          onSave={async (v) => {
            await api.createBookmark({ url: v.url, title: v.title, folders: [v.folder], tags: v.tags })
            setDialog(null)
            setError('')
            setNotice('Bookmark saved.')
            refresh()
          }}
        />
      )}
      {dialog?.kind === 'edit-bookmark' && (
        <BookmarkDialog
          bookmark={dialog.bookmark}
          folders={folderChoices}
          initialFolder={0}
          onClose={() => setDialog(null)}
          onSave={async (v) => {
            await api.updateBookmark(dialog.bookmark.id, { title: v.title, url: v.url, tags: v.tags })
            setDialog(null)
            setError('')
            setNotice('Bookmark saved.')
            refresh()
          }}
        />
      )}
      {dialog?.kind === 'new-folder' && (
        <FolderDialog
          onClose={() => setDialog(null)}
          onSave={async (name) => {
            await api.createFolder(selected > 0 ? selected : ROOT, name)
            if (selected > 0) setExpanded((e) => new Set(e).add(selected))
            setDialog(null)
            setError('')
            setNotice('Folder created.')
            refresh()
          }}
        />
      )}
      {dialog?.kind === 'rename-folder' && selectedFolder && (
        <FolderDialog
          initial={selectedFolder.node.title}
          onClose={() => setDialog(null)}
          onSave={async (name) => {
            await api.updateFolder(selected, { title: name })
            setDialog(null)
            setError('')
            setNotice('Folder renamed.')
            refresh()
          }}
        />
      )}
      {dialog?.kind === 'import' && (
        <ImportDialog
          api={api}
          folder={selected}
          folderName={selectedFolder ? selectedFolder.node.title : 'the top level'}
          onClose={() => setDialog(null)}
          onDone={(n) => {
            setDialog(null)
            setError('')
            setNotice(`Imported ${plural(n, 'bookmark')}.`)
            refresh()
          }}
        />
      )}
      {dialog?.kind === 'export' && (
        <ExportDialog
          api={api}
          folder={selected}
          folderName={exportName}
          onClose={() => setDialog(null)}
          onDone={() => {
            setDialog(null)
            setNotice('Export downloaded.')
          }}
        />
      )}
      {dialog?.kind === 'share' && selectedFolder && (
        <ShareDialog
          api={api}
          folderId={selected}
          folderName={selectedFolder.node.title}
          onClose={() => setDialog(null)}
          onChanged={setNotice}
        />
      )}
      {confirm && (
        <ConfirmDialog
          title="Move to Trash?"
          message={confirm.kind === 'bookmark' ? `“${confirm.bookmark.title}” will move to Trash. You can restore it for 30 days.` : `The folder “${confirm.title}” and everything in it will move to Trash. You can restore it for 30 days.`}
          confirm="Move to Trash"
          busy={confirmBusy}
          error={confirmError}
          onConfirm={runConfirm}
          onCancel={() => {
            setConfirm(null)
            setConfirmError('')
          }}
        />
      )}
    </div>
  )
}

function allIds(nodes: TreeNode[]): number[] {
  return nodes.flatMap((n) => (n.type === 'folder' ? [n.id, ...allIds(n.children ?? [])] : []))
}
