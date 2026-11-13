import { useEffect, useMemo, useRef, useState } from 'react'
import { ArrowDownToLine, Ban, Bot, Copy, FileText, RefreshCw, Search, ShieldCheck, TriangleAlert } from 'lucide-react'
import {
  Badge, Button, Callout, EmptyState, Input, ScrollArea, SkeletonList,
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue, StatusIndicator,
} from '@cyber/ui'
import { Page, Panel } from '@/components/Page'
import { RunEvidenceView } from '@/components/RunEvidenceView'
import { RunReview } from '@/components/RunReview'
import { api, ApiError } from '@/lib/api'
import {
  cancelCauseText,
  fmtDuration,
  fmtInt,
  fmtTime,
  purposeLabel,
  runCancelReason,
  runMatchesFilter,
  runStatusLabel,
  statusKind,
} from '@/lib/format'
import { useConsoleLocation, usePoll } from '@/lib/hooks'
import { useSessionState } from '@/lib/session-state'
import { navigate } from '@/lib/navigation'
import { createRequestGate } from '@/lib/requests'
import { groupRunHistory, participantsByIntent, runExecutor, selectRunHistory } from '@/lib/run-history'
import { scrollRunStreamToLatest } from '@/lib/run-stream'
import type { Event, Intent, Run } from '@/lib/types'

const PROFILE_SOURCE_LABEL: Record<string, string> = {
  explicit: '显式指定',
  default: '实例默认',
  project_worker: '项目 worker 档',
  project_strong: '项目 strong 档',
}

/**
 * Support runs are not executor attempts and must look different at a glance:
 * the commander is a session of its own (own model, own turns), verifier/report
 * are one-shot support jobs. A badge with an icon is the marker; the raw
 * purpose string stays in the tooltip.
 */
function PurposeBadge({ purpose }: { purpose: string }) {
  const common = 'shrink-0'
  switch (purpose) {
    case 'commander':
      return (
        <Badge size="sm" variant="primary" className={common} title="指挥官会话：决策与派发，不执行具体方向">
          <Bot className="h-2.5 w-2.5" /> 指挥官
        </Badge>
      )
    case 'verifier':
      return (
        <Badge size="sm" variant="info" className={common} title="复核 run：独立重跑并给事实下 verdict">
          <ShieldCheck className="h-2.5 w-2.5" /> 复核
        </Badge>
      )
    case 'report':
      return (
        <Badge size="sm" variant="muted" className={common} title="报告 run：从账本生成报告">
          <FileText className="h-2.5 w-2.5" /> 报告
        </Badge>
      )
    default:
      return (
        <Badge size="sm" variant="muted" className={common} title={`purpose=${purpose}`}>
          {purposeLabel(purpose)}
        </Badge>
      )
  }
}

export function Runs({
  onOpenIntent,
  onOpenGraphIntent,
}: {
  onOpenIntent?: (id: string) => void
  /** Cross-page link (docs/34 §4): open the run's direction on the graph as
   *  `s:<intent id>`. Optional — without it no graph entry is rendered. */
  onOpenGraphIntent?: (intentID: string) => void
}) {
  const runs = usePoll(api.runs, 4000)
  // A run is keyed by run id; the direction it executed is the unit the
  // operator reasons about. Joining intents into the list (and the detail)
  // is what makes "which direction is this run?" answerable, and lets the
  // page deep-link back to the state page for that intent.
  const intents = usePoll(api.intents, 6000)
  // The operator asks "which executor did the commander send this to", so the
  // left list names the worker (participant) and its session, not an intent id.
  const participants = usePoll(api.participants, 8000)
  // The frozen input versions name fact ids; resolve their titles and links. It
  // is a read-only index, and an unloaded/old id still renders as its id.
  const facts = usePoll(() => api.facts({ limit: 500 }), 10000)
  // Deep link from the fleet page: ?view=runs&run=<id>. Resolving it here keeps
  // the URL the single source of "which run am I looking at".
  const location = useConsoleLocation()
  const requestedRun = location.searchParams.get('run')
  const requestedGroup = location.searchParams.get('run_group')
  const [filter, setFilter] = useSessionState('runs.search', '')
  const [events, setEvents] = useState<Event[]>([])
  const [eventRun, setEventRun] = useState<string | null>(null)
  const [eventError, setEventError] = useState('')
  // A run without a journal is a fact about the run, not a console failure:
  // verifier/report runs started before D138 never wrote one. Render it as an
  // explanation instead of the red callout used for real read errors.
  const [eventMissing, setEventMissing] = useState(false)
  const [eventsLoading, setEventsLoading] = useState(false)
  // Which event stream row the execution review last pointed at. Highlights the
  // raw row and scrolls it into view; the stream keeps rendering every event.
  const [reviewFocus, setReviewFocus] = useState<number | null>(null)
  const streamRef = useRef<HTMLDivElement | null>(null)
  const [error, setError] = useState('')
  const [copied, setCopied] = useState('')
  const [canceling, setCanceling] = useState<string | null>(null)
  const cancelLock = useRef(false)
  const [notice, setNotice] = useState('')
  const [logRefresh, setLogRefresh] = useState(0)
  const refreshing = runs.loading || runs.refreshing || intents.refreshing || participants.refreshing || facts.refreshing
  function refreshAll() { runs.reload(); intents.reload(); participants.reload(); facts.reload(); setLogRefresh(n => n + 1) }


  async function copyBrief(path: string) {
    try {
      await navigator.clipboard.writeText(path)
      setCopied(path)
      window.setTimeout(() => setCopied(''), 1500)
    } catch {
      setError('复制失败：浏览器没有剪贴板权限')
    }
  }

  function selectRun(id: string) {
    const params = new URLSearchParams(window.location.search)
    params.set('run', id)
    params.delete('run_group')
    navigate(`${window.location.pathname}?${params.toString()}`)
  }

  function selectGroup(key: string, replace = false) {
    const params = new URLSearchParams(window.location.search)
    params.delete('run')
    params.set('run_group', key)
    navigate(`${window.location.pathname}?${params.toString()}`, replace)
  }

  /** A frozen input fact opens on the graph as the selected fact node: the
   *  evidence view is a pointer, so clicking it must be able to leave the list. */
  function openFactInGraph(factID: string) {
    navigate(`${window.location.pathname}?view=graph#${encodeURIComponent(`f:${factID}`)}`)
  }

  const groups = useMemo(() => groupRunHistory(runs.data ?? []), [runs.data])
  const intentByID = useMemo(() => {
    const m = new Map<string, Intent>()
    for (const i of intents.data ?? []) m.set(i.id, i)
    return m
  }, [intents.data])
  const intentOf = (r: Run): Intent | undefined => (!r.purpose && r.intent_id ? intentByID.get(r.intent_id) : undefined)
  const executorByIntent = useMemo(() => participantsByIntent(participants.data ?? []), [participants.data])
  const executorOf = (r: Run) => runExecutor(r, intentOf(r), executorByIntent)
  // Filter runs by what an operator actually has in hand: a run id from an
  // alert, an intent id from the state page, an executor/session, or a title
  // and model they remember.
  const needle = filter.trim().toLowerCase()
  const matchesFilter = (r: Run): boolean => {
    if (runMatchesFilter(r, intentOf(r), filter)) return true
    const executor = executorOf(r)
    return [executor.participantID, executor.sessionID, executor.label, executor.detail]
      .filter(Boolean)
      .join(' ')
      .toLowerCase()
      .includes(needle)
  }
  // Search every attempt but keep the full group so historical matches remain
  // reachable in the selector, even when the latest run has a different model.
  const visible = groups.filter(g => g.runs.some(matchesFilter))
  const { group: currentGroup, run: current } = selectRunHistory(groups, requestedRun, requestedGroup)
  const selected = current?.id ?? null
  const currentIntent = current ? intentOf(current) : undefined
  const currentExecutor = current ? executorOf(current) : null

  // Keep the initial group stable when another executor starts during polling.
  // The group still follows new attempts within its own history.
  const currentGroupKey = currentGroup?.key
  useEffect(() => {
    if (!requestedRun && !requestedGroup && currentGroupKey) selectGroup(currentGroupKey, true)
  }, [requestedRun, requestedGroup, currentGroupKey])

  useEffect(() => {
    if (eventRun !== selected) { setEvents([]); setNotice(''); setError('') }
    setEventRun(selected)
    setEventError('')
    setEventMissing(false)
    setReviewFocus(null)
    setEventsLoading(Boolean(selected))
    if (!selected) return
    const requests = createRequestGate<Event[]>()
    const tick = () => {
      if (document.visibilityState === 'hidden') return
      void requests.run(() => api.events(selected, 400), {
        start: () => {},
        value: evs => {
          setEvents(evs)
          setEventError('')
          setEventMissing(false)
        },
        error: e => {
          setEventMissing(e instanceof ApiError && e.status === 404)
          setEventError(e instanceof Error ? e.message : String(e))
        },
        finish: () => setEventsLoading(false),
      })
    }
    tick()
    const timer = window.setInterval(tick, 3000)
    return () => {
      requests.invalidate()
      window.clearInterval(timer)
    }
  }, [selected, logRefresh])

  // The execution review points at the raw stream instead of replacing it:
  // selecting a review row highlights that event and scrolls it into view.
  useEffect(() => {
    if (reviewFocus === null) return
    const row = streamRef.current?.querySelector(`[data-event-index="${reviewFocus}"]`)
    row?.scrollIntoView({ block: 'nearest' })
  }, [reviewFocus])

  async function cancel(run: Run) {
    if (cancelLock.current) return
    cancelLock.current = true
    setCanceling(run.id)
    setError('')
    setNotice('')
    try {
      await api.cancelRun(run.id)
      setNotice('取消请求已接受，正在同步运行状态。')
      runs.reload()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally { cancelLock.current = false; setCanceling(null) }
  }

  return (
    <Page title="运行" subtitle="按执行者与角色归并，默认显示最新运行，可切换历史记录" actions={
      <Button size="sm" variant="outline" disabled={refreshing} onClick={refreshAll}>
        <RefreshCw className={`h-3.5 w-3.5 ${refreshing ? "animate-spin" : ""}`} /> {refreshing ? "更新中…" : "刷新"}
      </Button>
    } scroll={false}>
      <div className="grid h-full min-h-0 grid-cols-1 gap-4 overflow-y-auto p-4 md:grid-cols-[340px_minmax(0,1fr)] md:overflow-hidden lg:p-6">
        <Panel
          title={`执行者与角色（${visible.length}${needle ? ` / ${groups.length}` : ''}）`}
          className="flex min-h-0 flex-col"
          bodyClassName="flex min-h-0 flex-1 flex-col"
        >
          {(runs.error || intents.error) && <Callout tone="destructive">{runs.error || intents.error}</Callout>}
          <div className="shrink-0 border-b border-border/60 p-2">
            <div className="mb-2 text-[11px] text-muted-foreground">
              每个执行者/角色显示最新一次 · 共 {fmtInt(runs.data?.length ?? 0)} 次运行
            </div>
            <div className="relative">
              <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder="搜索 run / 执行者 / 会话 / 方向标题 / 模型"
                className="h-8 pl-7 text-[12px]"
                aria-label="过滤运行"
              />
            </div>
          </div>
          <div className="min-h-0 flex-1 overflow-y-auto p-1">
            {runs.loading && <SkeletonList count={5} />}
            {runs.error && <div className="p-2 text-xs">读取失败。{runs.updatedAt ? `上次更新：${fmtTime(new Date(runs.updatedAt).toISOString())}` : '尚未取得运行记录。'}<Button size="xs" variant="outline" disabled={refreshing} onClick={refreshAll}>重试</Button></div>}
            {!runs.loading && !runs.error && groups.length === 0 && <div className="p-2 text-xs"><p>还没有运行。创建意图后，执行记录会出现在这里。</p><Button size="xs" onClick={() => navigate(`${window.location.pathname}?view=overview#new`)}>新建意图</Button></div>}
            {groups.length > 0 && visible.length === 0 && (
              <div className="p-2 text-[12px] text-muted-foreground">没有匹配的运行：{filter.trim()}<Button size="xs" variant="ghost" onClick={() => setFilter('')}>清除筛选</Button></div>
            )}
            {visible.map((group) => {
              const r = group.runs[0]
              const executor = executorOf(r)
              return (
                <div
                  key={group.key}
                  className={`mb-1 rounded transition-colors ${
                    currentGroup?.key === group.key ? 'bg-primary/10' : 'hover:bg-accent'
                  }`}
                >
                  <button
                    type="button"
                    onClick={() => selectGroup(group.key)}
                    className="w-full rounded px-2 py-1.5 text-left"
                  >
                  <div className="flex items-center gap-1.5">
                    <StatusIndicator compact status={statusKind(r.status)} label={runStatusLabel(r)} />
                    {r.purpose ? <PurposeBadge purpose={r.purpose} /> : <Badge size="sm" variant="muted">执行者</Badge>}
                    <span className="text-[10px] text-muted-foreground">{group.runs.length} 次运行</span>
                    <span className="ml-auto font-mono text-[11px] text-muted-foreground">
                      {fmtDuration(r.started_at, r.ended_at)}
                    </span>
                  </div>
                  <div className="truncate text-[12px] font-medium" title={executor.label}>
                    {executor.kind === 'executor' && <span className="text-muted-foreground">指挥官 → </span>}
                    {executor.label}
                  </div>
                  <div
                    className="truncate font-mono text-[10px] text-muted-foreground"
                    title={[executor.participantID, executor.sessionID].filter(Boolean).join('\n') || undefined}
                  >
                    {executor.detail || '未记录执行者/会话'}
                  </div>
                  {needle && !matchesFilter(r) && (
                    <div className="text-[11px] text-primary">匹配历史运行，可在详情下拉选择</div>
                  )}
                  <div className="truncate font-mono text-[10px] text-muted-foreground" title={r.id}>最新运行 · {r.id}</div>
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 whitespace-nowrap font-mono text-[11px] text-muted-foreground">
                    <span>{r.events} 事件</span>
                    <span>{r.declared_facts} 事实</span>
                    {r.tokens ? <span>{fmtInt(r.tokens)} tok</span> : null}
                    {(r.warnings?.length ?? 0) > 0 && (
                      <span className="text-warning" title={r.warnings!.join('\n')}>
                        ⚠ {r.warnings!.length} 告警
                      </span>
                    )}
                    {r.usage?.known ? (
                      <span title={`in ${fmtInt(r.usage.input_tokens ?? 0)} / out ${fmtInt(r.usage.output_tokens ?? 0)}`}>
                        in/out {fmtInt(r.usage.input_tokens ?? 0)}/{fmtInt(r.usage.output_tokens ?? 0)}
                      </span>
                    ) : r.usage && !r.usage.known ? <span>用量未知</span> : null}
                  </div>
                  <div className="flex flex-wrap items-center gap-x-1 gap-y-0.5 whitespace-nowrap font-mono text-[11px] text-muted-foreground">
                    <span className="rounded bg-muted px-1">{r.node}</span>
                    {r.resolved_profile || r.model ? (
                      <span className="max-w-full truncate" title={`${r.provider ?? ''} / ${r.model ?? ''}`}>
                        {r.resolved_profile ?? '默认'}
                        {r.model ? ` · ${r.model}` : ''}
                      </span>
                    ) : null}
                    {r.profile_source ? <span className="opacity-70">{PROFILE_SOURCE_LABEL[r.profile_source] ?? r.profile_source}</span> : null}
                  </div>
                  </button>
                  {/* A run row and a graph node are two views of one direction:
                      the entry stays a sibling of the row button (a button
                      nested in a button would be invalid) and only appears when
                      the shell provides the callback. */}
                  {onOpenGraphIntent && r.intent_id && (
                    <div className="flex justify-end px-2 pb-1">
                      <Button
                        size="xs"
                        variant="ghost"
                        data-testid="run-row-graph"
                        title={`在图谱中选中这条方向（${r.intent_id}）`}
                        onClick={() => onOpenGraphIntent(r.intent_id!)}
                      >
                        图谱
                      </Button>
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </Panel>

        <Panel
          className="flex min-h-0 flex-col"
          bodyClassName="min-h-0 flex-1 p-0"
          title={currentIntent?.title || (current ? purposeLabel(current.purpose || '') || '运行详情' : '运行详情')}
          extra={
            current && (
              <>
                <span className="font-mono text-[11px] text-muted-foreground">
                  {fmtTime(current.started_at)}
                </span>
                <Button
                  size="xs"
                  variant="outline"
                  title="跳到当前运行日志最下面"
                  disabled={eventsLoading || eventRun !== selected || events.length === 0}
                  onClick={() => {
                    setReviewFocus(null)
                    scrollRunStreamToLatest(streamRef.current)
                  }}
                >
                  <ArrowDownToLine className="h-3 w-3" /> 跳到最新
                </Button>
                {current.status === 'running' && (
                  <Button size="xs" variant="ghost" disabled={canceling !== null} onClick={() => cancel(current)}>
                    <Ban className="h-3 w-3" /> {canceling === current.id ? "取消中…" : "取消"}
                  </Button>
                )}
              </>
            )
          }
        >
          {runs.loading && <div className="p-4"><SkeletonList count={5} /></div>}
          {!current && !runs.loading && !runs.error && (
            <EmptyState compact title={requestedRun || requestedGroup ? '未找到指定运行' : '还没有运行'} description="按执行者或角色选择一组运行，再从历史下拉框查看具体记录。" />
          )}
          {current && (
            <div className="flex h-full min-h-0 flex-col">
              <div className="shrink-0 border-b border-border px-3 py-3 space-y-2">
                <div className="flex flex-wrap items-center gap-3 text-sm">
                  <StatusIndicator compact status={statusKind(current.status)} label={runStatusLabel(current)} />
                  <span>耗时 {fmtDuration(current.started_at, current.ended_at)}</span>
                  <span>Token {current.tokens == null ? '未记录' : fmtInt(current.tokens)}</span>
                  <span>工具失败 {current.tool_failures ?? '未记录'}</span>
                </div>
                {(current.status === 'canceled' || current.exit_reason) && <p className="text-xs text-warning">{current.status === 'canceled' ? runCancelReason(current) : current.exit_reason}</p>}
                {notice && <p role="status" className="text-xs">{notice}</p>}
                {error && <p role="alert" className="text-xs text-destructive">操作失败：{error}。请检查连接后重试。</p>}
              </div>
              <details key={current.id} className="shrink-0 max-h-[40vh] overflow-y-auto border-b border-border/60">
                <summary className="cursor-pointer px-3 py-2 text-xs text-muted-foreground">运行配置与证据 · {current.warnings?.length ?? 0} 条告警</summary>
                <div className="px-3 py-1 flex items-center gap-2 text-xs"><span className="truncate font-mono">{current.id}</span><Button size="xs" variant="ghost" onClick={() => copyBrief(JSON.stringify(current, null, 2))}>{copied === JSON.stringify(current, null, 2) ? '已复制' : '复制运行详情'}</Button></div>
              <div className="space-y-1.5 p-2 text-[11px]">
                {currentGroup && (
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="shrink-0 text-muted-foreground">运行历史（{currentGroup.runs.length}）</span>
                    <Select
                      value={requestedRun ? `run:${current.id}` : 'latest'}
                      onValueChange={value => value === 'latest' ? selectGroup(currentGroup.key) : selectRun(value.slice(4))}
                    >
                      <SelectTrigger className="h-8 min-w-0 flex-1 text-xs" aria-label="选择历史运行">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent className="max-w-[calc(100vw-2rem)]">
                        <SelectItem value="latest">最新一次（自动跟随）</SelectItem>
                        {currentGroup.runs.map((run, index) => (
                          <SelectItem key={run.id} value={`run:${run.id}`} className="text-xs [&>span:last-child]:break-all">
                            {index === 0 ? '最新 · ' : ''}{fmtTime(run.started_at)} · {runStatusLabel(run)} · {run.generation ? `第 ${run.generation} 代 · ` : ''}{run.continued_from ? '续话 · ' : ''}{run.model ? `${run.model} · ` : ''}{run.id}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    {requestedRun && current.id !== currentGroup.runs[0].id && <Badge size="sm" variant="secondary">查看历史</Badge>}
                  </div>
                )}
                <div className="flex flex-wrap items-center gap-1.5 font-mono">
                  <Badge size="sm" variant="secondary">
                    profile {current.resolved_profile || current.profile || '默认'}
                  </Badge>
                  {current.profile && current.profile !== current.resolved_profile && (
                    <Badge size="sm" variant="outline" title="派发时请求的 profile">
                      请求 {current.profile}
                    </Badge>
                  )}
                  {current.profile_revision && (
                    <span className="opacity-70">rev {current.profile_revision}</span>
                  )}
                  {current.profile_source && (
                    <span className="opacity-70">
                      {PROFILE_SOURCE_LABEL[current.profile_source] ?? current.profile_source}
                    </span>
                  )}
                  {current.purpose && <PurposeBadge purpose={current.purpose} />}
                  {current.generation && <span className="opacity-70">第 {current.generation} 代</span>}
                </div>
                <div className="flex items-center gap-1.5">
                  <span className="shrink-0 text-muted-foreground">意图</span>
                  {currentIntent ? (
                    onOpenIntent ? (
                      <button
                        type="button"
                        data-testid="run-intent-link"
                        onClick={() => onOpenIntent(currentIntent.id)}
                        className="min-w-0 truncate text-left text-primary hover:underline"
                        title="在状态页打开这条方向"
                      >
                        {currentIntent.title}
                        <span className="ml-1 font-mono text-[10px] text-muted-foreground">{currentIntent.id}</span>
                      </button>
                    ) : (
                      <span className="min-w-0 truncate">
                        {currentIntent.title}
                        <span className="ml-1 font-mono text-[10px] text-muted-foreground">{currentIntent.id}</span>
                      </span>
                    )
                  ) : !current.purpose && current.intent_id ? (
                    <span className="min-w-0 truncate font-mono text-warning" title="意图记录不在账本里">
                      {current.intent_id}（意图不在账本）
                    </span>
                  ) : (
                    <span className="text-muted-foreground">支持运行，不挂意图</span>
                  )}
                  {/* 跨页联动（docs/34 §4）：与「状态」入口并列的图谱入口。
                      只对有 intent_id 的 run 显示；support run 不挂方向，就没有。 */}
                  {onOpenGraphIntent && current.intent_id && (
                    <Button
                      size="xs"
                      variant="ghost"
                      data-testid="run-open-graph"
                      title="在图谱中选中这条方向"
                      onClick={() => onOpenGraphIntent(current.intent_id!)}
                    >
                      图谱
                    </Button>
                  )}
                </div>
                <div className="flex items-center gap-1.5">
                  <span className="shrink-0 text-muted-foreground">执行者</span>
                  {currentExecutor?.participantID ? (
                    <span
                      className="min-w-0 truncate"
                      title={[currentExecutor.participantID, currentExecutor.sessionID].filter(Boolean).join(' · ')}
                    >
                      <span className="font-mono">{currentExecutor.participantID}</span>
                      {currentExecutor.sessionID && (
                        <span className="ml-1 font-mono text-[10px] text-muted-foreground">
                          {' · 会话 '}{currentExecutor.sessionID.slice(0, 10)}…
                        </span>
                      )}
                    </span>
                  ) : (
                    <span className="text-muted-foreground">
                      {currentExecutor?.kind === 'executor' ? '执行者不在参与者列表' : '支持运行，没有执行者参与者'}
                    </span>
                  )}
                </div>
                <div className="flex flex-wrap items-center gap-x-3 gap-y-0.5 font-mono text-muted-foreground">
                  <span>
                    usage{' '}
                    {current.usage?.known
                      ? `in ${fmtInt(current.usage.input_tokens ?? 0)} / out ${fmtInt(current.usage.output_tokens ?? 0)} · 其中缓存输入 ${fmtInt(current.usage.cached_input_tokens ?? 0)} · ${fmtInt(current.usage.requests ?? 0)} req`
                      : current.usage
                        ? '未知（供应商未报告）'
                        : '未记录'}
                  </span>
                  {current.tool_failures !== undefined && <span>工具失败 {fmtInt(current.tool_failures)}</span>}
                  {current.exit_reason && current.status !== 'canceled' && (
                    <span className="truncate" title={current.exit_reason}>
                      exit {current.exit_reason}
                    </span>
                  )}
                </div>
                {current.status === 'canceled' && (
                  <div className="flex items-start gap-1.5">
                    <span className="shrink-0 rounded bg-warning/15 px-1 font-medium text-warning">取消原因</span>
                    <span
                      className="min-w-0 flex-1 text-foreground/85 tw-break"
                      title={cancelCauseText(current.cancel_cause)}
                    >
                      {runCancelReason(current)}
                    </span>
                  </div>
                )}
                {current.brief_path && (
                  <div className="flex items-center gap-1.5">
                    <span className="shrink-0 text-muted-foreground">任务书归档</span>
                    <span className="min-w-0 truncate font-mono text-foreground/80" title={current.brief_path}>
                      {current.brief_path}
                    </span>
                    <Button
                      size="icon-xs"
                      variant="ghost"
                      title="复制任务书路径（只读，不发起文件下载）"
                      onClick={() => copyBrief(current.brief_path!)}
                    >
                      <Copy className="h-3 w-3" />
                    </Button>
                    <span className="shrink-0 text-[10px] text-muted-foreground">
                      {copied === current.brief_path ? '已复制' : '只读路径'}
                    </span>
                  </div>
                )}
                {/* What this run was dispatched against, frozen: premise claim
                    versions and the context record versions it actually saw. */}
                <RunEvidenceView run={current} facts={facts.data ?? []} onSelectFact={openFactInGraph} />
                {(current.warnings?.length ?? 0) > 0 && (
                  <div className="space-y-0.5 rounded border border-warning/30 bg-warning/10 px-2 py-1">
                    <div className="flex items-center gap-1 font-semibold text-warning">
                      <TriangleAlert className="h-3 w-3" /> 告警（{current.warnings!.length}）
                      <span className="font-normal opacity-80">
                        · 含 harness 原生 subagent 使用审计
                      </span>
                    </div>
                    {current.warnings!.map((w, i) => (
                      <div key={i} className="tw-break leading-relaxed text-foreground/80">
                        {/subagent/i.test(w) && (
                          <Badge size="sm" variant="warning" className="mr-1">
                            原生 subagent
                          </Badge>
                        )}
                        {w}
                      </div>
                    ))}
                  </div>
                )}
              </div>
              </details>
              <ScrollArea className="min-h-0 flex-1">
                <div ref={streamRef} className="space-y-0.5 p-2">
                {error && <Callout tone="destructive">{error}</Callout>}
                {eventRun === selected && eventError && eventMissing && (
                  <div className="rounded border border-border/60 bg-muted/40 px-2 py-1.5 text-[12px] text-muted-foreground">
                    {eventError}
                  </div>
                )}
                {eventRun === selected && eventError && !eventMissing && <Callout tone="destructive">日志更新失败，已保留上次记录。<Button size="xs" variant="outline" onClick={() => setLogRefresh(n => n + 1)}>重试日志</Button><details><summary>错误详情</summary>{eventError}</details></Callout>}
                {(eventsLoading || eventRun !== selected) && <div role="status" className="py-3 text-xs text-muted-foreground">正在读取当前运行日志…</div>}
                {eventRun === selected && !eventsLoading && !eventError && events.length === 0 && <div className="text-[12px] text-muted-foreground">日志为空</div>}
                {eventRun === selected && !eventsLoading && !eventError && events.length > 0 && (
                  <RunReview
                    events={events}
                    totalTokens={current.tokens}
                    focusIndex={reviewFocus}
                    onSelect={setReviewFocus}
                  />
                )}
                {(eventRun === selected ? events : []).map((ev, i) => (
                  <div
                    key={i}
                    data-event-index={i}
                    className={`flex items-start gap-2 border-b border-border/40 py-0.5 text-[12px] ${
                      reviewFocus === i ? 'rounded bg-primary/10' : ''
                    }`}
                  >
                    <Badge size="sm" variant={eventVariant(ev.kind)} className="mt-0.5 shrink-0">
                      {ev.kind}
                    </Badge>
                    {ev.tool && <span className="tw-mono shrink-0 text-info">{ev.tool}</span>}
                    <span className="tw-break min-w-0 flex-1 text-foreground/85">{ev.text || ''}</span>
                    {ev.kind === 'turn_ended' && ev.stop_reason && (
                      <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
                        {ev.stop_reason}
                      </span>
                    )}
                    <span className="shrink-0 font-mono text-[11px] text-muted-foreground">
                      {fmtTime(ev.at).slice(6)}
                    </span>
                  </div>
                ))}
              </div>
            </ScrollArea>
            </div>
          )}
        </Panel>
      </div>
    </Page>
  )
}

function eventVariant(kind: string): 'primary' | 'info' | 'success' | 'danger' | 'muted' | 'warning' {
  switch (kind) {
    case 'tool_call':
      return 'info'
    case 'tool_result':
      return 'primary'
    case 'error':
      return 'danger'
    case 'turn_ended':
      return 'success'
    case 'usage':
      return 'warning'
    default:
      return 'muted'
  }
}
