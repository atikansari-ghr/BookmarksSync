// Same-origin JSON client for the BookmarksSync API. The session lives in an HttpOnly
// cookie the browser manages; the CSRF token is kept only in memory (never in
// localStorage/sessionStorage) and refreshed from every response that carries one.

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
    public readonly retryAfter?: number,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

/** Thrown when the user dismisses the password prompt. Callers ignore it quietly. */
export const CANCELLED = 'cancelled'

export interface RequestOptions {
  /** When false, a reauthentication_required answer is returned instead of prompting. */
  reauth?: boolean
}

export class Client {
  private csrf = ''
  /** Set by the app: asks the user for their password; resolves true when confirmed. */
  onReauth: (() => Promise<boolean>) | null = null

  setCSRF(token: string | undefined) {
    if (token) this.csrf = token
  }

  clear() {
    this.csrf = ''
  }

  async request<T = unknown>(method: string, path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
    const res = await this.send(method, path, body)
    if (res.status === 403 && opts.reauth !== false && this.onReauth) {
      const err = await errorFrom(res.clone())
      if (err.code === 'reauthentication_required') {
        if (!(await this.onReauth())) throw new ApiError(0, CANCELLED, 'Cancelled.')
        return this.handle<T>(await this.send(method, path, body))
      }
    }
    return this.handle<T>(res)
  }

  /** Sends a non-JSON request body (bookmark import) and returns the JSON answer. */
  async upload<T = unknown>(path: string, body: string, contentType: string): Promise<T> {
    return this.handle<T>(await this.send('POST', path, body, contentType))
  }

  /** Sends multipart form data without overriding its generated content type boundary. */
  async uploadForm<T = unknown>(path: string, body: FormData): Promise<T> {
    let res = await this.sendForm(path, body)
    if (res.status === 403 && this.onReauth) {
      const err = await errorFrom(res.clone())
      if (err.code === 'reauthentication_required') {
        if (!(await this.onReauth())) throw new ApiError(0, CANCELLED, 'Cancelled.')
        res = await this.sendForm(path, body)
      }
    }
    return this.handle<T>(res)
  }

  private sendForm(path: string, body: FormData) {
    const headers: Record<string, string> = { Accept: 'application/json' }
    if (this.csrf) headers['X-CSRF-Token'] = this.csrf
    return fetch(path, { method: 'POST', headers, body, credentials: 'same-origin', cache: 'no-store' })
  }

  /** Fetches a text document (bookmark export) with the session cookie. */
  async download(path: string): Promise<string> {
    const res = await this.send('GET', path, undefined, undefined, '*/*')
    if (!res.ok) throw await errorFrom(res)
    return res.text()
  }

  private send(method: string, path: string, body?: unknown, rawType?: string, accept = 'application/json') {
    const headers: Record<string, string> = { Accept: accept }
    if (rawType) headers['Content-Type'] = rawType
    else if (body !== undefined) headers['Content-Type'] = 'application/json'
    if (method !== 'GET' && method !== 'HEAD' && this.csrf) headers['X-CSRF-Token'] = this.csrf
    return fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : rawType ? (body as string) : JSON.stringify(body),
      credentials: 'same-origin',
      cache: 'no-store',
    })
  }

  private async handle<T>(res: Response): Promise<T> {
    if (!res.ok) throw await errorFrom(res)
    if (res.status === 204) return undefined as T
    const text = await res.text()
    const data = text ? JSON.parse(text) : undefined
    if (data && typeof data === 'object' && typeof (data as { csrfToken?: unknown }).csrfToken === 'string') {
      this.csrf = (data as { csrfToken: string }).csrfToken
    }
    return data as T
  }
}

async function errorFrom(res: Response): Promise<ApiError> {
  let code = 'http_' + res.status
  let message = res.statusText || 'Request failed'
  try {
    const data = (await res.json()) as { error?: { code?: string; message?: string } }
    if (data?.error?.code) code = data.error.code
    if (data?.error?.message) message = data.error.message
  } catch {
    // Non-JSON error body: keep the status-derived code.
  }
  const ra = Number(res.headers.get('Retry-After'))
  return new ApiError(res.status, code, message, Number.isFinite(ra) && ra > 0 ? ra : undefined)
}

export function isCancelled(e: unknown) {
  return e instanceof ApiError && e.code === CANCELLED
}

/** The server's own sentence for an API error, else the generic explanation. */
export function serverMessage(e: unknown, fallback = 'Something went wrong. Please try again.'): string {
  if (e instanceof ApiError && e.status >= 400 && e.status !== 429 && e.code !== 'locked' && e.code !== CANCELLED && e.message) return e.message
  return describe(e, fallback)
}

/** A short, user-facing explanation for common API failures. */
export function describe(e: unknown, fallback = 'Something went wrong. Please try again.'): string {
  if (e instanceof ApiError) {
    if (e.status === 429) return tooMany(e.retryAfter)
    if (e.status === 0 && e.code !== CANCELLED) return 'The server could not be reached. Check your connection and try again.'
    if (e.code === 'locked') return 'Floccus is syncing these bookmarks right now. Try again in a moment.'
  } else if (e instanceof TypeError) {
    return 'The server could not be reached. Check your connection and try again.'
  }
  return fallback
}

export function tooMany(retryAfter?: number) {
  return retryAfter ? `Too many attempts. Try again in ${retryAfter} seconds.` : 'Too many attempts. Try again later.'
}

// ---- shapes ----

export interface SessionState {
  user: { id: number; username: string; isAdmin: boolean }
  session: { id: number; label: string; current?: boolean }
  csrfToken: string
  recentAuthUntil?: string
}

export interface Provider {
  id: string
  name: string
}

export interface SessionInfo {
  id: number
  label: string
  createdAt: string
  lastSeenAt: string
  current: boolean
}

export interface PublicPresentation {
  appTitle: string
  signinTitle: string
  tagline: string
  supportingLine: string
  loginLayout: 'split' | 'centered' | 'plain'
  primaryColor: string
  accentColor: string
  backgroundColor: string
  version: string
  copyright: string
  logoUrl?: string
  backgroundUrl?: string
}

// ---- workspace (bookmarks) ----

export interface Bookmark {
  id: number
  url: string
  title: string
  tags: string[]
  folders: number[]
  favorite: boolean
}

export interface BookmarkPage {
  items: Bookmark[]
  page: number
  limit: number
  hasMore: boolean
}

export interface TreeNode {
  id: number
  type: 'folder' | 'bookmark'
  title: string
  url?: string
  children?: TreeNode[]
}

export interface Tag {
  name: string
  count: number
}

export interface ChildRef {
  type: 'folder' | 'bookmark'
  id: number
}

export interface DuplicateGroup {
  url: string
  bookmarks: Bookmark[]
}

export interface DuplicateList {
  count: number
  groups: DuplicateGroup[]
  page: number
  limit: number
  hasMore: boolean
}

export interface TrashItem {
  type: 'bookmark' | 'folder'
  id: number
  title: string
  url: string
  deletedAt: string
  purgeAt: string
}

export interface Share {
  id: number
  ownerId: number
  folderId: number
  recipientId: number
  mode: 'view' | 'edit'
  folderTitle: string
}

export interface LinkCheckResult {
  bookmarkId: number
  url: string
  status: 'pending' | 'ok' | 'broken' | 'skipped' | 'excluded' | 'blocked' | 'error'
  httpStatus?: number | null
  detail?: string
  redirects?: number
  checkedAt?: string | null
}

export interface LinkCheckJob {
  id: number
  status: 'queued' | 'running' | 'completed'
  total: number
  done: number
  createdAt?: string
  startedAt?: string | null
  finishedAt?: string | null
  results: LinkCheckResult[]
}

export interface ListFilter {
  folder?: number
  q?: string
  tags?: string[]
  favorite?: boolean
  page?: number
  limit?: number
}

/** Folder root of a collection (the private root or the root of a share). */
export const ROOT = -1

/**
 * WorkspaceApi is the typed bookmark API for one collection: the private collection,
 * or a share when `share` is set (every scoped call then carries ?share=<id>).
 */
export class WorkspaceApi {
  constructor(
    private readonly client: Client,
    readonly share?: number,
  ) {}

  private url(path: string, params: Record<string, string | number | boolean | undefined | string[]> = {}, scoped = true) {
    const q = new URLSearchParams()
    for (const [k, v] of Object.entries(params)) {
      if (v === undefined) continue
      if (Array.isArray(v)) v.forEach((x) => q.append(k, x))
      else q.set(k, String(v))
    }
    if (scoped && this.share !== undefined) q.set('share', String(this.share))
    const qs = q.toString()
    return `/api/v1${path}${qs ? '?' + qs : ''}`
  }

  folders() {
    return this.client.request<{ folders: TreeNode[] }>('GET', this.url('/folders', { parent: ROOT, depth: 1000 }))
  }
  listBookmarks(f: ListFilter = {}) {
    return this.client.request<BookmarkPage>(
      'GET',
      this.url('/bookmarks', { folder: f.folder ?? 0, q: f.q || undefined, tag: f.tags, favorite: f.favorite, page: f.page ?? 0, limit: f.limit ?? 200 }),
    )
  }
  createBookmark(b: { url: string; title: string; folders: number[]; tags: string[] }) {
    return this.client.request<Bookmark>('POST', this.url('/bookmarks'), b)
  }
  updateBookmark(id: number, b: { url?: string; title?: string; tags?: string[] }) {
    return this.client.request<Bookmark>('PATCH', this.url(`/bookmarks/${id}`), b)
  }
  deleteBookmark(id: number) {
    return this.client.request<void>('DELETE', this.url(`/bookmarks/${id}`))
  }
  setFavorite(id: number, on: boolean) {
    return this.client.request<void>(on ? 'PUT' : 'DELETE', this.url(`/bookmarks/${id}/favorite`))
  }
  bulkMove(bookmarkIds: number[], sourceFolderId: number, folderId: number) {
    return this.client.request<void>('POST', this.url('/bookmarks/bulk-move'), { bookmarkIds, sourceFolderId, folderId })
  }
  createFolder(parentId: number, title: string) {
    return this.client.request<{ id: number }>('POST', this.url('/folders'), { parentId, title })
  }
  updateFolder(id: number, patch: { title?: string; parentId?: number }) {
    return this.client.request<unknown>('PATCH', this.url(`/folders/${id}`), patch)
  }
  deleteFolder(id: number) {
    return this.client.request<void>('DELETE', this.url(`/folders/${id}`))
  }
  reorder(folderId: number, children: ChildRef[]) {
    return this.client.request<void>('PUT', this.url(`/folders/${folderId}/order`), { children })
  }
  tags() {
    return this.client.request<{ tags: Tag[] }>('GET', this.url('/tags'))
  }
  duplicates(page = 0) {
    return this.client.request<DuplicateList>('GET', this.url('/duplicates', { page }))
  }
  submitLinkCheck(bookmarkIds: number[]) {
    return this.client.request<LinkCheckJob>('POST', this.url('/link-checks'), { bookmarkIds })
  }
  linkCheckStatus(id: number) {
    return this.client.request<LinkCheckJob>('GET', this.url(`/link-checks/${id}`))
  }
  importFile(text: string, contentType: string, folder?: number) {
    return this.client.upload<{ imported: number }>(this.url('/import', { folder }), text, contentType)
  }
  exportFile(format: 'netscape' | 'xbel', folder?: number) {
    return this.client.download(this.url('/export', { format, folder }))
  }
  // Trash belongs to the private collection only.
  trash() {
    return this.client.request<{ items: TrashItem[]; hasMore: boolean }>('GET', '/api/v1/trash?limit=1000')
  }
  restore(type: TrashItem['type'], id: number) {
    return this.client.request<unknown>('POST', `/api/v1/trash/${type}/${id}/restore`)
  }
  purge(type: TrashItem['type'], id: number) {
    return this.client.request<void>('DELETE', `/api/v1/trash/${type}/${id}`)
  }
  emptyTrash() {
    return this.client.request<void>('DELETE', '/api/v1/trash')
  }
  // Shares are not collection-scoped.
  incomingShares() {
    return this.client.request<{ shares: Share[] }>('GET', '/api/v1/shares/incoming')
  }
  ownedShares() {
    return this.client.request<{ shares: Share[] }>('GET', '/api/v1/shares/owned')
  }
  grantShare(folderId: number, recipient: string, mode: Share['mode']) {
    return this.client.request<Share>('POST', '/api/v1/shares', { folderId, recipient, mode })
  }
  updateShare(id: number, mode: Share['mode']) {
    return this.client.request<Share>('PATCH', `/api/v1/shares/${id}`, { mode })
  }
  revokeShare(id: number) {
    return this.client.request<void>('DELETE', `/api/v1/shares/${id}`)
  }
}
