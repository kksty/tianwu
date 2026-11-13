import { test } from 'node:test'
import assert from 'node:assert/strict'
import { buildGraph, edgeKindLabel } from '../src/graph/model.ts'
import type { Fact, Goal, Intent } from '../src/lib/types.ts'

function fact(id: string, over: Partial<Fact> = {}): Fact {
  return {
    id,
    intent_id: 'in_src',
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

test('a hard prerequisite and a reference are drawn as different relations', () => {
  const task = intent({ from_fact_ids: ['f_hard'] })
  const graph = buildGraph(
    [fact('f_hard'), fact('f_ref')],
    [task],
    [],
    undefined,
    [],
    {},
    '',
    [],
    [
      // The ledger repeats the premise edge and adds the reference edge; the
      // console must not collapse them back into one "intent" line.
      { source: 'fact:f_hard', target: 'intent:in_task', kind: 'premise' },
      { source: 'fact:f_ref', target: 'intent:in_task', kind: 'reference' },
    ],
  )
  const hard = graph.edges.filter((e) => e.source === 'f:f_hard' && e.target === 's:in_task')
  assert.equal(hard.length, 1, 'the repeated premise edge is deduped, not doubled')
  assert.equal(hard[0].kind, 'premise')
  const ref = graph.edges.find((e) => e.source === 'f:f_ref' && e.target === 's:in_task')
  assert.equal(ref?.kind, 'reference')
  assert.notEqual(ref?.kind, hard[0].kind)
  // Both edges point at the direction, so both can open its panel.
  assert.equal(hard[0].intent?.id, 'in_task')
  assert.equal(ref?.intent?.id, 'in_task')
})

test('delegation stays the spawn lineage with the parent named', () => {
  const parent = intent({ id: 'in_parent', title: 'parent', status: 'waiting' })
  const child = intent({ id: 'in_child', title: 'child', parent_intent_id: 'in_parent' })
  const graph = buildGraph(
    [],
    [parent, child],
    [],
    undefined,
    [],
    {},
    '',
    [],
    [{ source: 'intent:in_parent', target: 'intent:in_child', kind: 'delegates' }],
  )
  const relative = graph.edges.find((e) => e.source === 's:in_parent' && e.target === 's:in_child')
  assert.equal(relative?.kind, 'spawn')
  assert.equal(relative?.intent?.id, 'in_child')
  assert.equal(relative?.parentIntent, 'in_parent')
  // The anchor edge (parent output → child step) remains the visible branch.
  assert.ok(graph.edges.some((e) => e.target === 's:in_child' && e.kind === 'spawn'))
})

test('a fact that supports a goal is its own relation, not an intent fan', () => {
  const goal: Goal = {
    id: 'g_1',
    statement: '达成目标',
    met: true,
    evidence_id: 'e_evidence',
    from: ['f_goal'],
    created_at: '2026-11-11T00:00:00Z',
  }
  const graph = buildGraph(
    [fact('f_goal')],
    [],
    [goal],
    undefined,
    [],
    {},
    '',
    [],
    [{ source: 'fact:f_goal', target: 'goal:g_1', kind: 'supports' }],
  )
  const support = graph.edges.find((e) => e.source === 'f:f_goal' && e.target === 'g:g_1')
  assert.equal(support?.kind, 'supports')
  assert.equal(support?.intent, undefined, 'a support edge names no direction to open')
})

test('relation labels say what each edge means, and the produced fan stays unlabelled', () => {
  assert.equal(edgeKindLabel('premise'), '必要前提')
  assert.equal(edgeKindLabel('reference'), '引用')
  assert.equal(edgeKindLabel('pin'), '固定')
  assert.equal(edgeKindLabel('supports'), '支持目标')
  // Goal proof has two spellings: the ledger's supports edge and the legacy
  // evidence-id line; both must be named so the legend can tell them apart.
  assert.equal(edgeKindLabel('goal'), '目标证据')
  assert.equal(edgeKindLabel('spawn'), '子任务委派')
  assert.equal(edgeKindLabel('brief'), '项目范围')
  assert.equal(edgeKindLabel('host'), '宿主机')
  assert.equal(edgeKindLabel('intent'), '')
})

test('a pinned read is its own relation, not a plain reference', () => {
  const graph = buildGraph(
    [fact('f_ref'), fact('f_pin')],
    [intent({ from_fact_ids: [] })],
    [],
    undefined,
    [],
    {},
    '',
    [],
    [
      { source: 'fact:f_ref', target: 'intent:in_task', kind: 'reference' },
      { source: 'fact:f_pin', target: 'intent:in_task', kind: 'pin' },
    ],
  )
  const ref = graph.edges.find((e) => e.source === 'f:f_ref' && e.target === 's:in_task')
  const pin = graph.edges.find((e) => e.source === 'f:f_pin' && e.target === 's:in_task')
  assert.equal(ref?.kind, 'reference')
  assert.equal(pin?.kind, 'pin')
  assert.notEqual(ref?.kind, pin?.kind)
})

test('an unknown ledger edge kind stays the neutral intent edge it used to be', () => {
  const graph = buildGraph(
    [fact('f_produced')],
    [intent()],
    [],
    undefined,
    [],
    {},
    '',
    [],
    [{ source: 'intent:in_task', target: 'fact:f_produced', kind: 'whatever' }],
  )
  const produced = graph.edges.find((e) => e.source === 's:in_task' && e.target === 'f:f_produced')
  assert.equal(produced?.kind, 'intent')
})
