import { useCallback, useEffect, useState } from 'react'
import { ApiError } from '../api'

export type Load<T> =
  | { state: 'loading' }
  | { state: 'ready'; data: T }
  | { state: 'unavailable' }
  | { state: 'error' }

/** Loads data on mount and on reload(). A 404 means the feature is not available. */
export function useLoad<T>(fetcher: () => Promise<T>) {
  const [load, setLoad] = useState<Load<T>>({ state: 'loading' })
  const reload = useCallback(async () => {
    try {
      const data = await fetcher()
      setLoad({ state: 'ready', data })
    } catch (err) {
      setLoad(err instanceof ApiError && err.status === 404 ? { state: 'unavailable' } : { state: 'error' })
    }
    // fetcher is stable per section (bound to the client)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  useEffect(() => {
    void reload()
  }, [reload])
  return [load, reload] as const
}
