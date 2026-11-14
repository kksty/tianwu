import { useEffect, useMemo, useRef, useState } from 'react'
import {
  Activity,
  Bot,
  CircleAlert,
  CircleDashed,
  FileText,
  Gauge,
  Terminal,
  Waypoints,
} from 'lucide-react'
import { Badge, Button, Callout, Chip, EmptyState, ListRow, Meter, SkeletonCard, Spinner, StatTile } from '@cyber/ui'
import { cn } from '@cyber/theme'
import { Page } from '@/components/Page'
import { api } from '@/lib/api'
import { PHASE_LABEL, isPhaseActive, phaseDetail, phaseOf, phaseProgress } from '@/lib/decider'
import {
  cancelCauseText,
  fmtBytes,
  fmtDuration,
  fmtInt,
  fmtTime,
  hasBrokenPremise,
  kindVariant,
  oneLine,
  runCancelReason,
  runStatusLabel,
  shortID,
  statusLabel,
} from '@/lib/format'
import { useConsoleLocation, usePoll } from '@/lib/hooks'
import { navigate } from '@/lib/navigation'
import type {
  BoardResponse,
  Event as RunEvent,
  FrontierItem,
  Intent,
  Run,
  RunPulse,
} from '@/lib/types'

/**
 * 状态 = 战场状态：指挥官此刻在做什么，每个执行者跑到哪一步、为什么停。
 *
 * The page is one reading of two ledgers, not a merged one:
 *
 *  - 执行者名单按 **意图** 成组，一次意图一行。An intent can be attempted
 *    several times (failed → retry / steer / handoff); every new attempt used to
 *    appear as another chip because the old page listed raw runs. The roster
 *    therefore keys on intent id, and the attempts live inside the row's detail
 *    as a chain — a retried direction keeps exactly one tab.
 *  - Only `purpose == ""` runs are executor attempts. Commander / verifier /
 *    report runs are support runs: they have no intent (a commander run carries
 *    an empty intent_id), so they must never be listed as executors and their
 *    count must not be repeated as "executor runs". The situation strip counts
 *    both scopes with explicit labels.
 *  - An intent run whose intent is missing from the ledger is surfaced as an
 *    orphan row instead of being dropped: a page must not hide an inconsistent
 *    ledger, it must show it.
 *
 * All numbers come from the server (board / intents / runs); nothing is
 * recomputed client-side except grouping and the live elapsed clock.
 *
 * Visual hierarchy (视觉规范 v2): the page is a read-only command board. The
 * 战况 strip is the widest, quietest summary; the roster rows are compact
 * 12.5px content rows whose selection is a 2px left marker + soft primary wash
 * (never a full red row); the detail pane gets the larger title and real
 * section rhythm, so "the list" and "the selected direction" never weigh the
 * same. Failure/warning colours only ever appear as small tints and dots.
 */
export function ProjectState({
  onNavigate,
  onOpenIntent,
  onOpenRun,
}: {
  /** Switch the console view (?view=…). */
  onNavigate?: (view: string) => void
  /** Open one direction in the graph. */
  onOpenIntent?: (id: string) => void
  /** Open one run in the run ledger. */
  onOpenRun?: (id: string) => void
}) {
  const board = usePoll(api.board, 4000)
  const intents = usePoll(api.intents, 4000)
  const runs = usePoll(api.runs, 4000)
  // Deep link from the run ledger: ?view=state&intent=<id>. The URL is the
  // route, so the selection starts from it instead of always landing on the
  // first row of the roster.
  const location = useConsoleLocation()
  const picked = new URLSearchParams(location.search).get('intent') ?? ''
  const [pickedRun, setPickedRun] = useState('')
  useEffect(() => { setPickedRun('') }, [picked])

  const allRuns = runs.data ?? []
  const unitList = useMemo(
    () => buildUnits(intents.data ?? [], allRuns, board.data),
    [intents.data, allRuns, board.data],
  )
  const grouped = useMemo(() => groupUnits(unitList), [unitList])
  const support = useMemo(() => supportCounts(allRuns), [allRuns])
  const orphans = useMemo(() => orphanRuns(intents.data ?? [], allRuns), [intents.data, allRuns])
  // Battlefield order: what is moving or blocked first, history last. The first
  // unit is the default selection, so opening the page lands where the operator
  // would look for trouble.
  const ordered = useMemo(
    () => GROUP_ORDER.flatMap((key) => sortGroup(key, grouped[key])),
    [grouped],
  )
  const selected = ordered.find((u) => u.intent.id === picked) ?? ordered[0]
  // An attempt is picked explicitly; otherwise the latest attempt of the
  // selected intent follows the ledger (a retry appears without a click).
  const attempt =
    selected?.attempts.find((r) => r.id === pickedRun) ?? selected?.attempts[selected.attempts.length - 1]

  /** Selection is deep-linkable: the run page links here with ?intent=<id>. */
  function pickIntent(id: string) {
    setPickedRun('')
    const params = new URLSearchParams(window.location.search)
    params.set('view', 'state')
    params.set('intent', id)
    navigate(`${window.location.pathname}?${params.toString()}`)
  }

  const loading = board.loading || intents.loading || runs.loading
  const refreshing = board.refreshing || intents.refreshing || runs.refreshing
  const retry = () => { board.reload(); intents.reload(); runs.reload() }
  const error = board.error ?? intents.error ?? runs.error

  return (
    <Page
      title="状态"
      subtitle="战场状态：指挥官与执行者各自做到哪一步，谁在跑、谁停了、为什么停。只读、实时。"
      meta={<LiveBadge board={board.data} unavailable={!!error || loading} />}
      scroll={false}
    >
      <div className="flex h-full min-h-0 flex-col gap-3 px-5 py-5 sm:px-6 lg:px-8">
        {error && (
          <Callout tone="destructive" className="shrink-0" icon={<CircleAlert className="h-4 w-4" />}>
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-[12px] leading-relaxed">
                更新失败，当前数据可能已过期。
                {board.updatedAt && `上次状态更新：${fmtTime(new Date(board.updatedAt).toISOString())}`}
              </span>
              <Button size="xs" variant="outline" disabled={loading || refreshing} onClick={retry}>
                {refreshing ? (
                  <>
                    <Spinner size="sm" className="mr-1.5" />
                    重试中…
                  </>
                ) : (
                  '重试更新'
                )}
              </Button>
            </div>
            <details className="mt-1.5 text-[11px] text-ink-3">
              <summary className="cursor-pointer">错误详情</summary>
              <div className="tw-break mt-1">{error}</div>
            </details>
          </Callout>
        )}
        {loading ? (
          <div className="min-h-0 flex-1 space-y-3" aria-busy="true">
            <SkeletonCard />
            <div className="grid min-h-0 grid-cols-1 gap-3 xl:grid-cols-[minmax(300px,340px)_minmax(0,1fr)]">
              <SkeletonCard />
              <SkeletonCard />
            </div>
          </div>
        ) : (
          <>
            <SituationBar board={board.data} units={unitList} support={support} />
            {unitList.length === 0 && orphans.length === 0 ? (
              // One empty message, not two: with no directions there is no roster
              // and no case, and the split panes would just repeat themselves.
              <div className="tw-panel min-h-0 flex-1 rounded-xl">
                <EmptyState
                  icon={Gauge}
                  title={error ? '暂时无法读取执行者状态' : '战场还没有执行者'}
                  description={
                    error
                      ? '请重试读取，恢复连接后会自动更新。'
                      : `这个项目还没有意图。支持运行不占执行者位置：全部运行 ${fmtInt(board.data?.runs.total ?? 0)} 条（指挥 ${fmtInt(support.commander)} · 复核 ${fmtInt(support.verifier)} · 报告 ${fmtInt(support.report)}）。派发第一条方向后，这里按意图显示尝试链与 case。`
                  }
                  action={
                    error ? (
                      <Button size="sm" variant="outline" onClick={retry} disabled={refreshing}>
                        重试读取
                      </Button>
                    ) : (
                      <Button size="sm" onClick={() => navigate(`${window.location.pathname}?view=overview#new`)}>
                        新建意图
                      </Button>
                    )
                  }
                />
              </div>
            ) : (
              <div className="grid min-h-0 flex-1 grid-cols-1 gap-3 overflow-y-auto xl:grid-cols-[minmax(300px,340px)_minmax(0,1fr)] xl:overflow-hidden">
                <Roster
                  grouped={grouped}
                  counts={{ total: unitList.length, running: board.data?.intents.running ?? 0 }}
                  selectedID={selected?.intent.id ?? ''}
                  orphans={orphans}
                  onSelect={pickIntent}
                  onNavigate={onNavigate}
                  className="min-h-[280px] xl:min-h-0"
                />
                <UnitDetail
                  unit={selected}
                  run={attempt}
                  onPickRun={setPickedRun}
                  onOpenIntent={onOpenIntent}
                  onOpenRun={onOpenRun}
                  onRunsChanged={retry}
                  className="min-h-[420px] xl:min-h-0"
                />
              </div>
            )}
          </>
        )}
      </div>
    </Page>
  )
}

/** A quiet live marker: 11px ink-3, one dot, no chrome competing with the page. */
function LiveBadge({ board, unavailable }: { board?: BoardResponse; unavailable: boolean }) {
  if (unavailable) {
    return (
      <span role="status" className="inline-flex items-center gap-1.5 text-[11px] text-warning">
        <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-warning" />
        状态待更新
      </span>
    )
  }
  const phase = phaseOf(board?.decider)
  const live = isPhaseActive(phase)
  const label = live ? PHASE_LABEL[phase] ?? phase : phase === 'paused' ? '已暂停' : phase === 'blocked' ? '未就绪' : '空闲'
  return (
    <span
      role="status"
      className="inline-flex items-center gap-1.5 rounded-full border border-hairline bg-surface-2/60 px-2 py-0.5 text-[11px] text-ink-3"
    >
      <span className="relative flex h-1.5 w-1.5">
        {live && <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-info opacity-60" />}
        <span
          className={cn(
            'relative inline-flex h-1.5 w-1.5 rounded-full',
            live ? 'bg-info' : phase === 'paused' ? 'bg-warning' : phase === 'blocked' ? 'bg-caution' : 'bg-ink-3/50',
          )}
        />
      </span>
      {live ? '实时 · ' : ''}
      {label}
    </span>
  )
}

// ---------------------------------------------------------------------------
// 战况条: the commander's live phase next to the counts that tell whether the
// battle is moving. Run counts are split by scope on purpose — "全部运行" alone
// is what made the old page look like it had 27 executors.
// ---------------------------------------------------------------------------

function SituationBar({
  board,
  units,
  support,
}: {
  board?: BoardResponse
  units: Unit[]
  support: SupportCounts
}) {
  const counts = board?.intents
  const pending = Math.max(0, (counts?.pending ?? 0) - (counts?.held ?? 0))
  const intentRuns = units.reduce((n, u) => n + u.attempts.length, 0)
  const parked = (counts?.parked ?? 0) + (counts?.held ?? 0)

  return (
    <section data-testid="state-situation" className="tw-panel shrink-0 rounded-xl">
      <header className="flex flex-wrap items-center gap-2 border-b border-hairline px-4 py-3">
        <div className="tw-section-title">战况</div>
        <div className="ml-auto text-[11px] text-ink-3">只统计执行者意图；支持运行单列</div>
      </header>
      <div className="grid gap-5 p-4 xl:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)_minmax(230px,0.85fr)] xl:gap-0">
        <CommanderLive board={board} />

        <div className="min-w-0 border-t border-hairline pt-4 xl:border-l xl:border-t-0 xl:pl-5 xl:pt-0">
          <div className="tw-eyebrow flex items-center gap-1.5">
            <Terminal className="h-3.5 w-3.5" /> 执行者 · 按意图
          </div>
          <div className="mt-3 grid grid-cols-2 gap-x-4 gap-y-3.5 sm:grid-cols-3">
            <StatTile label="意图" value={fmtInt(units.length)} hint={`完成 ${fmtInt(counts?.done ?? 0)}`} />
            <StatTile
              label="在跑"
              value={fmtInt(counts?.running ?? 0)}
              tone={(counts?.running ?? 0) > 0 ? 'primary' : 'muted'}
              hint="占用执行槽位"
            />
            <StatTile
              label="排队"
              value={fmtInt(pending)}
              tone={pending > 0 ? 'warning' : 'muted'}
              hint="等指挥官派发"
            />
            <StatTile
              label="等待"
              value={fmtInt(counts?.waiting ?? 0)}
              tone={(counts?.waiting ?? 0) > 0 ? 'warning' : 'muted'}
              hint="等子任务 / 外部动作"
            />
            <StatTile
              label="失败"
              value={fmtInt(counts?.failed ?? 0)}
              tone={(counts?.failed ?? 0) > 0 ? 'destructive' : 'muted'}
              hint="需要重派或止损"
            />
            <StatTile
              label="停放/叫停"
              value={fmtInt(parked)}
              tone={parked > 0 ? 'caution' : 'muted'}
              hint="不再自动派发"
            />
          </div>
          <p className="mt-3 text-[11px] leading-relaxed text-ink-3">
            意图的运行与支持运行分开计数：指挥官/复核/报告不会计入执行者。
          </p>
        </div>

        <div
          data-testid="run-counts"
          className="min-w-0 border-t border-hairline pt-4 xl:border-l xl:border-t-0 xl:pl-5 xl:pt-0"
        >
          <div className="tw-eyebrow">运行账本</div>
          <div className="mt-3 space-y-3 text-[11px] text-ink-3">
            <div className="flex items-baseline gap-2">
              <span className="shrink-0">意图运行</span>
              <b data-testid="count-intent-runs" className="font-mono text-[20px] font-semibold leading-none tw-nums text-ink">
                {fmtInt(intentRuns)}
              </b>
              <span>
                次 · <span className="font-mono tw-nums text-ink-2">{units.length}</span> 个方向 · 重派{' '}
                <span className="font-mono tw-nums text-ink-2">{fmtInt(board?.redispatch?.total ?? 0)}</span>
              </span>
            </div>
            <div className="flex flex-wrap items-baseline gap-x-1.5 leading-relaxed">
              <span>全部运行</span>
              <b className="font-mono text-[13px] tw-nums text-ink-2">{fmtInt(board?.runs.total ?? 0)}</b>
              <span>
                （指挥{' '}
                <b data-testid="count-support-commander" className="font-mono tw-nums text-ink-2">
                  {fmtInt(support.commander)}
                </b>{' '}
                · 复核 <b className="font-mono tw-nums text-ink-2">{fmtInt(support.verifier)}</b> · 报告{' '}
                <b className="font-mono tw-nums text-ink-2">{fmtInt(support.report)}</b>
                {support.other > 0 ? ` · 其他 ${fmtInt(support.other)}` : ''}）
              </span>
            </div>
            <div className="flex flex-wrap items-baseline gap-x-1.5">
              <span>目标</span>
              <b className="font-mono tw-nums text-ink-2">
                {fmtInt(board?.goals.met ?? 0)}/{fmtInt(board?.goals.total ?? 0)}
              </b>
            </div>
          </div>
        </div>
      </div>
    </section>
  )
}

function CommanderLive({ board }: { board?: BoardResponse }) {
  const dec = board?.decider
  const phase = phaseOf(dec)
  const active = isPhaseActive(phase)
  const now = useTick(active)
  const since = dec?.activity?.since ? new Date(dec.activity.since).getTime() : 0
  const elapsed = since > 0 ? Math.max(0, Math.round((now - since) / 1000)) : 0
  const detail = phaseDetail(dec)
  const dot = active
    ? 'animate-pulse bg-info'
    : phase === 'paused'
      ? 'bg-warning'
      : phase === 'blocked'
        ? 'bg-caution'
        : 'bg-ink-3/50'

  return (
    <div className="min-w-0">
      <div className="tw-eyebrow flex items-center gap-1.5">
        <Bot className="h-3.5 w-3.5 text-ai" /> 指挥官
      </div>
      <div className="mt-3 flex items-center gap-2">
        <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', dot)} />
        <span className="min-w-0 flex-1 truncate text-[15px] font-semibold leading-6 tracking-tight text-ink">
          {PHASE_LABEL[phase] ?? phase}
        </span>
        {active && since > 0 && <span className="shrink-0 font-mono text-[11px] tw-nums text-ink-3">{elapsed}s</span>}
        <span className="ml-auto shrink-0 font-mono text-[11px] tw-nums text-ink-3">
          {dec?.rounds ? `第 ${fmtInt(dec.rounds)} 轮` : '待命'}
        </span>
      </div>

      <Meter
        value={active ? phaseProgress(elapsed) : 0}
        tone={phase === 'blocked' || phase === 'paused' ? 'warning' : 'info'}
        className="mt-2 h-1"
        aria-label="指挥官本轮进度（不会到 100：轮次结束由下一状态重置）"
      />

      <div className="mt-2 flex items-center gap-2 text-[12px] text-ink-2">
        {dec?.activity?.wake && (
          <span className="shrink-0 rounded border border-hairline bg-surface-2 px-1.5 py-0.5 font-mono text-[11px]">
            {dec.activity.wake}
          </span>
        )}
        <span className="min-w-0 flex-1 truncate" title={detail}>
          {detail}
        </span>
      </div>

      <div className="mt-2 flex flex-wrap items-center gap-x-2.5 gap-y-1 text-[11px] text-ink-3">
        {dec?.paused && <span className="text-warning">项目已暂停（不再决策，也不会派发）</span>}
        {!dec?.enabled && <span className="text-caution">指挥官未启用</span>}
        {dec?.model && (
          <span className="max-w-[220px] truncate font-mono" title={dec.model}>
            {dec.model}
          </span>
        )}
        <span>
          决策累计 <span className="font-mono tw-nums">{fmtInt(dec?.tokens_total ?? 0)}</span> tok
        </span>
        {dec?.last_fallback && <span className="text-caution">降级 {dec.last_fallback}</span>}
      </div>

      {dec?.last_error && (
        <div className="mt-2 flex items-start gap-1.5 text-[12px] text-destructive">
          <CircleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span className="tw-break line-clamp-2">上一轮出错：{dec.last_error}</span>
        </div>
      )}

      {(board?.handoff_warnings?.length ?? 0) > 0 && (
        <div className="mt-1.5 flex items-start gap-1.5 text-[12px] text-warning">
          <CircleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0" />
          <span className="line-clamp-1" title={board!.handoff_warnings![0]?.warnings.join('\n')}>
            最近 {board!.handoff_warnings!.length} 个历史 run 有非致命告警（见「运行」页）
          </span>
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 执行者名单: one row per intent, grouped by what the battlefield needs first.
// ---------------------------------------------------------------------------

function Roster({
  grouped,
  counts,
  selectedID,
  orphans,
  onSelect,
  onNavigate,
  className,
}: {
  grouped: Record<GroupKey, Unit[]>
  counts: { total: number; running: number }
  selectedID: string
  orphans: Run[]
  onSelect: (id: string) => void
  onNavigate?: (view: string) => void
  className?: string
}) {
  return (
    <section
      data-testid="exec-roster"
      className={cn('tw-panel flex min-h-0 flex-col rounded-xl', className)}
    >
      <header className="flex h-11 shrink-0 items-center gap-2 border-b border-hairline px-4">
        <Terminal className="h-4 w-4 shrink-0 text-ink-3" />
        <span className="tw-section-title">执行者 · 意图</span>
        <span className="ml-auto font-mono text-[11px] tw-nums text-ink-3">
          {counts.total} 个方向 · {counts.running} 在跑
        </span>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {counts.total === 0 && orphans.length === 0 && (
          <EmptyState
            compact
            icon={Gauge}
            title="还没有执行者"
            description="项目里还没有意图。指挥官/复核/报告的运行是支持运行，不会出现在这里；等指挥官派发第一条方向后，这里按意图显示尝试链。"
          />
        )}
        {GROUP_ORDER.map((key) => {
          const list = sortGroup(key, grouped[key])
          if (list.length === 0) return null
          return (
            <div key={key}>
              <div className="sticky top-0 z-10 flex items-center gap-2 border-b border-hairline/70 bg-card/95 px-3 py-1.5 backdrop-blur">
                <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', GROUP_DOT[key])} />
                <span className="tw-eyebrow">{GROUP_LABEL[key]}</span>
                <span className="ml-auto font-mono text-[11px] tw-nums text-ink-3">{list.length}</span>
              </div>
              <div className="divide-y divide-hairline/60">
                {list.map((u) => (
                  <UnitRow
                    key={u.intent.id}
                    unit={u}
                    active={selectedID === u.intent.id}
                    onClick={() => onSelect(u.intent.id)}
                  />
                ))}
              </div>
            </div>
          )
        })}

        {orphans.length > 0 && (
          <div>
            <div className="flex items-center gap-2 border-b border-hairline/70 bg-destructive/[0.06] px-3 py-1.5">
              <CircleAlert className="h-3.5 w-3.5 shrink-0 text-destructive" />
              <span className="tw-eyebrow text-destructive">无主运行（意图不在账本里）</span>
              <span className="ml-auto font-mono text-[11px] tw-nums text-ink-3">{orphans.length}</span>
            </div>
            {orphans.map((r) => (
              <div key={r.id} className="tw-hover-row border-b border-hairline/60 px-3 py-2">
                <div className="flex items-center gap-2">
                  <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-destructive" />
                  <span className="font-mono text-[11px] tw-nums text-ink-3" title={r.id}>
                    {shortID(r.id)}
                  </span>
                  <span className="ml-auto rounded bg-destructive/10 px-1.5 py-0.5 text-[11px] leading-4 text-destructive">
                    {runStatusLabel(r)}
                  </span>
                </div>
                <div className="tw-break mt-1 truncate font-mono text-[11px] text-ink-3">
                  intent_id={r.intent_id || '（空）'} · {fmtTime(r.started_at)}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      <footer className="flex shrink-0 items-center gap-2 border-t border-hairline px-4 py-2 text-[11px] text-ink-3">
        <span>支持运行不计入执行者</span>
        <button
          type="button"
          className="ml-auto rounded px-1 font-medium text-primary transition-colors hover:text-primary/80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          onClick={() => onNavigate?.('runs')}
        >
          运行页 →
        </button>
      </footer>
    </section>
  )
}

function UnitRow({ unit, active, onClick }: { unit: Unit; active: boolean; onClick: () => void }) {
  const { intent, attempts, latest, live, pulse, frontier } = unit
  // Attempts that actually ran: intent.attempts is the claim count and can be
  // decremented when a run is canceled, so it must not drive this label.
  const attemptCount = attempts.length
  const stopped = latest && (latest.status === 'failed' || latest.status === 'canceled')
  const quiet = pulse ? pulse.idle_seconds : 0
  const lastPulse = pulse ? pulseLine(pulse) : ''

  return (
    <ListRow
      data-testid={`intent-tab-${intent.id}`}
      data-intent-id={intent.id}
      data-intent-status={intent.status}
      active={active}
      onClick={onClick}
      title={`${intent.title} · ${intent.id}`}
      leading={<span className={cn('mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full', stateDot(intent))} />}
      className="items-start rounded-none px-3 py-2.5 [&>span]:block"
    >
      <div className="flex items-center gap-2">
        <span className={cn('min-w-0 flex-1 truncate text-[12.5px] leading-5', active ? 'font-medium text-ink' : 'text-ink')}>
          {intent.title}
        </span>
        {attemptCount > 1 && (
          <Badge size="sm" variant="muted" className="shrink-0" title={`已派发 ${attemptCount} 次（首派 + 重派）`}>
            ×{attemptCount}
          </Badge>
        )}
        <span className={cn('shrink-0 rounded px-1.5 py-0.5 text-[11px] leading-4', stateTint(intent))}>
          {intentStateLabel(intent)}
        </span>
      </div>
      <div className="mt-1 flex items-center gap-2 text-[11px] text-ink-3">
        <span className="shrink-0 font-mono tw-nums">{shortID(intent.id)}</span>
        {live && pulse ? (
          <span className={cn('min-w-0 flex-1 truncate font-mono tw-nums', quiet >= 180 && 'text-warning')}>
            静默 {fmtIdle(quiet)} · {fmtInt(pulse.events)} ev{lastPulse ? ` · ${lastPulse}` : ''}
          </span>
        ) : stopped && latest ? (
          <span className="min-w-0 flex-1 truncate font-mono tw-nums" title={lastStopText(latest)}>
            {lastStopText(latest)} · {fmtTime(latest.started_at)}
          </span>
        ) : hasBrokenPremise(frontier) ? (
          <span
            className="min-w-0 flex-1 truncate text-caution"
            title={`前提事实不成立：${(frontier?.blocked_on ?? []).join('，')}（派发会被拒绝）`}
          >
            前提不成立（{frontier?.blocked_on?.length ?? 0}）
          </span>
        ) : intent.status === 'reviewing' ? (
          <span className="min-w-0 flex-1 truncate text-info">固定复核中；通过后才完成</span>
        ) : intent.status === 'review_failed' ? (
          <span className="min-w-0 flex-1 truncate text-destructive">复核未通过，可原地重试/续跑</span>
        ) : intent.status === 'failed' ? (
          <span className="min-w-0 flex-1 truncate">已失败</span>
        ) : intent.hold ? (
          <span className="min-w-0 flex-1 truncate">已叫停</span>
        ) : intent.status === 'waiting' && intent.wait ? (
          <span className="min-w-0 flex-1 truncate">
            {intent.wait.actions?.length
              ? `等待 ${intent.wait.actions.length} 个外部动作`
              : intent.wait.operator
                ? `等待人工：${intent.wait.operator.request}`
                : `等 ${intent.wait.children?.length ?? 0} 个子方向${intent.wait.mode ? ` · ${intent.wait.mode === 'any' ? '任一' : '全部'}` : ''}`}
          </span>
        ) : latest ? (
          <span className="min-w-0 flex-1 truncate font-mono tw-nums">
            最近 {fmtTime(latest.started_at)} · {fmtDuration(latest.started_at, latest.ended_at)}
          </span>
        ) : (
          <span className="min-w-0 flex-1 truncate">尚未派发</span>
        )}
        {latest && latest.tokens ? (
          <span className="shrink-0 font-mono tw-nums">{fmtInt(latest.tokens)} tok</span>
        ) : null}
      </div>
    </ListRow>
  )
}

// ---------------------------------------------------------------------------
// 选中意图: the order, its attempts, and the case of the selected attempt.
// ---------------------------------------------------------------------------

function UnitDetail({
  unit,
  run,
  onPickRun,
  onOpenIntent,
  onOpenRun,
  onRunsChanged,
  className,
}: {
  unit?: Unit
  run?: Run
  onPickRun: (id: string) => void
  onOpenIntent?: (id: string) => void
  onOpenRun?: (id: string) => void
  onRunsChanged?: () => void
  className?: string
}) {
  if (!unit) {
    return (
      <section className={cn('tw-panel flex min-h-0 flex-col rounded-xl', className)}>
        <EmptyState
          icon={Gauge}
          title="没有执行者"
          description="项目里还没有意图。派发一条方向后，这里显示它的意图说明、尝试链和 case；指挥官与支持运行在顶部战况条里。"
        />
      </section>
    )
  }

  const { intent, attempts, pulse, frontier } = unit

  return (
    <section
      data-testid="exec-detail"
      data-intent-id={intent.id}
      className={cn('tw-panel flex min-h-0 flex-col rounded-xl', className)}
    >
      <div className="min-h-0 flex-1 overflow-y-auto">
        <header className="border-b border-hairline px-4 py-4 sm:px-5">
          <div className="flex flex-wrap items-start gap-3">
            <div className="min-w-0 flex-1">
              <div className="tw-eyebrow flex flex-wrap items-center gap-2">
                <span>选中的方向</span>
                <span className="font-mono tw-nums text-ink-3">{shortID(intent.id)}</span>
              </div>
              <h2
                data-testid="detail-title"
                className="mt-1.5 min-w-0 break-words text-[17px] font-semibold leading-6 tracking-tight text-ink"
              >
                {intent.title}
              </h2>
              <div className="mt-2 flex flex-wrap items-center gap-1.5">
                <Badge
                  size="sm"
                  variant={intentStatusVariant(intent)}
                  title={`状态：${intent.status}${intent.hold ? '（已叫停）' : ''}`}
                >
                  {intentStateLabel(intent)}
                </Badge>
                {intent.parent_intent_id && (
                  <Badge size="sm" variant="muted" title={`父方向 ${intent.parent_intent_id}`}>
                    子方向
                  </Badge>
                )}
                {intent.hold && (
                  <Badge size="sm" variant="warning">
                    叫停
                  </Badge>
                )}
                {intent.aborted && (
                  <Badge size="sm" variant="danger">
                    子树已取消
                  </Badge>
                )}
                {/* "前提不成立" is a claim about from_facts, not a synonym for
                    "cannot dispatch": board.ready is also false for failed/held
                    directions, which already have their own badges above. */}
                {hasBrokenPremise(frontier) && (
                  <Badge
                    size="sm"
                    variant="warning"
                    title={`前提事实不成立：${(frontier?.blocked_on ?? []).join('，')}（派发会被拒绝）`}
                  >
                    前提不成立
                  </Badge>
                )}
              </div>
              <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 font-mono text-[11px] tw-nums text-ink-3">
                <span>创建 {fmtTime(intent.created_at)}</span>
                <span>更新 {fmtTime(intent.updated_at)}</span>
                {intent.node && <span>节点 {intent.node}</span>}
                {intent.profile && <span>画像 {intent.profile}</span>}
                {intent.claimed_by && <span>认领 {shortID(intent.claimed_by)}</span>}
                {attempts.length > 0 && (
                  <span className="tw-nums">尝试 {attempts.length} 次</span>
                )}
                {intent.steer_count ? <span className="tw-nums">续话 {intent.steer_count}</span> : null}
                {intent.say_count ? <span className="tw-nums">插话 {intent.say_count}</span> : null}
              </div>
            </div>
            <div className="flex shrink-0 items-center gap-1">
              {onOpenIntent && (
                <Button size="xs" variant="ghost" onClick={() => onOpenIntent(intent.id)} title="在事实意图图里打开这条方向">
                  <Waypoints className="h-3.5 w-3.5" /> 图谱
                </Button>
              )}
              {run && onOpenRun && (
                <Button size="xs" variant="ghost" onClick={() => onOpenRun(run.id)} title="在运行账本里打开这一条 run">
                  <Activity className="h-3.5 w-3.5" /> 运行
                </Button>
              )}
            </div>
          </div>
        </header>

        <div className="space-y-4 p-4 sm:p-5">
          {intent.description && (
            <DetailSection label="意图说明">
              <div className="tw-inset tw-break max-h-28 overflow-y-auto whitespace-pre-wrap rounded-lg px-3 py-2.5 text-[12px] leading-relaxed text-ink-2">
                {intent.description}
              </div>
            </DetailSection>
          )}

          {intent.wait && (
            <Callout tone="info" icon={<CircleDashed className="h-4 w-4" />}>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[12px]">
                <span>
                  {intent.wait.actions?.length
                    ? `等待 ${intent.wait.actions.length} 个外部动作（${intent.wait.mode === 'any' ? '任一结果' : '全部结果'}）`
                    : intent.wait.operator
                      ? `人工操作${intent.wait.operator.resolution === 'unavailable' ? '无法完成' : ''}：${intent.wait.operator.request}`
                      : `等待 ${intent.wait.children?.length ?? 0} 个子方向（${intent.wait.mode === 'any' ? '任一即可' : '全部完成'}）`}
                </span>
                {(intent.wait.children ?? []).map((c) => (
                  <Badge key={c} size="sm" variant="muted" className="font-mono">
                    {shortID(c)}
                  </Badge>
                ))}
                {intent.wait.deadline && (
                  <span className="ml-auto font-mono text-[11px] tw-nums">截止 {fmtTime(intent.wait.deadline)}</span>
                )}
                {intent.wait.wake_pending && <span className="text-info">已满足，待唤醒</span>}
              </div>
            </Callout>
          )}

          {intent.park_reason && (
            <div className="rounded-lg border border-caution/25 bg-caution/[0.07] px-3 py-2 text-[12px] leading-relaxed text-ink-2">
              停放原因：{intent.park_reason}
            </div>
          )}

          <AttemptChain intent={intent} attempts={attempts} selectedRunID={run?.id ?? ''} onPickRun={onPickRun} />

          {intent.notes && intent.notes.length > 0 && (
            <DetailSection label="备注">
              <div className="space-y-1.5">
                {intent.notes.map((n, i) => (
                  <div key={i} className="flex items-start gap-2 rounded-lg border border-hairline px-2.5 py-1.5 text-[12px]">
                    <Badge size="sm" variant={n.kind === 'retry' ? 'warning' : 'info'}>
                      {n.kind === 'retry' ? '重派备注' : n.kind === 'steer' ? '续话备注' : n.kind}
                    </Badge>
                    <span className="tw-break min-w-0 flex-1 leading-relaxed text-ink-2">{n.text}</span>
                    <span className="shrink-0 font-mono text-[11px] text-ink-3">{fmtTime(n.at)}</span>
                  </div>
                ))}
              </div>
            </DetailSection>
          )}

          <IntentDoc doc={intent.doc} />

          {run ? (
            <RunDetail
              key={run.id}
              run={run}
              pulse={pulse}
              attemptIndex={attempts.findIndex((r) => r.id === run.id) + 1}
              onOpenRun={onOpenRun}
              onRunsChanged={onRunsChanged}
            />
          ) : (
            <EmptyState
              compact
              icon={Activity}
              title="这条方向还没有派发过"
              description="它还在排队，等指挥官或操作员发起第一次运行；派发后这里显示这一轮的运行记录与 case。"
              action={
                onOpenIntent ? (
                  <Button size="xs" variant="outline" onClick={() => onOpenIntent(intent.id)}>
                    去图谱
                  </Button>
                ) : undefined
              }
            />
          )}
        </div>
      </div>
    </section>
  )
}

function AttemptChain({
  intent,
  attempts,
  selectedRunID,
  onPickRun,
}: {
  intent: Intent
  attempts: Run[]
  selectedRunID: string
  onPickRun: (id: string) => void
}) {
  return (
    <DetailSection
      label="尝试"
      extra={
        attempts.length > 0 ? (
          <span className="font-mono tw-nums">
            {attempts.length} 次 · 最近 {fmtTime(attempts[attempts.length - 1].started_at)}
          </span>
        ) : undefined
      }
    >
      {attempts.length === 0 ? (
        <div className="rounded-lg border border-dashed border-hairline px-3 py-2.5 text-[12px] text-ink-3">
          {intent.status === 'pending' ? '尚未派发（排队中）' : '账本里还没有这条方向的 run'}
        </div>
      ) : (
        <div className="flex flex-wrap items-center gap-1.5">
          {attempts.map((r, i) => {
            const active = r.id === selectedRunID
            return (
              <Chip
                key={r.id}
                data-testid={`attempt-tab-${r.id}`}
                active={active}
                onClick={() => onPickRun(r.id)}
                title={`${r.id} · ${runStatusLabel(r)}${r.stop_reason ? ` · ${r.stop_reason}` : ''}${r.cancel_cause ? ` · ${cancelCauseText(r.cancel_cause)}` : ''}${r.exit_reason ? ` · ${r.exit_reason}` : ''}`}
                className="gap-1.5 rounded-md px-2 py-1 text-[11px] font-normal leading-4"
              >
                <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', runDot(r))} />
                <span className="font-mono tw-nums">#{i + 1}</span>
                <span>{runStatusLabel(r)}</span>
                <span className="font-mono tw-nums opacity-70">{fmtDuration(r.started_at, r.ended_at)}</span>
              </Chip>
            )
          })}
        </div>
      )}
    </DetailSection>
  )
}

function IntentDoc({ doc }: { doc?: string }) {
  const [open, setOpen] = useState(false)
  if (!doc || !doc.trim()) return null
  return (
    <div className="rounded-lg border border-hairline">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-2 rounded-lg px-3 py-2 text-left text-[12px] text-ink-2 transition-colors hover:bg-surface-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        <FileText className="h-3.5 w-3.5 shrink-0 text-ink-3" />
        约束与交接 · doc
        <span className="ml-auto font-mono text-[11px] text-ink-3">{open ? '收起' : '展开'}</span>
      </button>
      {open && (
        <pre className="tw-break max-h-64 overflow-auto whitespace-pre-wrap border-t border-hairline px-3 py-2.5 font-mono text-[11px] leading-relaxed text-ink-2">
          {doc}
        </pre>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// One attempt: its ledger row and the case directory it was handed. Case files
// are keyed by intent (the API reads the intent's case dir), events by run.
// ---------------------------------------------------------------------------

function RunDetail({
  run,
  pulse,
  attemptIndex,
  onOpenRun,
  onRunsChanged,
}: {
  run: Run
  pulse?: RunPulse
  attemptIndex: number
  onOpenRun?: (id: string) => void
  onRunsChanged?: () => void
}) {
  return (
    <div className="space-y-4">
      <RunMeta
        run={run}
        pulse={pulse}
        attemptIndex={attemptIndex}
        onOpenRun={onOpenRun}
        onRunsChanged={onRunsChanged}
      />
      <RunCase key={run.id} run={run} />
    </div>
  )
}

function RunMeta({
  run,
  pulse,
  attemptIndex,
  onOpenRun,
  onRunsChanged,
}: {
  run: Run
  pulse?: RunPulse
  attemptIndex: number
  onOpenRun?: (id: string) => void
  onRunsChanged?: () => void
}) {
  const live = run.status === 'running'
  const quiet = pulse?.idle_seconds ?? 0
  const ctxTokens = pulse?.context_tokens ?? 0
  const ctxWindow = pulse?.context_window ?? 0
  const pressure = ctxWindow > 0 ? ctxTokens / ctxWindow : 0
  const [dismissing, setDismissing] = useState(false)
  const [dismissError, setDismissError] = useState('')

  async function dismissWarnings() {
    setDismissing(true)
    setDismissError('')
    try {
      await api.dismissRunWarnings(run.id)
      onRunsChanged?.()
    } catch (e) {
      setDismissError(e instanceof Error ? e.message : String(e))
    } finally {
      setDismissing(false)
    }
  }

  return (
    <div data-testid="attempt-meta" className="tw-inset rounded-lg">
      <div className="flex flex-wrap items-center gap-2 border-b border-hairline px-3 py-2">
        <span className="text-[11px] font-medium text-ink-3">第 {attemptIndex || 1} 次</span>
        <Badge size="sm" variant={runStatusVariant(run)} title={`run 状态：${run.status}`}>
          {runStatusLabel(run)}
        </Badge>
        {run.stop_reason && <span className="font-mono text-[11px] text-ink-3">stop: {run.stop_reason}</span>}
        <span className="tw-break min-w-0 truncate font-mono text-[11px] text-ink-3">{run.id}</span>
        <button
          type="button"
          className="ml-auto rounded px-1 text-[11px] font-medium text-primary transition-colors hover:text-primary/80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          onClick={() => onOpenRun?.(run.id)}
        >
          运行详情 →
        </button>
      </div>

      <div className="space-y-2.5 px-3 py-2.5">
        {run.status === 'canceled' && (
          <div className="flex items-start gap-2 rounded-lg border border-warning/25 bg-warning/[0.07] px-2.5 py-1.5 text-[12px]">
            <span className="shrink-0 font-medium text-warning">取消原因</span>
            <span className="tw-break min-w-0 flex-1 leading-relaxed text-ink-2" title={cancelCauseText(run.cancel_cause)}>
              {runCancelReason(run)}
            </span>
          </div>
        )}

        {live && pulse && (
          <div className="rounded-lg bg-surface-2/60 px-2.5 py-2">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px]">
              <span
                className={cn(
                  'flex items-center gap-1.5 font-mono tw-nums',
                  quiet >= 180 ? 'text-warning' : 'text-ink-3',
                )}
              >
                <span className={cn('h-1.5 w-1.5 rounded-full', quiet >= 180 ? 'bg-warning' : 'animate-pulse bg-info')} />
                静默 {fmtIdle(quiet)}
              </span>
              <span className="font-mono tw-nums text-ink-3">{fmtInt(pulse.events)} 事件</span>
              {ctxWindow > 0 && (
                <span className="font-mono tw-nums text-ink-3">
                  上下文 {fmtInt(ctxTokens)}/{fmtInt(ctxWindow)}
                </span>
              )}
              {pulse.state && <span className="truncate font-mono text-ink-3">{pulse.state}</span>}
              {pulseLine(pulse) && (
                <span className="min-w-0 flex-1 truncate text-ink-2" title={pulseLine(pulse)}>
                  {pulseLine(pulse)}
                </span>
              )}
            </div>
            {ctxWindow > 0 && (
              <Meter
                value={ctxTokens}
                max={ctxWindow}
                tone={pressure > 0.9 ? 'destructive' : pressure > 0.75 ? 'warning' : 'info'}
                className="mt-1.5 h-1"
                aria-label="执行者上下文压力"
              />
            )}
          </div>
        )}

        <div className="grid grid-cols-2 gap-x-4 gap-y-2.5 sm:grid-cols-3">
          <Meta label="开始">{fmtTime(run.started_at)}</Meta>
          <Meta label="耗时">{fmtDuration(run.started_at, run.ended_at)}</Meta>
          <Meta label="tokens">{fmtInt(run.tokens ?? 0)}</Meta>
          <Meta label="事件">{fmtInt(run.events)}</Meta>
          <Meta label="事实">{fmtInt(run.declared_facts)}</Meta>
          {run.node && <Meta label="节点">{run.node}</Meta>}
          {run.model && <Meta label="模型" title={run.model}>{run.model}</Meta>}
          {run.profile_source && run.profile_source !== 'default' && (
            <Meta label="画像">{run.resolved_profile || run.profile || run.profile_source}</Meta>
          )}
          {run.generation && run.generation > 1 ? <Meta label="代">{`第 ${run.generation} 代`}</Meta> : null}
          {run.continued_from && <Meta label="续自" title={run.continued_from}>{shortID(run.continued_from)}</Meta>}
          {run.handoff_from && (
            <Meta label="交接自" title={`${run.handoff_from} v${run.handoff_version ?? '?'}`}>
              {shortID(run.handoff_from)}
            </Meta>
          )}
          {run.brief_path && <Meta label="brief" title={run.brief_path}>{run.brief_path}</Meta>}
        </div>

        {(run.warnings?.length ?? 0) > 0 &&
          (run.warnings_dismissed ? (
            <div className="text-[11px] text-ink-3">
              交接告警已忽略（{run.warnings!.length} 条，原始记录仍保留在账本）
            </div>
          ) : (
            <details>
              <summary className="cursor-pointer rounded text-[11px] text-warning focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
                交接告警 {run.warnings!.length} 条（非致命）
              </summary>
              <ul className="mt-1.5 list-disc space-y-1 pl-4 text-[11px] leading-relaxed text-ink-2">
                {run.warnings!.map((w, i) => (
                  <li key={i} className="tw-break">{w}</li>
                ))}
              </ul>
              {run.status === 'done' && (
                <div className="mt-1.5 flex flex-wrap items-center gap-2">
                  <Button size="xs" variant="ghost" onClick={dismissWarnings} disabled={dismissing}>
                    {dismissing ? '忽略中…' : '忽略这些告警'}
                  </Button>
                  {dismissError && <span className="text-[11px] text-destructive">{dismissError}</span>}
                </div>
              )}
            </details>
          ))}
        {(run.infos?.length ?? 0) > 0 && (
          <details>
            <summary className="cursor-pointer rounded text-[11px] text-ink-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
              交接提示 {run.infos!.length} 条（非问题）
            </summary>
            <ul className="mt-1.5 list-disc space-y-1 pl-4 text-[11px] leading-relaxed text-ink-3">
              {run.infos!.map((info, i) => (
                <li key={i} className="tw-break">{info}</li>
              ))}
            </ul>
          </details>
        )}
      </div>
    </div>
  )
}

interface GraphSnapshot {
  scope?: string
  facts?: unknown[]
  goals?: unknown[]
  intents?: { open?: unknown[]; running?: unknown[]; parked?: unknown[]; done?: unknown[] }
}

function RunCase({ run }: { run: Run }) {
  const live = run.status === 'running'
  const ctx = usePoll(() => api.runContext(run.id), live ? 4000 : 0)
  const events = usePoll(() => api.events(run.id, 160), live ? 2000 : 15000)
  const data = ctx.data
  const graph = data?.graph as GraphSnapshot | undefined

  const counts = useMemo(() => {
    const g = graph?.intents
    return {
      facts: graph?.facts?.length ?? 0,
      open: g?.open?.length ?? 0,
      running: g?.running?.length ?? 0,
      goals: graph?.goals?.length ?? 0,
    }
  }, [graph])

  const lastEvent = (events.data ?? [])[(events.data ?? []).length - 1]

  return (
    <div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
        <h3 className="text-[12px] font-semibold text-ink">本次 case</h3>
        <span className="text-[11px] text-ink-3">文件按意图目录读取，事件流按这一条 run 读取</span>
        {live && (
          <span className="flex items-center gap-1.5 text-[11px] text-success">
            <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-success" /> 实时
          </span>
        )}
        {live && lastEvent && (
          <span className="text-[11px] text-ink-3">
            最后事件 <span className="font-mono tw-nums">{fmtTime(lastEvent.at)}</span>
          </span>
        )}
      </div>

      <div className="mt-3 space-y-4">
        <EventsPanel events={events.data ?? []} loading={events.loading} error={events.error} live={live} />

        {ctx.error && (
          <Callout tone="destructive">
            {ctx.error.includes('意图不存在')
              ? `这条 run 的意图不在账本里（intent_id=${run.intent_id || '空'}）：这是账本不一致，不是普通执行者。`
              : ctx.error}
          </Callout>
        )}

        <div className="grid gap-4 md:grid-cols-2">
          <CaseBlock
            title="派发简报"
            extra={
              data?.prompt ? (
                <>
                  <span className="font-mono tw-nums">{fmtInt(data.prompt.length)}</span> 字
                </>
              ) : run.brief_path ? (
                '归档未提供正文'
              ) : (
                '未记录'
              )
            }
          >
            {!data ? (
              <CaseSkeleton />
            ) : data.prompt ? (
              <pre className="tw-break max-h-64 overflow-auto whitespace-pre-wrap rounded-lg bg-surface-2/50 px-3 py-2.5 font-mono text-[11px] leading-relaxed text-ink-2">
                {data.prompt}
              </pre>
            ) : (
              <div className="space-y-1.5 text-[11px] leading-relaxed text-ink-3">
                <div>case 目录里没有 prompt.txt。</div>
                {run.brief_path ? (
                  <div className="tw-break font-mono">简报已归档：{run.brief_path}（当前接口不返回这份归档正文）</div>
                ) : (
                  <div>这条 run 没有归档简报（续跑/唤醒沿用原会话，不重写简报）。</div>
                )}
              </div>
            )}
          </CaseBlock>

          <CaseBlock
            title="只读图 · graph.json"
            extra={
              graph ? (
                <span className="block max-w-[220px] truncate font-mono" title={graph.scope || ''}>
                  scope: {graph.scope || '—'}
                </span>
              ) : (
                '未记录'
              )
            }
          >
            {!data ? (
              <CaseSkeleton rows={2} />
            ) : graph ? (
              <div className="grid grid-cols-2 gap-x-4 gap-y-3">
                <StatTile label="事实" value={fmtInt(counts.facts)} />
                <StatTile label="开放方向" value={fmtInt(counts.open)} />
                <StatTile label="在跑" value={fmtInt(counts.running)} />
                <StatTile label="目标" value={fmtInt(counts.goals)} />
              </div>
            ) : (
              <div className="text-[11px] text-ink-3">没有 graph.json。</div>
            )}
          </CaseBlock>

          <CaseBlock
            title="声明的事实 · facts.jsonl"
            extra={
              <>
                意图累积 <span className="font-mono tw-nums">{data?.declared?.length ?? 0}</span> 条 · 这一轮{' '}
                <span className="font-mono tw-nums">{fmtInt(run.declared_facts)}</span>
              </>
            }
          >
            {(data?.declared?.length ?? 0) === 0 ? (
              <div className="text-[11px] text-ink-3">这个 case 还没有声明事实。</div>
            ) : (
              <div className="max-h-56 overflow-y-auto rounded-lg border border-hairline">
                {data!.declared!.map((d, i) => (
                  <div
                    key={i}
                    className="flex items-start gap-2 border-b border-hairline/70 px-3 py-2 last:border-0"
                  >
                    <Badge size="sm" variant={kindVariant(String(d.kind ?? ''))}>
                      {String(d.kind ?? 'fact')}
                    </Badge>
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-[12px] text-ink">{String(d.title ?? d.summary ?? '(无标题)')}</div>
                      {d.summary ? (
                        <div className="line-clamp-1 text-[11px] text-ink-3">{String(d.summary)}</div>
                      ) : null}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </CaseBlock>

          <CaseBlock
            title="case 文件"
            extra={
              <>
                <span className="font-mono tw-nums">{data?.files?.length ?? 0}</span> 个
              </>
            }
          >
            {(data?.files?.length ?? 0) === 0 ? (
              <div className="text-[11px] text-ink-3">目录为空。</div>
            ) : (
              <div className="max-h-56 overflow-y-auto rounded-lg border border-hairline">
                {data!.files!.map((f) => (
                  <div
                    key={f.name}
                    className="flex items-center gap-2 border-b border-hairline/70 px-3 py-1.5 last:border-0"
                  >
                    <FileText className="h-3.5 w-3.5 shrink-0 text-ink-3" />
                    <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-ink-2">{f.name}</span>
                    <span className="shrink-0 font-mono text-[11px] tw-nums text-ink-3">{fmtBytes(f.size)}</span>
                  </div>
                ))}
              </div>
            )}
          </CaseBlock>
        </div>
      </div>
    </div>
  )
}

function EventsPanel({
  events,
  loading,
  error,
  live,
}: {
  events: RunEvent[]
  loading: boolean
  error?: string
  live: boolean
}) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = ref.current
    if (el && live) el.scrollTop = el.scrollHeight
  }, [events.length, live])

  return (
    <section>
      <div className="flex flex-wrap items-baseline gap-2">
        <h3 className="text-[12px] font-semibold text-ink">事件流 · 这一条 run</h3>
        <span className="ml-auto flex items-center gap-1.5 text-[11px] text-ink-3">
          {live && <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-success" />}
          <span className="font-mono tw-nums">{fmtInt(events.length)}</span> 事件
        </span>
      </div>
      <div className="mt-2">
        {error ? (
          <Callout tone="destructive">{error}</Callout>
        ) : loading && events.length === 0 ? (
          <CaseSkeleton rows={3} />
        ) : events.length === 0 ? (
          <EmptyState
            compact
            icon={Activity}
            title="还没有事件"
            description="这一条 run 产出工具事件后会出现在这里；结束的 run 只保留历史事件。"
          />
        ) : (
          <div ref={ref} className="max-h-64 overflow-y-auto rounded-lg border border-hairline">
            {events.slice(-80).map((ev, i) => (
              <div
                key={`${ev.at}-${i}`}
                className="flex items-start gap-2 border-b border-hairline/60 px-3 py-1.5 last:border-0"
              >
                <span className="w-[76px] shrink-0 whitespace-nowrap font-mono text-[11px] tw-nums text-ink-3">
                  {fmtTime(ev.at)}
                </span>
                <Badge size="sm" variant={ev.kind === 'turn_ended' ? 'info' : ev.kind === 'error' ? 'danger' : 'muted'}>
                  {ev.kind}
                </Badge>
                <span className="min-w-0 flex-1 truncate text-[11px] text-ink-2" title={ev.text ?? ''}>
                  {oneLine(ev.text ?? '', 160)}
                  {ev.kind === 'turn_ended' && ev.stop_reason ? ` · ${ev.stop_reason}` : ''}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  )
}

/** A flat sub-block of the case: a 12px title and a hairline, not another card. */
function CaseBlock({
  title,
  extra,
  children,
}: {
  title: string
  extra?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className="min-w-0 border-t border-hairline pt-3">
      <div className="flex flex-wrap items-baseline gap-2">
        <h3 className="text-[12px] font-semibold text-ink">{title}</h3>
        {extra && <span className="ml-auto text-[11px] text-ink-3">{extra}</span>}
      </div>
      <div className="mt-2">{children}</div>
    </section>
  )
}

/** A quiet section inside the detail pane: 12px heading + hairline. */
function DetailSection({
  label,
  extra,
  children,
}: {
  label: string
  extra?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <section className="min-w-0 border-t border-hairline pt-3 first:border-t-0 first:pt-0">
      <div className="mb-2 flex items-center gap-2">
        <h3 className="text-[12px] font-semibold text-ink">{label}</h3>
        {extra && <span className="ml-auto text-[11px] text-ink-3">{extra}</span>}
      </div>
      {children}
    </section>
  )
}

/** A labelled mono value: 11px label, 12px tabular number/ID. */
function Meta({ label, title, children }: { label: string; title?: string; children: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="text-[11px] leading-4 text-ink-3">{label}</div>
      <div className="truncate font-mono text-[12px] leading-5 tw-nums text-ink-2" title={title}>
        {children}
      </div>
    </div>
  )
}

/** A quiet loading row shaped like the text it replaces. */
function CaseSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-2" aria-busy="true">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="h-3.5 rounded bg-surface-3/70" style={{ width: `${88 - i * 14}%` }} />
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Aggregation. One intent = one unit; attempts are its `purpose == ""` runs.
// ---------------------------------------------------------------------------

interface Unit {
  intent: Intent
  /** Attempts of this intent, oldest first (the attempt chain order). */
  attempts: Run[]
  live?: Run
  latest?: Run
  /** Live heartbeat from /api/board (running intents only). */
  pulse?: RunPulse
  /** The open direction's premise state, when the board included it. */
  frontier?: FrontierItem
}

interface SupportCounts {
  commander: number
  verifier: number
  report: number
  other: number
  total: number
}

/** The two axes of the roster: what a direction is doing, then its detail. */
type GroupKey = 'live' | 'failed' | 'waiting' | 'queued' | 'held' | 'parked' | 'done'

const GROUP_ORDER: GroupKey[] = ['live', 'failed', 'waiting', 'queued', 'held', 'parked', 'done']

const GROUP_LABEL: Record<GroupKey, string> = {
  live: '在跑',
  failed: '失败',
  waiting: '等待结果',
  queued: '排队',
  held: '叫停',
  parked: '停放',
  done: '完成',
}

/** Group dots are semantic tints, never saturated blocks. */
const GROUP_DOT: Record<GroupKey, string> = {
  live: 'animate-pulse bg-success',
  failed: 'bg-destructive',
  waiting: 'bg-warning',
  queued: 'bg-info',
  held: 'bg-caution',
  parked: 'bg-ink-3/60',
  done: 'bg-ink-3/40',
}

function buildUnits(intents: Intent[], runs: Run[], board?: BoardResponse): Unit[] {
  const pulseByIntent = new Map<string, RunPulse>()
  for (const item of board?.running_intents ?? []) if (item.pulse) pulseByIntent.set(item.id, item.pulse)
  const frontierByIntent = new Map<string, FrontierItem>()
  for (const item of board?.frontier ?? []) frontierByIntent.set(item.id, item)

  const known = new Set(intents.map((i) => i.id))
  const byIntent = new Map<string, Run[]>()
  for (const r of runs) {
    // purpose != "" is a support run (commander / verifier / report): it belongs
    // to no executor. Only purpose == "" is an attempt on an intent.
    if (r.purpose) continue
    if (!known.has(r.intent_id)) continue // shown as an orphan row instead
    const list = byIntent.get(r.intent_id) ?? []
    list.push(r)
    byIntent.set(r.intent_id, list)
  }

  return intents.map((intent) => {
    // Chronological, not string order: two rows can carry different UTC
    // offsets (old writes, imported ledgers), and "T12:59Z" sorts before
    // "T17:03+08:00" even though it is later.
    const attempts = (byIntent.get(intent.id) ?? [])
      .slice()
      .sort((a, b) => timeValue(a.started_at) - timeValue(b.started_at))
    const latest = attempts[attempts.length - 1]
    const live = attempts.find((r) => r.status === 'running')
    return { intent, attempts, latest, live, pulse: pulseByIntent.get(intent.id), frontier: frontierByIntent.get(intent.id) }
  })
}

function groupUnits(units: Unit[]): Record<GroupKey, Unit[]> {
  const out: Record<GroupKey, Unit[]> = {
    live: [],
    failed: [],
    waiting: [],
    queued: [],
    held: [],
    parked: [],
    done: [],
  }
  for (const u of units) out[groupOf(u.intent)].push(u)
  return out
}

function groupOf(intent: Intent): GroupKey {
  switch (intent.status) {
    case 'running':
    case 'claimed':
    case 'reviewing':
      return 'live'
    case 'waiting':
      return 'waiting'
    case 'failed':
    case 'review_failed':
      return 'failed'
    case 'parked':
      return 'parked'
    case 'done':
      return 'done'
    case 'pending':
      return intent.hold ? 'held' : 'queued'
    default:
      return 'queued'
  }
}

/** Live groups lead with what moved last; the queue leads with what waited longest. */
function sortGroup(key: GroupKey, list: Unit[]): Unit[] {
  if (key === 'queued' || key === 'held') {
    return list.slice().sort((a, b) => timeValue(a.intent.created_at) - timeValue(b.intent.created_at))
  }
  return list.slice().sort((a, b) => timeValue(b.intent.updated_at) - timeValue(a.intent.updated_at))
}

/** ISO timestamp → comparable milliseconds; an unparsable value sorts oldest. */
function timeValue(iso?: string): number {
  if (!iso) return 0
  const n = Date.parse(iso)
  return Number.isNaN(n) ? 0 : n
}

function supportCounts(runs: Run[]): SupportCounts {
  const out: SupportCounts = { commander: 0, verifier: 0, report: 0, other: 0, total: 0 }
  for (const r of runs) {
    if (!r.purpose) continue
    out.total++
    switch (r.purpose) {
      case 'commander':
        out.commander++
        break
      case 'verifier':
        out.verifier++
        break
      case 'report':
        out.report++
        break
      default:
        out.other++
    }
  }
  return out
}

/** An intent run whose intent is gone. The page must show it, not drop it. */
function orphanRuns(intents: Intent[], runs: Run[]): Run[] {
  const known = new Set(intents.map((i) => i.id))
  return runs.filter((r) => !r.purpose && !known.has(r.intent_id))
}

// ---------------------------------------------------------------------------
// Labels. A run's stop cause is displayed through runStatusLabel (lib/format),
// which owns the one cancel-cause table shared with the run ledger; the 状态 page
// adds the why ("failed 4m23s" vs "第 2 次完成") around it.
// ---------------------------------------------------------------------------

function lastStopText(run: Run): string {
  if (run.status === 'canceled') return runCancelReason(run)
  if (run.stop_reason) return run.stop_reason
  return statusLabel(run.status)
}

function intentStateLabel(intent: Intent): string {
  if (intent.status === 'claimed') return '派发中'
  if (intent.status === 'pending' && intent.hold) return '已叫停'
  if (intent.status === 'waiting') return '等子任务'
  if (intent.status === 'running') return '运行中'
  if (intent.status === 'reviewing') return '固定复核中'
  return statusLabel(intent.status)
}

/** The one-line state chip on a roster row: small area, semantic tint only. */
function stateTint(intent: Intent): string {
  if (intent.status === 'failed' || intent.status === 'review_failed') return 'bg-destructive/10 text-destructive'
  if (intent.status === 'reviewing') return 'bg-info/10 text-info'
  if (intent.status === 'waiting') return 'bg-warning/10 text-warning'
  if (intent.status === 'running' || intent.status === 'claimed') return 'bg-info/10 text-info'
  if (intent.hold) return 'bg-caution/10 text-caution'
  if (intent.status === 'done') return 'text-ink-3'
  if (intent.status === 'parked') return 'bg-surface-2 text-ink-3'
  return 'bg-surface-2 text-ink-3'
}

function stateDot(intent: Intent): string {
  if (intent.status === 'claimed') return 'animate-pulse bg-info'
  if (intent.status === 'running') return 'animate-pulse bg-success'
  if (intent.status === 'reviewing') return 'animate-pulse bg-info'
  switch (intent.status) {
    case 'done':
      return 'bg-ink-3/40'
    case 'failed':
    case 'review_failed':
      return 'bg-destructive'
    case 'parked':
      return 'bg-ink-3/60'
    case 'waiting':
      return 'bg-warning'
    default:
      return intent.hold ? 'bg-caution' : 'bg-info'
  }
}

function runDot(run: Run): string {
  switch (run.status) {
    case 'running':
      return 'animate-pulse bg-success'
    case 'failed':
      return 'bg-destructive'
    case 'canceled':
      return 'bg-warning'
    default:
      return 'bg-ink-3/40'
  }
}

function intentStatusVariant(intent: Intent): 'success' | 'danger' | 'warning' | 'info' | 'muted' {
  switch (intent.status) {
    case 'done':
      return 'success'
    case 'failed':
      return 'danger'
    case 'parked':
      return 'warning'
    case 'running':
    case 'claimed':
      return 'info'
    default:
      return intent.hold ? 'warning' : 'muted'
  }
}

function runStatusVariant(run: Run): 'success' | 'danger' | 'warning' | 'info' | 'muted' {
  switch (run.status) {
    case 'done':
      return 'success'
    case 'failed':
      return 'danger'
    case 'canceled':
      return 'warning'
    case 'running':
      return 'info'
    default:
      return 'muted'
  }
}

function pulseLine(pulse: RunPulse): string {
  const text = oneLine(pulse.last_text ?? '', 40)
  if (text) return text
  if (pulse.last_tool) return pulse.last_tool
  return pulse.last_kind ?? ''
}

function fmtIdle(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  const m = Math.floor(seconds / 60)
  if (m < 60) return `${m}m`
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}m`
}

/** One-second clock while something is actually moving; frozen otherwise. */
function useTick(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    setNow(Date.now())
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [active])
  return now
}
