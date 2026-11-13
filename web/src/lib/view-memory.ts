/**
 * Per-view memory for the console shell (docs/34 §2).
 *
 * Two kinds of state share one sessionStorage namespace:
 *
 *  - the shell's view memory: leaving a view records `{ tab, search, hash }`
 *    (scope `project:<id>`, key = the view id) so a rail click can put the
 *    operator back on the same tab, the same record and the same graph
 *    selection;
 *  - scroll positions for the `<Page>` body (scope `scroll`, key =
 *    `pathname+search`), so returning to a page keeps the reading position.
 *
 * Every read is defensive. sessionStorage can be unavailable (server render,
 * sandboxed iframe), full (private mode) or hold a payload written by an older
 * build; losing memory is allowed, breaking the console is not. Values must be
 * JSON-serializable — that is the caller's contract.
 *
 * Memory is intentionally *session* storage: a new tab starts without another
 * tab's reading position, and a reload keeps it.
 */

import { useEffect, useLayoutEffect, useRef, type RefObject } from 'react'

const STORAGE_KEY = 'tw.viewMemory.v1'
/** Scroll positions are namespaced under this scope; views use `project:<id>`. */
const SCROLL_SCOPE = 'scroll'
/**
 * How long a scroll restore may wait for asynchronously loaded content to grow
 * the page tall enough (~2.5s at 60fps). After that the old value is kept: a
 * clamped top must not overwrite the position the operator actually left.
 */
const RESTORE_FRAMES = 150

type MemoryStore = Record<string, Record<string, unknown>>

function sessionStore(): Storage | null {
  try {
    return typeof window === 'undefined' ? null : window.sessionStorage
  } catch {
    return null
  }
}

function readStore(): MemoryStore {
  const storage = sessionStore()
  if (!storage) return {}
  try {
    const raw = storage.getItem(STORAGE_KEY)
    if (!raw) return {}
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    return parsed as MemoryStore
  } catch {
    return {}
  }
}

function writeStore(store: MemoryStore): void {
  const storage = sessionStore()
  if (!storage) return
  try {
    storage.setItem(STORAGE_KEY, JSON.stringify(store))
  } catch {
    // Quota exceeded / private mode: losing memory must never fail the page.
  }
}

/** Read a value; `undefined` on a missing scope/key or an unreadable payload. */
export function readViewMemory<T>(scope: string, key: string): T | undefined {
  if (!scope || !key) return undefined
  const bucket = readStore()[scope]
  return bucket ? (bucket[key] as T | undefined) : undefined
}

/** Write one value under `scope`/`key`; other scopes/keys are preserved. */
export function writeViewMemory<T>(scope: string, key: string, value: T): void {
  if (!scope || !key) return
  const store = readStore()
  const bucket = store[scope] ?? {}
  bucket[key] = value
  store[scope] = bucket
  writeStore(store)
}

/**
 * Remember where the operator was reading inside the page body. The key is
 * `pathname+search`, so every view — and every deep-linked record — keeps its
 * own position; the hash is excluded on purpose, so selecting a graph node does
 * not reset the scroll of the page that shows it. Works for /g/ pages too.
 *
 * Attach the returned ref to the scrolling element. A page that owns its own
 * panes (`scroll={false}`) simply does not attach it: the hook still runs but
 * has no element to observe or restore.
 */
export function useScrollMemory<T extends HTMLElement>(): RefObject<T> {
  const key = typeof window === 'undefined' ? '' : `${window.location.pathname}${window.location.search}`
  return useScrollMemoryOn<T>(key)
}

/**
 * Same as {@link useScrollMemory}, with an explicit key for split-pane pages
 * whose inner list is its own scroll container (the key must be unique per pane,
 * e.g. `run-stream:<runID>`).
 */
export function useScrollMemoryOn<T extends HTMLElement>(key: string): RefObject<T> {
  const ref = useRef<T>(null)

  // The passive cleanup can run after React detached the node, where `scrollTop`
  // may read as 0. A layout cleanup still sees the element laid out, so capture
  // the position there and let the passive cleanup persist that number.
  const finalTop = useRef(0)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    finalTop.current = el.scrollTop
    return () => {
      if (el.isConnected) finalTop.current = el.scrollTop
    }
  }, [key])

  useEffect(() => {
    const el = ref.current
    if (!el || !key) return

    const saved = readViewMemory<number>(SCROLL_SCOPE, key)
    const target = typeof saved === 'number' && Number.isFinite(saved) && saved > 0 ? saved : 0

    let frame = 0
    let saveFrame = 0
    let disposed = false
    // A restore is in flight: ignore the scroll events it causes and do not let
    // a clamped intermediate position overwrite the saved one.
    let restoring = target > 0
    // The saved position was reached (or there was nothing to restore).
    let restored = target === 0
    // The operator scrolled/typed while the page was still growing; they win.
    let userMoved = false
    let attempts = 0
    // Last position seen while the element was still laid out. The effect
    // cleanup can run after React detached the node, where `scrollTop` may read
    // as 0, so the remembered number is the source of truth on the way out.
    let lastTop = el.scrollTop

    /** Live position while connected; the synchronously tracked one otherwise. */
    const currentTop = () => {
      if (el.isConnected) lastTop = el.scrollTop
      return lastTop
    }

    const persist = () => {
      if (disposed || restoring) return
      writeViewMemory(SCROLL_SCOPE, key, currentTop())
    }

    const frameRestore = () => {
      frame = 0
      if (disposed || !restoring) return
      attempts += 1
      el.scrollTop = target
      lastTop = el.scrollTop
      finalTop.current = lastTop
      if (Math.abs(el.scrollTop - target) <= 1) {
        restoring = false
        restored = true
        return
      }
      if (attempts >= RESTORE_FRAMES) {
        // Content never grew enough (record deleted, shorter page). Keep the
        // previous memory and stop fighting the layout.
        restoring = false
        return
      }
      frame = requestAnimationFrame(frameRestore)
    }

    const stopRestore = () => {
      userMoved = true
      if (restoring) restoring = false
    }

    const onScroll = () => {
      // Track synchronously: a throttled save or an unmount must not lose the
      // operator's last position to a later layout change.
      lastTop = el.scrollTop
      finalTop.current = lastTop
      if (restoring || saveFrame) return
      saveFrame = requestAnimationFrame(() => {
        saveFrame = 0
        persist()
      })
    }

    const onPageHide = () => {
      if (saveFrame) {
        cancelAnimationFrame(saveFrame)
        saveFrame = 0
      }
      persist()
    }

    const inputEvents = ['wheel', 'touchstart', 'pointerdown', 'keydown'] as const
    if (restoring) frame = requestAnimationFrame(frameRestore)
    el.addEventListener('scroll', onScroll, { passive: true })
    for (const type of inputEvents) {
      window.addEventListener(type, stopRestore, { capture: true, passive: true })
    }
    window.addEventListener('pagehide', onPageHide)

    return () => {
      disposed = true
      if (frame) cancelAnimationFrame(frame)
      if (saveFrame) cancelAnimationFrame(saveFrame)
      el.removeEventListener('scroll', onScroll)
      for (const type of inputEvents) {
        window.removeEventListener(type, stopRestore, { capture: true })
      }
      window.removeEventListener('pagehide', onPageHide)
      // An unfinished restore (unmounted mid-flight / given up) must not replace
      // the remembered position with the clamped top it was still reaching for.
      if (restored || userMoved) writeViewMemory(SCROLL_SCOPE, key, finalTop.current)
    }
  }, [key])

  return ref
}
