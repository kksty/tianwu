import { useEffect, useMemo, useRef, useState } from 'react'
import {
  Activity,
  Bot,
  BookMarked,
  ChevronDown,
  ChevronUp,
  CircleAlert,
  CircleCheck,
  CircleDashed,
  Flag,
  FolderKanban,
  Network,
  Play,
  Plus,
  RefreshCw,
  ScrollText,
  Send,
  Server,
  ShieldCheck,
  Target,
  Terminal,
} from 'lucide-react'
import {
  Badge,
  Button,
  Callout,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  EmptyState,
  Field,
  ListRow,
  Meter,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SkeletonCard,
  Spinner,
  StatTile,
  StatusIndicator,
  Textarea,
} from '@cyber/ui'
import { cn } from '@cyber/theme'
import { Page, Panel } from '@/components/Page'
import { ChatComposer } from '@/components/ChatComposer'
import { CreateGoalDialog } from '@/components/CreateGoalDialog'
import { NewProjectDialog } from '@/components/NewProjectDialog'
import { api } from '@/lib/api'
import { PHASE_LABEL, isPhaseActive, phaseDetail, phaseOf, phaseProgress } from '@/lib/decider'
import {
  fmtInt,
  fmtTime,
  fmtUptime,
  frontierMarkers,
  frontierPremiseChangedText,
  kindVariant,
  originVariant,
  runStatusLabel,
  statusKind,
  statusLabel,
} from '@/lib/format'
import { useLoad, usePoll } from '@/lib/hooks'
import { navigate } from '@/lib/navigation'
import type { BoardItem, BoardResponse, Decision, Evidence, Goal, Playbook, ProjectInfo, Run } from '@/lib/types'

// A run quiet this long is worth surfacing on the dashboard. It is not the
// guard's reap threshold (that is --idle-timeout, shown in the panel): this is
// the "looks stuck" line, far earlier than the destructive one.
const QUIET_SECONDS = 300

/**
 * 概览 = 这个项目的作战台。
 *
 * Reading order, strongest → weakest (docs/UI · 视觉规范 v2):
 *   1. the page title and the single primary CTA (新建目标);
 *   2. the metrics that need action now (failed runs / directions to dispatch /
 *      blocked premises / running workers), then the quieter ledger totals;
 *   3. the foreground panels (goals, commander, dispatch, frontier, facts);
 *   4. reference material (knowledge, delivery/host diagnostics) collapsed.
 * The header carries only a quiet 11px meta line (project id, snapshot, live
 * phase, decision tokens) so the counters never compete with the actions.
 */
export function Overview({
  current,
  projects,
  board,
  onRefresh,
  onNavigate,
  onOpenNode,
  refreshGoalsSeq,
}: {
  current: string
  projects: ProjectInfo[]
  board?: BoardResponse
  onRefresh: () => void
  onNavigate?: (view: string) => void
  /** Cross-page link (docs/34 §4): select a graph node (`f:`/`s:`) on the graph
   *  page. Optional — without it the same rows stay plain text. */
  onOpenNode?: (graphNodeID: string) => void
  /** Bumped by the shell after "追加目标并继续" succeeds: the goals poll reloads
   *  immediately instead of waiting for its next period. */
  refreshGoalsSeq?: number
}) {
  const goals = usePoll(api.goals, 15000)
  const runs = usePoll(api.runs, 10000)
  const nodes = usePoll(api.nodes, 30000)
  const playbooks = usePoll(() => api.playbooksPage().then(page => page.playbooks), 15000)
  const decisions = usePoll(() => api.decisions(4), 15000)
  const memory = usePoll(api.state, 30000)
  const operator = usePoll(api.operatorRequests, 6000)
  const needHuman = (operator.data ?? []).filter(item => !item.request.completed_at)

  // The append-goal dialog lives in the project shell; when it succeeds the
  // overview's own goals poll must pick the new goal up right away.
  const goalsSeqRef = useRef(refreshGoalsSeq)
  useEffect(() => {
    if (refreshGoalsSeq === undefined || goalsSeqRef.current === refreshGoalsSeq) return
    goalsSeqRef.current = refreshGoalsSeq
    goals.reload()
    // goals.reload is stable for this poll; only a new shell signal retriggers.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshGoalsSeq])

  const [newGoal, setNewGoal] = useState(false)
  const [newProject, setNewProject] = useState(false)
  const [metGoal, setMetGoal] = useState<Goal | null>(null)
  const [evidenceID, setEvidenceID] = useState('')
  // The seal is only meaningful with an argument: the server requires the note.
  const [metNote, setMetNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [starting, setStarting] = useState(false)
  const [startNote, setStartNote] = useState('')

  // The evidence list is only needed by the "mark goal achieved" dialog: it
  // populates that picker. Loading it with the dashboard pulled every evidence
  // record (~870 KB on this engagement) for a picker the operator may never
  // open; it now loads when the dialog does.
  const evidence = useLoad(
    () => (metGoal ? api.evidence() : Promise.resolve<Evidence[]>([])),
    [metGoal],
  )

  const project = projects.find((p) => p.id === current)
  const decider = board?.decider
  const paused = Boolean(board?.project_paused || board?.decider.paused)
  const bound = (nodes.data?.nodes ?? []).filter((n) => n.bound)
  const defaultNode = nodes.data?.project_default ?? ''
  const runList = runs.data ?? []
  const verified = board?.facts.verified ?? 0
  const openGoals = useMemo(
    () => (goals.data ?? []).slice().sort((a, b) => Number(a.met) - Number(b.met)),
    [goals.data],
  )
  const fresh = Boolean(board) && board!.intents.total === 0 && board!.facts.total === 0 && openGoals.length === 0
  // Starting no longer needs a goal: the commander divides the brief into goals
  // itself (POST /api/goals). A bound local execution host is the only gate.
  const canStart = bound.length > 0
  const refreshing = goals.refreshing || runs.refreshing || nodes.refreshing || playbooks.refreshing || decisions.refreshing || memory.refreshing || operator.refreshing

  const autorun = useRef(new URLSearchParams(window.location.search).get('autorun') === '1')

  async function startRound() {
    setStarting(true)
    setStartNote('')
    try {
      await api.decide('operator')
      setStartNote('已排队：指挥官会读 board 派发，去「图谱」或「运行」看进展。')
    } catch (e) {
      setStartNote(e instanceof Error ? e.message : String(e))
    } finally {
      setStarting(false)
    }
  }

  // "创建并开始" lands here with ?autorun=1: one round, once, then the flag is
  // removed so a refresh does not dispatch again.
  useEffect(() => {
    if (!autorun.current) return
    autorun.current = false
    const params = new URLSearchParams(window.location.search)
    params.delete('autorun')
    const qs = params.toString()
    window.history.replaceState(null, '', `${window.location.pathname}${qs ? `?${qs}` : ''}`)
    void startRound()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  function refreshAll() {
    goals.reload()
    runs.reload()
    evidence.reload()
    nodes.reload()
    playbooks.reload()
    decisions.reload()
    memory.reload()
    operator.reload()
    onRefresh()
  }

  /** Closing the seal dialog always clears the draft: a note for goal A must
   *  never leak into the seal of goal B. */
  function closeMet() {
    setMetGoal(null)
    setEvidenceID('')
    setMetNote('')
    setError('')
  }

  async function markMet() {
    if (!metGoal || !evidenceID) {
      setError('盖章必须选一条证据')
      return
    }
    const why = metNote.trim()
    if (!why) {
      setError('盖章必须写一句为什么达成')
      return
    }
    setBusy(true)
    setError('')
    try {
      await api.metGoal(metGoal.id, evidenceID, why)
      closeMet()
      refreshAll()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  const staleGoals = (goals.data ?? []).filter((g) => g.receipt_stale)
  const dataError =
    goals.error || runs.error || nodes.error || playbooks.error || decisions.error || memory.error || operator.error
  const scope = project?.scope?.trim() || '未指定——按操作员当前有效指令与项目授权口径执行，执行者不自行扩大目标'

  return (
    <Page
      title="概览"
      subtitle={
        <span className="line-clamp-2">
          <span>项目简报（当前口径，可动态补充）：{scope}</span>
        </span>
      }
      meta={
        <>
          <span className="tw-break font-mono text-[11px] tw-nums text-ink-3" title={current}>ID {current}</span>
          <MetaDivider />
          <span className="text-[11px] text-ink-3">
            快照 <span className="font-mono tw-nums">{board ? fmtTime(board.generated_at) : '—'}</span>
          </span>
          <MetaDivider />
          <LiveMeta board={board} />
          <MetaDivider />
          <span className="text-[11px] text-ink-3">
            决策 tokens <span className="font-mono tw-nums">{fmtInt(decider?.tokens_total)}</span>
          </span>
        </>
      }
      actions={
        <>
          <Button
            size="sm"
            variant="ghost"
            onClick={refreshAll}
            disabled={refreshing}
            title="重新拉取事实意图图"
            aria-label="刷新概览数据"
          >
            <RefreshCw className={cn('h-3.5 w-3.5', refreshing && 'animate-spin')} />
            <span className="hidden sm:inline">刷新</span>
          </Button>
          <Button
            size="sm"
            variant="outline"
            className="sm:hidden"
            onClick={() => setNewProject(true)}
            aria-label="新建项目"
            title="新建项目"
          >
            <FolderKanban className="h-3.5 w-3.5" />
          </Button>
          <Button size="sm" variant="outline" className="hidden sm:inline-flex" onClick={() => setNewProject(true)}>
            <FolderKanban className="h-3.5 w-3.5" /> 新建项目
          </Button>
          <Button size="sm" onClick={() => setNewGoal(true)}>
            <Flag className="h-3.5 w-3.5" /> 新建目标
          </Button>
        </>
      }
      scroll={false}
    >
      <div className="mx-auto flex h-full min-h-0 w-full max-w-[1680px] flex-col gap-5 overflow-y-auto px-5 py-5 sm:px-6 lg:px-8">
        {(startNote || staleGoals.length > 0 || board?.reviews || dataError || needHuman.length > 0) && (
          <div className="flex shrink-0 flex-col gap-3">
            {startNote && (
              <Callout tone="info">
                <span className="text-[12.5px] leading-relaxed">{startNote}</span>
              </Callout>
            )}
            {staleGoals.length > 0 && (
              <Callout tone="warning" icon={<CircleAlert className="h-4 w-4" />}>
                <div className="text-[13px] font-semibold text-ink">有完成依据已变化，需要重新验收</div>
                <div className="mt-1 text-[12px] leading-relaxed text-ink-2">历史证明仍保留；请核对变化事实后决定是否重新打开目标。</div>
                <div className="mt-1.5 flex flex-wrap gap-x-3 gap-y-1">
                  {staleGoals.map((g) => (
                    <button
                      key={g.id}
                      type="button"
                      className="rounded text-[12px] text-primary underline-offset-2 transition-colors hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                      // One navigation, one history entry: switching the view and
                      // then assigning the hash would push twice, so Back once
                      // would keep the graph with the selection cleared. The
                      // shell subscribes to navigationEvent, so this both changes
                      // the page and selects the goal node.
                      onClick={() => navigate(`${window.location.pathname}?view=graph#${encodeURIComponent(`g:${g.id}`)}`)}
                    >
                      {g.statement} · 查看依据
                    </button>
                  ))}
                </div>
              </Callout>
            )}
            {board?.reviews && (board.reviews.pending > 0 || board.reviews.processing > 0 || board.reviews.paused) && (
              <Callout tone={board.reviews.paused ? 'warning' : 'info'}>
                <span className="text-[12px] leading-relaxed">
                  证据核验：<span className="font-mono tw-nums">{board.reviews.processing}</span> 项处理中，
                  <span className="font-mono tw-nums">{board.reviews.pending}</span> 项待处理
                  {board.reviews.cooling > 0 ? `（${board.reviews.cooling} 项等待重试）` : ''}。
                  {board.reviews.paused ? '复核已暂停，可在结论页恢复。' : board.reviews.automatic ? '队列会自动继续推进。' : '自动核验未开启，可在结论页发起复核。'}
                </span>
              </Callout>
            )}
            {dataError && (
              <Callout tone="warning" icon={<CircleAlert className="h-4 w-4" />}>
                部分数据读取失败，当前保留上次成功结果：<span className="tw-break">{dataError}</span>
              </Callout>
            )}
            {needHuman.length > 0 && (
              <button
                type="button"
                onClick={() => onNavigate?.('access')}
                className="group flex w-full items-center gap-3 rounded-xl border border-warning/25 bg-warning/[0.06] px-4 py-3 text-left transition-colors hover:border-warning/40 hover:bg-warning/[0.1] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-offset-2 focus-visible:ring-offset-background"
              >
                <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-warning/25 bg-warning/10 text-warning">
                  <CircleDashed className="h-4 w-4" />
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block text-[13px] font-semibold text-ink">{needHuman.length} 项操作等待你处理</span>
                  <span className="mt-0.5 block truncate text-[12px] text-ink-2" title={needHuman[0].request.request}>
                    {needHuman[0].request.request}
                  </span>
                </span>
                <span className="shrink-0 text-[12px] font-medium text-warning">前往交接 →</span>
              </button>
            )}
          </div>
        )}

        {!board ? (
          // Shape the loading state like the page that replaces it: the metrics
          // strip, then the foreground split, then the collapsed diagnostics.
          <div className="space-y-5" aria-busy="true">
            <SkeletonCard />
            <div className="grid grid-cols-1 gap-4 xl:grid-cols-12">
              <SkeletonCard className="xl:col-span-4" />
              <SkeletonCard className="xl:col-span-4" />
              <SkeletonCard className="xl:col-span-4" />
              <SkeletonCard className="xl:col-span-7" />
              <SkeletonCard className="xl:col-span-5" />
            </div>
          </div>
        ) : fresh ? (
          <div className="min-h-0 flex-1">
            <Onboarding
              project={project}
              scope={project?.scope ?? ''}
              bound={bound}
              defaultNode={defaultNode}
              deciderPaused={Boolean(decider?.paused)}
              canStart={canStart}
              starting={starting}
              onStart={startRound}
              onNewGoal={() => setNewGoal(true)}
              onNavigate={onNavigate}
            />
          </div>
        ) : (
          <>
            {/* Metrics are split by what they ask of the operator: the four
                action counters lead at the 26px metric size, the ledger totals
                follow at the tile size. Not every number deserves one weight. */}
            <KeyMetrics board={board} project={project} runs={runList} />

            {/* The console foreground (docs/05 §图与主工作界面, docs/35 §6):
                current goal, progress, blockers/waiting, latest evidence and the
                one dispatch box. Delivery/model/host diagnostics follow in the
                collapsed 诊断 expander instead of competing for this row. */}
            <div className="grid shrink-0 grid-cols-1 gap-4 xl:grid-cols-12">
              <GoalsPanel
                goals={openGoals}
                onNew={() => setNewGoal(true)}
                onPick={(g) => {
                  setMetGoal(g)
                  setEvidenceID('')
                  setMetNote('')
                  setError('')
                }}
                onOpenNode={onOpenNode}
                className="h-[300px] xl:col-span-4"
              />
              <CommanderPanel
                board={board}
                decisions={decisions.data ?? []}
                memory={memory.data?.state ?? ''}
                onNavigate={onNavigate}
                className="h-[300px] xl:col-span-4"
              />
              {/* One dispatch box: the same shared ChatComposer as the graph dock
                  and the 通信 page (one implementation, one directive service). */}
              <Panel
                title={
                  <span className="flex items-center gap-1.5">
                    <Send className="h-3.5 w-3.5 text-ink-3" /> 下发
                  </span>
                }
                extra={<span className="text-[11px] text-ink-3">一次性提示 / 持续指令 / 换向</span>}
                className="h-[300px] min-h-0 xl:col-span-4"
                bodyClassName="min-h-0 flex-1 overflow-y-auto p-0"
              >
                <ChatComposer paused={paused} runningExecutors={board.runs.running} onSent={refreshAll} />
              </Panel>
              <FrontierPanel
                items={board.frontier ?? []}
                waiting={board.waiting_intents ?? []}
                onOpen={() => onNavigate?.('graph')}
                onOpenNode={onOpenNode}
                className="h-[300px] xl:col-span-7"
              />
              <FactsPanel
                items={board.recent_facts ?? []}
                onOpen={() => onNavigate?.('graph')}
                onOpenNode={onOpenNode}
                className="h-[300px] xl:col-span-5"
              />
              <KnowledgePanel
                books={playbooks.data ?? []}
                stats={board.playbooks}
                onOpen={() => onNavigate?.('playbooks')}
                className="h-[240px] xl:col-span-12"
              />
            </div>

            {/* Reference material, not the console's foreground: what the next
                round will be told, delivery/handoff warnings, per-run/model/host
                details and the guard's sweep. Collapsed unless the operator asks. */}
            <details className="tw-panel shrink-0 rounded-xl [&[open]>summary>svg]:rotate-180">
              <summary className="flex cursor-pointer list-none items-center gap-2 px-4 py-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 [&::-webkit-details-marker]:hidden">
                <ScrollText className="h-4 w-4 shrink-0 text-ink-3" />
                <span className="tw-section-title">运行与投递诊断</span>
                <span className="hidden text-[11px] text-ink-3 sm:inline">交接告警 · 模型 / 宿主 / 投递积压</span>
                <ChevronDown className="ml-auto h-4 w-4 shrink-0 text-ink-3 transition-transform" />
              </summary>
              <div className="space-y-4 border-t border-hairline p-4">
                <BriefingBar board={board} />
                {(board.handoff_warnings?.length ?? 0) > 0 && (
                  <Callout tone="warning" icon={<CircleAlert className="h-4 w-4" />}>
                    <div className="space-y-1">
                      <div className="text-[12.5px] font-medium text-ink">
                        最近 <span className="font-mono tw-nums">{board.handoff_warnings!.length}</span> 个历史 run 有非致命告警（已带入下一轮提示词）
                      </div>
                      {board.handoff_warnings!.slice(0, 3).map((w) => (
                        <div key={w.run} className="tw-break text-[11px] leading-relaxed text-ink-2">
                          <span className="font-mono">{w.run}</span>
                          {w.intent_id ? ` · ${w.intent_id}` : ''}：{w.warnings[0] ?? ''}
                          {w.warnings.length > 1 ? `（共 ${w.warnings.length} 条）` : ''}
                        </div>
                      ))}
                      {board.handoff_warnings!.length > 3 && (
                        <div className="text-[11px] text-ink-3">…更多见「运行」页每条 run 的告警列表</div>
                      )}
                    </div>
                  </Callout>
                )}
                <ExecutorPanel
                  board={board}
                  runs={runList}
                  bound={bound}
                  defaultNode={defaultNode}
                  onNavigate={onNavigate}
                  className="h-auto"
                />
                <GuardPanel board={board} runs={runList} onNavigate={onNavigate} className="h-auto" />
              </div>
            </details>
          </>
        )}
      </div>

      <CreateGoalDialog open={newGoal} onOpenChange={setNewGoal} onCreated={refreshAll} />
      <NewProjectDialog open={newProject} onOpenChange={setNewProject} onCreated={refreshAll} />

      <Dialog open={!!metGoal} onOpenChange={(o) => !o && closeMet()}>
        <DialogContent className="max-w-md">
          <DialogHeader>
            <DialogTitle>标记达成</DialogTitle>
            <DialogDescription>
              达成必须挂一条真实存在的证据，并写一句为什么这条证据证明了目标；事实意图图会拒绝空证据、空理由或不存在的 id。
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="tw-inset tw-break rounded-lg px-3 py-2 text-[12.5px] leading-relaxed text-ink-2">
              {metGoal?.statement}
            </div>
            {evidence.error ? (
              <Callout tone="destructive">证据读取失败：<span className="tw-break">{evidence.error}</span></Callout>
            ) : (evidence.loading || evidence.refreshing) && (evidence.data ?? []).length === 0 ? (
              <div className="flex items-center gap-2 rounded-lg border border-hairline bg-surface-2/50 px-3 py-2.5 text-[12px] text-ink-2">
                <Spinner size="sm" />
                正在读取证据…
              </div>
            ) : (evidence.data ?? []).length === 0 ? (
              <Callout tone="warning">还没有证据。先跑一轮，等执行者产出原始文件。</Callout>
            ) : (
              <Field label="证据">
                <Select value={evidenceID} onValueChange={setEvidenceID}>
                  <SelectTrigger className="h-9 text-[12px]">
                    <SelectValue placeholder="选一条证据" />
                  </SelectTrigger>
                  <SelectContent>
                    {(evidence.data ?? []).map((e) => (
                      <SelectItem key={e.id} value={e.id} className="text-[12px]">
                        <span className="font-mono text-[11px] text-ink-3">{e.id}</span>
                        <span className="ml-2">{e.name}</span>
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
            )}
            <Field label="为什么达成" hint="必填：一句话说清这条证据怎样证明目标已达成">
              <Textarea
                rows={2}
                value={metNote}
                onChange={(e) => setMetNote(e.target.value)}
                placeholder="例如：/admin 未授权访问已复现，原始响应里带管理员会话 cookie"
              />
            </Field>
            {error && <Callout tone="destructive">{error}</Callout>}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={closeMet} disabled={busy}>
              取消
            </Button>
            <Button
              onClick={markMet}
              disabled={busy || (evidence.data ?? []).length === 0 || !evidenceID || !metNote.trim()}
            >
              {busy ? (
                <>
                  <Spinner size="sm" className="mr-1.5" />
                  提交中…
                </>
              ) : (
                '确认达成'
              )}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Page>
  )
}

// ---- header + metric primitives -------------------------------------------

function MetaDivider() {
  return <span aria-hidden="true" className="h-3 w-px shrink-0 bg-hairline" />
}

/** The live phase, kept at 11px so the header's meta line stays quiet. */
function LiveMeta({ board }: { board?: BoardResponse }) {
  const phase = phaseOf(board?.decider)
  const live = isPhaseActive(phase)
  const label = live ? PHASE_LABEL[phase] ?? phase : phase === 'paused' ? '已暂停' : phase === 'blocked' ? '未就绪' : '空闲'
  return (
    <span className="inline-flex items-center gap-1.5 text-[11px] text-ink-3">
      <span
        className={cn(
          'h-1.5 w-1.5 shrink-0 rounded-full',
          live
            ? 'animate-pulse bg-info'
            : phase === 'paused'
              ? 'bg-warning'
              : phase === 'blocked'
                ? 'bg-caution'
                : 'bg-ink-3/50',
        )}
      />
      实时 · {label}
    </span>
  )
}

/**
 * Metrics in two weights: the first group is what the operator may need to act
 * on (failed runs, directions waiting to be dispatched, blocked premises,
 * running workers) at the 26px metric size; the second is the quiet ledger.
 */
function KeyMetrics({ board, project, runs }: { board: BoardResponse; project?: ProjectInfo; runs: Run[] }) {
  const failed = board.runs.failed
  const pending = Math.max(0, board.intents.pending - (board.intents.held ?? 0))
  const blocked = (board.frontier ?? []).filter((f) => !f.ready).length
  const waiting = board.intents.waiting ?? 0
  const limit = project?.max_runs_effective
  // board.runs.canceled counts every cause (operator pause, retry, watchdog); the
  // hint names the watchdog recoveries, so it must use that cause, not the total.
  const watchdogCanceled = runs.filter((r) => r.cancel_cause === 'watchdog').length
  return (
    <section className="tw-panel shrink-0 rounded-xl" aria-label="关键指标">
      <header className="flex flex-wrap items-center gap-2 border-b border-hairline px-4 py-3">
        <div className="tw-section-title">关键指标</div>
        <div className="ml-auto text-[11px] text-ink-3">需要行动的一组在前，账本累计在后</div>
      </header>
      <div className="grid gap-5 p-4 sm:p-5 xl:grid-cols-[minmax(0,1.5fr)_minmax(0,1fr)] xl:gap-0">
        <div className="min-w-0">
          <div className="tw-eyebrow">现在需要行动</div>
          <div className="mt-3 grid grid-cols-2 gap-x-5 gap-y-5">
            <StatTile
              size="lg"
              label="失败运行"
              value={fmtInt(failed)}
              tone={failed > 0 ? 'destructive' : 'muted'}
              hint={failed > 0 ? '去运行页处理' : '没有失败的 run'}
            />
            <StatTile
              size="lg"
              label="待派方向"
              value={fmtInt(pending)}
              tone={pending > 0 ? 'warning' : 'muted'}
              hint={pending > 0 ? '指挥官将派发' : '没有排队方向'}
            />
            <StatTile
              size="lg"
              label="被挡方向"
              value={fmtInt(blocked)}
              tone={blocked > 0 ? 'caution' : 'muted'}
              hint={blocked > 0 ? '前提不成立' : '没有前提阻塞'}
            />
            <StatTile
              size="lg"
              label="在跑执行"
              value={`${fmtInt(board.runs.running)}/${limit ? fmtInt(limit) : '∞'}`}
              tone={board.runs.running > 0 ? 'primary' : 'muted'}
              hint="当前 / 并发上限"
            />
          </div>
        </div>
        <div className="min-w-0 border-t border-hairline pt-5 xl:border-l xl:border-t-0 xl:pl-6 xl:pt-0">
          <div className="tw-eyebrow">账本累计</div>
          <div className="mt-3 grid grid-cols-2 gap-x-5 gap-y-4 sm:grid-cols-3">
            <StatTile
              label="意图 · 在跑/总"
              value={`${fmtInt(board.intents.running)}/${fmtInt(board.intents.total)}`}
              tone={board.intents.running > 0 ? 'primary' : 'default'}
              hint={`完成 ${fmtInt(board.intents.done)}`}
            />
            <StatTile
              label="发现 / 证据"
              value={`${fmtInt(board.facts.total)} / ${fmtInt(board.evidence)}`}
              hint={`带证据 ${fmtInt(board.facts.with_evidence)}`}
            />
            <StatTile
              label="目标 · 达成/总"
              value={`${fmtInt(board.goals.met)}/${fmtInt(board.goals.total)}`}
              tone={board.goals.met > 0 ? 'success' : 'default'}
              hint={board.goals.met >= board.goals.total && board.goals.total > 0 ? '全部达成' : '未全部达成'}
            />
            <StatTile
              label="已复核发现"
              value={fmtInt(board.facts.verified)}
              tone={board.facts.verified > 0 ? 'success' : 'muted'}
              hint="通过复核"
            />
            <StatTile
              label="等待中方向"
              value={fmtInt(waiting)}
              tone={waiting > 0 ? 'warning' : 'muted'}
              hint="已让出槽位"
            />
            <StatTile
              label="取消运行"
              value={fmtInt(board.runs.canceled)}
              tone={board.runs.canceled > 0 ? 'caution' : 'muted'}
              hint={`守卫回收 ${fmtInt(watchdogCanceled)}`}
            />
          </div>
        </div>
      </div>
    </section>
  )
}

// ---- role panels ---------------------------------------------------------

/** A panel-header title: 13px/600 (via .tw-section-title), quiet glyph. */
function PanelTitle({ icon: Icon, children, glyphClass }: { icon: typeof Bot; children: React.ReactNode; glyphClass?: string }) {
  return (
    <span className="flex items-center gap-1.5">
      <Icon className={cn('h-3.5 w-3.5 text-ink-3', glyphClass)} />
      {children}
    </span>
  )
}

function CommanderPanel({
  board,
  decisions,
  memory,
  onNavigate,
  className,
}: {
  board: BoardResponse
  decisions: Decision[]
  memory: string
  onNavigate?: (view: string) => void
  className?: string
}) {
  const dec = board.decider
  const phase = phaseOf(dec)
  const active = isPhaseActive(phase)
  const now = useNow(active)
  const since = dec?.activity?.since ? new Date(dec.activity.since).getTime() : 0
  const elapsed = since > 0 ? Math.max(0, Math.round((now - since) / 1000)) : 0
  const detail = phaseDetail(dec)
  const paused = phase === 'paused'
  const blocked = phase === 'blocked'
  const status = paused ? 'warning' : blocked ? 'error' : active ? 'running' : 'idle'
  const dot = active ? 'animate-pulse bg-info' : paused ? 'bg-warning' : blocked ? 'bg-caution' : 'bg-ink-3/50'
  const actions = dec?.last_actions ?? []
  const notes = dec?.last_notes ?? []
  // The log reads oldest → newest; the panel wants the latest decision first.
  const recent = decisions.slice(-3).reverse()

  return (
    <Panel
      title={<PanelTitle icon={Bot} glyphClass="text-ai">指挥官</PanelTitle>}
      extra={<StatusIndicator compact status={status} label={PHASE_LABEL[phase] ?? phase} />}
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-3"
    >
      {!dec?.enabled ? (
        <EmptyState
          compact
          icon={Bot}
          title="指挥官未启用"
          description="没有配置模型端点 / Key，决策循环不会跑；执行者仍可手动派发。"
          action={
            <Button size="xs" variant="outline" onClick={() => window.location.assign('/g/?view=settings')}>
              去设置
            </Button>
          }
        />
      ) : (
        <>
          <div className="flex items-center gap-2">
            <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', dot)} />
            <span className="min-w-0 flex-1 truncate text-[13px] font-medium leading-5 text-ink" title={detail}>
              {detail}
            </span>
            {active && since > 0 && (
              <span className="shrink-0 font-mono text-[11px] tw-nums text-ink-3">{elapsed}s</span>
            )}
          </div>
          <Meter
            value={active ? phaseProgress(elapsed) : 0}
            max={100}
            tone={blocked || paused ? 'warning' : 'info'}
            className="mt-2 h-1"
            aria-label="指挥官本轮进度（不会到 100：轮次结束由下一状态重置）"
          />

          <div className="mt-3 grid grid-cols-2 gap-x-4 gap-y-2.5">
            <Row label="模型" value={dec.model || '—'} />
            <Row label="轮次" value={fmtInt(dec.rounds)} />
            <Row label="决策 tokens" value={fmtInt(dec.tokens_total)} />
            {/* 单小时 token 已下线（docs/34 §3）：这里只留决策轮自身的累计。 */}
            <Row
              label="会话"
              value={
                dec.session_rounds
                  ? `${fmtInt(dec.session_rounds)}${dec.session_dropped ? `（丢 ${dec.session_dropped}）` : ''}`
                  : '—'
              }
            />
          </div>

          {((dec.pending_attention ?? []).length > 0 || (dec.last_attention ?? []).length > 0) && (
            <div className="tw-inset mt-3 rounded-lg px-2.5 py-2" data-commander-attention>
              <div className="text-[11px] font-medium text-ink-3">
                {dec.pending_attention?.length ? '等待全局判断' : '上次为何启动'}
              </div>
              {(dec.pending_attention?.length ? dec.pending_attention : dec.last_attention)!.slice(0, 3).map((reason, i) => (
                <div key={`${reason.id}-${i}`} className="mt-1 text-[12px] leading-relaxed text-ink-2">
                  {reason.detail}
                </div>
              ))}
              {dec.pending_attention?.length ? (
                <div className="mt-1.5 text-[11px] leading-relaxed text-ink-3">
                  暂停或调用失败时仍保留；局部结果继续由其承接者处理。
                </div>
              ) : null}
            </div>
          )}

          {actions.length > 0 && (
            <SubSection label="上一轮动作">
              <div className="flex flex-wrap gap-1.5">
                {actions.slice(0, 5).map((a, i) => (
                  <Badge
                    key={`${a.kind}-${i}`}
                    size="sm"
                    variant={a.kind === 'intent' ? 'primary' : a.kind === 'error' ? 'danger' : 'muted'}
                    className="max-w-full"
                    title={a.text || a.id}
                  >
                    {a.kind}
                    {a.text ? ` · ${a.text.slice(0, 24)}` : a.id ? ` · ${a.id.slice(0, 8)}` : ''}
                  </Badge>
                ))}
              </div>
            </SubSection>
          )}

          {notes.length > 0 && (
            <div className="mt-2 space-y-0.5 text-[12px] leading-relaxed text-ink-3">
              {notes.slice(0, 2).map((n, i) => (
                <div key={i} className="truncate" title={n}>
                  · {n}
                </div>
              ))}
            </div>
          )}

          {decisions.length > 0 && (
            <SubSection
              label="最近决策"
              extra={
                <button
                  type="button"
                  className="rounded px-1 text-[11px] font-medium text-primary transition-colors hover:text-primary/80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                  onClick={() => onNavigate?.('decisions')}
                >
                  全部
                </button>
              }
            >
              <div className="divide-y divide-hairline/70">
                {recent.map((d) => (
                  <div key={d.round} className="flex items-center gap-2 py-1.5 text-[11px]">
                    <span className="w-8 shrink-0 font-mono tw-nums text-ink-3">#{d.round}</span>
                    <Badge size="sm" variant="muted" className="shrink-0">
                      {d.wake}
                    </Badge>
                    <span className="min-w-0 flex-1 truncate text-ink-2">
                      {(d.actions ?? []).map((a) => a.kind).join(' · ') || (d.fallback ? `降级：${d.fallback}` : '—')}
                    </span>
                    <span className="shrink-0 font-mono tw-nums text-ink-3">{fmtInt(d.tokens)}</span>
                  </div>
                ))}
              </div>
            </SubSection>
          )}

          {memory.trim() && (
            <SubSection label="state.md">
              <pre className="tw-inset tw-break max-h-24 overflow-y-auto whitespace-pre-wrap rounded-lg px-2.5 py-2 font-mono text-[11px] leading-relaxed text-ink-2">
                {memory.trim().slice(0, 400)}
              </pre>
            </SubSection>
          )}

          {dec.last_error && (
            <Callout tone="destructive" className="mt-3">
              <span className="tw-break text-[12px]">上一轮出错：{dec.last_error}</span>
            </Callout>
          )}

          <div className="mt-3 flex items-center gap-1.5 border-t border-hairline pt-3">
            <Button size="xs" variant="ghost" onClick={() => onNavigate?.('decisions')}>
              决策日志
            </Button>
          </div>
        </>
      )}
    </Panel>
  )
}

function ExecutorPanel({
  board,
  runs,
  bound,
  defaultNode,
  onNavigate,
  className,
}: {
  board: BoardResponse
  runs: Run[]
  bound: { id: string; transport?: string; endpoint?: string; bin?: string }[]
  defaultNode: string
  onNavigate?: (view: string) => void
  className?: string
}) {
  const running = board.running_intents ?? []
  const recent = useMemo(
    () => runs.slice().sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at)).slice(0, 5),
    [runs],
  )

  return (
    <Panel
      title={<PanelTitle icon={Terminal}>执行者</PanelTitle>}
      extra={
        <div className="flex items-center gap-1.5">
          <Badge size="sm" variant={running.length > 0 ? 'primary' : 'muted'}>
            {running.length} 在跑
          </Badge>
          {board.intents.pending > 0 && (
            <Badge size="sm" variant="warning">
              {board.intents.pending} 排队
            </Badge>
          )}
        </div>
      }
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-3"
    >
      {running.length === 0 ? (
        // When there is no run at all, the single empty state lives under
        // 最近运行 (below); this slot only explains an idle executor while the
        // recent-runs table still has history to show.
        recent.length === 0 ? null : (
          <EmptyState
            compact
            icon={Activity}
            title="没有在跑的 run"
            description="指挥官派发意图后，这里显示每个 run 的脉搏与上下文压力。"
            action={
              <Button size="xs" variant="outline" onClick={() => onNavigate?.('runs')}>
                查看运行
              </Button>
            }
          />
        )
      ) : (
        <div className="space-y-2">
          {running.map((it) => (
            <RunningRow key={it.id} item={it} />
          ))}
        </div>
      )}

      <SubSection
        label="最近运行"
        extra={
          <button
            type="button"
            className="rounded px-1 text-[11px] font-medium text-primary transition-colors hover:text-primary/80 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
            onClick={() => onNavigate?.('runs')}
          >
            全部
          </button>
        }
      >
        {recent.length === 0 ? (
          <EmptyState
            compact
            icon={Activity}
            title="还没有运行记录"
            description="派发第一条方向后，这里显示最近 5 条 run 的状态、节点与 token。"
            action={
              <Button size="xs" variant="outline" onClick={() => onNavigate?.('runs')}>
                查看运行
              </Button>
            }
          />
        ) : (
          <table className="w-full text-left text-[11px]">
            <thead className="text-ink-3">
              <tr>
                <th className="py-1 pr-2 font-medium">run</th>
                <th className="py-1 pr-2 font-medium">状态</th>
                <th className="hidden py-1 pr-2 font-medium sm:table-cell">节点</th>
                <th className="py-1 text-right font-medium">tok</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-hairline/70">
              {recent.map((r) => (
                <tr key={r.id} className="tw-hover-row">
                  <td className="py-1.5 pr-2 font-mono tw-nums text-ink-3" title={r.id}>
                    {r.id.slice(-6)}
                    <span className="ml-1 text-ink-3/70">
                      {r.purpose === 'verifier' ? '复' : r.purpose === 'report' ? '报' : ''}
                    </span>
                  </td>
                  <td className="py-1.5 pr-2">
                    <StatusIndicator compact status={statusKind(r.status)} label={runStatusLabel(r)} />
                  </td>
                  <td className="hidden py-1.5 pr-2 font-mono text-ink-3 sm:table-cell">{r.node || '—'}</td>
                  <td className="py-1.5 text-right font-mono tw-nums text-ink-2">{r.tokens ? fmtInt(r.tokens) : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </SubSection>

      <div className="mt-3 flex flex-wrap items-center gap-1.5 border-t border-hairline pt-3">
        <Badge size="sm" variant="success" className="font-mono tw-nums">
          完成 {fmtInt(board.runs.done)}
        </Badge>
        <Badge size="sm" variant="danger" className="font-mono tw-nums">
          失败 {fmtInt(board.runs.failed)}
        </Badge>
        <Badge size="sm" variant="muted" className="font-mono tw-nums">
          取消 {fmtInt(board.runs.canceled)}
        </Badge>
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-1.5 border-t border-hairline pt-3">
        <span className="text-[11px] text-ink-3">节点</span>
        {bound.length === 0 ? (
          <Badge size="sm" variant="warning">
            未绑定
          </Badge>
        ) : (
          bound.map((n) => (
            <Badge
              key={n.id}
              size="sm"
              variant={n.id === defaultNode ? 'primary' : 'secondary'}
              className="font-mono"
              title={n.transport === 'aop' ? n.endpoint : n.bin}
            >
              {n.id}
              {n.id === defaultNode ? ' · 首选' : ''}
            </Badge>
          ))
        )}
        <Button size="xs" variant="ghost" className="ml-auto" onClick={() => onNavigate?.('nodes')}>
          <Server className="h-3.5 w-3.5" /> 管理
        </Button>
      </div>
    </Panel>
  )
}

function RunningRow({ item }: { item: BoardItem }) {
  const idle = item.pulse?.idle_seconds ?? 0
  const events = item.pulse?.events ?? 0
  const ctx = item.pulse?.context_tokens ?? 0
  const win = item.pulse?.context_window ?? 0
  const hot = idle >= QUIET_SECONDS
  const pressure = win > 0 ? ctx / win : 0
  return (
    <div className="tw-inset rounded-lg px-2.5 py-2">
      <div className="flex items-center gap-2">
        <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', hot ? 'bg-warning' : 'animate-pulse bg-info')} />
        <span className="min-w-0 flex-1 truncate text-[12px] text-ink" title={item.title}>
          {item.title}
        </span>
        <span className={cn('shrink-0 font-mono text-[11px] tw-nums', hot ? 'text-warning' : 'text-ink-3')}>
          {fmtIdle(idle)} · {fmtInt(events)} ev
        </span>
        {(item.pulse?.tool_failures ?? 0) > 0 && (
          <Badge size="sm" variant="warning" className="font-mono tw-nums" title="本轮结构化失败工具结果的累计数；不代表任务失败">
            工具失败 {fmtInt(item.pulse?.tool_failures)}
          </Badge>
        )}
      </div>
      {win > 0 && (
        <div className="mt-1.5 flex items-center gap-2">
          <Meter
            value={ctx}
            max={win}
            tone={pressure > 0.9 ? 'destructive' : pressure > 0.75 ? 'warning' : 'info'}
            className="h-1"
            aria-label="这一轮的上下文压力"
          />
          <span className="shrink-0 font-mono text-[11px] tw-nums text-ink-3">{Math.round(pressure * 100)}%</span>
        </div>
      )}
    </div>
  )
}

function GuardPanel({
  board,
  runs,
  onNavigate,
  className,
}: {
  board: BoardResponse
  runs: Run[]
  onNavigate?: (view: string) => void
  className?: string
}) {
  const now = useNow(true)
  const dec = board.decider
  const guard = board.guard
  const interval = guard?.interval_seconds ?? 30
  const lastTick = guard?.last_tick_at ? Math.max(0, Math.round((now - new Date(guard.last_tick_at).getTime()) / 1000)) : null
  const freshTick = lastTick !== null && lastTick <= interval * 3
  const loopStatus = !dec?.enabled ? 'error' : dec?.paused ? 'warning' : 'success'
  const loopLabel = !dec?.enabled ? '未启用' : dec?.paused ? '已暂停' : '运行中'
  const lastRoundAt = dec?.last_round_at
    ? Math.max(0, Math.round((now - new Date(dec.last_round_at).getTime()) / 1000))
    : null
  const quiet = (board.running_intents ?? []).filter((it) => (it.pulse?.idle_seconds ?? 0) >= QUIET_SECONDS)
  const watchdogRuns = runs.filter((r) => r.cancel_cause === 'watchdog')
  const recoveries = watchdogRuns.length
  const recovered = watchdogRuns
    .sort((a, b) => Date.parse(b.started_at) - Date.parse(a.started_at))
    .slice(0, 3)
  const failed = runs.filter((r) => r.status === 'failed').length
  const stalled = dec?.stalled ?? 0

  return (
    <Panel
      title={<PanelTitle icon={ShieldCheck}>看门狗</PanelTitle>}
      extra={
        <div className="flex items-center gap-1.5">
          <StatusIndicator compact status={loopStatus} label={loopLabel} />
          <StatusIndicator compact status={freshTick ? 'success' : 'idle'} label={freshTick ? '巡检中' : '待巡检'} />
        </div>
      }
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-3"
    >
      <div className="tw-eyebrow">指挥循环</div>
      <div className="mt-2 grid grid-cols-2 gap-x-4 gap-y-2.5 sm:grid-cols-3">
        <Row label="循环" value={loopLabel} />
        <Row label="心跳" value={dec?.heartbeat_seconds ? fmtSeconds(dec.heartbeat_seconds) : '—'} />
        <Row label="上次决策" value={lastRoundAt === null ? '—' : `${fmtIdle(lastRoundAt)} 前`} />
        <Row label="上次唤醒" value={dec?.last_wake || '—'} />
        <Row label="已运行" value={fmtUptime(dec?.uptime_seconds)} />
        <Row label="轮次 / 无新事实" value={`${fmtInt(dec?.rounds ?? 0)} / ${fmtInt(stalled)}`} />
      </div>

      <div className="mt-3 border-t border-hairline pt-3">
        <div className="tw-eyebrow">巡检 · 回收</div>
        <div className="mt-2 grid grid-cols-2 gap-x-4 gap-y-2.5 sm:grid-cols-3">
          <Row label="巡检周期" value={`${interval}s`} />
          <Row label="静默阈值" value={guard?.idle_timeout_seconds ? fmtSeconds(guard.idle_timeout_seconds) : '关闭'} />
          <Row label="上次巡检" value={lastTick === null ? '—' : `${lastTick}s 前`} />
          <Row label="守卫回收" value={fmtInt(recoveries)} />
          <Row label="失败运行" value={fmtInt(failed)} />
          <Row label="静默 >5m" value={fmtInt(quiet.length)} />
        </div>
      </div>

      {quiet.length > 0 && (
        <Callout tone="warning" className="mt-3" icon={<CircleAlert className="h-4 w-4" />}>
          <div className="text-[12px] font-semibold text-ink">有 run 长时间没有事件</div>
          <div className="mt-1 space-y-0.5">
            {quiet.slice(0, 2).map((it) => (
              <div key={it.id} className="truncate text-[11px]">
                {it.title} · 静默 {fmtIdle(it.pulse?.idle_seconds ?? 0)}
              </div>
            ))}
          </div>
        </Callout>
      )}

      {recovered.length > 0 && (
        <SubSection label="最近回收">
          <div className="divide-y divide-hairline/70">
            {recovered.map((r) => (
              <div key={r.id} className="flex items-center gap-2 py-1.5 text-[11px]">
                <span className="shrink-0 font-mono tw-nums text-ink-3">{r.id.slice(-6)}</span>
                <span className="min-w-0 flex-1 truncate text-ink-2" title={r.exit_reason}>
                  {r.exit_reason || '守卫回收'}
                </span>
              </div>
            ))}
          </div>
        </SubSection>
      )}

      <p className="mt-3 text-[11px] leading-relaxed text-ink-3">
        心跳/唤醒让指挥官持续跑（只有唤醒才调模型）；巡检只做回收，不调模型。
      </p>
      <div className="mt-2">
        <Button size="xs" variant="ghost" onClick={() => onNavigate?.('runs')}>
          查看运行
        </Button>
      </div>
    </Panel>
  )
}

// ---- ledger panels -------------------------------------------------------

function GoalsPanel({
  goals,
  onNew,
  onPick,
  onOpenNode,
  className,
}: {
  goals: Goal[]
  onNew: () => void
  onPick: (g: Goal) => void
  /** Cross-page link: a sealed goal's cited facts become graph nodes. */
  onOpenNode?: (graphNodeID: string) => void
  className?: string
}) {
  return (
    <Panel
      title={<PanelTitle icon={Flag}>目标</PanelTitle>}
      extra={
        <Button size="xs" variant="ghost" onClick={onNew}>
          <Plus className="h-3.5 w-3.5" /> 新建
        </Button>
      }
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-0"
    >
      {goals.length === 0 ? (
        <EmptyState
          compact
          icon={Flag}
          title="还没有目标"
          description="目标由你定义：先写起点和终点；也可以先开跑，指挥官按简报推进，等你补充终点。达成必须挂真实证据。"
          action={
            <Button size="xs" variant="outline" onClick={onNew}>
              新建目标
            </Button>
          }
        />
      ) : (
        <ul className="divide-y divide-hairline/70">
          {goals.map((g) => (
            <li key={g.id} className="tw-hover-row px-3 py-3">
              <div className="flex items-start gap-2.5">
                <span className="mt-0.5 shrink-0">
                  {g.met ? (
                    <CircleCheck className="h-4 w-4 text-success" />
                  ) : (
                    <CircleDashed className="h-4 w-4 text-ink-3" />
                  )}
                </span>
                <div className="min-w-0 flex-1">
                  <div className="line-clamp-2 text-[12.5px] leading-relaxed text-ink">{g.statement}</div>
                  {g.start && (
                    <div className="mt-1 line-clamp-2 text-[11px] leading-relaxed text-ink-3" title={g.start}>起点：{g.start}</div>
                  )}
                  <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                    <StatusIndicator compact status={g.met ? 'success' : 'idle'} label={g.met ? '已达成' : '未达成'} />
                    <span className="tw-break font-mono text-[11px] text-ink-3">{g.id}</span>
                  </div>
                  {/* A sealed goal carries its own argument: why, on which facts,
                      and when. Old seals predate those fields, say so instead of
                      drawing a blank. */}
                  {g.met && (
                    <div className="mt-1.5 space-y-1">
                      {g.note ? (
                        <div className="line-clamp-2 text-[12px] leading-relaxed text-ink-2" title={g.note}>
                          为什么达成：{g.note}
                        </div>
                      ) : (
                        <div className="text-[11px] italic text-ink-3">盖章时未记录理由（旧数据）</div>
                      )}
                      <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-ink-3">
                        <span>依据</span>
                        {g.from && g.from.length > 0 ? (
                          // 依据是事实 id：给了跨页回调就变成图谱入口（f:<id>），
                          // 没给时保持原来的纯文本（可选 prop 不改变既有页面）。
                          g.from.map((fid) =>
                            onOpenNode ? (
                              <button
                                key={fid}
                                type="button"
                                data-testid="overview-goal-fact"
                                onClick={() => onOpenNode(`f:${fid}`)}
                                className="rounded border border-hairline bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] leading-4 text-ink-2 transition-colors hover:border-primary/40 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                                title={`在图谱中打开事实 ${fid}`}
                              >
                                {fid}
                              </button>
                            ) : (
                              <span
                                key={fid}
                                className="rounded border border-hairline bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] leading-4 text-ink-2"
                              >
                                {fid}
                              </span>
                            ),
                          )
                        ) : g.evidence_id ? (
                          <span className="font-mono text-[11px]" title="旧数据：只记录了证据 id">
                            证据 {g.evidence_id}
                          </span>
                        ) : (
                          <span>未记录</span>
                        )}
                      </div>
                      {g.completed_at && (
                        <div className="text-[11px] text-ink-3">
                          达成于 <span className="font-mono tw-nums">{fmtTime(g.completed_at)}</span>
                        </div>
                      )}
                      {/* F9 follow-through: the seal is historical and is not
                          rewritten when a cited fact changes; the server computes
                          the staleness, the UI reports it and never un-sets met. */}
                      {g.receipt_stale && (
                        <div
                          data-testid="overview-goal-receipt-stale"
                          className="mt-1.5 rounded-lg border border-warning/25 bg-warning/[0.07] px-2.5 py-2 text-[12px] leading-relaxed text-ink-2"
                          title="目标仍记为已达成；历史完成证明保留，需要重新判断依据是否还成立"
                        >
                          <div className="font-medium text-warning">收据过时：所依据事实已更新，需重新判断（历史完成证明保留）</div>
                          {(g.receipt_changed ?? []).length > 0 && (
                            <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                              <span className="text-ink-3">变更事实</span>
                              {g.receipt_changed!.map((fid) =>
                                onOpenNode ? (
                                  <button
                                    key={fid}
                                    type="button"
                                    onClick={() => onOpenNode(`f:${fid}`)}
                                    className="rounded border border-warning/30 bg-card/60 px-1.5 py-0.5 font-mono text-[11px] leading-4 text-ink-2 transition-colors hover:border-warning/60 hover:text-warning focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
                                    title={`在图谱中打开事实 ${fid}`}
                                  >
                                    {fid}
                                  </button>
                                ) : (
                                  <span key={fid} className="font-mono text-[11px]">
                                    {fid}
                                  </span>
                                ),
                              )}
                            </div>
                          )}
                        </div>
                      )}
                    </div>
                  )}
                </div>
                {!g.met && (
                  <Button size="xs" variant="soft" className="shrink-0" onClick={() => onPick(g)}>
                    达成
                  </Button>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </Panel>
  )
}

function FrontierPanel({
  items,
  waiting = [],
  onOpen,
  onOpenNode,
  className,
}: {
  items: NonNullable<BoardResponse['frontier']>
  /** Directions whose turn yielded (children/operator/external action): the
   *  board reports them separately from pending work, and the console must show
   *  them as "等待中" instead of letting them look like missing work. */
  waiting?: BoardItem[]
  onOpen: () => void
  /** Cross-page link: a frontier row selects its step node (`s:<intent id>`). */
  onOpenNode?: (graphNodeID: string) => void
  className?: string
}) {
  const blocked = items.filter((f) => !f.ready).length
  return (
    <Panel
      title={
        <span className="flex items-center gap-1.5">
          <Target className="h-3.5 w-3.5 text-ink-3" /> 待办方向
          {blocked > 0 && (
            <Badge size="sm" variant="warning" title="前提不成立或已被否定的方向">
              {blocked} 被挡
            </Badge>
          )}
          {waiting.length > 0 && (
            <Badge size="sm" variant="secondary" title="等待子任务 / 操作员 / 外部动作的方向">
              {waiting.length} 等待中
            </Badge>
          )}
        </span>
      }
      extra={
        <Button size="xs" variant="ghost" onClick={onOpen}>
          图谱
        </Button>
      }
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-0"
    >
      {waiting.length > 0 && (
        <div className="border-b border-hairline px-3 py-2.5" data-testid="overview-waiting-intents">
          <div className="tw-eyebrow">等待中（不是卡住：已让出槽位）</div>
          <ul className="mt-1.5 space-y-1">
            {waiting.map((item) => {
              const label = (
                <div className="flex min-w-0 items-center gap-2">
                  <span className="min-w-0 flex-1 truncate text-[12px] text-ink" title={item.title}>
                    {item.title}
                  </span>
                  {item.status && <span className="shrink-0 text-[11px] text-ink-3">{statusLabel(item.status)}</span>}
                </div>
              )
              return (
                <li key={item.id}>
                  {onOpenNode ? (
                    <ListRow
                      onClick={() => onOpenNode(`s:${item.id}`)}
                      title="在图谱中选中这条方向"
                      leading={<StatusIndicator compact status="warning" label="等待" className="shrink-0" />}
                      className="items-center rounded-lg px-2 py-1.5"
                    >
                      {label}
                    </ListRow>
                  ) : (
                    <div className="flex items-center gap-2 px-2 py-1.5">
                      <StatusIndicator compact status="warning" label="等待" className="shrink-0" />
                      {label}
                    </div>
                  )}
                </li>
              )
            })}
          </ul>
        </div>
      )}
      {items.length === 0 ? (
        waiting.length === 0 ? (
          <EmptyState
            compact
            icon={Target}
            title="没有待办方向"
            description="指挥官派发或你新建意图后，这里显示每个方向的前提是否成立。"
            action={
              <Button size="xs" variant="outline" onClick={onOpen}>
                去图谱
              </Button>
            }
          />
        ) : null
      ) : (
        <ul className="divide-y divide-hairline/70">
          {items.map((f) => {
            // Markers are display-only: a row from an older board simply has no
            // new fields, so nothing is invented for it.
            const markers = frontierMarkers(f)
            const changedText = frontierPremiseChangedText(f)
            const body = (
              <div className="min-w-0">
                <div className="truncate text-[12.5px] text-ink">{f.title}</div>
                {markers.length > 0 && (
                  <div className="mt-1 flex flex-wrap items-center gap-1">
                    {markers.map((marker) => (
                      <Badge key={marker.key} size="sm" variant={marker.tone} title={marker.detail}>
                        {marker.label}
                      </Badge>
                    ))}
                  </div>
                )}
                <div className="mt-0.5 flex flex-wrap gap-x-2 gap-y-0.5 text-[11px] text-ink-3">
                  {f.last_stop && <span>上轮 {f.last_stop}</span>}
                  <span>声明 {f.last_declared ?? 0}</span>
                  {changedText && <span className="text-warning">{changedText}</span>}
                  {f.blocked_on?.length ? <span className="text-caution">前提 {f.blocked_on.join('、')}</span> : null}
                </div>
              </div>
            )
            const gate = (
              <StatusIndicator
                compact
                status={f.ready ? 'success' : 'error'}
                label={f.ready ? '可派' : '被挡'}
                className="mt-0.5 shrink-0"
              />
            )
            return (
              <li key={f.id}>
                {onOpenNode ? (
                  <ListRow
                    data-testid="overview-frontier-link"
                    onClick={() => onOpenNode(`s:${f.id}`)}
                    title="在图谱中选中这条方向"
                    leading={gate}
                    className="items-start rounded-none px-3 py-2.5 [&>span]:block"
                  >
                    {body}
                  </ListRow>
                ) : (
                  <div className="flex items-start gap-3 px-3 py-2.5">
                    {gate}
                    {body}
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </Panel>
  )
}

function FactsPanel({
  items,
  onOpen,
  onOpenNode,
  className,
}: {
  items: BoardItem[]
  onOpen: () => void
  /** Cross-page link: a recent fact opens as its graph node (`f:<id>`). */
  onOpenNode?: (graphNodeID: string) => void
  className?: string
}) {
  return (
    <Panel
      title={<PanelTitle icon={Network}>最近事实</PanelTitle>}
      extra={
        <Button size="xs" variant="ghost" onClick={onOpen}>
          图谱
        </Button>
      }
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-0"
    >
      {items.length === 0 ? (
        <EmptyState
          compact
          icon={Network}
          title="还没有事实"
          description="执行者按交接契约写下结论后，事实意图图会把它们列在这里。"
          action={
            <Button size="xs" variant="outline" onClick={onOpen}>
              去图谱
            </Button>
          }
        />
      ) : (
        <ul className="divide-y divide-hairline/70">
          {items.map((f) => {
            const body = (
              <div className="min-w-0">
                <div className="truncate text-[12.5px] text-ink">{f.title}</div>
                <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-ink-3">
                  <span className="tw-break font-mono">{f.id}</span>
                  <span>证据 {f.evidence ?? 0}</span>
                  {f.origin && (
                    <Badge variant={originVariant(f.origin)} size="sm">
                      {f.origin}
                    </Badge>
                  )}
                  {f.source_ref?.startsWith('ioa:') && (
                    <Badge
                      variant="outline"
                      size="sm"
                      title="来自 harness IOA 记录：子会话在账本外，没有 usage、工具事件与独立 run"
                    >
                      IOA · 账本外
                    </Badge>
                  )}
                  <StatusIndicator compact status={statusKind(f.status)} label={statusLabel(f.status)} />
                </div>
              </div>
            )
            return (
              <li key={f.id}>
                {onOpenNode ? (
                  <ListRow
                    data-testid="overview-fact-link"
                    onClick={() => onOpenNode(`f:${f.id}`)}
                    title="在图谱中打开这条事实"
                    leading={
                      <Badge variant={kindVariant(f.kind)} size="sm" className="mt-0.5 shrink-0">
                        {f.kind}
                      </Badge>
                    }
                    className="items-start rounded-none px-3 py-2.5 [&>span]:block"
                  >
                    {body}
                  </ListRow>
                ) : (
                  <div className="flex items-start gap-3 px-3 py-2.5">
                    <Badge variant={kindVariant(f.kind)} size="sm" className="mt-0.5 shrink-0">
                      {f.kind}
                    </Badge>
                    {body}
                  </div>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </Panel>
  )
}

function KnowledgePanel({
  books,
  stats,
  onOpen,
  className,
}: {
  books: Playbook[]
  stats?: BoardResponse['playbooks']
  onOpen: () => void
  className?: string
}) {
  const confirmed = stats?.confirmed ?? books.filter((b) => b.status === 'confirmed').length
  const candidates = stats?.candidates ?? books.filter((b) => b.status === 'candidate').length
  const retired = stats?.retired ?? books.filter((b) => b.status === 'retired').length
  const top = useMemo(() => {
    const rank = (s: string) => (s === 'confirmed' ? 0 : s === 'candidate' ? 1 : 2)
    return books.slice().sort((a, b) => rank(a.status) - rank(b.status)).slice(0, 3)
  }, [books])

  return (
    <Panel
      title={<PanelTitle icon={BookMarked}>战场知识</PanelTitle>}
      extra={
        <Button size="xs" variant="ghost" onClick={onOpen}>
          打开
        </Button>
      }
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-4"
    >
      <div className="grid grid-cols-3 gap-4">
        <StatTile label="已确认" value={fmtInt(confirmed)} tone={confirmed > 0 ? 'success' : 'muted'} />
        <StatTile label="候选" value={fmtInt(candidates)} tone={candidates > 0 ? 'warning' : 'muted'} />
        <StatTile label="已退役" value={fmtInt(retired)} tone="muted" />
      </div>

      {top.length === 0 ? (
        <EmptyState
          compact
          icon={BookMarked}
          title="还没有打法记录"
          description="执行者跑过一轮后声明打法；指挥官只能提名候选。项目私有知识，不进报告。"
          action={
            <Button size="xs" variant="outline" onClick={onOpen}>
              打开战场知识
            </Button>
          }
        />
      ) : (
        <ul className="mt-3 divide-y divide-hairline/70">
          {top.map((p) => (
            <li key={p.id} className="tw-hover-row py-2">
              <div className="flex items-center gap-2">
                <span className="min-w-0 flex-1 truncate text-[12px] text-ink" title={p.title}>
                  {p.title}
                </span>
                <Badge
                  size="sm"
                  variant={p.status === 'confirmed' ? 'success' : p.status === 'candidate' ? 'warning' : 'muted'}
                >
                  {p.status === 'confirmed' ? '确认' : p.status === 'candidate' ? '候选' : '退役'}
                </Badge>
              </div>
              <div className="mt-0.5 line-clamp-1 text-[11px] text-ink-3">{p.when}</div>
            </li>
          ))}
        </ul>
      )}
      <p className="mt-3 text-[11px] leading-relaxed text-ink-3">
        确认来自跑过的那一轮；候选没有执行证据，派发时只作线索。
        {(stats?.archived ?? 0) > 0 && ` 已归档 ${stats?.archived} 条，可在战场知识中查看。`}
      </p>
    </Panel>
  )
}

// ---- briefing (inside the diagnostics expander) ---------------------------

/** The board as one sentence, plus the only number the decider actually reads:
 *  the digest. The panels below show the ledger; this strip shows the briefing —
 *  what the next round will be told, and whether there will be a next round at
 *  all. `digest` is the same value the heartbeat compares with `last_digest`, so
 *  "same" means the next wake is skipped, not that nothing is happening.
 *
 *  It reports, it does not judge: no same-family call, no coverage arithmetic
 *  beyond "a host has no direction that names it". Those stay with the commander
 *  (D54/D59). */
function BriefingBar({ board }: { board: BoardResponse }) {
  const [open, setOpen] = useState(false)
  const dec = board.decider
  const digest = board.digest ?? ''
  const last = dec?.last_digest ?? ''
  const same = Boolean(digest && last && digest === last)

  const held = board.intents.held ?? 0
  const pending = Math.max(0, board.intents.pending - held)
  const running = board.running_intents ?? []
  const quiet = running.filter((it) => (it.pulse?.idle_seconds ?? 0) >= QUIET_SECONDS)
  const blocked = (board.frontier ?? []).filter((f) => !f.ready)
  const ports = board.ports ?? []
  const uncovered = (board.coverage ?? []).filter((c) => !c.open && !c.running)

  const parts = [
    pending > 0 ? `${fmtInt(pending)} 个方向待派` : '没有待派方向',
    board.intents.running > 0
      ? `${fmtInt(board.intents.running)} 个在跑${quiet.length ? `（${quiet.length} 个静默 >5m）` : ''}`
      : '没有在跑的 run',
    blocked.length ? `${blocked.length} 个前提被挡` : null,
    (board.intents.waiting ?? 0) > 0 ? `${board.intents.waiting} 个方向等待中` : null,
    held ? `${held} 个已停放` : null,
    ports.length ? `${ports.length} 组端口` : null,
    uncovered.length ? `${uncovered.length} 台主机没有方向覆盖` : null,
  ].filter(Boolean)

  const verdict = !dec?.enabled
    ? '指挥官未启用：摘要只作观测'
    : !last
      ? '还没有决策轮'
      : same
        ? '与上次决策一致：唤醒会被跳过'
        : '有变化：下一次唤醒会跑一轮'

  return (
    <div className="tw-inset shrink-0 rounded-lg">
      <div className="flex flex-wrap items-center gap-2 px-3 py-2">
        <span className="flex items-center gap-1.5 text-[12px] font-medium text-ink">
          <ScrollText className="h-3.5 w-3.5 text-ink-3" /> 作战简报
        </span>
        <Badge
          size="sm"
          variant={!dec?.enabled ? 'muted' : same ? 'muted' : 'warning'}
          className="font-mono"
          title={`本轮 ${digest || '—'}${last ? ` · 上次决策 ${last}` : ''}`}
        >
          摘要 {digest ? digest.slice(0, 8) : '—'}
        </Badge>
        <span className="text-[11px] text-ink-3">{verdict}</span>
        <Button size="xs" variant="ghost" className="ml-auto" onClick={() => setOpen((v) => !v)}>
          {open ? <ChevronUp className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
          {open ? '收起' : '展开'}
        </Button>
      </div>

      <div className="tw-break px-3 pb-2.5 text-[12px] leading-relaxed text-ink-2">{parts.join(' · ')}</div>

      {open && (
        <div className="grid gap-x-5 gap-y-3 border-t border-hairline px-3 py-3 text-[11px] xl:grid-cols-2">
          <BriefGroup label="前提被挡" empty="没有方向卡在前提上">
            {blocked.map((f) => (
              <div key={f.id} className="truncate" title={f.title}>
                {f.title}
                <span className="ml-1 text-caution">（{(f.blocked_on ?? []).join('、') || '—'}）</span>
              </div>
            ))}
          </BriefGroup>
          <BriefGroup label="端口观测" empty="还没有 service 事实">
            {ports.map((p) => (
              <div key={p.port} className="truncate font-mono tw-nums">
                {p.port} × {p.hosts.length}
              </div>
            ))}
          </BriefGroup>
          <BriefGroup label="未覆盖主机" empty="每一台已知主机都有方向">
            {uncovered.slice(0, 8).map((c) => (
              <div key={c.host} className="truncate font-mono">
                {c.host}
                {c.services ? <span className="ml-1 text-ink-3">{c.services} 服务</span> : null}
              </div>
            ))}
            {uncovered.length > 8 && <div className="text-ink-3">还有 {uncovered.length - 8} 台…</div>}
          </BriefGroup>
        </div>
      )}
    </div>
  )
}

function BriefGroup({
  label,
  empty,
  children,
}: {
  label: string
  empty: string
  children: React.ReactNode
}) {
  // Children arrive as one node, an array from a map, or an array of those when
  // a group has a second expression (the "还有 N 台…" line). Flatten one level
  // so a group with no rows still gets its empty state instead of rendering [].
  const items = (Array.isArray(children) ? children.flat(1) : [children]).filter(Boolean)
  return (
    <div className="min-w-0">
      <div className="tw-eyebrow mb-1">{label}</div>
      {items.length === 0 ? <div className="text-ink-3">{empty}</div> : items}
    </div>
  )
}

// ---- onboarding -----------------------------------------------------------

function Onboarding({
  project,
  scope,
  bound,
  defaultNode,
  deciderPaused,
  canStart,
  starting,
  onStart,
  onNewGoal,
  onNavigate,
}: {
  project?: ProjectInfo
  scope: string
  bound: { id: string; transport?: string }[]
  defaultNode: string
  deciderPaused: boolean
  canStart: boolean
  starting: boolean
  onStart: () => void
  onNewGoal: () => void
  onNavigate?: (view: string) => void
}) {
  return (
    <Panel title="从这里开始" extra={<span className="text-[11px] text-ink-3">三步进入作战状态</span>}>
      <div className="grid gap-6 md:grid-cols-3">
        <div className="min-w-0 space-y-2.5">
          <div className="flex items-center gap-2 text-[13px] font-semibold text-ink">
            <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary/12 font-mono text-[11px] tw-nums text-primary">1</span>
            写项目简报
          </div>
          <p className="text-[12px] leading-relaxed text-ink-2">
            目标与起点、已知资产、时间窗口与禁止动作。简报和内置约束会原样进入每次派发和决策：测试中新增或移除的资产，直接用「图谱」右侧下发窗口补充，不必回来改这里。
          </p>
          <div className="tw-inset tw-break rounded-lg px-2.5 py-2 font-mono text-[11px] leading-relaxed text-ink-3">
            {scope || '未指定'}
          </div>
          <Button size="xs" variant="ghost" onClick={() => window.location.assign('/g/?view=projects')}>
            去改项目简报
          </Button>
        </div>
        <div className="min-w-0 space-y-2.5">
          <div className="flex items-center gap-2 text-[13px] font-semibold text-ink">
            <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary/12 font-mono text-[11px] tw-nums text-primary">2</span>
            目标（可先不定）
          </div>
          <p className="text-[12px] leading-relaxed text-ink-2">
            可以先不写目标：开跑后指挥官会按项目简报推进；目标由你在新建项目或「新建目标」里定义（起点 + 终点），达成必须挂真实证据。
          </p>
          <Button size="xs" variant="outline" onClick={onNewGoal}>
            <Flag className="h-3.5 w-3.5" /> 新建目标
          </Button>
        </div>
        <div className="min-w-0 space-y-2.5">
          <div className="flex items-center gap-2 text-[13px] font-semibold text-ink">
            <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary/12 font-mono text-[11px] tw-nums text-primary">3</span>
            本机执行
          </div>
          <p className="text-[12px] leading-relaxed text-ink-2">
            单机实例只有一个执行位置：控制台启动的本机 aiscan web。项目自动绑定它，不需要人工选节点。
          </p>
          <div className="flex flex-wrap gap-1">
            {bound.length ? (
              bound.map((n) => (
                <Badge key={n.id} variant={n.id === defaultNode ? 'primary' : 'secondary'} className="font-mono">
                  {n.id}
                  {n.id === defaultNode ? ' · 首选' : ''}
                </Badge>
              ))
            ) : (
              <Badge variant="warning">还没有绑定本机节点</Badge>
            )}
          </div>
          <Button size="xs" variant="ghost" onClick={() => onNavigate?.('nodes')}>
            查看执行宿主
          </Button>
        </div>
      </div>
      <div className="mt-4 flex flex-wrap items-center gap-2 border-t border-hairline pt-4 text-[12px] text-ink-2">
        <Play className="h-3.5 w-3.5 text-ink-3" />
        写清项目简报（起点）、绑好本机节点就能开跑；终点由你在新建项目或「新建目标」里定义。
        <Button
          size="sm"
          className="ml-auto"
          onClick={onStart}
          disabled={!canStart || starting || deciderPaused}
          title={
            !canStart
              ? '还没有绑定本机执行节点：先到「执行」页确认本机宿主正在运行'
              : deciderPaused
                ? '项目已暂停：先点顶部「继续项目」'
                : undefined
          }
        >
          {starting ? (
            <>
              <Spinner size="sm" className="mr-1.5" />
              已排队…
            </>
          ) : (
            '开始运行'
          )}
        </Button>
      </div>
      <div className="mt-2 text-[11px] text-ink-3">
        项目：{project?.name ?? '—'} · ID <span className="tw-break font-mono">{project?.id ?? '—'}</span>
      </div>
    </Panel>
  )
}

// ---- helpers --------------------------------------------------------------

/** One-second clock; the page re-renders only while mounted. */
function useNow(active = true): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [active])
  return now
}

/** A labelled mono value: 11px label, 12px tabular number/ID. */
function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="text-[11px] leading-4 text-ink-3">{label}</div>
      <div
        className="truncate font-mono text-[12px] leading-5 tw-nums text-ink-2"
        title={typeof value === 'string' ? value : undefined}
      >
        {value}
      </div>
    </div>
  )
}

/** A quiet section inside a panel: 11px label + hairline, so detail hierarchies
 *  read as sections rather than as nested cards. */
function SubSection({
  label,
  extra,
  children,
}: {
  label: string
  extra?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className="mt-3 border-t border-hairline pt-3">
      <div className="mb-2 flex items-center gap-2">
        <span className="text-[11px] font-medium text-ink-3">{label}</span>
        {extra && <span className="ml-auto">{extra}</span>}
      </div>
      {children}
    </div>
  )
}

function fmtIdle(seconds: number): string {
  if (seconds < 60) return `${seconds}s`
  const m = Math.floor(seconds / 60)
  if (m < 60) return `${m}m${String(seconds % 60).padStart(2, '0')}s`
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}m`
}

function fmtSeconds(seconds: number): string {
  if (seconds >= 3600) return `${Math.round(seconds / 3600)}h`
  if (seconds >= 60) return `${Math.round(seconds / 60)}m`
  return `${seconds}s`
}
