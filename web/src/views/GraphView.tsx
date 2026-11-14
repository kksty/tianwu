// The project shell (T1, docs/34 §1/§2): rail + merged view routing + project
// chrome + selection/hash deep link + the create-intent dialog. The 图谱 page
// body itself lives in components/graph/GraphPanel.tsx.
//
// Routing is the URL: `?view=<group>&tab=<tab>[&run=…][&intent=…]`. The old
// one-view-per-page ids (`?view=workers`, `?view=focus`, `?view=traffic` …) are
// aliases and still resolve; the shell normalizes them with `replaceState` so a
// bookmark keeps working but the address bar shows the merged form.
//
// Jumping away and coming back (docs/34 §2): leaving a view records its
// `{ tab, search, hash }` in view-memory; a rail click restores the remembered
// tab/record/graph selection, while explicit deep-link parameters always win.
// Browser Back needs no memory — the history entry already carries its URL.
//
// The ledger snapshot stays here on purpose: `useGraphSnapshot(graphActive,
// current)` must keep its data when the operator visits another panel and comes
// back (T2's contract), which only the always-mounted shell can do. GraphPanel
// receives it as a prop and owns every graph-local poll (runs / changes).

import { Suspense, lazy, useEffect, useMemo, useRef, useState } from 'react'
import {
  Activity,
  BookMarked,
  BrainCircuit,
  Bug,
  FileText,
  Gauge,
  Globe,
  LayoutDashboard,
  MessagesSquare,
  Paperclip,
  Pause,
  Play,
  Plus,
  Server,
  ShieldCheck,
  Target,
  Waypoints,
} from 'lucide-react'
import {
  Badge,
  Button,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  useConfirm,
} from '@cyber/ui'
import { ConsoleRail } from '@/components/ConsoleRail'
import { AppendGoalDialog } from '@/components/AppendGoalDialog'
import { CreateIntentDialog } from '@/components/CreateIntentDialog'
import { SealGoalDialog, type SealGoalRef } from '@/components/SealGoalDialog'
import { ProjectChromeContext } from '@/components/Page'
import { PageSkeleton } from '@/components/PageSkeleton'
import { SideNav } from '@/components/SideNav'
import type { CommanderTab } from '@/views/CommanderView'
import type { EvidenceTab } from '@/views/EvidenceHub'
import type { RuntimeTab } from '@/views/ExecutionEnv'
import type { RunTab } from '@/views/RunCenter'
import type { StateTab } from '@/views/StateCenter'
import { api, gotoProject } from '@/lib/api'
import { fmtInt } from '@/lib/format'
import { useGraphSnapshot } from '@/lib/graph-data'
import { useConsoleLocation, usePoll } from '@/lib/hooks'
import { decodeLocationPart, locationPath, navigate } from '@/lib/navigation'
import { pageTitle } from '@/lib/page-title'
import { createPrefetchRegistry } from '@/lib/prefetch'
import type { BoardResponse, ProjectInfo } from '@/lib/types'
import { readViewMemory, writeViewMemory } from '@/lib/view-memory'

// ---------------------------------------------------------------------------
// Lazy chunks. One loader per rail item, used twice: `lazy` renders it and the
// rail's hover prefetch starts the download early. Keeping a single definition
// means the chunk a hover warms is always the chunk the panel imports. The
// merged groups load their TabBar + all child panels as one chunk (T2).
// ---------------------------------------------------------------------------
const PANEL_LOADERS = {
  overview: () => import('@/views/Overview').then((m) => ({ default: m.Overview })),
  graph: () => import('@/components/graph/GraphPanel').then((m) => ({ default: m.GraphPanel })),
  state: () => import('@/views/StateCenter').then((m) => ({ default: m.StateCenter })),
  messages: () => import('@/views/Messages').then((m) => ({ default: m.Messages })),
  runs: () => import('@/views/RunCenter').then((m) => ({ default: m.RunCenter })),
  access: () => import('@/views/Access').then((m) => ({ default: m.Access })),
  evidence: () => import('@/views/EvidenceHub').then((m) => ({ default: m.EvidenceHub })),
  vulns: () => import('@/views/Vulns').then((m) => ({ default: m.Vulns })),
  playbooks: () => import('@/views/Playbooks').then((m) => ({ default: m.Playbooks })),
  reports: () => import('@/views/Reports').then((m) => ({ default: m.Reports })),
  commander: () => import('@/views/CommanderView').then((m) => ({ default: m.CommanderView })),
  runtime: () => import('@/views/ExecutionEnv').then((m) => ({ default: m.ExecutionEnv })),
}

type PanelID = keyof typeof PANEL_LOADERS

// Module scope on purpose: the registry must survive re-renders so a second
// hover on the same row does not start a second download.
const prefetchRegistry = createPrefetchRegistry()
for (const id of Object.keys(PANEL_LOADERS) as PanelID[]) {
  prefetchRegistry.register(id, PANEL_LOADERS[id])
}

const Overview = lazy(PANEL_LOADERS.overview)
const GraphPanel = lazy(PANEL_LOADERS.graph)
const StateCenter = lazy(PANEL_LOADERS.state)
const Messages = lazy(PANEL_LOADERS.messages)
const RunCenter = lazy(PANEL_LOADERS.runs)
const Access = lazy(PANEL_LOADERS.access)
const EvidenceHub = lazy(PANEL_LOADERS.evidence)
const Vulns = lazy(PANEL_LOADERS.vulns)
const Playbooks = lazy(PANEL_LOADERS.playbooks)
const Reports = lazy(PANEL_LOADERS.reports)
const CommanderView = lazy(PANEL_LOADERS.commander)
const ExecutionEnv = lazy(PANEL_LOADERS.runtime)

// ---------------------------------------------------------------------------
// Navigation model (docs/34 §1.1). 12 rail items in 3 groups; five of them are
// merged pages whose tabs live in `?tab=`.
// ---------------------------------------------------------------------------
const NAV = [
  { id: 'overview', label: '概览', icon: LayoutDashboard },
  { id: 'graph', label: '图谱', icon: Waypoints },
  { id: 'state', label: '状态', icon: Gauge },
  { id: 'messages', label: '通信', icon: MessagesSquare },
  { id: 'runs', label: '运行', icon: Activity },
  { id: 'access', label: '访问与交接', icon: ShieldCheck },
  { id: 'evidence', label: '证据', icon: Paperclip },
  { id: 'vulns', label: '漏洞', icon: Bug },
  { id: 'playbooks', label: '战场知识', icon: BookMarked },
  { id: 'reports', label: '报告', icon: FileText },
  { id: 'commander', label: '决策', icon: BrainCircuit },
  { id: 'runtime', label: '执行环境', icon: Server },
] as const

type ViewID = (typeof NAV)[number]['id']

const NAV_GROUPS: { label: string; items: readonly ViewID[] }[] = [
  { label: '任务现场', items: ['overview', 'graph', 'state', 'messages', 'runs', 'access'] },
  { label: '发现与证据', items: ['evidence', 'vulns', 'playbooks', 'reports'] },
  { label: '指挥与运行', items: ['commander', 'runtime'] },
]

const GROUP_IDS = ['runs', 'runtime', 'commander', 'evidence', 'state'] as const
type GroupID = (typeof GROUP_IDS)[number]

// The tab unions come from the containers T2 owns: importing them (type-only,
// erased at build time) makes a renamed tab a compile error here instead of a
// dead link at runtime.
type TabID = RunTab | RuntimeTab | CommanderTab | EvidenceTab | StateTab

const RUN_TABS = ['runs', 'workers', 'actions'] as const satisfies readonly RunTab[]
const RUNTIME_TABS = ['nodes', 'terminal', 'repl', 'toolbox'] as const satisfies readonly RuntimeTab[]
const COMMANDER_TABS = ['decisions', 'memory'] as const satisfies readonly CommanderTab[]
const EVIDENCE_TABS = ['evidence', 'traffic', 'assets'] as const satisfies readonly EvidenceTab[]
const STATE_TABS = ['state', 'focus'] as const satisfies readonly StateTab[]

const GROUP_TABS: Record<GroupID, { tabs: readonly TabID[]; default: TabID }> = {
  runs: { tabs: RUN_TABS, default: 'runs' },
  runtime: { tabs: RUNTIME_TABS, default: 'nodes' },
  commander: { tabs: COMMANDER_TABS, default: 'decisions' },
  evidence: { tabs: EVIDENCE_TABS, default: 'evidence' },
  state: { tabs: STATE_TABS, default: 'state' },
}

// Old `?view=` ids → merged group + tab (docs/34 §1.1). Plain pages map to
// themselves, so this table is also the single list of accepted view ids.
const VIEW_ALIASES: Record<string, { view: ViewID; tab?: TabID }> = {
  overview: { view: 'overview' },
  graph: { view: 'graph' },
  state: { view: 'state' },
  focus: { view: 'state', tab: 'focus' },
  messages: { view: 'messages' },
  runs: { view: 'runs' },
  workers: { view: 'runs', tab: 'workers' },
  actions: { view: 'runs', tab: 'actions' },
  access: { view: 'access' },
  evidence: { view: 'evidence' },
  traffic: { view: 'evidence', tab: 'traffic' },
  assets: { view: 'evidence', tab: 'assets' },
  vulns: { view: 'vulns' },
  playbooks: { view: 'playbooks' },
  reports: { view: 'reports' },
  commander: { view: 'commander' },
  decisions: { view: 'commander', tab: 'decisions' },
  memory: { view: 'commander', tab: 'memory' },
  runtime: { view: 'runtime' },
  nodes: { view: 'runtime', tab: 'nodes' },
  terminal: { view: 'runtime', tab: 'terminal' },
  repl: { view: 'runtime', tab: 'repl' },
  toolbox: { view: 'runtime', tab: 'toolbox' },
}

// Record params name a row inside one view. They belong to the view that renders
// them, so switching away must not leave a stale pointer behind, and switching
// back may restore it from memory.
const RECORD_KEYS = ['run', 'run_group', 'intent', 'vuln'] as const
type RecordKey = (typeof RECORD_KEYS)[number]

const RECORD_OWNERS: Record<RecordKey, ViewID> = {
  run: 'runs',
  run_group: 'runs',
  intent: 'state',
  vuln: 'vulns',
}

/** `#new` opens the create-intent dialog; it is not a node selection. */
const DIALOG_HASH = '#new'

interface ViewMemory {
  tab?: TabID
  search?: string
  hash?: string
}

interface ViewTarget {
  view: ViewID
  tab?: TabID
}

function isGroup(view: ViewID): view is GroupID {
  return (GROUP_IDS as readonly string[]).includes(view)
}

function validTab(view: ViewID, tab: string | null | undefined): TabID | undefined {
  if (!tab || !isGroup(view)) return undefined
  return GROUP_TABS[view].tabs.includes(tab as TabID) ? (tab as TabID) : undefined
}

function resolveView(raw: string | null | undefined): ViewTarget | null {
  if (!raw) return null
  return VIEW_ALIASES[raw] ?? null
}

function ownsRecord(view: ViewID, key: RecordKey): boolean {
  return RECORD_OWNERS[key] === view
}

function restorableHash(hash: string | undefined): string {
  if (!hash || hash === '#' || hash === DIALOG_HASH) return ''
  return hash
}

/** Canonical URL: `view` first, `tab` only for merged groups, everything else kept. */
function buildViewURL(view: ViewID, tab: TabID | '', params: URLSearchParams, hash: string): string {
  const next = new URLSearchParams()
  next.set('view', view)
  if (tab) next.set('tab', tab)
  params.forEach((value, key) => {
    if (key === 'view' || key === 'tab') return
    next.append(key, value)
  })
  const search = next.toString()
  return `${window.location.pathname}${search ? `?${search}` : ''}${hash}`
}

interface ViewRoute {
  view: ViewID
  tab: TabID | ''
  params: URLSearchParams
  search: string
  hash: string
  /** The canonical form of the URL this route describes. */
  canonical: string
}

/** A record param already names the tab that renders it: `?view=runs&run=…`
 *  must open 运行记录 even when another tab of that group was visited last. */
function recordTab(view: ViewID, params: URLSearchParams): TabID | undefined {
  if (view === 'runs' && (params.get('run') || params.get('run_group'))) return 'runs'
  if (view === 'state' && params.get('intent')) return 'state'
  return undefined
}

/** Resolve the URL into the view the shell renders (alias + memory aware). */
function resolveRoute(location: URL, scope: string): ViewRoute {
  const params = new URLSearchParams(location.search)
  // `?vuln=<id>` alone still lands on the vulnerability library, otherwise a
  // shared detail link would stop on the overview page that never renders it.
  const raw = params.get('view') ?? (params.get('vuln') ? 'vulns' : '')
  const base = resolveView(raw) ?? { view: 'overview' as ViewID }
  const resolved = base.view
  // Explicit `?tab=` (or the alias's own tab) beats memory; memory beats the
  // group's default (docs/34 §1.1). A deep-linked record implies its own tab
  // before memory does, so `?view=runs&run=<id>` never lands on another tab.
  const tab: TabID | '' = isGroup(resolved)
    ? (validTab(resolved, base.tab) ??
      validTab(resolved, params.get('tab')) ??
      recordTab(resolved, params) ??
      validTab(resolved, readViewMemory<ViewMemory>(scope, resolved)?.tab) ??
      GROUP_TABS[resolved].default)
    : ''
  const hash = location.hash
  return {
    view: resolved,
    tab,
    params,
    search: location.search,
    hash,
    canonical: buildViewURL(resolved, tab, params, hash),
  }
}

interface Props {
  current: string
  projects: ProjectInfo[]
  board?: BoardResponse
  loadError?: string
  onRefresh: () => void
}

export function GraphView({ current, projects, board, onRefresh, loadError }: Props) {
  const confirm = useConfirm()
  const location = useConsoleLocation()
  const scope = `project:${current}`
  const route = useMemo(() => resolveRoute(location, scope), [location, scope])
  const view = route.view
  const tab = route.tab

  // The snapshot itself lives in the shell so leaving the map does not refetch
  // it; the map's own polls (runs / changes, and the 实时 reload) live in
  // GraphPanel and stop when the panel unmounts.
  const graphActive = view === 'graph'
  const humanRequests = usePoll(api.operatorRequests, 6000)
  const humanPending = (humanRequests.data ?? []).filter((item) => !item.request.completed_at).length
  const projectName = projects.find((p) => p.id === current)?.name
  const project = projects.find((p) => p.id === current)

  // Old bookmarks/aliases: rewrite the address bar to the merged form without
  // adding a history entry (docs/34 §2). Explicit record params and the graph
  // hash are preserved by the canonical builder; only `view`/`tab` change.
  useEffect(() => {
    if (route.canonical !== locationPath()) navigate(route.canonical, true)
  }, [route.canonical])

  // Leaving a view records the tab, record params and graph selection, so a
  // rail click can put the operator back where they were (docs/34 §2). Tab
  // changes inside one group are not a leave.
  const previousRoute = useRef<ViewRoute | null>(null)
  useEffect(() => {
    const previous = previousRoute.current
    if (previous && previous.view !== route.view) {
      writeViewMemory<ViewMemory>(scope, previous.view, {
        tab: previous.tab || undefined,
        search: previous.search,
        hash: previous.hash,
      })
    }
    previousRoute.current = route
  }, [route, scope])

  // The tab names the engagement and the panel: several projects/panels open in
  // parallel are otherwise indistinguishable tabs.
  useEffect(() => {
    document.title = pageTitle([projectName, NAV.find((n) => n.id === view)?.label])
  }, [projectName, view])

  const hash = route.hash ? decodeLocationPart(route.hash.replace(/^#/, '')) : ''
  const [creating, setCreating] = useState(hash === 'new')
  const [selected, setSelected] = useState<string | null>(hash && hash !== 'new' ? hash : null)
  useEffect(() => {
    setSelected(hash && hash !== 'new' ? hash : null)
    setCreating(hash === 'new')
  }, [hash])
  const [deciding, setDeciding] = useState(false)
  const [pausing, setPausing] = useState(false)
  // The explicit "append a goal and continue" action (not chat): see
  // internal/api/goal_continue.go. goalRefreshSeq lets the always-mounted
  // overview reload its own goals poll after a successful append.
  const [appendGoalOpen, setAppendGoalOpen] = useState(false)
  // The pause banner's "认可达成并收口" opens the same seal dialog the overview
  // goal card uses. It is separate from "append a new goal": sealing the open
  // goal does not create a second one and does not resume the project.
  const [sealGoal, setSealGoal] = useState<SealGoalRef | null>(null)
  const [goalRefreshSeq, setGoalRefreshSeq] = useState(0)
  const [actionError, setActionError] = useState('')
  // The runs poll lives in GraphPanel (it only feeds the map model); its error
  // is still part of the page chrome, so the panel reports it up.
  const [runsError, setRunsError] = useState<string>()

  const graph = useGraphSnapshot(graphActive, current)

  /** Selection is deep-linkable: the hash names the node. */
  function select(id: string | null) {
    setSelected(id)
    const qs = window.location.search
    const next = id
      ? `${window.location.pathname}${qs}#${encodeURIComponent(id)}`
      : `${window.location.pathname}${qs}`
    navigate(next, true)
  }

  interface OpenViewOptions {
    /** Explicit tab (old alias or a tab click): wins over memory and default. */
    tab?: TabID
    /** Record params this navigation sets; `''` clears one. */
    params?: Partial<Record<RecordKey, string>>
    /** `''` clears the hash; `undefined` lets the graph memory decide. */
    hash?: string
    replace?: boolean
  }

  /**
   * Open a rail view. Precedence for the tab, the view's own record params and
   * the graph hash: explicit URL argument → view memory → group default. Only
   * the URL of the *current* view counts as explicit for the target (a stale
   * `?run=` from the page we are leaving must not follow us).
   */
  function openView(target: ViewID, options: OpenViewOptions = {}) {
    const group = isGroup(target)
    const memory = readViewMemory<ViewMemory>(scope, target)
    // Memory is for *returning* to a view. Staying inside it (re-clicking the
    // active rail row, switching its tabs) must read the live URL only, or an
    // old selection kept in memory would jump back in.
    const returning = route.view !== target

    const explicitTab = group
      ? (validTab(target, options.tab) ??
        (route.view === target ? validTab(target, route.tab) : undefined))
      : undefined
    const tab: TabID | '' = group
      ? (explicitTab ??
        (returning ? validTab(target, memory?.tab) : undefined) ??
        GROUP_TABS[target].default)
      : ''

    // Keep the operator's unrelated params (`case=`, `autorun=1`, …); every
    // record param is rebuilt below from the target's point of view.
    const params = new URLSearchParams(window.location.search)
    for (const key of RECORD_KEYS) params.delete(key)

    const remembered = returning ? new URLSearchParams(memory?.search ?? '') : null
    for (const key of RECORD_KEYS) {
      if (!ownsRecord(target, key)) continue
      const value = (route.view === target ? route.params.get(key) : null) ?? remembered?.get(key)
      if (value) params.set(key, value)
    }
    if (options.params) {
      for (const [key, value] of Object.entries(options.params) as [RecordKey, string | undefined][]) {
        if (!value) params.delete(key)
        else params.set(key, value)
      }
    }

    let hash = ''
    if (options.hash !== undefined) hash = options.hash
    else if (target === 'graph') {
      // Staying on / re-clicking the map keeps the selection in the URL; only a
      // return from another view pulls the remembered selection back.
      hash = returning ? restorableHash(memory?.hash) : route.hash
    }

    navigate(buildViewURL(target, tab, params, hash), options.replace ?? false)
  }

  /** Rail clicks and alias navigation: `?view` may be an old view id. */
  function selectView(next: string) {
    const target = resolveView(next)
    if (target) openView(target.view, { tab: target.tab })
  }

  /** Tab clicks inside a merged group: same view, new tab (push). */
  function selectTab(next: TabID) {
    if (!isGroup(view) || !GROUP_TABS[view].tabs.includes(next)) return
    openView(view, { tab: next })
  }

  /** Cross-page link: one direction on the state page (`?intent=`). */
  function openIntent(intentID: string) {
    openView('state', { tab: 'state', params: { intent: intentID } })
  }

  /** Cross-page link: one run in the run ledger (`?run=`). */
  function openRun(runID: string) {
    // A specific run pins the history row; a remembered group key would point at
    // another group, so clear it explicitly.
    openView('runs', { tab: 'runs', params: { run: runID, run_group: undefined } })
  }

  /** Selection is a graph affordance: select the node (`f:`/`s:`/edge id). */
  function openGraphNode(nodeID: string) {
    selectView('graph')
    select(nodeID)
  }

  function refresh() {
    // Board/projects are the shell's shared chrome, so they always reload. The
    // ledger is only pulled when the map is on screen: reopening 图谱 revalidates
    // it immediately, so refreshing another panel does not download it twice.
    if (graphActive) {
      graph.reload()
    }
    onRefresh()
  }

  async function decideRound() {
    setDeciding(true)
    setActionError('')
    try {
      await api.decide('operator')
      onRefresh()
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
    } finally {
      setDeciding(false)
    }
  }

  /** The whole-project switch: stop deciding *and* stop the executors. The
   *  canceled directions go back to pending, so resuming re-dispatches them. */
  async function toggleProject(resumeGoalID?: string) {
    if (!board) return
    const projectStopped = Boolean(board.project_paused || board.decider.paused)
    if (!projectStopped) {
      const ok = await confirm({
        title: '暂停项目',
        description:
          running > 0
            ? `指挥官会停止决策，当前 ${running} 个正在跑的 run 会被取消（已写出的证据保留）。继续后指挥官会重新派发待办方向。`
            : '指挥官会停止决策，直到你点「继续项目」。',
        confirmLabel: '暂停项目',
      })
      if (!ok) return
    }
    setPausing(true)
    try {
      if (projectStopped) await api.resumeProject(resumeGoalID)
      else await api.pauseProject()
      refresh()
    } catch (e) {
      setActionError(e instanceof Error ? e.message : String(e))
    } finally {
      setPausing(false)
    }
  }

  function switchProject(id: string) {
    if (id !== current && projects.some(p => p.id === id)) gotoProject(id)
  }

  const running = board?.intents.running ?? 0

  const panel =
    view === 'overview' ? (
      <Overview
        current={current}
        projects={projects}
        board={board}
        onRefresh={refresh}
        onNavigate={selectView}
        onOpenNode={openGraphNode}
        refreshGoalsSeq={goalRefreshSeq}
      />
    ) : view === 'state' ? (
      <StateCenter
        tab={tab as StateTab}
        onTab={selectTab}
        onNavigate={selectView}
        // ProjectState's 图谱 entry opens the direction on the map (docs/34
        // §1.2 contract: "ProjectState: 图谱"), unlike RunCenter's 状态 entry.
        onOpenIntent={(id) => openGraphNode(`s:${id}`)}
        onOpenRun={openRun}
        onOpenFact={(id) => openGraphNode(`f:${id}`)}
      />
    ) : view === 'evidence' ? (
      <EvidenceHub tab={tab as EvidenceTab} onTab={selectTab} />
    ) : view === 'vulns' ? (
      <Vulns lockedProject={current} />
    ) : view === 'runs' ? (
      <RunCenter
        tab={tab as RunTab}
        onTab={selectTab}
        current={current}
        projects={projects}
        onRefresh={refresh}
        onOpenIntent={openIntent}
        onOpenGraphIntent={(id) => openGraphNode(`s:${id}`)}
        onOpenRun={openRun}
      />
    ) : view === 'messages' ? (
      <Messages />
    ) : view === 'access' ? (
      <Access />
    ) : view === 'commander' ? (
      <CommanderView tab={tab as CommanderTab} onTab={selectTab} board={board} />
    ) : view === 'playbooks' ? (
      <Playbooks />
    ) : view === 'reports' ? (
      <Reports onGoVerify={() => selectView('vulns')} />
    ) : view === 'runtime' ? (
      <ExecutionEnv tab={tab as RuntimeTab} onTab={selectTab} />
    ) : null

  const paused = Boolean(board?.project_paused || board?.decider.paused)
  // A finished engagement pauses itself, so the switch reads 「继续项目」. Say why
  // it is paused instead of leaving the operator to guess: the reason is either
  // "目标已完成" (auto), a provider failure, the operator's own pause, or the
  // commander's explicit stop request.
  const pauseReason = paused ? (board?.decider.paused_reason ?? '') : ''
  const finished = pauseReason.includes('目标已完成')
  // Who stopped the project is a machine code, not prose (project_pause_kind):
  // a commander stop must stay visible in the banner and still be undone only by
  // the operator's explicit 「继续项目」. "Open goal + commander stop" is the
  // ambiguous state the banner has to explain: the commander stopped, but the
  // ledger has not stamped the goal, so the terminal is still unmet. Once every
  // goal is met the banner says 已收口 instead of repeating the stop request.
  const currentGoal = board?.goals.open?.[0]
  const goalOpen = (board?.goals.total ?? 0) > (board?.goals.met ?? 0)
  const commanderStoodBy = paused && board?.project_pause_kind === 'commander_standby' && goalOpen
  const commanderClosed =
    paused && board?.project_pause_kind === 'commander_standby' && !goalOpen && (board?.goals.total ?? 0) > 0

  // The board counters are a quiet meta line, not chips: label at 11px ink-3,
  // the number mono/tabular at ink-2, groups separated by a hairline. They sit
  // on the header's lower row next to the snapshot/live/token meta, so the top
  // row stays "what can I do" and never competes with the title.
  const stats = view === 'overview' ? null : (
    <div className="hidden min-h-8 items-center gap-2.5 text-[11px] leading-4 text-ink-3 xl:flex">
      <span className="inline-flex items-baseline gap-1.5">
        意图
        <b className="tw-nums font-mono text-[11px] font-semibold text-ink-2">{fmtInt(board?.intents.total)}</b>
      </span>
      <span className="h-3 w-px shrink-0 bg-hairline" aria-hidden />
      <span className="inline-flex items-baseline gap-1.5">
        事实
        <b className="tw-nums font-mono text-[11px] font-semibold text-ink-2">{fmtInt(board?.facts.total)}</b>
      </span>
      <span className="h-3 w-px shrink-0 bg-hairline" aria-hidden />
      <span className="inline-flex items-baseline gap-1.5">
        证据
        <b className="tw-nums font-mono text-[11px] font-semibold text-ink-2">{fmtInt(board?.evidence)}</b>
      </span>
      <span className="h-3 w-px shrink-0 bg-hairline" aria-hidden />
      <span className="inline-flex items-baseline gap-1.5">
        在跑
        <b
          className={`tw-nums font-mono text-[11px] font-semibold ${running > 0 ? 'text-primary' : 'text-ink-2'}`}
        >
          {running}
        </b>
      </span>
    </div>
  )

  // The project-wide controls ride in every page banner (ProjectChromeContext),
  // so the kill switch is never buried in one view. One compact size (h-8) and
  // one hierarchy: a single primary CTA (新建意图) on the right, the rest
  // outline/ghost so the header has one thing to click first.
  const controls = (
    <>
      {commanderStoodBy && (
        <Badge size="sm" variant="warning" title={pauseReason} className="shrink-0">
          指挥官请求停止 · 目标仍未达成
        </Badge>
      )}
      {commanderClosed && (
        <Badge size="sm" variant="success" title={pauseReason} className="shrink-0">
          已收口
        </Badge>
      )}
      {finished && (
        <Badge size="sm" variant="success" title={pauseReason} className="shrink-0">
          已完成
        </Badge>
      )}
      {board && (
        <Button
          size="sm"
          variant="outline"
          className="gap-1.5"
          onClick={() => toggleProject(commanderStoodBy ? currentGoal?.id : undefined)}
          disabled={pausing}
          title={
            commanderStoodBy
              ? `恢复项目并唤醒指挥官，继续当前目标「${currentGoal?.statement ?? ''}」，不新增目标`
              : paused
                ? pauseReason
                  ? `${pauseReason}；点「继续项目」恢复决策`
                  : '继续项目：指挥官恢复决策，并重新派发待办方向'
                : '暂停项目：停止指挥官，并取消所有正在跑的 run'
          }
        >
          {paused ? <Play className="h-4 w-4" /> : <Pause className="h-4 w-4" />}
          <span className="whitespace-nowrap">
            {paused ? (commanderStoodBy ? '继续当前目标' : '继续项目') : '暂停项目'}
          </span>
        </Button>
      )}
      {commanderStoodBy && currentGoal && (
        <Button
          size="sm"
          variant="outline"
          className="gap-1.5"
          onClick={() => setSealGoal(currentGoal)}
          title="用一条真实证据把当前目标盖章为已达成；项目保持暂停，不会自动恢复派发"
        >
          <Target className="h-4 w-4" />
          <span className="whitespace-nowrap">认可达成并收口</span>
        </Button>
      )}
      {/* Completion stand-by / manual pause: append a NEW goal and resume through
          one explicit operator action. Only offered when the project is parked
          and already has goals (a fresh project uses 新建目标 instead). */}
      {paused && (board?.goals.total ?? 0) > 0 && (
        <Button
          size="sm"
          variant="ghost"
          className="gap-1.5"
          onClick={() => setAppendGoalOpen(true)}
          title="追加一个新的独立目标，并按这次显式操作恢复派发；旧目标的完成收据保持不变。普通消息不会自动恢复暂停的项目。"
        >
          <Target className="h-4 w-4" />
          <span className="whitespace-nowrap">追加目标并继续</span>
        </Button>
      )}
      <Button
        size="sm"
        variant="outline"
        className="gap-1.5"
        onClick={decideRound}
        disabled={deciding || !board?.decider.enabled || paused}
        title={
          !board?.decider.enabled
            ? '未配置模型'
            : paused
              ? `指挥官已暂停：先点「${commanderStoodBy ? '继续当前目标' : '继续项目'}」`
              : '让指挥官立刻跑一轮'
        }
      >
        <Play className="h-4 w-4" />
        <span className="whitespace-nowrap">跑一轮</span>
      </Button>
      {/* One saturated action per page: the graph page itself has no other
          primary, so 新建意图 owns the accent there. Every other panel has its
          own page-level CTA (新建目标 / 造一个人 / 下发 …); the shared chrome
          drops to `soft` so the banner never shows two red buttons. */}
      <Button
        size="sm"
        className="gap-1.5"
        variant={view === 'graph' ? 'default' : 'soft'}
        onClick={() => setCreating(true)}
      >
        <Plus className="h-4 w-4" />
        <span className="whitespace-nowrap">新建意图</span>
      </Button>
    </>
  )

  return (
    <div className="flex h-full w-full flex-col overflow-hidden lg:flex-row">
      {/* ---- left rail ---- */}
      <ConsoleRail
        title="项目工作区"
        active={view}
        context={
          <Select value={current} onValueChange={switchProject}>
            <SelectTrigger
              className="h-9 w-full border-hairline bg-surface-2/60 text-[12px] text-ink-2"
              aria-label="切换项目"
              title={projects.find((p) => p.id === current)?.scope || '范围未指定'}
            >
              <SelectValue placeholder="项目" />
            </SelectTrigger>
            <SelectContent>
              {projects.map((p) => (
                <SelectItem key={p.id} value={p.id} className="text-[12px]">
                  {p.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        }
        footer={
          <Button
            size="sm"
            variant="ghost"
            className="w-full justify-start gap-2 text-ink-2"
            onClick={() => window.location.assign('/g/')}
            title="项目、全局漏洞库、POC 库与模型设置"
          >
            <Globe className="h-4 w-4 text-ink-3" />
            全局控制台
          </Button>
        }
      >
        <nav aria-label="项目导航" className="min-h-0 flex-1 space-y-5 overflow-y-auto px-3 pb-4">
          {NAV_GROUPS.map((group) => (
            <div key={group.label}>
              <div className="tw-eyebrow mb-2 px-3">{group.label}</div>
              <div className="space-y-0.5">
                {group.items.map((id) => {
                  const item = NAV.find((entry) => entry.id === id)!
                  return (
                    <SideNav
                      key={item.id}
                      active={view === item.id}
                      icon={item.icon}
                      label={item.label}
                      badge={item.id === 'access' && humanPending > 0 ? humanPending : undefined}
                      onClick={() => selectView(item.id)}
                      onIntent={() => prefetchRegistry.prefetch(item.id)}
                    />
                  )
                })}
              </div>
            </div>
          ))}
        </nav>
      </ConsoleRail>

      {/* ---- main ---- */}
      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
        <ProjectChromeContext.Provider
          value={{
            stats,
            controls,
            projectName,
            error: actionError || loadError || (graphActive ? graph.error || runsError : undefined),
          }}
        >
          {view === 'graph' ? (
            <Suspense fallback={<PageSkeleton />}>
              <GraphPanel
                current={current}
                projectName={projectName}
                project={project}
                board={board}
                graph={graph}
                selected={selected}
                onSelect={select}
                onNavigate={selectView}
                onCreateIntent={() => setCreating(true)}
                onRefresh={refresh}
                onError={setRunsError}
              />
            </Suspense>
          ) : (
            <div className="min-h-0 flex-1 overflow-hidden">
              <Suspense fallback={<PageSkeleton />}>{panel}</Suspense>
            </div>
          )}
        </ProjectChromeContext.Provider>
      </div>

      <CreateIntentDialog open={creating} onOpenChange={setCreating} onCreated={(id) => { refresh(); openIntent(id) }} />
      <AppendGoalDialog
        projectID={current}
        open={appendGoalOpen}
        onOpenChange={setAppendGoalOpen}
        paused={paused}
        pauseReason={pauseReason}
        onPartial={() => { setGoalRefreshSeq((n) => n + 1); refresh() }}
        onCompleted={() => {
          // The goal exists now; the board (pause state) and the overview's own
          // goals poll must both re-read, so "追加目标并继续" shows the result.
          setGoalRefreshSeq((n) => n + 1)
          refresh()
        }}
      />
      <SealGoalDialog
        goal={sealGoal}
        open={!!sealGoal}
        onOpenChange={(open) => { if (!open) setSealGoal(null) }}
        onSealed={() => {
          // The board now says the goal is met; the banner switches to 已收口.
          // The project deliberately stays paused: sealing is not resuming.
          setSealGoal(null)
          setGoalRefreshSeq((n) => n + 1)
          refresh()
        }}
      />
    </div>
  )
}
