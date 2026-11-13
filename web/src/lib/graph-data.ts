import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type GraphRead } from './api.ts'
import {
  GRAPH_BATCH_MAX_PAGES,
  GRAPH_FOCUS_MAX_PAGES,
  GRAPH_PAGE_LIMIT,
  batchComplete,
  graphHasRoot,
  graphPageKey,
  mergeGraphPages,
} from './graph-pages.ts'

// The pure page algebra also lives in graph-pages.ts (testable without React);
// re-exported here so callers that already import this module keep one entry.
export { graphPageKey, mergeGraphPages } from './graph-pages.ts'

// The graph page is a fixed snapshot: expanded pages belong to one snapshot and
// new content is admitted only by reloading a whole snapshot (manual refresh, or
// the 实时 probe in GraphPanel), so a live write can never move the cursor
// underneath it. Pages of two versions are never merged.
//
// The first screen is fetched in a bounded batch (structural order + graph
// projection) and set once. Calling setData per page would rebuild the model —
// and re-run the layout — while the operator is already looking at the map.
// Leaving the page keeps the data: only a refresh or a project switch swaps the
// snapshot.
export function useGraphSnapshot(active: boolean, project: string) {
  const [data, setData] = useState<GraphRead>()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string>()
  const [root, setRoot] = useState('')
  const [busy, setBusy] = useState(false)
  const generation = useRef(0)
  const dataRef = useRef<GraphRead>()
  const loaded = useRef(false)
  const projectRef = useRef(project)

  const write = useCallback((next: GraphRead) => {
    dataRef.current = next
    loaded.current = true
    setData(next)
  }, [])

  const load = useCallback(
    async (nextRoot: string) => {
      const request = ++generation.current
      setLoading(true)
      setError(undefined)
      setBusy(false)
      setRoot(nextRoot)
      try {
        const maxPages = nextRoot ? GRAPH_FOCUS_MAX_PAGES : GRAPH_BATCH_MAX_PAGES
        let acc: GraphRead | undefined
        let failure: unknown
        let snapshot = ''
        let cursor = ''
        for (let page = 0; page < maxPages; page++) {
          let next: GraphRead
          try {
            next = await api.graph({
              ...(nextRoot ? { root: nextRoot } : {}),
              ...(cursor ? { cursor } : {}),
              limit: GRAPH_PAGE_LIMIT,
              order: 'structural',
              projection: 'graph',
            })
          } catch (e) {
            // A mid-batch failure degrades to the pages already on hand; only a
            // batch that produced nothing is an error.
            failure = e
            break
          }
          if (generation.current !== request) return
          const key = graphPageKey(next)
          if (page === 0) snapshot = key
          // The snapshot moved between pages: keep the consistent prefix.
          else if (key !== snapshot) break
          acc = acc ? mergeGraphPages(acc, next) : next
          const pages = page + 1
          if (!next.has_more || pages >= maxPages) break
          if (nextRoot ? graphHasRoot(acc, nextRoot) : batchComplete(acc, pages)) break
          cursor = next.next_cursor ?? ''
          if (!cursor) break
        }
        if (generation.current !== request) return
        if (!acc) throw failure ?? new Error('graph snapshot is empty')
        write(acc)
      } catch (e) {
        if (generation.current === request) {
          setError(e instanceof Error ? e.message : String(e))
        }
      } finally {
        if (generation.current === request) setLoading(false)
      }
    },
    [write],
  )

  useEffect(() => {
    const switched = projectRef.current !== project
    if (switched) {
      // A different project is a different ledger: drop the old snapshot even
      // while the page is inactive, so the first activation cannot show it.
      projectRef.current = project
      generation.current++
      dataRef.current = undefined
      loaded.current = false
      setData(undefined)
      setRoot('')
      setError(undefined)
    }
    if (!active) {
      // Leaving the page keeps the snapshot; only the loading flag settles.
      setLoading(false)
      return
    }
    if (switched || !loaded.current) void load('')
  }, [active, project, load])

  const reload = useCallback(() => {
    void load(root)
  }, [load, root])

  const more = useCallback(async () => {
    const current = dataRef.current
    if (!current?.has_more || !current.next_cursor || busy) return
    const request = generation.current
    setBusy(true)
    setError(undefined)
    try {
      const page = await api.graph({
        ...(root ? { root } : {}),
        cursor: current.next_cursor,
        limit: GRAPH_PAGE_LIMIT,
        order: 'structural',
        projection: 'graph',
      })
      if (request !== generation.current) return
      write(mergeGraphPages(current, page))
    } catch (e) {
      if (request === generation.current) {
        setError(e instanceof Error ? e.message : String(e))
      }
    } finally {
      if (request === generation.current) setBusy(false)
    }
  }, [busy, root, write])

  return { data, loading, error, root, busy, reload, more, focus: load }
}
