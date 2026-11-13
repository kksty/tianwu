import { useCallback, useRef, useState, type SetStateAction } from 'react'
import { readViewMemory, writeViewMemory } from './view-memory'

function compatible(value: unknown, initial: unknown): boolean {
  if (initial === null) return value === null
  if (Array.isArray(initial)) return Array.isArray(value) && value.every(item => typeof item === 'string')
  if (typeof initial === 'object') return !!value && typeof value === 'object' && !Array.isArray(value) &&
    Object.entries(initial).every(([key, fallback]) => compatible((value as Record<string, unknown>)[key], fallback))
  return typeof value === typeof initial
}

/** Persist at the edit boundary, so navigating away cannot beat an effect. */
export function useSessionState<T>(key: string, initial: T) {
  const scope = `draft:${window.location.pathname}`
  const [value, setValue] = useState<T>(() => {
    const saved = readViewMemory<T>(scope, key)
    return saved !== undefined && compatible(saved, initial) ? saved : initial
  })
  const latest = useRef(value)
  const set = useCallback((action: SetStateAction<T>) => {
    const next = typeof action === 'function' ? (action as (previous: T) => T)(latest.current) : action
    latest.current = next
    writeViewMemory(scope, key, next)
    setValue(next)
  }, [scope, key])
  return [value, set] as const
}
