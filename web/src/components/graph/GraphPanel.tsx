// The graph page body (T6, docs/33 §4.6). Everything the old GraphView rendered
// inside its `<Page>` lives here: canvas, filters, toolbar, legend, goals card,
// hover/pinned inspectors, chat dock, empty states. The shell (GraphView.tsx)
// keeps the rail, the other panels, project chrome, selection + hash deep link
// and the create-intent dialog.
//
// The snapshot itself is owned by the shell (`useGraphSnapshot(graphActive,
// current)`) because it must survive leaving the page without refetching; this
// component receives it as a prop. Leaving the page unmounts GraphPanel, so all
// polls (runs/changes) stop with the map, as before.

import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { ChevronDown, Crosshair, Plus, RefreshCw } from 'lucide-react'
import { Button } from '@cyber/ui'
import { CommanderCard } from '@/components/CommanderCard'
import { CreateChildIntentDialog } from '@/components/CreateChildIntentDialog'
import { GraphCanvas, type GraphControls } from '@/components/GraphCanvas'
import { GraphChatDock } from '@/components/GraphChatDock'
import { Page } from '@/components/Page'
import { ProjectTokens } from '@/components/TokenUsage'
import { cameraStorageKey } from '@/graph/camera'
import type { GraphNode } from '@/graph/model'
import type { LayoutMode } from '@/graph/sim'
import { useSessionState } from '@/lib/session-state'
import { api } from '@/lib/api'
import type { useGraphSnapshot } from '@/lib/graph-data'
import { useDebounced, useLoad, usePoll } from '@/lib/hooks'
import type { BoardResponse, ProjectInfo } from '@/lib/types'
import { GraphFilters } from './GraphFilters'
import { GraphHoverInspector } from './GraphHoverInspector'
import { GraphLegend } from './GraphLegend'
import { GraphListView } from './GraphListView'
import { GraphMinimap } from './GraphMinimap'
import { GraphToolbar } from './GraphToolbar'
import { defaultGraphFilters, useGraphModel, type GraphFiltersState } from './use-graph-model'
import { useFocusLayer } from './use-focus-layer'

/** The ledger snapshot lives in the shell; props stay structural so this file
 *  does not need to re-export T2's hook type. */
export type GraphSnapshot = ReturnType<typeof useGraphSnapshot>

// The pinned node panel is ~800 lines of detail/actions. Lazy loading it keeps
// the GraphView chunk inside the P1-c budget; the map is usable before it lands.
const GraphNodePanel = lazy(() => import('./GraphNodePanel'))

export interface GraphPanelProps {
  current: string
  /** Project display name; absent while the project list is still loading. */
  projectName?: string
  project?: ProjectInfo
  board?: BoardResponse
  graph: GraphSnapshot
  selected: string | null
  /** Shell-owned selection: writes the hash deep link. */
  onSelect: (id: string | null) => void
  /** Switch the shell to another panel (overview / messages / decisions …). */
  onNavigate: (view: string) => void
  onCreateIntent: () => void
  /** Shell refresh: reload the snapshot and the shared board/projects chrome. */
  onRefresh: () => void
  /** Runs poll errors are part of the page chrome; the shell shows them. */
  onError: (error: string | undefined) => void
}

export function GraphPanel({
  current,
  projectName,
  project,
  board,
  graph,
  selected,
  onSelect,
  onNavigate,
  onCreateIntent,
  onRefresh,
  onError,
}: GraphPanelProps) {
  const canvas = useRef<GraphControls | null>(null)
  // The runs poll is the only source of per-direction warnings, the resolved
  // profile and the model each direction actually ran on. It stops with the
  // page (this component unmounts when the rail moves to another view).
  const runs = usePoll(api.runs, 6000)
  const profiles = useLoad(api.modelProfiles, [])
  // A graph can change while the operator reads it. The changes probe is a
  // *notice*, never a merge: a new ledger version must be admitted by reloading
  // a fresh snapshot, because pages of two snapshots cannot be mixed.
  const changes = usePoll(api.changes, 6000)

  const [savedFilters, saveFilters] = useSessionState('graph.filters', { ...defaultGraphFilters(), hiddenKinds: [] as string[] })
  const filters = useMemo<GraphFiltersState>(() => ({ ...savedFilters, hiddenKinds: new Set(savedFilters.hiddenKinds) }), [savedFilters])
  const setFilters = (next: GraphFiltersState | ((previous: GraphFiltersState) => GraphFiltersState)) => {
    const value = typeof next === 'function' ? next(filters) : next
    saveFilters({ ...value, hiddenKinds: [...value.hiddenKinds] })
  }
  const [q, setQ] = useSessionState('graph.search', '')
  const query = useDebounced(q, 200)
  const [showLabels, setShowLabels] = useState(false)
  // Layered is the reading view (facts → step → next fact, left to right); the
  // force map stays one click away for "what is connected to what".
  const [layout, setLayout] = useState<LayoutMode>(() => {
    try {
      return localStorage.getItem('tw.graph.layout') === 'force' ? 'force' : 'layered'
    } catch {
      return 'layered'
    }
  })
  const [goalsOpen, setGoalsOpen] = useState(false)
  // 下发 lives on the 图谱 page's right dock: intervention next to the map. The
  // full conversation record stays in the 通信 page.
  const [chatDock, setChatDock] = useState(false)
  // The inspector asks GraphPanel to open the child dialog for the direction the
  // operator is looking at; the dialog lives here because it belongs to the map,
  // not to the project shell.
  const [childParent, setChildParent] = useState<{ id: string; title: string } | null>(null)
  // Bumped after a child is created so the inspector's (otherwise static) child
  // list reloads without waiting for the operator to reselect the node.
  const [childRefresh, setChildRefresh] = useState(0)
  const [listView, setListView] = useState(false)
  const [focusOverlay, setFocusOverlay] = useState(false)

  const chooseLayout = (next: LayoutMode) => {
    setLayout(next)
    try {
      localStorage.setItem('tw.graph.layout', next)
    } catch {
      /* private mode: the choice just does not persist */
    }
  }

  const profileMeta = useMemo(() => {
    const out: Record<string, { tier?: string; model?: string }> = {}
    for (const p of profiles.data?.profiles ?? []) out[p.id] = { tier: p.tier, model: p.model }
    return out
  }, [profiles.data])

  // A direction that has not run yet (pending intent, or a run that never
  // reported a model) falls back to the instance's active profile model.
  const defaultModel = useMemo(() => {
    const active = profiles.data?.active
    const list = profiles.data?.profiles ?? []
    return (list.find((p) => p.id === active) ?? list[0])?.model ?? ''
  }, [profiles.data])

  const model = useGraphModel(
    { data: graph.data, project, runs: runs.data ?? [], profileMeta, defaultModel, selected },
    filters,
  )

  useEffect(() => {
    onError(graph.error || runs.error)
  }, [graph.error, runs.error, onError])

  // Deep link: a hash naming a node that is not in the snapshot (yet) asks the
  // snapshot for the neighborhood around it. The shell owns the hash; this is
  // just the read side that turns it into a focused page.
  useEffect(() => {
    if (!graph.data || graph.loading || !selected) return
    const root = selected.startsWith('f:')
      ? `fact:${selected.slice(2)}`
      : selected.startsWith('s:')
        ? `intent:${selected.slice(2)}`
        : ''
    if (root && graph.root !== root && !model.byId.has(selected)) void graph.focus(root)
  }, [graph.data, graph.loading, graph.root, selected, model])

  // The three ledger polls that shaped the old graph are now one snapshot, so
  // "settled" is simply "the snapshot request finished". Settled (not "has
  // data") on purpose: a failed load must still release the camera instead of
  // leaving the map parked at the origin.
  const graphReady = !graph.loading

  const matches = useMemo(() => {
    if (!query.trim()) return null
    const needle = query.trim().toLowerCase()
    const set = new Set<string>()
    for (const n of model.nodes) {
      if (n.label.toLowerCase().includes(needle) || (n.sub ?? '').toLowerCase().includes(needle)) {
        set.add(n.id)
      }
    }
    for (const e of model.edges) {
      if (e.label?.toLowerCase().includes(needle)) set.add(e.id)
    }
    return set
  }, [query, model])

  // Open goals are not graph nodes (they have no produced fact to anchor to);
  // the graph shows them as a compact card instead.
  const openGoals = useMemo(() => (graph.data?.goals ?? []).filter((g) => !g.met), [graph.data])

  // Selection lookup goes through the model indexes (T2): no linear scans on a
  // 5k-node graph, and an edge id resolves to the direction it stands for.
  const selectedEdge = selected ? model.edgesById.get(selected) : undefined
  const selectedStep = selectedEdge?.intent?.id
    ? model.stepByIntent.get(selectedEdge.intent.id)
    : undefined
  const node: GraphNode | null = selectedEdge?.intent
    ? {
        id: selectedEdge.id,
        type: 'intent',
        label: selectedEdge.intent.title,
        sub: selectedEdge.intent.description,
        status: selectedEdge.intent.status,
        x: 0,
        y: 0,
        vx: 0,
        vy: 0,
        degree: 0,
        w: 0,
        h: 0,
        depth: 1,
        tier: selectedStep?.tier,
        model: selectedStep?.model,
        resolvedProfile: selectedStep?.resolvedProfile,
        meta: { intent: selectedEdge.intent },
      }
    : selected
      ? (model.byId.get(selected) ?? null)
      : null

  /** The intent a selection names, for the focus overlay. Edges and step/goal
   *  nodes all resolve to the direction whose work they describe. */
  const selectedIntentID = useMemo(() => {
    if (!selected) return null
    if (selected.startsWith('s:')) return selected.slice(2)
    const edge = model.edgesById.get(selected)
    if (edge?.intent?.id) return edge.intent.id
    return model.byId.get(selected)?.meta?.intent?.id ?? null
  }, [selected, model])

  const focusLayer = useFocusLayer(selectedIntentID, focusOverlay && Boolean(selectedIntentID))

  // "Only the current chain" cannot outlive its anchor: clearing the selection
  // restores the full drawing instead of leaving a checked-but-inert switch.
  useEffect(() => {
    if (!selected && filters.focusOnly) setFilters((current) => ({ ...current, focusOnly: false }))
  }, [selected, filters.focusOnly])

  // Hover pulse for the inspector: a running direction shows idle time/context
  // pressure while the pointer rests on it.
  const pulseFor = useCallback(
    (intentID: string) => board?.running_intents.find((item) => item.id === intentID)?.pulse,
    [board],
  )

  const loadedNodes = graph.data
    ? graph.data.facts.length + graph.data.intents.length + graph.data.goals.length
    : 0
  const totalNodes = graph.data?.total ?? 0
  const snapshotVersion = graph.data?.snapshot_version
  const watermark = changes.data?.high_watermark
  const hasNewChanges = Boolean(
    snapshotVersion &&
      watermark &&
      (watermark.epoch !== snapshotVersion.epoch || watermark.seq > snapshotVersion.seq),
  )

  function refresh() {
    graph.reload()
    onRefresh()
  }

  function handleCanvasSelect(id: string | null) {
    // Opening a node means "show me this", so the chat dock yields the right
    // side instead of sitting on top of the inspector.
    setChatDock(false)
    if (!id) {
      onSelect(null)
      return
    }
    // A boundary node is a not-yet-loaded reference: ask the snapshot for its
    // neighborhood instead of pinning a node the model does not have.
    const picked = model.byId.get(id)
    if (picked?.boundary) {
      void graph.focus(picked.boundary)
      return
    }
    onSelect(id)
  }

  const paused = Boolean(board?.project_paused || board?.decider.paused)
  const running = board?.intents.running ?? 0

  return (
    <Page
      title="图谱"
      subtitle={
        <>
          {projectName ?? current}
          <span className="mx-1.5 text-muted-foreground/50">·</span>
          <span className="font-mono text-[11px]">{current}</span>
        </>
      }
      scroll={false}
      className="relative"
      actions={
        <div className="flex flex-wrap items-center justify-end gap-x-2 gap-y-1 text-[11px]">
          {/* Literal prefix kept: the acceptance script waits for 快照已载. */}
          <span className="whitespace-nowrap">
            快照已载 {loadedNodes}/{totalNodes} 节点
          </span>
          {/* The one place the project token total lives now (docs/34 §3): the
              whole project, including cached input, reported by the model. */}
          <ProjectTokens usage={board?.usage} />
          {hasNewChanges && (
            <Button
              size="sm"
              variant="soft"
              onClick={refresh}
              title="账本已有新变化。刷新会重取一份新快照；旧快照的页面不会被静默拼接。"
            >
              有新变化，点击刷新
            </Button>
          )}
          {graph.data?.has_more && (
            <>
              <span className="max-w-[220px] text-[10px] leading-tight text-muted-foreground">
                图是快照，未载入的部分在账本里，点击加载更多
              </span>
              <Button size="sm" variant="ghost" disabled={graph.busy} onClick={() => void graph.more()}>
                加载更多
              </Button>
            </>
          )}
          {graph.root && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                onSelect(null)
                void graph.focus('')
              }}
            >
              全图概览
            </Button>
          )}
          <a
            className="text-info"
            href={`/p/${encodeURIComponent(current)}/api/graph/export`}
            download
            title="导出账本里的全图（不是当前快照页）"
          >
            导出全图
          </a>
          <Button size="sm" variant="ghost" onClick={refresh} title="读取最新版本并刷新图谱">
            <RefreshCw className="h-3.5 w-3.5" />
          </Button>
        </div>
      }
    >
      {/* The canvas is the first and only full-bleed child of the page body. At
          390px the other cards wrap around it instead of taking its place. */}
      <GraphCanvas
        ref={canvas}
        model={model}
        selected={selected}
        matches={matches}
        showLabels={showLabels}
        layout={layout}
        // One camera per project: a reload or a visit to another panel comes
        // back to the same pan/zoom instead of the opening centre.
        cameraKey={cameraStorageKey(current)}
        ready={graphReady}
        focusLayer={focusLayer}
        onSelect={handleCanvasSelect}
      />

      <GraphFilters
        current={current}
        q={q}
        onQueryChange={setQ}
        matchCount={matches ? matches.size : null}
        counts={board?.facts.by_kind ?? {}}
        filters={filters}
        onFiltersChange={setFilters}
        onFocusFact={(factID) => {
          // Whole-ledger search re-roots the snapshot at the fact, keeping the
          // historical behaviour: it does not pin a selection.
          void graph.focus(`fact:${factID}`)
        }}
        hasSelection={Boolean(selected)}
      />

      <GraphToolbar
        chatDock={chatDock}
        onToggleChatDock={() => {
          setChatDock((open) => !open)
          onSelect(null)
        }}
        layout={layout}
        onLayout={chooseLayout}
        showLabels={showLabels}
        onToggleLabels={() => setShowLabels((value) => !value)}
        listView={listView}
        onToggleListView={() => setListView((value) => !value)}
        focusOverlay={focusOverlay}
        focusOverlayAvailable={Boolean(selectedIntentID)}
        onToggleFocusOverlay={() => setFocusOverlay((value) => !value)}
        onZoom={(factor) => canvas.current?.zoom(factor)}
        onFit={() => canvas.current?.fit()}
        onReheat={() => canvas.current?.reheat()}
      />

      {/* goals + legend + live commander card: the bottom-left corner is
          "where are we going and what does the colour mean". Open goals are not
          graph nodes, so they live here; an achieved goal becomes a target node
          on the map. The legend (T7) starts collapsed because it is a reference,
          not the content. */}
      <div className="pointer-events-none absolute bottom-3 left-3 flex max-w-[calc(100vw-1.5rem)] flex-col gap-2">
        {openGoals.length > 0 && (
          <div className="pointer-events-auto max-w-[268px] rounded-lg border border-border bg-card/85 px-2.5 py-1.5 text-[11px] shadow-lifted backdrop-blur">
            <button
              type="button"
              onClick={() => setGoalsOpen((v) => !v)}
              className="flex w-full items-center gap-1.5 text-left"
              title={goalsOpen ? '收起未达成目标' : '展开全部未达成目标'}
            >
              <Crosshair className="h-3 w-3 shrink-0 text-warning" />
              <span className="font-semibold text-foreground">未达成目标 · {openGoals.length}</span>
              <ChevronDown
                className={`ml-auto h-3 w-3 text-muted-foreground transition-transform ${goalsOpen ? '' : '-rotate-90'}`}
              />
            </button>
            <div className={goalsOpen ? 'max-h-52 overflow-y-auto' : ''}>
              {(goalsOpen ? openGoals : openGoals.slice(0, 3)).map((g) => (
                <div key={g.id} className="tw-break mt-0.5 text-[10.5px] text-muted-foreground" title={g.statement}>
                  {g.statement}
                </div>
              ))}
            </div>
            {!goalsOpen && openGoals.length > 3 && (
              <div className="mt-0.5 text-[10px] text-muted-foreground">…还有 {openGoals.length - 3} 条</div>
            )}
            <div className="mt-0.5 text-[10px] text-muted-foreground/80">
              目标不上图；达成后作为终点接到证据事实上
            </div>
          </div>
        )}

        <GraphLegend />

        <CommanderCard board={board} onOpen={() => onNavigate('decisions')} />
      </div>

      {/* live */}
      <div className="absolute bottom-3 right-3 flex items-center gap-1.5 rounded-full border border-border bg-card/85 px-2.5 py-1 text-[11px] text-muted-foreground backdrop-blur">
        <span className={`h-1.5 w-1.5 rounded-full ${running > 0 ? 'animate-pulse bg-info' : 'bg-muted-foreground'}`} />
        {running > 0 ? `${running} 个意图在跑` : '事实意图图每 6 秒刷新'}
      </div>

      {/* Minimap: its own canvas (the acceptance script finds the main canvas by
          data-testid, not by "first canvas"). Hidden while a right-side panel
          owns that corner. */}
      {!node && !chatDock && !listView && (
        <div className="absolute bottom-12 right-3 hidden sm:block">
          <GraphMinimap model={model} onCenter={(x, y) => canvas.current?.centerOn(x, y)} />
        </div>
      )}

      {!chatDock && node && (
        <Suspense fallback={null}>
          <GraphNodePanel
            node={node}
            facts={graph.data?.facts ?? []}
            runs={runs.data ?? []}
            onSelectFact={(id) => {
              if (!model.byId.get(`f:${id}`)?.meta?.fact) void graph.focus(`fact:${id}`)
              onSelect(`f:${id}`)
            }}
            onClose={() => onSelect(null)}
            onChanged={refresh}
            childRefresh={childRefresh}
            onChildIntent={(parent) => setChildParent(parent)}
          />
        </Suspense>
      )}

      {/* Hover is published by the canvas (T5's view-store) and subscribed only
          by this inspector: moving the pointer no longer re-renders GraphPanel.
          It overlays the pinned panel, which is what the px-identical card used
          to do by replacing it. */}
      {!chatDock && !listView && (
        <GraphHoverInspector model={model} selected={selected} pulseFor={pulseFor} />
      )}

      {chatDock && (
        <GraphChatDock
          paused={paused}
          runningExecutors={running}
          onSent={refresh}
          onClose={() => setChatDock(false)}
          onOpenConversation={() => {
            setChatDock(false)
            onNavigate('messages')
          }}
        />
      )}

      {/* List view is the accessible equivalent of the canvas. It overlays the
          canvas (its own root is absolute inset-0) instead of replacing it, so
          the camera/simulation survive the toggle and Esc can return focus to
          the still-mounted main canvas. */}
      {listView && (
        <GraphListView
          model={model}
          selected={selected}
          matches={matches}
          onSelect={handleCanvasSelect}
          onClose={() => setListView(false)}
        />
      )}

      {!model.nodes.some((n) => n.type !== 'project') && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
          <div className="pointer-events-auto flex w-[320px] flex-col items-center gap-2 rounded-lg border border-border bg-card/90 p-5 text-center shadow-lifted backdrop-blur">
            <Crosshair className="h-6 w-6 text-muted-foreground/50" />
            <div className="text-ui font-semibold text-foreground">这个项目还是空的</div>
            <div className="text-[12px] leading-relaxed text-muted-foreground">
              点「开始运行」让指挥官按项目简报划分目标并派发；项目简报和内置约束可在「概览」里补改，也可以手动新建意图。
            </div>
            <div className="mt-1 flex gap-2">
              <Button size="xs" variant="outline" onClick={() => onNavigate('overview')}>
                打开概览
              </Button>
              <Button size="xs" onClick={onCreateIntent}>
                <Plus className="h-3 w-3" /> 新建意图
              </Button>
            </div>
          </div>
        </div>
      )}

      {model.nodes.some((n) => n.type !== 'project') && (graph.data?.facts ?? []).length === 0 && (
        <div className="pointer-events-none absolute bottom-16 left-1/2 -translate-x-1/2">
          <div className="rounded-full border border-border bg-card/90 px-3 py-1 text-[11px] text-muted-foreground shadow-lifted backdrop-blur">
            执行者还没有交接事实：写 `.tw/facts.jsonl` 后，事实会以圆点出现在意图周围
          </div>
        </div>
      )}

      {/* Child dialog: rendered here, not in the shell, because it is opened from
          the node inspector this panel owns. */}
      {childParent && (
        <CreateChildIntentDialog
          open
          onOpenChange={(v) => {
            if (!v) setChildParent(null)
          }}
          parent={childParent}
          onCreated={() => {
            setChildRefresh((n) => n + 1)
            refresh()
          }}
        />
      )}
    </Page>
  )
}
