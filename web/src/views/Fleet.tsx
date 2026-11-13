import { useEffect, useMemo, useState } from 'react'
import { ArrowUpRight, Bot, RefreshCw, Server, SlidersHorizontal } from 'lucide-react'
import {
  Button,
  Meter,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SkeletonList,
  StatTile,
} from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { FleetTimeline } from '@/components/FleetTimeline'
import { TokenHistory } from '@/components/TokenHistory'
import { api } from '@/lib/api'
import { isPhaseActive, PHASE_LABEL, phaseDetail, phaseOf, phaseProgress } from '@/lib/decider'
import { fmtDuration, fmtInt, fmtTime, runStatusLabel } from '@/lib/format'
import { usePoll } from '@/lib/hooks'
import type { FleetProject, FleetResponse, FleetRun, FleetTask } from '@/lib/types'

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

function dotOf(phase: string): string {
  if (isPhaseActive(phase)) return 'animate-pulse bg-info'
  if (phase === 'paused') return 'bg-warning'
  if (phase === 'blocked') return 'bg-caution'
  return 'bg-muted-foreground'
}

/** A compact commander card: the project is the unit the decider belongs to. */
function CommanderTile({ project, now }: { project: FleetProject; now: number }) {
  const dec = project.decider
  const phase = phaseOf(dec)
  const active = isPhaseActive(phase)
  const since = dec.activity?.since ? new Date(dec.activity.since).getTime() : 0
  const elapsed = since > 0 ? Math.max(0, Math.round((now - since) / 1000)) : 0
  const bar = active ? phaseProgress(elapsed) : 0
  const detail = phaseDetail(dec)

  return (
    <button
      type="button"
      onClick={() => openProject(project.id)}
      className="flex min-w-[238px] flex-1 flex-col rounded-lg border border-border bg-card px-3 py-2 text-left transition-colors hover:border-primary/50"
      title={`${project.name}${project.scope ? ` · ${project.scope}` : ''}`}
    >
      <div className="flex items-center gap-1.5">
        <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${dotOf(phase)}`} />
        <span className="truncate text-[12px] font-medium text-foreground">{project.name}</span>
        <span className="ml-auto shrink-0 font-mono text-[10px] text-muted-foreground">
          {dec.rounds ? `第 ${fmtInt(dec.rounds)} 轮` : '待命'}
        </span>
      </div>

      <div className="mt-0.5 flex items-center gap-1.5 text-[11px]">
        <span className="text-foreground/90">{PHASE_LABEL[phase] ?? phase}</span>
        {active && elapsed > 0 && <span className="font-mono text-[10px] text-muted-foreground">{elapsed}s</span>}
        {dec.activity?.wake && (
          <span className="ml-auto shrink-0 rounded bg-muted px-1 font-mono text-[10px] text-muted-foreground">
            {dec.activity.wake}
          </span>
        )}
      </div>

      <Meter
        value={bar}
        max={100}
        tone={phase === 'paused' || phase === 'blocked' ? 'warning' : 'info'}
        className="mt-1.5 h-1"
        aria-label="指挥官本轮进度"
      />

      <div className="mt-1 truncate text-[10px] text-muted-foreground" title={detail}>
        {detail}
      </div>

      <div className="mt-1 flex items-center gap-2 text-[10px] text-muted-foreground">
        <span className={project.intents.running > 0 ? 'text-primary' : ''}>在跑 {project.intents.running}</span>
        <span>待派 {project.intents.pending}</span>
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

function statusDot(status: string): string {
  switch (status) {
    case 'running':
    case 'claimed':
      return 'animate-pulse bg-info'
    case 'pending':
      return 'bg-warning'
    case 'done':
      return 'bg-success'
    case 'failed':
      return 'bg-destructive'
    default:
      return 'bg-muted-foreground'
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

function openRow(row: Row) {
  if (row.kind === 'open') {
    openProject(row.project.id, { view: 'graph', intent: row.task.id })
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
}: {
  projects: FleetProject[]
  nodeFilter: string
  onlyRunning: boolean
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
          if (!nodeMatches(task.node)) continue
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
    return <div className="px-3 py-8 text-center text-[12px] text-muted-foreground">没有匹配的任务。</div>
  }

  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[920px] border-collapse text-[12px]">
        <thead>
          <tr className="border-b border-border text-left text-[10px] uppercase tracking-wider text-muted-foreground">
            <th className="px-3 py-1.5 font-medium">项目</th>
            <th className="px-3 py-1.5 font-medium">任务</th>
            <th className="px-3 py-1.5 font-medium">状态</th>
            <th className="px-3 py-1.5 font-medium">时长</th>
            <th className="px-3 py-1.5 font-medium">上下文 / 静默</th>
            <th className="px-3 py-1.5 text-right font-medium">事件</th>
            <th className="px-3 py-1.5 text-right font-medium">tokens</th>
            <th className="px-3 py-1.5" />
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
            const events =
              row.kind === 'done' ? row.run.events : row.kind === 'running' ? (pulse?.events ?? 0) : 0
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
                className="cursor-pointer border-b border-border/50 last:border-0 hover:bg-accent/40"
                onClick={() => openRow(row)}
                title={stop ? `停止原因：${stop}` : undefined}
              >
                <td className="max-w-[130px] truncate px-3 py-1.5 text-muted-foreground" title={row.project.name}>
                  {row.project.name}
                </td>
                <td className="px-3 py-1.5">
                  <div className="flex min-w-0 items-center gap-1.5">
                    <span className="max-w-[300px] truncate text-foreground" title={title}>
                      {title || '—'}
                    </span>
                    {node && (
                      <span className="shrink-0 rounded bg-muted px-1 font-mono text-[10px] text-muted-foreground">
                        {node}
                      </span>
                    )}
                    {purpose && <span className="shrink-0 text-[10px] text-ai">{purpose}</span>}
                    {row.kind !== 'done' && row.task.attempts > 1 && (
                      <span className="shrink-0 text-[10px] text-muted-foreground">第 {row.task.attempts} 次</span>
                    )}
                  </div>
                </td>
                <td className="px-3 py-1.5">
                  <span className="inline-flex items-center gap-1.5">
                    <span className={`h-1.5 w-1.5 rounded-full ${statusDot(status)}`} />
                    <span className="text-foreground/90">{row.kind === 'done' ? runStatusLabel(row.run) : STATUS_TEXT[status] ?? status}</span>
                  </span>
                </td>
                <td className="whitespace-nowrap px-3 py-1.5 font-mono text-[11px] text-muted-foreground">
                  {elapsed}
                  {row.kind === 'open' && <span className="ml-1 text-[10px]">排队</span>}
                </td>
                <td className="px-3 py-1.5">
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
                        <span className="whitespace-nowrap font-mono text-[10px] text-muted-foreground">
                          上下文 {ctxPct}%
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
                        <span className="whitespace-nowrap font-mono text-[10px] text-muted-foreground">
                          静默 {idle}s
                        </span>
                      </div>
                    )
                  ) : row.kind === 'done' ? (
                    <span className="text-[11px] text-muted-foreground">
                      {row.run.declared_facts ? `${fmtInt(row.run.declared_facts)} 事实` : '—'}
                    </span>
                  ) : (
                    <span className="text-[11px] text-muted-foreground">等待派发</span>
                  )}
                </td>
                <td className="px-3 py-1.5 text-right font-mono text-[11px] text-muted-foreground">
                  {events || '—'}
                </td>
                <td className="px-3 py-1.5 text-right font-mono text-[11px] text-muted-foreground">
                  {tokens ? fmtInt(tokens) : '—'}
                </td>
                <td className="px-3 py-1.5 text-right">
                  <Button
                    size="icon-xs"
                    variant="ghost"
                    title="打开这条任务"
                    onClick={(event) => {
                      event.stopPropagation()
                      openRow(row)
                    }}
                  >
                    <ArrowUpRight className="h-3 w-3" />
                  </Button>
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
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
  const filtering = Boolean(projectFilter || nodeFilter || onlyRunning)

  function clearFilters() {
    setProjectFilter('')
    setNodeFilter('')
    setOnlyRunning(false)
  }

  return (
    <Page
      title="态势"
      subtitle="项目指挥官 + 执行节点的实时态势；上面看谁在想什么，下面看任务跑了多久"
      actions={
        <div className="flex items-center gap-1.5 whitespace-nowrap">
          <div className="flex items-center rounded-md border border-border p-0.5">
            {WINDOWS.map((w) => (
              <button
                key={w.minutes}
                type="button"
                onClick={() => setWindowMinutes(w.minutes)}
                className={`rounded px-1.5 py-0.5 text-[11px] ${
                  windowMinutes === w.minutes
                    ? 'bg-accent text-foreground'
                    : 'text-muted-foreground hover:text-foreground'
                }`}
              >
                {w.label}
              </button>
            ))}
          </div>
          <Button size="xs" variant="ghost" onClick={fleet.reload} title="重新拉取态势">
            <RefreshCw className="h-3.5 w-3.5" />
          </Button>
        </div>
      }
    >
      <div className="space-y-3">
        {fleet.error && <div className="text-[12px] text-destructive">态势读不出来：{fleet.error}</div>}
        {fleet.loading && !data && <SkeletonList count={4} />}

        {data && (
          <>
            <div className="grid grid-cols-2 gap-3 rounded-lg border border-border bg-card px-3 py-2 sm:grid-cols-3 lg:grid-cols-4">
              <StatTile
                label="项目"
                value={filtering ? `${filteredProjects.length}/${projects.length}` : fmtInt(projects.length)}
              />
              <StatTile label="在跑意图" value={fmtInt(running)} tone={running > 0 ? 'primary' : 'default'} />
              <StatTile
                label="指挥官活动"
                value={`${activeDeciders}/${filteredProjects.length}`}
                tone={activeDeciders > 0 ? 'primary' : 'muted'}
              />
              <StatTile label="待派" value={fmtInt(pending)} tone={pending > 0 ? 'warning' : 'muted'} />
            </div>

            <div className="flex flex-wrap items-center gap-2 text-[11px]">
              <SlidersHorizontal className="h-3.5 w-3.5 text-muted-foreground" />
              <Select
                value={projectFilter || 'all'}
                onValueChange={(value) => setProjectFilter(value === 'all' ? '' : value)}
              >
                <SelectTrigger className="h-7 w-40 text-[11px]">
                  <SelectValue placeholder="全部项目" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all" className="text-xs">
                    全部项目
                  </SelectItem>
                  {projects.map((project) => (
                    <SelectItem key={project.id} value={project.id} className="text-xs">
                      {project.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>

              <span className="flex items-center gap-1 text-muted-foreground">
                <Server className="h-3.5 w-3.5" />
                节点
              </span>
              <button
                type="button"
                onClick={() => setNodeFilter('')}
                className={`rounded-full border px-2 py-0.5 ${
                  !nodeFilter ? 'border-primary/50 bg-primary/10 text-primary' : 'border-border text-muted-foreground'
                }`}
              >
                全部
              </button>
              {nodes.map((node) => {
                const health = node.health
                const known = Boolean(health?.checked_at)
                const dot = !known ? 'bg-muted-foreground/40' : health.online ? 'bg-success' : 'bg-destructive'
                const label = health?.error
                  ? health.error
                  : `${health?.online ? '在线' : '离线'} · ${health?.latency_ms ?? 0}ms${
                      health?.agents ? ` · ${health.agents} agent` : ''
                    }${node.endpoint ? ` · ${node.endpoint}` : ''}`
                return (
                  <button
                    key={node.id}
                    type="button"
                    onClick={() => setNodeFilter(nodeFilter === node.id ? '' : node.id)}
                    className={`inline-flex items-center gap-1 rounded-full border px-2 py-0.5 ${
                      nodeFilter === node.id
                        ? 'border-primary/50 bg-primary/10 text-primary'
                        : 'border-border bg-card/60 hover:border-primary/40'
                    }`}
                    title={label}
                  >
                    <span className={`h-1.5 w-1.5 rounded-full ${dot}`} />
                    <span className="font-mono text-foreground/90">{node.id}</span>
                    {node.transport && <span className="text-muted-foreground">{node.transport}</span>}
                    {node.running > 0 && <span className="font-mono text-primary">{node.running}</span>}
                  </button>
                )
              })}
              {nodes.length === 0 && <span className="text-muted-foreground">没有节点</span>}

              <span className="mx-1 h-4 w-px bg-border" />
              <Button
                size="xs"
                variant={onlyRunning ? 'soft' : 'ghost'}
                onClick={() => setOnlyRunning((value) => !value)}
                title="只显示在跑的任务"
              >
                只看在跑
              </Button>
              {filtering && (
                <button type="button" className="text-muted-foreground underline-offset-2 hover:underline" onClick={clearFilters}>
                  清空筛选
                </button>
              )}
            </div>

            {projects.length === 0 ? (
              <div className="rounded-lg border border-border bg-card px-4 py-10 text-center text-[12px] text-muted-foreground">
                还没有项目。
                <button
                  type="button"
                  className="ml-1 text-primary hover:underline"
                  onClick={() => window.location.assign('/g/?view=projects')}
                >
                  先建一个项目
                </button>
              </div>
            ) : filteredProjects.length === 0 ? (
              <div className="rounded-lg border border-border bg-card px-4 py-10 text-center text-[12px] text-muted-foreground">
                没有匹配的项目。
                <button type="button" className="ml-1 text-primary hover:underline" onClick={clearFilters}>
                  清空筛选
                </button>
              </div>
            ) : (
              <>
                <div className="flex gap-2 overflow-x-auto pb-1">
                  {timelineProjects.map((project) => (
                    <CommanderTile key={project.id} project={project} now={now} />
                  ))}
                </div>

                <Panel
                  title={
                    <span className="inline-flex items-center gap-1.5">
                      <Bot className="h-3.5 w-3.5 text-ai" /> 运行时间线
                    </span>
                  }
                  extra={
                    <div className="flex flex-wrap items-center justify-end gap-x-3 gap-y-1 text-[10px] text-muted-foreground">
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
                        <span className="h-2 w-2 rounded-sm bg-muted-foreground/35" /> 取消
                      </span>
                      <span className="flex items-center gap-1">
                        <span className="h-3 w-px bg-warning/60" /> 上一轮决策
                      </span>
                      <span className="hidden xl:inline">点击泳道打开运行页</span>
                    </div>
                  }
                  bodyClassName="p-2.5"
                >
                  <FleetTimeline
                    projects={timelineProjects}
                    windowMinutes={windowMinutes}
                    onOpen={(project, run) => openProject(project, run ? { view: 'runs', run } : {})}
                  />
                </Panel>

                <Panel
                  title="Token 消耗"
                  extra={<span className="text-[10px] text-muted-foreground">窗口内 · 按 run 结束时刻归集</span>}
                  bodyClassName="px-2.5 py-2"
                >
                  <TokenHistory projects={filteredProjects} windowMinutes={windowMinutes} nodeFilter={nodeFilter} />
                </Panel>

                <Panel
                  title="任务 / 运行"
                  extra={
                    <span className="text-[10px] text-muted-foreground">
                      {fmtInt(running)} 在跑 · {fmtInt(pending)} 待派 · {fmtInt(goalsOpen)} 个未达目标
                    </span>
                  }
                  bodyClassName="p-0"
                >
                  <TasksTable projects={filteredProjects} nodeFilter={nodeFilter} onlyRunning={onlyRunning} />
                </Panel>

                <div className="pb-1 text-right text-[10px] text-muted-foreground">
                  每 3 秒刷新（页面隐藏时暂停） · 快照 {fmtTime(data.generated_at)}
                </div>
              </>
            )}
          </>
        )}
      </div>
    </Page>
  )
}
