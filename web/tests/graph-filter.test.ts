import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  breadcrumbPath,
  factInTimeRange,
  filterGraphInputs,
  GRAPH_TIME_RANGES,
  graphNodeOrder,
  timeRangeMs,
} from '../src/lib/graph-filter.ts'
import type { GraphEdge, GraphNode } from '../src/graph/model.ts'
import type { Fact, Goal, Intent } from '../src/lib/types.ts'

const NOW = Date.parse('2026-11-12T12:00:00Z')

function fact(id: string, over: Partial<Fact> = {}): Fact {
  return {
    id,
    intent_id: 'in_a',
    kind: 'note',
    title: `fact ${id}`,
    summary: '',
    confidence: 'observed',
    status: 'unverified',
    claim_version: 1,
    created_at: '2026-11-12T11:00:00Z',
    ...over,
  }
}

function intent(id: string, over: Partial<Intent> = {}): Intent {
  return {
    id,
    title: `intent ${id}`,
    description: '',
    status: 'done',
    attempts: 1,
    created_at: '2026-11-12T10:00:00Z',
    updated_at: '2026-11-12T10:00:00Z',
    ...over,
  }
}

function goal(id: string, over: Partial<Goal> = {}): Goal {
  return {
    id,
    statement: `goal ${id}`,
    met: false,
    created_at: '2026-11-12T10:00:00Z',
    ...over,
  }
}

function node(id: string, over: Partial<GraphNode> = {}): GraphNode {
  return {
    id,
    type: 'fact',
    label: id,
    x: 0,
    y: 0,
    vx: 0,
    vy: 0,
    degree: 0,
    w: 6,
    h: 6,
    depth: 1,
    ...over,
  }
}

// ---- filterGraphInputs: hiddenKinds ----

test('hiddenKinds drops matching facts and prunes references to them', () => {
  const hidden = fact('f_vuln', { kind: 'vuln' })
  const kept = fact('f_note', { kind: 'note' })
  const other = intent('in_b', { status: 'running', from_fact_ids: [] })
  const task = intent('in_a', {
    from_fact_ids: ['f_vuln', 'f_note'],
    context_fact_ids: ['f_vuln'],
    pinned_fact_ids: ['f_note', 'f_vuln'],
  })
  const out = filterGraphInputs(
    { facts: [hidden, kept], intents: [task, other], goals: [] },
    { hiddenKinds: new Set(['vuln']), now: NOW },
  )
  assert.deepEqual(out.facts.map((f) => f.id), ['f_note'])
  // The hidden fact is a display choice, not a missing ledger row: the intent
  // copy must not keep a reference buildGraph would draw as a boundary node.
  assert.deepEqual(out.intents[0].from_fact_ids, ['f_note'])
  assert.deepEqual(out.intents[0].context_fact_ids, [])
  assert.deepEqual(out.intents[0].pinned_fact_ids, ['f_note'])
  // Untouched intents keep their identity, so memo signatures do not churn.
  assert.equal(out.intents[1], other)
})

test('hiddenKinds accepts an array and leaves every other kind alone', () => {
  const out = filterGraphInputs(
    { facts: [fact('a', { kind: 'host' }), fact('b', { kind: 'service' })], intents: [], goals: [] },
    { hiddenKinds: ['host'], now: NOW },
  )
  assert.deepEqual(out.facts.map((f) => f.id), ['b'])
})

test('no filters is the identity (same arrays, same intent objects)', () => {
  const facts = [fact('a')]
  const intents = [intent('in_a')]
  const goals = [goal('g1', { met: true })]
  const out = filterGraphInputs({ facts, intents, goals }, {})
  assert.deepEqual(out.facts, facts)
  assert.equal(out.intents[0], intents[0])
  assert.deepEqual(out.goals, goals)
})

// ---- filterGraphInputs: timeRange ----

test('timeRange keeps the window (inclusive) and never hides unknown timestamps', () => {
  const boundary = new Date(NOW - 60 * 60 * 1000).toISOString()
  const tooOld = new Date(NOW - 60 * 60 * 1000 - 1).toISOString()
  const future = new Date(NOW + 60 * 1000).toISOString()
  const out = filterGraphInputs(
    {
      facts: [
        fact('boundary', { created_at: boundary }),
        fact('old', { created_at: tooOld }),
        fact('future', { created_at: future }),
        fact('unknown', { created_at: '' }),
        fact('missing', { created_at: undefined }),
      ],
      intents: [],
      goals: [],
    },
    { timeRange: '1h', now: NOW },
  )
  assert.deepEqual(out.facts.map((f) => f.id), ['boundary', 'future', 'unknown', 'missing'])
})

test('timeRange all is a no-op and ranges only touch facts', () => {
  const old = fact('old', { created_at: '2020-01-01T00:00:00Z' })
  const task = intent('in_a')
  const win = goal('g1')
  assert.deepEqual(
    filterGraphInputs({ facts: [old], intents: [task], goals: [win] }, { timeRange: 'all', now: NOW }).facts,
    [old],
  )
  const out = filterGraphInputs({ facts: [old], intents: [task], goals: [win] }, { timeRange: '24h', now: NOW })
  assert.equal(out.facts.length, 0)
  assert.equal(out.intents[0], task)
  assert.equal(out.goals[0], win)
})

test('time range table is ordered and timeRangeMs is defensive', () => {
  assert.deepEqual(
    GRAPH_TIME_RANGES.map((r) => r.id),
    ['all', '1h', '24h', '7d', '30d'],
  )
  assert.equal(timeRangeMs('24h'), 24 * 60 * 60 * 1000)
  assert.equal(timeRangeMs('all'), 0)
  // Unknown garbage never widens or narrows the view by accident.
  assert.equal(timeRangeMs('nonsense' as never), 0)
  assert.equal(factInTimeRange(undefined, 1000, NOW), true)
  assert.equal(factInTimeRange('not a date', 1000, NOW), true)
})

// ---- filterGraphInputs: hideDone ----

test('hideDone drops finished directions and the facts they produced, keeps goals', () => {
  const doneFact = fact('f_done', { intent_id: 'in_done' })
  const liveFact = fact('f_live', { intent_id: 'in_live' })
  const done = intent('in_done', { status: 'done' })
  const live = intent('in_live', { status: 'running' })
  const out = filterGraphInputs(
    { facts: [doneFact, liveFact], intents: [done, live], goals: [goal('g1', { met: true })] },
    { hideDone: true, now: NOW },
  )
  assert.deepEqual(out.facts.map((f) => f.id), ['f_live'])
  assert.deepEqual(out.intents.map((i) => i.id), ['in_live'])
  assert.equal(out.goals.length, 1)
})

test('hideDone also strips a surviving intent premise that pointed at a hidden fact', () => {
  const doneFact = fact('f_done', { intent_id: 'in_done' })
  const done = intent('in_done', { status: 'done' })
  const live = intent('in_live', { status: 'running', from_fact_ids: ['f_done'] })
  const out = filterGraphInputs(
    { facts: [doneFact], intents: [done, live], goals: [] },
    { hideDone: true, now: NOW },
  )
  assert.deepEqual(out.intents[0].from_fact_ids, [])
})

// ---- filterGraphInputs: focusOnly ----

test('focusOnly keeps only nodes on the focused path', () => {
  const onPath = fact('f_on')
  const offPath = fact('f_off')
  const onStep = intent('in_on')
  const offStep = intent('in_off')
  const focusSet = new Set(['p:p', 'f:f_on', 's:in_on', 'g:g_on'])
  const out = filterGraphInputs(
    {
      facts: [onPath, offPath],
      intents: [onStep, offStep],
      goals: [goal('g_on'), goal('g_off')],
    },
    { focusOnly: true, focusSet, now: NOW },
  )
  assert.deepEqual(out.facts.map((f) => f.id), ['f_on'])
  assert.deepEqual(out.intents.map((i) => i.id), ['in_on'])
  assert.deepEqual(out.goals.map((g) => g.id), ['g_on'])
})

test('focusOnly recognises a ghost ring id and is a no-op without a set', () => {
  const empty = intent('in_ghost')
  const out = filterGraphInputs(
    { facts: [], intents: [empty], goals: [] },
    { focusOnly: true, focusSet: new Set(['w:in_ghost']), now: NOW },
  )
  assert.deepEqual(out.intents.map((i) => i.id), ['in_ghost'])
  const noSet = filterGraphInputs({ facts: [fact('a')], intents: [intent('in_a')], goals: [] }, {
    focusOnly: true,
    focusSet: null,
    now: NOW,
  })
  assert.equal(noSet.facts.length, 1)
  assert.equal(noSet.intents.length, 1)
})

// ---- graphNodeOrder ----

test('graphNodeOrder is depth, then y, then x, then id — and never mutates', () => {
  const nodes = [
    node('z', { depth: 2, y: 0, x: 0 }),
    node('b', { depth: 1, y: 0, x: 10 }),
    node('a', { depth: 1, y: 0, x: 10 }),
    node('top', { depth: 1, y: -5, x: 99 }),
    node('left', { depth: 1, y: 0, x: -5 }),
  ]
  const original = [...nodes]
  const ordered = graphNodeOrder(nodes)
  assert.deepEqual(ordered.map((n) => n.id), ['top', 'left', 'a', 'b', 'z'])
  // The input array order is untouched (callers pass model.nodes directly).
  assert.deepEqual(
    nodes.map((n) => n.id),
    original.map((n) => n.id),
  )
  // Deterministic: a second call on the same input returns the same order.
  assert.deepEqual(
    graphNodeOrder(nodes).map((n) => n.id),
    ordered.map((n) => n.id),
  )
})

test('graphNodeOrder compares coordinates, not truthiness (negative and zero y)', () => {
  const nodes = [node('b', { depth: 0, y: 0, x: 0 }), node('a', { depth: 0, y: -1, x: 0 }), node('c', { depth: 0, y: 0, x: -1 })]
  assert.deepEqual(
    graphNodeOrder(nodes).map((n) => n.id),
    ['a', 'c', 'b'],
  )
})

// ---- breadcrumbPath ----

function edge(source: string, target: string, kind: GraphEdge['kind'] = 'premise'): GraphEdge {
  return { id: `${source}->${target}:${kind}`, source, target, kind }
}

test('breadcrumbPath returns the shortest upstream path project → … → node', () => {
  const nodes = [
    node('p:proj', { type: 'project', depth: 0 }),
    node('f:a'),
    node('s:in_a', { type: 'step', depth: 2 }),
    node('f:b', { depth: 3 }),
  ]
  const edges = [edge('p:proj', 'f:a'), edge('f:a', 's:in_a'), edge('s:in_a', 'f:b')]
  // Minimal model (no incoming index): falls back to building it from edges.
  const path = breadcrumbPath({ nodes, edges }, 'f:b')
  assert.deepEqual(path.map((n) => n.id), ['p:proj', 'f:a', 's:in_a', 'f:b'])
  // The built model's incoming index takes precedence when present.
  const incoming = new Map([
    ['f:a', ['p:proj']],
    ['s:in_a', ['f:a']],
    ['f:b', ['s:in_a']],
  ])
  const viaIndex = breadcrumbPath({ nodes, edges: [], incoming }, 'f:b')
  assert.deepEqual(viaIndex.map((n) => n.id), ['p:proj', 'f:a', 's:in_a', 'f:b'])
  // The project is its own breadcrumb.
  assert.deepEqual(breadcrumbPath({ nodes, edges }, 'p:proj').map((n) => n.id), ['p:proj'])
})

test('breadcrumbPath takes a shorter route when two paths reach the project', () => {
  const nodes = [node('p:proj', { type: 'project', depth: 0 }), node('f:long'), node('f:short'), node('s:in_a', { type: 'step' }), node('f:b')]
  const edges = [
    edge('p:proj', 'f:long'),
    edge('f:long', 'f:short'),
    edge('f:short', 'p:proj'), // cycle back to the project
    edge('p:proj', 'f:short'),
    edge('f:short', 's:in_a'),
    edge('s:in_a', 'f:b'),
  ]
  assert.deepEqual(breadcrumbPath({ nodes, edges }, 'f:b').map((n) => n.id), ['p:proj', 'f:short', 's:in_a', 'f:b'])
})

test('breadcrumbPath handles cycles, unknown ids and a missing project', () => {
  const nodes = [node('a'), node('b'), node('c')]
  const edges = [edge('a', 'b'), edge('b', 'a'), edge('b', 'c')]
  // No project anywhere: the path is not invented, it is empty.
  assert.deepEqual(breadcrumbPath({ nodes, edges }, 'c'), [])
  assert.deepEqual(breadcrumbPath({ nodes, edges }, 'nope'), [])
  assert.deepEqual(breadcrumbPath({ nodes, edges }, null), [])
  // Self-loop and two-node cycle: terminates and still finds no project.
  const cyclic = [node('x'), node('y')]
  assert.deepEqual(breadcrumbPath({ nodes: cyclic, edges: [edge('x', 'x'), edge('x', 'y'), edge('y', 'x')] }, 'y'), [])
})

test('breadcrumbPath prefers the project when it is also upstream of a cycle', () => {
  const nodes = [node('p:proj', { type: 'project', depth: 0 }), node('f:a'), node('f:b')]
  const edges = [edge('p:proj', 'f:a'), edge('f:a', 'f:b'), edge('f:b', 'f:a')]
  assert.deepEqual(breadcrumbPath({ nodes, edges }, 'f:b').map((n) => n.id), ['p:proj', 'f:a', 'f:b'])
})
