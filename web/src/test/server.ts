import { vi } from 'vitest'

export interface Call {
  method: string
  path: string
  query: URLSearchParams
  body: unknown
  headers: Headers
}

export interface Reply {
  status: number
  body?: unknown
  headers?: Record<string, string>
}

export type Route = Reply | ((call: Call) => Reply)

/**
 * mockServer replaces fetch with a small route table keyed by "METHOD /path" (path
 * without the query). Unknown routes answer 404 with the API error shape. Calls are
 * recorded in order.
 */
export function mockServer(routes: Record<string, Route>) {
  const calls: Call[] = []
  const table = { ...routes }
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, 'http://bookmarks.test')
    const method = (init?.method ?? 'GET').toUpperCase()
    let body: unknown = undefined
    if (typeof init?.body === 'string') {
      try {
        body = JSON.parse(init.body)
      } catch {
        body = init.body
      }
    } else if (typeof FormData !== 'undefined' && init?.body instanceof FormData) {
      body = init.body
    }
    const call: Call = { method, path: url.pathname, query: url.searchParams, body, headers: new Headers(init?.headers) }
    calls.push(call)
    const route = table[`${method} ${url.pathname}`]
    const reply: Reply = route === undefined
      ? { status: 404, body: { error: { code: 'not_found', message: 'resource not found' } } }
      : typeof route === 'function' ? route(call) : route
    const text = reply.body === undefined ? '' : JSON.stringify(reply.body)
    return new Response(reply.status === 204 ? null : text, {
      status: reply.status,
      headers: { 'Content-Type': 'application/json', ...(reply.headers ?? {}) },
    })
  })
  vi.stubGlobal('fetch', fetchMock)
  return {
    calls,
    set(key: string, route: Route) {
      table[key] = route
    },
    find(method: string, path: string) {
      return calls.filter((c) => c.method === method && c.path === path)
    },
  }
}

export const sessionState = (username = 'alice', csrf = 'csrf-1', id = 7) => ({
  user: { id: 1, username, isAdmin: false },
  session: { id, label: 'Firefox', current: true },
  csrfToken: csrf,
  recentAuthUntil: new Date(Date.now() + 60_000).toISOString(),
})

export const err = (status: number, code: string, message = code): Reply => ({ status, body: { error: { code, message } } })

/** go navigates the jsdom window to path before rendering the app. */
export function go(path: string) {
  window.history.replaceState(null, '', path)
}
