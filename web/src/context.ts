import { createContext, useContext } from 'react'
import type { Client, SessionState } from './api'

export interface AppContextValue {
  client: Client
  navigate: (path: string, opts?: { replace?: boolean; notice?: string }) => void
  session: SessionState | null
  setSession: (s: SessionState | null) => void
  /** Leave the app for an external URL (identity provider). */
  leave: (url: string) => void
}

export const AppContext = createContext<AppContextValue | null>(null)

export function useApp(): AppContextValue {
  const v = useContext(AppContext)
  if (!v) throw new Error('useApp outside AppContext')
  return v
}
