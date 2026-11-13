// Barnes–Hut spatial index for the force map.
//
// Below six hundred nodes the simulation compares every pair: exact, and cheap
// enough that changing it would only make the picture subtly different for no
// gain. Past that the pairwise loop *is* the frame, so the same repulsion is
// summed against a quadtree instead: a distant cluster of bodies pulls as one
// weighted centre of mass, and each body only opens the quads it is genuinely
// close to. The collision pass stays exact — it only has to look at bodies that
// are within a node footprint of each other, which is exactly what the tree is
// good at finding.
//
// Everything here is pure geometry over `GraphNode` positions: no DOM, no clock,
// no Math.random (coincident bodies are nudged apart by a direction derived from
// their indices, so one input always produces one output — the exact pairwise
// tick keeps its historical random nudge instead).

import type { GraphNode } from './model.ts'

/** Repulsion constant, shared with the exact pairwise tick in sim.ts. */
export const REPULSION_K = 14000
/** Clearance added on top of two node footprints (the exact tick's +26). */
export const COLLIDE_PAD = 26
/** Strength of the short-range separation term (the exact tick's 1.1). */
export const COLLIDE_STRENGTH = 1.1

/** The circle the renderer draws for a node. */
export function radiusOf(n: GraphNode): number {
  if (n.type === 'intent') return Math.hypot(n.w, n.h) * 0.78
  if (n.type === 'goal') return Math.hypot(n.w, n.h) * 0.7
  if (n.type === 'project') return Math.hypot(n.w, n.h) * 0.72
  if (n.type === 'step') return 8
  if (n.type === 'ghost') return 7
  return 7 + Math.min(n.degree, 6) * 1.6
}

/** Bigger than the circle for a pill: a 190px title must not sit on a fact. */
export function collideRadius(n: GraphNode): number {
  if (n.type === 'intent' || n.type === 'project' || n.type === 'goal') {
    return Math.max(radiusOf(n), n.w * 0.92)
  }
  return radiusOf(n)
}

/**
 * A deterministic direction for two bodies sitting on top of each other.
 *
 * The exact tick uses Math.random() here; the tree path cannot, because the
 * whole point of the budgeted warm is that the same ledger produces the same
 * picture twice in a row (and a test can assert that). The index pair is stable
 * for a given node array, so the nudge is too.
 */
function coincidentAngle(i: number, j: number): number {
  let h = (Math.imul(i + 1, 0x9e3779b1) ^ Math.imul(j + 1, 0x85ebca6b)) >>> 0
  h = (h ^ (h >>> 15)) >>> 0
  return (h / 4294967295) * Math.PI * 2
}

export interface QuadtreeOptions {
  /** Barnes–Hut opening angle: smaller is more accurate, larger is faster. */
  theta?: number
  /** Bodies a leaf may hold before it splits. */
  capacity?: number
  /** Hard ceiling on depth, for a pile of coincident bodies. */
  maxDepth?: number
}

export interface QuadNode {
  /** Lower-left corner of the cell, in world units. */
  x0: number
  y0: number
  /** Cell width (and height: cells are square). */
  size: number
  /** Weighted centre of the bodies inside. */
  cx: number
  cy: number
  mass: number
  /** Largest collide radius of any body in this cell. */
  maxRadius: number
  leaf: boolean
  children: QuadNode[] | null
  /** Body indices (into the source array) for a leaf. */
  body: number[] | null
}

const DEFAULT_THETA = 0.7
const DEFAULT_CAPACITY = 8
const DEFAULT_MAX_DEPTH = 20

/**
 * The sum of one body's repulsion against all the others, approximated by
 * opening cells that are close and aggregating the ones that are far.
 */
export class Quadtree {
  readonly nodes: ReadonlyArray<GraphNode>
  readonly theta: number
  readonly capacity: number
  readonly maxDepth: number
  readonly root: QuadNode
  /**
   * Number of cell/body interactions across repulse/collide/queryRadius. The
   * tests assert how it grows with n instead of a wall-clock number.
   */
  visits = 0

  constructor(nodes: ReadonlyArray<GraphNode>, opts: QuadtreeOptions = {}) {
    this.nodes = nodes
    this.theta = opts.theta ?? DEFAULT_THETA
    this.capacity = Math.max(1, Math.floor(opts.capacity ?? DEFAULT_CAPACITY))
    this.maxDepth = Math.max(1, Math.floor(opts.maxDepth ?? DEFAULT_MAX_DEPTH))
    this.root = this.build()
  }

  resetVisits(): void {
    this.visits = 0
  }

  /**
   * Indices of the bodies within `r` of (x, y), ascending. This is the same
   * answer a linear scan gives; the tree only makes the scan cheaper.
   */
  queryRadius(x: number, y: number, r: number): number[] {
    if (this.root.mass === 0 || !(r >= 0)) return []
    const out: number[] = []
    const r2 = r * r
    const stack: QuadNode[] = [this.root]
    while (stack.length > 0) {
      const q = stack.pop()!
      this.visits++
      if (q.mass === 0) continue
      if (minDist2(q, x, y) > r2) continue
      if (q.leaf) {
        for (const idx of q.body!) {
          this.visits++
          const n = this.nodes[idx]
          const dx = n.x - x
          const dy = n.y - y
          if (dx * dx + dy * dy <= r2) out.push(idx)
        }
      } else {
        for (const child of q.children!) stack.push(child)
      }
    }
    out.sort((a, b) => a - b)
    return out
  }

  private build(): QuadNode {
    const nodes = this.nodes
    if (nodes.length === 0) {
      return {
        x0: 0,
        y0: 0,
        size: 1,
        cx: 0,
        cy: 0,
        mass: 0,
        maxRadius: 0,
        leaf: true,
        children: null,
        body: [],
      }
    }
    let minX = Infinity
    let minY = Infinity
    let maxX = -Infinity
    let maxY = -Infinity
    for (const n of nodes) {
      if (n.x < minX) minX = n.x
      if (n.x > maxX) maxX = n.x
      if (n.y < minY) minY = n.y
      if (n.y > maxY) maxY = n.y
    }
    // Square cells, and two units of margin so every body sits strictly inside
    // the half-open bounds the split rule uses (a body on the shared edge must
    // belong to exactly one child).
    const span = Math.max(maxX - minX, maxY - minY)
    const size = (span > 0 ? span : 1) + 2
    const root: QuadNode = {
      x0: (minX + maxX) / 2 - size / 2,
      y0: (minY + maxY) / 2 - size / 2,
      size,
      cx: 0,
      cy: 0,
      mass: 0,
      maxRadius: 0,
      leaf: true,
      children: null,
      body: [],
    }
    for (let i = 0; i < nodes.length; i++) this.insert(root, i, 0)
    return root
  }

  private insert(q: QuadNode, idx: number, depth: number): void {
    const body = this.nodes[idx]
    const mass = q.mass + 1
    // Running weighted mean, so a cell's centre of mass is the true mean of its
    // bodies no matter how they were inserted.
    q.cx = (q.cx * q.mass + body.x) / mass
    q.cy = (q.cy * q.mass + body.y) / mass
    q.mass = mass
    const r = collideRadius(body)
    if (r > q.maxRadius) q.maxRadius = r

    if (q.leaf) {
      q.body!.push(idx)
      if (q.body!.length <= this.capacity || depth >= this.maxDepth) return
      const held = q.body!
      q.body = null
      q.leaf = false
      q.children = makeChildren(q)
      for (const heldIdx of held) this.insertIntoChild(q, heldIdx, depth)
      return
    }
    this.insertIntoChild(q, idx, depth)
  }

  private insertIntoChild(q: QuadNode, idx: number, depth: number): void {
    const body = this.nodes[idx]
    const half = q.size / 2
    // Same half-open rule as `contains`: x < midX is the west child, and a body
    // exactly on the mid-line lands in the east/north child.
    const east = body.x >= q.x0 + half ? 1 : 0
    const north = body.y >= q.y0 + half ? 1 : 0
    this.insert(q.children![north * 2 + east], idx, depth + 1)
  }
}

function makeChildren(q: QuadNode): QuadNode[] {
  const half = q.size / 2
  const out: QuadNode[] = []
  for (let i = 0; i < 4; i++) {
    out.push({
      x0: q.x0 + (i % 2) * half,
      y0: q.y0 + Math.floor(i / 2) * half,
      size: half,
      cx: 0,
      cy: 0,
      mass: 0,
      maxRadius: 0,
      leaf: true,
      children: null,
      body: [],
    })
  }
  return out
}

function contains(q: QuadNode, x: number, y: number): boolean {
  return x >= q.x0 && x < q.x0 + q.size && y >= q.y0 && y < q.y0 + q.size
}

/** Squared distance from a point to the nearest edge of a cell (0 inside). */
function minDist2(q: QuadNode, x: number, y: number): number {
  const dx = x < q.x0 ? q.x0 - x : x > q.x0 + q.size ? x - (q.x0 + q.size) : 0
  const dy = y < q.y0 ? q.y0 - y : y > q.y0 + q.size ? y - (q.y0 + q.size) : 0
  return dx * dx + dy * dy
}

export function buildQuadtree(
  nodes: ReadonlyArray<GraphNode>,
  opts?: QuadtreeOptions,
): Quadtree {
  return new Quadtree(nodes, opts)
}

/**
 * One pass of the 1/d² repulsion, Barnes–Hut style.
 *
 * `nodes` must be the same array (same order) the tree was built from; the tree
 * stores indices into it. Only velocities are written — positions are integrated
 * by the caller after springs and gravity, exactly like the exact pairwise tick.
 */
export function repulse(
  nodes: ReadonlyArray<GraphNode>,
  tree: Quadtree,
  alpha: number,
): void {
  const bodies = tree.nodes
  const theta2 = tree.theta * tree.theta
  const stack: QuadNode[] = []
  for (let i = 0; i < nodes.length; i++) {
    const target = nodes[i]
    let sp = 0
    stack[sp++] = tree.root
    while (sp > 0) {
      const q = stack[--sp]
      tree.visits++
      if (q.mass === 0) continue
      const dx = q.cx - target.x
      const dy = q.cy - target.y
      const d2 = dx * dx + dy * dy
      // A cell that holds the body itself cannot be approximated: its centre of
      // mass would include the body's own repulsion against itself. Open it.
      if (!contains(q, target.x, target.y) && d2 > 0 && q.size * q.size < theta2 * d2) {
        const f = (REPULSION_K * alpha * q.mass) / d2
        const inv = f / Math.sqrt(d2)
        target.vx -= dx * inv
        target.vy -= dy * inv
      } else if (q.leaf) {
        for (const j of q.body!) {
          if (j === i) continue
          tree.visits++
          const other = bodies[j]
          let ex = other.x - target.x
          let ey = other.y - target.y
          let e2 = ex * ex + ey * ey
          if (e2 < 0.01) {
            const angle = coincidentAngle(i, j)
            ex = Math.cos(angle) * 2
            ey = Math.sin(angle) * 2
            e2 = 4
          }
          const d = Math.sqrt(e2)
          const f = (REPULSION_K * alpha) / e2
          target.vx -= (ex / d) * f
          target.vy -= (ey / d) * f
        }
      } else {
        for (const child of q.children!) stack[sp++] = child
      }
    }
  }
}

/**
 * Keep node footprints (and therefore labels) apart, on the same terms as the
 * exact tick: a pair closer than its two collide radii plus padding is pushed
 * apart proportionally to the overlap. Short range, so the tree prunes whole
 * cells whose nearest body is already out of reach; the pair itself is still
 * measured exactly, so this is not an approximation.
 *
 * `alpha` mirrors the exact tick's scaling and defaults to 1.
 */
export function collide(
  nodes: ReadonlyArray<GraphNode>,
  tree: Quadtree,
  alpha = 1,
): void {
  const bodies = tree.nodes
  const stack: QuadNode[] = []
  for (let i = 0; i < nodes.length; i++) {
    const a = nodes[i]
    const ra = collideRadius(a)
    let sp = 0
    stack[sp++] = tree.root
    while (sp > 0) {
      const q = stack[--sp]
      tree.visits++
      if (q.mass === 0) continue
      // No body in this cell can reach `a`: prune the whole subtree.
      const reach = ra + q.maxRadius + COLLIDE_PAD
      if (minDist2(q, a.x, a.y) > reach * reach) continue
      if (q.leaf) {
        for (const j of q.body!) {
          // Each unordered pair is applied once, the same way the pairwise loop
          // applies it (i is the lower index and is pushed away from j).
          if (j <= i) continue
          tree.visits++
          const b = bodies[j]
          let dx = b.x - a.x
          let dy = b.y - a.y
          let d2 = dx * dx + dy * dy
          if (d2 < 0.01) {
            const angle = coincidentAngle(i, j)
            dx = Math.cos(angle) * 2
            dy = Math.sin(angle) * 2
            d2 = 4
          }
          const d = Math.sqrt(d2)
          const minD = ra + collideRadius(b) + COLLIDE_PAD
          if (d >= minD) continue
          const f = (minD - d) * COLLIDE_STRENGTH * alpha
          const fx = (dx / d) * f
          const fy = (dy / d) * f
          a.vx -= fx
          a.vy -= fy
          b.vx += fx
          b.vy += fy
        }
      } else {
        for (const child of q.children!) stack[sp++] = child
      }
    }
  }
}
