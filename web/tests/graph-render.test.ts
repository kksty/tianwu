// Render budget rules from docs/33 §4.3: viewport clipping, level of detail,
// the label collision grid and the settled-frame geometry cache.
//
// These tests deliberately run without a canvas: the DOM-touching part of
// render.ts is `paint` only (and even there the background tile is built lazily),
// so the pure rules can be pinned under `node --test`.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  BOX_CELL,
  BoxGrid,
  CANVAS,
  EDGE_COLORS,
  FACT_COLORS,
  GeometryCache,
  INTENT_COLORS,
  LOD_SHAPES_K,
  VIEWPORT_MARGIN,
  cachedEdgeCurve,
  clipNodes,
  edgeCurve,
  edgeSetIdentity,
  edgeStyle,
  fitText,
  isEdgeVisible,
  isNodeVisible,
  lod,
  nodeColor,
  viewportOf,
  wrapText,
} from '../src/graph/render.ts'
import type { Box, EdgeCurve } from '../src/graph/render.ts'
import type { GraphEdge, GraphNode } from '../src/graph/model.ts'

function fact(id: string, x = 0, y = 0): GraphNode {
  return {
    id,
    type: 'fact',
    label: id,
    x,
    y,
    vx: 0,
    vy: 0,
    degree: 0,
    w: 6,
    h: 6,
    depth: 1,
  }
}

function pill(id: string, x = 0, y = 0, w = 86, h = 20): GraphNode {
  return { id, type: 'intent', label: id, x, y, vx: 0, vy: 0, degree: 0, w, h, depth: 1 }
}

function edge(id: string, source: string, target: string, kind: GraphEdge['kind'] = 'intent'): GraphEdge {
  return { id, source, target, kind }
}

// --- viewport -------------------------------------------------------------

test('viewportOf maps the camera to world coordinates with a draw margin', () => {
  const vp = viewportOf({ x: 100, y: -50, k: 2 }, 800, 600)
  // screen (0,0) → world ((0 - cam.x)/k, (0 - cam.y)/k) = (-50, 25)
  assert.equal(vp.x0, -50 - VIEWPORT_MARGIN)
  assert.equal(vp.y0, 25 - VIEWPORT_MARGIN)
  // screen (800,600) → ((800-100)/2, (600+50)/2) = (350, 325)
  assert.equal(vp.x1, 350 + VIEWPORT_MARGIN)
  assert.equal(vp.y1, 325 + VIEWPORT_MARGIN)
})

test('viewportOf survives a degenerate zoom without producing infinities', () => {
  const vp = viewportOf({ x: 0, y: 0, k: 0 }, 100, 100)
  for (const value of [vp.x0, vp.y0, vp.x1, vp.y1]) assert.ok(Number.isFinite(value))
})

test('isNodeVisible keeps nodes that straddle the edge and drops far ones', () => {
  const vp = viewportOf({ x: 0, y: 0, k: 1 }, 800, 600)
  assert.equal(isNodeVisible(fact('f:in', 400, 300), vp), true)
  // A pill whose centre is outside but whose body reaches into the rect draws.
  assert.equal(isNodeVisible(pill('f:straddle', vp.x1 + 40, 300), vp), true)
  // A dot well past the right edge does not.
  assert.equal(isNodeVisible(fact('f:out', vp.x1 + 200, 300), vp), false)
  assert.equal(isNodeVisible(fact('f:far', 1e6, 1e6), vp), false)
})

test('clipNodes counts zero draws for nodes outside the viewport', () => {
  const vp = viewportOf({ x: 0, y: 0, k: 1 }, 1200, 800)
  const outside = Array.from({ length: 5000 }, (_, i) => fact(`f:out:${i}`, 100000 + i, 100000))
  assert.equal(clipNodes(outside, vp).length, 0, 'a map that is entirely off-screen paints nothing')

  const inside = Array.from({ length: 10 }, (_, i) => fact(`f:in:${i}`, 600 + i, 400))
  const drew = clipNodes([...outside, ...inside], vp)
  assert.equal(drew.length, 10)
  assert.ok(drew.every((n) => n.id.startsWith('f:in:')), 'only the visible nodes are drawn')
})

test('clipping filters the draw list without touching the model', () => {
  const nodes = [fact('f:a', 0, 0), fact('f:b', 1e6, 0)]
  const vp = viewportOf({ x: 0, y: 0, k: 1 }, 800, 600)
  const before = nodes.map((n) => ({ ...n }))
  const drew = clipNodes(nodes, vp)
  assert.equal(nodes.length, 2, 'the model keeps every node')
  assert.deepEqual(nodes.map((n) => ({ ...n })), before, 'clipping never mutates a node')
  assert.equal(drew.length, 1)
})

test('isEdgeVisible keeps an edge that crosses the viewport between off-screen nodes', () => {
  const vp = viewportOf({ x: 0, y: 0, k: 1 }, 800, 600)
  // Both endpoints are far outside, but the spoke runs through the screen: the
  // control-point hull test has to keep it.
  assert.equal(isEdgeVisible(fact('f:s', -5000, 300), fact('f:t', 5000, 300), 'intent', false, vp), true)
  // An edge that passes entirely below the viewport is dropped.
  assert.equal(isEdgeVisible(fact('f:s', -5000, -5000), fact('f:t', 5000, -5000), 'intent', false, vp), false)
  // Layered curves are tested on the same hull.
  assert.equal(isEdgeVisible(fact('f:s', -5000, 300), fact('f:t', 5000, 300), 'intent', true, vp), true)
})

// --- LOD ------------------------------------------------------------------

test('lod lowers detail as the camera zooms out', () => {
  const close = lod(2, { layered: false, showLabels: false })
  assert.equal(close.shapes, true)
  assert.equal(close.factLabels, true, 'fact names are worth their box when zoomed in')
  assert.equal(close.stepLabels, true)
  assert.ok(close.maxLabels >= 100)

  const far = lod(0.1, { layered: false, showLabels: false })
  assert.equal(far.shapes, false, 'far out the map costs dots and lines only')
  assert.equal(far.factLabels, false)
  assert.equal(far.stepLabels, false)
  assert.equal(far.maxLabels, 12)
})

test('lod shape threshold is where the contract says it is', () => {
  assert.equal(lod(LOD_SHAPES_K, { layered: false, showLabels: false }).shapes, true)
  assert.equal(lod(LOD_SHAPES_K - 0.01, { layered: false, showLabels: false }).shapes, false)
  // The chain tree keeps its pills a little longer than the force map.
  assert.equal(lod(LOD_SHAPES_K, { layered: true, showLabels: false }).shapes, true)
  assert.equal(lod(LOD_SHAPES_K * 0.7, { layered: true, showLabels: false }).shapes, true)
})

test('lod honours showLabels and a monotonic label budget', () => {
  // showLabels is the operator saying "give me every intent name", so it turns
  // background step labels on even when the zoom alone would not.
  assert.equal(lod(0.3, { layered: false, showLabels: false }).stepLabels, false)
  assert.equal(lod(0.3, { layered: false, showLabels: true }).stepLabels, true)

  let previous = 0
  for (const k of [0.05, 0.2, 0.4, 0.6, 0.9, 1.2, 1.7, 3]) {
    const level = lod(k, { layered: false, showLabels: false })
    assert.ok(level.maxLabels > 0, 'a frame always gets some labels')
    assert.ok(level.maxLabels >= previous, `budget must not fall as the map gets bigger (k=${k})`)
    previous = level.maxLabels
  }
  // A degenerate camera (zoom 0 / NaN) falls back to the 1:1 level instead of
  // silently dropping the whole picture to dots.
  assert.deepEqual(lod(0, { layered: false, showLabels: false }), lod(1, { layered: false, showLabels: false }))
  assert.deepEqual(lod(Number.NaN, { layered: false, showLabels: false }), lod(1, { layered: false, showLabels: false }))
})

// --- label collision grid -------------------------------------------------

/** The linear implementation the grid must agree with. */
function linearOverlaps(boxes: Box[], box: Box, pad: number): boolean {
  return boxes.some(
    (b) =>
      box.x1 < b.x2 + pad && box.x2 > b.x1 - pad && box.y1 < b.y2 + pad && box.y2 > b.y1 - pad,
  )
}

function pseudoRandom(seed: number): () => number {
  let state = seed >>> 0
  return () => {
    state = (Math.imul(state, 1664525) + 1013904223) >>> 0
    return state / 4294967296
  }
}

test('BoxGrid agrees with the linear collision check', () => {
  const rnd = pseudoRandom(0x5eed)
  const boxes: Box[] = []
  for (let i = 0; i < 200; i++) {
    const x = (rnd() - 0.5) * 4000
    const y = (rnd() - 0.5) * 4000
    boxes.push({ x1: x, y1: y, x2: x + 10 + rnd() * 260, y2: y + 10 + rnd() * 40 })
  }
  // One very wide box exercises the "kept off the buckets" path.
  boxes.push({ x1: -3000, y1: -30, x2: 3000, y2: 30 })

  const grid = new BoxGrid()
  for (const box of boxes) grid.insert(box)
  assert.equal(grid.size, boxes.length)

  for (let q = 0; q < 400; q++) {
    const x = (rnd() - 0.5) * 4400
    const y = (rnd() - 0.5) * 4400
    const query: Box = { x1: x, y1: y, x2: x + rnd() * 240, y2: y + rnd() * 40 }
    for (const pad of [0, 3, 12, 64]) {
      assert.equal(
        grid.overlaps(query, pad),
        linearOverlaps(boxes, query, pad),
        `query ${q} pad ${pad} disagrees with the linear scan`,
      )
    }
  }
})

test('BoxGrid buckets by 64 world units and keeps clear space empty', () => {
  const grid = new BoxGrid()
  assert.equal(grid.overlaps({ x1: 0, y1: 0, x2: 10, y2: 10 }), false)
  grid.insert({ x1: 0, y1: 0, x2: 10, y2: 10 })
  assert.equal(grid.overlaps({ x1: 20, y1: 20, x2: 30, y2: 30 }), false)
  assert.equal(grid.overlaps({ x1: 20, y1: 20, x2: 30, y2: 30 }, 20), true)
  // Exactly the contract's cell size: -1 and +1 land in different buckets.
  assert.equal(BOX_CELL, 64)
  grid.clear()
  assert.equal(grid.size, 0)
  assert.equal(grid.overlaps({ x1: 0, y1: 0, x2: 10, y2: 10 }), false)
})

// --- edge geometry cache --------------------------------------------------

test('settled frames reuse edge curves by id; moving frames rebuild them', () => {
  const a = fact('f:a', 0, 0)
  const b = fact('f:b', 100, 40)
  const e = edge('f:a->f:b:intent:in_1', 'f:a', 'f:b')
  const cache = new GeometryCache<EdgeCurve>()

  assert.equal(cache.begin(edgeSetIdentity([e], false)), true)
  const first = cachedEdgeCurve(cache, e, a, b, false, true)
  assert.equal(cache.size, 1)
  const again = cachedEdgeCurve(cache, e, a, b, false, true)
  assert.equal(again, first, 'a settled frame hands back the same curve object')

  // A moving frame (settled=false) must not read or write the cache.
  b.x = 300
  b.y = -50
  const moving = cachedEdgeCurve(cache, e, a, b, false, false)
  assert.notEqual(moving, first, 'a moving node gets fresh geometry')
  assert.notEqual(moving.tx, first.tx)
  assert.equal(cache.size, 1, 'unsettled frames never write the cache')
})

test('a new edge picture drops every cached curve; the same picture keeps them', () => {
  const a = fact('f:a', 0, 0)
  const b = fact('f:b', 100, 0)
  const c = fact('f:c', 0, 100)
  const one = edge('f:a->f:b:intent:', 'f:a', 'f:b')
  const two = edge('f:a->f:c:intent:', 'f:a', 'f:c')
  const cache = new GeometryCache<EdgeCurve>()

  const base = edgeSetIdentity([one], false)
  cache.begin(base)
  cachedEdgeCurve(cache, one, a, b, false, true)
  assert.equal(cache.size, 1)

  // Same edge set → same identity → entries stay.
  assert.equal(cache.begin(edgeSetIdentity([one], false)), false)
  assert.equal(cache.size, 1)

  // A poll that added an edge is a different picture.
  const withMore = edgeSetIdentity([one, two], false)
  assert.notEqual(withMore, base)
  assert.equal(cache.begin(withMore), true)
  assert.equal(cache.size, 0, 'a changed edge set must not reuse a stale curve')
})

test('edge set identity also separates layout modes', () => {
  const e = edge('f:a->f:b:intent:', 'f:a', 'f:b')
  assert.equal(edgeSetIdentity([e], false), edgeSetIdentity([edge(e.id, e.source, e.target)], false))
  assert.notEqual(edgeSetIdentity([e], true), edgeSetIdentity([e], false))
  // A different edge count or a different id is a different picture.
  assert.notEqual(edgeSetIdentity([e], false), edgeSetIdentity([], false))
  assert.notEqual(
    edgeSetIdentity([e], false),
    edgeSetIdentity([edge('f:a->f:c:intent:', 'f:a', 'f:c')], false),
  )
})

test('edgeCurve stays consistent with the cached path', () => {
  const a = fact('f:a', 10, 10)
  const b = fact('f:b', 210, 80)
  const e = edge('f:a->f:b:premise:', 'f:a', 'f:b', 'premise')
  const cache = new GeometryCache<EdgeCurve>()
  cache.begin(edgeSetIdentity([e], false))
  const cached = cachedEdgeCurve(cache, e, a, b, false, true)
  assert.deepEqual(cached, edgeCurve(a, b, false, 'premise'))
})

// --- bounded text fitting ------------------------------------------------

test('fitText caches measurements and still shortens long text', () => {
  let measured = 0
  const ctx = {
    font: '600 12px "Inter",sans-serif',
    measureText(text: string) {
      measured++
      return { width: text.length * 7 }
    },
  }

  // Unique strings keep this test independent of the module-level cache.
  const short = 'render-fit-short-label'
  assert.equal(fitText(ctx, short, 100000), short)
  const afterShort = measured
  assert.equal(measured, afterShort)
  assert.equal(fitText(ctx, short, 100000), short)
  assert.equal(measured, afterShort, 'a repeated (font, text, maxW) is served from the cache')

  const long = 'render-fit-long-label-'.repeat(12)
  const fitted = fitText(ctx, long, 70)
  assert.ok(fitted.endsWith('…'), 'long text is shortened')
  assert.ok(fitted.length < long.length)
  assert.ok(measured > afterShort, 'the first fit of a new string measures it')
})

// --- multi-line pill labels ----------------------------------------------

test('wrapText keeps Latin words whole, breaks CJK, caps lines and ellipsises', () => {
  // Width model: 7px per latin glyph, 12px per CJK glyph — enough to prove the
  // break decisions without a real canvas.
  const ctx = {
    font: '600 12px "Inter",sans-serif',
    measureText(text: string) {
      let w = 0
      for (const ch of text) w += /[\u0000-\u007f]/u.test(ch) ? 7 : 12
      return { width: w }
    },
  }

  const latin = wrapText(ctx, 'alpha beta gamma delta epsilon', 100, 3)
  assert.ok(latin.length > 1 && latin.length <= 3, 'a long latin label wraps')
  for (const line of latin) assert.ok(ctx.measureText(line).width <= 100, `"${line}" fits the box`)
  assert.ok(latin.every((line) => !line.startsWith(' ')), 'no line starts with a space')

  const cjk = wrapText(ctx, '首轮测绘目标主机全端口与站点指纹识别', 60, 3)
  assert.ok(cjk.length > 1, 'CJK with no spaces still wraps')
  for (const line of cjk) assert.ok(ctx.measureText(line).width <= 60, `"${line}" fits the box`)

  // A single unbroken token (a URL / run id) is broken per character.
  const id = wrapText(ctx, 'r_03f938be6983c2036be3639279e17e98', 70, 3)
  assert.ok(id.length > 1)
  for (const line of id) assert.ok(ctx.measureText(line).width <= 70)

  const capped = wrapText(ctx, 'a'.repeat(200), 70, 2)
  assert.equal(capped.length, 2, 'never more lines than the cap')
  assert.ok(capped[1].endsWith('…'), 'the overflow is ellipsised, not dropped silently')

  assert.deepEqual(wrapText(ctx, '   ', 50, 2), [''], 'blank labels stay one empty line')
})

// --- relation encoding ---------------------------------------------------
//
// edgeStyle is the one contract the painter and the legend describe: solid blue
// is the hard gate, a blocked gate is dotted red, and a reviewing direction's
// produced-fact edge is dashed and teal so a solid blue line can only mean one
// thing. These are pure assertions on that contract; `paint` calls the same
// function and the painter picks the exact dash pattern (dotted for a blocked
// gate, long dashes for live execution).

function relation(over: Partial<GraphEdge> = {}): GraphEdge {
  return { id: 'e', source: 'f:x', target: 's:in_x', kind: 'premise', ...over }
}

test('a blocked premise belongs to the non-solid family and keeps the blocked colour', () => {
  const style = edgeStyle(relation({ blocked: true }))
  assert.equal(style.dashed, true, 'a blocked gate must not be a solid line')
  assert.equal(style.color, EDGE_COLORS.premiseBlocked)
  assert.notEqual(style.color, INTENT_COLORS.running, 'live-execution red belongs to running intents')
  assert.notEqual(style.color, CANVAS.primary)
})

test('a normal (satisfied) premise stays the solid hard-gate blue', () => {
  const style = edgeStyle(relation())
  assert.equal(style.dashed, false)
  assert.equal(style.color, EDGE_COLORS.premise)
})

test('a reviewing intent has its own verification hue and a dashed edge', () => {
  const reviewing = edgeStyle(relation({ kind: 'intent', status: 'reviewing' }))
  assert.equal(reviewing.dashed, true)
  assert.equal(reviewing.color, INTENT_COLORS.reviewing)
  assert.equal(INTENT_COLORS.reviewing, CANVAS.review)
  // The whole point: "复核中" must not read as a necessary-premise gate, and it
  // must not read as a finished (green) direction either.
  assert.notEqual(INTENT_COLORS.reviewing, EDGE_COLORS.premise)
  assert.notEqual(INTENT_COLORS.reviewing, INTENT_COLORS.done)
  assert.notEqual(INTENT_COLORS.reviewing, CANVAS.info)
  // Running uses marching dashes painted from the live animation, not the
  // static dashed family; its colour is the one red in the picture.
  const running = edgeStyle(relation({ kind: 'intent', status: 'running' }))
  assert.equal(running.dashed, false)
  assert.equal(running.color, INTENT_COLORS.running)
})

test('a step in review is not coloured like the premise gate', () => {
  const node: GraphNode = {
    id: 's:in_review',
    type: 'step',
    label: '等待复核的方向',
    status: 'reviewing',
    x: 0,
    y: 0,
    vx: 0,
    vy: 0,
    degree: 2,
    w: 40,
    h: 10,
    depth: 3,
  }
  // nodeColor is the painter's single choice; the legend imports the same table.
  assert.equal(nodeColor(node), INTENT_COLORS.reviewing)
  assert.notEqual(nodeColor(node), EDGE_COLORS.premise)
})

test('the blocked flag only dashes the premise it belongs to', () => {
  const done = edgeStyle(relation({ kind: 'intent', status: 'done', blocked: true }))
  assert.equal(done.dashed, false, 'a blocked flag must not leak into another relation')
  assert.equal(done.color, INTENT_COLORS.done)
})

test('every intent status has a colour; pre-run states share amber and review_failed is not claimed', () => {
  const statuses = [
    'pending',
    'claimed',
    'waiting',
    'running',
    'reviewing',
    'done',
    'failed',
    'review_failed',
    'parked',
  ]
  for (const status of statuses) {
    assert.ok(INTENT_COLORS[status], `${status} must have a graph colour`)
  }
  // Claiming and waiting for a child are both "not executing yet"; the panel
  // labels separate them, but the graph deliberately keeps them in one family.
  assert.equal(INTENT_COLORS.claimed, INTENT_COLORS.pending)
  assert.equal(INTENT_COLORS.waiting, INTENT_COLORS.pending)
  // The old map painted claimed and review_failed with the same orange; a
  // rejected review must not look like a direction that just got claimed.
  assert.notEqual(INTENT_COLORS.review_failed, INTENT_COLORS.claimed)
  assert.notEqual(INTENT_COLORS.review_failed, INTENT_COLORS.pending)
})

test('a waiting direction is amber, not the parked grey', () => {
  const node: GraphNode = {
    id: 's:in_wait',
    type: 'step',
    label: '等待子任务',
    status: 'waiting',
    x: 0,
    y: 0,
    vx: 0,
    vy: 0,
    degree: 2,
    w: 40,
    h: 10,
    depth: 3,
  }
  assert.equal(nodeColor(node), INTENT_COLORS.waiting)
  assert.notEqual(nodeColor(node), INTENT_COLORS.parked)
})

test('pre-run and waiting intent edges are dashed, not solid', () => {
  for (const status of ['pending', 'claimed', 'waiting'] as const) {
    assert.equal(edgeStyle(relation({ kind: 'intent', status })).dashed, true, status)
  }
  // Terminal states keep a solid produced edge.
  assert.equal(edgeStyle(relation({ kind: 'intent', status: 'done' })).dashed, false)
  assert.equal(edgeStyle(relation({ kind: 'intent', status: 'review_failed' })).dashed, false)
})

test('no accidental duplicate colour inside one channel', () => {
  // Fact kinds are one channel: seven kinds, seven hues.
  const factColors = Object.values(FACT_COLORS)
  assert.equal(new Set(factColors).size, factColors.length, 'two fact kinds share a colour')
  // Relations are one channel: each relation keeps its own stroke (the alpha
  // variants are deliberate, but the literal values must stay distinct).
  const edgeColors = Object.values(EDGE_COLORS)
  assert.equal(new Set(edgeColors).size, edgeColors.length, 'two relations share a colour')
  // Intent families: the three pre-run states are one deliberate amber family;
  // every other status is its own hue.
  const families = ['pending', 'running', 'reviewing', 'done', 'failed', 'review_failed', 'parked'].map(
    (status) => INTENT_COLORS[status],
  )
  assert.equal(new Set(families).size, families.length, 'two intent families share a colour')
})
