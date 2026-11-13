import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  evidenceVersionHint,
  evidenceVersionLabel,
  evidenceVersionRows,
  latestExecutorRun,
  runEvidenceFactTitle,
  versionSetState,
} from '../src/lib/run-evidence.ts'
import type { Fact, Run } from '../src/lib/types.ts'

function run(over: Partial<Run> = {}): Run {
  return {
    id: 'r_example',
    intent_id: 'in_example',
    node: 'local',
    status: 'done',
    started_at: '2026-11-11T00:00:00Z',
    events: 0,
    declared_facts: 0,
    ...over,
  }
}

const facts: Pick<Fact, 'id' | 'title'>[] = [
  { id: 'f_premise', title: '前提事实' },
  { id: 'f_context', title: '参考事实' },
]

test('premise claim versions and context record versions stay different statements', () => {
  const saved = run({
    premise_claim_versions: { f_premise: 3 },
    context_fact_versions: { f_context: 7 },
  })
  const premise = evidenceVersionRows(saved.premise_claim_versions, 'premise')
  const context = evidenceVersionRows(saved.context_fact_versions, 'context')
  assert.deepEqual(premise, [{ id: 'f_premise', version: 3, kind: 'premise' }])
  assert.deepEqual(context, [{ id: 'f_context', version: 7, kind: 'context' }])
  // Same number, different meaning: assertion version vs ledger record version.
  assert.equal(evidenceVersionLabel('premise', 3), '断言 v3')
  assert.equal(evidenceVersionLabel('context', 7), '记录 r7')
  assert.notEqual(evidenceVersionHint('premise'), evidenceVersionHint('context'))
})

test('an absent version map is not the same as a recorded empty selection', () => {
  assert.equal(versionSetState(undefined), 'not_recorded')
  assert.equal(versionSetState({}), 'empty')
  assert.equal(versionSetState({ f_premise: 1 }), 'rows')
  assert.deepEqual(evidenceVersionRows(undefined, 'premise'), [])
})

test('rows are stable and keep a zero/unrecorded version honest instead of inventing v1', () => {
  const rows = evidenceVersionRows({ f_b: 2, f_a: 0 }, 'premise')
  assert.deepEqual(rows.map((row) => row.id), ['f_a', 'f_b'])
  assert.equal(rows[0].version, 0)
  assert.equal(evidenceVersionLabel('premise', 0), '未记录')
  assert.equal(evidenceVersionLabel('context', 0), '未记录')
})

test('the latest executor attempt is chosen, not a verifier or report support run', () => {
  const runs = [
    run({ id: 'r_old', intent_id: 'in_task', started_at: '2026-11-10T00:00:00Z' }),
    run({ id: 'r_new', intent_id: 'in_task', started_at: '2026-11-12T00:00:00Z' }),
    // A verifier run for the same intent is newer but proves facts; it is not
    // the direction's dispatch evidence.
    run({ id: 'r_verifier', intent_id: 'in_task', purpose: 'verifier', started_at: '2026-11-13T00:00:00Z' }),
    run({ id: 'r_other', intent_id: 'in_other', started_at: '2026-11-14T00:00:00Z' }),
  ]
  assert.equal(latestExecutorRun(runs, 'in_task')?.id, 'r_new')
  assert.equal(latestExecutorRun(runs, 'in_missing'), undefined)
})

test('fact titles resolve by id and fall back honestly when the fact is not loaded', () => {
  assert.equal(runEvidenceFactTitle(facts, 'f_premise'), '前提事实')
  assert.equal(runEvidenceFactTitle(facts, 'f_gone'), '事实不在当前列表')
})
