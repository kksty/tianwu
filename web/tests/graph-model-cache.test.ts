// The F13 regression (docs/35 §4 / docs/handoff §5 F13): the graph model is
// cached by a signature, so the signature has to cover every field the map and
// its inspectors actually render — not just row identities and status. These
// tests pin the two halves of the rule:
//   * a correction that leaves status unchanged (title, summary/claim version,
//     goal note/from, review state) must rebuild the model;
//   * a poll that returns the same data must reuse the same model object, or the
//     canvas re-lays out and the operator's pan/zoom jump on every poll.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  defaultGraphFilters,
  graphModelSignature,
  resolveGraphModelMemo,
  type GraphModelSource,
  type ResolvedGraphModel,
} from '../src/lib/graph-model-cache.ts'
import type { GraphRead } from '../src/lib/api.ts'
import type { Fact, Goal, Intent } from '../src/lib/types.ts'

const NOW = Date.parse('2026-11-12T12:00:00Z')

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
    created_at: '2026-11-12T00:00:00Z',
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
    created_at: '2026-11-12T00:00:00Z',
    updated_at: '2026-11-12T00:00:00Z',
    ...over,
  }
}

function goal(over: Partial<Goal> = {}): Goal {
  return {
    id: 'g_1',
    statement: '拿到域管',
    met: true,
    created_at: '2026-11-12T00:00:00Z',
    ...over,
  }
}

/** One fresh object graph per call, like a poll response: same content, new
 *  arrays/objects. Reference identity must not be what keeps the cache warm. */
function source(
  facts: Fact[],
  intents: Intent[] = [intent()],
  goals: Goal[] = [],
  extra: Partial<GraphRead> = {},
): GraphModelSource {
  const data: GraphRead = {
    facts,
    intents,
    goals,
    edges: [],
    boundary: [],
    has_more: false,
    total: facts.length + intents.length + goals.length,
    snapshot_version: { epoch: 'e1', seq: 1 },
    ...extra,
  }
  return { data, runs: [], profileMeta: {}, defaultModel: 'model-a', selected: null }
}

function resolve(prev: ResolvedGraphModel | null, src: GraphModelSource, focus = false): ResolvedGraphModel {
  return resolveGraphModelMemo(
    prev?.base ?? null,
    prev?.chain ?? null,
    src,
    { ...defaultGraphFilters(), focusOnly: focus },
    NOW,
  )
}

// --- unchanged poll -------------------------------------------------------

test('an identical poll produces the same signature and reuses the same model object', () => {
  const facts = [fact('f_1', { title: '域控', summary: '普通域用户可读' }), fact('f_2')]
  const goals = [goal({ note: '证据 e_1 复现', from: ['f_1'], evidence_id: 'e_1' })]
  const first = resolve(null, source(facts, [intent()], goals))
  // A later poll: fresh object graph, identical content.
  const second = resolve(first, source(
    [fact('f_1', { title: '域控', summary: '普通域用户可读' }), fact('f_2')],
    [intent()],
    [goal({ note: '证据 e_1 复现', from: ['f_1'], evidence_id: 'e_1' })],
  ))
  assert.equal(second.base.sig, first.base.sig)
  assert.equal(second.base.model, first.base.model, 'no re-layout on an unchanged poll')
  assert.equal(second.model, first.base.model)
})

test('the signature is pure over content: a second call on the same data is identical', () => {
  const src = source([fact('f_1', { title: '标题' })], [intent({ title: '方向' })], [goal({ from: ['f_1'] })])
  const inputs = { facts: src.data!.facts, intents: src.data!.intents, goals: src.data!.goals }
  assert.equal(graphModelSignature(src, inputs), graphModelSignature(src, inputs))
})

test('changing the initiator refreshes task details without changing task ownership', () => {
  const first = resolve(null, source([], [intent({ parent_intent_id: 'in_parent', created_by_participant_id: 'commander' })]))
  const second = resolve(first, source([], [intent({ parent_intent_id: 'in_parent', created_by_participant_id: 'pt_in_parent' })]))
  assert.notEqual(second.base.sig, first.base.sig)
  assert.notEqual(second.base.model, first.base.model)
})

// --- fact corrections that keep the same status --------------------------

test('a corrected fact title rebuilds the model although the status did not change', () => {
  const first = resolve(null, source([fact('f_1', { title: '旧标题', status: 'unverified' })]))
  const second = resolve(first, source([fact('f_1', { title: '更正后的标题', status: 'unverified' })]))
  assert.notEqual(second.base.sig, first.base.sig)
  assert.notEqual(second.base.model, first.base.model, 'a retitled fact must rebuild the model')
  assert.equal(second.base.model.byId.get('f:f_1')?.label, '更正后的标题')
})

test('a corrected summary (claim version bumped) rebuilds the model with the same status', () => {
  const before = fact('f_1', { title: '暴露面', summary: '旧摘要', claim_version: 1, status: 'unverified' })
  const after = fact('f_1', { title: '暴露面', summary: '更正后的摘要', claim_version: 2, status: 'unverified' })
  const first = resolve(null, source([before]))
  const second = resolve(first, source([after]))
  assert.notEqual(second.base.sig, first.base.sig)
  assert.notEqual(second.base.model, first.base.model)
  assert.equal(second.base.model.byId.get('f:f_1')?.sub, '更正后的摘要')
  assert.equal(second.base.model.factsById.get('f_1')?.claim_version, 2)
})

test('a claim version bump alone rebuilds: the version is a real record identity', () => {
  const first = resolve(null, source([fact('f_1', { claim_version: 1 })]))
  const second = resolve(first, source([fact('f_1', { claim_version: 2 })]))
  assert.notEqual(second.base.sig, first.base.sig, 'a new assertion version is not a cache hit')
})

test('a legacy fact without a claim version falls back to hashing its claim text', () => {
  const legacy = (summary: string): Fact =>
    fact('f_1', { summary, claim_version: 0, title: '旧事实' })
  const first = resolve(null, source([legacy('旧摘要')]))
  const second = resolve(first, source([legacy('旧摘要')]))
  assert.equal(second.base.model, first.base.model, 'same legacy text is still a cache hit')
  const third = resolve(second, source([legacy('旧摘要（更正）')]))
  assert.notEqual(third.base.sig, second.base.sig)
  assert.notEqual(third.base.model, second.base.model)
})

test('a review-state change (fact_meta) rebuilds and reaches the fact node', () => {
  const first = resolve(null, source([fact('f_1')]))
  const second = resolve(first, source([fact('f_1')], [intent()], [], {
    fact_meta: [{ id: 'f_1', needs_review: true, reviewed_claim_version: 1, evidence_count: 2, ref_count: 1 }],
  }))
  assert.notEqual(second.base.sig, first.base.sig)
  const node = second.base.model.byId.get('f:f_1')
  assert.equal(node?.needsReview, true)
  assert.equal(node?.evidenceCount, 2)
  assert.equal(node?.refCount, 1)
})

test('an identical repeat of the reviewed fact_meta keeps the model warm', () => {
  const withMeta = () =>
    source([fact('f_1')], [intent()], [], {
      fact_meta: [{ id: 'f_1', needs_review: false, reviewed_claim_version: 1, evidence_count: 0, ref_count: 0 }],
    })
  const first = resolve(null, withMeta())
  const second = resolve(first, withMeta())
  assert.equal(second.base.model, first.base.model)
})

// --- goal corrections ----------------------------------------------------

test('a goal note change rebuilds the model although met did not change', () => {
  const sealed = (note: string, from: string[]) =>
    goal({ note, from, evidence_id: 'e_1', completed_at: '2026-11-12T01:00:00Z' })
  const first = resolve(null, source([fact('f_1')], [intent()], [sealed('理由 A', ['f_1'])]))
  const second = resolve(first, source([fact('f_1')], [intent()], [sealed('理由 B', ['f_1'])]))
  assert.notEqual(second.base.sig, first.base.sig)
  assert.notEqual(second.base.model, first.base.model)
  assert.equal(second.base.model.byId.get('g:g_1')?.meta?.goal?.note, '理由 B')
})

test('a goal from-list change rebuilds the model and keeps the goal listed as met', () => {
  const sealed = (from: string[]) => goal({ note: '依据变化', from })
  const first = resolve(null, source([fact('f_1'), fact('f_2')], [intent()], [sealed(['f_1'])]))
  const second = resolve(first, source([fact('f_1'), fact('f_2')], [intent()], [sealed(['f_1', 'f_2'])]))
  assert.notEqual(second.base.sig, first.base.sig)
  assert.notEqual(second.base.model, first.base.model)
  const node = second.base.model.byId.get('g:g_1')
  assert.equal(node?.status, 'met')
  assert.deepEqual(node?.meta?.goal?.from, ['f_1', 'f_2'])
})

// --- server omission rows (details) ---------------------------------------

test('a details row that omits a direction field rebuilds the model', () => {
  const first = resolve(null, source([], [intent()]))
  assert.ok(
    first.base.model.edges.some((e) => e.source === 'p:project' && e.target === 's:in_task' && e.kind === 'premise'),
    'without an omission row a direction with no premise starts at the brief',
  )
  const second = resolve(first, source([], [intent()], [], {
    details: [{ id: 'intent:in_task', query: 'intent:in_task', omitted: ['from_fact_ids'] }],
  }))
  assert.notEqual(second.base.sig, first.base.sig, 'omitted fields are part of what the model reads')
  assert.notEqual(second.base.model, first.base.model)
  // sourcesOf returns only the context list when the server omitted the premise
  // field: an empty selection no longer falls back to the project brief, and the
  // model must not invent a premise edge either way.
  assert.ok(
    !second.base.model.edges.some((e) => e.target === 's:in_task' && e.kind === 'premise'),
    'the omitted premise list is not guessed from the project brief',
  )
})

// --- boundary state is rendered state too --------------------------------
test('a boundary reference changing from not_loaded to missing rebuilds with an honest label', () => {
  const child = intent({ id: 'in_child', from_fact_ids: ['old'] })
  const page = (state: string): GraphRead => ({
    facts: [],
    intents: [child],
    goals: [],
    edges: [],
    boundary: [{ id: 'fact:old', state }],
    has_more: false,
    total: 1,
    snapshot_version: { epoch: 'e1', seq: 1 },
  })
  const first = resolve(null, { ...source([], [child]), data: page('not_loaded') })
  const second = resolve(first, { ...source([], [child]), data: page('missing') })
  assert.notEqual(second.base.sig, first.base.sig, 'the boundary label state is part of the key')
  assert.notEqual(second.base.model, first.base.model)
  assert.match(second.base.model.byId.get('f:old')?.label ?? '', /引用缺失/)
  assert.equal(second.base.model.byId.get('f:old')?.boundaryState, 'missing')
})

// --- focus-only cache ----------------------------------------------------

test('focusOnly reuses its chain for the same base signature and rebuilds when it changes', () => {
  const src = (title: string) => ({
    ...source([fact('f_1', { title })], [intent()]),
    selected: 's:in_task',
  })
  const first = resolve(null, src('方向标题'), true)
  assert.ok(first.chain, 'the chain model is built while focusOnly is on')
  const again = resolve(first, src('方向标题'), true)
  assert.equal(again.chain?.model, first.chain?.model)
  const changed = resolve(again, src('方向标题（更正）'), true)
  assert.notEqual(changed.chain?.model, again.chain?.model)
  assert.equal(changed.model, changed.chain?.model)
})
