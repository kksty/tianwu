import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildGraph, edgeKindLabel, focusPath, stepID } from '../src/graph/model.ts'
import type { Fact, Goal, Intent } from '../src/lib/types.ts'

function fact(id: string, over: Partial<Fact> = {}): Fact {
  return {
    id,
    intent_id: 'in_task',
    kind: 'note',
    title: id,
    summary: '',
    confidence: 'observed',
    status: 'unverified',
    claim_version: 1,
    created_at: '2026-11-11T00:00:00Z',
    ...over,
  }
}

function intent(over: Partial<Intent> = {}): Intent {
  return {
    id: 'in_task',
    title: 'task',
    description: '',
    status: 'running',
    attempts: 1,
    created_at: '2026-11-11T00:00:00Z',
    updated_at: '2026-11-11T00:00:00Z',
    ...over,
  }
}

function goal(over: Partial<Goal> = {}): Goal {
  return {
    id: 'g_1',
    statement: '达成目标',
    met: true,
    created_at: '2026-11-11T00:00:00Z',
    ...over,
  }
}

function sortedAdjacency(m: Map<string, string[]>): Map<string, string[]> {
  return new Map([...m].map(([k, v]) => [k, [...v].sort()]))
}

test('pin and reference are distinct relations; the dedupe key carries the kind', () => {
  const graph = buildGraph(
    [fact('f_ref'), fact('f_pin'), fact('f_both')],
    [intent({ from_fact_ids: [] })],
    [],
    undefined,
    [],
    {},
    '',
    [],
    [
      { source: 'fact:f_ref', target: 'intent:in_task', kind: 'reference' },
      // The ledger may repeat an edge; same source/target/kind must still dedupe.
      { source: 'fact:f_ref', target: 'intent:in_task', kind: 'reference' },
      { source: 'fact:f_pin', target: 'intent:in_task', kind: 'pin' },
      // One fact can be both read as context and pinned: both relations survive.
      { source: 'fact:f_both', target: 'intent:in_task', kind: 'reference' },
      { source: 'fact:f_both', target: 'intent:in_task', kind: 'pin' },
    ],
  )
  const at = (source: string, kind: string) =>
    graph.edges.filter((e) => e.source === source && e.target === 's:in_task' && e.kind === kind)
  assert.equal(at('f:f_ref', 'reference').length, 1, 'a repeated reference edge is deduped')
  assert.equal(at('f:f_ref', 'pin').length, 0, 'a reference never turns into a pin')
  assert.equal(at('f:f_pin', 'pin').length, 1)
  assert.equal(at('f:f_pin', 'reference').length, 0, 'a pin never turns into a reference')
  assert.equal(at('f:f_both', 'reference').length, 1)
  assert.equal(at('f:f_both', 'pin').length, 1, 'pair+kind dedupe keeps both relations')
  // A pin still names the direction whose panel it opens.
  assert.equal(at('f:f_pin', 'pin')[0].intent?.id, 'in_task')
  assert.equal(edgeKindLabel('reference'), '引用')
  assert.equal(edgeKindLabel('pin'), '固定')
})

test('the model indexes every edge and derives step/fact/incoming/outgoing lookups', () => {
  const produced = fact('f_out')
  const graph = buildGraph(
    [fact('f_pre', { intent_id: 'in_other' }), produced],
    [intent({ from_fact_ids: ['f_pre'] })],
    [],
  )
  // edge.id → edge.
  assert.equal(graph.edgesById.size, new Set(graph.edges.map((e) => e.id)).size)
  for (const e of graph.edges) assert.equal(graph.edgesById.get(e.id), e)
  // intent.id → its step node (the one the canvas selects).
  assert.equal(graph.stepByIntent.get('in_task'), graph.byId.get(stepID('in_task')))
  assert.equal(graph.stepByIntent.get('in_task')?.type, 'step')
  // fact.id (unprefixed) → the fact body, for detail re-fetch.
  assert.equal(graph.factsById.get('f_out'), produced)
  assert.equal(graph.factsById.get('f_pre')?.id, 'f_pre')
  // Adjacency indexes match the edges exactly.
  assert.deepEqual(graph.incoming.get('s:in_task'), ['f:f_pre'])
  assert.deepEqual(graph.outgoing.get('s:in_task'), ['f:f_out'])
  assert.deepEqual(graph.incoming.get('f:f_out'), ['s:in_task'])
  const naiveIn = new Map<string, string[]>()
  const naiveOut = new Map<string, string[]>()
  for (const e of graph.edges) {
    naiveIn.set(e.target, [...(naiveIn.get(e.target) ?? []), e.source])
    naiveOut.set(e.source, [...(naiveOut.get(e.source) ?? []), e.target])
  }
  assert.deepEqual(sortedAdjacency(graph.incoming), sortedAdjacency(naiveIn))
  assert.deepEqual(sortedAdjacency(graph.outgoing), sortedAdjacency(naiveOut))
})

test('host/service pairing still takes the first host with a matching title', () => {
  const graph = buildGraph(
    [
      fact('h_first', { kind: 'host', title: '10.0.0.5' }),
      fact('h_second', { kind: 'host', title: '10.0.0.5' }),
      fact('svc', { kind: 'service', title: '10.0.0.5:8080' }),
      fact('svc_orphan', { kind: 'service', title: '10.9.9.9:443' }),
    ],
    [],
    [],
  )
  const hostEdges = graph.edges.filter((e) => e.kind === 'host')
  assert.equal(hostEdges.length, 1, 'only the service with a known host is paired')
  assert.equal(hostEdges[0].source, 'f:h_first', 'the first host fact wins, as before')
  assert.equal(hostEdges[0].target, 'f:svc')
})

test('goal evidence ownership takes the first fact carrying that evidence', () => {
  const first = fact('f_first', { evidence_ids: ['e_shared'] })
  const second = fact('f_second', { evidence_ids: ['e_shared'] })
  const graph = buildGraph(
    [first, second],
    [],
    [goal({ evidence_id: 'e_shared' })],
  )
  const goalEdges = graph.edges.filter((e) => e.kind === 'goal')
  assert.equal(goalEdges.length, 1)
  assert.equal(goalEdges[0].source, 'f:f_first')
  assert.equal(goalEdges[0].target, 'g:g_1')
  // No owner → the brief fallback edge, not a guessed goal line.
  const fallback = buildGraph([], [], [goal({ evidence_id: 'e_missing' })])
  assert.equal(fallback.edges.filter((e) => e.kind === 'goal').length, 0)
  assert.equal(fallback.edges.filter((e) => e.kind === 'brief').length, 1)
})

test('an unmet goal stays off the map, unconnected, until it is achieved', () => {
  const graph = buildGraph([fact('f_out')], [intent()], [goal({ met: false })])
  assert.equal(graph.byId.has('g:g_1'), false, 'an unmet goal must not be drawn as a node')
  assert.equal(graph.edges.some((e) => e.target === 'g:g_1'), false, 'an unmet goal must not be connected')

  // Once achieved it is a target node anchored to the fact carrying its evidence.
  const done = buildGraph(
    [fact('f_done', { evidence_ids: ['e_done'] })],
    [],
    [goal({ evidence_id: 'e_done' })],
  )
  assert.equal(done.byId.get('g:g_1')?.status, 'met')
  assert.equal(done.edges.filter((e) => e.kind === 'goal').length, 1)
})

test('fact_meta merges onto the matching fact nodes only', () => {
  const graph = buildGraph(
    [fact('f_meta'), fact('f_plain')],
    [],
    [],
    undefined,
    [],
    {},
    '',
    [],
    [],
    [],
    [
      {
        id: 'f_meta',
        needs_review: true,
        reviewed_claim_version: 2,
        evidence_count: 3,
        ref_count: 1,
      },
      // A meta row for a fact that is not on this page is ignored.
      {
        id: 'f_elsewhere',
        needs_review: false,
        reviewed_claim_version: 0,
        evidence_count: 0,
        ref_count: 0,
      },
    ],
  )
  const meta = graph.byId.get('f:f_meta')
  assert.equal(meta?.needsReview, true)
  assert.equal(meta?.reviewedClaimVersion, 2)
  assert.equal(meta?.evidenceCount, 3)
  assert.equal(meta?.refCount, 1)
  assert.equal(graph.byId.get('f:f_plain')?.needsReview, undefined)
  assert.equal(graph.byId.has('f:f_elsewhere'), false)
})

test('focusPath uses the model indexes and agrees with the minimal-object fallback', () => {
  const graph = buildGraph(
    [
      fact('f_out', { intent_id: 'in_1' }),
      fact('f_later', { intent_id: 'in_2' }),
    ],
    [
      intent({ id: 'in_1' }),
      intent({ id: 'in_2', from_fact_ids: ['f_out'], created_at: '2026-11-11T01:00:00Z' }),
    ],
    [],
  )
  // The real model passes the indexes; a small caller passes only nodes/edges.
  const minimal = { nodes: graph.nodes, edges: graph.edges }
  const candidates: Array<string | null> = [
    null,
    'f:f_out',
    's:in_1',
    'g:g_missing',
    graph.edges[0]?.id ?? '',
    'f:missing',
  ]
  for (const id of candidates) {
    const viaIndex = focusPath(graph, id)
    const viaFallback = focusPath(minimal, id)
    assert.deepEqual(viaIndex, viaFallback, `focusPath(${id}) must not depend on how the model was built`)
  }
  const chain = focusPath(graph, 's:in_1')
  assert.ok(chain?.has('p:project'), 'the path reaches the brief')
  assert.ok(chain?.has('f:f_out'), 'the produced fact is a descendant')
  // An unknown id focuses nothing.
  assert.equal(focusPath(graph, 'f:missing'), null)
})
