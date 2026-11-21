import { useEffect, useMemo, useState } from 'react'
import { WorkspaceApi, type Client, type Share } from '../api'

export interface Scope {
  key: string
  label: string
  share?: Share
  /** A view-only share: nothing may change, but favorites still work. */
  readOnly: boolean
}

/** The collections the user can open: their own and incoming shares. */
export function useScopes(client: Client) {
  const [shares, setShares] = useState<Share[]>([])
  const [shareError, setShareError] = useState('')
  const [activeKey, setActiveKey] = useState('private')

  useEffect(() => {
    let live = true
    new WorkspaceApi(client)
      .incomingShares()
      .then((d) => live && setShares(d.shares ?? []))
      .catch(() => live && setShareError('Shared folders could not be loaded.'))
    return () => {
      live = false
    }
  }, [client])

  const scopes = useMemo<Scope[]>(
    () => [
      { key: 'private', label: 'My bookmarks', readOnly: false },
      ...shares.map((s) => ({
        key: `share-${s.id}`,
        label: `${s.folderTitle} (${s.mode === 'view' ? 'view-only' : 'can edit'})`,
        share: s,
        readOnly: s.mode === 'view',
      })),
    ],
    [shares],
  )
  const active = scopes.find((s) => s.key === activeKey) ?? scopes[0]
  const api = useMemo(() => new WorkspaceApi(client, active.share?.id), [client, active.share?.id])
  return { scopes, active, setActive: setActiveKey, api, shareError }
}
