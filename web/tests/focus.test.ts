import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  FOCUS_LAYERS,
  focusCounts,
  focusFactQueryHref,
  focusLayerHint,
  focusLayerLabel,
  focusReasonLabel,
  focusSearchParams,
  focusSnapshotLabel,
  focusStatusLabel,
  mergeFocusPages,
} from '../src/lib/focus.ts'
import type { FocusFact, FocusPage } from '../src/lib/focus.ts'
import { api } from '../src/lib/api.ts'

function fact(id: string, temperature: FocusFact['temperature'] = 'cold'): FocusFact {
  return {
    id,
    status: 'unverified',
    temperature,
    reasons: [],
    query: `/api/facts/${id}`,
    claim_version: 2,
    record_version: 7,
  }
}

function page(overrides: Partial<FocusPage> = {}): FocusPage {
  return {
    intent_id: 'in_fixture',
    layer: 'cold',
    facts: [],
    hot: 3,
    warm: 2,
    cold: 40,
    index_query: '/api/focus?intent=in_fixture',
    cold_query: '/api/focus?intent=in_fixture&layer=cold',
    fact_query: '/api/facts?intent=in_fixture',
    premises: '.tw/context.json',
    experience_query: '/api/experience?intent=in_fixture',
    has_more: false,
    total: 40,
    snapshot_version: { epoch: 'ledger_abcdefgh', seq: 12 },
    ...overrides,
  }
}

test('focus layer, reason and status labels stay distinct and honest', () => {
  assert.deepEqual(
    FOCUS_LAYERS.map((layer) => layer.id),
    ['focus', 'hot', 'warm', 'cold'],
  )
  assert.equal(focusReasonLabel('required'), '必要前提')
  assert.equal(focusReasonLabel('same_recorded_entity'), '同实体（跨分支）')
  assert.equal(focusReasonLabel('future_reason'), 'future_reason')
  assert.equal(focusStatusLabel('missing'), '缺失')
  assert.equal(focusStatusLabel('rejected'), '已否定')
  assert.notEqual(focusStatusLabel('missing'), focusStatusLabel('rejected'))
  assert.equal(focusLayerLabel('cold'), '冷')
  assert.match(focusLayerHint('cold'), /始终可检索/)
})

test('focus query keeps the default layer out of the URL and carries the cursor', () => {
  const index = focusSearchParams({ intent: 'in_one' })
  assert.equal(index.get('intent'), 'in_one')
  assert.equal(index.get('layer'), null)
  assert.equal(index.get('cursor'), null)
  assert.equal(index.get('limit'), '50')

  const cold = focusSearchParams({ intent: 'in_one', layer: 'cold', cursor: 'cur_1', limit: 4 })
  assert.equal(cold.get('layer'), 'cold')
  assert.equal(cold.get('cursor'), 'cur_1')
  assert.equal(cold.get('limit'), '4')
})

test('focus pagination appends only within one frozen snapshot', () => {
  const first = page({ facts: [fact('f_1', 'hot')], has_more: true, next_cursor: 'cur_1', total: 3 })
  const second = page({ facts: [fact('f_2', 'warm'), fact('f_3')], total: 3 })
  const merged = mergeFocusPages(first, second)
  assert.deepEqual(
    merged.facts.map((row) => row.id),
    ['f_1', 'f_2', 'f_3'],
  )
  assert.equal(merged.has_more, false)

  // A different snapshot is a different list: mixing pages would duplicate or
  // shift rows, so the newest page wins.
  const moved = page({ facts: [fact('f_9')], total: 1, snapshot_version: { epoch: 'ledger_abcdefgh', seq: 13 } })
  assert.deepEqual(
    mergeFocusPages(merged, moved).facts.map((row) => row.id),
    ['f_9'],
  )
})

test('focus rows link to the project-scoped by-id read path', () => {
  assert.equal(focusFactQueryHref('/api/facts/f_1', 'acme'), '/p/acme/api/facts/f_1')
  assert.equal(focusFactQueryHref('/api/facts/f_1', ''), '/api/facts/f_1')
  assert.equal(focusFactQueryHref('', 'acme'), '')
})

test('focus snapshot and counts describe the whole projection, not the page', () => {
  const view = page({ facts: [fact('f_1')] })
  assert.equal(focusSnapshotLabel(view), '快照 abcdefgh #12')
  assert.deepEqual(focusCounts(view), { hot: 3, warm: 2, cold: 40 })
  assert.deepEqual(focusCounts(undefined), { hot: 0, warm: 0, cold: 0 })
})

test('the focus API client reads one page from the project namespace', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  const calls: { url: string; method: string }[] = []
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/fixture/' } } })
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), method: init?.method ?? 'GET' })
    return new Response(JSON.stringify(page()))
  }
  try {
    const result = await api.focus({ intent: 'in_fixture', layer: 'cold', limit: 50 })
    assert.deepEqual(calls, [
      { url: '/p/fixture/api/focus?intent=in_fixture&layer=cold&limit=50', method: 'GET' },
    ])
    assert.equal(result.cold, 40)
    assert.equal(result.premises, '.tw/context.json')
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})
