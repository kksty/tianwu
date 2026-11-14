// Contract rules for the force simulation (docs/33 §4.4).
//
// Two things are pinned here:
//   * warmWithin — the budgeted synchronous warm. A fixed tick count used to
//     block the main thread for hundreds of milliseconds before the first paint;
//     the warm now stops at a wall-clock budget (injectable clock, no waiting),
//     a tick cap, or the settled alpha, whichever comes first.
//   * the Barnes–Hut tick — above 600 nodes the pairwise repulsion is summed
//     against a quadtree. The approximation must stay a *force* approximation:
//     same direction, close magnitude, deterministic, no NaN, and the short-range
//     collision separation must survive or wide pills start overlapping again.
//
// No absolute-time assertions: the cost rule is "visits grow sub-quadratically",
// and the accuracy rule is "close to the pairwise sum", both measured on a fixed
// deterministic input.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  BH_MIN_NODES,
  ForceSim,
  SETTLE_ALPHA,
  WARM_BUDGET_MS,
  warmWithin,
} from '../src/graph/sim.ts'
import {
  COLLIDE_PAD,
  COLLIDE_STRENGTH,
  REPULSION_K,
  buildQuadtree,
  collide,
  collideRadius,
  repulse,
  type QuadNode,
  type Quadtree,
} from '../src/graph/quadtree.ts'
import { buildGraph, stepID } from '../src/graph/model.ts'
import type { GraphEdge, GraphModel, GraphNode } from '../src/graph/model.ts'
import type { Fact, Intent } from '../src/lib/types.ts'

// A deterministic scatter: tests must not depend on Math.random's output, and a
// failing assertion should reproduce byte for byte.
function rng(seed: number): () => number {
  let s = seed >>> 0
  return () => {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0
    return s / 4294967296
  }
}

function makeNode(id: string, x: number, y: number, over: Partial<GraphNode> = {}): GraphNode {
  return { id, type: 'fact', label: id, x, y, vx: 0, vy: 0, degree: 2, w: 6, h: 6, depth: 1, ...over }
}

/** `count` nodes in a square of `spread`; every `dupEvery`-th sits on its predecessor. */
function scatter(count: number, spread = 2400, seed = 123456789, dupEvery = 0): GraphNode[] {
  const r = rng(seed)
  const nodes: GraphNode[] = []
  for (let i = 0; i < count; i++) {
    const duplicate = dupEvery > 0 && i > 0 && i % dupEvery === 0
    const prev = nodes[i - 1]
    nodes.push(
      makeNode(`f:${i}`, duplicate ? prev.x : (r() - 0.5) * spread, duplicate ? prev.y : (r() - 0.5) * spread),
    )
  }
  return nodes
}

function makeModel(nodes: GraphNode[], edges: GraphEdge[] = []): GraphModel {
  return { nodes, edges, byId: new Map(nodes.map((n) => [n.id, n])), maxDepth: 1 }
}

/** The pairwise 1/d² repulsion the tree path approximates, for comparison. */
function bruteRepulse(nodes: GraphNode[], alpha = 1): void {
  for (let i = 0; i < nodes.length; i++) {
    const a = nodes[i]
    for (let j = i + 1; j < nodes.length; j++) {
      const b = nodes[j]
      const dx = b.x - a.x
      const dy = b.y - a.y
      const d2 = dx * dx + dy * dy
      if (d2 < 1e-12) continue
      const d = Math.sqrt(d2)
      const f = (REPULSION_K * alpha) / d2
      a.vx -= (dx / d) * f
      a.vy -= (dy / d) * f
      b.vx += (dx / d) * f
      b.vy += (dy / d) * f
    }
  }
}

/** The pairwise collision push the tree path must reproduce exactly. */
function bruteCollide(nodes: GraphNode[], alpha = 1): void {
  for (let i = 0; i < nodes.length; i++) {
    const a = nodes[i]
    for (let j = i + 1; j < nodes.length; j++) {
      const b = nodes[j]
      const dx = b.x - a.x
      const dy = b.y - a.y
      const d = Math.sqrt(dx * dx + dy * dy)
      const minD = collideRadius(a) + collideRadius(b) + COLLIDE_PAD
      if (d >= minD) continue
      const f = (minD - d) * COLLIDE_STRENGTH * alpha
      a.vx -= (dx / d) * f
      a.vy -= (dy / d) * f
      b.vx += (dx / d) * f
      b.vy += (dy / d) * f
    }
  }
}

function velocities(nodes: GraphNode[]): number[] {
  return nodes.flatMap((n) => [n.vx, n.vy])
}

function fakeClock(stepMs: number, start = 1000): () => number {
  let at = start
  return () => {
    const now = at
    at += stepMs
    return now
  }
}

/** Walk every cell: mass adds up, centre of mass is the weighted mean. */
function checkCell(tree: Quadtree, q: QuadNode): void {
  if (q.leaf) {
    assert.equal(q.mass, q.body!.length, 'leaf mass is its body count')
    if (q.mass === 0) return
    let sx = 0
    let sy = 0
    let maxRadius = 0
    for (const idx of q.body!) {
      const n = tree.nodes[idx]
      sx += n.x
      sy += n.y
      maxRadius = Math.max(maxRadius, collideRadius(n))
      assert.ok(n.x >= q.x0 && n.x <= q.x0 + q.size, 'body x is inside its cell')
      assert.ok(n.y >= q.y0 && n.y <= q.y0 + q.size, 'body y is inside its cell')
    }
    assert.ok(Math.abs(q.cx - sx / q.mass) < 1e-6, 'leaf centre of mass is the mean')
    assert.ok(Math.abs(q.cy - sy / q.mass) < 1e-6, 'leaf centre of mass is the mean')
    assert.ok(Math.abs(q.maxRadius - maxRadius) < 1e-6, 'leaf keeps the widest footprint')
    return
  }
  assert.ok(Math.abs(q.size / 2 - q.children![0].size) < 1e-9, 'children are half the cell')
  let mass = 0
  let sx = 0
  let sy = 0
  let maxRadius = 0
  for (const child of q.children!) {
    checkCell(tree, child)
    mass += child.mass
    sx += child.cx * child.mass
    sy += child.cy * child.mass
    maxRadius = Math.max(maxRadius, child.maxRadius)
  }
  assert.equal(q.mass, mass, 'cell mass is the sum of its children')
  if (mass > 0) {
    assert.ok(Math.abs(q.cx - sx / mass) < 1e-6, 'cell centre of mass is the weighted mean')
    assert.ok(Math.abs(q.cy - sy / mass) < 1e-6, 'cell centre of mass is the weighted mean')
  }
  assert.ok(Math.abs(q.maxRadius - maxRadius) < 1e-6, 'cell keeps the widest footprint')
}

// ---------------------------------------------------------------------------
// quadtree.ts
// ---------------------------------------------------------------------------

test('the quadtree covers every body, with the true centre of mass', () => {
  const nodes = scatter(500)
  const tree = buildQuadtree(nodes)
  assert.equal(tree.root.mass, nodes.length)
  for (const n of nodes) {
    assert.ok(n.x >= tree.root.x0 && n.x <= tree.root.x0 + tree.root.size)
    assert.ok(n.y >= tree.root.y0 && n.y <= tree.root.y0 + tree.root.size)
  }
  const meanX = nodes.reduce((sum, n) => sum + n.x, 0) / nodes.length
  const meanY = nodes.reduce((sum, n) => sum + n.y, 0) / nodes.length
  assert.ok(Math.abs(tree.root.cx - meanX) < 1e-6)
  assert.ok(Math.abs(tree.root.cy - meanY) < 1e-6)
  assert.equal(tree.root.maxRadius, Math.max(...nodes.map(collideRadius)))
  checkCell(tree, tree.root)
})

test('a radius query returns exactly what a linear scan returns', () => {
  const nodes = scatter(800, 2400, 99)
  const tree = buildQuadtree(nodes)
  const r = rng(424242)
  for (let q = 0; q < 40; q++) {
    const x = (r() - 0.5) * 2800
    const y = (r() - 0.5) * 2800
    const radius = r() * 500
    const expected: number[] = []
    for (let i = 0; i < nodes.length; i++) {
      const dx = nodes[i].x - x
      const dy = nodes[i].y - y
      if (dx * dx + dy * dy <= radius * radius) expected.push(i)
    }
    assert.deepEqual(tree.queryRadius(x, y, radius), expected, `query ${q}`)
  }
  // A point sits on its own body: a zero radius still finds it.
  const first = nodes[0]
  assert.ok(tree.queryRadius(first.x, first.y, 0).includes(0))
  assert.deepEqual(tree.queryRadius(1e9, 1e9, 10), [])
})

test('with a zero opening angle the tree repulsion is the exact pairwise sum', () => {
  const nodes = scatter(250, 2000, 5)
  const exact = nodes.map((n) => ({ ...n }))
  bruteRepulse(exact)
  const tree = buildQuadtree(nodes, { theta: 0 })
  repulse(nodes, tree, 1)
  let worst = 0
  for (let i = 0; i < nodes.length; i++) {
    const magnitude = Math.hypot(exact[i].vx, exact[i].vy)
    assert.ok(magnitude > 0)
    worst = Math.max(
      worst,
      Math.hypot(nodes[i].vx - exact[i].vx, nodes[i].vy - exact[i].vy) / magnitude,
    )
  }
  assert.ok(worst < 1e-9, `theta=0 must not approximate (worst error ${worst})`)
})

test('Barnes–Hut repulsion tracks the exact sum closely', () => {
  const nodes = scatter(400, 2400, 123456789)
  const exact = nodes.map((n) => ({ ...n }))
  bruteRepulse(exact)
  const tree = buildQuadtree(nodes, { theta: 0.7 })
  repulse(nodes, tree, 1)

  const errors: number[] = []
  let minDot = 1
  for (let i = 0; i < nodes.length; i++) {
    const exactMagnitude = Math.hypot(exact[i].vx, exact[i].vy)
    const approxMagnitude = Math.hypot(nodes[i].vx, nodes[i].vy)
    assert.ok(Number.isFinite(nodes[i].vx) && Number.isFinite(nodes[i].vy), 'no NaN')
    if (exactMagnitude < 1e-6) continue
    errors.push(
      Math.hypot(nodes[i].vx - exact[i].vx, nodes[i].vy - exact[i].vy) / exactMagnitude,
    )
    if (approxMagnitude > 1e-9) {
      minDot = Math.min(
        minDot,
        (nodes[i].vx * exact[i].vx + nodes[i].vy * exact[i].vy) /
          (approxMagnitude * exactMagnitude),
      )
    }
  }
  errors.sort((a, b) => a - b)
  const at = (q: number) => errors[Math.min(errors.length - 1, Math.floor(q * errors.length))]
  assert.ok(errors.length > 0)
  assert.ok(at(0.5) < 0.1, `median error ${at(0.5)}`)
  assert.ok(at(0.9) < 0.25, `p90 error ${at(0.9)}`)
  assert.ok(minDot > 0.7, `force direction stays aligned (min dot ${minDot})`)
})

test('the tree path is deterministic, even for coincident bodies', () => {
  const nodes = scatter(64, 800, 7, 5) // duplicates use a deterministic nudge
  const run = () => {
    const copy = nodes.map((n) => ({ ...n }))
    const tree = buildQuadtree(copy, { theta: 0.7 })
    repulse(copy, tree, 1)
    collide(copy, tree, 1)
    return velocities(copy)
  }
  const first = run()
  const second = run()
  assert.deepEqual(second, first, 'same input, same output')
  assert.ok(first.every(Number.isFinite), 'coincident bodies do not produce NaN')
})

test('tree collision equals the pairwise collision', () => {
  // Small spread, wide pills: many overlapping pairs for the exact comparison.
  const r = rng(2024)
  const nodes: GraphNode[] = []
  for (let i = 0; i < 300; i++) {
    const wide = i % 9 === 0
    nodes.push(
      makeNode(
        `f:${i}`,
        (r() - 0.5) * 500,
        (r() - 0.5) * 500,
        wide ? { type: 'intent', w: 150, h: 16 } : {},
      ),
    )
  }
  const exact = nodes.map((n) => ({ ...n }))
  bruteCollide(exact)
  const tree = buildQuadtree(nodes, { theta: 0.7 })
  collide(nodes, tree, 1)
  for (let i = 0; i < nodes.length; i++) {
    assert.ok(
      Math.hypot(nodes[i].vx - exact[i].vx, nodes[i].vy - exact[i].vy) < 1e-6,
      `node ${i} collision matches the pairwise pass`,
    )
  }
})

test('cell visits grow sub-quadratically from 1000 to 4000 nodes', () => {
  const measure = (count: number) => {
    const nodes = scatter(count, 2400, 31)
    const tree = buildQuadtree(nodes, { theta: 0.7 })
    tree.resetVisits()
    repulse(nodes, tree, 1)
    const repulseVisits = tree.visits
    tree.resetVisits()
    collide(nodes, tree, 1)
    return { repulseVisits, collideVisits: tree.visits }
  }
  const small = measure(1000)
  const large = measure(4000)
  assert.ok(small.repulseVisits > 0 && large.repulseVisits > small.repulseVisits)
  assert.ok(small.collideVisits > 0 && large.collideVisits > small.collideVisits)
  // 4× the nodes: a pairwise pass costs 16×. Barnes–Hut must stay well under it.
  assert.ok(
    large.repulseVisits < small.repulseVisits * 8,
    `repulse visits ${large.repulseVisits} vs ${small.repulseVisits}`,
  )
  assert.ok(
    large.collideVisits < small.collideVisits * 8,
    `collide visits ${large.collideVisits} vs ${small.collideVisits}`,
  )
  // And absolutely: fewer visits than a quarter of the 4000-node pair count.
  assert.ok(large.repulseVisits < (4000 * 3999) / 2 / 4)
})

// ---------------------------------------------------------------------------
// ForceSim: strategy threshold, BH forces, collision survival, layered untouched
// ---------------------------------------------------------------------------

test('the force tick switches to Barnes–Hut at 600 nodes', () => {
  assert.equal(BH_MIN_NODES, 600)
  const below = new ForceSim(makeModel(scatter(599, 1200, 3)))
  assert.equal(below.tickStrategy, 'exact', '599 nodes keep the exact pairwise tick')
  const atThreshold = new ForceSim(makeModel(scatter(600, 1200, 3)))
  assert.equal(atThreshold.tickStrategy, 'barnes-hut', '600 nodes use the tree')
})

test('the layered tree layout is untouched by the Barnes–Hut path', () => {
  const nodes: GraphNode[] = [
    makeNode('p:1', 0, 0, { type: 'project', label: 'p', w: 86, h: 20, depth: 0 }),
  ]
  for (let i = 0; i < 599; i++) nodes.push(makeNode(`f:${i}`, 0, 0, { label: 'fact' }))
  const edges: GraphEdge[] = nodes
    .slice(1)
    .map((n, i) => ({ id: `e${i}`, source: 'p:1', target: n.id, kind: 'brief' as const }))
  const sim = new ForceSim(makeModel(nodes, edges), { mode: 'layered' })
  assert.equal(sim.tickStrategy, 'exact', 'the chain tree never repels')
  const bands = sim.depthBands.map((b) => ({ ...b }))
  assert.ok(bands.length >= 2)
  const before = nodes.map((n) => ({ x: n.x, y: n.y }))
  sim.tick()
  const moved = nodes.filter((n, i) => n.x !== before[i].x || n.y !== before[i].y)
  assert.equal(moved.length, 0, 'a settled tree tick is a no-op')
  assert.deepEqual(
    sim.depthBands.map((b) => ({ ...b })),
    bands,
    'depth bands stay where the tree put them',
  )
})

test('a Barnes–Hut tick pushes two clusters apart', () => {
  // Two small clusters at x=±50, with 596 far bodies (radius 1e6) so the graph
  // is past the 600-node threshold while the only force that matters is the one
  // between the clusters. Pairs inside a cluster are 60 apart — outside their
  // footprint — so the measured velocity is the repulsion, not the cap.
  const nodes: GraphNode[] = []
  for (let i = 0; i < 596; i++) {
    nodes.push(makeNode(`far:${i}`, Math.cos(i) * 1e6, Math.sin(i) * 1e6, { degree: 0 }))
  }
  nodes.push(
    makeNode('la', -50, -30),
    makeNode('lb', -50, 30),
    makeNode('ra', 50, -30),
    makeNode('rb', 50, 30),
  )
  const sim = new ForceSim(makeModel(nodes))
  assert.equal(sim.tickStrategy, 'barnes-hut')
  const cluster = (prefix: string) => nodes.filter((n) => n.id.startsWith(prefix))
  const meanX = (prefix: string) => {
    const list = cluster(prefix)
    return list.reduce((sum, n) => sum + n.x, 0) / list.length
  }
  const meanVx = (prefix: string) => {
    const list = cluster(prefix)
    return list.reduce((sum, n) => sum + n.vx, 0) / list.length
  }
  const leftBefore = meanX('la')
  const rightBefore = meanX('ra')
  assert.ok(leftBefore < 0 && rightBefore > 0)
  sim.tick()
  assert.ok(meanVx('la') < -1 && meanVx('lb') < -1, 'the left cluster is pushed left')
  assert.ok(meanVx('ra') > 1 && meanVx('rb') > 1, 'the right cluster is pushed right')
  assert.ok(meanX('la') < leftBefore, 'the left cluster actually moved left')
  assert.ok(meanX('ra') > rightBefore, 'the right cluster actually moved right')
  assert.ok(nodes.every((n) => Number.isFinite(n.x) && Number.isFinite(n.y)))
})

test('the collision separation survives the Barnes–Hut path', () => {
  // Wide pills 297 units apart: their footprints need 138+138+26 = 302. The
  // 1/d² repulsion at that distance is ~0.16, the overlap push is ~5.5 — so a
  // velocity of a few units after one tick proves the collision term ran.
  const nodes: GraphNode[] = []
  for (let i = 0; i < 598; i++) {
    nodes.push(makeNode(`far:${i}`, Math.cos(i) * 1e6, Math.sin(i) * 1e6, { degree: 0 }))
  }
  const a = makeNode('intent:a', -148.5, 0, { type: 'intent', w: 150, h: 16 })
  const b = makeNode('intent:b', 148.5, 0, { type: 'intent', w: 150, h: 16 })
  nodes.push(a, b)
  const distance = () => Math.hypot(b.x - a.x, b.y - a.y)
  assert.ok(distance() < 300, 'the pills start overlapping')

  const sim = new ForceSim(makeModel(nodes))
  assert.equal(sim.tickStrategy, 'barnes-hut')
  sim.tick()
  assert.ok(a.vx < -2, `the left pill is pushed left (vx ${a.vx})`)
  assert.ok(b.vx > 2, `the right pill is pushed right (vx ${b.vx})`)
  assert.ok(distance() > 300, 'one tick already opens the gap')

  // Keep the pressure on: the pair settles just outside the required clearance.
  for (let i = 0; i < 20; i++) {
    sim.alpha = Math.max(sim.alpha, 0.85)
    sim.tick()
  }
  assert.ok(distance() > 300, `the gap is held (${distance()})`)
  assert.ok(Number.isFinite(distance()))
})

test('Barnes–Hut ticks stay finite and bounded by the velocity cap', () => {
  const nodes = scatter(600, 2400, 77, 17)
  const sim = new ForceSim(makeModel(nodes))
  assert.equal(sim.tickStrategy, 'barnes-hut')
  let furthestStep = 0
  for (let tick = 0; tick < 10; tick++) {
    const before = nodes.map((n) => ({ x: n.x, y: n.y }))
    sim.tick()
    for (let i = 0; i < nodes.length; i++) {
      assert.ok(Number.isFinite(nodes[i].x) && Number.isFinite(nodes[i].y), 'no NaN')
      furthestStep = Math.max(
        furthestStep,
        Math.hypot(nodes[i].x - before[i].x, nodes[i].y - before[i].y),
      )
    }
  }
  // tick() caps the velocity at 30 world units; nothing may fly further.
  assert.ok(furthestStep <= 30.5, `furthest step ${furthestStep}`)
})

// ---------------------------------------------------------------------------
// warmWithin: budget, tick cap, settled alpha, injectable clock
// ---------------------------------------------------------------------------

test('the warm budget is 24ms and the settle alpha is the running threshold', () => {
  assert.equal(WARM_BUDGET_MS, 24)
  const sim = new ForceSim(makeModel(scatter(4)))
  assert.equal(sim.alpha, 1)
  assert.equal(SETTLE_ALPHA, 0.015)
  assert.equal(sim.running, true)
})

test('a warm stops when the wall-clock budget is spent', () => {
  const sim = new ForceSim(makeModel(scatter(40, 800, 21)))
  // 5ms per tick: after five ticks 25ms have passed, past the 24ms budget.
  const result = warmWithin(sim, { now: fakeClock(5) })
  assert.equal(result.ticks, 5)
  assert.equal(result.spentMs, 25)
  assert.ok(result.spentMs >= WARM_BUDGET_MS, 'the warm actually filled the budget')
})

test('a warm never runs past maxTicks, even with budget left', () => {
  const sim = new ForceSim(makeModel(scatter(40, 800, 22)))
  // The clock advances 1ms per read, so the budget cannot be what stops it.
  const result = warmWithin(sim, { maxTicks: 3, now: fakeClock(1) })
  assert.equal(result.ticks, 3)
  assert.equal(result.spentMs, 3)
})

test('a warm stops at the settled alpha instead of the budget', () => {
  const sim = new ForceSim(makeModel(scatter(20, 600, 23)))
  const result = warmWithin(sim, { now: fakeClock(0) }) // never spends wall time
  assert.ok(result.ticks > 0)
  assert.ok(sim.alpha <= SETTLE_ALPHA, `alpha ${sim.alpha} is settled`)
  assert.ok(sim.running === false)
  // A second warm on a settled simulation does nothing at all.
  assert.deepEqual(warmWithin(sim, { now: fakeClock(0) }), { ticks: 0, spentMs: 0 })
})

test('a warm honours a custom minAlpha', () => {
  const sim = new ForceSim(makeModel(scatter(8)))
  sim.alpha = 0.5
  assert.deepEqual(warmWithin(sim, { minAlpha: 0.6, now: fakeClock(0) }), { ticks: 0, spentMs: 0 })
  sim.alpha = 1
  assert.deepEqual(warmWithin(sim, { minAlpha: 1, now: fakeClock(0) }), { ticks: 0, spentMs: 0 })
})

test('a zero budget or a zero tick cap warms nothing', () => {
  const sim = new ForceSim(makeModel(scatter(8)))
  assert.deepEqual(warmWithin(sim, { budgetMs: 0, now: fakeClock(5) }), { ticks: 0, spentMs: 0 })
  assert.deepEqual(warmWithin(sim, { maxTicks: 0, now: fakeClock(5) }), { ticks: 0, spentMs: 0 })
  assert.equal(sim.alpha, 1, 'nothing moved')
})

test('a warm reports the wall time it actually spent', () => {
  const sim = new ForceSim(makeModel(scatter(12)))
  // 10ms per tick: two ticks fit inside 24ms, the third crosses it.
  const result = warmWithin(sim, { now: fakeClock(10) })
  assert.equal(result.ticks, 3)
  assert.equal(result.spentMs, 30)
})

test('a warm on a layered simulation only relaxes the tree springs', () => {
  const nodes: GraphNode[] = [
    makeNode('p:1', 0, 0, { type: 'project', label: 'p', w: 86, h: 20, depth: 0 }),
    makeNode('f:1', 0, 0, { label: 'fact' }),
  ]
  const sim = new ForceSim(makeModel(nodes), { mode: 'layered' })
  const before = nodes.map((n) => ({ x: n.x, y: n.y }))
  // The canvas pins layered layouts to alpha=0; warmWithin must accept that.
  sim.alpha = 0
  assert.deepEqual(warmWithin(sim, { now: fakeClock(0) }), { ticks: 0, spentMs: 0 })
  const result = warmWithin(sim, { minAlpha: -1, maxTicks: 2, now: fakeClock(0) })
  assert.equal(result.ticks, 2)
  assert.ok(nodes.every((n) => Number.isFinite(n.x) && Number.isFinite(n.y)))
  assert.deepEqual(
    nodes.map((n, i) => (n.x === before[i].x && n.y === before[i].y ? 'same' : 'moved')),
    ['same', 'same'],
    'no repulsion in layered mode: settled anchors mean no motion',
  )
})

// ---------------------------------------------------------------------------
// Chain-tree placement: the hop a step is drawn on, and manually dropped nodes
// ---------------------------------------------------------------------------

function chainFact(id: string, intentID: string, created: string): Fact {
  return {
    id,
    intent_id: intentID,
    kind: 'host',
    title: id,
    summary: '',
    confidence: 'observed',
    status: 'verified',
    claim_version: 1,
    created_at: created,
  }
}

function chainIntent(id: string, created: string, from: string[] = []): Intent {
  return {
    id,
    title: id,
    description: '',
    status: 'done',
    attempts: 1,
    created_at: created,
    updated_at: created,
    from_fact_ids: from,
  }
}

/**
 * in_probe(1) → f_inject/f_swagger(2) → in_dig(3) → f_cred(4) → in_waits(5).
 * `in_waits` lists the shallow premise first, exactly as the ledger does; its
 * hop is still the last premise's hop + 1, not the first one's.
 */
function chainModel(): GraphModel {
  return buildGraph(
    [
      chainFact('f_inject', 'in_probe', '2026-11-11T00:00:01Z'),
      chainFact('f_swagger', 'in_probe', '2026-11-11T00:00:02Z'),
      chainFact('f_cred', 'in_dig', '2026-11-11T00:00:04Z'),
    ],
    [
      chainIntent('in_probe', '2026-11-11T00:00:01Z'),
      chainIntent('in_dig', '2026-11-11T00:00:02Z', ['f_inject']),
      chainIntent('in_waits', '2026-11-11T00:00:03Z', ['f_inject', 'f_cred']),
    ],
    [],
  )
}

test('a step is drawn after its deepest premise, not its first one', () => {
  const graph = chainModel()
  assert.equal(graph.byId.get(stepID('in_dig'))?.depth, 3)
  assert.equal(graph.byId.get('f:f_cred')?.depth, 4)
  assert.equal(graph.byId.get(stepID('in_waits'))?.depth, 5, 'the model already called this hop 5')

  const sim = new ForceSim(graph, { mode: 'layered' })
  const columnOf = (id: string): number | undefined => {
    const node = graph.byId.get(id)
    if (!node) return undefined
    return sim.depthBands.find((band) => node.x >= band.x1 && node.x <= band.x2)?.depth
  }
  assert.equal(columnOf('f:f_inject'), 2)
  assert.equal(columnOf('f:f_cred'), 4)
  // The first premise (hop 2) used to own the step's column, drawing the
  // direction at 第 3 跳 beside work it was still waiting for.
  assert.equal(columnOf(stepID('in_waits')), 5)
  assert.equal(columnOf(stepID('in_waits')), graph.byId.get(stepID('in_waits'))?.depth)
})

test('a dropped node holds its place until the explicit re-layout', () => {
  const graph = chainModel()
  const sim = new ForceSim(graph, { mode: 'layered' })
  const first = graph.byId.get(stepID('in_dig'))!
  const second = graph.byId.get(stepID('in_waits'))!
  const firstHome = { x: first.x, y: first.y }
  const secondHome = { x: second.x, y: second.y }
  const firstDrop = { x: first.x + 40, y: first.y - 90 }
  const secondDrop = { x: second.x, y: second.y + 120 }

  // The operator drops `first`, then drags `second`. The old anchors sprang the
  // first node back while the second one moved.
  sim.holdAt(first.id, firstDrop.x, firstDrop.y)
  sim.holdAt(second.id, secondDrop.x, secondDrop.y)
  sim.reheat(1)
  for (let i = 0; i < 120; i++) sim.tick()
  assert.deepEqual({ x: first.x, y: first.y }, firstDrop, 'the first drop is not reset')
  assert.deepEqual({ x: second.x, y: second.y }, secondDrop, 'the second drop sticks')

  // 重新布局 is the one action that discards the drops. It rebuilds the slots
  // and lets the springs carry the nodes home.
  sim.relayout()
  for (let i = 0; i < 400; i++) sim.tick()
  assert.ok(Math.abs(first.x - firstHome.x) < 0.1 && Math.abs(first.y - firstHome.y) < 0.1)
  assert.ok(Math.abs(second.x - secondHome.x) < 0.1 && Math.abs(second.y - secondHome.y) < 0.1)
  const bands = sim.depthBands.map((band) => ({ ...band }))
  sim.tick()
  assert.deepEqual(
    sim.depthBands.map((band) => ({ ...band })),
    bands,
    're-layout rebuilt the columns once, not appended duplicates',
  )
})
