import { useEffect, useMemo, useState } from 'react'
import {
  Activity,
  ArrowUpRight,
  FolderPlus,
  RefreshCw,
  SearchX,
  Server,
  SlidersHorizontal,
  TriangleAlert,
} from 'lucide-react'
import {
  Button,
  Callout,
  Chip,
  EmptyState,
  Meter,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  Skeleton,
  SkeletonList,
  StatTile,
  StatusIndicator,
  type StatusKind,
} from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { FleetTimeline } from '@/components/FleetTimeline'
import { TokenHistory } from '@/components/TokenHistory'
import { api } from '@/lib/api'
import { isPhaseActive, PHASE_LABEL, phaseDetail, phaseOf, phaseProgress } from '@/lib/decider'
import { fmtDuration, fmtInt, fmtTime, runStatusLabel } from '@/lib/format'
import { usePoll } from '@/lib/hooks'
import type { FleetProject, FleetRun, FleetTask } from '@/lib/types'

/** One-second clock for live durations. */
function useNow(ms = 1000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), ms)
    return () => window.clearInterval(timer)
  }, [ms])
  return now
}

function openProject(id: string, opts: { view?: string; run?: string; intent?: string } = {}) {
  const params = new URLSearchParams()
  if (opts.view) params.set('view', opts.view)
  if (opts.run) params.set('run', opts.run)
  const qs = params.toString()
  const hash = opts.intent ? `#${encodeURIComponent(opts.intent)}` : ''
  window.location.assign(`/p/${encodeURIComponent(id)}/${qs ? `?${qs}` : ''}${hash}`)
}

/** Commander phase → the one status vocabulary every list shares. */
function phaseStatus(phase: string): StatusKind {
  if (isPhaseActive(phase)) return 'running'
  if (phase === 'paused') return 'warning'
  if (phase === 'blocked') return 'error'
  if (phase === 'loading') return 'pending'
  return 'idle'
}

/** Direction / run status → the same status vocabulary. */
function runStatus(status: string): StatusKind {
  switch (status) {
    case 'running':
    case 'claimed':
    case 'reviewing':
      return 'running'
    case 'pending':
    case 'waiting':
      return 'pending'
    case 'done':
      return 'success'
    case 'failed':
    case 'review_failed':
      return 'error'
    case 'canceled':
    case 'parked':
      return 'warning'
    default:
      return 'idle'
  }
}

const STATUS_TEXT: Record<string, string> = {
  running: '在跑',
  claimed: '认领',
  pending: '待办',
  done: '完成',
  failed: '失败',
  canceled: '取消',
}

/**
 * One project as a card, not a chip: the title is the entity, the phase is what
 * it is doing now, and the most recent run plus its token spend is the evidence
 * underneath. Reading order is name → phase → latest run → tokens.
 */
function ProjectCard({ project, now }: { project: FleetProject; now: number }) {
  const dec = project.decider
  const phase = phaseOf(dec)
  const active = isPhaseActive(phase)
  const since = dec.activity?.since ? new Date(dec.activity.since).getTime() : 0
  const elapsed = since > 0 ? Math.max(0, Math.round((now - since) / 1000)) : 0
  const bar = active ? phaseProgress(elapsed) : 0
  const detail = phaseDetail(dec)
  const recent = project.recent_runs?.[0]
  const tokens = project.tokens ?? 0
  const goalsOpen = Math.max(0, project.goals.total - project.goals.met)

  return (
    <button
      type="button"
      onClick={() => openProject(project.id)}
      className="group flex min-w-0 flex-col rounded-xl border border-hairline bg-card p-4 text-left shadow-soft transition-colors hover:border-ink-3/35 hover:bg-surface-2/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
      title={`${project.name}${project.scope ? ` · ${project.scope}` : ''}`}
    >
      <div className="flex min-w-0 items-start gap-2">
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-semibold leading-5 text-ink">{project.name}</div>
          <div className="tw-break mt-0.5 line-clamp-1 text-[11px] leading-4 text-ink-3">
            {project.scope || project.id}
          </div>
        </div>
        <StatusIndicator
          status={phaseStatus(phase)}
          label={PHASE_LABEL[phase] ?? phase}
          compact
          className="shrink-0"
        />
      </div>

      <div className="mt-2 flex min-w-0 items-center gap-2 text-[11px] leading-4 text-ink-3">
        <span className="tw-nums shrink-0 font-mono">
          {dec.rounds ? `第 ${fmtInt(dec.rounds)} 轮` : '待命'}
        </span>
        {active && elapsed > 0 && <span className="tw-nums shrink-0 font-mono">{elapsed}s</span>}
        {dec.activity?.wake && (
          <span className="ml-auto shrink-0 truncate rounded bg-surface-2 px-1.5 py-0.5 font-mono text-ink-3">
            {dec.activity.wake}
          </span>
        )}
      </div>

      <Meter
        value={bar}
        max={100}
        tone={phase === 'paused' || phase === 'blocked' ? 'warning' : 'info'}
        className="mt-2 h-1"
        aria-label="指挥官本轮进度"
      />

      <div className="mt-1.5 truncate text-[11px] leading-4 text-ink-3" title={detail}>
        {detail}
      </div>

      <div className="mt-auto pt-3">
        <div className="flex min-w-0 items-center gap-1.5 border-t border-hairline pt-2.5 text-[11px] leading-4 text-ink-3">
          <span className="shrink-0">最近 run</span>
          {recent ? (
            <>
              <StatusIndicator status={runStatus(recent.status)} label={runStatusLabel(recent)} compact className="shrink-0" />
              <span className="min-w-0 flex-1 truncate text-[12px] text-ink-2" title={recent.title}>
                {recent.title || '—'}
              </span>
              <span className="tw-nums shrink-0 font-mono text-ink-2">
                {recent.tokens ? fmtInt(recent.tokens) : '—'} tok
              </span>
            </>
          ) : (
            <span className="truncate">还没有运行</span>
          )}
        </div>
        <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] leading-4 text-ink-3">
          <span className={project.intents.running > 0 ? 'text-primary' : ''}>
            在跑 <span className="tw-nums font-mono">{fmtInt(project.intents.running)}</span>
          </span>
          <span>
            待派 <span className="tw-nums font-mono">{fmtInt(project.intents.pending)}</span>
          </span>
          {goalsOpen > 0 && (
            <span>
              未达目标 <span className="tw-nums font-mono">{fmtInt(goalsOpen)}</span>
            </span>
          )}
          <span className="ml-auto tw-nums font-mono">累计 {fmtInt(tokens)} tok</span>
        </div>
      </div>
    </button>
  )
}

type Row =
  | { key: string; kind: 'running'; project: FleetProject; task: FleetTask }
  | { key: string; kind: 'open'; project: FleetProject; task: FleetTask }
  | { key: string; kind: 'done'; project: FleetProject; run: FleetRun }

function rowStart(row: Row): number {
  if (row.kind === 'running') {
    return new Date(row.task.pulse?.started_at ?? row.task.created_at).getTime()
  }
  if (row.kind === 'open') return new Date(row.task.created_at).getTime()
  return new Date(row.run.started_at).getTime()
}

function openRow(row: Row) {
  if (row.kind === 'open') {
    // The graph selects by graph key: a bare id is not in model.byId, so the
    // page would open with nothing selected. `s:` is the step/intent key.
    openProject(row.project.id, { view: 'graph', intent: `s:${row.task.id}` })
    return
  }
  if (row.kind === 'running') {
    openProject(row.project.id, row.task.run_id ? { view: 'runs', run: row.task.run_id } : { view: 'runs' })
    return
  }
  openProject(row.project.id, { view: 'runs', run: row.run.id })
}

/** The row list: live directions first, then queued, then what just finished. */
function TasksTable({
  projects,
  nodeFilter,
  onlyRunning,
  hasFilter,
  onClearFilter,
}: {
  projects: FleetProject[]
  nodeFilter: string
  onlyRunning: boolean
  hasFilter: boolean
  onClearFilter: () => void
}) {
  const rows = useMemo(() => {
    const running: Row[] = []
    const open: Row[] = []
    const done: Row[] = []
    const nodeMatches = (node?: string) => !nodeFilter || node === nodeFilter

    for (const project of projects) {
      for (const task of project.running) {
        if (!nodeMatches(task.node)) continue
        running.push({ key: `r:${project.id}:${task.id}`, kind: 'running', project, task })
      }
      if (!onlyRunning) {
        for (const task of project.open) {
          // A queued direction has no node yet (the fleet projection only
          // assigns one once it runs), so a node filter must not hide it: the
          // 待派 count above still includes it.
          open.push({ key: `o:${project.id}:${task.id}`, kind: 'open', project, task })
        }
        let finished = 0
        for (const run of project.recent_runs) {
          if (run.status === 'running' || !nodeMatches(run.node)) continue
          done.push({ key: `d:${run.id}`, kind: 'done', project, run })
          if (++finished >= 4) break
        }
      }
    }
    running.sort((a, b) => rowStart(a) - rowStart(b))
    open.sort((a, b) => rowStart(a) - rowStart(b))
    done.sort((a, b) => rowStart(b) - rowStart(a))
    return [...running, ...open, ...done].slice(0, 80)
  }, [projects, nodeFilter, onlyRunning])

  if (rows.length === 0) {
    return hasFilter ? (
      <EmptyState
        compact
        icon={Activity}
        title="没有匹配的任务"
        description="当前筛选下没有在跑、排队或刚结束的方向；清空筛选可查看全部。"
        action={<Button size="sm" variant="outline" onClick={onClearFilter}>清空筛选</Button>}
      />
    ) : (
      <EmptyState
        compact
        icon={Activity}
        title="还没有任务或运行"
        description="派发意图、执行者跑起来之后，在跑 / 排队 / 刚结束的任务会按时间出现在这里。"
      />
    )
  }

  return (
    <table className="w-full min-w-0 table-fixed border-collapse text-[12px]">
      <thead>
        <tr className="border-b border-hairline bg-surface-2/50 text-left text-[11px] font-medium text-ink-3">
          <th className="w-[104px] px-2.5 py-2 font-medium sm:w-[130px]">项目</th>
          <th className="px-2.5 py-2 font-medium">任务</th>
          <th className="w-[84px] px-2.5 py-2 font-medium">状态</th>
          <th className="hidden w-[96px] px-2.5 py-2 text-right font-medium sm:table-cell">时长</th>
          <th className="hidden w-[150px] px-2.5 py-2 font-medium md:table-cell">上下文 / 静默</th>
          <th className="hidden w-[72px] px-2.5 py-2 text-right font-medium lg:table-cell">事件</th>
          <th className="hidden w-[84px] px-2.5 py-2 text-right font-medium sm:table-cell">tokens</th>
          <th className="w-10 px-2 py-2" />
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => {
          const node = row.kind === 'done' ? row.run.node : row.task.node
          const title = row.kind === 'done' ? row.run.title : row.task.title
          const status = row.kind === 'done' ? row.run.status : row.task.status
          const pulse = row.kind === 'running' ? row.task.pulse : undefined
          const idle = pulse?.idle_seconds ?? 0
          const ctxTokens = pulse?.context_tokens ?? 0
          const ctxWindow = pulse?.context_window ?? 0
          const ctxPct = ctxWindow > 0 ? Math.round((ctxTokens / ctxWindow) * 100) : 0
          const events = row.kind === 'done' ? row.run.events : row.kind === 'running' ? (pulse?.events ?? 0) : 0
          const tokens = row.kind === 'done' ? (row.run.tokens ?? 0) : 0
          const elapsed =
            row.kind === 'done'
              ? row.run.ended_at
                ? fmtDuration(row.run.started_at, row.run.ended_at)
                : '—'
              : fmtDuration(
                  row.kind === 'open' ? row.task.created_at : (row.task.pulse?.started_at ?? row.task.created_at),
                )
          const purpose = row.kind === 'done' ? row.run.purpose : undefined
          const stop = row.kind === 'done' ? row.run.stop_reason : undefined
          return (
            <tr
              key={row.key}
              tabIndex={0}
              className="cursor-pointer border-b border-hairline transition-colors last:border-0 hover:bg-surface-2/60 focus-visible:bg-surface-3 focus-visible:outline-none"
              onClick={() => openRow(row)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' || event.key === ' ') {
                  event.preventDefault()
                  openRow(row)
                }
              }}
              title={stop ? `停止原因：${stop}` : undefined}
            >
              <td className="truncate px-2.5 py-3 text-[12px] text-ink-3" title={row.project.name}>
                {row.project.name}
              </td>
              <td className="min-w-0 px-2.5 py-3">
                <div className="flex min-w-0 items-center gap-1.5">
                  <span className="min-w-0 flex-1 truncate text-[12px] text-ink" title={title}>
                    {title || '—'}
                  </span>
                  {node && (
                    <span className="hidden shrink-0 rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] text-ink-3 sm:inline">
                      {node}
                    </span>
                  )}
                  {purpose && <span className="hidden shrink-0 text-[11px] text-ai sm:inline">{purpose}</span>}
                  {row.kind !== 'done' && row.task.attempts > 1 && (
                    <span className="hidden shrink-0 font-mono text-[11px] text-ink-3 sm:inline">
                      第 {row.task.attempts} 次
                    </span>
                  )}
                </div>
              </td>
              <td className="px-2.5 py-3">
                <StatusIndicator
                  status={runStatus(status)}
                  label={row.kind === 'done' ? runStatusLabel(row.run) : (STATUS_TEXT[status] ?? status)}
                  compact
                />
              </td>
              <td className="hidden whitespace-nowrap px-2.5 py-3 text-right font-mono text-[11px] text-ink-3 sm:table-cell">
                <span className="tw-nums">{elapsed}</span>
                {row.kind === 'open' && <span className="ml-1 text-[11px] text-warning">排队</span>}
              </td>
              <td className="hidden px-2.5 py-3 md:table-cell">
                {row.kind === 'running' ? (
                  ctxWindow > 0 && ctxTokens > 0 ? (
                    <div
                      className="flex items-center gap-2"
                      title={`上下文 ${fmtInt(ctxTokens)} / ${fmtInt(ctxWindow)} tok${pulse?.state ? ` · ${pulse.state}` : ''}`}
                    >
                      <Meter
                        value={Math.min(ctxTokens, ctxWindow)}
                        max={ctxWindow}
                        tone={ctxPct >= 80 ? 'warning' : 'info'}
                        className="h-1 w-20"
                        aria-label="上下文占用"
                      />
                      <span className="tw-nums whitespace-nowrap font-mono text-[11px] text-ink-3">
                        {ctxPct}%
                      </span>
                    </div>
                  ) : (
                    <div
                      className="flex items-center gap-2"
                      title={pulse?.state ? `harness：${pulse.state}` : '还没有 usage 事件'}
                    >
                      <Meter
                        value={Math.min(idle, 1800)}
                        max={1800}
                        tone={idle > 600 ? 'warning' : 'info'}
                        className="h-1 w-20"
                        aria-label="运行静默进度"
                      />
                      <span className="tw-nums whitespace-nowrap font-mono text-[11px] text-ink-3">
                        静默 {idle}s
                      </span>
                    </div>
                  )
                ) : row.kind === 'done' ? (
                  <span className="tw-nums text-[11px] text-ink-3">
                    {row.run.declared_facts ? `${fmtInt(row.run.declared_facts)} 事实` : '—'}
                  </span>
                ) : (
                  <span className="text-[11px] text-ink-3">等待派发</span>
                )}
              </td>
              <td className="tw-nums hidden whitespace-nowrap px-2.5 py-3 text-right font-mono text-[11px] text-ink-3 lg:table-cell">
                {events || '—'}
              </td>
              <td className="tw-nums hidden whitespace-nowrap px-2.5 py-3 text-right font-mono text-[11px] text-ink-3 sm:table-cell">
                {tokens ? fmtInt(tokens) : '—'}
              </td>
              <td className="px-2 py-3 text-right">
                <Button
                  size="icon-xs"
                  variant="ghost"
                  aria-label="打开这条任务"
                  title="打开这条任务"
                  onClick={(event) => {
                    event.stopPropagation()
                    openRow(row)
                  }}
                >
                  <ArrowUpRight className="h-3.5 w-3.5" />
                </Button>
              </td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}

const WINDOWS = [
  { minutes: 30, label: '30m' },
  { minutes: 60, label: '1h' },
  { minutes: 180, label: '3h' },
]

export function Fleet() {
  const fleet = usePoll(api.fleet, 3000)
  const [windowMinutes, setWindowMinutes] = useState(60)
  const [projectFilter, setProjectFilter] = useState('')
  const [nodeFilter, setNodeFilter] = useState('')
  const [onlyRunning, setOnlyRunning] = useState(false)
  const now = useNow()

  const data = fleet.data
  // The wire format is Go's: an empty slice marshals as null, not []. Normalise
  // once here so every consumer can treat the lists as arrays.
  const projects = useMemo(
    () =>
      (data?.projects ?? []).map((p) => ({
        ...p,
        running: p.running ?? [],
        open: p.open ?? [],
        recent_runs: p.recent_runs ?? [],
        token_series: p.token_series ?? [],
      })),
    [data],
  )
  const nodes = data?.nodes ?? []

  const filteredProjects = useMemo(
    () => projects.filter((project) => !projectFilter || project.id === projectFilter),
    [projects, projectFilter],
  )

  // The timeline is node-aware: filtering by node drops the other nodes' bars
  // and adjusts the lane's running count instead of hiding the project.
  const timelineProjects = useMemo(
    () =>
      filteredProjects.map((project) => {
        if (!nodeFilter) return project
        return {
          ...project,
          recent_runs: project.recent_runs.filter((run) => run.node === nodeFilter),
          running: project.running.filter((task) => task.node === nodeFilter),
          intents: {
            ...project.intents,
            running: project.running.filter((task) => task.node === nodeFilter).length,
          },
        }
      }),
    [filteredProjects, nodeFilter],
  )

  const running = timelineProjects.reduce((n, p) => n + p.running.length, 0)
  const activeDeciders = filteredProjects.filter((p) => isPhaseActive(phaseOf(p.decider))).length
  const pending = filteredProjects.reduce((n, p) => n + p.intents.pending, 0)
  const goalsOpen = filteredProjects.reduce((n, p) => n + Math.max(0, p.goals.total - p.goals.met), 0)
  const filtered = Boolean(projectFilter || nodeFilter || onlyRunning)

  function clearFilters() {
    setProjectFilter('')
    setNodeFilter('')
    setOnlyRunning(false)
  }

  return (
    <Page
      title="态势"
      subtitle="项目指挥官 + 执行节点的实时态势；上面看谁在想什么，下面看任务跑了多久"
      meta={
        data ? (
          <span className="tw-eyebrow normal-case tracking-normal">
            每 3 秒刷新 · 快照 <span className="tw-nums font-mono">{fmtTime(data.generated_at)}</span>
          </span>
        ) : undefined
      }
      actions={
        <div className="flex items-center gap-1.5 whitespace-nowrap">
          <div
            className="flex items-center gap-0.5 rounded-md border border-hairline bg-surface-2 p-0.5"
            role="group"
            aria-label="时间窗口"
          >
            {WINDOWS.map((w) => (
              <button
                key={w.minutes}
                type="button"
                onClick={() => setWindowMinutes(w.minutes)}
                aria-pressed={windowMinutes === w.minutes}
                className={`tw-nums h-6 rounded px-2 font-mono text-[11px] transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${
                  windowMinutes === w.minutes
                    ? 'bg-surface-3 text-ink shadow-soft'
                    : 'text-ink-2 hover:bg-surface-3/60 hover:text-ink'
                }`}
              >
                {w.label}
              </button>
            ))}
          </div>
          <Button size="sm" variant="ghost" onClick={fleet.reload} aria-label="重新拉取态势" title="重新拉取态势">
            <RefreshCw className={`h-3.5 w-3.5 ${fleet.refreshing ? 'motion-safe:animate-spin' : ''}`} />
          </Button>
        </div>
      }
    >
      <div className="space-y-4">
        {fleet.error && (
          <div role="alert">
            <Callout tone="destructive" icon={<TriangleAlert className="h-4 w-4" />}>
              <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                <span className="min-w-0 tw-break">态势读不出来：{fleet.error}</span>
                <Button size="xs" variant="outline" onClick={fleet.reload} disabled={fleet.loading}>
                  重试
                </Button>
              </div>
            </Callout>
          </div>
        )}

        {fleet.loading && !data && (
          <div className="space-y-4" aria-busy="true" aria-label="态势加载中">
            <div className="tw-panel rounded-xl p-4 sm:p-5">
              <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 xl:grid-cols-5 xl:gap-6">
                {Array.from({ length: 5 }).map((_, i) => (
                  <div key={i} className="space-y-2">
                    <Skeleton className="h-2.5 w-16" />
                    <Skeleton className="h-7 w-20" />
                  </div>
                ))}
              </div>
            </div>
            <div className="grid gap-4 lg:grid-cols-2 xl:grid-cols-3">
              {Array.from({ length: 3 }).map((_, i) => (
                <div key={i} className="tw-panel space-y-3 rounded-xl p-4">
                  <Skeleton className="h-4 w-2/3" />
                  <Skeleton className="h-3 w-1/3" />
                  <Skeleton className="h-1 w-full" />
                  <Skeleton className="h-3 w-4/5" />
                </div>
              ))}
            </div>
            <div className="tw-panel rounded-xl p-4">
              <SkeletonList count={5} />
            </div>
          </div>
        )}

        {data && (
          <>
            {/* 先总后分: the five numbers that say whether anything is happening,
                then the projects, then the raw run table. */}
            <div className="tw-panel rounded-xl px-4 py-4 sm:px-5 sm:py-5">
              <div className="grid grid-cols-2 gap-4 sm:grid-cols-3 xl:grid-cols-5 xl:gap-6">
                <StatTile
                  size="lg"
                  label="项目"
                  value={filtered ? `${fmtInt(filteredProjects.length)}/${fmtInt(projects.length)}` : fmtInt(projects.length)}
                  hint={filtered ? '当前筛选 / 全部' : '实例内全部项目'}
                />
                <StatTile
                  size="lg"
                  label="在跑意图"
                  value={fmtInt(running)}
                  tone={running > 0 ? 'primary' : 'muted'}
                  hint={nodeFilter ? `节点 ${nodeFilter} 上` : '执行者正在跑的方向'}
                />
                <StatTile
                  size="lg"
                  label="指挥官活动"
                  value={`${fmtInt(activeDeciders)}/${fmtInt(filteredProjects.length)}`}
                  tone={activeDeciders > 0 ? 'primary' : 'muted'}
                  hint="正在思考/决策的项目数"
                />
                <StatTile
                  size="lg"
                  label="待派"
                  value={fmtInt(pending)}
                  tone={pending > 0 ? 'warning' : 'muted'}
                  hint="排队等待执行槽"
                />
                <StatTile
                  size="lg"
                  label="未达目标"
                  value={fmtInt(goalsOpen)}
                  tone={goalsOpen > 0 ? 'caution' : 'muted'}
                  hint="目标总数减已完成"
                />
              </div>
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <SlidersHorizontal className="h-3.5 w-3.5 shrink-0 text-ink-3" aria-hidden="true" />
              <Select
                value={projectFilter || 'all'}
                onValueChange={(value) => setProjectFilter(value === 'all' ? '' : value)}
              >
                <SelectTrigger className="h-8 w-40 text-[12px]" aria-label="按项目筛选">
                  <SelectValue placeholder="全部项目" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">全部项目</SelectItem>
                  {projects.map((project) => (
                    <SelectItem key={project.id} value={project.id}>
                      {project.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>

              <span className="flex items-center gap-1 text-[11px] text-ink-3">
                <Server className="h-3.5 w-3.5" aria-hidden="true" />
                节点
              </span>
              <Chip active={!nodeFilter} onClick={() => setNodeFilter('')} className="h-7 text-[11px]">
                全部
              </Chip>
              {nodes.map((node) => {
                const health = node.health
                const known = Boolean(health?.checked_at)
                const dot = !known ? 'bg-ink-3/40' : health.online ? 'bg-success' : 'bg-destructive'
                const label = health?.error
                  ? health.error
                  : `${health?.online ? '在线' : '离线'} · ${health?.latency_ms ?? 0}ms${
                      health?.agents ? ` · ${health.agents} agent` : ''
                    }${node.endpoint ? ` · ${node.endpoint}` : ''}`
                return (
                  <Chip
                    key={node.id}
                    active={nodeFilter === node.id}
                    onClick={() => setNodeFilter(nodeFilter === node.id ? '' : node.id)}
                    title={label}
                    className="h-7 gap-1.5 text-[11px]"
                  >
                    <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${dot}`} />
                    <span className="font-mono text-ink-2">{node.id}</span>
                    {node.transport && <span className="text-ink-3">{node.transport}</span>}
                    {node.running > 0 && <span className="tw-nums font-mono text-primary">{node.running}</span>}
                  </Chip>
                )
              })}
              {nodes.length === 0 && <span className="text-[11px] text-ink-3">没有节点</span>}

              <span className="mx-1 hidden h-4 w-px bg-hairline sm:block" />
              <Button
                size="xs"
                variant={onlyRunning ? 'soft' : 'ghost'}
                onClick={() => setOnlyRunning((value) => !value)}
                active={onlyRunning}
                title="只显示在跑的任务"
              >
                只看在跑
              </Button>
              {filtered && (
                <Button size="xs" variant="ghost" onClick={clearFilters} className="text-ink-2">
                  清空筛选
                </Button>
              )}
            </div>

            {projects.length === 0 ? (
              <div className="tw-panel rounded-xl">
                <EmptyState
                  icon={FolderPlus}
                  title="还没有项目"
                  description="创建第一个项目并派发意图后，这里会显示指挥官活动、运行时间线与 token 消耗。"
                  action={
                    <Button size="sm" onClick={() => window.location.assign('/g/?view=projects')}>
                      先建一个项目
                    </Button>
                  }
                />
              </div>
            ) : filteredProjects.length === 0 ? (
              <div className="tw-panel rounded-xl">
                <EmptyState
                  icon={SearchX}
                  title="没有匹配的项目"
                  description="当前项目/节点筛选下没有项目；清空筛选可查看全部态势。"
                  action={
                    <Button size="sm" variant="outline" onClick={clearFilters}>
                      清空筛选
                    </Button>
                  }
                />
              </div>
            ) : (
              <>
                <div className="grid min-w-0 gap-4 sm:grid-cols-2 xl:grid-cols-3">
                  {timelineProjects.map((project) => (
                    <ProjectCard key={project.id} project={project} now={now} />
                  ))}
                </div>

                <Panel
                  title={
                    <span className="inline-flex items-center gap-1.5">
                      <Activity className="h-3.5 w-3.5 text-ink-3" aria-hidden="true" /> 运行时间线
                    </span>
                  }
                  extra={
                    <div className="flex flex-wrap items-center justify-end gap-x-3 gap-y-1 text-[11px] text-ink-3">
                      <span className="flex items-center gap-1">
                        <span className="h-2 w-2 rounded-sm bg-primary/80" /> 在跑
                      </span>
                      <span className="flex items-center gap-1">
                        <span className="h-2 w-2 rounded-sm bg-success/70" /> 完成
                      </span>
                      <span className="flex items-center gap-1">
                        <span className="h-2 w-2 rounded-sm bg-destructive/75" /> 失败
                      </span>
                      <span className="flex items-center gap-1">
                        <span className="h-2 w-2 rounded-sm bg-ink-3/35" /> 取消
                      </span>
                      <span className="hidden items-center gap-1 sm:flex">
                        <span className="h-3 w-px bg-warning/60" /> 上一轮决策
                      </span>
                      <span className="hidden xl:inline">点击泳道打开运行页</span>
                    </div>
                  }
                  bodyClassName="p-2.5 sm:p-3"
                >
                  <FleetTimeline
                    projects={timelineProjects}
                    windowMinutes={windowMinutes}
                    onOpen={(project, run) => openProject(project, run ? { view: 'runs', run } : {})}
                  />
                </Panel>

                <Panel
                  title="Token 消耗"
                  extra={<span className="text-[11px] text-ink-3">窗口内 · 按 run 结束时刻归集</span>}
                  bodyClassName="px-3 py-2.5"
                >
                  <TokenHistory projects={filteredProjects} windowMinutes={windowMinutes} nodeFilter={nodeFilter} />
                </Panel>

                <Panel
                  title="任务 / 运行"
                  extra={
                    <span className="text-[11px] text-ink-3">
                      {fmtInt(running)} 在跑 · {fmtInt(pending)} 待派 · {fmtInt(goalsOpen)} 个未达目标
                    </span>
                  }
                  bodyClassName="p-0"
                >
                  <TasksTable
                    projects={filteredProjects}
                    nodeFilter={nodeFilter}
                    onlyRunning={onlyRunning}
                    hasFilter={filtered}
                    onClearFilter={clearFilters}
                  />
                </Panel>
              </>
            )}
          </>
        )}
      </div>
    </Page>
  )
}
