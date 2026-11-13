import type { GraphEdge, GraphModel, GraphNode } from './model'
import {
  COLLIDE_PAD,
  COLLIDE_STRENGTH,
  REPULSION_K,
  buildQuadtree,
  collide,
  collideRadius,
  radiusOf,
  repulse,
} from './quadtree.ts'

// Node geometry lives with the quadtree (the collide pass needs the same radii);
// it is re-exported here because render.ts and the canvas read it from the sim.
export { radiusOf }

/**
 * Two layouts over one graph.
 *
 * force    — the original map: nodes settle on springs, the brief near the
 *            centre. Good for "what is connected to what".
 * layered  — 链路树 / 递进分层: one column per hop, left to right. An intent
 *            step is a labelled pill, the facts it produced are packed in the
 *            column immediately to its right, and a step that consumes those
 *            facts sits at their centre. The tree is laid out deterministically
 *            (tidy-tree packing), so no edge has to cross a neighbouring fan to
 *            reach its node: reading left→right is reading the engagement's
 *            progression, and every intent step is a visible stop instead of a
 *            spoke.
 */
export type LayoutMode = 'force' | 'layered'

export interface SimOptions {
  mode?: LayoutMode
}

/** What a rebuild needs to know about the simulation it replaces. Only the mode
 *  and the node ids matter: the previous x/y are read by the canvas itself. */
export interface SimSnapshot {
  mode: LayoutMode
  nodes: ReadonlyArray<{ id: string }>
}

// An opening picture must already look like a graph before its first paint (a
// background tab may never get an animation frame), so it is warmed generously.
const INITIAL_WARM_BASE = 520
const INITIAL_WARM_PER_NODE = 6
const INITIAL_WARM_MAX = 900

// A poll that kept the settled force-map picture only has to pull its new nodes
// in; the frame loop finishes the settle. Bounded so one huge poll cannot freeze
// the main thread the way a full warm does (the force tick is O(n²)).
const REBUILD_WARM_BASE = 24
const REBUILD_WARM_PER_ADDED = 8
/** Bound on the rebuild warm: one huge poll cannot stall the main thread. */
export const REBUILD_WARM_MAX = 180

/** Ticks needed to warm a picture with no usable previous positions. */
export function initialWarmTicks(nodeCount: number): number {
  return Math.min(INITIAL_WARM_MAX, INITIAL_WARM_BASE + nodeCount * INITIAL_WARM_PER_NODE)
}

/**
 * How many synchronous settle ticks a (re)build needs before the first paint.
 *
 * Rebuilding after every poll used to re-run the opening warm — hundreds of
 * force ticks, each O(n²) — even though the operator was already looking at a
 * settled picture. The old positions survive a poll, so only the nodes the poll
 * introduced need the simulation to move anything; with none, zero ticks keep
 * the picture exactly still. A layout switch, or a model that shares no node
 * with the previous one (another project's ledger), has no usable previous
 * layout and is warmed like the opening picture.
 */
export function warmTicks(
  prev: SimSnapshot | null,
  model: Pick<GraphModel, 'nodes'>,
  layout: LayoutMode,
): number {
  if (!prev || prev.mode !== layout) return initialWarmTicks(model.nodes.length)
  const known = new Set(prev.nodes.map((n) => n.id))
  let kept = 0
  let added = 0
  for (const n of model.nodes) {
    if (known.has(n.id)) kept++
    else added++
  }
  if (added === 0) return 0
  // Nothing recognisable survived: this is a new picture, not this layout with
  // a few extra nodes, so it needs the opening warm to look like a graph.
  if (kept === 0) return initialWarmTicks(model.nodes.length)
  return Math.min(REBUILD_WARM_MAX, REBUILD_WARM_BASE + added * REBUILD_WARM_PER_ADDED)
}

/** One depth column's horizontal span, for the canvas depth bands. */
export interface DepthBand {
  depth: number
  x1: number
  x2: number
}

export interface MoveCoalescer {
  /** Record the pointer position; the hit test runs at most once per frame. */
  move(x: number, y: number): void
  /** Drop a pending test (pointer left the canvas, or the canvas unmounted). */
  cancel(): void
}

/**
 * Collapse pointer moves to at most one hit test per animation frame.
 *
 * A pointermove fires several times per frame, and each hover test walked every
 * node and sampled every intent/brief edge sixteen times; on a large map that
 * was the hottest path while the operator pointed at something. Only the last
 * position of a frame can be drawn anyway, so the intermediate samples are
 * dropped. This lives beside the simulation because both ride the canvas's one
 * animation-frame clock (and render.ts stays free of DOM/clock calls); the
 * scheduler is injectable so the rule is testable without a DOM.
 */
export function createMoveCoalescer(
  onFrame: (x: number, y: number) => void,
  schedule: (cb: () => void) => number = (cb) => requestAnimationFrame(cb),
  cancel: (handle: number) => void = (handle) => cancelAnimationFrame(handle),
): MoveCoalescer {
  let frame = 0
  let pending: { x: number; y: number } | null = null
  const run = () => {
    frame = 0
    const at = pending
    pending = null
    // A cancel() after the frame was queued leaves nothing to test.
    if (at) onFrame(at.x, at.y)
  }
  return {
    move(x, y) {
      // Latest wins: the frame only needs to know where the pointer ended up.
      pending = { x, y }
      if (frame === 0) frame = schedule(run)
    },
    cancel() {
      if (frame !== 0) cancel(frame)
      frame = 0
      pending = null
    },
  }
}

/** Synchronous warm budget before the first paint (docs/33 §4.4). */
export const WARM_BUDGET_MS = 24

/** Alpha at or below which a simulation counts as settled. */
export const SETTLE_ALPHA = 0.015

/** Node count at which the force tick switches to Barnes–Hut repulsion. */
export const BH_MIN_NODES = 600

/** Opening angle of the Barnes–Hut approximation: smaller = more accurate. */
const BH_THETA = 0.7

export interface WarmWithinOptions {
  /** Wall-clock budget for the synchronous warm. Defaults to WARM_BUDGET_MS. */
  budgetMs?: number
  /** Hard cap on ticks, on top of the budget. Unlimited by default. */
  maxTicks?: number
  /** Stop once alpha is at or below this. Defaults to SETTLE_ALPHA. */
  minAlpha?: number
  /** Injectable clock so the budget rule is testable without real waiting. */
  now?: () => number
}

export interface WarmWithinResult {
  ticks: number
  spentMs: number
}

/**
 * Run synchronous settle ticks until the frame budget, the tick cap or the
 * settled alpha is reached, whichever comes first.
 *
 * The old warm ran a fixed number of ticks: on a large map that was hundreds of
 * milliseconds of blocked main thread before the first paint, and a poll that
 * brought in sixty new nodes cost another frozen frame. The budget makes the
 * warm a *bounded stall*: the frame loop finishes whatever the budget could not,
 * and because the picture is already drawn (and the sim resumes from the same
 * positions) stopping mid-settle is invisible.
 *
 * `now` is injectable; the tests use a fake clock and never wait for real time.
 */
export function warmWithin(sim: ForceSim, opts: WarmWithinOptions = {}): WarmWithinResult {
  const budgetMs = opts.budgetMs ?? WARM_BUDGET_MS
  const maxTicks = opts.maxTicks === undefined ? Infinity : Math.max(0, Math.floor(opts.maxTicks))
  const minAlpha = opts.minAlpha ?? SETTLE_ALPHA
  if (budgetMs <= 0 || maxTicks <= 0) return { ticks: 0, spentMs: 0 }
  const now =
    opts.now ??
    (() => (typeof performance !== 'undefined' ? performance.now() : Date.now()))
  const start = now()
  let ticks = 0
  let spentMs = 0
  while (ticks < maxTicks && sim.alpha > minAlpha) {
    sim.tick()
    ticks++
    spentMs = now() - start
    if (spentMs >= budgetMs) break
  }
  return { ticks, spentMs }
}

// Chain-tree layout constants (layered mode). The renderer only needs the
// resulting bands; everything else is private to the placement below.
const COLUMN_GAP = 130 // clear space between two columns' drawn extents
const ROW_H = 22 // vertical slot of one leaf (a fact or ghost dot)
const SIBLING_GAP = 10 // between two stacked subtrees
const BAND_PAD = 18

export class ForceSim {
  nodes: GraphNode[]
  edges: GraphEdge[]
  alpha = 1
  mode: LayoutMode
  readonly depthBands: DepthBand[] = []
  private index = new Map<string, GraphNode>()
  private anchors = new Map<string, { x: number; y: number }>()

  constructor(model: GraphModel, opts: SimOptions = {}) {
    this.nodes = model.nodes
    this.edges = model.edges
    this.mode = opts.mode ?? 'force'
    for (const n of model.nodes) this.index.set(n.id, n)
    if (this.mode === 'layered') this.placeLayered()
  }

  /**
   * Deterministic chain-tree placement.
   *
   * The tree edges are the ones the ledger already implies: a step produces its
   * facts, a fact is context for the next step, an achieved goal is anchored to
   * the fact that carries its evidence. Cross-links (extra context facts, host
   * edges, spawn lineage) stay on the canvas but do not own a slot, so they can
   * never bend the columns.
   */
  private placeLayered() {
    const project = this.nodes.find((n) => n.type === 'project') ?? this.nodes[0]
    if (!project) return

    // ---- layout tree: every node hangs off exactly one parent ----
    const children = new Map<string, GraphNode[]>()
    const parentOf = new Map<string, string>()
    const addChild = (child: GraphNode, parent: GraphNode) => {
      if (child === parent || parentOf.has(child.id)) return
      parentOf.set(child.id, parent.id)
      const list = children.get(parent.id) ?? []
      list.push(child)
      children.set(parent.id, list)
    }
    const nodeOf = (id: string) => this.index.get(id)

    // Output first: step → the fact (or ghost) it produced.
    for (const e of this.edges) {
      if (e.kind !== 'intent') continue
      const s = nodeOf(e.source)
      const t = nodeOf(e.target)
      if (!s || !t) continue
      if (s.type === 'step' && (t.type === 'fact' || t.type === 'ghost')) addChild(t, s)
    }
    // Context: the first context fact (or the brief) is the tree parent of a
    // step. Hard premises are that context; references/pins are cross-links and
    // must not pull a step into a lane as if they were required.
    for (const e of this.edges) {
      if (e.kind !== 'intent' && e.kind !== 'premise') continue
      const s = nodeOf(e.source)
      const t = nodeOf(e.target)
      if (!s || !t) continue
      if (t.type === 'step' && (s.type === 'fact' || s.type === 'project')) addChild(t, s)
    }
    // Spawn lineage only fills in a step that has no context edge at all.
    for (const e of this.edges) {
      if (e.kind !== 'spawn') continue
      const s = nodeOf(e.source)
      const t = nodeOf(e.target)
      if (s && t && t.type === 'step') addChild(t, s)
    }
    // An achieved goal is a leaf of the fact that carries its evidence.
    for (const e of this.edges) {
      if (e.kind !== 'goal') continue
      const s = nodeOf(e.source)
      const t = nodeOf(e.target)
      if (s && t && t.type === 'goal') addChild(t, s)
    }
    // A goal with no evidence edge, or any fact the operator wrote directly,
    // hangs off the brief so it still appears on the map (connected, not loose).
    for (const e of this.edges) {
      if (e.kind !== 'brief') continue
      const s = nodeOf(e.source)
      const t = nodeOf(e.target)
      if (s && t) addChild(t, s)
    }
    for (const n of this.nodes) {
      if (n !== project && !parentOf.has(n.id)) addChild(n, project)
    }

    const rank = (n: GraphNode) => (n.type === 'step' ? 0 : n.type === 'goal' ? 2 : 1)
    for (const list of children.values()) {
      list.sort((a, b) => {
        const at = a.orderTime ?? ''
        const bt = b.orderTime ?? ''
        if (at !== bt) return at < bt ? -1 : 1
        const ra = rank(a)
        const rb = rank(b)
        if (ra !== rb) return ra - rb
        return a.label < b.label ? -1 : a.label > b.label ? 1 : 0
      })
    }

    // ---- columns: one hop per column, depth-first from the brief ----
    const column = new Map<string, number>()
    const walk = (n: GraphNode, col: number) => {
      if (column.has(n.id)) return
      column.set(n.id, col)
      for (const child of children.get(n.id) ?? []) walk(child, col + 1)
    }
    walk(project, 0)
    for (const n of this.nodes) if (!column.has(n.id)) column.set(n.id, 0)

    // ---- rows: tidy-tree packing, parent centred on its children ----
    const slotOf = (n: GraphNode) => Math.max(ROW_H, 2 * n.h + 8)
    const place = (n: GraphNode, top: number): number => {
      const kids = children.get(n.id) ?? []
      const own = slotOf(n)
      if (kids.length === 0) {
        n.y = top + own / 2
        return own
      }
      let cursor = top
      let first = 0
      let last = 0
      for (const [i, kid] of kids.entries()) {
        const h = place(kid, cursor)
        if (i === 0) first = kid.y
        last = kid.y
        cursor += h
        if (i < kids.length - 1) cursor += SIBLING_GAP
      }
      n.y = (first + last) / 2
      return Math.max(cursor - top, own)
    }
    place(project, 0)

    // Centre the whole tree on y = 0 so "fit to canvas" keeps it in the middle.
    let minY = Infinity
    let maxY = -Infinity
    for (const n of this.nodes) {
      minY = Math.min(minY, n.y)
      maxY = Math.max(maxY, n.y)
    }
    const shift = (minY + maxY) / 2
    for (const n of this.nodes) n.y -= shift

    // ---- x: columns sized by what is actually drawn in them ----
    const halfWidth = (n: GraphNode) => {
      if (n.type === 'fact' || n.type === 'ghost') return radiusOf(n)
      return Math.max(n.w, 8)
    }
    const widest = new Map<number, number>()
    for (const n of this.nodes) {
      const col = column.get(n.id) ?? 0
      widest.set(col, Math.max(widest.get(col) ?? 0, halfWidth(n)))
    }
    const lastColumn = Math.max(0, ...widest.keys())
    const columnX: number[] = [0]
    for (let col = 1; col <= lastColumn; col++) {
      columnX[col] =
        columnX[col - 1] + (widest.get(col - 1) ?? 0) + COLUMN_GAP + (widest.get(col) ?? 0)
    }
    for (const n of this.nodes) {
      n.x = columnX[column.get(n.id) ?? 0]
      n.vx = 0
      n.vy = 0
      this.anchors.set(n.id, { x: n.x, y: n.y })
    }
    for (let col = 0; col <= lastColumn; col++) {
      const half = widest.get(col) ?? 0
      this.depthBands.push({
        depth: col,
        x1: columnX[col] - half - BAND_PAD,
        x2: columnX[col] + half + BAND_PAD,
      })
    }
  }

  reheat(alpha = 0.85) {
    this.alpha = Math.max(this.alpha, alpha)
  }

  get running() {
    return this.alpha > SETTLE_ALPHA
  }

  /**
   * Which repulsion the next force tick uses. Read-only and derived from the
   * model, so a test can pin the threshold without watching timings.
   *
   * `layered` never repels at all (the tree layout is the picture), so it reports
   * `exact`: neither approximation is in play there.
   */
  get tickStrategy(): 'exact' | 'barnes-hut' {
    return this.mode !== 'layered' && this.nodes.length >= BH_MIN_NODES
      ? 'barnes-hut'
      : 'exact'
  }

  tick() {
    if (this.mode === 'layered') {
      // The tree layout is the picture; there is nothing to settle. A drag can
      // still lift a node off its slot, so a gentle spring returns it once the
      // pointer lets go (the same elastic feel the force map has).
      const a = this.alpha
      for (const n of this.nodes) {
        const anchor = this.anchors.get(n.id)
        if (anchor) {
          n.x += (anchor.x - n.x) * 0.12 * a
          n.y += (anchor.y - n.y) * 0.12 * a
        }
        n.vx = 0
        n.vy = 0
      }
      this.alpha *= 0.985
      return
    }

    const nodes = this.nodes
    const n = nodes.length
    if (n === 0) return
    const a = this.alpha

    // Repulsion + short-range separation.
    if (this.tickStrategy === 'barnes-hut') {
      // n ≥ 600: the pairwise sum is the frame. Aggregate distant cells as one
      // weighted centre of mass, but keep the same two terms — 1/d² repulsion
      // and the overlap push that keeps wide pills from sitting on each other.
      // The collision pass is exact (it only looks within a footprint), so this
      // is an approximation of the forces' *aggregation*, not of the clearance.
      const tree = buildQuadtree(nodes, { theta: BH_THETA })
      repulse(nodes, tree, a)
      collide(nodes, tree, a)
    } else {
      for (let i = 0; i < n; i++) {
        const A = nodes[i]
        for (let j = i + 1; j < n; j++) {
          const B = nodes[j]
          let dx = B.x - A.x
          let dy = B.y - A.y
          let d2 = dx * dx + dy * dy
          if (d2 < 0.01) {
            dx = (Math.random() - 0.5) * 2
            dy = (Math.random() - 0.5) * 2
            d2 = dx * dx + dy * dy
          }
          const d = Math.sqrt(d2)
          // Long titles make pills wide; separating only by the circle radius
          // let neighbouring pills/facts overlap horizontally.
          const minD = collideRadius(A) + collideRadius(B) + COLLIDE_PAD
          let f = (REPULSION_K * a) / d2
          if (d < minD) f += (minD - d) * COLLIDE_STRENGTH * a
          const fx = (dx / d) * f
          const fy = (dy / d) * f
          A.vx -= fx
          A.vy -= fy
          B.vx += fx
          B.vy += fy
        }
      }
    }

    // Springs.
    for (const e of this.edges) {
      const s = this.index.get(e.source)
      const t = this.index.get(e.target)
      if (!s || !t) continue
      const dx = t.x - s.x
      const dy = t.y - s.y
      const d = Math.max(Math.sqrt(dx * dx + dy * dy), 0.01)
      const rest = restOf(e.kind)
      const f = (d - rest) * 0.011 * a
      const fx = (dx / d) * f
      const fy = (dy / d) * f
      s.vx += fx
      s.vy += fy
      t.vx -= fx
      t.vy -= fy
    }

    // Gravity so disconnected islands do not drift to infinity.
    const gravity = 0.0012
    for (const node of nodes) {
      node.vx -= node.x * gravity * a
      node.vy -= node.y * gravity * a
      node.vx *= 0.82
      node.vy *= 0.82
      const v = Math.hypot(node.vx, node.vy)
      const cap = 30
      if (v > cap) {
        node.vx = (node.vx / v) * cap
        node.vy = (node.vy / v) * cap
      }
      node.x += node.vx
      node.y += node.vy
    }

    this.alpha *= 0.985
  }
}

// radiusOf/collideRadius live in quadtree.ts now: the collide pass needs exactly
// the same footprints, and two copies would drift. `radiusOf` is re-exported
// above so render.ts and the canvas keep their import site.
function restOf(kind: GraphEdge['kind']): number {
  switch (kind) {
    case 'brief':
      return 300
    case 'intent':
    case 'premise':
      return 220
    case 'reference':
      return 260
    case 'supports':
      return 230
    case 'host':
      return 175
    case 'goal':
      return 230
    default:
      return 190
  }
}
