// Painting the engagement graph.
//
// This module owns what a frame costs, not what the graph means. Positions come
// from the simulation, relations from the ledger, and docs/33 §4.3 keeps the
// budget honest here:
//
//   viewport clipping — a node/edge/label outside the camera rectangle is not
//                       drawn (clipping is a draw filter: the model is untouched)
//   LOD               — at low zoom the map is dots and short lines; floating
//                       labels obey a per-frame budget, emphasis labels never do
//   geometry cache    — while the layout is settled the Bézier of every edge is
//                       reused by edge id; a new picture (edge set change) drops it
//   label occupancy   — collision boxes live in a 64 world-unit grid, not a list
//
// Everything here must stay loadable without a DOM: node tests import this
// module (and the value imports below carry their .ts extension for Node ESM),
// so `document` is only touched lazily inside `paint`.

import type { GraphEdge, GraphNode } from './model.ts'
import { edgeKindLabel } from './model.ts'
import { radiusOf, type DepthBand } from './sim.ts'

export interface Camera {
  x: number
  y: number
  k: number
}

/** How much attention the focus layer asks for a node (docs/33 §4.3, P1-1). */
export type FocusTemperature = 'hot' | 'warm' | 'cold'
export type FocusTemperatures = ReadonlyMap<string, FocusTemperature>

export interface PaintOptions {
  ctx: CanvasRenderingContext2D
  width: number
  height: number
  dpr: number
  cam: Camera
  nodes: GraphNode[]
  edges: GraphEdge[]
  byId: Map<string, GraphNode>
  selected: string | null
  hovered: string | null
  matches: Set<string> | null
  focus: Set<string> | null
  /** Operator toggle: show every intent label, not only the focused/running ones. */
  showLabels: boolean
  time: number
  /** Layered layout draws depth bands; the force map stays paper-like. */
  layered: boolean
  /** False honours `prefers-reduced-motion`: no comets, pulses or ripples. */
  motion: boolean
  /** Node id → performance.now() when it first appeared (scale-in + ripple). */
  appear: Map<string, number>
  /** Depth columns, computed by the layered simulation. */
  bands: DepthBand[]
  /** True = layout and drags are at rest: edge geometry and label buckets may be
   *  reused. False = rebuild the geometry every frame (nodes are still moving). */
  settled: boolean
  /** Optional focus layer (P1-1). null/undefined = off. */
  focusLayer?: FocusTemperatures | null
  /** Keyboard focus node (canvas arrow-key selection); undefined = none. */
  keyboardFocus?: string | null
}

// Red is one meaning only: the executor is running this intent right now
// (solid edge + marching dashes + a travelling comet). A failed intent is not
// executing anything, so it gets its own hue instead of borrowing the live red.
const FACT_COLORS: Record<string, string> = {
  vuln: '#ff4d4d',
  cred: '#b388ff',
  host: '#6aa9ff',
  service: '#2dd4bf',
  loot: '#ffc457',
  artifact: '#ff9f43',
  note: '#8b8b93',
}

const INTENT_COLORS: Record<string, string> = {
  running: '#ff4d4d',
  claimed: '#ff9f43',
  pending: '#f2c94c',
  done: '#39d98a',
  failed: '#e879f9',
  parked: '#7b8494',
}

// Teal is the "reading choice" family: reference is a dashed teal line, pin is
// the same hue as a solid double line so the operator never has to guess whether
// a relation is a gate (blue premise) or context they asked to see.
const PIN_COLOR = 'rgba(45,212,191,0.95)'
const REFERENCE_COLOR = 'rgba(45,212,191,0.6)'
const FOCUS_COLD_ALPHA = 0.18
const FOCUS_HOT_RING = 'rgba(255,255,255,0.92)'
const KEYBOARD_FOCUS_RING = 'rgba(56,189,248,0.95)'
const REVIEW_RING = 'rgba(245,158,11,0.9)'

export function nodeColor(n: GraphNode): string {
  if (n.type === 'ghost' || n.type === 'step') return INTENT_COLORS[n.status ?? ''] ?? '#9aa0aa'
  if (n.type === 'project') return '#8b8b93'
  if (n.type === 'intent') return INTENT_COLORS[n.status ?? ''] ?? '#9aa0aa'
  if (n.type === 'goal') return n.status === 'met' ? '#39d98a' : '#ffd166'
  // A boundary leaf is not a fact: its colour says why it is not on the map.
  // not_loaded is expandable; missing is gone from the ledger; rejected exists
  // but was explicitly negated. The same grey for all three hid the difference.
  if (n.type === 'boundary') {
    if (n.boundaryState === 'missing') return 'rgba(255,77,77,0.72)'
    if (n.boundaryState === 'rejected') return 'rgba(232,121,249,0.72)'
    return '#9aa0aa'
  }
  return FACT_COLORS[n.kind ?? ''] ?? '#9aa0aa'
}

// Tier rings are a second, quieter channel: status colour already means "what
// is happening", so tier is a thin outer ring, not another fill.
export const TIER_COLORS: Record<string, string> = {
  worker: '#2dd4bf',
  strong: '#f59e0b',
}

function edgeColor(e: GraphEdge): string {
  const kind = e.kind
  if (kind === 'intent') return INTENT_COLORS[e.status ?? ''] ?? '#9aa0aa'
  switch (kind) {
    case 'brief':
      return 'rgba(139,139,147,0.5)'
    case 'host':
      return 'rgba(139,139,147,0.35)'
    case 'goal':
      return 'rgba(255,209,102,0.4)'
    case 'spawn':
      return 'rgba(179,136,255,0.75)'
    // Blue = a hard prerequisite; a reference must never borrow that colour.
    case 'premise':
      return 'rgba(106,169,255,0.9)'
    // Teal dashed = a reading choice (reference); teal solid double = a pin.
    case 'reference':
      return REFERENCE_COLOR
    case 'pin':
      return PIN_COLOR
    // Gold = a fact that backs an achieved goal's evidence.
    case 'supports':
      return 'rgba(255,209,102,0.55)'
    default:
      return 'rgba(255,77,77,0.3)'
  }
}

/** Horizontal half-extent of the drawn shape: where an edge may touch it. */
export function halfExtentX(n: GraphNode, layered: boolean): number {
  if (n.type === 'fact' || n.type === 'ghost') return radiusOf(n)
  // In the chain-tree view a step is a labelled pill; the force map keeps the
  // small diamond so springs still read as a force picture.
  if (n.type === 'step') return layered ? Math.max(n.w, 8) : 8
  return Math.max(n.w, radiusOf(n))
}

/** Distance from the centre to the drawn boundary in a given direction. */
function boundaryAlong(n: GraphNode, ux: number, uy: number): number {
  if (n.type === 'fact' || n.type === 'ghost') return radiusOf(n)
  if (n.type === 'step') return 8
  const tx = Math.abs(ux) > 1e-6 ? n.w / Math.abs(ux) : Infinity
  const ty = Math.abs(uy) > 1e-6 ? n.h / Math.abs(uy) : Infinity
  return Math.min(tx, ty)
}

/** A cubic Bézier in graph coordinates, shared by the painter and hit-testing. */
export interface EdgeCurve {
  sx: number
  sy: number
  c1x: number
  c1y: number
  c2x: number
  c2y: number
  tx: number
  ty: number
}

/**
 * Edge geometry. In the chain-tree view every hop goes right, so an edge leaves
 * the right side of its source and enters the left side of its target with
 * horizontal tangents: a fan of children nests instead of crossing, and a line
 * never runs through the pill it starts from. The force map keeps the radial,
 * slightly bent spoke.
 */
export function edgeCurve(
  s: GraphNode,
  t: GraphNode,
  layered: boolean,
  kind: GraphEdge['kind'],
): EdgeCurve {
  if (layered) {
    const sx = s.x + halfExtentX(s, true)
    const sy = s.y
    const tx = t.x - halfExtentX(t, true)
    const ty = t.y
    const dx = tx - sx
    if (dx > 16) {
      return {
        sx,
        sy,
        c1x: sx + dx * 0.45,
        c1y: sy,
        c2x: tx - dx * 0.45,
        c2y: ty,
        tx,
        ty,
      }
    }
    // A cross-link pointing back (host edge between two fact columns, a retry
    // lineage, …) is not a forward hop: draw it as the ordinary curved spoke so
    // it visibly differs from the chain.
  }
  const dx = t.x - s.x
  const dy = t.y - s.y
  const d = Math.max(Math.hypot(dx, dy), 0.001)
  const ux = dx / d
  const uy = dy / d
  const r1 = boundaryAlong(s, ux, uy)
  const r2 = boundaryAlong(t, ux, uy)
  const sx = s.x + ux * r1
  const sy = s.y + uy * r1
  const tx = t.x - ux * r2
  const ty = t.y - uy * r2
  const bend = kind === 'brief' ? 0.05 : kind === 'spawn' ? 0.18 : 0.08
  const mx = (sx + tx) / 2
  const my = (sy + ty) / 2
  const ctrlX = mx - (ty - sy) * bend
  const ctrlY = my + (tx - sx) * bend
  return {
    sx,
    sy,
    c1x: sx + (2 / 3) * (ctrlX - sx),
    c1y: sy + (2 / 3) * (ctrlY - sy),
    c2x: tx + (2 / 3) * (ctrlX - tx),
    c2y: ty + (2 / 3) * (ctrlY - ty),
    tx,
    ty,
  }
}

/** Point on a cubic at t ∈ [0,1]; the live comet rides this. */
export function pointOnCurve(c: EdgeCurve, t: number): { x: number; y: number } {
  const inv = 1 - t
  const a = inv * inv * inv
  const b = 3 * inv * inv * t
  const d = 3 * inv * t * t
  const e = t * t * t
  return {
    x: a * c.sx + b * c.c1x + d * c.c2x + e * c.tx,
    y: a * c.sy + b * c.c1y + d * c.c2y + e * c.ty,
  }
}

// ---------------------------------------------------------------------------
// Viewport clipping (docs/33 §4.3). These are pure helpers on purpose: the node
// tests assert the draw filter with no canvas involved.
// ---------------------------------------------------------------------------

export interface Viewport {
  x0: number
  y0: number
  x1: number
  y1: number
}

/** World-space margin around the visible rectangle, so a node straddling the
 *  canvas edge still draws. */
export const VIEWPORT_MARGIN = 80

/** The world rectangle the camera can currently see (plus the margin). */
export function viewportOf(cam: Camera, width: number, height: number): Viewport {
  const k = Math.max(cam.k, 0.001)
  return {
    x0: -cam.x / k - VIEWPORT_MARGIN,
    y0: -cam.y / k - VIEWPORT_MARGIN,
    x1: (width - cam.x) / k + VIEWPORT_MARGIN,
    y1: (height - cam.y) / k + VIEWPORT_MARGIN,
  }
}

function pointInViewport(x: number, y: number, vp: Viewport): boolean {
  return x >= vp.x0 && x <= vp.x1 && y >= vp.y0 && y <= vp.y1
}

/** Half extents of a node's drawn footprint (labels excluded; a label's own box
 *  is what the label pass measures). */
function nodeHalfW(n: GraphNode): number {
  if (n.type === 'fact' || n.type === 'ghost') return radiusOf(n)
  return Math.max(n.w, radiusOf(n))
}

function nodeHalfH(n: GraphNode): number {
  if (n.type === 'fact' || n.type === 'ghost') return radiusOf(n)
  return Math.max(n.h, radiusOf(n))
}

/** Is any part of the node's glyph inside the viewport? */
export function isNodeVisible(n: GraphNode, vp: Viewport): boolean {
  return (
    n.x + nodeHalfW(n) >= vp.x0 &&
    n.x - nodeHalfW(n) <= vp.x1 &&
    n.y + nodeHalfH(n) >= vp.y0 &&
    n.y - nodeHalfH(n) <= vp.y1
  )
}

/** Conservative curve-vs-rectangle test: the Bézier lies inside the convex hull
 *  of its four control points, so a hull that misses the viewport misses the
 *  viewport too. */
function curveInViewport(c: EdgeCurve, vp: Viewport): boolean {
  const minX = Math.min(c.sx, c.c1x, c.c2x, c.tx)
  const maxX = Math.max(c.sx, c.c1x, c.c2x, c.tx)
  if (maxX < vp.x0 || minX > vp.x1) return false
  const minY = Math.min(c.sy, c.c1y, c.c2y, c.ty)
  const maxY = Math.max(c.sy, c.c1y, c.c2y, c.ty)
  return !(maxY < vp.y0 || minY > vp.y1)
}

/** Is the drawn curve inside the viewport? Uses the control-point hull, so an
 *  edge that crosses the screen between two off-screen nodes still draws. */
export function isEdgeVisible(
  s: GraphNode,
  t: GraphNode,
  kind: GraphEdge['kind'],
  layered: boolean,
  vp: Viewport,
): boolean {
  return curveInViewport(edgeCurve(s, t, layered, kind), vp)
}

/** The nodes the paint pass may draw: clipping is a filter over the same array,
 *  never a mutation of the model. Exported so the test can count the draws. */
export function clipNodes(nodes: readonly GraphNode[], vp: Viewport): GraphNode[] {
  return clipInto(nodes, vp, [])
}

/** `clipNodes` into a caller-owned buffer, so a frame can reuse one array. */
function clipInto(nodes: readonly GraphNode[], vp: Viewport, out: GraphNode[]): GraphNode[] {
  out.length = 0
  for (const n of nodes) if (isNodeVisible(n, vp)) out.push(n)
  return out
}

// The canvas repaints the same graph frame after frame; reusing one scratch
// array keeps the clip pass allocation-free (paint is not reentrant).
const visibleBuffer: GraphNode[] = []

// ---------------------------------------------------------------------------
// Level of detail (docs/33 §4.3): at low zoom a frame must cost a dot, not a
// pill plus a measured label.
// ---------------------------------------------------------------------------

/** Below this zoom the force map drops pills and curves for dots and lines. */
export const LOD_SHAPES_K = 0.22
/** Above this zoom fact names are worth their measured box. */
export const LOD_FACT_LABEL_K = 1.25
/** Force-map step titles: same threshold the painter always used. */
export const LOD_STEP_LABEL_K = 1.05

export interface Lod {
  /** Draw full pills/paths; false = dots and short straight lines only. */
  shapes: boolean
  /** Draw unselected fact labels at this zoom. */
  factLabels: boolean
  /** Draw background step labels at this zoom (showLabels forces them on). */
  stepLabels: boolean
  /** Background floating-label budget for one frame (sel/hover/hits exempt). */
  maxLabels: number
}

export function lod(camK: number, flags: { layered: boolean; showLabels: boolean }): Lod {
  const k = Number.isFinite(camK) && camK > 0 ? camK : 1
  // The chain tree is read through its pill text, so it keeps its shapes a
  // little longer than the paper-like force map when the operator zooms out.
  const shapesFrom = flags.layered ? LOD_SHAPES_K * 0.7 : LOD_SHAPES_K
  const shapes = k >= shapesFrom
  const factLabels = k >= LOD_FACT_LABEL_K
  const stepLabels = flags.showLabels || k >= LOD_STEP_LABEL_K
  const maxLabels = k >= 1.6 ? 160 : k >= 1.1 ? 90 : k >= 0.7 ? 48 : k >= 0.4 ? 24 : 12
  return { shapes, factLabels, stepLabels, maxLabels }
}

// ---------------------------------------------------------------------------
// Label occupancy grid (docs/33 §4.3). Boxes are inserted into 64 world-unit
// buckets; a query only walks the buckets its own box (plus the collision pad)
// touches. `overlaps` is equivalent to a linear scan by construction (widened
// boxes always share a bucket) and the test asserts that equivalence.
// ---------------------------------------------------------------------------

export interface Box {
  x1: number
  y1: number
  x2: number
  y2: number
}

export const BOX_CELL = 64
/** A box covering more than this many cells is kept on a short "wide" list and
 *  tested linearly: a label that spans half the map must not allocate thousands
 *  of bucket arrays. */
const MAX_BUCKET_CELLS = 256
/** A query covering more than this many cells scans every box instead. */
const MAX_QUERY_CELLS = 1024

export class BoxGrid {
  private readonly cell: number
  private readonly buckets = new Map<string, Box[]>()
  private readonly wide: Box[] = []
  private readonly all: Box[] = []

  constructor(cell = BOX_CELL) {
    this.cell = Number.isFinite(cell) && cell > 0 ? cell : BOX_CELL
  }

  /** Number of boxes inserted since the last clear. */
  get size(): number {
    return this.all.length
  }

  clear(): void {
    this.buckets.clear()
    this.wide.length = 0
    this.all.length = 0
  }

  insert(box: Box): void {
    this.all.push(box)
    const c = this.cell
    if (
      !Number.isFinite(box.x1) ||
      !Number.isFinite(box.y1) ||
      !Number.isFinite(box.x2) ||
      !Number.isFinite(box.y2)
    ) {
      this.wide.push(box)
      return
    }
    const cx0 = Math.floor(box.x1 / c)
    const cx1 = Math.floor(box.x2 / c)
    const cy0 = Math.floor(box.y1 / c)
    const cy1 = Math.floor(box.y2 / c)
    if ((cx1 - cx0 + 1) * (cy1 - cy0 + 1) > MAX_BUCKET_CELLS) {
      this.wide.push(box)
      return
    }
    for (let cx = cx0; cx <= cx1; cx++) {
      for (let cy = cy0; cy <= cy1; cy++) {
        const key = `${cx},${cy}`
        const list = this.buckets.get(key)
        if (list) list.push(box)
        else this.buckets.set(key, [box])
      }
    }
  }

  /** Would `box` overlap any inserted box, with `pad` world units of slack? */
  overlaps(box: Box, pad = 0): boolean {
    if (this.all.length === 0) return false
    const c = this.cell
    const qx0 = box.x1 - pad
    const qy0 = box.y1 - pad
    const qx1 = box.x2 + pad
    const qy1 = box.y2 + pad
    if (!Number.isFinite(qx0) || !Number.isFinite(qy0) || !Number.isFinite(qx1) || !Number.isFinite(qy1)) {
      return this.linear(box, pad)
    }
    const cx0 = Math.floor(qx0 / c)
    const cx1 = Math.floor(qx1 / c)
    const cy0 = Math.floor(qy0 / c)
    const cy1 = Math.floor(qy1 / c)
    if ((cx1 - cx0 + 1) * (cy1 - cy0 + 1) > MAX_QUERY_CELLS) return this.linear(box, pad)
    for (let cx = cx0; cx <= cx1; cx++) {
      for (let cy = cy0; cy <= cy1; cy++) {
        const list = this.buckets.get(`${cx},${cy}`)
        if (!list) continue
        for (const b of list) if (boxesOverlap(box, b, pad)) return true
      }
    }
    for (const b of this.wide) if (boxesOverlap(box, b, pad)) return true
    return false
  }

  private linear(box: Box, pad: number): boolean {
    for (const b of this.all) if (boxesOverlap(box, b, pad)) return true
    return false
  }
}

function boxesOverlap(a: Box, b: Box, pad: number): boolean {
  return a.x1 < b.x2 + pad && a.x2 > b.x1 - pad && a.y1 < b.y2 + pad && a.y2 > b.y1 - pad
}

// ---------------------------------------------------------------------------
// Edge geometry cache (docs/33 §4.3). A settled frame reuses last frame's
// curves by edge id; `settled === false` clears them because the nodes moved.
// The identity string folds the edge set in, so a poll that added/removed any
// edge cannot hand a stale curve to a new picture.
// ---------------------------------------------------------------------------

/** Generic identity-scoped map: the picture key decides whether entries live. */
export class GeometryCache<V> {
  private readonly values = new Map<string, V>()
  private identity = ''

  get size(): number {
    return this.values.size
  }

  /** The identity of the picture the current entries belong to. */
  get currentIdentity(): string {
    return this.identity
  }

  has(key: string): boolean {
    return this.values.has(key)
  }

  get(key: string): V | undefined {
    return this.values.get(key)
  }

  set(key: string, value: V): void {
    this.values.set(key, value)
  }

  clear(): void {
    this.values.clear()
  }

  /** Start a picture. Returns true (and drops every entry) when the identity
   *  changed, i.e. the operator is looking at a different edge set now. */
  begin(identity: string): boolean {
    if (identity === this.identity) return false
    this.identity = identity
    this.values.clear()
    return true
  }
}

// Hashing the whole edge list every frame is avoidable: the simulation keeps the
// same edge array until it rebuilds the model, so the array reference is the key.
const identityMemo = new WeakMap<readonly GraphEdge[], string>()

function hashEdges(edges: readonly GraphEdge[]): string {
  let h = 2166136261
  for (const e of edges) {
    const id = e.id
    for (let i = 0; i < id.length; i++) {
      h ^= id.charCodeAt(i)
      h = Math.imul(h, 16777619)
    }
    // The id already carries source/target/kind/intent, but mixing the kind in
    // keeps an unchanged id with a changed meaning a distinct identity.
    const kind = e.kind
    for (let i = 0; i < kind.length; i++) {
      h ^= kind.charCodeAt(i)
      h = Math.imul(h, 16777619)
    }
  }
  return (h >>> 0).toString(36)
}

/** Identity of "the edge picture": edge count + id/kind hash + layout mode. */
export function edgeSetIdentity(edges: readonly GraphEdge[], layered: boolean): string {
  let base = identityMemo.get(edges)
  if (base === undefined) {
    base = `${edges.length}:${hashEdges(edges)}`
    identityMemo.set(edges, base)
  }
  return `${layered ? 'layered' : 'force'}:${base}`
}

/** The curve for one edge, from the cache when the frame may reuse geometry. */
export function cachedEdgeCurve(
  cache: GeometryCache<EdgeCurve>,
  edge: GraphEdge,
  source: GraphNode,
  target: GraphNode,
  layered: boolean,
  reuse: boolean,
): EdgeCurve {
  if (!reuse) return edgeCurve(source, target, layered, edge.kind)
  const hit = cache.get(edge.id)
  if (hit) return hit
  const curve = edgeCurve(source, target, layered, edge.kind)
  cache.set(edge.id, curve)
  return curve
}

const curveCache = new GeometryCache<EdgeCurve>()

// ---------------------------------------------------------------------------
// Background: one 26×26 offscreen tile + createPattern instead of a double
// fillRect loop per frame. Built lazily per context: the module top level must
// not touch `document`, because the node tests import this file.
// ---------------------------------------------------------------------------

const BACKGROUND_GAP = 26
const patternCache = new WeakMap<CanvasRenderingContext2D, CanvasPattern | null>()

function dotPattern(ctx: CanvasRenderingContext2D): CanvasPattern | null {
  if (patternCache.has(ctx)) return patternCache.get(ctx) ?? null
  let pattern: CanvasPattern | null = null
  try {
    const doc = typeof document !== 'undefined' ? document : null
    if (doc) {
      const tile = doc.createElement('canvas')
      tile.width = BACKGROUND_GAP
      tile.height = BACKGROUND_GAP
      const tileCtx = tile.getContext('2d')
      if (tileCtx) {
        tileCtx.fillStyle = 'rgba(255,255,255,0.035)'
        tileCtx.fillRect(0, 0, 1, 1)
        pattern = ctx.createPattern(tile, 'repeat')
      }
    }
  } catch {
    pattern = null
  }
  patternCache.set(ctx, pattern)
  return pattern
}

function paintBackground(
  ctx: CanvasRenderingContext2D,
  width: number,
  height: number,
  cam: Camera,
): void {
  const ox = cam.x % BACKGROUND_GAP
  const oy = cam.y % BACKGROUND_GAP
  const pattern = dotPattern(ctx)
  if (pattern) {
    ctx.save()
    ctx.fillStyle = pattern
    // The tile's dot sits at its origin; scrolling the pattern origin with the
    // camera keeps the paper still under the graph.
    ctx.translate(ox, oy)
    ctx.fillRect(-BACKGROUND_GAP, -BACKGROUND_GAP, width + BACKGROUND_GAP * 2, height + BACKGROUND_GAP * 2)
    ctx.restore()
    return
  }
  // No DOM (node tests never paint; this is for exotic 2d implementations).
  ctx.fillStyle = 'rgba(255,255,255,0.035)'
  for (let x = ox; x < width; x += BACKGROUND_GAP) {
    for (let y = oy; y < height; y += BACKGROUND_GAP) {
      ctx.fillRect(x, y, 1, 1)
    }
  }
}

function hashString(s: string): number {
  let h = 2166136261
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return (h >>> 0) / 4294967295
}

const TAU = Math.PI * 2
/** A screen-space-ish pad in world units: rings stay visible when zoomed out. */
function screenPad(camK: number, pixels: number): number {
  return pixels / Math.max(camK, 0.35)
}

/** A ring around a node's drawn shape, following the pill for boxed nodes. */
function nodeRing(ctx: CanvasRenderingContext2D, n: GraphNode, layered: boolean, pad: number): void {
  ctx.beginPath()
  if (n.type === 'fact' || n.type === 'ghost') {
    ctx.arc(n.x, n.y, radiusOf(n) + pad, 0, TAU)
    return
  }
  if (n.type === 'step' && !layered) {
    ctx.arc(n.x, n.y, Math.max(radiusOf(n), Math.hypot(n.w, n.h)) + pad, 0, TAU)
    return
  }
  roundRect(ctx, n.x - n.w - pad, n.y - n.h - pad, 2 * n.w + 2 * pad, 2 * n.h + 2 * pad, 9)
}

/** Trace one curve into the current path; the caller strokes it. */
function traceCurve(ctx: CanvasRenderingContext2D, c: EdgeCurve, dx = 0, dy = 0): void {
  ctx.beginPath()
  ctx.moveTo(c.sx + dx, c.sy + dy)
  ctx.bezierCurveTo(c.c1x + dx, c.c1y + dy, c.c2x + dx, c.c2y + dy, c.tx + dx, c.ty + dy)
}

// ---------------------------------------------------------------------------
// Text fitting cache. Measuring a long title character by character every frame
// showed up on large maps; the result is a pure function of (font, text, maxW),
// so it can be reused until the bounded cache is dropped.
// ---------------------------------------------------------------------------

const FIT_CACHE_LIMIT = 2048
const fitCache = new Map<string, string>()

/** Shorten to what fits the pill, measured rather than guessed. */
export function fitText(ctx: CanvasRenderingContext2D, text: string, maxW: number): string {
  const key = `${ctx.font}\u0000${maxW}\u0000${text}`
  const cached = fitCache.get(key)
  if (cached !== undefined) return cached
  let out = text
  if (ctx.measureText(text).width > maxW) {
    let s = text
    while (s.length > 1 && ctx.measureText(`${s}…`).width > maxW) s = s.slice(0, -1)
    out = `${s}…`
  }
  // Bounded: a cache miss is always cheaper than unbounded growth on a long run.
  if (fitCache.size >= FIT_CACHE_LIMIT) fitCache.clear()
  fitCache.set(key, out)
  return out
}

export function paint(o: PaintOptions) {
  const { ctx, width, height, dpr, cam, nodes, edges, byId } = o
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, width, height)
  paintBackground(ctx, width, height, cam)

  const vp = viewportOf(cam, width, height)
  const level = lod(cam.k, { layered: o.layered, showLabels: o.showLabels })

  ctx.save()
  ctx.translate(cam.x, cam.y)
  ctx.scale(cam.k, cam.k)

  // Existence (even an empty map) means the operator turned the layer on: the
  // contract is "everything not in the layer fades".
  const focusDimming = Boolean(o.focus)
  const layerDimming = Boolean(o.focusLayer)
  const dim = (id: string): number => {
    // The common frame has neither a selection nor a focus layer: do not pay for
    // two lookups per edge endpoint to learn "full alpha".
    if (!focusDimming && !layerDimming) return 1
    const focusDim = focusDimming && o.focus && !o.focus.has(id) ? FOCUS_COLD_ALPHA : 1
    if (!layerDimming || !o.focusLayer) return focusDim
    const temp = o.focusLayer.get(id)
    // hot keeps the node at full brightness; warm is untouched; cold (and
    // anything the layer does not name) fades, but never brighter than focus dim.
    if (temp === 'hot' || temp === 'warm') return focusDim
    return Math.min(focusDim, FOCUS_COLD_ALPHA)
  }
  const fadeIn = (id: string) => {
    if (!o.motion) return 1
    const t = o.appear.get(id)
    if (t === undefined) return 1
    return Math.min(1, Math.max(0, (o.time - t) / 380))
  }

  // Clipping is a draw filter only: the model, search and focus keep everything.
  const visible = clipInto(nodes, vp, visibleBuffer)

  // ---- depth bands (layered mode only) ----
  if (o.layered && o.bands.length > 0) {
    // Bands span the visible world (not just the nodes) so the columns read as
    // lanes. They tile the whole width — a column owns the space up to the
    // midpoint of the gap to the next column — so the alternating stripes frame
    // the hops instead of floating around each node cluster.
    const top = -cam.y / Math.max(cam.k, 0.001) - 40
    const bottom = (height - cam.y) / Math.max(cam.k, 0.001) + 40
    const lanes = o.bands.map((band, i) => {
      const prev = o.bands[i - 1]
      const next = o.bands[i + 1]
      const left = prev ? (prev.x2 + band.x1) / 2 : band.x1 - 60
      const right = next ? (band.x2 + next.x1) / 2 : band.x2 + 60
      return { band, left, right }
    })
    for (const [i, lane] of lanes.entries()) {
      if (lane.right < vp.x0 || lane.left > vp.x1) continue
      ctx.globalAlpha = i % 2 === 0 ? 0.03 : 0.015
      ctx.fillStyle = '#ffffff'
      ctx.fillRect(lane.left, top, lane.right - lane.left, bottom - top)
      if (i < lanes.length - 1) {
        ctx.globalAlpha = 0.18
        ctx.strokeStyle = 'rgba(255,255,255,0.35)'
        ctx.lineWidth = 1 / Math.max(cam.k, 0.5)
        ctx.setLineDash([2 / cam.k, 8 / cam.k])
        ctx.beginPath()
        ctx.moveTo(lane.right, top)
        ctx.lineTo(lane.right, bottom)
        ctx.stroke()
        ctx.setLineDash([])
      }
    }
  }

  // Screen-space occupancy, in world units (the camera scales uniformly): a
  // label may only be drawn where it does not sit on a node or another label.
  // Without this, nine intents pile their titles on top of each other at fit
  // zoom and the picture is unreadable.
  const boxes = new BoxGrid()
  for (const n of visible) {
    if (n.type === 'project' || n.type === 'goal' || (o.layered && n.type === 'step')) {
      boxes.insert({ x1: n.x - n.w - 6, y1: n.y - n.h - 6, x2: n.x + n.w + 6, y2: n.y + n.h + 6 })
    } else if (n.type === 'fact') {
      const r = radiusOf(n)
      boxes.insert({ x1: n.x - r, y1: n.y - r, x2: n.x + r, y2: n.y + r })
    } else if (n.type === 'step') {
      boxes.insert({ x1: n.x - 10, y1: n.y - 10, x2: n.x + 10, y2: n.y + 10 })
    } else {
      boxes.insert({ x1: n.x - 9, y1: n.y - 9, x2: n.x + 9, y2: n.y + 9 })
    }
  }

  // A label budget for the whole frame: unselected background labels stop at
  // lod().maxLabels; sel/hover/hit labels are exempt and always draw.
  const budget = { left: level.maxLabels }
  const takeLabel = (exempt: boolean): boolean => {
    if (exempt) return true
    if (budget.left <= 0) return false
    budget.left--
    return true
  }
  const drawFactLabel = (n: GraphNode, alpha: number, exempt: boolean): void => {
    const r = radiusOf(n)
    ctx.font = `${11}px "JetBrains Mono","Cascadia Code",monospace`
    ctx.textAlign = 'left'
    ctx.textBaseline = 'middle'
    const text = clip(n.label, 26)
    const w = ctx.measureText(text).width
    const box = { x1: n.x + r + 2, y1: n.y - 10, x2: n.x + r + 2 + w + 8, y2: n.y + 10 }
    if (!exempt && boxes.overlaps(box, 2)) return
    if (!takeLabel(exempt)) return
    boxes.insert(box)
    ctx.globalAlpha = alpha * 0.92
    ctx.fillStyle = 'rgba(10,10,11,0.72)'
    ctx.fillRect(n.x + r + 4, n.y - 8, w + 6, 16)
    ctx.fillStyle = 'rgba(233,233,238,0.92)'
    ctx.fillText(text, n.x + r + 7, n.y + 0.5)
  }

  // ---- edges ----
  const reuse = o.settled
  curveCache.begin(edgeSetIdentity(edges, o.layered))
  if (!reuse) curveCache.clear()

  for (const e of edges) {
    const s = byId.get(e.source)
    const t = byId.get(e.target)
    if (!s || !t) continue
    // Cheap reject before any geometry: both ends on the same outside side.
    if (
      (s.x < vp.x0 && t.x < vp.x0) ||
      (s.x > vp.x1 && t.x > vp.x1) ||
      (s.y < vp.y0 && t.y < vp.y0) ||
      (s.y > vp.y1 && t.y > vp.y1)
    ) {
      continue
    }
    const kind = e.kind
    const isSel = e.id === o.selected
    const isHover = e.id === o.hovered
    const curve = cachedEdgeCurve(curveCache, e, s, t, o.layered, reuse)
    if (!curveInViewport(curve, vp)) continue

    const alpha = Math.min(dim(s.id), dim(t.id)) * Math.min(fadeIn(s.id), fadeIn(t.id))
    const intentEdge = kind === 'intent'
    // A step→fact edge is one leaf of a fan: dozens of them must not out-shout
    // the chain. They are thinner and quieter until the pointer or a selection
    // makes that direction the subject.
    const fan = intentEdge && t.type === 'fact'
    const emphasized = isSel || isHover || Boolean(o.focus && (o.focus.has(s.id) || o.focus.has(t.id)))

    if (!level.shapes) {
      // Dot-level zoom: one straight line, no curve, dashes, arrows or comets.
      ctx.globalAlpha = alpha
      ctx.strokeStyle = edgeColor(e)
      ctx.lineWidth = (isSel || isHover ? 2 : 1) / Math.max(cam.k, 0.5)
      ctx.setLineDash([])
      ctx.lineDashOffset = 0
      ctx.beginPath()
      ctx.moveTo(s.x, s.y)
      ctx.lineTo(t.x, t.y)
      ctx.stroke()
      continue
    }

    ctx.globalAlpha = alpha * (fan && !emphasized ? 0.62 : 1)
    ctx.strokeStyle = edgeColor(e)
    ctx.lineWidth =
      (intentEdge
        ? (isSel ? 2.8 : fan ? 1.1 : 1.7)
        : kind === 'premise'
          ? (isSel ? 2.4 : 1.5)
          : kind === 'spawn'
            ? (isSel ? 2 : 1.2)
            : kind === 'brief'
              ? 1.2
              : kind === 'reference'
                ? (isSel ? 2 : 1.1)
                : kind === 'pin'
                  ? (isSel ? 2.4 : 1.5)
                  : kind === 'supports'
                    ? (isSel ? 2 : 1)
                    : kind === 'goal'
                      ? (isSel ? 2 : 1.2)
                      : 1) / Math.max(cam.k, 0.5)
    const running = intentEdge && e.status === 'running'
    const dashed =
      (intentEdge && (e.status === 'pending' || e.status === 'parked')) ||
      kind === 'spawn' ||
      kind === 'reference' ||
      kind === 'supports'
    ctx.setLineDash(
      kind === 'spawn'
        ? [6 / cam.k, 5 / cam.k]
        : kind === 'reference'
          ? [5 / cam.k, 4 / cam.k]
          : kind === 'supports'
            ? [2 / cam.k, 4 / cam.k]
            : dashed
              ? [5 / cam.k, 4 / cam.k]
              : running
                ? [7 / cam.k, 5 / cam.k]
                : kind === 'host'
                  ? [4 / cam.k, 4 / cam.k]
                  : [],
    )
    ctx.lineDashOffset = running ? -((o.time / 45) % 12) / Math.max(cam.k, 0.5) : 0
    traceCurve(ctx, curve)
    ctx.stroke()

    // A pin is a solid *double* teal line: the second parallel stroke is what
    // keeps it apart from the dashed reference at a glance.
    if (kind === 'pin') {
      const dx = t.x - s.x
      const dy = t.y - s.y
      const len = Math.max(Math.hypot(dx, dy), 0.001)
      const off = 1.5 / Math.max(cam.k, 0.5)
      const ox = (-dy / len) * off
      const oy = (dx / len) * off
      traceCurve(ctx, curve, ox, oy)
      ctx.stroke()
      traceCurve(ctx, curve, -ox, -oy)
      ctx.stroke()
    }

    // Arrowheads point into the node the hop produced. A leaf fan is only
    // arrowed when it is the subject; otherwise 30 triangles are noise. Relation
    // edges (premise/reference/pin/supports/goal) always point at what they feed.
    if (
      (intentEdge && (!fan || emphasized)) ||
      kind === 'brief' ||
      kind === 'spawn' ||
      kind === 'premise' ||
      kind === 'reference' ||
      kind === 'pin' ||
      kind === 'supports' ||
      kind === 'goal'
    ) {
      const ang = Math.atan2(curve.ty - curve.c2y, curve.tx - curve.c2x)
      const size = 6 / Math.max(cam.k, 0.5)
      ctx.beginPath()
      ctx.moveTo(curve.tx, curve.ty)
      ctx.lineTo(curve.tx - Math.cos(ang - 0.4) * size, curve.ty - Math.sin(ang - 0.4) * size)
      ctx.lineTo(curve.tx - Math.cos(ang + 0.4) * size, curve.ty - Math.sin(ang + 0.4) * size)
      ctx.closePath()
      ctx.fillStyle = edgeColor(e)
      ctx.fill()
    }

    // Travelling comet: the live work visibly moves from context to output.
    if (running && o.motion) {
      const phase = (o.time / 1100 + hashString(e.id) * 0.7) % 1
      drawComet(ctx, o, curve, phase, edgeColor(e))
    }
  }
  ctx.setLineDash([])
  ctx.lineDashOffset = 0

  // ---- floating labels (spawn lineage + intent steps + emphasised names) ----
  type Floating = {
    lx: number
    ly: number
    text: string
    width: number
    alpha: number
    priority: number
    color: string
    /** Selected/hovered/searched labels never count against the budget. */
    exempt: boolean
  }
  const floaters: Floating[] = []
  ctx.font = `600 11px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
  for (const e of edges) {
    if (e.kind !== 'spawn' || !e.label) continue
    const s = byId.get(e.source)
    const t = byId.get(e.target)
    if (!s || !t) continue
    const isSel = e.id === o.selected
    const isHover = e.id === o.hovered
    if (!isSel && !isHover && !o.showLabels && cam.k < 1.8) continue
    const curve = cachedEdgeCurve(curveCache, e, s, t, o.layered, reuse)
    if (!curveInViewport(curve, vp)) continue
    const mid = pointOnCurve(curve, 0.5)
    if (!pointInViewport(mid.x, mid.y, vp)) continue
    const text = fitText(ctx, e.label, 90)
    floaters.push({
      lx: mid.x,
      ly: mid.y,
      text,
      width: ctx.measureText(text).width + 10,
      alpha: Math.min(dim(s.id), dim(t.id)),
      priority: isSel ? 0 : isHover ? 1 : 4,
      color: edgeColor(e),
      exempt: isSel || isHover,
    })
  }
  // Hover/selection names the relation it is on: a reference/pin must read as
  // "引用"/"固定" and a hard prerequisite as "必要前提", because the same line
  // used to mean both. Labels stay off the resting picture; the pointer asks.
  for (const e of edges) {
    const kind = e.kind
    if (
      kind !== 'premise' &&
      kind !== 'reference' &&
      kind !== 'supports' &&
      kind !== 'goal' &&
      kind !== 'pin'
    )
      continue
    const isSel = e.id === o.selected
    const isHover = e.id === o.hovered
    if (!isSel && !isHover) continue
    const s = byId.get(e.source)
    const t = byId.get(e.target)
    if (!s || !t) continue
    const curve = cachedEdgeCurve(curveCache, e, s, t, o.layered, reuse)
    if (!curveInViewport(curve, vp)) continue
    const mid = pointOnCurve(curve, 0.5)
    if (!pointInViewport(mid.x, mid.y, vp)) continue
    const text = edgeKindLabel(kind)
    floaters.push({
      lx: mid.x,
      ly: mid.y,
      text,
      width: ctx.measureText(text).width + 10,
      alpha: Math.min(dim(s.id), dim(t.id)),
      priority: isSel ? 0 : 1,
      color: edgeColor(e),
      exempt: true,
    })
  }
  for (const n of visible) {
    if (n.type !== 'step') continue
    // In the chain-tree view the step is a pill: its title is inside the node,
    // so there is no floating label to collide with the fan of produced facts.
    if (o.layered) continue
    const isSel = n.id === o.selected
    const isHover = n.id === o.hovered
    const isMatch = o.matches?.has(n.id) === true
    const running = n.status === 'running'
    if (!isSel && !isHover && !isMatch && !running && !level.stepLabels) continue
    const text = fitText(ctx, n.label, 190)
    floaters.push({
      lx: n.x,
      ly: n.y + 20,
      text,
      width: ctx.measureText(text).width + 10,
      alpha: dim(n.id),
      priority: isSel ? 0 : isHover ? 1 : running ? 2 : isMatch ? 2 : 3,
      color: nodeColor(n),
      exempt: isSel || isHover || isMatch,
    })
  }
  // Dot-level zoom keeps the names of the nodes the operator is acting on: the
  // glyph is a dot, so the label is the only identity left on screen.
  if (!level.shapes) {
    for (const n of visible) {
      if (n.type === 'fact') continue
      const isSel = n.id === o.selected
      const isHover = n.id === o.hovered
      const isMatch = o.matches?.has(n.id) === true
      if (!isSel && !isHover && !isMatch && n.id !== o.keyboardFocus) continue
      const text = fitText(ctx, n.label, 190)
      floaters.push({
        lx: n.x,
        ly: n.y + Math.max(n.h, radiusOf(n)) + 14,
        text,
        width: ctx.measureText(text).width + 10,
        alpha: dim(n.id),
        priority: isSel ? 0 : isHover ? 1 : 2,
        color: nodeColor(n),
        exempt: true,
      })
    }
  }
  floaters.sort((a, b) => a.priority - b.priority)
  for (const label of floaters) {
    if (!pointInViewport(label.lx, label.ly, vp)) continue
    const { lx, width } = label
    let ly = label.ly
    let box = { x1: lx - width / 2, y1: ly - 9, x2: lx + width / 2, y2: ly + 9 }
    if (label.priority > 2) {
      // A background label may step down/up a little to get out of the way of
      // its own node or a neighbouring one; skipping it entirely used to hide
      // every non-running step title behind its own glyph box.
      let placed = false
      for (const dy of [0, 20, -20, 40, -40]) {
        const candidate = { x1: box.x1, y1: box.y1 + dy, x2: box.x2, y2: box.y2 + dy }
        if (!boxes.overlaps(candidate, 3)) {
          ly += dy
          box = candidate
          placed = true
          break
        }
      }
      if (!placed) continue
    }
    if (!takeLabel(label.exempt)) continue
    boxes.insert(box)
    ctx.globalAlpha = label.alpha
    ctx.fillStyle = 'rgba(12,12,15,0.88)'
    ctx.fillRect(lx - width / 2, ly - 9, width, 18)
    ctx.strokeStyle = label.color
    ctx.lineWidth = 0.8 / Math.max(cam.k, 0.5)
    ctx.strokeRect(lx - width / 2, ly - 9, width, 18)
    ctx.fillStyle = label.color
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.fillText(label.text, lx, ly + 0.5)
  }

  // ---- nodes ----
  for (const n of visible) {
    const color = nodeColor(n)
    const alpha = dim(n.id)
    const isSel = n.id === o.selected
    const isHover = n.id === o.hovered
    const isMatch = o.matches?.has(n.id) === true
    const keyboard = n.id === o.keyboardFocus
    const born = o.appear.get(n.id)
    const age = o.motion && born !== undefined ? o.time - born : 9999
    const entry = o.motion && age < 360 ? 0.55 + 0.45 * (age / 360) : 1

    if (!level.shapes) {
      // Dot-level zoom: one dot per node, no pill, no per-node text. The
      // emphasised node still gets its ring and (below) its label.
      ctx.globalAlpha = alpha * fadeIn(n.id)
      const r = Math.max(radiusOf(n), 2 / Math.max(cam.k, 0.001))
      ctx.beginPath()
      ctx.arc(n.x, n.y, r, 0, TAU)
      ctx.fillStyle = color
      ctx.fill()
      if (isSel || isHover || isMatch || keyboard) {
        ctx.beginPath()
        ctx.arc(n.x, n.y, r + screenPad(cam.k, 3), 0, TAU)
        ctx.strokeStyle = keyboard
          ? KEYBOARD_FOCUS_RING
          : isMatch
            ? 'rgba(255,77,77,0.9)'
            : 'rgba(255,255,255,0.7)'
        ctx.lineWidth = 1.4 / Math.max(cam.k, 0.5)
        ctx.stroke()
      }
      if (n.type === 'fact' && (isSel || isHover || isMatch)) drawFactLabel(n, alpha, true)
      ctx.globalAlpha = alpha
      continue
    }

    ctx.globalAlpha = alpha * fadeIn(n.id)

    // New node: expanding ripple, drawn under the glyph.
    if (o.motion && age < 1100) {
      const k = age / 1100
      const r = Math.max(radiusOf(n), 9) + k * 26
      ctx.beginPath()
      ctx.arc(n.x, n.y, r, 0, TAU)
      ctx.strokeStyle = color
      ctx.lineWidth = 1.4 / cam.k
      ctx.globalAlpha = alpha * (1 - k) * 0.55
      ctx.stroke()
      ctx.globalAlpha = alpha * fadeIn(n.id)
    }

    if (n.type === 'intent' || n.type === 'goal' || n.type === 'project' || (o.layered && n.type === 'step')) {
      const w = n.w * 2
      const h = n.h * 2
      const x = n.x - n.w
      const y = n.y - n.h
      const r = 7
      ctx.beginPath()
      roundRect(ctx, x, y, w, h, r)
      ctx.fillStyle = 'rgba(18,18,21,0.92)'
      ctx.fill()
      ctx.lineWidth = (isSel ? 2.4 : 1.4) / cam.k
      ctx.strokeStyle = color
      ctx.stroke()

      if ((n.type === 'intent' || n.type === 'step') && n.status === 'running' && o.motion) {
        const pulse = 0.5 + 0.5 * Math.sin(o.time / 420)
        ctx.beginPath()
        roundRect(ctx, x - 3, y - 3, w + 6, h + 6, r + 3)
        ctx.strokeStyle = color
        ctx.globalAlpha = alpha * 0.35 * pulse
        ctx.lineWidth = 2 / cam.k
        ctx.stroke()
        ctx.globalAlpha = alpha
      }

      // Tier ring: a thin outer outline, never a second fill. The chain-tree
      // view replaces it with the actual model name on the pill's second line —
      // "strong" alone does not tell the operator which model ran the direction —
      // so the ring is only kept for the force map.
      const tierColor = n.tier && !(o.layered && n.type === 'step') ? TIER_COLORS[n.tier] : undefined
      if (tierColor) {
        ctx.beginPath()
        roundRect(ctx, x - 4.5, y - 4.5, w + 9, h + 9, r + 4)
        ctx.strokeStyle = tierColor
        ctx.globalAlpha = alpha * 0.8
        ctx.lineWidth = 1.2 / cam.k
        ctx.setLineDash([3 / cam.k, 3 / cam.k])
        ctx.stroke()
        ctx.setLineDash([])
        ctx.globalAlpha = alpha
      }

      ctx.textAlign = 'center'
      ctx.textBaseline = 'middle'
      if (n.type === 'step' && n.model) {
        // Two lines: the intent title and the model that ran it.
        ctx.font = `600 11.5px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
        ctx.fillStyle = color
        ctx.fillText(fitText(ctx, n.label, w - 16), n.x, n.y - 6)
        ctx.font = `500 10px "JetBrains Mono","Cascadia Code",monospace`
        ctx.fillStyle = 'rgba(233,233,238,0.68)'
        ctx.fillText(fitText(ctx, n.model, w - 16), n.x, n.y + 10)
      } else {
        ctx.font = `600 ${n.type === 'step' ? 11.5 : 12}px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
        ctx.fillStyle = color
        ctx.fillText(fitText(ctx, n.label, w - 14), n.x, n.y + 0.5)
      }
    } else if (n.type === 'step') {
      const r = 7 * entry
      const running = n.status === 'running'
      ctx.save()
      ctx.translate(n.x, n.y)
      ctx.rotate(Math.PI / 4)
      ctx.beginPath()
      ctx.rect(-r * 0.72, -r * 0.72, r * 1.44, r * 1.44)
      ctx.fillStyle = 'rgba(18,18,21,0.95)'
      ctx.fill()
      ctx.strokeStyle = color
      ctx.lineWidth = (isSel ? 2.2 : 1.5) / cam.k
      ctx.stroke()
      ctx.restore()

      if (running && o.motion) {
        const pulse = 0.55 + 0.45 * Math.sin(o.time / 320)
        ctx.beginPath()
        ctx.arc(n.x, n.y, 12, 0, TAU)
        ctx.strokeStyle = color
        ctx.lineWidth = 1.6 / cam.k
        ctx.globalAlpha = alpha * 0.4 * pulse
        ctx.stroke()
        ctx.globalAlpha = alpha
      }
    } else if (n.type === 'ghost') {
      // The un-produced end of an intent step: a small pulsing ring, never a
      // pill. It disappears when the fact node replaces it.
      const pulse = n.status === 'running' && o.motion ? 0.55 + 0.45 * Math.sin(o.time / 320) : 0.7
      ctx.beginPath()
      ctx.arc(n.x, n.y, 7, 0, TAU)
      ctx.strokeStyle = color
      ctx.lineWidth = 1.6 / cam.k
      ctx.globalAlpha = alpha * pulse
      ctx.stroke()
      // The ghost only marks "no produced fact yet"; the step pill already names
      // the model, so the force-map tier ring has nothing to add here.
      const tierColor = n.tier && !o.layered ? TIER_COLORS[n.tier] : undefined
      if (tierColor) {
        ctx.beginPath()
        ctx.arc(n.x, n.y, 10.5, 0, TAU)
        ctx.strokeStyle = tierColor
        ctx.globalAlpha = alpha * 0.85
        ctx.setLineDash([3 / cam.k, 3 / cam.k])
        ctx.stroke()
        ctx.setLineDash([])
      }
      ctx.beginPath()
      ctx.arc(n.x, n.y, 2.4, 0, TAU)
      ctx.fillStyle = color
      ctx.fill()
      ctx.globalAlpha = alpha
    } else {
      const r = radiusOf(n) * entry
      // Halo for nodes that carry evidence or are verified.
      if (n.status === 'verified' || (n.meta?.fact?.evidence_ids?.length ?? 0) > 0) {
        ctx.beginPath()
        ctx.arc(n.x, n.y, r + 4, 0, TAU)
        ctx.fillStyle = color
        ctx.globalAlpha = alpha * 0.1
        ctx.fill()
        ctx.globalAlpha = alpha
      }
      ctx.beginPath()
      ctx.arc(n.x, n.y, r, 0, TAU)
      ctx.fillStyle = color
      ctx.fill()
      ctx.lineWidth = (isSel ? 2 : 1) / cam.k
      ctx.strokeStyle = isSel ? '#ffffff' : 'rgba(0,0,0,0.45)'
      ctx.stroke()
      // needs_review: the claim asserted a version newer than the last review.
      // A dashed outer ring says "read this before trusting it" without touching
      // the verified halo above (which stays a soft fill).
      if (n.type === 'fact' && n.needsReview) {
        ctx.beginPath()
        ctx.arc(n.x, n.y, r + 4.5, 0, TAU)
        ctx.strokeStyle = REVIEW_RING
        ctx.globalAlpha = alpha * 0.95
        ctx.lineWidth = 1.3 / Math.max(cam.k, 0.5)
        ctx.setLineDash([3 / cam.k, 3 / cam.k])
        ctx.stroke()
        ctx.setLineDash([])
        ctx.globalAlpha = alpha
      }
      if (n.status === 'rejected') {
        ctx.beginPath()
        ctx.moveTo(n.x - r * 0.6, n.y - r * 0.6)
        ctx.lineTo(n.x + r * 0.6, n.y + r * 0.6)
        ctx.strokeStyle = '#0a0a0b'
        ctx.lineWidth = 1.6 / cam.k
        ctx.stroke()
      }
    }

    // Step decorations live outside the shape branch: the chain-tree view draws
    // a step as a pill, the force map as a diamond; both carry the same attempt
    // count and "continued from an earlier session" arc.
    if (n.type === 'step') {
      const hw = o.layered ? n.w : 8
      const hh = o.layered ? n.h : 8
      // Attempt badge: "×N" means the direction was retried, so the reader can
      // tell one clean attempt from a path that was walked again.
      if (n.attempts && n.attempts > 1) {
        const bx = n.x + hw - 1
        const by = n.y - hh - 1
        ctx.beginPath()
        ctx.arc(bx, by, 6, 0, TAU)
        ctx.fillStyle = 'rgba(56,189,248,0.92)'
        ctx.fill()
        ctx.lineWidth = 1 / Math.max(cam.k, 0.5)
        ctx.strokeStyle = 'rgba(10,10,11,0.9)'
        ctx.stroke()
        ctx.fillStyle = '#0a0a0b'
        ctx.font = `700 8px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
        ctx.textAlign = 'center'
        ctx.textBaseline = 'middle'
        ctx.fillText(`×${Math.min(n.attempts, 99)}`, bx, by + 0.5)
      }
      // Retry/resume arc: the step was continued from an earlier session.
      if (n.retried) {
        const top = n.y - hh - (o.layered ? 26 : 26)
        ctx.beginPath()
        ctx.moveTo(n.x - hw + 1, n.y - hh - 3)
        ctx.quadraticCurveTo(n.x, top, n.x + hw - 1, n.y - hh - 3)
        ctx.strokeStyle = color
        ctx.globalAlpha = alpha * 0.7
        ctx.lineWidth = 1.2 / cam.k
        ctx.setLineDash([3 / cam.k, 3 / cam.k])
        ctx.stroke()
        ctx.setLineDash([])
        if (o.layered || cam.k > 1.05 || isSel || isHover) {
          ctx.fillStyle = color
          ctx.font = `600 9px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
          ctx.textAlign = 'center'
          ctx.textBaseline = 'bottom'
          ctx.fillText('续跑', n.x, top - 1)
        }
        ctx.globalAlpha = alpha
      }
    }

    // Hover/search get a quiet halo. A selected node uses the focus dim instead
    // of a big ring: the related graph staying bright is the highlight. The
    // ring hugs the drawn glyph (a ~2.5px screen-space pad); the old +6 world
    // units read as a loud "big ring", and non-layered steps used to get a
    // Math.hypot-sized circle around their diamond.
    if ((isHover || isMatch) && !isSel) {
      const pad = screenPad(cam.k, 2.5)
      ctx.beginPath()
      if (n.type === 'step' && !o.layered) {
        // Force-map steps are diamonds: trace the same rounded box rotated onto
        // the diamond, so the ring sits just outside the glyph's real outline.
        const rr = 7 * entry * 0.72 + pad
        ctx.save()
        ctx.translate(n.x, n.y)
        ctx.rotate(Math.PI / 4)
        roundRect(ctx, -rr, -rr, 2 * rr, 2 * rr, 3)
        ctx.restore()
      } else if (
        n.type === 'intent' ||
        n.type === 'goal' ||
        n.type === 'project' ||
        (o.layered && n.type === 'step')
      ) {
        // Pill nodes: follow the box (radius 7) at a 2.5px offset.
        roundRect(
          ctx,
          n.x - n.w - pad,
          n.y - n.h - pad,
          2 * n.w + 2 * pad,
          2 * n.h + 2 * pad,
          7 + pad,
        )
      } else {
        // Facts, ghosts and boundary leaves: a thin ring one pad outside the dot.
        ctx.arc(n.x, n.y, radiusOf(n) + pad, 0, TAU)
      }
      ctx.strokeStyle = isMatch ? 'rgba(255,77,77,0.7)' : 'rgba(255,255,255,0.42)'
      ctx.lineWidth = 1 / Math.max(cam.k, 0.5)
      ctx.stroke()
    }

    // Focus layer: hot stays fully lit and gets an outer ring; warm is normal
    // (its dim() is already 1). Cold/faded nodes were dimmed above.
    if (o.focusLayer?.get(n.id) === 'hot') {
      nodeRing(ctx, n, o.layered, screenPad(cam.k, 7))
      ctx.strokeStyle = FOCUS_HOT_RING
      ctx.globalAlpha = alpha
      ctx.lineWidth = 1.5 / Math.max(cam.k, 0.5)
      ctx.stroke()
    }
    // Keyboard focus (canvas arrow keys) is a separate ring: the operator can
    // move the focus without selecting, so it must be visible next to selection.
    if (keyboard) {
      nodeRing(ctx, n, o.layered, screenPad(cam.k, 4))
      ctx.strokeStyle = KEYBOARD_FOCUS_RING
      ctx.globalAlpha = alpha
      ctx.lineWidth = 2 / Math.max(cam.k, 0.5)
      ctx.stroke()
    }

    // Labels for facts and boundary leaves: only what the pointer/search is on,
    // or a genuinely zoomed-in view. The graph is a map, not a table; hover is
    // the detail. A boundary leaf's label is what says why it is not a node
    // (`未载入` expandable vs `引用缺失` gone from the ledger vs `已否定`).
    if (
      (n.type === 'fact' || n.type === 'boundary') &&
      (isSel || isHover || isMatch || level.factLabels)
    ) {
      drawFactLabel(n, alpha, isSel || isHover || isMatch)
    }
  }

  // Warnings badge: a small amber disc with a count, drawn last so it sits on
  // top of every node family. Hover/selection still reads the full text in the
  // inspector; the badge is only "this direction has something to read".
  for (const n of visible) {
    const count = n.warnings?.length ?? 0
    if (count === 0) continue
    const alpha = dim(n.id)
    let bx = n.x
    let by = n.y
    if (n.type === 'intent') {
      bx = n.x + n.w - 2
      by = n.y - n.h - 2
    } else if (n.type === 'ghost') {
      bx = n.x + 10
      by = n.y - 10
    } else if (n.type === 'step') {
      bx = o.layered ? n.x + n.w - 2 : n.x + 9
      by = o.layered ? n.y - n.h - 2 : n.y - 9
    } else if (n.type === 'fact') {
      const r = radiusOf(n)
      bx = n.x + r + 1
      by = n.y - r - 1
    } else {
      bx = n.x + n.w - 6
      by = n.y - n.h - 4
    }
    ctx.globalAlpha = alpha
    ctx.beginPath()
    ctx.arc(bx, by, 6, 0, TAU)
    ctx.fillStyle = '#f59e0b'
    ctx.fill()
    ctx.lineWidth = 1 / Math.max(cam.k, 0.5)
    ctx.strokeStyle = 'rgba(10,10,11,0.9)'
    ctx.stroke()
    ctx.fillStyle = '#0a0a0b'
    ctx.font = `700 8.5px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.fillText(count > 9 ? '9+' : String(count), bx, by + 0.5)
  }

  ctx.globalAlpha = 1
  ctx.restore()

  // Depth labels are screen-space: readable at any zoom, never clipped by the
  // fit padding, and fixed while panning. The top corners are occupied by the
  // DOM filter card (left) and the view controls (right); a label that would sit
  // under one of them is dropped instead of peeking out from behind it.
  if (o.layered && o.bands.length > 0) {
    ctx.font = `600 10.5px "Inter","PingFang SC","Microsoft YaHei",sans-serif`
    ctx.textAlign = 'center'
    ctx.textBaseline = 'top'
    const left = 252
    const right = width - 268
    for (const band of o.bands) {
      if (band.x2 < vp.x0 || band.x1 > vp.x1) continue
      const sx = cam.x + ((band.x1 + band.x2) / 2) * cam.k
      if (sx < left || sx > right) continue
      ctx.fillStyle = 'rgba(233,233,238,0.55)'
      ctx.fillText(depthLabel(band.depth), sx, 8)
    }
  }
}

/** A comet moving along one edge: context → step → produced fact. */
function drawComet(
  ctx: CanvasRenderingContext2D,
  o: PaintOptions,
  curve: EdgeCurve,
  phase: number,
  color: string,
) {
  const r = 3.2 / Math.max(o.cam.k, 0.5)
  const at = (t: number) => pointOnCurve(curve, t)
  // A white-hot core over a coloured halo reads on top of both the dashed
  // running edge and the node colours; the tail makes the direction obvious.
  const head = at(phase)
  ctx.beginPath()
  ctx.arc(head.x, head.y, r * 2.1, 0, TAU)
  ctx.fillStyle = color
  ctx.globalAlpha = 0.28
  ctx.fill()
  for (let i = 4; i >= 0; i--) {
    const t = phase - i * 0.016
    if (t < 0) continue
    const p = at(t)
    ctx.beginPath()
    ctx.arc(p.x, p.y, r * (0.3 + (4 - i) * 0.12), 0, TAU)
    ctx.fillStyle = 'rgba(255,255,255,0.92)'
    ctx.globalAlpha = i === 0 ? 0.95 : 0.18 + (4 - i) * 0.1
    ctx.fill()
  }
  ctx.globalAlpha = 1
}

function depthLabel(depth: number): string {
  return depth === 0 ? '起点' : `第 ${depth} 跳`
}

function roundRect(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  w: number,
  h: number,
  r: number,
) {
  const rr = Math.min(r, w / 2, h / 2)
  ctx.moveTo(x + rr, y)
  ctx.arcTo(x + w, y, x + w, y + h, rr)
  ctx.arcTo(x + w, y + h, x, y + h, rr)
  ctx.arcTo(x, y + h, x, y, rr)
  ctx.arcTo(x, y, x + w, y, rr)
  ctx.closePath()
}

function clip(s: string, n: number): string {
  return s.length > n ? `${s.slice(0, n - 1)}…` : s
}
