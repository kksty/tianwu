import { useEffect, useMemo, useRef, useState } from 'react'
import {
  Activity,
  Bot,
  BookMarked,
  ChevronDown,
  ChevronUp,
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
  Meter,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  SkeletonList,
  StatTile,
  StatusIndicator,
  Textarea,
} from '@cyber/ui'
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
import type { BoardItem, BoardResponse, Decision, Evidence, Goal, Playbook, ProjectInfo, Run } from '@/lib/types'

// A run quiet this long is worth surfacing on the dashboard. It is not the
// guard's reap threshold (that is --idle-timeout, shown in the panel): this is
// the "looks stuck" line, far earlier than the destructive one.
const QUIET_SECONDS = 300

/** Prioritize the active work, goals and handoffs; supporting panels follow
 * in a scrolling workspace rather than competing for the viewport height. */
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

  return (
    <Page
      title="概览"
      subtitle={
        <>
          {project?.name ?? current}
          <span className="mx-1.5 text-muted-foreground/50">·</span>
          <span className="font-mono text-[11px]">{current}</span>
        </>
      }
      actions={
        <>
          <Button size="sm" variant="ghost" onClick={refreshAll} title="重新拉取事实意图图">
            <RefreshCw className="h-3.5 w-3.5" />
          </Button>
          <Button size="sm" variant="outline" className="hidden sm:inline-flex" onClick={() => setNewProject(true)}>
            <FolderKanban className="h-3.5 w-3.5" /> 新建项目
          </Button>
          <Button size="sm" variant="outline" onClick={() => setNewGoal(true)}>
            <Flag className="h-3.5 w-3.5" /> 新建目标
          </Button>
        </>
      }
      scroll={false}
    >
      <div className="mx-auto flex h-full min-h-0 w-full max-w-[1680px] flex-col gap-5 overflow-y-auto p-4 sm:p-6 lg:p-7">
        <div
          className="shrink-0 truncate text-[11px] text-muted-foreground"
          title={project?.scope || '未指定'}
        >
          {/* F12.1: a one-shot message may supplement or narrow the work, but it
              does not silently revoke the effective persistent instructions; the
              project authorization scope is the hard boundary. */}
          项目简报（当前口径，可动态补充）：{project?.scope || '未指定——按操作员当前有效指令与项目授权口径执行，执行者不自行扩大目标'}
        </div>

        {startNote && <Callout tone="info" className="shrink-0">{startNote}</Callout>}
        {goals.data?.some((g) => g.receipt_stale) && (
          <Callout tone="warning" className="shrink-0">
            <div className="font-medium">有完成依据已变化，需要重新验收</div>
            <div className="mt-1 text-xs">历史证明仍保留；请核对变化事实后决定是否重新打开目标。</div>
            {goals.data.filter((g) => g.receipt_stale).map((g) => <button key={g.id} className="mt-1 mr-3 text-xs underline" onClick={() => { onNavigate?.('graph'); window.location.hash=encodeURIComponent(`g:${g.id}`) }}>{g.statement} · 查看依据</button>)}
          </Callout>
        )}
        {board?.reviews && (board.reviews.pending > 0 || board.reviews.processing > 0 || board.reviews.paused) && (
          <Callout tone={board.reviews.paused ? 'warning' : 'info'} className="shrink-0">
            <div className="text-xs">证据核验：{board.reviews.processing} 项处理中，{board.reviews.pending} 项待处理{board.reviews.cooling > 0 ? `（${board.reviews.cooling} 项等待重试）` : ''}。{board.reviews.paused ? '复核已暂停，可在结论页恢复。' : board.reviews.automatic ? '队列会自动继续推进。' : '自动核验未开启，可在结论页发起复核。'}</div>
          </Callout>
        )}
        {(goals.error || runs.error || nodes.error || playbooks.error || decisions.error || memory.error || operator.error) && <Callout tone="warning">部分数据读取失败，当前保留上次成功结果：{goals.error || runs.error || nodes.error || playbooks.error || decisions.error || memory.error || operator.error}</Callout>}
        {needHuman.length > 0 && <button type="button" onClick={() => onNavigate?.('access')} className="flex shrink-0 items-center gap-4 rounded-xl border border-amber-500/30 bg-amber-500/[0.06] px-5 py-4 text-left transition-colors hover:bg-amber-500/10">
          <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-amber-500/10 text-amber-500"><CircleDashed className="h-5 w-5" /></div>
          <div className="min-w-0 flex-1"><div className="text-sm font-semibold">{needHuman.length} 项操作等待你处理</div><div className="mt-1 truncate text-xs text-muted-foreground">{needHuman[0].request.request}</div></div>
          <span className="shrink-0 text-xs text-amber-500">前往交接 →</span>
        </button>}

        {!board ? (
          <SkeletonList count={4} />
        ) : fresh ? (
          <div className="min-h-0 flex-1 overflow-y-auto">
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
            {/* Ledger counters + the one concurrency number the operator acts
                on. The token strip that used to live below is gone (docs/34 §3):
                project totals now live on the graph page header via
                ProjectTokens; this dashboard does not repeat them (and no
                single-hour token anywhere). */}
            <div className="grid shrink-0 grid-cols-2 gap-6 rounded-xl border border-border/80 bg-card p-5 [&_.font-mono]:text-2xl [&_.font-mono]:font-medium sm:grid-cols-3 sm:p-6 lg:grid-cols-5">
              <StatTile
                label="意图 · 在跑/总"
                value={`${fmtInt(board.intents.running)}/${fmtInt(board.intents.total)}`}
                tone={board.intents.running > 0 ? 'primary' : 'default'}
                icon={<Target className="h-3 w-3" />}
              />
              <div><StatTile label="发现 / 证据" value={`${fmtInt(board.facts.total)} / ${fmtInt(board.evidence)}`} icon={<Network className="h-3 w-3" />} /></div>
              <StatTile label="目标 · 达成/总" value={`${fmtInt(board.goals.met)}/${fmtInt(board.goals.total)}`} icon={<Flag className="h-3 w-3" />} />
              <StatTile label="已复核发现" value={fmtInt(verified)} tone={verified > 0 ? 'success' : 'muted'} icon={<ShieldCheck className="h-3 w-3" />} />
              <div title="本项目当前在跑 run 数 / 项目并发上限（实例默认或项目覆盖）；不会改变账本">
                <StatTile
                  label="worker 在跑/上限"
                  value={`${fmtInt(board.runs.running)}/${project?.max_runs_effective ? fmtInt(project.max_runs_effective) : '∞'}`}
                  tone={board.runs.running > 0 ? 'primary' : 'muted'}
                  icon={<Server className="h-3 w-3" />}
                />
              </div>
            </div>

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
              <div className="flex h-[300px] min-h-0 flex-col overflow-hidden rounded-xl border border-border/80 bg-card xl:col-span-4">
                <div className="flex shrink-0 items-center gap-1.5 border-b border-border/70 px-3 py-2">
                  <Send className="h-3.5 w-3.5 text-muted-foreground" />
                  <span className="text-[12px] font-medium text-foreground">下发</span>
                  <span className="ml-auto truncate text-[10px] text-muted-foreground">
                    一次性提示 / 持续指令 / 换向
                  </span>
                </div>
                <div className="min-h-0 flex-1 overflow-y-auto">
                  <ChatComposer paused={paused} runningExecutors={board.runs.running} onSent={refreshAll} />
                </div>
              </div>
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
            <details className="shrink-0 rounded-xl border border-border bg-card p-4">
              <summary className="cursor-pointer text-xs font-medium text-muted-foreground">
                运行与投递诊断（交接告警 · 模型 / 宿主 / 投递积压）
              </summary>
              <div className="mt-3 space-y-3">
                <BriefingBar board={board} />
                {(board.handoff_warnings?.length ?? 0) > 0 && (
                  <Callout tone="warning">
                    <div className="space-y-0.5">
                      <div className="font-medium">
                        ⚠ 最近 {board.handoff_warnings!.length} 个 run 有交接告警（非致命，已带入下一轮提示词）
                      </div>
                      {board.handoff_warnings!.slice(0, 3).map((w) => (
                        <div key={w.run} className="tw-break text-[11px] leading-relaxed opacity-90">
                          <span className="font-mono">{w.run}</span>
                          {w.intent_id ? ` · ${w.intent_id}` : ''}：{w.warnings[0] ?? ''}
                          {w.warnings.length > 1 ? `（共 ${w.warnings.length} 条）` : ''}
                        </div>
                      ))}
                      {board.handoff_warnings!.length > 3 && (
                        <div className="text-[11px] opacity-80">…更多见「运行」页每条 run 的告警列表</div>
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
                  className="h-[320px]"
                />
                <GuardPanel board={board} runs={runList} onNavigate={onNavigate} className="h-[280px]" />
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
            <div className="rounded border border-border bg-muted/40 px-2 py-1.5 text-[12px] text-foreground">
              {metGoal?.statement}
            </div>
            {evidence.error ? <Callout tone="destructive">证据读取失败：{evidence.error}</Callout> : (evidence.loading || evidence.refreshing) && (evidence.data ?? []).length === 0 ? (
              <Callout tone="info">正在读取证据…</Callout>
            ) : (evidence.data ?? []).length === 0 ? (
              <Callout tone="warning">还没有证据。先跑一轮，等执行者产出原始文件。</Callout>
            ) : (
              <Field label="证据">
                <Select value={evidenceID} onValueChange={setEvidenceID}>
                  <SelectTrigger className="h-9 text-xs">
                    <SelectValue placeholder="选一条证据" />
                  </SelectTrigger>
                  <SelectContent>
                    {(evidence.data ?? []).map((e) => (
                      <SelectItem key={e.id} value={e.id} className="text-xs">
                        <span className="font-mono text-[11px]">{e.id}</span>
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
            {error && <div className="text-[12px] text-destructive">{error}</div>}
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={closeMet}>
              取消
            </Button>
            <Button
              onClick={markMet}
              disabled={busy || (evidence.data ?? []).length === 0 || !evidenceID || !metNote.trim()}
            >
              {busy ? '提交中…' : '确认达成'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Page>
  )
}

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
    <div className="shrink-0 rounded-lg border border-border bg-card">
      <div className="flex flex-wrap items-center gap-2 px-3 py-1.5">
        <span className="flex items-center gap-1.5 text-[12px] font-medium text-foreground">
          <ScrollText className="h-3.5 w-3.5 text-primary" /> 作战简报
        </span>
        <Badge
          size="sm"
          variant={!dec?.enabled ? 'muted' : same ? 'muted' : 'warning'}
          className="font-mono"
          title={`本轮 ${digest || '—'}${last ? ` · 上次决策 ${last}` : ''}`}
        >
          摘要 {digest ? digest.slice(0, 8) : '—'}
        </Badge>
        <span className="text-[11px] text-muted-foreground">{verdict}</span>
        <Button size="xs" variant="ghost" className="ml-auto" onClick={() => setOpen((v) => !v)}>
          {open ? <ChevronUp className="h-3 w-3" /> : <ChevronDown className="h-3 w-3" />}
          {open ? '收起' : '展开'}
        </Button>
      </div>

      <div className="tw-break px-3 pb-1.5 text-[12px] leading-relaxed text-foreground/80">
        {parts.join(' · ')}
      </div>

      {open && (
        <div className="grid gap-x-4 gap-y-2 border-t border-border px-3 py-2 text-[11px] xl:grid-cols-2">
          <BriefGroup label="前提被挡" empty="没有方向卡在前提上">
            {blocked.map((f) => (
              <div key={f.id} className="truncate" title={f.title}>
                {f.title}
                <span className="ml-1 text-destructive">（{(f.blocked_on ?? []).join('、') || '—'}）</span>
              </div>
            ))}
          </BriefGroup>
          <BriefGroup label="端口观测" empty="还没有 service 事实">
            {ports.map((p) => (
              <div key={p.port} className="truncate font-mono">
                {p.port} × {p.hosts.length}
              </div>
            ))}
          </BriefGroup>
          <BriefGroup label="未覆盖主机" empty="每一台已知主机都有方向">
            {uncovered.slice(0, 8).map((c) => (
              <div key={c.host} className="truncate font-mono">
                {c.host}
                {c.services ? <span className="ml-1 text-muted-foreground/70">{c.services} 服务</span> : null}
              </div>
            ))}
            {uncovered.length > 8 && (
              <div className="text-muted-foreground/70">还有 {uncovered.length - 8} 台…</div>
            )}
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
      <div className="mb-0.5 text-[10px] uppercase tracking-wider text-muted-foreground">{label}</div>
      {items.length === 0 ? <div className="text-muted-foreground/70">{empty}</div> : items}
    </div>
  )
}

// ---- role panels ---------------------------------------------------------
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
  const dot = active ? 'animate-pulse bg-info' : paused ? 'bg-warning' : blocked ? 'bg-caution' : 'bg-muted-foreground'
  const actions = dec?.last_actions ?? []
  const notes = dec?.last_notes ?? []
  // The log reads oldest → newest; the panel wants the latest decision first.
  const recent = decisions.slice(-3).reverse()

  return (
    <Panel
      title={
        <span className="flex items-center gap-1.5">
          <Bot className="h-3.5 w-3.5 text-ai" /> 指挥官
        </span>
      }
      extra={<StatusIndicator compact status={status} label={PHASE_LABEL[phase] ?? phase} />}
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-3"
    >
      {!dec?.enabled ? (
        <Callout tone="warning" className="space-y-2">
          <div className="font-semibold">指挥官未启用</div>
          <div>没有配置模型端点 / Key，决策循环不会跑；执行者仍可手动派发。</div>
          <Button size="xs" variant="outline" onClick={() => window.location.assign('/g/?view=settings')}>
            去设置
          </Button>
        </Callout>
      ) : (
        <>
          <div className="flex items-center gap-2 text-[12px]">
            <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${dot}`} />
            <span className="min-w-0 flex-1 truncate font-medium text-foreground" title={detail}>
              {detail}
            </span>
            {active && since > 0 && (
              <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{elapsed}s</span>
            )}
          </div>
          <Meter
            value={active ? phaseProgress(elapsed) : 0}
            max={100}
            tone={blocked || paused ? 'warning' : 'info'}
            className="mt-1.5 h-1"
            aria-label="指挥官本轮进度（不会到 100：轮次结束由下一状态重置）"
          />

          <div className="mt-2.5 grid grid-cols-2 gap-x-4 gap-y-1.5">
            <Row label="模型" value={dec.model || '—'} />
            <Row label="轮次" value={fmtInt(dec.rounds)} />
            <Row label="决策 tokens" value={fmtInt(dec.tokens_total)} />
            {/* 单小时 token 已下线（docs/34 §3）：这里只留决策轮自身的累计。 */}
            <Row
              label="会话"
              value={dec.session_rounds ? `${fmtInt(dec.session_rounds)}${dec.session_dropped ? `（丢 ${dec.session_dropped}）` : ''}` : '—'}
            />
          </div>

          {((dec.pending_attention ?? []).length > 0 || (dec.last_attention ?? []).length > 0) && (
            <div className="mt-2.5 rounded border border-border/60 px-2 py-1.5 text-[11px]" data-commander-attention>
              <div className="font-medium">{dec.pending_attention?.length ? '等待全局判断' : '上次为何启动'}</div>
              {(dec.pending_attention?.length ? dec.pending_attention : dec.last_attention)!.slice(0, 3).map((reason, i) => (
                <div key={`${reason.id}-${i}`} className="mt-0.5 text-muted-foreground">{reason.detail}</div>
              ))}
              {dec.pending_attention?.length ? <div className="mt-1 text-muted-foreground">暂停或调用失败时仍保留；局部结果继续由其承接者处理。</div> : null}
            </div>
          )}

          {actions.length > 0 && (
            <div className="mt-2.5">
              <div className="mb-1 text-[10px] uppercase tracking-wider text-muted-foreground">上一轮动作</div>
              <div className="flex flex-wrap gap-1">
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
            </div>
          )}

          {notes.length > 0 && (
            <div className="mt-2 text-[11px] leading-relaxed text-muted-foreground">
              {notes.slice(0, 2).map((n, i) => (
                <div key={i} className="truncate" title={n}>
                  · {n}
                </div>
              ))}
            </div>
          )}

          {decisions.length > 0 && (
            <div className="mt-2.5 border-t border-border pt-2">
              <div className="mb-1 flex items-center justify-between text-[10px] uppercase tracking-wider text-muted-foreground">
                <span>最近决策</span>
                <button
                  type="button"
                  className="normal-case text-primary hover:underline"
                  onClick={() => onNavigate?.('decisions')}
                >
                  全部
                </button>
              </div>
              <div className="space-y-0.5">
                {recent.map((d) => (
                  <div key={d.round} className="flex items-center gap-1.5 text-[11px]">
                    <span className="shrink-0 font-mono text-[10px] text-muted-foreground">#{d.round}</span>
                    <Badge size="sm" variant="muted" className="shrink-0">
                      {d.wake}
                    </Badge>
                    <span className="min-w-0 flex-1 truncate text-foreground/80">
                      {(d.actions ?? []).map((a) => a.kind).join(' · ') ||
                        (d.fallback ? `降级：${d.fallback}` : '—')}
                    </span>
                    <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
                      {fmtInt(d.tokens)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}

          {memory.trim() && (
            <div className="mt-2.5 border-t border-border pt-2">
              <div className="mb-1 text-[10px] uppercase tracking-wider text-muted-foreground">state.md</div>
              <pre className="max-h-24 overflow-y-auto whitespace-pre-wrap break-words rounded border border-border bg-muted/30 px-2 py-1 font-mono text-[10.5px] leading-relaxed text-muted-foreground">
                {memory.trim().slice(0, 400)}
              </pre>
            </div>
          )}

          {dec.last_error && (
            <Callout tone="destructive" className="mt-2 text-[11px]">
              上一轮出错：{dec.last_error}
            </Callout>
          )}

          <div className="mt-2.5 flex items-center gap-1.5">
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
      title={
        <span className="flex items-center gap-1.5">
          <Terminal className="h-3.5 w-3.5" /> 执行者
        </span>
      }
      extra={
        <div className="flex items-center gap-1">
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
        <EmptyState
          compact
          icon={Activity}
          title="没有在跑的 run"
          description="指挥官派发意图后，这里显示每个 run 的脉搏与上下文压力。"
        />
      ) : (
        <div className="space-y-1.5">
          {running.map((it) => (
            <RunningRow key={it.id} item={it} />
          ))}
        </div>
      )}

      <div className="mt-3 border-t border-border pt-2">
        <div className="mb-1 flex items-center justify-between text-[10px] uppercase tracking-wider text-muted-foreground">
          <span>最近运行</span>
          <button type="button" className="normal-case text-primary hover:underline" onClick={() => onNavigate?.('runs')}>
            全部
          </button>
        </div>
        {recent.length === 0 ? (
          <div className="py-2 text-[11px] text-muted-foreground">还没有运行。</div>
        ) : (
          <table className="w-full text-left text-[11px]">
            <thead className="text-[10px] uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="py-1 pr-2 font-medium">run</th>
                <th className="py-1 pr-2 font-medium">状态</th>
                <th className="py-1 pr-2 font-medium">节点</th>
                <th className="py-1 text-right font-medium">tok</th>
              </tr>
            </thead>
            <tbody>
              {recent.map((r) => (
                <tr key={r.id} className="border-t border-border/50">
                  <td className="py-1 pr-2 font-mono text-[10px] text-muted-foreground" title={r.id}>
                    {r.id.slice(-6)}
                    <span className="ml-1 text-muted-foreground/60">
                      {r.purpose === 'verifier' ? '复' : r.purpose === 'report' ? '报' : ''}
                    </span>
                  </td>
                  <td className="py-1 pr-2">
                    <StatusIndicator compact status={statusKind(r.status)} label={runStatusLabel(r)} />
                  </td>
                  <td className="py-1 pr-2 font-mono text-[10px]">{r.node || '—'}</td>
                  <td className="py-1 text-right font-mono">{r.tokens ? fmtInt(r.tokens) : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      <div className="mt-2 flex flex-wrap items-center gap-1 border-t border-border pt-2">
        <Badge size="sm" variant="success">
          完成 {fmtInt(board.runs.done)}
        </Badge>
        <Badge size="sm" variant="danger">
          失败 {fmtInt(board.runs.failed)}
        </Badge>
        <Badge size="sm" variant="muted">
          取消 {fmtInt(board.runs.canceled)}
        </Badge>
      </div>

      <div className="mt-2 flex flex-wrap items-center gap-1 border-t border-border pt-2">
        <span className="text-[10px] uppercase tracking-wider text-muted-foreground">节点</span>
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
          <Server className="h-3 w-3" /> 管理
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
    <div className="rounded-md border border-border/70 bg-muted/20 px-2 py-1.5">
      <div className="flex items-center gap-1.5">
        <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${hot ? 'bg-warning' : 'animate-pulse bg-info'}`} />
        <span className="min-w-0 flex-1 truncate text-[12px] text-foreground" title={item.title}>
          {item.title}
        </span>
        <span className={`shrink-0 font-mono text-[10px] ${hot ? 'text-warning' : 'text-muted-foreground'}`}>
          {fmtIdle(idle)} · {fmtInt(events)} ev
        </span>
        {(item.pulse?.tool_failures ?? 0) > 0 && <Badge size="sm" variant="warning" title="本轮结构化失败工具结果的累计数；不代表任务失败">工具失败 {fmtInt(item.pulse?.tool_failures)}</Badge>}
      </div>
      {win > 0 && (
        <div className="mt-1 flex items-center gap-1.5">
          <Meter
            value={ctx}
            max={win}
            tone={pressure > 0.9 ? 'destructive' : pressure > 0.75 ? 'warning' : 'info'}
            className="h-1"
            aria-label="这一轮的上下文压力"
          />
          <span className="shrink-0 font-mono text-[9.5px] text-muted-foreground">
            {Math.round(pressure * 100)}%
          </span>
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
  const fresh = lastTick !== null && lastTick <= interval * 3
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
      title={
        <span className="flex items-center gap-1.5">
          <ShieldCheck className="h-3.5 w-3.5" /> 看门狗
        </span>
      }
      extra={
        <div className="flex items-center gap-1">
          <StatusIndicator compact status={loopStatus} label={loopLabel} />
          <StatusIndicator compact status={fresh ? 'success' : 'idle'} label={fresh ? '巡检中' : '待巡检'} />
        </div>
      }
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-3"
    >
      <div className="mb-1 text-[10px] uppercase tracking-wider text-muted-foreground">指挥循环</div>
      <div className="grid grid-cols-2 gap-x-4 gap-y-1.5">
        <Row label="循环" value={loopLabel} />
        <Row label="心跳" value={dec?.heartbeat_seconds ? fmtSeconds(dec.heartbeat_seconds) : '—'} />
        <Row label="上次决策" value={lastRoundAt === null ? '—' : `${fmtIdle(lastRoundAt)} 前`} />
        <Row label="上次唤醒" value={dec?.last_wake || '—'} />
        <Row label="已运行" value={fmtUptime(dec?.uptime_seconds)} />
        <Row label="轮次 / 无新事实" value={`${fmtInt(dec?.rounds ?? 0)} / ${fmtInt(stalled)}`} />
      </div>

      <div className="mt-2.5 border-t border-border pt-2">
        <div className="mb-1 text-[10px] uppercase tracking-wider text-muted-foreground">巡检 · 回收</div>
        <div className="grid grid-cols-2 gap-x-4 gap-y-1.5">
          <Row label="巡检周期" value={`${interval}s`} />
          <Row label="静默阈值" value={guard?.idle_timeout_seconds ? fmtSeconds(guard.idle_timeout_seconds) : '关闭'} />
          <Row label="上次巡检" value={lastTick === null ? '—' : `${lastTick}s 前`} />
          <Row label="守卫回收" value={fmtInt(recoveries)} />
          <Row label="失败运行" value={fmtInt(failed)} />
          <Row label="静默 >5m" value={fmtInt(quiet.length)} />
        </div>
      </div>

      {quiet.length > 0 && (
        <Callout tone="warning" className="mt-2 text-[11px]">
          <div className="font-semibold">有 run 长时间没有事件</div>
          <div className="mt-0.5 space-y-0.5">
            {quiet.slice(0, 2).map((it) => (
              <div key={it.id} className="truncate">
                {it.title} · 静默 {fmtIdle(it.pulse?.idle_seconds ?? 0)}
              </div>
            ))}
          </div>
        </Callout>
      )}

      {recovered.length > 0 && (
        <div className="mt-2.5 border-t border-border pt-2">
          <div className="mb-1 text-[10px] uppercase tracking-wider text-muted-foreground">最近回收</div>
          <div className="space-y-0.5">
            {recovered.map((r) => (
              <div key={r.id} className="flex items-center gap-1.5 text-[11px]">
                <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{r.id.slice(-6)}</span>
                <span className="min-w-0 flex-1 truncate text-muted-foreground" title={r.exit_reason}>
                  {r.exit_reason || '守卫回收'}
                </span>
              </div>
            ))}
          </div>
        </div>
      )}

      <p className="mt-2 text-[11px] leading-relaxed text-muted-foreground">
        心跳/唤醒让指挥官持续跑（只有唤醒才调模型）；巡检只做回收，不调模型。
      </p>
      <div className="mt-1.5">
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
      title={
        <span className="flex items-center gap-1.5">
          <Flag className="h-3.5 w-3.5" /> 目标
        </span>
      }
      extra={
        <Button size="xs" variant="ghost" onClick={onNew}>
          <Plus className="h-3 w-3" /> 新建
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
          description="可以直接开跑：指挥官会从项目简报里划分目标并写入账本（POST /api/goals）。也可以现在补一句能判真假的话；达成必须挂真实证据。"
          action={
            <Button size="xs" onClick={onNew}>
              新建目标
            </Button>
          }
        />
      ) : (
        <ul className="divide-y divide-border/60">
          {goals.map((g) => (
            <li key={g.id} className="flex items-start gap-2 px-3 py-2">
              <span className="mt-0.5 shrink-0">
                {g.met ? (
                  <CircleCheck className="h-3.5 w-3.5 text-success" />
                ) : (
                  <CircleDashed className="h-3.5 w-3.5 text-muted-foreground" />
                )}
              </span>
              <div className="min-w-0 flex-1">
                <div className="line-clamp-2 text-[12px] text-foreground">{g.statement}</div>
                {g.quantified_target && (
                  <div className="mt-0.5 truncate text-[10.5px] text-muted-foreground">
                    量化：{g.quantified_target}
                  </div>
                )}
                <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[10.5px] text-muted-foreground">
                  <StatusIndicator compact status={g.met ? 'success' : 'idle'} label={g.met ? '已达成' : '未达成'} />
                  <span className="font-mono">{g.id}</span>
                </div>
                {/* A sealed goal carries its own argument: why, on which facts,
                    and when. Old seals predate those fields, say so instead of
                    drawing a blank. */}
                {g.met && (
                  <div className="mt-1 space-y-0.5">
                    {g.note ? (
                      <div className="line-clamp-2 text-[10.5px] leading-relaxed text-foreground/80" title={g.note}>
                        为什么达成：{g.note}
                      </div>
                    ) : (
                      <div className="text-[10.5px] italic text-muted-foreground">
                        盖章时未记录理由（旧数据）
                      </div>
                    )}
                    <div className="flex flex-wrap items-center gap-1 text-[10px] text-muted-foreground">
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
                              className="rounded border border-border/70 bg-muted/40 px-1 font-mono text-[9.5px] leading-4 text-foreground/80 hover:border-primary/40 hover:text-primary focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                              title={`在图谱中打开事实 ${fid}`}
                            >
                              {fid}
                            </button>
                          ) : (
                            <span
                              key={fid}
                              className="rounded border border-border/70 bg-muted/40 px-1 font-mono text-[9.5px] leading-4 text-foreground/80"
                            >
                              {fid}
                            </span>
                          ),
                        )
                      ) : g.evidence_id ? (
                        <span className="font-mono text-[9.5px]" title="旧数据：只记录了证据 id">
                          证据 {g.evidence_id}
                        </span>
                      ) : (
                        <span>未记录</span>
                      )}
                    </div>
                    {g.completed_at && (
                      <div className="text-[10px] text-muted-foreground">达成于 {fmtTime(g.completed_at)}</div>
                    )}
                    {/* F9 follow-through: the seal is historical and is not
                        rewritten when a cited fact changes; the server computes
                        the staleness, the UI reports it and never un-sets met. */}
                    {g.receipt_stale && (
                      <div
                        data-testid="overview-goal-receipt-stale"
                        className="mt-1 rounded border border-warning/40 bg-warning/10 px-1.5 py-1 text-[10.5px] leading-relaxed text-warning"
                        title="目标仍记为已达成；历史完成证明保留，需要重新判断依据是否还成立"
                      >
                        收据过时：所依据事实已更新，需重新判断（历史完成证明保留）
                        {(g.receipt_changed ?? []).length > 0 && (
                          <div className="mt-1 flex flex-wrap items-center gap-1">
                            <span className="text-muted-foreground">变更事实</span>
                            {g.receipt_changed!.map((fid) =>
                              onOpenNode ? (
                                <button
                                  key={fid}
                                  type="button"
                                  onClick={() => onOpenNode(`f:${fid}`)}
                                  className="rounded border border-warning/40 bg-card/60 px-1 font-mono text-[9.5px] leading-4 hover:border-warning/70 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                                  title={`在图谱中打开事实 ${fid}`}
                                >
                                  {fid}
                                </button>
                              ) : (
                                <span key={fid} className="font-mono text-[9.5px]">
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
          <Target className="h-3.5 w-3.5" /> 待办方向
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
        <div className="border-b border-border/60 px-3 py-1.5" data-testid="overview-waiting-intents">
          <div className="text-[10px] uppercase tracking-wider text-muted-foreground">
            等待中（不是卡住：已让出槽位）
          </div>
          <ul className="mt-1 space-y-0.5">
            {waiting.map((item) => {
              const label = (
                <>
                  <StatusIndicator compact status="warning" label="等待" className="shrink-0" />
                  <span className="min-w-0 flex-1 truncate text-[11.5px] text-foreground" title={item.title}>
                    {item.title}
                  </span>
                  {item.status && (
                    <span className="shrink-0 text-[10px] text-muted-foreground">{statusLabel(item.status)}</span>
                  )}
                </>
              )
              return (
                <li key={item.id}>
                  {onOpenNode ? (
                    <button
                      type="button"
                      onClick={() => onOpenNode(`s:${item.id}`)}
                      title="在图谱中选中这条方向"
                      className="flex w-full items-center gap-1.5 rounded px-1 py-0.5 text-left hover:bg-accent focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                    >
                      {label}
                    </button>
                  ) : (
                    <div className="flex items-center gap-1.5 px-1 py-0.5">{label}</div>
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
          />
        ) : null
      ) : (
        <ul className="divide-y divide-border/60">
          {items.map((f) => {
            // Markers are display-only: a row from an older board simply has no
            // new fields, so nothing is invented for it.
            const markers = frontierMarkers(f)
            const changedText = frontierPremiseChangedText(f)
            const content = (
              <>
                <StatusIndicator
                  compact
                  status={f.ready ? 'success' : 'error'}
                  label={f.ready ? '可派' : '被挡'}
                  className="mt-0.5 shrink-0"
                />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[12px] text-foreground">{f.title}</div>
                  {markers.length > 0 && (
                    <div className="mt-0.5 flex flex-wrap items-center gap-1">
                      {markers.map((marker) => (
                        <Badge
                          key={marker.key}
                          size="sm"
                          variant={marker.tone}
                          title={marker.detail}
                        >
                          {marker.label}
                        </Badge>
                      ))}
                    </div>
                  )}
                  <div className="mt-0.5 flex flex-wrap gap-x-2 text-[10.5px] text-muted-foreground">
                    {f.last_stop && <span>上轮 {f.last_stop}</span>}
                    <span>声明 {f.last_declared ?? 0}</span>
                    {changedText && <span className="text-warning">{changedText}</span>}
                    {f.blocked_on?.length ? <span className="text-destructive">{f.blocked_on.join('、')}</span> : null}
                  </div>
                </div>
              </>
            )
            return (
              <li key={f.id}>
                {onOpenNode ? (
                  <button
                    type="button"
                    data-testid="overview-frontier-link"
                    onClick={() => onOpenNode(`s:${f.id}`)}
                    title="在图谱中选中这条方向"
                    className="flex w-full items-start gap-2 px-3 py-1.5 text-left transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                  >
                    {content}
                  </button>
                ) : (
                  <div className="flex items-start gap-2 px-3 py-1.5">{content}</div>
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
      title={
        <span className="flex items-center gap-1.5">
          <Network className="h-3.5 w-3.5" /> 最近事实
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
      {items.length === 0 ? (
        <EmptyState
          compact
          icon={Network}
          title="还没有事实"
          description="执行者按交接契约写下结论后，事实意图图会把它们列在这里。"
        />
      ) : (
        <ul className="divide-y divide-border/60">
          {items.map((f) => {
            const content = (
              <>
                <Badge variant={kindVariant(f.kind)} size="sm" className="mt-0.5 shrink-0">
                  {f.kind}
                </Badge>
                <div className="min-w-0 flex-1">
                  <div className="truncate text-[12px] text-foreground">{f.title}</div>
                  <div className="mt-0.5 flex flex-wrap items-center gap-x-2 text-[10.5px] text-muted-foreground">
                    <span className="font-mono">{f.id}</span>
                    <span>证据 {f.evidence ?? 0}</span>
                    {f.origin && (
                      <Badge variant={originVariant(f.origin)} size="sm">
                        {f.origin}
                      </Badge>
                    )}
                    {f.source_ref?.startsWith('ioa:') && (
                      <Badge variant="outline" size="sm" title="来自 harness IOA 记录：子会话在账本外，没有 usage、工具事件与独立 run">
                        IOA · 账本外
                      </Badge>
                    )}
                    <StatusIndicator compact status={statusKind(f.status)} label={statusLabel(f.status)} />
                  </div>
                </div>
              </>
            )
            return (
              <li key={f.id}>
                {onOpenNode ? (
                  <button
                    type="button"
                    data-testid="overview-fact-link"
                    onClick={() => onOpenNode(`f:${f.id}`)}
                    title="在图谱中打开这条事实"
                    className="flex w-full items-start gap-2 px-3 py-1.5 text-left transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                  >
                    {content}
                  </button>
                ) : (
                  <div className="flex items-start gap-2 px-3 py-1.5">{content}</div>
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
      title={
        <span className="flex items-center gap-1.5">
          <BookMarked className="h-3.5 w-3.5" /> 战场知识
        </span>
      }
      extra={
        <Button size="xs" variant="ghost" onClick={onOpen}>
          打开
        </Button>
      }
      className={'flex min-h-0 flex-col ' + (className ?? '')}
      bodyClassName="min-h-0 flex-1 overflow-y-auto p-3"
    >
      <div className="grid grid-cols-3 gap-2">
        <StatTile label="已确认" value={fmtInt(confirmed)} tone={confirmed > 0 ? 'success' : 'muted'} />
        <StatTile label="候选" value={fmtInt(candidates)} tone={candidates > 0 ? 'warning' : 'muted'} />
        <StatTile label="已退役" value={fmtInt(retired)} tone="muted" />
      </div>

      {top.length === 0 ? (
        <div className="mt-2 text-[11px] leading-relaxed text-muted-foreground">
          执行者跑过一轮后声明打法；指挥官只能提名候选。项目私有知识，不进报告。
        </div>
      ) : (
        <ul className="mt-2 divide-y divide-border/60">
          {top.map((p) => (
            <li key={p.id} className="py-1.5">
              <div className="flex items-center gap-1.5">
                <span className="min-w-0 flex-1 truncate text-[12px] text-foreground" title={p.title}>
                  {p.title}
                </span>
                <Badge size="sm" variant={p.status === 'confirmed' ? 'success' : p.status === 'candidate' ? 'warning' : 'muted'}>
                  {p.status === 'confirmed' ? '确认' : p.status === 'candidate' ? '候选' : '退役'}
                </Badge>
              </div>
              <div className="mt-0.5 line-clamp-1 text-[10.5px] text-muted-foreground">{p.when}</div>
            </li>
          ))}
        </ul>
      )}
      <p className="mt-2 text-[10.5px] text-muted-foreground">
        确认来自跑过的那一轮；候选没有执行证据，派发时只作线索。
        {(stats?.archived??0)>0&&` 已归档 ${stats?.archived} 条，可在战场知识中查看。`}
      </p>
    </Panel>
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
    <Panel title="从这里开始">
      <div className="grid gap-4 md:grid-cols-3">
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-ui font-semibold text-foreground">
            <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary/15 font-mono text-[11px] text-primary">1</span>
            写项目简报
          </div>
          <p className="text-[12px] leading-relaxed text-muted-foreground">
            目标与起点、已知资产、时间窗口与禁止动作。简报和内置约束会原样进入每次派发和决策：测试中新增或移除的资产，直接用「图谱」右侧下发窗口补充，不必回来改这里。
          </p>
          <div className="tw-break rounded border border-border bg-muted/40 px-2 py-1.5 font-mono text-[11px] text-muted-foreground">
            {scope || '未指定'}
          </div>
          <Button size="xs" variant="ghost" onClick={() => window.location.assign('/g/?view=projects')}>
            去改项目简报
          </Button>
        </div>
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-ui font-semibold text-foreground">
            <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary/15 font-mono text-[11px] text-primary">2</span>
            目标（可先不定）
          </div>
          <p className="text-[12px] leading-relaxed text-muted-foreground">
            可以先不写目标：开跑后指挥官会从简报里划分目标并写入账本（POST /api/goals）。也可以现在补一句能判真假的话；达成必须挂真实证据。
          </p>
          <Button size="xs" onClick={onNewGoal}>
            <Flag className="h-3 w-3" /> 新建目标
          </Button>
        </div>
        <div className="space-y-2">
          <div className="flex items-center gap-2 text-ui font-semibold text-foreground">
            <span className="flex h-5 w-5 items-center justify-center rounded-full bg-primary/15 font-mono text-[11px] text-primary">3</span>
            本机执行
          </div>
          <p className="text-[12px] leading-relaxed text-muted-foreground">
            单机实例只有一个执行位置：控制台启动的本机 aiscan web。项目自动绑定它，不需要人工选节点。
          </p>
          <div className="flex flex-wrap gap-1">
            {bound.length ? (
              bound.map((n) => (
                <Badge key={n.id} variant={n.id === defaultNode ? 'primary' : 'secondary'} className="font-mono text-[11px]">
                  {n.id}
                  {n.id === defaultNode ? ' · 首选' : ''}
                </Badge>
              ))
            ) : (
              <Badge variant="warning" className="text-[11px]">
                还没有绑定本机节点
              </Badge>
            )}
          </div>
          <Button size="xs" variant="ghost" onClick={() => onNavigate?.('nodes')}>
            查看执行宿主
          </Button>
        </div>
      </div>
      <div className="mt-3 flex flex-wrap items-center gap-2 border-t border-border pt-3 text-[12px] text-muted-foreground">
        <Play className="h-3 w-3" />
        写清项目简报、绑好本机节点就能开跑；目标可以之后由指挥官从简报划分，或在这里手动补。
        <Button
          size="xs"
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
          {starting ? '已排队…' : '开始运行'}
        </Button>
      </div>
      <div className="mt-2 text-[11px] text-muted-foreground">
        项目：{project?.name ?? '—'} · ID {project?.id ?? '—'}
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

function Row({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="text-[10px] uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="truncate font-mono text-[12px] text-foreground" title={typeof value === 'string' ? value : undefined}>
        {value}
      </div>
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
