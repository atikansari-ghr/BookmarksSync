import { useEffect, useState } from 'react'
import { ApiError, type SessionState } from '../api'
import { useApp } from '../context'
import { connectionHandle } from '../connection'
import { AuthLayout, LoginPage } from './LoginPage'

export function ConnectPage() {
  const { client, session, setSession } = useApp()
  const [flow] = useState(connectionHandle)
  const [loading, setLoading] = useState(true)
  const [approved, setApproved] = useState(false)
  const [pending, setPending] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (!flow) { setLoading(false); setError('This connection link is invalid. Start again from your app.'); return }
    let live = true
    void client.request<SessionState>('GET', '/api/v1/auth/session', undefined, { reauth: false }).then(state => {
      if (!live) return
      setSession(state)
    }).catch(e => {
      if (live && (!(e instanceof ApiError) || e.status !== 401)) setError('This connection request has expired or was already used. Start again from your app.')
    }).finally(() => { if (live) setLoading(false) })
    return () => { live = false }
  }, [client, flow, setSession])

  useEffect(() => {
    if (!session || !flow) return
    let live = true
    void client.request('GET', `/api/v1/auth/connect?flow=${encodeURIComponent(flow)}`).then(() => {
      if (live) setPending(true)
    }).catch(() => {
      if (live) setError('This connection request has expired or was already used. Start again from your app.')
    })
    return () => { live = false }
  }, [client, flow, session?.user.id])

  async function approve() {
    setBusy(true); setError('')
    try {
      await client.request('POST', '/api/v1/auth/connect/approve', { flow, sessionId: session?.session.id })
      setApproved(true)
    } catch (e) {
      setError(e instanceof ApiError && e.status === 404 ? 'This connection request has expired or was already used. Start again from your app.' : 'The connection could not be approved. Try again.')
    } finally { setBusy(false) }
  }

  if (!loading && !error && !session) return <LoginPage />
  return <AuthLayout><div className="stack">
    <p className="eyebrow">App connection</p>
    <h1>{approved ? 'Connection approved' : 'Connect your bookmarks app'}</h1>
    {loading ? <p className="muted" role="status">Checking connection request…</p> : approved ? <>
      <p>Your app can now finish connecting. You can close this tab.</p>
      <p className="muted">Revoke this connection at any time from Account security.</p>
      <a className="btn" href="/auth/security">Account security</a>
    </> : <>
      {error && <p className="error" role="alert">{error}</p>}
      {session && pending && <>
        <p>Signed in as <strong>{session.user.username}</strong>. Approving lets the app read, create, change and delete your private bookmarks and folders.</p>
        <p className="muted">Approve only if you just started this connection from your own app. The app receives its own revocable credential; your sign-in password stays private.</p>
        <div className="actions"><button className="btn primary" disabled={busy} onClick={() => void approve()}>{busy ? 'Approving…' : 'Approve connection'}</button><a className="btn" href="/auth/security">Cancel</a></div>
      </>}
    </>}
  </div></AuthLayout>
}
