import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import { err, go, mockServer } from './server'
import { fakeWorkspace } from './workspace'

const tree = () => screen.getByRole('tree', { name: 'Folders' })
const folder = (name: string) => within(tree()).getByRole('treeitem', { name })
const rowOf = (title: string) => screen.getByText(title, { selector: '.bm-title' }).closest('li') as HTMLElement
const rows = () => within(screen.getByRole('list', { name: 'Bookmarks' })).getAllByRole('listitem').map((li) => li.querySelector('.bm-title')?.textContent)

async function openWork(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByRole('tree', { name: 'Folders' })
  await user.click(await within(tree()).findByRole('treeitem', { name: 'Work' }))
  await screen.findByText('GitHub', { selector: '.bm-title' })
}

describe('workspace organizer', () => {
  beforeEach(() => go('/bookmarks'))

  it('OrganizerLoadsAndShowsServerErrors', async () => {
    const ws = fakeWorkspace()
    const srv = mockServer({ ...ws.routes, 'GET /api/v1/folders': err(500, 'internal', 'database unavailable') })
    render(<App />)
    expect(await screen.findByRole('alert')).toHaveTextContent('database unavailable')
    expect(screen.queryByRole('tree', { name: 'Folders' })).not.toBeInTheDocument()
    srv.set('GET /api/v1/folders', ws.routes['GET /api/v1/folders'])
    await userEvent.setup().click(screen.getByRole('button', { name: 'Try again' }))
    expect(await within(await screen.findByRole('tree', { name: 'Folders' })).findByRole('treeitem', { name: 'Bookmarks Bar' })).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: 'Workspace' })).toBeInTheDocument()
  })

  it('sends signed-out visitors to the sign-in page without calling data APIs', async () => {
    const ws = fakeWorkspace()
    const srv = mockServer({ ...ws.routes, 'GET /api/v1/auth/session': err(401, 'unauthenticated') })
    render(<App />)
    await waitFor(() => expect(window.location.pathname).toBe('/auth/login'))
    expect(srv.find('GET', '/api/v1/folders')).toHaveLength(0)
  })

  it('OrganizerSupportsCRUDSearchAndTags', async () => {
    const ws = fakeWorkspace()
    const srv = mockServer(ws.routes)
    render(<App />)
    const user = userEvent.setup()
    await openWork(user)
    expect(rows()).toEqual(['GitHub', 'Debian', 'Proxmox'])

    // Create
    await user.click(screen.getByRole('button', { name: 'New bookmark' }))
    let dialog = await screen.findByRole('dialog', { name: 'New bookmark' })
    await user.type(within(dialog).getByLabelText('Title'), 'Go Docs')
    await user.type(within(dialog).getByLabelText('URL'), 'https://go.dev')
    await user.type(within(dialog).getByLabelText('Tags'), 'dev, reference')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await screen.findByText('Go Docs', { selector: '.bm-title' })
    expect(srv.find('POST', '/api/v1/bookmarks')[0].body).toEqual({ url: 'https://go.dev', title: 'Go Docs', folders: [11], tags: ['dev', 'reference'] })
    expect(srv.find('POST', '/api/v1/bookmarks')[0].headers.get('X-CSRF-Token')).toBe('csrf-1')
    expect(await screen.findByRole('status')).toHaveTextContent('Bookmark saved.')

    // Edit
    await user.click(within(rowOf('Debian')).getByRole('button', { name: 'Edit Debian' }))
    dialog = await screen.findByRole('dialog', { name: 'Edit bookmark' })
    const title = within(dialog).getByLabelText('Title')
    await user.clear(title)
    await user.type(title, 'Debian Linux')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await screen.findByText('Debian Linux', { selector: '.bm-title' })
    expect(srv.find('PATCH', '/api/v1/bookmarks/2')[0].body).toMatchObject({ title: 'Debian Linux' })

    // Favorite
    await user.click(within(rowOf('GitHub')).getByRole('button', { name: 'Add GitHub to favorites' }))
    expect(await within(rowOf('GitHub')).findByRole('button', { name: 'Remove GitHub from favorites' })).toHaveAttribute('aria-pressed', 'true')
    expect(srv.find('PUT', '/api/v1/bookmarks/1/favorite')).toHaveLength(1)

    // Search (across the collection) and tag filter
    await user.type(screen.getByRole('searchbox', { name: 'Search bookmarks' }), 'prox')
    await waitFor(() => expect(rows()).toEqual(['Proxmox']))
    expect(srv.calls.filter((c) => c.path === '/api/v1/bookmarks').some((c) => c.query.get('q') === 'prox')).toBe(true)
    await user.clear(screen.getByRole('searchbox', { name: 'Search bookmarks' }))
    await waitFor(() => expect(rows()).toContain('GitHub'))
    await user.click(screen.getByRole('button', { name: /^infra/ }))
    await waitFor(() => expect(rows()).toEqual(['Debian Linux', 'Proxmox']))
    expect(srv.calls.some((c) => c.path === '/api/v1/bookmarks' && c.query.getAll('tag').includes('infra'))).toBe(true)
    await user.click(screen.getByRole('button', { name: /^infra/ }))

    // Delete moves to Trash only after confirmation
    await user.click(within(rowOf('Proxmox')).getByRole('button', { name: 'Delete Proxmox' }))
    dialog = await screen.findByRole('dialog', { name: 'Move to Trash?' })
    expect(srv.find('DELETE', '/api/v1/bookmarks/3')).toHaveLength(0)
    await user.click(within(dialog).getByRole('button', { name: 'Move to Trash' }))
    await waitFor(() => expect(screen.queryByText('Proxmox', { selector: '.bm-title' })).not.toBeInTheDocument())
    expect(srv.find('DELETE', '/api/v1/bookmarks/3')).toHaveLength(1)

    // Folders
    await user.click(screen.getByRole('button', { name: 'New folder' }))
    dialog = await screen.findByRole('dialog', { name: 'New folder' })
    await user.type(within(dialog).getByLabelText('Name'), 'Projects')
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await within(tree()).findByRole('treeitem', { name: 'Projects' })
    expect(srv.find('POST', '/api/v1/folders')[0].body).toEqual({ parentId: 11, title: 'Projects' })
  })

  it('OrganizerMovesSelectionByPointerDrop', async () => {
    const ws = fakeWorkspace()
    const srv = mockServer(ws.routes)
    render(<App />)
    const user = userEvent.setup()
    await openWork(user)
    await user.click(within(rowOf('GitHub')).getByRole('checkbox', { name: 'Select GitHub' }))
    await user.click(within(rowOf('Debian')).getByRole('checkbox', { name: 'Select Debian' }))
    const data = { setData: vi.fn(), effectAllowed: '', dropEffect: '' }
    fireEvent.dragStart(rowOf('GitHub'), { dataTransfer: data })
    fireEvent.dragOver(folder('News'), { dataTransfer: data })
    fireEvent.drop(folder('News'), { dataTransfer: data })
    await waitFor(() => expect(srv.find('POST', '/api/v1/bookmarks/bulk-move')).toHaveLength(1))
    const call = srv.find('POST', '/api/v1/bookmarks/bulk-move')[0]
    expect(call.body).toEqual({ bookmarkIds: [1, 2], sourceFolderId: 11, folderId: 12 })
    expect(call.headers.get('X-CSRF-Token')).toBe('csrf-1')
    await waitFor(() => expect(rows()).toEqual(['Proxmox']))
    expect(await screen.findByRole('status')).toHaveTextContent('Moved 2 bookmarks to News.')

    // A rejected move changes nothing on screen: the list is reloaded from the server.
    ws.state.failBulkMove = true
    fireEvent.dragStart(rowOf('Proxmox'), { dataTransfer: data })
    fireEvent.drop(folder('News'), { dataTransfer: data })
    expect(await screen.findByRole('alert')).toHaveTextContent('already in the destination folder')
    expect(screen.queryByText(/Moved 1 bookmark/)).not.toBeInTheDocument()
    expect(rows()).toEqual(['Proxmox'])
  })

  it('OrganizerMovesSelectionWithKeyboard', async () => {
    const ws = fakeWorkspace()
    const srv = mockServer(ws.routes)
    render(<App />)
    const user = userEvent.setup()
    await openWork(user)
    await user.click(within(rowOf('GitHub')).getByRole('checkbox', { name: 'Select GitHub' }))
    await user.click(within(rowOf('Proxmox')).getByRole('checkbox', { name: 'Select Proxmox' }))
    const row = rowOf('GitHub')
    row.focus()
    await user.keyboard(' ')
    expect(screen.getByTestId('announcer')).toHaveTextContent('Picked up 2 bookmarks')

    // Escape cancels without calling the server.
    await user.keyboard('{ArrowDown}{Escape}')
    expect(screen.getByTestId('announcer')).toHaveTextContent('Move cancelled')
    expect(srv.find('POST', '/api/v1/bookmarks/bulk-move')).toHaveLength(0)

    row.focus()
    await user.keyboard(' {ArrowDown}')
    const target = screen.getByTestId('announcer').textContent ?? ''
    expect(target).toMatch(/Move to /)
    // Walk to News (visible folders in tree order: Bookmarks Bar, Work, News, Other Bookmarks; Work is the source).
    while (!/Move to News/.test(screen.getByTestId('announcer').textContent ?? '')) await user.keyboard('{ArrowDown}')
    await user.keyboard('{Enter}')
    await waitFor(() => expect(srv.find('POST', '/api/v1/bookmarks/bulk-move')).toHaveLength(1))
    expect(srv.find('POST', '/api/v1/bookmarks/bulk-move')[0].body).toEqual({ bookmarkIds: [1, 3], sourceFolderId: 11, folderId: 12 })
    await waitFor(() => expect(rows()).toEqual(['Debian']))
    expect(await screen.findByRole('status')).toHaveTextContent('Moved 2 bookmarks to News.')
  })

  it('OrganizerReordersFoldersAndBookmarksWithTheKeyboard', async () => {
    const ws = fakeWorkspace()
    const srv = mockServer(ws.routes)
    render(<App />)
    const user = userEvent.setup()
    await openWork(user)
    rowOf('GitHub').focus()
    await user.keyboard('{Alt>}{ArrowDown}{/Alt}')
    await waitFor(() => expect(srv.find('PUT', '/api/v1/folders/11/order')).toHaveLength(1))
    expect(srv.find('PUT', '/api/v1/folders/11/order')[0].body).toEqual({
      children: [{ type: 'bookmark', id: 2 }, { type: 'bookmark', id: 1 }, { type: 'bookmark', id: 3 }],
    })
    await waitFor(() => expect(rows()).toEqual(['Debian', 'GitHub', 'Proxmox']))

    folder('News').focus()
    await user.keyboard('{Alt>}{ArrowUp}{/Alt}')
    await waitFor(() => expect(srv.find('PUT', '/api/v1/folders/10/order')).toHaveLength(1))
    expect(srv.find('PUT', '/api/v1/folders/10/order')[0].body).toEqual({ children: [{ type: 'folder', id: 12 }, { type: 'folder', id: 11 }] })
  })

  it('OrganizerHonorsReadOnlyShare', async () => {
    const ws = fakeWorkspace({ shares: [{ id: 5, ownerId: 2, folderId: 11, recipientId: 1, mode: 'view', folderTitle: 'Family' }] })
    const srv = mockServer({
      ...ws.routes,
      'GET /api/v1/folders': (c) => (c.query.get('share') === '5'
        ? { status: 200, body: { folders: [{ id: 31, type: 'folder', title: 'Recipes', loaded: true, children: [{ id: 7, type: 'bookmark', title: 'Soup', url: 'https://soup.example' }] }] } }
        : ws.routes['GET /api/v1/folders'] instanceof Function ? (ws.routes['GET /api/v1/folders'] as (c: unknown) => never)(c) : { status: 500 }),
      'GET /api/v1/bookmarks': (c) => (c.query.get('share') === '5'
        ? { status: 200, body: { items: [{ id: 7, url: 'https://soup.example', title: 'Soup', tags: [], folders: [31], favorite: false }], page: 0, limit: 200, hasMore: false } }
        : { status: 200, body: { items: [], page: 0, limit: 200, hasMore: false } }),
      'PUT /api/v1/bookmarks/7/favorite': { status: 204 },
    })
    render(<App />)
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: /Family/ }))
    await user.click(await within(await screen.findByRole('tree', { name: 'Folders' })).findByRole('treeitem', { name: 'Recipes' }))
    await screen.findByText('Soup', { selector: '.bm-title' })
    expect(srv.find('GET', '/api/v1/bookmarks').some((c) => c.query.get('share') === '5')).toBe(true)
    expect(screen.getByRole('note')).toHaveTextContent(/view-only/i)

    for (const name of ['New bookmark', 'New folder', 'Import', 'Rename folder', 'Delete folder', 'Edit Soup', 'Delete Soup', 'Share folder']) {
      expect(screen.queryByRole('button', { name }), name).not.toBeInTheDocument()
    }
    expect(screen.queryByRole('checkbox', { name: 'Select Soup' })).not.toBeInTheDocument()
    expect(rowOf('Soup')).not.toHaveAttribute('draggable', 'true')
    expect(screen.getByRole('button', { name: 'Export' })).toBeEnabled()

    // Favorites stay usable in a view share, scoped to the share.
    await user.click(within(rowOf('Soup')).getByRole('button', { name: 'Add Soup to favorites' }))
    await waitFor(() => expect(srv.find('PUT', '/api/v1/bookmarks/7/favorite')).toHaveLength(1))
    expect(srv.find('PUT', '/api/v1/bookmarks/7/favorite')[0].query.get('share')).toBe('5')

    // Keyboard pick-up does nothing in a read-only scope.
    rowOf('Soup').focus()
    await user.keyboard(' ')
    expect(screen.getByTestId('announcer')).not.toHaveTextContent('Picked up')
    expect(srv.find('POST', '/api/v1/bookmarks/bulk-move')).toHaveLength(0)
  })

  it('DuplicatesAreReviewedBeforeRemoval', async () => {
    go('/duplicates')
    const groups = [
      { url: 'https://debian.org', bookmarks: [
        { id: 2, url: 'https://debian.org', title: 'Debian', tags: [], folders: [11], favorite: false },
        { id: 3, url: 'https://debian.org', title: 'Debian mirror', tags: [], folders: [12], favorite: false },
      ] },
    ]
    let removed = false
    const ws = fakeWorkspace({ shares: [{ id: 5, ownerId: 2, folderId: 11, recipientId: 1, mode: 'view', folderTitle: 'Family' }] })
    const srv = mockServer({
      ...ws.routes,
      'GET /api/v1/duplicates': (c) => (c.query.get('share') === '5'
        ? { status: 200, body: { count: 1, groups, page: 0, limit: 50, hasMore: false } }
        : { status: 200, body: { count: removed ? 0 : 1, groups: removed ? [] : groups, page: 0, limit: 50, hasMore: false } }),
      'DELETE /api/v1/bookmarks/3': () => { removed = true; return { status: 204 } },
    })
    render(<App />)
    const user = userEvent.setup()
    expect(await screen.findByRole('heading', { name: 'Duplicates' })).toBeInTheDocument()
    expect(await screen.findByText('https://debian.org')).toBeInTheDocument()
    expect(screen.getByText(/1 duplicate/)).toBeInTheDocument()
    expect(srv.calls.filter((c) => c.method === 'DELETE')).toHaveLength(0) // review never removes

    await user.click(screen.getByRole('button', { name: 'Remove Debian mirror' }))
    let dialog = await screen.findByRole('dialog', { name: 'Move to Trash?' })
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(srv.calls.filter((c) => c.method === 'DELETE')).toHaveLength(0)

    await user.click(screen.getByRole('button', { name: 'Remove Debian mirror' }))
    dialog = await screen.findByRole('dialog', { name: 'Move to Trash?' })
    await user.click(within(dialog).getByRole('button', { name: 'Move to Trash' }))
    await waitFor(() => expect(srv.find('DELETE', '/api/v1/bookmarks/3')).toHaveLength(1))
    expect(await screen.findByText(/No duplicates/)).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Moved to Trash')

    // A view-only share can be reviewed but not changed.
    await user.selectOptions(screen.getByRole('combobox', { name: 'Collection' }), 'Family (view-only)')
    await screen.findByText('Debian mirror')
    expect(screen.queryByRole('button', { name: 'Remove Debian mirror' })).not.toBeInTheDocument()
  })

  it('OrganizerImportsExportsAndRestoresTrash', async () => {
    const ws = fakeWorkspace()
    const srv = mockServer(ws.routes)
    const createObjectURL = vi.fn(() => 'blob:export')
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL, revokeObjectURL: vi.fn() }))
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    render(<App />)
    const user = userEvent.setup()
    await openWork(user)

    // Import into the selected folder.
    await user.click(screen.getByRole('button', { name: 'Import' }))
    const dialog = await screen.findByRole('dialog', { name: 'Import bookmarks' })
    const file = new File(['<!DOCTYPE NETSCAPE-Bookmark-file-1><DL></DL>'], 'bookmarks.html', { type: 'text/html' })
    await user.upload(within(dialog).getByLabelText('Bookmarks file'), file)
    await user.click(within(dialog).getByRole('button', { name: 'Import' }))
    await waitFor(() => expect(srv.find('POST', '/api/v1/import')).toHaveLength(1))
    const imp = srv.find('POST', '/api/v1/import')[0]
    expect(imp.query.get('folder')).toBe('11')
    expect(imp.headers.get('Content-Type')).toBe('text/html')
    expect(imp.headers.get('X-CSRF-Token')).toBe('csrf-1')
    expect(String(imp.body)).toContain('NETSCAPE-Bookmark-file-1')
    expect(await screen.findByRole('status')).toHaveTextContent('Imported 3 bookmarks.')

    // Export downloads what the server returns.
    await user.click(screen.getByRole('button', { name: 'Export' }))
    await user.click(await screen.findByRole('button', { name: 'Download Netscape HTML' }))
    await waitFor(() => expect(srv.find('GET', '/api/v1/export')).toHaveLength(1))
    expect(srv.find('GET', '/api/v1/export')[0].query.get('format')).toBe('netscape')
    await waitFor(() => expect(click).toHaveBeenCalled())
    expect(createObjectURL).toHaveBeenCalled()

    // Trash: restore, purge after confirmation.
    await user.click(screen.getByRole('button', { name: 'Trash' }))
    const trash = await screen.findByRole('region', { name: 'Trash' })
    expect(await within(trash).findByText('Old link')).toBeInTheDocument()
    await user.click(within(trash).getByRole('button', { name: 'Restore Old link' }))
    await waitFor(() => expect(srv.find('POST', '/api/v1/trash/bookmark/9/restore')).toHaveLength(1))
    await waitFor(() => expect(within(trash).queryByText('Old link')).not.toBeInTheDocument())
    expect(await screen.findByRole('status')).toHaveTextContent('Restored Old link.')

    await user.click(within(trash).getByRole('button', { name: 'Delete Stale page permanently' }))
    const confirm = await screen.findByRole('dialog', { name: 'Delete permanently?' })
    expect(srv.find('DELETE', '/api/v1/trash/bookmark/10')).toHaveLength(0)
    await user.click(within(confirm).getByRole('button', { name: 'Delete permanently' }))
    await waitFor(() => expect(srv.find('DELETE', '/api/v1/trash/bookmark/10')).toHaveLength(1))
    expect(await within(trash).findByText('Trash is empty.')).toBeInTheDocument()
  })
})
