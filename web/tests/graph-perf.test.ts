// Render/interaction cost rules for the console graph. Both regressions pinned
// here froze the map on a large engagement: every ledger poll re-ran the opening
// layout warm (hundreds of O(n²) force ticks) although the operator was looking
// at a settled picture, and every pointermove ran a full node/edge hit test even
// though the canvas can only paint one hover per frame.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  ForceSim,
  REBUILD_WARM_MAX,
  WARM_BUDGET_MS,
  createMoveCoalescer,
  initialWarmTicks,
  warmTicks,
  warmWithin,
  type LayoutMode,
  type SimSnapshot,
} from '../src/graph/sim.ts'
import type { GraphModel, GraphNode } from '../src/graph/model.ts'
import { createFramePublisher } from '../src/graph/view-store.ts'

function fact(id: string): GraphNode {
  return { id, type: 'fact', label: id, x: 0, y: 0, vx: 0, vy: 0, degree: 0, w: 6, h: 6, depth: 1 }
}

function model(ids: string[]): GraphModel {
  const nodes = ids.map(fact)
  return { nodes, edges: [], byId: new Map(nodes.map((n) => [n.id, n])), maxDepth: 1 }
}

function ids(count: number, prefix = 'f'): string[] {
  return Array.from({ length: count }, (_, i) => `${prefix}:${i}`)
}

/** The previous simulation, reduced to what the rebuild decision reads. */
function snapshot(mode: LayoutMode, nodeIDs: string[]): SimSnapshot {
  return { mode, nodes: nodeIDs.map((id) => ({ id })) }
}

test('opening a project warms the layout before the first paint', () => {
  const graph = model(ids(160))
  for (const layout of ['force', 'layered'] as const) {
    const warm = warmTicks(null, graph, layout)
    assert.ok(warm > 0, `${layout} opening needs settling ticks`)
    assert.equal(warm, initialWarmTicks(graph.nodes.length))
    assert.ok(warm <= 900, 'the opening warm is bounded')
  }
})

test('a same-layout poll with no new nodes settles the picture in place', () => {
  const nodeIDs = ids(200)
  const graph = model(nodeIDs)
  const first = initialWarmTicks(nodeIDs.length)
  for (const layout of ['force', 'layered'] as const) {
    const warm = warmTicks(snapshot(layout, nodeIDs), graph, layout)
    assert.equal(warm, 0, `${layout} poll must not re-warm the whole graph`)
    assert.ok(warm < 50, 'a no-op rebuild stays far below the opening warm')
    assert.ok(warm < first / 10, 'a no-op rebuild costs at most a tenth of the opening warm')
  }
})

test('a layout switch still warms like the opening picture', () => {
  const nodeIDs = ids(120)
  const graph = model(nodeIDs)
  // Both directions: force positions are meaningless in the chain tree and the
  // tree anchors are not force-map positions, so neither can be reused.
  assert.equal(warmTicks(snapshot('force', nodeIDs), graph, 'layered'), initialWarmTicks(nodeIDs.length))
  assert.equal(warmTicks(snapshot('layered', nodeIDs), graph, 'force'), initialWarmTicks(nodeIDs.length))
})

test('a different ledger in the same canvas opens like a first build', () => {
  // Switching project reuses the canvas instance but shares no node, so there is
  // no settled picture to keep: it must not start as an empty ring that animates
  // into place.
  const graph = model(ids(80, 'new'))
  assert.equal(warmTicks(snapshot('force', ids(80, 'old')), graph, 'force'), initialWarmTicks(80))
})

test('nodes introduced by a poll earn extra settling ticks, within a cap', () => {
  const nodeIDs = ids(200)
  const prev = snapshot('force', nodeIDs)
  const few = warmTicks(prev, model([...nodeIDs, 'f:new-1']), 'force')
  const many = warmTicks(prev, model([...nodeIDs, ...ids(60, 'f:new')]), 'force')
  const huge = warmTicks(prev, model([...nodeIDs, ...ids(5000, 'f:new')]), 'force')

  assert.ok(few > 0, 'a new node needs the simulation to pull it in')
  assert.ok(many > few, 'more new nodes need more settling')
  assert.ok(huge <= REBUILD_WARM_MAX, 'one huge poll cannot freeze the main thread')
  assert.ok(huge <= initialWarmTicks(nodeIDs.length + 5000), 'a rebuild never out-warms an opening')
})

test('pointer moves are hit-tested once per frame, at the latest position', () => {
  const scheduled: Array<() => void> = []
  const seen: Array<[number, number]> = []
  const coalescer = createMoveCoalescer(
    (x, y) => seen.push([x, y]),
    (cb) => {
      scheduled.push(cb)
      return scheduled.length
    },
    () => assert.fail('nothing is pending to cancel'),
  )

  coalescer.move(10, 20)
  coalescer.move(11, 21)
  coalescer.move(12, 22)
  assert.equal(scheduled.length, 1, 'three moves in one frame share one test')
  assert.deepEqual(seen, [], 'the test waits for the frame, not the event')

  scheduled.shift()!()
  assert.deepEqual(seen, [[12, 22]], 'the frame tests where the pointer ended up')

  coalescer.move(13, 23)
  assert.equal(scheduled.length, 1, 'the next frame is scheduled again')
  scheduled.shift()!()
  assert.deepEqual(seen, [[12, 22], [13, 23]])
})

test('a cancelled frame does not hover the node the pointer left', () => {
  const scheduled: Array<() => void> = []
  const seen: number[] = []
  let cancelled = 0
  const coalescer = createMoveCoalescer(
    (x) => seen.push(x),
    (cb) => {
      scheduled.push(cb)
      return scheduled.length
    },
    (handle) => {
      cancelled = handle
    },
  )

  coalescer.move(1, 1)
  coalescer.cancel()
  assert.equal(cancelled, 1)
  // A callback the browser had already queued finds nothing to test.
  scheduled.shift()!()
  assert.deepEqual(seen, [])
  assert.equal(scheduled.length, 0)

  coalescer.move(2, 2)
  assert.equal(scheduled.length, 1, 'leaving and coming back schedules a fresh test')
  scheduled.shift()!()
  assert.deepEqual(seen, [2])
})

// --- the warm is a budget, not a tick count (docs/33 §4.4) -----------------
// The opening warm used to run `initialWarmTicks(model)` ticks unconditionally:
// on a 4k-node map that is seconds of blocked main thread, and the first paint
// waits behind all of it. warmWithin trades "fully settled before paint" for a
// bounded stall; the frame loop finishes the rest. These pin the budget rule
// itself (details of cap/alpha/clock live in graph-sim.test.ts).

/** A clock that advances `stepMs` on every read; no real time passes. */
function clock(stepMs: number): () => number {
  let at = 1000
  return () => {
    const now = at
    at += stepMs
    return now
  }
}

test('a warm is bounded by its wall-clock budget, not by its tick target', () => {
  // 40 nodes with no usable previous picture would ask for hundreds of ticks.
  const sim = new ForceSim(model(ids(40)))
  assert.ok(initialWarmTicks(40) > 100, 'the tick target alone is a long stall')
  // 5ms per clock read: five ticks spend 25ms, past the default 24ms budget.
  const spent = warmWithin(sim, { now: clock(5) })
  assert.equal(spent.ticks, 5)
  assert.ok(spent.spentMs >= WARM_BUDGET_MS, 'the budget is what stopped the warm')
  assert.ok(spent.ticks < initialWarmTicks(40) / 10, 'the budget cuts the opening warm short')
})

test('a warm on a settled picture does not tick at all', () => {
  const sim = new ForceSim(model(ids(40)))
  sim.alpha = 0
  assert.deepEqual(warmWithin(sim, { now: clock(5) }), { ticks: 0, spentMs: 0 })
})

// --- camera publishing is merged to one frame (docs/33 §4.5) -----------------
// A pan fires many pointermoves per frame and a resize can fire a burst too; the
// minimap must see the camera once per frame, at its latest value. The canvas
// wires this publisher to setView, so the rule is pinned here.

test('a pan publishes the camera once per frame, at the latest position', () => {
  const frames: Array<() => void> = []
  const published: number[] = []
  const publisher = createFramePublisher<number>(
    (value) => published.push(value),
    (cb) => {
      frames.push(cb)
      return frames.length
    },
    () => assert.fail('nothing is pending to cancel'),
  )

  for (let i = 0; i < 9; i++) publisher.publish(i)
  assert.equal(frames.length, 1, 'nine moves in one frame share one publish')
  assert.deepEqual(published, [], 'the publish waits for the frame, not the event')
  frames.shift()!()
  assert.deepEqual(published, [8], 'the frame publishes where the camera ended up')

  publisher.publish(9)
  assert.equal(frames.length, 1, 'the next frame is scheduled again')
  frames.shift()!()
  assert.deepEqual(published, [8, 9])
})

test('a cancelled camera frame never reaches the minimap', () => {
  const frames: Array<() => void> = []
  const published: number[] = []
  let cancelled = 0
  const publisher = createFramePublisher<number>(
    (value) => published.push(value),
    (cb) => {
      frames.push(cb)
      return frames.length
    },
    (handle) => {
      cancelled = handle
    },
  )

  publisher.publish(1)
  publisher.cancel()
  assert.equal(cancelled, 1)
  // A callback the browser had already queued finds nothing to publish.
  frames.shift()!()
  assert.deepEqual(published, [])
  assert.equal(frames.length, 0, 'a cancelled frame does not leave a stale callback')

  publisher.publish(2)
  assert.equal(frames.length, 1, 'a later move schedules a fresh frame')
  frames.shift()!()
  assert.deepEqual(published, [2])
})

test('flush publishes the pending camera without waiting for a frame', () => {
  const frames: Array<() => void> = []
  const published: number[] = []
  let cancelled = 0
  const publisher = createFramePublisher<number>(
    (value) => published.push(value),
    (cb) => {
      frames.push(cb)
      return frames.length
    },
    () => {
      cancelled++
    },
  )

  publisher.publish(7)
  publisher.flush()
  assert.deepEqual(published, [7], 'teardown publishes the last view immediately')
  assert.equal(cancelled, 1, 'the queued frame is cancelled, not left to fire')
  // The scheduler still holds the callback the browser had queued; running it
  // must not publish a second time.
  frames.shift()!()
  assert.deepEqual(published, [7])
  // Nothing pending: a second flush is a no-op.
  publisher.flush()
  assert.deepEqual(published, [7])
})
