const valid = /^[A-Za-z0-9_-]{43}$/

export function connectionHandle() {
  const value = new URLSearchParams(window.location.search).get('flow') || ''
  return valid.test(value) ? value : ''
}

// The independent browser handle remains in the URL or an HttpOnly cookie during
// provider sign-in. No authentication data enters browser storage.
export async function signInDestination() {
  const flow = window.location.pathname === '/auth/connect' ? connectionHandle() : ''
  if (flow) return '/auth/connect?flow=' + flow
  try {
    const response = await fetch('/api/v1/auth/connect/continuation', { credentials: 'same-origin', cache: 'no-store' })
    if (response.ok) {
      const data = await response.json()
      if (typeof data.destination === 'string' && /^\/auth\/connect\?flow=[A-Za-z0-9_-]{43}$/.test(data.destination)) return data.destination
    }
  } catch { /* An unavailable continuation uses the account page. */ }
  return '/auth/security'
}
