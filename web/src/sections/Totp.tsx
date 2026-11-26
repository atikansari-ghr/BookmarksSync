import { useRef, useState, type FormEvent } from 'react'
import { ApiError, describe, isCancelled } from '../api'
import { useApp } from '../context'
import { Dialog } from '../components/Dialog'
import { Busy, CopyButton, Reveal, Section } from '../components/common'
import type { Notify } from '../pages/SecurityPage'
import { useLoad } from './useLoad'

interface Status {
  enabled: boolean
  recoveryCodesRemaining: number
}

function codeError(err: unknown) {
  return err instanceof ApiError && err.status === 401 ? 'That code did not work.' : describe(err)
}

export function TotpSection({ notify }: { notify: Notify }) {
  const { client } = useApp()
  const [load, reload] = useLoad(() => client.request<Status>('GET', '/api/v1/auth/totp'))
  const [enrollment, setEnrollment] = useState<{ secret: string; provisioningUri: string } | null>(null)
  const [code, setCode] = useState('')
  const [codes, setCodes] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [dialog, setDialog] = useState<'' | 'disable' | 'regenerate'>('')

  async function begin() {
    setError('')
    setBusy(true)
    try {
      setEnrollment(await client.request('POST', '/api/v1/auth/totp/enroll'))
    } catch (err) {
      if (!isCancelled(err)) setError(describe(err))
    } finally {
      setBusy(false)
    }
  }

  async function confirm(e: FormEvent) {
    e.preventDefault()
    setError('')
    setBusy(true)
    try {
      const d = await client.request<{ recoveryCodes: string[] }>('POST', '/api/v1/auth/totp/confirm', { code: code.trim() })
      setEnrollment(null)
      setCode('')
      setCodes(d.recoveryCodes ?? [])
      notify('Authenticator app turned on. Your other sessions were signed out.')
    } catch (err) {
      setCode('')
      if (!isCancelled(err)) setError(codeError(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Section title="Authenticator app" intro="Ask for a one-time code from an authenticator app after your password.">
      {error && <p className="error" role="alert">{error}</p>}
      {codes.length > 0 ? (
        <Reveal title="Save your recovery codes" doneLabel="I have saved them" onDone={() => { setCodes([]); void reload() }}>
          <p className="muted">Each code signs you in once if you lose your authenticator app. They will not be shown again.</p>
          <ul className="codes">
            {codes.map((c) => <li key={c}><code>{c}</code></li>)}
          </ul>
          <CopyButton text={codes.join('\n')} label="Copy codes" />
        </Reveal>
      ) : enrollment ? (
        <form className="stack compact" onSubmit={confirm} noValidate>
          <p>Add this account to your authenticator app, then enter the code it shows.</p>
          <dl className="details">
            <dt>Setup key</dt>
            <dd className="secret"><code>{enrollment.secret}</code> <CopyButton text={enrollment.secret} /></dd>
            <dt>Setup link</dt>
            <dd className="secret small"><code>{enrollment.provisioningUri}</code></dd>
          </dl>
          <label className="field">
            <span>Authentication code</span>
            <input autoComplete="one-time-code" inputMode="numeric" maxLength={6} value={code} onChange={(e) => setCode(e.target.value)} />
          </label>
          <div className="actions">
            <button type="button" className="btn ghost" onClick={() => { setEnrollment(null); setCode('') }}>Cancel</button>
            <button type="submit" className="btn primary" disabled={busy || code.trim() === ''}>Turn on</button>
          </div>
        </form>
      ) : (
        <>
          {load.state === 'loading' && <Busy />}
          {load.state === 'unavailable' && <p className="muted">Authenticator apps are not available on this server.</p>}
          {load.state === 'error' && <p className="error" role="alert">The authenticator status could not be loaded.</p>}
          {load.state === 'ready' && !load.data.enabled && (
            <button type="button" className="btn primary" disabled={busy} onClick={begin}>Set up authenticator app</button>
          )}
          {load.state === 'ready' && load.data.enabled && (
            <>
              <p>Authenticator app is on. {load.data.recoveryCodesRemaining} recovery codes left.</p>
              <div className="actions start">
                <button type="button" className="btn ghost" onClick={() => setDialog('regenerate')}>New recovery codes</button>
                <button type="button" className="btn danger" onClick={() => setDialog('disable')}>Turn off</button>
              </div>
            </>
          )}
        </>
      )}
      {dialog && (
        <CodeDialog
          mode={dialog}
          onClose={() => setDialog('')}
          onDone={async (fresh) => {
            setDialog('')
            if (fresh) setCodes(fresh)
            else notify('Authenticator app turned off. Your other sessions were signed out.')
            await reload()
          }}
        />
      )}
    </Section>
  )
}

function CodeDialog({ mode, onClose, onDone }: { mode: 'disable' | 'regenerate'; onClose: () => void; onDone: (codes?: string[]) => void }) {
  const { client } = useApp()
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const input = useRef<HTMLInputElement>(null)
  const disable = mode === 'disable'

  async function submit(e: FormEvent) {
    e.preventDefault()
    setError('')
    setBusy(true)
    const v = value.trim()
    try {
      if (disable) {
        await client.request('POST', '/api/v1/auth/totp/disable', /^\d{6}$/.test(v) ? { code: v } : { recoveryCode: v })
        onDone()
      } else {
        const d = await client.request<{ recoveryCodes: string[] }>('POST', '/api/v1/auth/recovery-codes', { code: v })
        onDone(d.recoveryCodes ?? [])
      }
    } catch (err) {
      setValue('')
      if (isCancelled(err)) onClose()
      else setError(codeError(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog title={disable ? 'Turn off the authenticator app?' : 'Create new recovery codes?'} onClose={onClose} initialFocus={input}>
      <form className="stack compact" onSubmit={submit} noValidate>
        <p className="muted">{disable ? 'Your other sessions will be signed out.' : 'Your old recovery codes will stop working.'}</p>
        {error && <p className="error" role="alert">{error}</p>}
        <label className="field">
          <span>{disable ? 'Code from your app or a recovery code' : 'Code from your authenticator app'}</span>
          <input ref={input} autoComplete="one-time-code" value={value} onChange={(e) => setValue(e.target.value)} />
        </label>
        <div className="actions">
          <button type="button" className="btn ghost" onClick={onClose}>Cancel</button>
          <button type="submit" className={disable ? 'btn danger' : 'btn primary'} disabled={busy || !value.trim()}>
            {disable ? 'Turn off authenticator app' : 'Create new codes'}
          </button>
        </div>
      </form>
    </Dialog>
  )
}
