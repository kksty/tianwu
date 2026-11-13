import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { deepEqual } from './equal'
import { createRequestGate } from './requests'
import { locationPath, subscribeLocation } from './navigation'

/** Read the current route on both application navigation and Back/Forward. */
export function useConsoleLocation(): URL {
  const [path, setPath] = useState(locationPath)
  useEffect(() => subscribeLocation(() => setPath(locationPath())), [])
  return useMemo(() => new URL(path, window.location.origin), [path])
}

export interface AsyncState<T> {
  data: T | undefined
  error: string | undefined
  loading: boolean
  refreshing: boolean
  updatedAt?: number
  reload: () => void
  setData: (next: T) => void
}

function useResource<T>(load: () => Promise<T>, deps: unknown[]) {
  const [data, setSnapshot] = useState<T>()
  const [error, setError] = useState<string>()
  const [loading, setLoading] = useState(true)
  const [updatedAt, setUpdatedAt] = useState<number>()
  const [refreshing, setRefreshing] = useState(false)
  const [requests] = useState(() => createRequestGate<T>())
  const loadRef = useRef(load)
  loadRef.current = load
  const dataRef = useRef<T>()

  const setData = useCallback((value: T) => {
    dataRef.current = value
    setSnapshot(prev => deepEqual(prev, value) ? prev : value)
  }, [])
  const run = useCallback((replace: boolean) => requests.run(() => loadRef.current(), {
    start: () => {
      setLoading(dataRef.current === undefined)
      setRefreshing(dataRef.current !== undefined)
    },
    value: value => { setData(value); setUpdatedAt(Date.now()); setError(undefined) },
    error: e => setError(e instanceof Error ? e.message : String(e)),
    finish: () => { setLoading(false); setRefreshing(false) },
  }, replace), [requests, setData])

  useEffect(() => {
    void run(true)
    return () => requests.invalidate()
    // Each dependency describes the resource/query; the caller owns that list.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [...deps, run, requests])

  const reload = useCallback(() => { void run(true) }, [run])
  const revalidate = useCallback(() => { void run(false) }, [run])
  return { data, error, loading, refreshing, updatedAt, reload, setData, revalidate }
}

/** Keep the last successful snapshot while a new query or refresh is loading. */
export function useLoad<T>(load: () => Promise<T>, deps: unknown[] = []): AsyncState<T> {
  return useResource(load, deps)
}

/** Polling and manual refresh share request ordering, loading and error state. */
export function usePoll<T>(load: () => Promise<T>, ms: number, deps: unknown[] = []): AsyncState<T> {
  const state = useResource(load, deps)
  const { revalidate } = state
  const previousMs = useRef<number>()
  useEffect(() => {
    const previous = previousMs.current
    previousMs.current = ms
    if (ms <= 0) return
    const tick = () => {
      if (document.visibilityState !== 'hidden') revalidate()
    }
    if (previous !== undefined && previous <= 0) tick()
    const timer = window.setInterval(tick, ms)
    const visible = () => { if (document.visibilityState === 'visible') tick() }
    document.addEventListener('visibilitychange', visible)
    return () => {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', visible)
    }
  }, [ms, revalidate])
  return state
}

export function useDebounced<T>(value: T, ms = 250): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = window.setTimeout(() => setV(value), ms)
    return () => window.clearTimeout(t)
  }, [value, ms])
  return v
}
