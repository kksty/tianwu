// The graph page's search/filter card (T6, docs/33 §4.6). Two groups:
//  1) the historical card (retrieval mode, whole-ledger search, match list,
//     free-text node search, fact-kind chips, match count);
//  2) the drawing-only filters added by T7's lib/graph-filter.ts: recency
//     window over facts, hide finished branches, keep only the selected chain.
// None of these change what the ledger says; they change what is drawn.
//
// Layout: 228px wide, scrolls inside the viewport on short screens so the card
// stays usable at 390px wide phones without covering the canvas permanently.

import { useEffect, useRef, useState } from 'react'
import { Search } from 'lucide-react'
import { Button, Chip, Input } from '@cyber/ui'
import { api } from '@/lib/api'
import { useLoad } from '@/lib/hooks'
import { GRAPH_TIME_RANGES } from '@/lib/graph-filter'
import type { Fact } from '@/lib/types'
import type { GraphFiltersState } from './use-graph-model'

const FACT_KINDS = ['vuln', 'cred', 'host', 'service', 'loot', 'artifact', 'note']

export interface GraphFiltersProps {
  /** Project id: the index build and the search are project-scoped. */
  current: string
  /** Debounced query the canvas highlights; owned by GraphPanel. */
  q: string
  onQueryChange: (q: string) => void
  /** Size of the current match set, or null when the query is empty. */
  matchCount: number | null
  /** Board fact counts per kind, for the chips. */
  counts: Record<string, number>
  filters: GraphFiltersState
  onFiltersChange: (next: GraphFiltersState) => void
  /** Jump the map to a fact found by the whole-ledger search. */
  onFocusFact: (factID: string) => void
  /** focusOnly needs a selection to walk from; disabled without one. */
  hasSelection: boolean
}

export function GraphFilters({
  current,
  q,
  onQueryChange,
  matchCount,
  counts,
  filters,
  onFiltersChange,
  onFocusFact,
  hasSelection,
}: GraphFiltersProps) {
  const [searchMode, setSearchMode] = useState('lexical')
  const [searchCursor, setSearchCursor] = useState('')
  const [searchTerm, setSearchTerm] = useState('')
  const [globalMatches, setGlobalMatches] = useState<Fact[]>([])
  const [searchingGraph, setSearchingGraph] = useState(false)
  const embedding = useLoad(api.retrievalStatus, [])
  const [indexing, setIndexing] = useState(false)
  const [indexProgress, setIndexProgress] = useState('')
  // Search/index failures are local to this card (the page-level action error
  // belongs to pause/decide); never swallow a rejected promise.
  const [searchError, setSearchError] = useState('')
  const indexLive = useRef(true)
  const indexGeneration = useRef(0)
  useEffect(() => {
    indexGeneration.current++
    indexLive.current = true
    setIndexing(false)
    setIndexProgress('')
    setSearchError('')
    setGlobalMatches([])
    setSearchCursor('')
    return () => {
      indexLive.current = false
      indexGeneration.current++
    }
  }, [current])

  async function buildIndex() {
    setIndexing(true)
    setSearchError('')
    let cursor = ''
    const generation = indexGeneration.current
    try {
      do {
        const page = await api.indexFacts(cursor)
        if (!indexLive.current || generation !== indexGeneration.current) return
        setIndexProgress(`尚待读取 ${page.remaining_to_visit}/${page.total}`)
        cursor = page.next_cursor ?? ''
        if (!page.has_more) break
      } while (cursor && indexLive.current && generation === indexGeneration.current)
      setIndexProgress('索引已就绪')
    } catch (e) {
      if (indexLive.current && generation === indexGeneration.current) {
        setSearchError(e instanceof Error ? e.message : String(e))
      }
    } finally {
      if (indexLive.current && generation === indexGeneration.current) setIndexing(false)
    }
  }

  async function searchGraph(cursor = '') {
    setSearchingGraph(true)
    setSearchError('')
    try {
      const term = cursor ? searchTerm : q
      const page = await api.factsPage({ q: term, mode: searchMode, limit: 20, cursor })
      setSearchTerm(term)
      setSearchCursor(page.next_cursor ?? '')
      setGlobalMatches((previous) => (cursor ? [...previous, ...page.facts] : page.facts))
    } catch (e) {
      setSearchError(e instanceof Error ? e.message : String(e))
    } finally {
      setSearchingGraph(false)
    }
  }

  function toggleKind(kind: string) {
    const next = new Set(filters.hiddenKinds)
    if (next.has(kind)) next.delete(kind)
    else next.add(kind)
    onFiltersChange({ ...filters, hiddenKinds: next })
  }

  const patch = (part: Partial<GraphFiltersState>) => onFiltersChange({ ...filters, ...part })

  return (
    <div className="pointer-events-none absolute left-3 top-3 w-[232px]">
      <div className="pointer-events-auto max-h-[calc(100vh-10rem)] overflow-y-auto rounded-xl border border-hairline bg-card/90 p-3 shadow-lifted backdrop-blur">
        {/* One 12px toolbar: search first, then the retrieval mode, then the
            drawing-only filters. Everything is the same face size so the card
            reads as a control strip, not a form. */}
        <div className="relative mb-2">
          <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-3" />
          <Input
            value={q}
            onChange={(e) => onQueryChange(e.target.value)}
            placeholder="搜索节点"
            aria-label="搜索节点"
            className="h-8 border-hairline bg-surface-2/50 pl-8 text-[12px]"
          />
        </div>
        <div className="mb-2 flex flex-wrap gap-1">
          {FACT_KINDS.map((k) => (
            <Chip
              key={k}
              active={!filters.hiddenKinds.has(k)}
              onClick={() => toggleKind(k)}
              className="gap-1 px-2 py-0.5 text-[11px] leading-4"
              title={filters.hiddenKinds.has(k) ? `显示 ${k}` : `隐藏 ${k}`}
            >
              {k}
              <span className="tw-nums font-mono text-[11px] opacity-60">{counts[k] ?? 0}</span>
            </Chip>
          ))}
        </div>
        {matchCount !== null && (
          <div className="mb-1.5 text-[11px] text-ink-3">
            命中 <span className="tw-nums font-mono font-semibold text-ink-2">{matchCount}</span> 个节点
          </div>
        )}

        <div className="flex gap-1.5">
          <select
            className="h-8 min-w-0 flex-1 rounded-md border border-hairline bg-surface-2/50 px-2 text-[12px] text-ink-2 outline-none transition-colors hover:border-ink-3/40 focus-visible:border-ring/60 focus-visible:ring-2 focus-visible:ring-ring/35"
            value={searchMode}
            aria-label="检索模式"
            onChange={(e) => {
              setSearchMode(e.target.value)
              setSearchCursor('')
              setGlobalMatches([])
            }}
          >
            <option value="lexical">全文检索</option>
            <option value="semantic" disabled={!embedding.data?.configured}>
              语义检索
            </option>
            <option value="hybrid" disabled={!embedding.data?.configured}>
              混合检索
            </option>
          </select>
          {embedding.data?.configured && (
            <Button size="xs" variant="ghost" disabled={indexing} onClick={() => void buildIndex()}>
              构建索引
            </Button>
          )}
        </div>
        <div className="mt-1 flex items-center gap-1.5">
          <Button size="xs" variant="ghost" onClick={() => void searchGraph()} disabled={searchingGraph || !q.trim()}>
            全库检索
          </Button>
          <span className="text-[11px] text-ink-3">边界节点可点击展开</span>
        </div>
        {indexProgress && <div className="mt-1 text-[11px] text-ink-3">{indexProgress}</div>}
        {searchError && <div className="mt-1 text-[11px] text-destructive">{searchError}</div>}
        {globalMatches.map((f) => (
          <button
            key={f.id}
            className="mt-1 block w-full truncate rounded px-1.5 py-1 text-left text-[12px] text-info transition-colors hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
            onClick={() => {
              setGlobalMatches([])
              onQueryChange('')
              onFocusFact(f.id)
            }}
          >
            {f.title}
          </button>
        ))}
        {searchCursor && (
          <Button size="xs" variant="ghost" disabled={searchingGraph} onClick={() => void searchGraph(searchCursor)}>
            继续检索结果
          </Button>
        )}

        {/* Drawing-only filters. The ledger is never cut: these decide what the
            canvas draws (and therefore what the layout/hit-test sees). */}
        <div className="mt-2.5 space-y-2 border-t border-hairline pt-2.5">
          <div className="flex items-center gap-2">
            <span className="shrink-0 text-[11px] text-ink-3">时间</span>
            <select
              className="h-7 min-w-0 flex-1 rounded-md border border-hairline bg-surface-2/50 px-1.5 text-[11px] text-ink-2 outline-none transition-colors hover:border-ink-3/40 focus-visible:border-ring/60 focus-visible:ring-2 focus-visible:ring-ring/35"
              value={filters.timeRange}
              aria-label="时间范围（按事实创建时间）"
              title="只按事实创建时间过滤；没有时间戳的事实视为「时间未知」而保留"
              onChange={(e) => patch({ timeRange: e.target.value as GraphFiltersState['timeRange'] })}
            >
              {GRAPH_TIME_RANGES.map((range) => (
                <option key={range.id} value={range.id}>
                  {range.label}
                </option>
              ))}
            </select>
          </div>
          <label className="flex cursor-pointer items-center gap-2 text-[12px] text-ink-2 transition-colors hover:text-ink">
            <input
              type="checkbox"
              className="h-3.5 w-3.5 accent-primary"
              checked={filters.hideDone}
              onChange={(e) => patch({ hideDone: e.target.checked })}
            />
            隐藏已完成分支
          </label>
          <label
            className={`flex items-center gap-2 text-[12px] transition-colors ${
              hasSelection ? 'cursor-pointer text-ink-2 hover:text-ink' : 'cursor-not-allowed text-ink-3/50'
            }`}
            title={hasSelection ? '只画当前选中项的上下游链路' : '先选中一个节点，才能只看它的链路'}
          >
            <input
              type="checkbox"
              className="h-3.5 w-3.5 accent-primary"
              checked={filters.focusOnly}
              disabled={!hasSelection}
              onChange={(e) => patch({ focusOnly: e.target.checked })}
            />
            只显示当前链路
          </label>
        </div>
      </div>
    </div>
  )
}
