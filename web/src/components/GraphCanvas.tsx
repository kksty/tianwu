import { forwardRef, useEffect, useImperativeHandle, useRef } from 'react'
import { edgeKindLabel, focusPath, type GraphEdge, type GraphModel, type GraphNode } from '@/graph/model'
import {
  ForceSim,
  createMoveCoalescer,
  radiusOf,
  warmTicks,
  warmWithin,
  type LayoutMode,
  type MoveCoalescer,
} from '@/graph/sim'
import { PILL_MAX_LINES, PILL_TEXT_W, fitText, halfExtentX, paint, wrapText, type Camera, type FocusTemperatures } from '@/graph/render'
import { layoutBounds, openCamera, parseCamera, type NodeExtent } from '@/graph/camera'
import { GraphIndex, edgeHitTest, nodeHitTest } from '@/graph/hit'
import {
  createFramePublisher,
  setHovered,
  setView,
  type FramePublisher,
  type GraphView,
} from '@/graph/view-store'

export interface GraphControls {
  fit: () => void
  reheat: () => void
  zoom: (factor: number) => void
  /** Centre the camera on a graph-space point (minimap click/drag). */
  centerOn: (x: number, y: number) => void
  /** Set the zoom around the current viewport centre (minimap/toolbar). */
  zoomTo: (k: number) => void
}

/** Screen-space pointer slop when picking a node (graph units at zoom 1). */
const HIT_PAD_PX = 6
/** Screen-space tolerance when picking an edge curve. */
const HIT_EDGE_TOL_PX = 12
/** Fingers are fatter and less precise than a cursor (also used for coarse pointers). */
const TOUCH_HIT_SCALE = 2.2
/** Redraw interval for a running edge over a settled layout (30fps). */
const BREATHING_FRAME_MS = 33
/** How much of a frame the settle loop may spend ticking before it paints. */
const TICK_FRAME_BUDGET_MS = 12
/** Hard cap so a fast tick cannot starve the paint on small graphs. */
const MAX_TICKS_PER_FRAME = 8
/** How long a node that just appeared scales/ripples in. */
const RIPPLE_MS = 1100

interface Props {
  model: GraphModel
  selected: string | null
  matches: Set<string> | null
  /** Show every intent label, not just the focused/running ones. */
  showLabels: boolean
  /** Force map or left-to-right penetration layers. */
  layout: LayoutMode
  /** localStorage key for the camera. With one, the operator's pan/zoom survives
   *  a reload and a visit to another panel; without one the view is per-instance. */
  cameraKey?: string
  /** False while the ledger snapshot is still loading: the opening centre must
   *  not frame a half-built layout (the saved camera, if any, is unaffected). */
  ready?: boolean
  /** Optional focus layer (P1-1): the canvas only draws it, it does not compute it. */
  focusLayer?: FocusTemperatures | null
  onSelect: (id: string | null) => void
}

interface DragState {
  mode: 'pan' | 'node' | null
  id?: string
  sx: number
  sy: number
  cx: number
  cy: number
  moved: boolean
}

const NO_DRAG: DragState = { mode: null, sx: 0, sy: 0, cx: 0, cy: 0, moved: false }

/**
 * One canvas, one camera: the engagement graph.
 *
 * Interaction state lives in refs, not React state, for two reasons: a hover or
 * a pan must not re-render GraphView (the inspector/minimap subscribe to the
 * view-store instead), and the canvas is repainted from an animation-frame loop
 * that cannot wait for a React render. Props are mirrored into a ref each render
 * so the frame loop always paints the current selection.
 */
export const GraphCanvas = forwardRef<GraphControls, Props>(function GraphCanvas(
  { model, selected, matches, showLabels, layout, cameraKey, ready = true, focusLayer, onSelect },
  ref,
) {
  const wrapRef = useRef<HTMLDivElement | null>(null)
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const simRef = useRef<ForceSim | null>(null)
  const camRef = useRef<Camera>({ x: 0, y: 0, k: 1 })
  // The animation-frame loop that settles the simulation / animates ripples.
  const rafRef = useRef(0)
  // A pan (or a zoom step) only needs one paint per frame; pointermove schedules
  // it, and several moves in the same frame share it.
  const paintRafRef = useRef(0)
  // Camera/size publishing to the minimap is coalesced the same way: one publish
  // per frame, latest camera wins.
  const publishRef = useRef<FramePublisher<GraphView> | null>(null)
  // When the loop last painted idle breathing, for the 30fps cap.
  const lastPaintRef = useRef(0)
  // The saved camera (or, on a first visit, the fixed-zoom centre) is decided
  // once. After that the camera belongs to the operator: polls, resizes and
  // layout switches must not move it (no fit-to-canvas).
  const centeredRef = useRef(false)
  const sizeRef = useRef({ w: 0, h: 0, dpr: 1 })
  const saveTimerRef = useRef(0)
  // A throttle is pending: the view changed but localStorage has not caught up.
  const saveDirtyRef = useRef(false)
  // Node id → when it first appeared on this canvas. New facts/steps ripple and
  // scale in for a second, then the map is pruned.
  const appearRef = useRef(new Map<string, number>())
  // performance.now() at which the last ripple ends; lets the loop skip walking
  // the appearance map every frame.
  const rippleUntilRef = useRef(0)
  // Honours prefers-reduced-motion: false disables comets, pulses and ripples.
  const motionRef = useRef(true)
  // A coarse pointer / touch pointer widens the hit slop.
  const coarseRef = useRef(false)
  const lastPointerTypeRef = useRef('mouse')
  // Last touch tap (for double-tap zoom, which mobile browsers may not deliver
  // as a dblclick event).
  const lastTapRef = useRef<{ at: number; x: number; y: number } | null>(null)
  // Pointer is over (or dragging) this node/edge id; published to the view-store.
  const hoverRef = useRef<string | null>(null)
  // Arrow-key focus node; drawn as a ring by the renderer, never a React state.
  const keyboardFocusRef = useRef<string | null>(null)
  const dragRef = useRef<DragState>(NO_DRAG)
  const stateRef = useRef({ selected, matches, showLabels, focusLayer })
  stateRef.current = { selected, matches, showLabels, focusLayer }

  // Multi-touch pan/zoom: live touches and the pinch anchor (world point under
  // the midpoint of the two fingers when the gesture started).
  const touchesRef = useRef(new Map<number, { x: number; y: number }>())
  const pinchRef = useRef<{ dist: number; worldX: number; worldY: number; baseK: number } | null>(null)

  // A pointermove fires several times per frame. Hit-testing every sample (every
  // node, then every intent/brief edge) was the hot path while hovering a large
  // map; one test per painted frame sees the same picture.
  const hoverMoveRef = useRef<MoveCoalescer | null>(null)
  // Derived from the current simulation, but pure: id → node and the focus path
  // change only when the sim, the selection or the hover changes. Rebuilding them
  // on every animation frame was pure garbage.
  const derivedRef = useRef<{
    sim: ForceSim | null
    byId: Map<string, GraphNode>
    focusID: string | null | undefined
    focus: Set<string> | null
  }>({ sim: null, byId: new Map(), focusID: undefined, focus: null })
  // Pill widths are a constant of (font, text); a poll keeps almost every label,
  // so re-shaping all of them on every rebuild was wasted main-thread time.
  const measureRef = useRef(new Map<string, { w: number; h: number; lines: string[] }>())
  // Lazily-built spatial index for the settled layout (docs/33 §4.5). Null while
  // the simulation runs or a node is dragged: positions move every frame then, so
  // hit-testing falls back to the linear scan.
  const indexRef = useRef<{ sim: ForceSim; index: GraphIndex } | null>(null)
  // Cached "does this sim have a running edge" flag; edges are fixed per sim.
  const breathingRef = useRef<{ sim: ForceSim | null; value: boolean }>({ sim: null, value: false })
  // Keyboard traversal order for the current sim (depth, y, x): deterministic.
  const keyboardOrderRef = useRef<{ sim: ForceSim | null; nodes: GraphNode[] }>({ sim: null, nodes: [] })

  // One coalescer for the canvas lifetime: it owns the pending pointer frame.
  if (hoverMoveRef.current === null) {
    hoverMoveRef.current = createMoveCoalescer((x, y) => applyHover(x, y))
  }
  // One publisher for the canvas lifetime: it owns the pending camera publish.
  if (publishRef.current === null) {
    publishRef.current = createFramePublisher<GraphView>((view) => setView(view))
  }

  // Rebuild the simulation when the ledger or the layout changes, keeping node
  // positions so a poll does not make the picture jump. A layout switch resets
  // them: the new anchors are the layout.
  useEffect(() => {
    const prev = simRef.current
    const prevNodes = prev && prev.mode === layout ? prev.nodes : null
    measureNodes(model)
    // The chain-tree view derives every x/y from the model (placeLayered), so a
    // poll has to rebuild its anchors instead of pinning yesterday's coordinates
    // on top of them. The force map's positions are free, and those are the ones
    // worth carrying across a poll so the picture does not jump.
    const prior = prevNodes ? new Set(prevNodes.map((n) => n.id)) : null
    const kept = new Map<string, { x: number; y: number }>()
    if (prevNodes && layout === 'force') {
      for (const n of prevNodes) kept.set(n.id, { x: n.x, y: n.y })
    }
    const sim = new ForceSim(model, { mode: layout })
    if (kept.size > 0) {
      for (const n of sim.nodes) {
        const old = kept.get(n.id)
        if (old) {
          n.x = old.x
          n.y = old.y
          n.vx = 0
          n.vy = 0
        }
      }
    }
    // Only nodes this poll introduced have to be pulled into place; everything
    // else already sits where the operator saw it.
    const added = prior
      ? sim.nodes.reduce((count, n) => count + (prior.has(n.id) ? 0 : 1), 0)
      : sim.nodes.length
    // Record new node appearances; the very first paint marks everything as old
    // so opening a project does not ripple all of its history.
    const now = performance.now()
    const seen = appearRef.current
    const next = new Map<string, number>()
    let rippleUntil = 0
    for (const n of model.nodes) {
      const at = prev ? seen.get(n.id) ?? now : 0
      next.set(n.id, at)
      if (at > 0 && at + RIPPLE_MS > rippleUntil) rippleUntil = at + RIPPLE_MS
    }
    appearRef.current = next
    rippleUntilRef.current = rippleUntil

    // A rebuilt sim means every cached hit/adjacency/keyboard derivation is for
    // the old layout.
    simRef.current = sim
    invalidateIndex()
    derivedRef.current = { sim: null, byId: new Map(), focusID: undefined, focus: null }
    breathingRef.current = { sim: null, value: false }
    keyboardOrderRef.current = { sim: null, nodes: [] }

    // Settle the layout before the first paint: a background tab may never get
    // animation frames, and the picture must already look like a graph. The old
    // fixed warm blocked the main thread for hundreds of milliseconds on a large
    // map; the warm is now a bounded stall (docs/33 §4.4) and the frame loop
    // finishes whatever the budget could not.
    if (layout === 'layered' || added === 0) {
      // The chain tree is deterministic, and a poll that added nothing must not
      // animate a settled picture. warmTicks() also returns 0 for that case.
      sim.alpha = 0
    } else {
      const warm = warmTicks(prev, model, layout)
      if (warm <= 0) sim.alpha = 0
      else warmWithin(sim, { maxTicks: warm })
    }

    // Opening view only. A later rebuild came from a poll or the operator
    // switching layout, and either way the camera stays where it was.
    if (!centeredRef.current) centeredRef.current = openingCamera()
    start()
    requestPublish()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [model, layout])

  // A queued hover test must not fire after the canvas is gone, and the frame
  // loop/paint publishes must not outlive it either.
  useEffect(
    () => () => {
      hoverMoveRef.current?.cancel()
      cancelAnimationFrame(rafRef.current)
      rafRef.current = 0
      if (paintRafRef.current) cancelAnimationFrame(paintRafRef.current)
      paintRafRef.current = 0
      publishRef.current?.cancel()
      // Nobody is hovering a canvas that no longer exists.
      if (hoverRef.current !== null) {
        hoverRef.current = null
        setHovered(null)
      }
    },
    [],
  )

  // prefers-reduced-motion is an accessibility setting, not a style choice: the
  // graph keeps all of its information, it just stops moving.
  useEffect(() => {
    const mq = window.matchMedia('(prefers-reduced-motion: reduce)')
    const sync = () => {
      motionRef.current = !mq.matches
      paintOnce()
    }
    sync()
    mq.addEventListener('change', sync)
    return () => mq.removeEventListener('change', sync)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // A touch/coarse pointer gets a larger hit slop.
  useEffect(() => {
    const mq = window.matchMedia?.('(pointer: coarse)')
    if (!mq) return
    const sync = () => {
      coarseRef.current = mq.matches
    }
    sync()
    mq.addEventListener('change', sync)
    return () => mq.removeEventListener('change', sync)
  }, [])

  useEffect(() => {
    const el = wrapRef.current
    if (!el) return
    // Browser zoom changes the CSS size *and* devicePixelRatio without
    // necessarily firing one clean resize, so every path that can change the
    // canvas geometry funnels here: ResizeObserver, window resize, visual
    // viewport (pinch) and the dpr media query.
    const sync = () => syncSize()
    const ro = new ResizeObserver(sync)
    ro.observe(el)
    window.addEventListener('resize', sync)
    window.visualViewport?.addEventListener('resize', sync)
    let mq: MediaQueryList | null = null
    const watchDpr = () => {
      mq?.removeEventListener('change', watchDpr)
      mq = window.matchMedia(`(resolution: ${window.devicePixelRatio || 1}dppx)`)
      mq.addEventListener('change', watchDpr)
      sync()
    }
    watchDpr()
    sync()
    return () => {
      ro.disconnect()
      window.removeEventListener('resize', sync)
      window.visualViewport?.removeEventListener('resize', sync)
      mq?.removeEventListener('change', watchDpr)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // The camera outlives this canvas: opening another panel unmounts it, and a
  // reload closes the page. Flush the last view on either way out, so coming
  // back lands exactly where the operator left off.
  useEffect(() => {
    const flush = () => {
      if (saveTimerRef.current) {
        window.clearTimeout(saveTimerRef.current)
        saveTimerRef.current = 0
      }
      // Only an un-flushed operator move is worth writing: an untouched canvas
      // must not freeze its opening centre over a layout that has since grown.
      if (saveDirtyRef.current) saveCamera()
    }
    window.addEventListener('pagehide', flush)
    return () => {
      window.removeEventListener('pagehide', flush)
      flush()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cameraKey])

  // The last poll can settle without changing the model (a failed goals fetch,
  // for example), so readiness alone may be what unblocks the opening camera.
  useEffect(() => {
    if (!ready || centeredRef.current) return
    centeredRef.current = openingCamera()
    paintOnce()
    requestPublish()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready])

  // Props that change what is drawn (but not the layout) repaint directly; hover
  // is handled internally and does not go through React at all.
  useEffect(() => {
    paintOnce()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected, matches, showLabels, focusLayer])

  /** Resize the backing store to the CSS box at the current dpr. Keeps the view
   *  centred when the geometry changes (browser zoom), so the picture grows with
   *  the page instead of drifting off the canvas. */
  function syncSize() {
    const el = wrapRef.current
    const c = canvasRef.current
    if (!el || !c) return
    const rect = el.getBoundingClientRect()
    if (rect.width < 1 || rect.height < 1) return
    // Cap only for pathological dpr; 3 keeps 300% browser zoom crisp.
    const dpr = Math.max(1, Math.min(window.devicePixelRatio || 1, 3))
    const prev = sizeRef.current
    const bw = Math.max(1, Math.round(rect.width * dpr))
    const bh = Math.max(1, Math.round(rect.height * dpr))
    sizeRef.current = { w: rect.width, h: rect.height, dpr }
    if (c.width !== bw || c.height !== bh) {
      c.width = bw
      c.height = bh
    }
    c.style.width = `${rect.width}px`
    c.style.height = `${rect.height}px`
    if (prev.w > 0 && (prev.w !== rect.width || prev.h !== rect.height)) {
      // Keep whatever was at the viewport centre at the centre.
      camRef.current.x += (rect.width - prev.w) / 2
      camRef.current.y += (rect.height - prev.h) / 2
      saveCameraSoon()
    }
    // A resize never reframes the graph: the centre-preserving shift above is
    // the whole response, so browser zoom does not undo the operator's view.
    if (!centeredRef.current) centeredRef.current = openingCamera()
    paintOnce()
    requestPublish()
  }

  /** id → node for the current simulation, rebuilt only when the sim is. */
  function indexOf(sim: ForceSim): Map<string, GraphNode> {
    const derived = derivedRef.current
    if (derived.sim !== sim) {
      derived.sim = sim
      derived.byId = new Map(sim.nodes.map((n) => [n.id, n]))
      derived.focusID = undefined
      derived.focus = null
    }
    return derived.byId
  }

  /** What the selection/hover dims to, cached until the subject changes. */
  function focusOf(sim: ForceSim, selectedID: string | null, hoveredID: string | null): Set<string> | null {
    const derived = derivedRef.current
    // Also (re)builds byId and drops the previous sim's focus when it changed.
    indexOf(sim)
    const id = selectedID ?? hoveredID
    if (derived.focusID !== id) {
      derived.focusID = id
      derived.focus = focusPath(sim, id)
    }
    return derived.focus
  }

  /** Does this layout contain a running intent edge? (Edges are fixed per sim.) */
  function hasRunningEdge(sim: ForceSim): boolean {
    const cached = breathingRef.current
    if (cached.sim === sim) return cached.value
    const value = sim.edges.some((e) => e.kind === 'intent' && e.status === 'running')
    breathingRef.current = { sim, value }
    return value
  }

  /** Which time-based animations are currently running (reduced motion: none). */
  function animations(now: number): { breathing: boolean; rippling: boolean } {
    const sim = simRef.current
    if (!sim || !motionRef.current) return { breathing: false, rippling: false }
    return { breathing: hasRunningEdge(sim), rippling: now < rippleUntilRef.current }
  }

  /**
   * True while the layout and the pointer are at rest: geometry can be cached,
   * the spatial index is valid, and the renderer may reuse its labels. A pan is
   * "settled" (nodes do not move); a node drag is not.
   */
  function isSettled(): boolean {
    const sim = simRef.current
    if (!sim) return false
    const { breathing, rippling } = animations(performance.now())
    return !sim.running && dragRef.current.mode === null && !breathing && !rippling
  }

  function invalidateIndex() {
    indexRef.current = null
  }

  /** Build the spatial index for a settled layout on first use (never per frame). */
  function ensureIndex(sim: ForceSim): GraphIndex {
    const cached = indexRef.current
    if (cached && cached.sim === sim) return cached.index
    const index = new GraphIndex(sim.nodes, sim.edges, { layered: sim.mode === 'layered' })
    indexRef.current = { sim, index }
    return index
  }

  function paintOnce() {
    const canvas = canvasRef.current
    const ctx = canvas?.getContext('2d')
    const sim = simRef.current
    if (!canvas || !ctx || !sim) return
    const { w, h, dpr } = sizeRef.current
    if (w === 0 || h === 0) return
    const now = performance.now()
    const { breathing, rippling } = animations(now)
    const state = stateRef.current
    // A settled frame lets the renderer reuse edge geometry and label buckets;
    // while the sim runs, a node is dragged or an animation is playing, it
    // rebuilds them (the canvas only reports the state, render decides).
    const settled = !sim.running && dragRef.current.mode === null && !breathing && !rippling
    canvas.style.cursor =
      dragRef.current.mode === 'pan' ? 'grabbing' : hoverRef.current ? 'pointer' : 'grab'
    paint({
      ctx,
      width: w,
      height: h,
      dpr,
      cam: camRef.current,
      nodes: sim.nodes,
      edges: sim.edges,
      byId: indexOf(sim),
      selected: state.selected,
      hovered: hoverRef.current,
      matches: state.matches,
      focus: focusOf(sim, state.selected, hoverRef.current),
      showLabels: state.showLabels,
      time: now,
      layered: sim.mode === 'layered',
      motion: motionRef.current,
      appear: appearRef.current,
      bands: sim.depthBands,
      settled,
      focusLayer: state.focusLayer,
      keyboardFocus: keyboardFocusRef.current,
    })
  }

  /** Publish cam + size to the view-store, at most once per animation frame. */
  function requestPublish() {
    publishRef.current?.publish({
      camera: camRef.current,
      width: sizeRef.current.w,
      height: sizeRef.current.h,
    })
  }

  /** One coalesced paint, for pointer paths that must not paint per event. */
  function schedulePaint() {
    if (paintRafRef.current) return
    paintRafRef.current = requestAnimationFrame(() => {
      paintRafRef.current = 0
      paintOnce()
    })
  }

  function start() {
    cancelAnimationFrame(rafRef.current)
    rafRef.current = requestAnimationFrame(loop)
  }

  function loop() {
    rafRef.current = 0
    const sim = simRef.current
    if (!sim) return
    const now = performance.now()
    const { breathing, rippling } = animations(now)
    const dragging = dragRef.current.mode === 'node'
    if (!sim.running && !dragging && !breathing && !rippling) {
      paintOnce()
      return
    }
    if (sim.running || dragging) {
      // Settle with a per-frame budget instead of exactly one tick: a fast graph
      // (small n) converges in a few hundred ms, a 4k-node Barnes–Hut tick is
      // ~8ms so it still runs one or two per frame. The paint always happens, so
      // the operator sees the layout relax rather than a frozen first frame.
      const deadline = performance.now() + TICK_FRAME_BUDGET_MS
      let ticks = 0
      while (sim.running && ticks < MAX_TICKS_PER_FRAME && performance.now() < deadline) {
        sim.tick()
        ticks++
      }
      if (ticks === 0) sim.tick()
      // Positions moved: the settled-layout spatial index is stale.
      invalidateIndex()
    }
    // A running edge on a settled map is a pulse, not motion: 30fps is plenty,
    // and it keeps a 5k-node paint from eating the whole frame every frame.
    const idlePulse = !sim.running && !dragging && !rippling
    if (idlePulse && now - lastPaintRef.current < BREATHING_FRAME_MS) {
      rafRef.current = requestAnimationFrame(loop)
      return
    }
    lastPaintRef.current = now
    paintOnce()
    rafRef.current = requestAnimationFrame(loop)
  }

  /**
   * Pill boxes come from the real text, wrapped rather than clipped: a long CJK
   * title becomes 2–3 lines inside a compact pill, so the chain-tree columns
   * stay close together and no label is cut at the pill edge. Height is derived
   * from the line count; the layered layout already sizes rows from node h.
   */
  function measureNodes(model: GraphModel) {
    const ctx = canvasRef.current?.getContext('2d')
    if (!ctx) return
    const cache = measureRef.current
    // The label set is bounded by the engagement's history, but a cache miss is
    // always cheaper than unbounded growth: drop it and re-measure next rebuild.
    if (cache.size > 4096) cache.clear()
    const TITLE_FONT = `600 12px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
    const MODEL_FONT = `500 11px "JetBrains Mono","Cascadia Code",monospace`
    const LINE_H = 16
    const MODEL_H = 15
    const V_PAD = 10
    for (const n of model.nodes) {
      if (n.type === 'fact' || n.type === 'ghost') continue
      // A pill's box depends on its text and, on a step, on the model line.
      const key = `${n.type}\u0000${n.label}\u0000${n.type === 'step' ? (n.model ?? '') : ''}`
      const cached = cache.get(key)
      if (cached) {
        n.w = cached.w
        n.h = cached.h
        n.labelLines = cached.lines
        continue
      }
      // Wrap only the drawn box types; a force-map step is a diamond and has no
      // label inside it.
      ctx.font = TITLE_FONT
      const wraps = n.type === 'step' || n.type === 'intent' || n.type === 'goal' || n.type === 'project'
      const lines = wraps ? wrapText(ctx, n.label, PILL_TEXT_W, PILL_MAX_LINES) : [n.label]
      let maxLineW = 0
      for (const line of lines) maxLineW = Math.max(maxLineW, ctx.measureText(line).width)
      const hasModel = n.type === 'step' && Boolean(n.model)
      if (hasModel) {
        ctx.font = MODEL_FONT
        maxLineW = Math.max(maxLineW, ctx.measureText(fitText(ctx, n.model!, PILL_TEXT_W)).width)
      }
      if (n.type === 'step') {
        // The chain-tree layout draws a step as a labelled pill: title lines plus
        // the model line. The force map ignores w/h for steps and keeps the
        // diamond, so measuring the wider two-line box only widens the columns.
        n.w = Math.max(40, Math.min(maxLineW / 2 + 12, 140))
        n.h = (lines.length * LINE_H + (hasModel ? MODEL_H : 0) + V_PAD) / 2
      } else {
        n.w = Math.max(46, Math.min(maxLineW / 2 + 12, 140))
        n.h = (lines.length * LINE_H + V_PAD) / 2
      }
      n.labelLines = lines
      cache.set(key, { w: n.w, h: n.h, lines })
    }
  }

  /** Drawn footprint of every node, label margin included: what framing and the
   *  opening camera measure. */
  function nodeExtents(nodes: GraphNode[], layered: boolean): NodeExtent[] {
    return nodes.map((n) => {
      const halfW = Math.max(radiusOf(n), halfExtentX(n, layered))
      // Facts and ghosts are dots; everything else draws a box of half-height h.
      const halfH = Math.max(radiusOf(n), n.type === 'fact' || n.type === 'ghost' ? radiusOf(n) : n.h)
      return { x: n.x, y: n.y, halfW: halfW + 30, halfH: halfH + 30 }
    })
  }

  /**
   * Place the camera for the first layout that has a canvas to live in: layout
   * centre at the viewport centre, at the fixed opening zoom. Returns false
   * while either the layout or the canvas size is still missing, so the caller
   * can retry when it arrives.
   */
  function centerOnOpen(): boolean {
    const sim = simRef.current
    const { w, h } = sizeRef.current
    if (!sim) return false
    const cam = openCamera(nodeExtents(sim.nodes, sim.mode === 'layered'), w, h)
    if (!cam) return false
    camRef.current = cam
    return true
  }

  /**
   * The camera this canvas opens with: the operator's saved view for this
   * project when there is one, otherwise the fixed-zoom centre. Called once;
   * returns false while it still has to wait for data or a canvas size.
   *
   * The opening centre is not persisted: it is derived from the layout, and a
   * reload should derive it from the same complete layout rather than freeze a
   * half-loaded one. Only the operator's own moves are saved.
   */
  function openingCamera(): boolean {
    const saved = readSavedCamera()
    if (saved) {
      camRef.current = saved
      return true
    }
    if (!ready) return false
    return centerOnOpen()
  }

  function readSavedCamera(): Camera | null {
    if (!cameraKey) return null
    try {
      return parseCamera(window.localStorage.getItem(cameraKey))
    } catch {
      /* storage blocked: nothing was saved, fall back to the opening centre */
      return null
    }
  }

  function saveCamera() {
    // Nothing to remember until this canvas has an opening camera: before that
    // camRef is still the placeholder and a leave-before-data would freeze it.
    if (!cameraKey || !centeredRef.current) return
    saveDirtyRef.current = false
    try {
      window.localStorage.setItem(cameraKey, JSON.stringify(camRef.current))
    } catch {
      /* storage unavailable or full: the view just will not survive a reload */
    }
  }

  /** Wheel and resize fire many times per second; write once they pause. */
  function saveCameraSoon() {
    if (!cameraKey || !centeredRef.current) return
    saveDirtyRef.current = true
    if (saveTimerRef.current) window.clearTimeout(saveTimerRef.current)
    saveTimerRef.current = window.setTimeout(() => {
      saveTimerRef.current = 0
      saveCamera()
    }, 180)
  }

  /** Explicit fit: toolbar button and double-click. The only code that scales
   *  the camera to the canvas — data refreshes never call it. */
  function fit() {
    const sim = simRef.current
    const { w, h } = sizeRef.current
    if (!sim || w === 0) return
    const b = layoutBounds(nodeExtents(sim.nodes, sim.mode === 'layered'))
    if (!b) return
    const pad = 60
    const k = Math.min((w - pad) / Math.max(b.maxX - b.minX, 1), (h - pad) / Math.max(b.maxY - b.minY, 1), 2)
    const kk = Math.max(0.12, k)
    camRef.current = {
      k: kk,
      x: w / 2 - ((b.minX + b.maxX) / 2) * kk,
      y: h / 2 - ((b.minY + b.maxY) / 2) * kk,
    }
    paintOnce()
    saveCamera()
    requestPublish()
  }

  /** Zoom by `factor` keeping the viewport centre fixed. */
  function zoomAroundCentre(factor: number) {
    const { w, h } = sizeRef.current
    const cam = camRef.current
    applyZoom(cam, w / 2, h / 2, factor)
  }

  /** Zoom by `factor` keeping screen point (mx,my) fixed. */
  function applyZoom(cam: Camera, mx: number, my: number, factor: number) {
    const k = Math.min(4, Math.max(0.08, cam.k * factor))
    cam.x = mx - ((mx - cam.x) / cam.k) * k
    cam.y = my - ((my - cam.y) / cam.k) * k
    cam.k = k
  }

  /** Zoom around a pointer position (wheel / double-tap). */
  function zoomAt(clientX: number, clientY: number, factor: number) {
    const rect = canvasRef.current?.getBoundingClientRect()
    if (!rect) return
    applyZoom(camRef.current, clientX - rect.left, clientY - rect.top, factor)
    paintOnce()
    saveCameraSoon()
    requestPublish()
  }

  useImperativeHandle(ref, () => ({
    fit,
    reheat: () => {
      simRef.current?.reheat(1)
      start()
    },
    zoom: (factor: number) => {
      zoomAroundCentre(factor)
      paintOnce()
      saveCamera()
      requestPublish()
    },
    // Minimap: centre a world point without changing the zoom.
    centerOn: (x: number, y: number) => {
      const { w, h } = sizeRef.current
      if (w === 0 || h === 0) return
      const cam = camRef.current
      cam.x = w / 2 - x * cam.k
      cam.y = h / 2 - y * cam.k
      paintOnce()
      saveCamera()
      requestPublish()
    },
    // Minimap/toolbar: absolute zoom around the viewport centre.
    zoomTo: (k: number) => {
      const { w, h } = sizeRef.current
      if (w === 0 || h === 0) return
      const cam = camRef.current
      const target = Math.min(4, Math.max(0.08, k))
      cam.x = w / 2 - ((w / 2 - cam.x) / cam.k) * target
      cam.y = h / 2 - ((h / 2 - cam.y) / cam.k) * target
      cam.k = target
      paintOnce()
      saveCamera()
      requestPublish()
    },
  }))

  function toWorld(clientX: number, clientY: number) {
    const rect = canvasRef.current!.getBoundingClientRect()
    const cam = camRef.current
    return {
      x: (clientX - rect.left - cam.x) / cam.k,
      y: (clientY - rect.top - cam.y) / cam.k,
    }
  }

  /** Touch/coarse pointers are less precise: widen the pick radius. */
  function hitScale(): number {
    return coarseRef.current || lastPointerTypeRef.current === 'touch' ? TOUCH_HIT_SCALE : 1
  }

  function hitTest(clientX: number, clientY: number): GraphNode | null {
    const sim = simRef.current
    if (!sim) return null
    const p = toWorld(clientX, clientY)
    const pad = (HIT_PAD_PX / camRef.current.k) * hitScale()
    // While the layout is at rest the grid is valid; while it runs or a node is
    // dragged the positions move every frame, so scanning is cheaper than
    // rebuilding an index that would be stale by the next pointer sample.
    if (isSettled()) return ensureIndex(sim).nodeAt(p.x, p.y, pad)
    return nodeHitTest(sim.nodes, p.x, p.y, pad, sim.mode === 'layered')
  }

  /** Nearest selectable/hoverable edge, sampled on the same curve the renderer draws. */
  function edgeAt(clientX: number, clientY: number): GraphEdge | null {
    const sim = simRef.current
    if (!sim) return null
    const p = toWorld(clientX, clientY)
    const tol = (HIT_EDGE_TOL_PX / Math.max(camRef.current.k, 0.4)) * hitScale()
    if (isSettled()) return ensureIndex(sim).edgeAt(p.x, p.y, tol)
    return edgeHitTest(sim.edges, indexOf(sim), p.x, p.y, tol, sim.mode === 'layered')
  }

  function onWheel(e: React.WheelEvent) {
    // Ctrl/Cmd + wheel is the browser's zoom gesture. Swallowing it here is why
    // zooming with the cursor over the graph did nothing; let the page zoom and
    // the sync above repaints at the new dpr.
    if (e.ctrlKey || e.metaKey) return
    e.preventDefault()
    const rect = canvasRef.current?.getBoundingClientRect()
    if (!rect) return
    applyZoom(camRef.current, e.clientX - rect.left, e.clientY - rect.top, Math.exp(-e.deltaY * 0.0015))
    paintOnce()
    saveCameraSoon()
    requestPublish()
  }

  function onPointerDown(e: React.PointerEvent<HTMLCanvasElement>) {
    try {
      e.currentTarget.setPointerCapture(e.pointerId)
    } catch {
      /* capture is best-effort (some browsers throw for a released pointer) */
    }
    e.currentTarget.focus({ preventScroll: true })
    const touch = e.pointerType === 'touch'
    lastPointerTypeRef.current = e.pointerType || 'mouse'
    if (touch) {
      touchesRef.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
      if (touchesRef.current.size === 2) {
        // Second finger: this is a pinch, not a node drag or a pan.
        dragRef.current = NO_DRAG
        hoverMoveRef.current?.cancel()
        beginPinch()
        return
      }
      if (touchesRef.current.size > 2) return
    }
    const node = hitTest(e.clientX, e.clientY)
    // The edge is the intent. Select it on press, not on release: a click on a
    // thin curve is otherwise easy to lose to a one-pixel pointer drift.
    if (!node) {
      const edge = edgeAt(e.clientX, e.clientY)
      if (edge?.intent) onSelect(edge.id === stateRef.current.selected ? null : edge.id)
    }
    if (node) {
      dragRef.current = {
        mode: 'node',
        id: node.id,
        sx: e.clientX,
        sy: e.clientY,
        cx: node.x,
        cy: node.y,
        moved: false,
      }
      // The node moves off its settled position: the grid is stale from now on.
      invalidateIndex()
      simRef.current?.reheat(0.35)
    } else {
      dragRef.current = {
        mode: 'pan',
        sx: e.clientX,
        sy: e.clientY,
        cx: camRef.current.x,
        cy: camRef.current.y,
        moved: false,
      }
    }
    paintOnce()
    start()
  }

  function beginPinch() {
    const points = [...touchesRef.current.values()]
    const canvas = canvasRef.current
    if (points.length < 2 || !canvas) return
    const rect = canvas.getBoundingClientRect()
    const [a, b] = points
    const mx = (a.x + b.x) / 2 - rect.left
    const my = (a.y + b.y) / 2 - rect.top
    const cam = camRef.current
    pinchRef.current = {
      dist: Math.max(Math.hypot(a.x - b.x, a.y - b.y), 1),
      // The world point under the midpoint stays under the midpoint while the
      // fingers zoom: that is what makes pinch feel attached to the map.
      worldX: (mx - cam.x) / cam.k,
      worldY: (my - cam.y) / cam.k,
      baseK: cam.k,
    }
  }

  /** Apply the current pinch geometry; returns false when no pinch is active. */
  function updatePinch(): boolean {
    const pinch = pinchRef.current
    const canvas = canvasRef.current
    if (!pinch || !canvas) return false
    const points = [...touchesRef.current.values()]
    if (points.length < 2) return false
    const rect = canvas.getBoundingClientRect()
    const [a, b] = points
    const mx = (a.x + b.x) / 2 - rect.left
    const my = (a.y + b.y) / 2 - rect.top
    const dist = Math.max(Math.hypot(a.x - b.x, a.y - b.y), 1)
    const cam = camRef.current
    cam.k = Math.min(4, Math.max(0.08, (pinch.baseK * dist) / pinch.dist))
    cam.x = mx - pinch.worldX * cam.k
    cam.y = my - pinch.worldY * cam.k
    schedulePaint()
    requestPublish()
    return true
  }

  function onPointerMove(e: React.PointerEvent<HTMLCanvasElement>) {
    const touch = e.pointerType === 'touch'
    if (touch) {
      if (touchesRef.current.has(e.pointerId)) {
        touchesRef.current.set(e.pointerId, { x: e.clientX, y: e.clientY })
      }
      if (pinchRef.current && updatePinch()) return
    }
    lastPointerTypeRef.current = e.pointerType || 'mouse'
    const d = dragRef.current
    if (d.mode === 'node' && d.id) {
      const sim = simRef.current
      const node = sim ? indexOf(sim).get(d.id) : undefined
      if (node) {
        const cam = camRef.current
        // Only the target position changes here; the main loop paints the frame.
        node.x = d.cx + (e.clientX - d.sx) / cam.k
        node.y = d.cy + (e.clientY - d.sy) / cam.k
        node.vx = 0
        node.vy = 0
        d.moved = true
        invalidateIndex()
      }
      return
    }
    if (d.mode === 'pan') {
      camRef.current.x = d.cx + (e.clientX - d.sx)
      camRef.current.y = d.cy + (e.clientY - d.sy)
      if (Math.abs(e.clientX - d.sx) + Math.abs(e.clientY - d.sy) > 2) d.moved = true
      // One coalesced paint + publish per frame; the camera is saved on pointerup.
      schedulePaint()
      requestPublish()
      return
    }
    // Hover is not urgent: hand the position to the coalescer, which runs the
    // node/edge hit test once for this frame with the latest position. Copy the
    // coordinates now — React recycles the synthetic event after this handler.
    hoverMoveRef.current?.move(e.clientX, e.clientY)
  }

  /** Hit-test the last position of a frame and publish the hover only on change. */
  function applyHover(clientX: number, clientY: number) {
    const node = hitTest(clientX, clientY)
    const edge = node ? null : edgeAt(clientX, clientY)
    // A relation edge without an intent (goal support) is still worth inspecting:
    // the inspector names the relation even when there is no direction panel.
    const id = node ? node.id : edge ? edge.id : null
    if (id === hoverRef.current) return
    hoverRef.current = id
    // The inspector/minimap subscribe here; GraphView itself does not re-render.
    setHovered(id)
    paintOnce()
  }

  function onPointerUp(e: React.PointerEvent<HTMLCanvasElement>) {
    const touch = e.pointerType === 'touch'
    if (touch) {
      touchesRef.current.delete(e.pointerId)
      if (pinchRef.current) {
        pinchRef.current = null
        // A pinch changes the camera; persist the gesture, not every frame.
        saveCamera()
        requestPublish()
        // One finger may still be down: continue that finger as a pan from
        // where it is, so the map keeps following it instead of jumping when
        // its next pointermove arrives.
        const rest = touchesRef.current.values().next().value
        if (rest) {
          dragRef.current = {
            mode: 'pan',
            sx: rest.x,
            sy: rest.y,
            cx: camRef.current.x,
            cy: camRef.current.y,
            moved: true,
          }
          return
        }
      }
      if (touchesRef.current.size > 0) return
    }
    const d = dragRef.current
    dragRef.current = NO_DRAG
    // Double-tap on empty canvas zooms in. Pointer events give no dblclick on
    // several touch browsers, so the gesture is detected from the taps
    // themselves; onDoubleClick ignores touch pointers for the same reason.
    if (touch && d.mode === 'pan' && !d.moved) {
      const now = performance.now()
      const last = lastTapRef.current
      lastTapRef.current = { at: now, x: e.clientX, y: e.clientY }
      if (last && now - last.at < 320 && Math.hypot(e.clientX - last.x, e.clientY - last.y) < 24) {
        lastTapRef.current = null
        zoomAt(e.clientX, e.clientY, 1.6)
        return
      }
    } else if (touch) {
      lastTapRef.current = null
    }
    // A pan changes the camera; a click or node drag does not. Persist on the
    // released gesture instead of on every pointermove.
    if (d.mode === 'pan' && d.moved) {
      saveCamera()
      requestPublish()
    }
    if (d.mode === 'pan' && !d.moved) {
      // Edge selection already happened on press; a click on empty canvas only
      // clears the current selection.
      if (!edgeAt(e.clientX, e.clientY)) onSelect(null)
    }
    if (d.mode === 'node' && d.id && !d.moved) {
      const sim = simRef.current
      const node = sim ? indexOf(sim).get(d.id) : undefined
      if (node?.type === 'ghost') {
        const edge = sim?.edges.find((edge) => edge.target === node.id)
        onSelect(edge?.intent ? edge.id : null)
        return
      }
      onSelect(d.id === stateRef.current.selected ? null : d.id)
    }
    paintOnce()
  }

  function onPointerCancel(e: React.PointerEvent<HTMLCanvasElement>) {
    if (e.pointerType === 'touch') {
      touchesRef.current.delete(e.pointerId)
      if (pinchRef.current && touchesRef.current.size < 2) {
        pinchRef.current = null
        // A cancelled pinch still moved the camera: keep where the operator is.
        saveCamera()
        requestPublish()
      }
    }
    dragRef.current = NO_DRAG
    paintOnce()
  }

  function onPointerLeave(e: React.PointerEvent<HTMLCanvasElement>) {
    // Touch pointers leave after their up; only the ended finger goes away (the
    // other finger of a pinch is still down).
    if (e.pointerType === 'touch') {
      touchesRef.current.delete(e.pointerId)
      if (touchesRef.current.size < 2) pinchRef.current = null
      return
    }
    // Drop a hover test still queued for this frame, or it would re-hover the
    // node the pointer just left.
    hoverMoveRef.current?.cancel()
    if (hoverRef.current !== null) {
      hoverRef.current = null
      setHovered(null)
      paintOnce()
    }
  }

  function onDoubleClick(e: React.MouseEvent<HTMLCanvasElement>) {
    // Touch double-tap is detected in pointerup (many mobile browsers never fire
    // dblclick for it), so a synthetic dblclick after a touch must not zoom twice.
    if (lastPointerTypeRef.current === 'touch') return
    // Touch/coarse without a wheel: double-tap zooms around the tap.
    if (coarseRef.current) {
      zoomAt(e.clientX, e.clientY, 1.6)
      return
    }
    // Mouse keeps the existing "fit the whole map" action.
    fit()
  }

  /** Keyboard order: depth, then y, then x, then id — deterministic and stable. */
  function keyboardOrder(sim: ForceSim): GraphNode[] {
    const cached = keyboardOrderRef.current
    if (cached.sim === sim) return cached.nodes
    const nodes = sim.nodes
      .filter((n) => n.type !== 'ghost')
      .sort((a, b) => {
        if (a.depth !== b.depth) return a.depth - b.depth
        if (a.y !== b.y) return a.y - b.y
        if (a.x !== b.x) return a.x - b.x
        return a.id < b.id ? -1 : a.id > b.id ? 1 : 0
      })
    keyboardOrderRef.current = { sim, nodes }
    return nodes
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLCanvasElement>) {
    const sim = simRef.current
    if (!sim) return
    if (e.key === 'Escape') {
      keyboardFocusRef.current = null
      onSelect(null)
      paintOnce()
      e.preventDefault()
      return
    }
    if (e.key === 'Enter') {
      const id = keyboardFocusRef.current
      if (!id) return
      e.preventDefault()
      onSelect(id === stateRef.current.selected ? null : id)
      return
    }
    const forward = e.key === 'ArrowRight' || e.key === 'ArrowDown'
    const backward = e.key === 'ArrowLeft' || e.key === 'ArrowUp'
    if (!forward && !backward) return
    e.preventDefault()
    const order = keyboardOrder(sim)
    if (order.length === 0) return
    const current = keyboardFocusRef.current
    const at = current ? order.findIndex((n) => n.id === current) : -1
    let next = at < 0 ? 0 : at + (forward ? 1 : -1)
    if (next < 0) next = 0
    if (next >= order.length) next = order.length - 1
    keyboardFocusRef.current = order[next].id
    paintOnce()
  }

  function onBlur() {
    // The ring belongs to keyboard focus on the canvas, not to the selection.
    if (keyboardFocusRef.current === null) return
    keyboardFocusRef.current = null
    paintOnce()
  }

  const selectedNode = selected ? model.byId.get(selected) : null
  const selectedEdge = !selectedNode && selected ? model.edgesById?.get(selected) ?? null : null
  const selectionText = selectedNode
    ? `当前选中：${selectedNode.label}。`
    : selectedEdge
      ? `当前选中关系：${edgeKindLabel(selectedEdge.kind)}。`
      : '当前未选中节点。'
  const ariaLabel =
    `${model.nodes.length} 个节点，${model.edges.length} 条边。` +
    selectionText +
    '方向键在节点间移动，Enter 打开，Escape 清除。'

  return (
    <div ref={wrapRef} className="relative h-full w-full">
      <canvas
        ref={canvasRef}
        data-testid="graph-canvas"
        tabIndex={0}
        role="application"
        aria-label={ariaLabel}
        className="absolute inset-0 touch-none outline-none focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring/70 focus-visible:outline-offset-2"
        onWheel={onWheel}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerCancel}
        onPointerLeave={onPointerLeave}
        onDoubleClick={onDoubleClick}
        onKeyDown={onKeyDown}
        onBlur={onBlur}
      />
    </div>
  )
})
