// Conversions between the server's WebAuthn JSON (base64url fields, the
// PublicKeyCredential*OptionsJSON shape) and the browser's ArrayBuffer API. Done by
// hand so every browser with WebAuthn works, not only those with parse*FromJSON.

export function webauthnSupported(): boolean {
  return typeof window !== 'undefined' && typeof window.PublicKeyCredential === 'function' && !!navigator.credentials
}

export function b64urlToBuffer(s: string): ArrayBuffer {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/') + '==='.slice((s.length + 3) % 4)
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out.buffer
}

export function bufferToB64url(buf: ArrayBuffer | ArrayBufferView | null | undefined): string {
  if (!buf) return ''
  const bytes = buf instanceof ArrayBuffer ? new Uint8Array(buf) : new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength)
  let bin = ''
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

interface DescriptorJSON {
  type: string
  id: string
  transports?: string[]
}

type Json = Record<string, unknown>

function descriptors(list: unknown): PublicKeyCredentialDescriptor[] | undefined {
  if (!Array.isArray(list)) return undefined
  return (list as DescriptorJSON[]).map((d) => ({
    type: 'public-key' as const,
    id: b64urlToBuffer(d.id),
    ...(d.transports ? { transports: d.transports as AuthenticatorTransport[] } : {}),
  }))
}

/** Creation options JSON (the "publicKey" member) to the browser form. */
export function creationOptions(pk: Json): PublicKeyCredentialCreationOptions {
  const user = pk.user as Json
  return {
    ...(pk as object),
    challenge: b64urlToBuffer(pk.challenge as string),
    user: { ...(user as object), id: b64urlToBuffer(user.id as string) } as PublicKeyCredentialUserEntity,
    excludeCredentials: descriptors(pk.excludeCredentials),
  } as PublicKeyCredentialCreationOptions
}

/** Request options JSON (the "publicKey" member) to the browser form. */
export function requestOptions(pk: Json): PublicKeyCredentialRequestOptions {
  return {
    ...(pk as object),
    challenge: b64urlToBuffer(pk.challenge as string),
    allowCredentials: descriptors(pk.allowCredentials),
  } as PublicKeyCredentialRequestOptions
}

interface CredentialLike {
  id: string
  rawId: ArrayBuffer
  type: string
  authenticatorAttachment?: string | null
  response: Record<string, unknown> & { getTransports?: () => string[] }
  getClientExtensionResults?: () => unknown
}

/** A registration credential to the JSON the server parses. */
export function attestationJSON(cred: Credential | null): Json {
  const c = cred as unknown as CredentialLike
  const r = c.response
  return {
    id: c.id,
    rawId: bufferToB64url(c.rawId),
    type: c.type,
    authenticatorAttachment: c.authenticatorAttachment ?? undefined,
    response: {
      clientDataJSON: bufferToB64url(r.clientDataJSON as ArrayBuffer),
      attestationObject: bufferToB64url(r.attestationObject as ArrayBuffer),
      transports: typeof r.getTransports === 'function' ? r.getTransports() : [],
    },
    clientExtensionResults: c.getClientExtensionResults?.() ?? {},
  }
}

/** An authentication credential to the JSON the server parses. */
export function assertionJSON(cred: Credential | null): Json {
  const c = cred as unknown as CredentialLike
  const r = c.response
  return {
    id: c.id,
    rawId: bufferToB64url(c.rawId),
    type: c.type,
    authenticatorAttachment: c.authenticatorAttachment ?? undefined,
    response: {
      authenticatorData: bufferToB64url(r.authenticatorData as ArrayBuffer),
      clientDataJSON: bufferToB64url(r.clientDataJSON as ArrayBuffer),
      signature: bufferToB64url(r.signature as ArrayBuffer),
      userHandle: r.userHandle ? bufferToB64url(r.userHandle as ArrayBuffer) : undefined,
    },
    clientExtensionResults: c.getClientExtensionResults?.() ?? {},
  }
}

/** True for the DOMException a browser throws when the user dismisses the prompt. */
export function webauthnCancelled(e: unknown): boolean {
  return e instanceof DOMException && (e.name === 'NotAllowedError' || e.name === 'AbortError')
}
