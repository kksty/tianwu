import { test } from 'node:test'
import assert from 'node:assert/strict'
import type { GraphRead } from '../src/lib/api.ts'
import type { Fact, Goal, Intent } from '../src/lib/types.ts'
import {
  GRAPH_BATCH_MAX_NODES,
  GRAPH_BATCH_MAX_PAGES,
  batchComplete,
  graphHasRoot,
  graphNodeCount,
  graphPageKey,
  mergeGraphPages,
} from '../src/lib/graph-pages.ts'

function fact(id: string, over: Partial<Fact> = {}): Fact {
  return {
    id,
    intent_id: 'in_1',
    kind: 'note',
    title: id,
    summary: '',
    confidence: 'observed',
    status: 'unverified',
    created_at: '2026-11-11T00:00:00Z',
    ...over,
  }
}

function intent(id: string): Intent {
  return {
    id,
    title: id,
    description: '',
    status: 'running',
    attempts: 1,
    created_at: '2026-11-11T00:00:00Z',
    updated_at: '2026-11-11T00:00:00Z',
  }
}

function goal(id: string): Goal {
  return {
    id,
    statement: id,
    met: true,
    created_at: '2026-11-11T00:00:00Z',
  }
}

function page(over: Partial<GraphRead> = {}): GraphRead {
  return {
    facts: [],
    intents: [],
    goals: [],
    edges: [],
    boundary: [],
    has_more: false,
    total: 0,
    snapshot_version: { epoch: 'epoch-1', seq: 7 },
    ...over,
  }
}

test('mergeGraphPages merges the four streams by identity and lets the later page win', () => {
  const a = page({
    facts: [fact('f1', { title: 'old' }), fact('f2')],
    intents: [intent('in_1')],
    goals: [goal('g_1')],
    edges: [{ source: 'f:f1', target: 's:in_1', kind: 'reference' }],
    boundary: [{ id: 'fact:gone', state: 'missing' }],
    details: [{ id: 'fact:f1', query: 'q', omitted: ['doc'] }],
    fact_meta: [
      { id: 'f1', needs_review: false, reviewed_claim_version: 0, evidence_count: 0, ref_count: 0 },
    ],
    next_cursor: 'c1',
    has_more: true,
    total: 2,
  })
  const b = page({
    facts: [fact('f1', { title: 'new' }), fact('f3')],
    intents: [intent('in_1'), intent('in_2')],
    goals: [],
    edges: [
      // Same endpoints, different relation: both must survive.
      { source: 'f:f1', target: 's:in_1', kind: 'pin' },
      // Same relation: refreshed by the later page, not duplicated.
      { source: 'f:f1', target: 's:in_1', kind: 'reference' },
    ],
    boundary: [{ id: 'fact:gone', state: 'rejected' }],
    details: [{ id: 'fact:f3', query: 'q3', omitted: [] }],
    fact_meta: [
      { id: 'f1', needs_review: true, reviewed_claim_version: 3, evidence_count: 2, ref_count: 1 },
    ],
    next_cursor: 'c2',
    has_more: false,
    total: 3,
    snapshot_version: { epoch: 'epoch-1', seq: 7 },
  })
  const merged = mergeGraphPages(a, b)

  assert.deepEqual(merged.facts.map((f) => f.id), ['f1', 'f2', 'f3'])
  assert.equal(merged.facts[0].title, 'new', 'the later page refreshes a row')
  assert.deepEqual(merged.intents.map((i) => i.id), ['in_1', 'in_2'])
  assert.deepEqual(merged.goals.map((g) => g.id), ['g_1'])
  assert.deepEqual(
    merged.edges.map((e) => e.kind).sort(),
    ['pin', 'reference'],
    'edges dedupe by source/target/kind',
  )
  assert.equal(merged.edges.find((e) => e.kind === 'reference')?.source, 'f:f1')
  assert.deepEqual(merged.boundary, [{ id: 'fact:gone', state: 'rejected' }])
  assert.deepEqual(merged.details?.map((d) => d.id), ['fact:f1', 'fact:f3'])
  assert.deepEqual(merged.fact_meta, [
    { id: 'f1', needs_review: true, reviewed_claim_version: 3, evidence_count: 2, ref_count: 1 },
  ])
  // The envelope is the later page's: that is where reading continues.
  assert.equal(merged.has_more, false)
  assert.equal(merged.next_cursor, 'c2')
  assert.equal(merged.total, 3)
  assert.equal(graphPageKey(merged), 'epoch-1:7')
  // 3 facts + 2 intents + 1 goal; steps are derived, not page rows.
  assert.equal(graphNodeCount(merged), 6)
  // Input pages are not mutated.
  assert.equal(a.facts[0].title, 'old')
  assert.equal(a.edges.length, 1)
})

test('mergeGraphPages keeps totals the later page omitted (older server)', () => {
  const a = page({ intents_total: 9, facts_total: 40, goals_total: 2, edges_total: 50, boundary_total: 3 })
  const b = page({ intents_total: 9, facts_total: undefined })
  const merged = mergeGraphPages(a, b)
  assert.equal(merged.intents_total, 9)
  assert.equal(merged.facts_total, 40)
  assert.equal(merged.goals_total, 2)
  assert.equal(merged.edges_total, 50)
  assert.equal(merged.boundary_total, 3)
})

test('graphPageKey identifies one fixed snapshot', () => {
  assert.equal(graphPageKey(page()), 'epoch-1:7')
  assert.notEqual(
    graphPageKey(page()),
    graphPageKey(page({ snapshot_version: { epoch: 'epoch-2', seq: 7 } })),
  )
})

test('batchComplete stops only for an honest reason', () => {
  assert.equal(batchComplete(page({ has_more: false }), 0), false, 'no page taken yet')
  assert.equal(batchComplete(page({ has_more: false }), 1), true, 'snapshot exhausted')
  assert.equal(batchComplete(page({ has_more: true }), 1), false, 'more to read')
  assert.equal(
    batchComplete(
      page({
        has_more: true,
        intents_total: 2,
        intents: [intent('a'), intent('b')],
        facts: [fact('f1')],
      }),
      1,
    ),
    true,
    'every intent is on screen and the batch already carries facts',
  )
  // Stopping on "all intents arrived" with zero facts would make buildGraph draw
  // every premise as a boundary node: the first screen must keep reading.
  assert.equal(
    batchComplete(
      page({ has_more: true, intents_total: 2, intents: [intent('a'), intent('b')], facts_total: 30 }),
      1,
    ),
    false,
    'intents complete but no fact yet: keep reading',
  )
  assert.equal(
    batchComplete(
      page({ has_more: true, intents_total: 2, intents: [intent('a'), intent('b')], facts_total: 0 }),
      1,
    ),
    true,
    'a project with no facts has nothing more to wait for',
  )
  assert.equal(
    batchComplete(page({ has_more: true, intents_total: 5, intents: [intent('a')] }), 1),
    false,
    'intents are still missing',
  )
  assert.equal(
    batchComplete(page({ has_more: true }), GRAPH_BATCH_MAX_PAGES),
    true,
    'the page budget is spent',
  )
  const many = Array.from({ length: GRAPH_BATCH_MAX_NODES }, (_, i) => fact(`f${i}`))
  assert.equal(batchComplete(page({ has_more: true, facts: many }), 1), true, 'the node budget is spent')
  const under = many.slice(1)
  assert.equal(batchComplete(page({ has_more: true, facts: under }), 1), false, 'one node under budget')
})

test('graphHasRoot locates the deep-link target without guessing', () => {
  const p = page({
    facts: [fact('f1')],
    intents: [intent('in_1')],
    goals: [goal('g_1')],
    boundary: [{ id: 'fact:old', state: 'missing' }],
  })
  assert.equal(graphHasRoot(p, 'fact:f1'), true)
  assert.equal(graphHasRoot(p, 'intent:in_1'), true)
  assert.equal(graphHasRoot(p, 'goal:g_1'), true)
  assert.equal(graphHasRoot(p, 'fact:old'), true, 'a boundary root is still a root')
  assert.equal(graphHasRoot(p, 'fact:f2'), false)
  assert.equal(graphHasRoot(p, ''), false)
  assert.equal(graphHasRoot(p, 'f:1'), false, 'wire roots are not node ids')
})
