import { useCallback, useEffect, useRef, useState } from 'react'
import { Button } from '@cyber/ui'
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
 * registry wires the route with these values instead of restating the label. */
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
    <div className="h-full overflow-auto p-3 text-xs" data-testid="focus-panel">
      <p className="mb-3 text-muted-foreground">
        焦点是本次派发的有界索引：热层是必要前提、固定与参考事实，温层是来源链与同一记录实体的跨分支观察，冷层是其余事实。索引截断或未列出不等于事实不存在；冷层、完整前提目录（
        <code>.tw/context.json</code>）与按 id 读取始终可用。只读，不是新结论。
      </p>

      {intent ? null : (
        <label className="mb-3 block text-muted-foreground">
          方向
          <select
            aria-label="焦点方向"
            className="mt-1 w-full max-w-md rounded border border-border bg-background p-2"
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
        </label>
      )}

      <div className="mb-3 flex flex-wrap items-center gap-2" data-testid="focus-layer-switch">
        {FOCUS_LAYERS.map((option) => (
          <Button
            key={option.id}
            size="xs"
            variant="ghost"
            active={layer === option.id}
            disabled={loading}
            title={option.hint}
            onClick={() => setLayer(option.id)}
          >
            {option.label}
          </Button>
        ))}
        <span className="text-muted-foreground">{focusLayerHint(layer)}</span>
      </div>

      <div className="mb-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted-foreground" data-testid="focus-counts">
        <span>热 {counts.hot}</span>
        <span>温 {counts.warm}</span>
        <span>冷 {counts.cold}</span>
        <span>{focusSnapshotLabel(page)}</span>
        <span>本页 {focusLayerLabel(layer)}</span>
      </div>

      {!target ? (
        <p className="text-muted-foreground">选择一个方向后读取它的焦点投影。</p>
      ) : null}

      <ul className="space-y-2">
        {page?.facts.map((fact) => (
          <FocusRow key={fact.id} fact={fact} project={project} onOpenFact={onOpenFact} />
        ))}
      </ul>

      {target && page && page.facts.length === 0 ? (
        <p className="text-muted-foreground">
          {layer === 'cold' ? '冷层没有更多事实。' : '该方向没有热/温事实；切到冷层可读其余事实。'}
        </p>
      ) : null}

      <div className="mt-3 flex flex-wrap items-center gap-2">
        <span className="text-muted-foreground">
          已载入 {page?.facts.length ?? 0} / {page?.total ?? 0}
        </span>
        {page?.has_more && page.next_cursor ? (
          <Button size="xs" disabled={loading} onClick={() => void load(page.next_cursor ?? '', false)}>
            继续加载
          </Button>
        ) : null}
        {layer === 'cold' ? null : (
          <Button size="xs" variant="ghost" disabled={loading} onClick={() => setLayer('cold')}>
            查看冷层
          </Button>
        )}
        <Button size="xs" variant="ghost" disabled={loading || !target} onClick={() => void load('', true)}>
          刷新
        </Button>
      </div>

      {intents.error ? <p className="mt-2 text-destructive">方向列表读取失败：{intents.error}</p> : null}
      {error ? (
        <p role="alert" className="mt-2 text-destructive">
          焦点读取失败：{error}
        </p>
      ) : null}
      {loading ? <p className="mt-2 text-muted-foreground">读取中…</p> : null}
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
      className="rounded border border-border p-2"
      data-testid="focus-fact"
      data-temperature={fact.temperature}
      data-status={fact.status}
    >
      <div className="mb-1 flex flex-wrap items-center gap-2 text-[11px]">
        <span className={badgeClass(fact.temperature)}>{temperatureLabel(fact.temperature)}</span>
        <span className={missing ? 'text-destructive' : 'text-muted-foreground'}>{focusStatusLabel(fact.status)}</span>
        <span className="font-mono text-muted-foreground">{fact.id}</span>
        {fact.kind ? <span className="text-muted-foreground">{fact.kind}</span> : null}
      </div>
      {fact.title ? <p className="tw-break line-clamp-2 font-medium">{fact.title}</p> : null}
      {fact.summary ? <p className="tw-break line-clamp-3 text-muted-foreground">{fact.summary}</p> : null}
      <div className="mt-1 flex flex-wrap items-center gap-2 text-[11px] text-muted-foreground">
        {fact.reasons.map((reason) => (
          <span key={reason} className="rounded bg-muted/40 px-1.5 py-0.5">
            {focusReasonLabel(reason)}
          </span>
        ))}
        {fact.source_run_id ? <span>来源 run {fact.source_run_id}</span> : null}
        {fact.observed_at ? <span>观察于 {fact.observed_at}</span> : null}
        <span>断言版本 v{fact.claim_version ?? '—'}</span>
        <span>{fact.record_version === undefined ? '记录版本未知' : `记录版本 v${fact.record_version}`}</span>
        <a className="underline" href={focusFactQueryHref(fact.query, project)} target="_blank" rel="noreferrer">
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
  if (temperature === 'hot') return 'rounded bg-destructive/15 px-1.5 py-0.5 text-destructive'
  if (temperature === 'warm') return 'rounded bg-amber-500/15 px-1.5 py-0.5 text-amber-600'
  return 'rounded bg-muted px-1.5 py-0.5 text-muted-foreground'
}
