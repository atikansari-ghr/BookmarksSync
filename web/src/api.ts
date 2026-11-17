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

  private send(method: string, path: string, body?: unknown) {
    const headers: Record<string, string> = { Accept: 'application/json' }
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    if (method !== 'GET' && method !== 'HEAD' && this.csrf) headers['X-CSRF-Token'] = this.csrf
    return fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
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
