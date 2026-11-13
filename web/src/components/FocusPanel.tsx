import { useCallback, useEffect, useRef, useState } from 'react'
import { Crosshair, RefreshCw } from 'lucide-react'
import { Button, Callout, EmptyState, SkeletonList } from '@cyber/ui'
import { api, projectID } from '@/lib/api'
import { usePoll } from '@/lib/hooks'
import {
  FOCUS_LAYERS,
  focusCounts,
  focusFactQueryHref,
  focusLayerHint,
  focusLayerLabel,
  focusReasonLabel,
  focusSnapshotLabel,
  focusStatusLabel,
  mergeFocusPages,
} from '@/lib/focus'
import type { FocusFact, FocusLayer, FocusPage } from '@/lib/focus'

const PAGE_LIMIT = 50

/** Host-shell descriptor for the panel. A shell that renders panels from its own
 *  registry wires the route with these values instead of restating the label. */
export const focusPanel = { id: 'focus', label: '焦点' } as const

/**
 * Read-only focus panel: the task's hot/warm/cold projection with the recorded
 * reasons, the frozen snapshot it was cut from, and cursor pagination. It is an
 * index over the ledger, not a second store: cold rows stay queryable with the
 * layer switch, and the run's full premise directory is untouched.
 *
 * `intent` is supplied when the host view already knows the direction (e.g. a
 * graph selection); otherwise the panel offers a direction picker. A
 * participant calling the API is always scoped to its own run by the server.
 */
export function FocusPanel({
  intent,
  onOpenFact,
}: {
  intent?: string
  /** Optional host action: open the fact on the graph. The API link is always there. */
  onOpenFact?: (id: string) => void
}) {
  const intents = usePoll(api.intents, 0)
  const [picked, setPicked] = useState('')
  const [layer, setLayer] = useState<FocusLayer>('focus')
  const [page, setPage] = useState<FocusPage>()
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const generation = useRef(0)
  const target = intent ?? picked
  const project = projectID()

  const load = useCallback(
    async (cursor: string, replace: boolean) => {
      if (!target) return
      const request = ++generation.current
      setLoading(true)
      setError('')
      try {
        const next = await api.focus({
          intent: target,
          layer,
          cursor: cursor || undefined,
          limit: PAGE_LIMIT,
        })
        if (request !== generation.current) return
        setPage((previous) => (cursor ? mergeFocusPages(previous, next) : next))
      } catch (caught) {
        if (request === generation.current) setError(caught instanceof Error ? caught.message : String(caught))
      } finally {
        if (request === generation.current) setLoading(false)
      }
    },
    [target, layer],
  )

  useEffect(() => {
    // A new direction or layer is a new query: the old page and its cursor no
    // longer describe what the operator is looking at.
    setPage(undefined)
    if (target) void load('', true)
  }, [target, layer, load])

  useEffect(
    () => () => {
      // A late response from the retired query must not repaint the panel.
      generation.current++
    },
    [],
  )

  const counts = focusCounts(page)

  return (
    <div className="flex h-full min-h-0 flex-col" data-testid="focus-panel">
      <div className="shrink-0 border-b border-hairline px-5 py-3 text-[12px] leading-5 text-ink-2 sm:px-6 lg:px-8">
        焦点是本次派发的有界索引：热层是必要前提、固定与参考事实，温层是来源链与同一记录实体的跨分支观察，冷层是其余事实。索引截断或未列出不等于事实不存在；冷层、完整前提目录（
        <code className="font-mono text-[11px] text-ink-3">.tw/context.json</code>）与按 id 读取始终可用。只读，不是新结论。
      </div>

      <div className="flex shrink-0 flex-wrap items-center gap-2 border-b border-hairline px-5 py-2 sm:px-6 lg:px-8">
        {intent ? null : (
          <select
            aria-label="焦点方向"
            className="h-8 min-w-0 max-w-[320px] rounded-md border border-hairline bg-surface-2/60 px-2 text-[12px] text-ink transition-colors hover:border-ink-3/40 focus-visible:border-ring/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/35"
            value={picked}
            onChange={(event) => setPicked(event.target.value)}
          >
            <option value="">选择一个方向…</option>
            {intents.data?.map((row) => (
              <option key={row.id} value={row.id}>
                {row.title}（{row.id}）
              </option>
            ))}
          </select>
        )}
        <div className="flex items-center gap-2" data-testid="focus-layer-switch">
          {FOCUS_LAYERS.map((option) => (
            <Button
              key={option.id}
              size="sm"
              variant="ghost"
              active={layer === option.id}
              disabled={loading}
              title={option.hint}
              onClick={() => setLayer(option.id)}
            >
              {option.label}
            </Button>
          ))}
        </div>
        <span className="min-w-0 truncate text-[11px] text-ink-3">{focusLayerHint(layer)}</span>
        <Button
          size="sm"
          variant="outline"
          className="ml-auto shrink-0"
          disabled={loading || !target}
          onClick={() => void load('', true)}
        >
          <RefreshCw className={`h-3.5 w-3.5 ${loading ? 'animate-spin' : ''}`} />
          刷新
        </Button>
      </div>

      <div className="flex shrink-0 flex-wrap items-center gap-x-4 gap-y-1 border-b border-hairline px-5 py-2 text-[11px] text-ink-3 sm:px-6 lg:px-8" data-testid="focus-counts">
        <span className="tw-nums font-mono">热 {counts.hot}</span>
        <span className="tw-nums font-mono">温 {counts.warm}</span>
        <span className="tw-nums font-mono">冷 {counts.cold}</span>
        <span>{focusSnapshotLabel(page)}</span>
        <span>本页 {focusLayerLabel(layer)}</span>
      </div>

      {intents.error && (
        <div role="alert" className="shrink-0 px-5 pt-3 sm:px-6 lg:px-8">
          <Callout tone="destructive">方向列表读取失败：{intents.error}</Callout>
        </div>
      )}
      {error && (
        <div role="alert" className="shrink-0 px-5 pt-3 sm:px-6 lg:px-8">
          <Callout tone="destructive">
            <div className="flex flex-wrap items-center gap-2">
              <span>焦点读取失败：{error}</span>
              <Button size="xs" variant="outline" disabled={loading} onClick={() => void load('', true)}>重试</Button>
            </div>
          </Callout>
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto px-5 py-3 sm:px-6 lg:px-8">
        {loading && !page && <SkeletonList count={5} />}
        {!target && (
          <EmptyState
            compact
            icon={Crosshair}
            title="选择一个方向"
            description="选定方向后，这里显示它的热/温/冷焦点投影与截取依据。"
          />
        )}
        {target && !loading && page && page.facts.length === 0 && (
          <EmptyState
            compact
            icon={Crosshair}
            title={layer === 'cold' ? '冷层没有更多事实' : '该方向没有热/温事实'}
            description={layer === 'cold' ? '这一方向的事实已经全部列完。' : '切到冷层可读其余事实；索引截断不代表事实不存在。'}
            action={layer === 'cold' ? undefined : (
              <Button size="sm" variant="outline" onClick={() => setLayer('cold')}>查看冷层</Button>
            )}
          />
        )}
        <ul className="divide-y divide-hairline">
          {page?.facts.map((fact) => (
            <FocusRow key={fact.id} fact={fact} project={project} onOpenFact={onOpenFact} />
          ))}
        </ul>
        {loading && page && (
          <div className="pt-2"><SkeletonList count={2} /></div>
        )}
      </div>

      <div className="flex shrink-0 flex-wrap items-center gap-2 border-t border-hairline px-5 py-2 sm:px-6 lg:px-8">
        <span className="tw-nums font-mono text-[11px] text-ink-3">
          已载入 {page?.facts.length ?? 0} / {page?.total ?? 0}
        </span>
        {page?.has_more && page.next_cursor ? (
          <Button size="xs" variant="outline" disabled={loading} onClick={() => void load(page.next_cursor ?? '', false)}>
            继续加载
          </Button>
        ) : null}
        {layer === 'cold' ? null : (
          <Button size="xs" variant="ghost" disabled={loading} onClick={() => setLayer('cold')}>
            查看冷层
          </Button>
        )}
      </div>
    </div>
  )
}

function FocusRow({
  fact,
  project,
  onOpenFact,
}: {
  fact: FocusFact
  project: string
  onOpenFact?: (id: string) => void
}) {
  const missing = fact.status === 'missing'
  return (
    <li
      className="py-2.5 transition-colors hover:bg-surface-2/60"
      data-testid="focus-fact"
      data-temperature={fact.temperature}
      data-status={fact.status}
    >
      <div className="flex flex-wrap items-center gap-2 text-[11px]">
        <span className={badgeClass(fact.temperature)}>{temperatureLabel(fact.temperature)}</span>
        <span className={missing ? 'font-medium text-destructive' : 'text-ink-3'}>{focusStatusLabel(fact.status)}</span>
        <span className="font-mono text-ink-3">{fact.id}</span>
        {fact.kind ? <span className="text-ink-3">{fact.kind}</span> : null}
      </div>
      {fact.title ? <p className="tw-break mt-1 line-clamp-2 text-[12px] font-medium leading-relaxed text-ink">{fact.title}</p> : null}
      {fact.summary ? <p className="tw-break mt-0.5 line-clamp-3 text-[12px] leading-relaxed text-ink-2">{fact.summary}</p> : null}
      <div className="mt-1 flex flex-wrap items-center gap-2 text-[11px] text-ink-3">
        {fact.reasons.map((reason) => (
          <span key={reason} className="rounded border border-hairline bg-surface-2 px-1.5 py-0.5">
            {focusReasonLabel(reason)}
          </span>
        ))}
        {fact.source_run_id ? <span className="font-mono">来源 run {fact.source_run_id}</span> : null}
        {fact.observed_at ? <span className="font-mono">观察于 {fact.observed_at}</span> : null}
        <span className="tw-nums font-mono">断言版本 v{fact.claim_version ?? '—'}</span>
        <span className="tw-nums font-mono">{fact.record_version === undefined ? '记录版本未知' : `记录版本 v${fact.record_version}`}</span>
        <a className="underline decoration-hairline underline-offset-2 hover:text-ink" href={focusFactQueryHref(fact.query, project)} target="_blank" rel="noreferrer">
          按 id 查询
        </a>
        {onOpenFact && !missing ? (
          <Button size="xs" variant="ghost" onClick={() => onOpenFact(fact.id)}>
            在图谱中打开
          </Button>
        ) : null}
      </div>
    </li>
  )
}

function temperatureLabel(temperature: FocusFact['temperature']): string {
  if (temperature === 'hot') return '热'
  if (temperature === 'warm') return '温'
  return '冷'
}

function badgeClass(temperature: FocusFact['temperature']): string {
  if (temperature === 'hot') {
    return 'rounded border border-destructive/25 bg-destructive/12 px-1.5 py-0.5 font-medium text-destructive'
  }
  if (temperature === 'warm') {
    return 'rounded border border-warning/25 bg-warning/12 px-1.5 py-0.5 font-medium text-warning'
  }
  return 'rounded border border-hairline bg-surface-2 px-1.5 py-0.5 text-ink-3'
}
