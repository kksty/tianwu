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
import { Badge, Button, Callout, EmptyState, Meter, SkeletonList } from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
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
      actions={<LiveBadge board={board.data} unavailable={!!error || loading} />}
      scroll={false}
    >
      <div className="flex h-full min-h-0 flex-col gap-3">
        {error && (
          <Callout tone="destructive" className="shrink-0">
            更新失败，当前数据可能已过期。{board.updatedAt && `上次状态更新：${fmtTime(new Date(board.updatedAt).toISOString())}`}
            <Button size="xs" variant="outline" disabled={loading || refreshing} onClick={retry}>重试更新</Button>
            <details><summary>错误详情</summary>{error}</details>
          </Callout>
        )}
        {loading ? (
          <div className="min-h-0 flex-1 p-4">
            <SkeletonList count={8} />
          </div>
        ) : (
          <>
            <SituationBar board={board.data} units={unitList} support={support} />
            {unitList.length === 0 && orphans.length === 0 ? (
              // One empty message, not two: with no directions there is no roster
              // and no case, and the split panes would just repeat themselves.
              <div className="min-h-0 flex-1 rounded-lg border border-border bg-card">
                <EmptyState
                  icon={Gauge}
                  title={error ? "暂时无法读取执行者状态" : "战场还没有执行者"}
                  description={error ? "请重试读取，恢复连接后会自动更新。" : `这个项目还没有意图。支持运行不占执行者位置：全部运行 ${fmtInt(board.data?.runs.total ?? 0)} 条（指挥 ${fmtInt(support.commander)} · 复核 ${fmtInt(support.verifier)} · 报告 ${fmtInt(support.report)}）。派发第一条方向后，这里按意图显示尝试链与 case。`}
                />
                {!error && <div className="flex justify-center pb-4"><Button size="sm" onClick={() => navigate(`${window.location.pathname}?view=overview#new`)}>新建意图</Button></div>}
              </div>
            ) : (
              <div className="grid min-h-0 flex-1 grid-cols-1 gap-3 overflow-y-auto xl:grid-cols-[320px_minmax(0,1fr)] xl:overflow-hidden">
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

function LiveBadge({ board, unavailable }: { board?: BoardResponse; unavailable: boolean }) {
  if (unavailable) return <span role="status" className="text-xs text-warning">状态待更新</span>
  const phase = phaseOf(board?.decider)
  const live = isPhaseActive(phase)
  return (
    <span className="flex items-center gap-1.5 rounded-full border border-border bg-card px-2 py-1 text-[11px] text-muted-foreground">
      <span className="relative flex h-1.5 w-1.5">
        {live && <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />}
        <span
          className={
            'relative inline-flex h-1.5 w-1.5 rounded-full ' +
            (live ? 'bg-emerald-500' : phase === 'paused' ? 'bg-warning' : 'bg-muted-foreground/50')
          }
        />
      </span>
      {live ? PHASE_LABEL[phase] ?? phase : phase === 'paused' ? '已暂停' : '空闲'}
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
    <section
      data-testid="state-situation"
      className="grid shrink-0 gap-3 rounded-lg border border-border bg-card px-3.5 py-2.5 xl:grid-cols-[minmax(300px,1.15fr)_minmax(0,1fr)_minmax(250px,0.9fr)]"
    >
      <CommanderLive board={board} />

      <div className="flex min-w-0 flex-col justify-center gap-1.5 border-border/70 xl:border-l xl:pl-3">
        <div className="flex items-center gap-1 text-[10px] uppercase tracking-wider text-muted-foreground">
          <Terminal className="h-3 w-3" /> 执行者 · 按意图
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <Count label="意图" value={units.length} />
          <Count label="在跑" value={counts?.running ?? 0} tone={(counts?.running ?? 0) > 0 ? 'primary' : 'muted'} />
          <Count label="排队" value={pending} tone={pending > 0 ? 'warning' : 'muted'} />
          <Count label="等待" value={counts?.waiting ?? 0} />
          <Count label="失败" value={counts?.failed ?? 0} tone={(counts?.failed ?? 0) > 0 ? 'danger' : 'muted'} />
          <Count label="停放/叫停" value={parked} tone={parked > 0 ? 'warning' : 'muted'} />
          <Count label="完成" value={counts?.done ?? 0} tone={(counts?.done ?? 0) > 0 ? 'success' : 'muted'} />
        </div>
        <div className="text-[10.5px] text-muted-foreground">
          意图的运行与支持运行分开计数：指挥官/复核/报告不会计入执行者。
        </div>
      </div>

      <div
        data-testid="run-counts"
        className="flex min-w-0 flex-col justify-center gap-1 border-border/70 text-[11px] xl:border-l xl:pl-3"
      >
        <div className="flex items-baseline gap-1.5">
          <span className="text-muted-foreground">意图运行</span>
          <b data-testid="count-intent-runs" className="font-mono text-[15px] text-foreground">
            {fmtInt(intentRuns)}
          </b>
          <span className="text-muted-foreground">
            次 · {units.length} 个方向 · 重派 {fmtInt(board?.redispatch?.total ?? 0)}
          </span>
        </div>
        <div className="flex flex-wrap items-baseline gap-x-1.5 text-muted-foreground">
          <span>全部运行</span>
          <b className="font-mono text-[13px] text-foreground">{fmtInt(board?.runs.total ?? 0)}</b>
          <span>
            （指挥 <b data-testid="count-support-commander" className="font-mono text-foreground">{fmtInt(support.commander)}</b>
            {' · '}复核 <b className="font-mono text-foreground">{fmtInt(support.verifier)}</b>
            {' · '}报告 <b className="font-mono text-foreground">{fmtInt(support.report)}</b>
            {support.other > 0 ? ` · 其他 ${fmtInt(support.other)}` : ''}）
          </span>
        </div>
        <div className="flex flex-wrap items-baseline gap-x-1.5 text-muted-foreground">
          <span>目标</span>
          <b className="font-mono text-foreground">
            {fmtInt(board?.goals.met ?? 0)}/{fmtInt(board?.goals.total ?? 0)}
          </b>
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
        : 'bg-muted-foreground'

  return (
    <div className="min-w-0">
      <div className="flex items-center gap-1.5">
        <Bot className="h-3.5 w-3.5 shrink-0 text-ai" />
        <span className="text-[10px] uppercase tracking-wider text-muted-foreground">指挥官</span>
        <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${dot}`} />
        <span className="truncate text-[13px] font-medium text-foreground">{PHASE_LABEL[phase] ?? phase}</span>
        {active && since > 0 && <span className="font-mono text-[10px] text-muted-foreground">{elapsed}s</span>}
        <span className="ml-auto shrink-0 font-mono text-[10.5px] text-muted-foreground">
          {dec?.rounds ? `第 ${fmtInt(dec.rounds)} 轮` : '待命'}
        </span>
      </div>

      <Meter
        value={active ? phaseProgress(elapsed) : 0}
        tone={phase === 'blocked' || phase === 'paused' ? 'warning' : 'info'}
        className="mt-1.5 h-1"
        aria-label="指挥官本轮进度（不会到 100：轮次结束由下一状态重置）"
      />

      <div className="mt-1 flex items-center gap-1.5 text-[11px] text-muted-foreground">
        {dec?.activity?.wake && (
          <span className="shrink-0 rounded bg-muted px-1 font-mono text-[10px]">{dec.activity.wake}</span>
        )}
        <span className="min-w-0 flex-1 truncate" title={detail}>
          {detail}
        </span>
      </div>

      <div className="mt-1 flex flex-wrap items-center gap-x-2.5 gap-y-0.5 text-[10px] text-muted-foreground">
        {dec?.paused && <span className="text-warning">项目已暂停（不再决策，也不会派发）</span>}
        {!dec?.enabled && <span className="text-caution">指挥官未启用</span>}
        {dec?.model && <span className="truncate font-mono" title={dec.model}>{dec.model}</span>}
        <span>决策累计 {fmtInt(dec?.tokens_total ?? 0)} tok</span>
        {dec?.last_fallback && <span className="text-caution">降级 {dec.last_fallback}</span>}
      </div>

      {dec?.last_error && (
        <div className="mt-1 flex items-start gap-1 text-[10.5px] text-destructive">
          <CircleAlert className="mt-0.5 h-3 w-3 shrink-0" />
          <span className="line-clamp-2 tw-break">上一轮出错：{dec.last_error}</span>
        </div>
      )}

      {(board?.handoff_warnings?.length ?? 0) > 0 && (
        <div className="mt-1 flex items-start gap-1 text-[10.5px] text-warning">
          <CircleAlert className="mt-0.5 h-3 w-3 shrink-0" />
          <span className="line-clamp-1" title={board!.handoff_warnings![0]?.warnings.join('\n')}>
            最近运行有 {board!.handoff_warnings!.length} 条交接告警（见「运行」页）
          </span>
        </div>
      )}
    </div>
  )
}

function Count({ label, value, tone = 'default' }: { label: string; value: number; tone?: 'default' | 'primary' | 'warning' | 'danger' | 'success' | 'muted' }) {
  const color =
    tone === 'primary'
      ? 'text-primary'
      : tone === 'warning'
        ? 'text-warning'
        : tone === 'danger'
          ? 'text-destructive'
          : tone === 'success'
            ? 'text-success'
            : tone === 'muted'
              ? 'text-muted-foreground'
              : 'text-foreground'
  return (
    <span className="inline-flex items-center gap-1 rounded border border-border bg-muted/30 px-1.5 py-0.5 text-[10.5px]">
      <span className="text-muted-foreground">{label}</span>
      <span className={`font-mono ${color}`}>{fmtInt(value)}</span>
    </span>
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
      className={'flex min-h-0 flex-col rounded-lg border border-border bg-card ' + (className ?? '')}
    >
      <header className="flex h-9 shrink-0 items-center gap-1.5 border-b border-border px-3">
        <Terminal className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-[10.5px] font-semibold uppercase tracking-wider text-muted-foreground">执行者 · 意图</span>
        <span className="ml-auto font-mono text-[10.5px] text-muted-foreground">
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
              <div className="sticky top-0 z-10 flex items-center gap-1.5 border-b border-border/60 bg-card/95 px-3 py-1 text-[10px] uppercase tracking-wider text-muted-foreground backdrop-blur">
                <span className={`h-1.5 w-1.5 rounded-full ${GROUP_DOT[key]}`} />
                {GROUP_LABEL[key]}
                <span className="ml-auto font-mono">{list.length}</span>
              </div>
              {list.map((u) => (
                <UnitRow
                  key={u.intent.id}
                  unit={u}
                  active={selectedID === u.intent.id}
                  onClick={() => onSelect(u.intent.id)}
                />
              ))}
            </div>
          )
        })}

        {orphans.length > 0 && (
          <div>
            <div className="flex items-center gap-1.5 border-b border-border/60 px-3 py-1 text-[10px] uppercase tracking-wider text-destructive">
              <CircleAlert className="h-2.5 w-2.5" />
              无主运行（意图不在账本里）
              <span className="ml-auto font-mono">{orphans.length}</span>
            </div>
            {orphans.map((r) => (
              <div key={r.id} className="border-b border-border/60 px-3 py-1.5">
                <div className="flex items-center gap-1.5">
                  <span className="h-1.5 w-1.5 rounded-full bg-destructive" />
                  <span className="font-mono text-[10px] text-muted-foreground" title={r.id}>
                    {shortID(r.id)}
                  </span>
                  <span className="ml-auto text-[10px] text-destructive">{runStatusLabel(r)}</span>
                </div>
                <div className="truncate font-mono text-[10px] text-muted-foreground">
                  intent_id={r.intent_id || '（空）'} · {fmtTime(r.started_at)}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      <footer className="flex shrink-0 items-center gap-2 border-t border-border px-3 py-1.5 text-[10px] text-muted-foreground">
        <span>支持运行不计入执行者</span>
        <button
          type="button"
          className="ml-auto text-primary hover:underline"
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
    <button
      type="button"
      data-testid={`intent-tab-${intent.id}`}
      data-intent-id={intent.id}
      data-intent-status={intent.status}
      aria-pressed={active}
      onClick={onClick}
      className={
        'block w-full border-b border-border/50 px-3 py-1.5 text-left transition-colors ' +
        (active ? 'bg-primary/10' : 'hover:bg-accent/50')
      }
      title={`${intent.title} · ${intent.id}`}
    >
      <div className="flex items-center gap-1.5">
        <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${stateDot(intent)}`} />
        <span className={'min-w-0 flex-1 truncate text-[12px] ' + (active ? 'text-primary' : 'text-foreground')}>
          {intent.title}
        </span>
        {attemptCount > 1 && (
          <Badge size="sm" variant="muted" className="shrink-0" title={`已派发 ${attemptCount} 次（首派 + 重派）`}>
            ×{attemptCount}
          </Badge>
        )}
        <span className="shrink-0 text-[10px] text-muted-foreground">{intentStateLabel(intent)}</span>
      </div>
      <div className="mt-0.5 flex items-center gap-1.5 text-[10px] text-muted-foreground">
        <span className="shrink-0 font-mono">{shortID(intent.id)}</span>
        {live && pulse ? (
          <span className={`min-w-0 flex-1 truncate font-mono ${quiet >= 180 ? 'text-warning' : ''}`}>
            静默 {fmtIdle(quiet)} · {fmtInt(pulse.events)} ev{lastPulse ? ` · ${lastPulse}` : ''}
          </span>
        ) : stopped && latest ? (
          <span className="min-w-0 flex-1 truncate" title={lastStopText(latest)}>
            {lastStopText(latest)} · {fmtTime(latest.started_at)}
          </span>
        ) : hasBrokenPremise(frontier) ? (
          <span
            className="min-w-0 flex-1 truncate text-warning"
            title={`前提事实不成立：${(frontier?.blocked_on ?? []).join('，')}（派发会被拒绝）`}
          >
            前提不成立（{frontier?.blocked_on?.length ?? 0}）
          </span>
        ) : intent.status === 'failed' ? (
          <span className="min-w-0 flex-1 truncate">已失败</span>
        ) : intent.hold ? (
          <span className="min-w-0 flex-1 truncate">已叫停</span>
        ) : intent.status === 'waiting' && intent.wait ? (
          <span className="min-w-0 flex-1 truncate">
            {intent.wait.actions?.length ? `等待 ${intent.wait.actions.length} 个外部动作` : intent.wait.operator ? `等待人工：${intent.wait.operator.request}` : `等 ${intent.wait.children?.length ?? 0} 个子方向${intent.wait.mode ? ` · ${intent.wait.mode === 'any' ? '任一' : '全部'}` : ''}`}
          </span>
        ) : latest ? (
          <span className="min-w-0 flex-1 truncate font-mono">
            最近 {fmtTime(latest.started_at)} · {fmtDuration(latest.started_at, latest.ended_at)}
          </span>
        ) : (
          <span className="min-w-0 flex-1 truncate">尚未派发</span>
        )}
        {latest && latest.tokens ? <span className="shrink-0 font-mono">{fmtInt(latest.tokens)} tok</span> : null}
      </div>
    </button>
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
  className,
}: {
  unit?: Unit
  run?: Run
  onPickRun: (id: string) => void
  onOpenIntent?: (id: string) => void
  onOpenRun?: (id: string) => void
  className?: string
}) {
  if (!unit) {
    return (
      <section className={'flex min-h-0 flex-col rounded-lg border border-border bg-card ' + (className ?? '')}>
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
      className={'flex min-h-0 flex-col rounded-lg border border-border bg-card ' + (className ?? '')}
    >
      <div className="min-h-0 flex-1 overflow-y-auto p-3">
        <div className="flex items-start gap-2">
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-center gap-1.5">
              <h2 data-testid="detail-title" className="min-w-0 truncate text-[15px] font-semibold text-foreground">
                {intent.title}
              </h2>
              <Badge size="sm" variant={intentStatusVariant(intent)} title={`状态：${intent.status}${intent.hold ? '（已叫停）' : ''}`}>
                {intentStateLabel(intent)}
              </Badge>
              <span className="font-mono text-[10.5px] text-muted-foreground">{shortID(intent.id)}</span>
              {intent.parent_intent_id && (
                <Badge size="sm" variant="muted" title={`父方向 ${intent.parent_intent_id}`}>
                  子方向
                </Badge>
              )}
              {intent.hold && <Badge size="sm" variant="warning">叫停</Badge>}
              {intent.aborted && <Badge size="sm" variant="danger">子树已取消</Badge>}
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
            <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-[10.5px] text-muted-foreground">
              <span>创建 {fmtTime(intent.created_at)}</span>
              <span>更新 {fmtTime(intent.updated_at)}</span>
              {intent.node && <span>节点 {intent.node}</span>}
              {intent.profile && <span>画像 {intent.profile}</span>}
              {intent.claimed_by && <span>认领 {shortID(intent.claimed_by)}</span>}
              {attempts.length > 0 && <span>尝试 {attempts.length} 次</span>}
              {intent.steer_count ? <span>续话 {intent.steer_count}</span> : null}
              {intent.say_count ? <span>插话 {intent.say_count}</span> : null}
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-1">
            {onOpenIntent && (
              <Button size="xs" variant="ghost" onClick={() => onOpenIntent(intent.id)} title="在事实意图图里打开这条方向">
                <Waypoints className="h-3 w-3" /> 图谱
              </Button>
            )}
            {run && onOpenRun && (
              <Button size="xs" variant="ghost" onClick={() => onOpenRun(run.id)} title="在运行账本里打开这一条 run">
                <Activity className="h-3 w-3" /> 运行
              </Button>
            )}
          </div>
        </div>

        {intent.description && (
          <div className="mt-2">
            <div className="mb-1 text-[10px] uppercase tracking-wider text-muted-foreground">意图说明</div>
            <div className="max-h-28 overflow-y-auto whitespace-pre-wrap rounded-md border border-border/70 bg-muted/20 px-2 py-1.5 text-[11.5px] leading-relaxed text-foreground/85 tw-break">
              {intent.description}
            </div>
          </div>
        )}

        {intent.wait && (
          <div className="mt-2 flex flex-wrap items-center gap-1.5 rounded-md border border-border/70 bg-muted/20 px-2 py-1.5 text-[11px] text-muted-foreground">
            <CircleDashed className="h-3 w-3 shrink-0" />
            <span>
              {intent.wait.actions?.length ? `等待 ${intent.wait.actions.length} 个外部动作（${intent.wait.mode === 'any' ? '任一结果' : '全部结果'}）` : intent.wait.operator ? `人工操作${intent.wait.operator.resolution === 'unavailable' ? '无法完成' : ''}：${intent.wait.operator.request}` : `等待 ${intent.wait.children?.length ?? 0} 个子方向（${intent.wait.mode === 'any' ? '任一即可' : '全部完成'}）`}
            </span>
            {(intent.wait.children ?? []).map((c) => (
              <Badge key={c} size="sm" variant="muted" className="font-mono">
                {shortID(c)}
              </Badge>
            ))}
            {intent.wait.deadline && <span className="ml-auto font-mono">截止 {fmtTime(intent.wait.deadline)}</span>}
            {intent.wait.wake_pending && <span className="text-info">已满足，待唤醒</span>}
          </div>
        )}

        {intent.park_reason && (
          <div className="mt-2 text-[11px] text-caution">停放原因：{intent.park_reason}</div>
        )}

        <AttemptChain intent={intent} attempts={attempts} selectedRunID={run?.id ?? ''} onPickRun={onPickRun} />

        {intent.notes && intent.notes.length > 0 && (
          <div className="mt-2 space-y-1">
            {intent.notes.map((n, i) => (
              <div key={i} className="flex items-start gap-1.5 rounded-md border border-border/60 px-2 py-1 text-[11px]">
                <Badge size="sm" variant={n.kind === 'retry' ? 'warning' : 'info'}>
                  {n.kind === 'retry' ? '重派备注' : n.kind === 'steer' ? '续话备注' : n.kind}
                </Badge>
                <span className="min-w-0 flex-1 text-foreground/85 tw-break">{n.text}</span>
                <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{fmtTime(n.at)}</span>
              </div>
            ))}
          </div>
        )}

        <IntentDoc doc={intent.doc} />

        {run ? (
          <RunDetail
            key={run.id}
            run={run}
            pulse={pulse}
            attemptIndex={attempts.findIndex((r) => r.id === run.id) + 1}
            onOpenRun={onOpenRun}
          />
        ) : (
          <div className="mt-2 rounded-lg border border-dashed border-border px-3 py-6 text-center text-[12px] text-muted-foreground">
            这条方向还没有派发过：它还在排队，等指挥官或操作员发起第一次运行。
          </div>
        )}
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
    <div className="mt-2 flex flex-wrap items-center gap-1 border-t border-border/60 pt-2">
      <span className="mr-1 text-[10px] uppercase tracking-wider text-muted-foreground">尝试</span>
      {attempts.length === 0 ? (
        <span className="text-[11px] text-muted-foreground">
          {intent.status === 'pending' ? '尚未派发（排队中）' : '账本里还没有这条方向的 run'}
        </span>
      ) : (
        attempts.map((r, i) => {
          const active = r.id === selectedRunID
          return (
            <button
              key={r.id}
              type="button"
              data-testid={`attempt-tab-${r.id}`}
              aria-pressed={active}
              onClick={() => onPickRun(r.id)}
              title={`${r.id} · ${runStatusLabel(r)}${r.stop_reason ? ` · ${r.stop_reason}` : ''}${r.cancel_cause ? ` · ${cancelCauseText(r.cancel_cause)}` : ''}${r.exit_reason ? ` · ${r.exit_reason}` : ''}`}
              className={
                'inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-[10.5px] transition-colors ' +
                (active
                  ? 'border-primary/40 bg-primary/10 text-primary'
                  : 'border-border bg-card text-muted-foreground hover:bg-muted/60')
              }
            >
              <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${runDot(r)}`} />
              <span className="font-mono">#{i + 1}</span>
              <span>{runStatusLabel(r)}</span>
              <span className="opacity-70">{fmtDuration(r.started_at, r.ended_at)}</span>
            </button>
          )
        })
      )}
      <span className="ml-auto text-[10px] text-muted-foreground">
        {attempts.length > 0
          ? `${attempts.length} 次 · 最近 ${fmtTime(attempts[attempts.length - 1].started_at)}`
          : ''}
      </span>
    </div>
  )
}

function IntentDoc({ doc }: { doc?: string }) {
  const [open, setOpen] = useState(false)
  if (!doc || !doc.trim()) return null
  return (
    <div className="mt-2 rounded-md border border-border/60">
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-center gap-1.5 px-2 py-1 text-left text-[10.5px] text-muted-foreground hover:bg-accent/40"
      >
        <FileText className="h-3 w-3" />
        约束与交接 · doc
        <span className="ml-auto font-mono">{open ? '收起' : '展开'}</span>
      </button>
      {open && (
        <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words border-t border-border/60 px-2 py-1.5 font-mono text-[10.5px] leading-relaxed text-foreground/80 tw-break">
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
}: {
  run: Run
  pulse?: RunPulse
  attemptIndex: number
  onOpenRun?: (id: string) => void
}) {
  return (
    <div className="mt-2 space-y-2">
      <RunMeta run={run} pulse={pulse} attemptIndex={attemptIndex} onOpenRun={onOpenRun} />
      <RunCase key={run.id} run={run} />
    </div>
  )
}

function RunMeta({
  run,
  pulse,
  attemptIndex,
  onOpenRun,
}: {
  run: Run
  pulse?: RunPulse
  attemptIndex: number
  onOpenRun?: (id: string) => void
}) {
  const live = run.status === 'running'
  const quiet = pulse?.idle_seconds ?? 0
  const ctxTokens = pulse?.context_tokens ?? 0
  const ctxWindow = pulse?.context_window ?? 0
  const pressure = ctxWindow > 0 ? ctxTokens / ctxWindow : 0

  return (
    <div data-testid="attempt-meta" className="rounded-md border border-border bg-muted/15 px-2.5 py-1.5">
      <div className="flex flex-wrap items-center gap-1.5">
        <span className="text-[10px] uppercase tracking-wider text-muted-foreground">
          第 {attemptIndex || 1} 次
        </span>
        <Badge size="sm" variant={runStatusVariant(run)} title={`run 状态：${run.status}`}>
          {runStatusLabel(run)}
        </Badge>
        {run.stop_reason && <span className="font-mono text-[10.5px] text-muted-foreground">stop: {run.stop_reason}</span>}
        <span className="font-mono text-[10.5px] text-muted-foreground">{run.id}</span>
        <button
          type="button"
          className="ml-auto text-[10px] text-primary hover:underline"
          onClick={() => onOpenRun?.(run.id)}
        >
          运行详情 →
        </button>
      </div>

      {run.status === 'canceled' && (
        <div className="mt-1 flex items-start gap-1.5 text-[10.5px]">
          <span className="shrink-0 rounded bg-warning/15 px-1 font-medium text-warning">取消原因</span>
          <span className="min-w-0 flex-1 text-foreground/85 tw-break" title={cancelCauseText(run.cancel_cause)}>
            {runCancelReason(run)}
          </span>
        </div>
      )}

      {live && pulse && (
        <div className="mt-1 rounded border border-border/70 bg-card/60 px-2 py-1">
          <div className="flex flex-wrap items-center gap-2 text-[10.5px]">
            <span
              className={
                'flex items-center gap-1 font-mono ' +
                (quiet >= 180 ? 'text-warning' : 'text-muted-foreground')
              }
            >
              <span className={`h-1.5 w-1.5 rounded-full ${quiet >= 180 ? 'bg-warning' : 'animate-pulse bg-emerald-500'}`} />
              静默 {fmtIdle(quiet)}
            </span>
            <span className="font-mono text-muted-foreground">{fmtInt(pulse.events)} 事件</span>
            {ctxWindow > 0 && (
              <span className="font-mono text-muted-foreground">
                上下文 {fmtInt(ctxTokens)}/{fmtInt(ctxWindow)}
              </span>
            )}
            {pulse.state && <span className="truncate font-mono text-muted-foreground">{pulse.state}</span>}
            {pulseLine(pulse) && (
              <span className="min-w-0 flex-1 truncate text-foreground/80" title={pulseLine(pulse)}>
                {pulseLine(pulse)}
              </span>
            )}
          </div>
          {ctxWindow > 0 && (
            <Meter
              value={ctxTokens}
              max={ctxWindow}
              tone={pressure > 0.9 ? 'destructive' : pressure > 0.75 ? 'warning' : 'info'}
              className="mt-1 h-0.5"
              aria-label="执行者上下文压力"
            />
          )}
        </div>
      )}

      <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-[10.5px] text-muted-foreground">
        <span>开始 {fmtTime(run.started_at)}</span>
        <span>耗时 {fmtDuration(run.started_at, run.ended_at)}</span>
        <span>{fmtInt(run.tokens ?? 0)} tok</span>
        <span>{fmtInt(run.events)} 事件</span>
        <span>{fmtInt(run.declared_facts)} 事实</span>
        {run.node && <span>节点 {run.node}</span>}
        {run.model && <span className="truncate" title={run.model}>{run.model}</span>}
        {run.profile_source && run.profile_source !== 'default' && (
          <span>画像 {run.resolved_profile || run.profile || run.profile_source}</span>
        )}
        {run.generation && run.generation > 1 ? <span>第 {run.generation} 代</span> : null}
        {run.continued_from && <span title={run.continued_from}>续自 {shortID(run.continued_from)}</span>}
        {run.handoff_from && (
          <span title={`${run.handoff_from} v${run.handoff_version ?? '?'}`}>
            交接自 {shortID(run.handoff_from)}
          </span>
        )}
        {run.brief_path && <span className="truncate" title={run.brief_path}>brief {run.brief_path}</span>}
      </div>
      {(run.warnings?.length ?? 0) > 0 && (
        <details className="mt-1">
          <summary className="cursor-pointer text-[10.5px] text-warning">
            交接告警 {run.warnings!.length} 条（非致命）
          </summary>
          <ul className="mt-1 list-disc space-y-0.5 pl-4 text-[10.5px] text-foreground/80">
            {run.warnings!.map((w, i) => (
              <li key={i} className="tw-break">{w}</li>
            ))}
          </ul>
        </details>
      )}
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
      <div className="mb-1.5 flex flex-wrap items-center gap-2 text-[10px] text-muted-foreground">
        <span>case 文件按意图目录读取，事件流按这一条 run 读取。</span>
        {live && (
          <span className="flex items-center gap-1 text-emerald-500">
            <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-emerald-500" /> 实时
          </span>
        )}
        {live && lastEvent && <span>最后事件 {fmtTime(lastEvent.at)}</span>}
      </div>

      <div className="grid gap-2 md:grid-cols-2">
        <div className="md:col-span-2">
          <EventsPanel events={events.data ?? []} loading={events.loading} error={events.error} live={live} />
        </div>

        {ctx.error && (
          <div className="md:col-span-2">
            <Callout tone="destructive">
              {ctx.error.includes('意图不存在')
                ? `这条 run 的意图不在账本里（intent_id=${run.intent_id || '空'}）：这是账本不一致，不是普通执行者。`
                : ctx.error}
            </Callout>
          </div>
        )}

        <Panel
          title="派发简报"
          extra={<span className="text-[10.5px] text-muted-foreground">{data?.prompt ? `${fmtInt(data.prompt.length)} 字` : run.brief_path ? '归档未提供正文' : '未记录'}</span>}
        >
          {!data ? (
            <SkeletonList count={3} />
          ) : data.prompt ? (
            <pre className="max-h-64 overflow-auto whitespace-pre-wrap break-words font-mono text-[10.5px] leading-relaxed text-foreground/85 tw-break">
              {data.prompt}
            </pre>
          ) : (
            <div className="space-y-1 text-[11px] text-muted-foreground">
              <div>case 目录里没有 prompt.txt。</div>
              {run.brief_path ? (
                <div className="font-mono text-[10px] tw-break">简报已归档：{run.brief_path}（当前接口不返回这份归档正文）</div>
              ) : (
                <div>这条 run 没有归档简报（续跑/唤醒沿用原会话，不重写简报）。</div>
              )}
            </div>
          )}
        </Panel>

        <Panel
          title="只读图 · graph.json"
          extra={
            graph ? (
              <span className="max-w-[220px] truncate font-mono text-[10px] text-muted-foreground" title={graph.scope || ''}>
                scope: {graph.scope || '—'}
              </span>
            ) : (
              <span className="text-[10.5px] text-muted-foreground">未记录</span>
            )
          }
        >
          {!data ? (
            <SkeletonList count={2} />
          ) : graph ? (
            <div className="grid grid-cols-2 gap-1.5">
              <MiniStat label="事实" value={counts.facts} />
              <MiniStat label="开放方向" value={counts.open} />
              <MiniStat label="在跑" value={counts.running} />
              <MiniStat label="目标" value={counts.goals} />
            </div>
          ) : (
            <div className="text-[11px] text-muted-foreground">没有 graph.json。</div>
          )}
        </Panel>

        <Panel
          title="声明的事实 · facts.jsonl"
          extra={
            <span className="text-[10.5px] text-muted-foreground">
              意图累积 {data?.declared?.length ?? 0} 条 · 这一轮 {fmtInt(run.declared_facts)}
            </span>
          }
          bodyClassName="p-0"
        >
          {(data?.declared?.length ?? 0) === 0 ? (
            <div className="p-3 text-[11px] text-muted-foreground">这个 case 还没有声明事实。</div>
          ) : (
            <div className="max-h-56 overflow-y-auto">
              {data!.declared!.map((d, i) => (
                <div key={i} className="flex items-start gap-1.5 border-b border-border/60 px-3 py-1 last:border-0">
                  <Badge size="sm" variant={kindVariant(String(d.kind ?? ''))}>
                    {String(d.kind ?? 'fact')}
                  </Badge>
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-[11.5px] text-foreground">{String(d.title ?? d.summary ?? '(无标题)')}</div>
                    {d.summary ? (
                      <div className="line-clamp-1 text-[10.5px] text-muted-foreground">{String(d.summary)}</div>
                    ) : null}
                  </div>
                </div>
              ))}
            </div>
          )}
        </Panel>

        <Panel
          title="case 文件"
          extra={<span className="text-[10.5px] text-muted-foreground">{data?.files?.length ?? 0} 个</span>}
          bodyClassName="p-0"
        >
          {(data?.files?.length ?? 0) === 0 ? (
            <div className="p-3 text-[11px] text-muted-foreground">目录为空。</div>
          ) : (
            <div className="max-h-56 overflow-y-auto">
              {data!.files!.map((f) => (
                <div key={f.name} className="flex items-center gap-2 border-b border-border/60 px-3 py-0.5 last:border-0">
                  <FileText className="h-3 w-3 shrink-0 text-muted-foreground" />
                  <span className="min-w-0 flex-1 truncate font-mono text-[10.5px] text-foreground/85">{f.name}</span>
                  <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{fmtBytes(f.size)}</span>
                </div>
              ))}
            </div>
          )}
        </Panel>

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
    <Panel
      title="事件流 · 这一条 run"
      extra={
        <span className="flex items-center gap-1.5 text-[10.5px] text-muted-foreground">
          {live && <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-emerald-500" />}
          {events.length} 事件
        </span>
      }
    >
      {error ? (
        <Callout tone="destructive">{error}</Callout>
      ) : loading && events.length === 0 ? (
        <SkeletonList count={3} />
      ) : events.length === 0 ? (
        <div className="text-[11px] text-muted-foreground">还没有事件。</div>
      ) : (
        <div ref={ref} className="max-h-64 overflow-y-auto">
          {events.slice(-80).map((ev, i) => (
            <div key={`${ev.at}-${i}`} className="flex items-start gap-2 border-b border-border/50 py-0.5 last:border-0">
              <span className="w-[74px] shrink-0 whitespace-nowrap font-mono text-[10px] text-muted-foreground">{fmtTime(ev.at)}</span>
              <Badge size="sm" variant={ev.kind === 'turn_ended' ? 'info' : ev.kind === 'error' ? 'danger' : 'muted'}>
                {ev.kind}
              </Badge>
              <span className="min-w-0 flex-1 truncate text-[10.5px] text-foreground/80" title={ev.text ?? ''}>
                {oneLine(ev.text ?? '', 160)}
                {ev.kind === 'turn_ended' && ev.stop_reason ? ` · ${ev.stop_reason}` : ''}
              </span>
            </div>
          ))}
        </div>
      )}
    </Panel>
  )
}

function MiniStat({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-md border border-border px-2 py-1">
      <div className="text-[10px] text-muted-foreground">{label}</div>
      <div className="font-mono text-[15px] leading-tight text-foreground">{fmtInt(value)}</div>
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

const GROUP_DOT: Record<GroupKey, string> = {
  live: 'animate-pulse bg-emerald-500',
  failed: 'bg-rose-500',
  waiting: 'bg-amber-500',
  queued: 'bg-sky-500',
  held: 'bg-orange-500',
  parked: 'bg-muted-foreground/50',
  done: 'bg-muted-foreground/40',
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
      return 'live'
    case 'waiting':
      return 'waiting'
    case 'failed':
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
  return statusLabel(intent.status)
}

function stateDot(intent: Intent): string {
  if (intent.status === 'claimed') return 'animate-pulse bg-info'
  if (intent.status === 'running') return 'animate-pulse bg-emerald-500'
  switch (intent.status) {
    case 'done':
      return 'bg-muted-foreground/40'
    case 'failed':
      return 'bg-rose-500'
    case 'parked':
      return 'bg-muted-foreground/50'
    case 'waiting':
      return 'bg-amber-500'
    default:
      return intent.hold ? 'bg-orange-500' : 'bg-sky-500'
  }
}

function runDot(run: Run): string {
  switch (run.status) {
    case 'running':
      return 'animate-pulse bg-emerald-500'
    case 'failed':
      return 'bg-rose-500'
    case 'canceled':
      return 'bg-amber-500'
    default:
      return 'bg-muted-foreground/40'
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
