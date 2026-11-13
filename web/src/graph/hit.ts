// Spatial hit-testing for the canvas.
//
// The pointer handler used to walk every node and then every hoverable edge,
// sampling each edge's curve sixteen times — fine for a few hundred nodes, the
// hot path of the whole interaction once the ledger grows. Everything the
// pointer may pick is geometric and changes only when the layout settles
// (positions are static while the simulation is at rest), so a uniform grid over
// node AABBs and over each edge's 16-segment polyline makes a hover a handful of
// bucket lookups plus a tiny exact check.
//
// The exact predicates live here, not in GraphCanvas, so the indexed path and the
// linear fallback used while the simulation runs or a node is dragged can never
// disagree. Both use the renderer's own `edgeCurve`/`pointOnCurve` and its
// `halfExtentX`, so "what you can point at" is the curve that is drawn.
//
// The index is built lazily on the first hover of a settled map, so its build is
// a real main-thread cost once per settle: it stores entities in flat numeric
// arrays, keys buckets with one integer, and skips re-inserting an edge into a
// bucket it already occupies. A 5k-node/5k-edge build stays in the low tens of
// milliseconds instead of the ~200ms an object-per-segment version cost.
//
// This module is pure: no DOM, no clock. Tests exercise it directly.
import type { GraphEdge, GraphNode } from './model.ts'
import { edgeCurve, halfExtentX, pointOnCurve } from './render.ts'
import { radiusOf } from './sim.ts'

/**
 * Edges the pointer may inspect. Every relation the ledger proves is here,
 * including references, pins, goal support and the legacy goal-evidence line:
 * hovering one is how the operator learns that it is not a required premise.
 * host edges stay background (they are grouping lines, not selectable relations).
 */
export const HOVERABLE_EDGE_KINDS: ReadonlySet<GraphEdge['kind']> = new Set([
  'intent',
  'premise',
  'reference',
  'pin',
  'supports',
  'goal',
  'brief',
  'spawn',
])

/** The curve is sampled in this many segments; hit-testing and painting agree. */
const EDGE_SEGMENTS = 16

// The grid cell is derived from the bucketed extent: roughly one item per cell
// (clamped, so a tiny graph does not create one bucket per world unit and a huge
// sparse graph does not collapse into a single bucket). The bucket map is sparse,
// so empty space costs nothing.
const MIN_CELL = 32
const MAX_CELL = 256

/** Guard against a pathological query range; beyond it the grid scans its own
 *  (small) bucket map instead of an enormous rectangle. Correct, just slower. */
const MAX_CELLS_PER_QUERY = 4096

// Integer bucket keys: (ix + KEY_BIAS) * KEY_SPAN + (iy + KEY_BIAS). The bias
// keeps negative indices positive and the span makes the pair collision-free as
// long as both indices stay within ±KEY_BIAS, which every real layout does.
const KEY_BIAS = 1 << 20
const KEY_SPAN = 1 << 21

export interface GraphIndexStats {
  /** Nodes whose AABB was tested exactly, summed over all node queries. */
  nodeCandidates: number
  /** Edges whose polyline was tested exactly, summed over all edge queries. */
  edgeCandidates: number
  /** nodeAt/edgeAt calls. Divide the candidate counters by this for averages. */
  queries: number
}

/** Point-to-segment distance, in graph coordinates. */
function distToSegment(
  px: number,
  py: number,
  x1: number,
  y1: number,
  x2: number,
  y2: number,
): number {
  const dx = x2 - x1
  const dy = y2 - y1
  const len = dx * dx + dy * dy
  if (len <= 0.0001) return Math.hypot(px - x1, py - y1)
  let t = ((px - x1) * dx + (py - y1) * dy) / len
  t = Math.max(0, Math.min(1, t))
  return Math.hypot(px - (x1 + t * dx), py - (y1 + t * dy))
}

interface HalfExtent {
  halfW: number
  halfH: number
}

/**
 * The drawn footprint the pointer tests against: facts are dots, every other
 * node is a box of half-height h. Matches the painter's shape (a wide pill stays
 * selectable at its ends without swallowing its neighbours).
 */
function drawnExtent(n: GraphNode, layered: boolean): HalfExtent {
  const dot = n.type === 'fact'
  return {
    halfW: dot ? radiusOf(n) : halfExtentX(n, layered),
    halfH: dot ? radiusOf(n) : n.h,
  }
}

/**
 * Exact node hit test over a candidate list; returns the nearest hit by the
 * normalised box distance. Ghost rings are not pointable (they stand for a step
 * that produced nothing yet; the step itself is the node), matching the canvas.
 */
export function nodeHitTest(
  nodes: readonly GraphNode[],
  x: number,
  y: number,
  pad: number,
  layered: boolean,
): GraphNode | null {
  let best: GraphNode | null = null
  let bestD = Infinity
  for (const n of nodes) {
    if (n.type === 'ghost') continue
    const { halfW, halfH } = drawnExtent(n, layered)
    const w = halfW + pad
    const h = halfH + pad
    const dx = x - n.x
    const dy = y - n.y
    if (Math.abs(dx) > w || Math.abs(dy) > h) continue
    const d = Math.hypot(dx / Math.max(w, 0.001), dy / Math.max(h, 0.001))
    if (d < bestD) {
      best = n
      bestD = d
    }
  }
  return best
}

/**
 * Exact edge hit test over a candidate list: samples the same cubic the renderer
 * draws and returns the edge whose curve comes closest within `tol`.
 */
export function edgeHitTest(
  edges: readonly GraphEdge[],
  byId: ReadonlyMap<string, GraphNode>,
  x: number,
  y: number,
  tol: number,
  layered: boolean,
): GraphEdge | null {
  let best: GraphEdge | null = null
  let bestD = Infinity
  for (const e of edges) {
    if (!HOVERABLE_EDGE_KINDS.has(e.kind)) continue
    const s = byId.get(e.source)
    const t = byId.get(e.target)
    if (!s || !t) continue
    const curve = edgeCurve(s, t, layered, e.kind)
    let prev = pointOnCurve(curve, 0)
    for (let i = 1; i <= EDGE_SEGMENTS; i++) {
      const at = pointOnCurve(curve, i / EDGE_SEGMENTS)
      const d = distToSegment(x, y, prev.x, prev.y, at.x, at.y)
      if (d < tol && d < bestD) {
        best = e
        bestD = d
      }
      prev = at
    }
  }
  return best
}

/**
 * A uniform grid over axis-aligned boxes.
 *
 * Entities are held in flat parallel arrays (`indexes[i]` owns the box at
 * `boxes[4i..4i+3]`) so building the index allocates nothing per segment. Cells
 * are addressed by an integer pair packed into one number, stored in a sparse
 * map: only occupied cells exist, and a query visits the cells its pointer box
 * overlaps.
 *
 * No inflation on insert: the query box is the exact pick box, so a box that can
 * be hit by that query necessarily overlaps it and shares a visited cell. That
 * keeps long edges to one bucket write per sampled segment.
 */
class UniformGrid {
  private readonly cell: number
  private readonly ox: number
  private readonly oy: number
  private readonly buckets = new Map<number, number[]>()

  /**
   * `density` is the number of entities the grid is sized for. For nodes that is
   * one per node; for edges it is one per edge (not per sampled segment), so a
   * long curve is stored in cells sized for the map's edge density — fewer
   * bucket writes to build and a query box that still holds only a few edges.
   */
  constructor(indexes: readonly number[], boxes: readonly number[], density = indexes.length) {
    const count = indexes.length
    let minX = Infinity
    let minY = Infinity
    let maxX = -Infinity
    let maxY = -Infinity
    for (let i = 0; i < count; i++) {
      const b = i * 4
      if (boxes[b] < minX) minX = boxes[b]
      if (boxes[b + 1] < minY) minY = boxes[b + 1]
      if (boxes[b + 2] > maxX) maxX = boxes[b + 2]
      if (boxes[b + 3] > maxY) maxY = boxes[b + 3]
    }
    if (count === 0 || !Number.isFinite(minX) || !Number.isFinite(maxX)) {
      this.cell = MIN_CELL
      this.ox = 0
      this.oy = 0
      return
    }
    const area = Math.max((maxX - minX) * (maxY - minY), 1)
    // ~one item per cell where the items are, within sane bounds.
    this.cell = Math.min(MAX_CELL, Math.max(MIN_CELL, Math.sqrt(area / Math.max(1, density))))
    this.ox = minX
    this.oy = minY
    for (let i = 0; i < count; i++) {
      const b = i * 4
      const index = indexes[i]
      this.insert(index, boxes[b], boxes[b + 1], boxes[b + 2], boxes[b + 3])
    }
  }

  private insert(index: number, minX: number, minY: number, maxX: number, maxY: number) {
    const cell = this.cell
    const ix0 = Math.floor((minX - this.ox) / cell)
    const ix1 = Math.floor((maxX - this.ox) / cell)
    const iy0 = Math.floor((minY - this.oy) / cell)
    const iy1 = Math.floor((maxY - this.oy) / cell)
    for (let ix = ix0; ix <= ix1; ix++) {
      for (let iy = iy0; iy <= iy1; iy++) {
        const key = (ix + KEY_BIAS) * KEY_SPAN + (iy + KEY_BIAS)
        const list = this.buckets.get(key)
        if (list) {
          // Consecutive segments of one edge often share a cell; an index that
          // is already at the tail of the list is already represented. (Any
          // other duplicate is dropped by query()'s dedupe.)
          if (list[list.length - 1] !== index) list.push(index)
        } else {
          this.buckets.set(key, [index])
        }
      }
    }
  }

  /**
   * Every entity whose box overlaps the query box, deduplicated and in ascending
   * entity order (so a tie is resolved exactly like a linear scan).
   */
  query(minX: number, minY: number, maxX: number, maxY: number): number[] {
    if (!Number.isFinite(minX) || !Number.isFinite(minY) || !Number.isFinite(maxX) || !Number.isFinite(maxY)) {
      return []
    }
    const cell = this.cell
    const ix0 = Math.floor((Math.min(minX, maxX) - this.ox) / cell)
    const ix1 = Math.floor((Math.max(minX, maxX) - this.ox) / cell)
    const iy0 = Math.floor((Math.min(minY, maxY) - this.oy) / cell)
    const iy1 = Math.floor((Math.max(minY, maxY) - this.oy) / cell)
    const seen = new Set<number>()
    const cells = (ix1 - ix0 + 1) * (iy1 - iy0 + 1)
    if (cells > MAX_CELLS_PER_QUERY) {
      // A query wider than the guard: read the whole (sparse) map. Still exact.
      for (const list of this.buckets.values()) for (const i of list) seen.add(i)
    } else {
      for (let ix = ix0; ix <= ix1; ix++) {
        const row = (ix + KEY_BIAS) * KEY_SPAN
        for (let iy = iy0; iy <= iy1; iy++) {
          const list = this.buckets.get(row + (iy + KEY_BIAS))
          if (!list) continue
          for (const i of list) seen.add(i)
        }
      }
    }
    return [...seen].sort((a, b) => a - b)
  }
}

interface FlatBoxes {
  indexes: number[]
  boxes: number[]
}

/**
 * Hit-test index over one settled layout.
 *
 * Build it lazily once the simulation is at rest; while the simulation runs or a
 * node is dragged, the canvas uses the linear predicates above, because node
 * positions move every frame and rebuilding a grid per frame would cost more
 * than the scan it replaces.
 */
export class GraphIndex {
  private readonly layered: boolean
  private readonly byId: Map<string, GraphNode>
  private readonly nodeList: GraphNode[] = []
  private readonly edgeList: GraphEdge[] = []
  private readonly nodeGrid: UniformGrid
  private readonly edgeGrid: UniformGrid
  readonly stats: GraphIndexStats = { nodeCandidates: 0, edgeCandidates: 0, queries: 0 }

  constructor(nodes: readonly GraphNode[], edges: readonly GraphEdge[], opts: { layered: boolean }) {
    this.layered = opts.layered
    this.byId = new Map(nodes.map((n) => [n.id, n]))

    const nodeData: FlatBoxes = { indexes: [], boxes: [] }
    for (const n of nodes) {
      if (n.type === 'ghost') continue
      const { halfW, halfH } = drawnExtent(n, this.layered)
      nodeData.indexes.push(this.nodeList.length)
      this.nodeList.push(n)
      nodeData.boxes.push(n.x - halfW, n.y - halfH, n.x + halfW, n.y + halfH)
    }
    this.nodeGrid = new UniformGrid(nodeData.indexes, nodeData.boxes)

    const edgeData: FlatBoxes = { indexes: [], boxes: [] }
    for (const e of edges) {
      if (!HOVERABLE_EDGE_KINDS.has(e.kind)) continue
      const s = this.byId.get(e.source)
      const t = this.byId.get(e.target)
      if (!s || !t) continue
      const index = this.edgeList.length
      this.edgeList.push(e)
      const curve = edgeCurve(s, t, this.layered, e.kind)
      let prev = pointOnCurve(curve, 0)
      for (let i = 1; i <= EDGE_SEGMENTS; i++) {
        const at = pointOnCurve(curve, i / EDGE_SEGMENTS)
        edgeData.indexes.push(index)
        edgeData.boxes.push(
          Math.min(prev.x, at.x),
          Math.min(prev.y, at.y),
          Math.max(prev.x, at.x),
          Math.max(prev.y, at.y),
        )
        prev = at
      }
    }
    this.edgeGrid = new UniformGrid(edgeData.indexes, edgeData.boxes, this.edgeList.length)
  }

  nodeAt(x: number, y: number, pad: number): GraphNode | null {
    this.stats.queries++
    if (this.nodeList.length === 0) return null
    const r = Math.max(pad, 0)
    const hits = this.nodeGrid.query(x - r, y - r, x + r, y + r)
    this.stats.nodeCandidates += hits.length
    if (hits.length === 0) return null
    const candidates = hits.map((i) => this.nodeList[i])
    return nodeHitTest(candidates, x, y, pad, this.layered)
  }

  edgeAt(x: number, y: number, tol: number): GraphEdge | null {
    this.stats.queries++
    if (this.edgeList.length === 0) return null
    const r = Math.max(tol, 0)
    const hits = this.edgeGrid.query(x - r, y - r, x + r, y + r)
    this.stats.edgeCandidates += hits.length
    if (hits.length === 0) return null
    const candidates = hits.map((i) => this.edgeList[i])
    return edgeHitTest(candidates, this.byId, x, y, tol, this.layered)
  }
}
