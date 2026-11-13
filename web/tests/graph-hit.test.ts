// The canvas hit-test index: a hover on a settled map must be a few bucket
// lookups, not a walk over every node and every curve sample. These tests pin the
// two rules that make it safe to use instead of the linear scan:
//   1. it returns exactly what a brute-force scan returns (same node/edge id),
//      including at pads/tolerances that cross bucket boundaries;
//   2. it actually tests far fewer candidates than the full entity list.
// The brute-force reference here is written out independently on purpose — if
// hit.ts and the reference drifted together, the test would prove nothing.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { GraphIndex, HOVERABLE_EDGE_KINDS } from '../src/graph/hit.ts'
import type { GraphEdge, GraphNode } from '../src/graph/model.ts'
import { edgeCurve, halfExtentX, pointOnCurve } from '../src/graph/render.ts'
import { radiusOf } from '../src/graph/sim.ts'

/** Deterministic PRNG so a failure is always reproducible. */
function mulberry32(seed: number): () => number {
  let a = seed >>> 0
  return () => {
    a = (a + 0x6d2b79f5) >>> 0
    let t = a
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

function makeNodes(count: number, rand: () => number): GraphNode[] {
  const nodes: GraphNode[] = []
  for (let i = 0; i < count; i++) {
    const roll = rand()
    const type: GraphNode['type'] =
      roll < 0.55 ? 'fact' : roll < 0.72 ? 'step' : roll < 0.87 ? 'intent' : roll < 0.95 ? 'goal' : 'project'
    const w = type === 'step' ? 8 + rand() * 26 : 20 + rand() * 90
    const h = type === 'step' || type === 'fact' ? 8 + rand() * 4 : 12 + rand() * 6
    nodes.push({
      id: `n:${i}`,
      type,
      label: `node ${i}`,
      x: (rand() - 0.5) * 1200,
      y: (rand() - 0.5) * 1200,
      vx: 0,
      vy: 0,
      degree: Math.floor(rand() * 8),
      w,
      h,
      depth: Math.floor(rand() * 6),
    })
  }
  return nodes
}

const EDGE_KINDS: GraphEdge['kind'][] = [...HOVERABLE_EDGE_KINDS, 'host', 'goal']

function makeEdges(nodes: GraphNode[], count: number, rand: () => number): GraphEdge[] {
  const edges: GraphEdge[] = []
  for (let i = 0; i < count; i++) {
    const a = nodes[Math.floor(rand() * nodes.length)]
    let b = nodes[Math.floor(rand() * nodes.length)]
    if (b === a) b = nodes[(nodes.indexOf(a) + 1) % nodes.length]
    const kind = EDGE_KINDS[Math.floor(rand() * EDGE_KINDS.length)]
    edges.push({ id: `e:${i}`, source: a.id, target: b.id, kind })
  }
  return edges
}

/** Reference node hit test: the exact linear scan the canvas used before. */
function bruteNode(
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
    const dot = n.type === 'fact'
    const halfW = (dot ? radiusOf(n) : halfExtentX(n, layered)) + pad
    const halfH = (dot ? radiusOf(n) : n.h) + pad
    const dx = x - n.x
    const dy = y - n.y
    if (Math.abs(dx) > halfW || Math.abs(dy) > halfH) continue
    const d = Math.hypot(dx / Math.max(halfW, 0.001), dy / Math.max(halfH, 0.001))
    if (d < bestD) {
      best = n
      bestD = d
    }
  }
  return best
}

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

/** Reference edge hit test: the exact linear scan the canvas used before. */
function bruteEdge(
  nodes: readonly GraphNode[],
  edges: readonly GraphEdge[],
  x: number,
  y: number,
  tol: number,
  layered: boolean,
): GraphEdge | null {
  const byId = new Map(nodes.map((n) => [n.id, n]))
  let best: GraphEdge | null = null
  let bestD = Infinity
  for (const e of edges) {
    if (!HOVERABLE_EDGE_KINDS.has(e.kind)) continue
    const s = byId.get(e.source)
    const t = byId.get(e.target)
    if (!s || !t) continue
    const curve = edgeCurve(s, t, layered, e.kind)
    let prev = pointOnCurve(curve, 0)
    for (let i = 1; i <= 16; i++) {
      const at = pointOnCurve(curve, i / 16)
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

function sameNode(got: GraphNode | null, want: GraphNode | null): boolean {
  return (got?.id ?? null) === (want?.id ?? null)
}

function sameEdge(got: GraphEdge | null, want: GraphEdge | null): boolean {
  return (got?.id ?? null) === (want?.id ?? null)
}

test('indexed hover matches the linear scan at many pointer positions', () => {
  const rand = mulberry32(0x5eed)
  const nodes = makeNodes(300, rand)
  const edges = makeEdges(nodes, 500, rand)
  const byId = new Map(nodes.map((n) => [n.id, n]))

  for (const layered of [false, true]) {
    const index = new GraphIndex(nodes, edges, { layered })
    let checked = 0

    // 1) Uniform random pointers, as a real hover stream is.
    for (let i = 0; i < 200; i++) {
      const x = (rand() - 0.5) * 1400
      const y = (rand() - 0.5) * 1400
      for (const pad of [0, 3, 6, 14, 40]) {
        assert.ok(
          sameNode(index.nodeAt(x, y, pad), bruteNode(nodes, x, y, pad, layered)),
          `node mismatch at ${x},${y} pad=${pad} layered=${layered}`,
        )
        checked++
      }
      for (const tol of [2, 6, 12, 30]) {
        assert.ok(
          sameEdge(index.edgeAt(x, y, tol), bruteEdge(nodes, edges, x, y, tol, layered)),
          `edge mismatch at ${x},${y} tol=${tol} layered=${layered}`,
        )
        checked++
      }
    }

    // 2) Pointers right on/near nodes: the hot path, and the one where a
    //    candidate bucket is easiest to miss.
    for (const n of nodes) {
      if (rand() > 0.4) continue
      const x = n.x + (rand() - 0.5) * (Math.max(radiusOf(n), n.w) * 2 + 40)
      const y = n.y + (rand() - 0.5) * (Math.max(radiusOf(n), n.h) * 2 + 40)
      for (const pad of [0, 6, 14]) {
        assert.ok(
          sameNode(index.nodeAt(x, y, pad), bruteNode(nodes, x, y, pad, layered)),
          `near-node mismatch at ${x},${y} pad=${pad}`,
        )
        checked++
      }
    }

    // 3) Points exactly on a hoverable curve and just off it.
    for (const e of edges) {
      if (!HOVERABLE_EDGE_KINDS.has(e.kind)) continue
      if (rand() > 0.3) continue
      const s = byId.get(e.source)!
      const t = byId.get(e.target)!
      const curve = edgeCurve(s, t, layered, e.kind)
      for (let k = 0; k < 3; k++) {
        const at = pointOnCurve(curve, rand())
        for (const off of [0, 4, -6]) {
          const x = at.x + off
          const y = at.y + (rand() - 0.5) * 10
          assert.ok(
            sameEdge(index.edgeAt(x, y, 12), bruteEdge(nodes, edges, x, y, 12, layered)),
            `on-curve mismatch at ${x},${y} layered=${layered}`,
          )
          checked++
        }
      }
    }

    assert.ok(checked > 500, 'the comparison covered a meaningful sample')
  }
})

test('the index tests a small fraction of the graph, not the whole graph', () => {
  const rand = mulberry32(0xbeef)
  const nodes = makeNodes(300, rand)
  const edges = makeEdges(nodes, 500, rand)
  const index = new GraphIndex(nodes, edges, { layered: false })

  const nodeQueries = 300
  for (let i = 0; i < nodeQueries; i++) {
    index.nodeAt((rand() - 0.5) * 1400, (rand() - 0.5) * 1400, 6)
  }
  const avgNodes = index.stats.nodeCandidates / nodeQueries
  assert.ok(
    avgNodes < nodes.length * 0.25,
    `node hover tested ${avgNodes} candidates of ${nodes.length}; expected far fewer`,
  )

  const edgeQueries = 300
  for (let i = 0; i < edgeQueries; i++) {
    index.edgeAt((rand() - 0.5) * 1400, (rand() - 0.5) * 1400, 12)
  }
  const avgEdges = index.stats.edgeCandidates / edgeQueries
  assert.ok(
    avgEdges < edges.length * 0.5,
    `edge hover tested ${avgEdges} candidates of ${edges.length}; expected far fewer`,
  )
  assert.ok(index.stats.queries >= nodeQueries + edgeQueries)
})

test('a realistic local graph tests almost no edge candidates per hover', () => {
  const rand = mulberry32(0x1234)
  const nodes = makeNodes(400, rand)
  // Real ledgers connect nearby nodes (produced facts, next steps), not random
  // long chords: pick the nearest of a few candidates.
  const edges: GraphEdge[] = []
  const kinds: GraphEdge['kind'][] = ['intent', 'premise', 'reference', 'pin', 'spawn']
  for (let i = 0; i < 600; i++) {
    const a = nodes[Math.floor(rand() * nodes.length)]
    let best: GraphNode | null = null
    let bestD = Infinity
    for (let k = 0; k < 6; k++) {
      const b = nodes[Math.floor(rand() * nodes.length)]
      if (b === a) continue
      const d = Math.hypot(b.x - a.x, b.y - a.y)
      if (d < bestD) {
        bestD = d
        best = b
      }
    }
    if (!best) continue
    edges.push({ id: `e:${i}`, source: a.id, target: best.id, kind: kinds[i % kinds.length] })
  }

  const index = new GraphIndex(nodes, edges, { layered: false })
  const queries = 300
  for (let i = 0; i < queries; i++) {
    index.edgeAt((rand() - 0.5) * 1400, (rand() - 0.5) * 1400, 12)
  }
  const avg = index.stats.edgeCandidates / queries
  assert.ok(
    avg < edges.length * 0.08,
    `local edge hover tested ${avg} candidates of ${edges.length}; expected very few`,
  )
})

test('results and candidate counts are deterministic', () => {
  const rand = mulberry32(42)
  const nodes = makeNodes(120, rand)
  const edges = makeEdges(nodes, 160, rand)
  const a = new GraphIndex(nodes, edges, { layered: true })
  const b = new GraphIndex(nodes, edges, { layered: true })
  const points: Array<[number, number]> = []
  for (let i = 0; i < 80; i++) points.push([(rand() - 0.5) * 800, (rand() - 0.5) * 800])
  for (const [x, y] of points) {
    assert.equal(a.nodeAt(x, y, 6)?.id ?? null, b.nodeAt(x, y, 6)?.id ?? null)
    assert.equal(a.edgeAt(x, y, 12)?.id ?? null, b.edgeAt(x, y, 12)?.id ?? null)
  }
  assert.deepEqual(a.stats, b.stats)
})

test('edge filtering, ghosts and missing endpoints behave like the canvas', () => {
  const nodes: GraphNode[] = [
    { id: 'f:a', type: 'fact', label: 'a', x: 0, y: 0, vx: 0, vy: 0, degree: 1, w: 8, h: 8, depth: 1 },
    { id: 'f:b', type: 'fact', label: 'b', x: 300, y: 0, vx: 0, vy: 0, degree: 1, w: 8, h: 8, depth: 1 },
    // Ghosts stand for nothing pointable; the step node is the target.
    { id: 'w:x', type: 'ghost', label: 'ghost', x: 0, y: 300, vx: 0, vy: 0, degree: 1, w: 8, h: 8, depth: 2 },
  ]
  const edges: GraphEdge[] = [
    { id: 'e:host', source: 'f:a', target: 'f:b', kind: 'host' },
    { id: 'e:intent', source: 'f:a', target: 'f:b', kind: 'intent' },
    { id: 'e:missing', source: 'f:a', target: 'f:missing', kind: 'premise' },
  ]
  const index = new GraphIndex(nodes, edges, { layered: false })

  const mid = pointOnCurve(edgeCurve(nodes[0], nodes[1], false, 'intent'), 0.5)
  // The host edge shares the same endpoints but must never be hovered; the
  // intent edge is the one relation that is.
  assert.equal(index.edgeAt(mid.x, mid.y, 12)?.id, 'e:intent')
  // Ghost centre is empty canvas: the ghost is not a hit target.
  assert.equal(index.nodeAt(0, 300, 6), null)
  // Exact node centres resolve to that node.
  assert.equal(index.nodeAt(0, 0, 6)?.id, 'f:a')
  assert.equal(index.nodeAt(300, 0, 6)?.id, 'f:b')
  // Far outside everything.
  assert.equal(index.nodeAt(5000, 5000, 6), null)
  assert.equal(index.edgeAt(5000, 5000, 12), null)
})

test('an empty graph is not a crash', () => {
  const index = new GraphIndex([], [], { layered: false })
  assert.equal(index.nodeAt(0, 0, 6), null)
  assert.equal(index.edgeAt(0, 0, 12), null)
  assert.deepEqual(index.stats, { nodeCandidates: 0, edgeCandidates: 0, queries: 2 })
})
