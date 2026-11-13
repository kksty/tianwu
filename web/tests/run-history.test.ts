import { test } from 'node:test'
import assert from 'node:assert/strict'
import { groupRunHistory, participantsByIntent, runExecutor, selectRunHistory } from '../src/lib/run-history.ts'
import type { Intent, Participant, Run } from '../src/lib/types.ts'

function run(id: string, seconds: number, fields: Partial<Run> = {}): Run {
  return {
    id, intent_id: 'intent-a', node: 'local', status: 'done', events: 0, declared_facts: 0,
    started_at: new Date(Date.UTC(2026, 10, 10, 0, 0, seconds)).toISOString(),
    ...fields,
  }
}

test('executor restarts and continuations share an intent history across sessions and models', () => {
  const old = run('old', 0, { session_id: 'session-old', generation: 1 })
  const latest = run('latest', 20, { session_id: 'session-new', generation: 2, model: 'new-model', continued_from: 'old' })
  const other = run('other', 10, { intent_id: 'intent-b' })
  const input = [old, latest, other]
  const groups = groupRunHistory(input)
  assert.deepEqual(groups.map(g => g.runs.map(r => r.id)), [['latest', 'old'], ['other']])
  assert.deepEqual(input.map(r => r.id), ['old', 'latest', 'other'])
})

test('commander restarts share one history despite synthetic intent ids; support roles stay separate', () => {
  const groups = groupRunHistory([
    run('commander-old', 0, { purpose: 'commander', intent_id: 'round-1' }),
    run('executor', 1),
    run('commander-new', 5, { purpose: 'commander', intent_id: 'round-2' }),
    run('verifier', 3, { purpose: 'verifier' }),
    run('report', 2, { purpose: 'report' }),
  ])
  assert.equal(groups.length, 4)
  assert.equal(groups[0].key, 'purpose:commander')
  assert.deepEqual(groups[0].runs.map(r => r.id), ['commander-new', 'commander-old'])
})

test('unattributed runs remain individually accessible', () => {
  assert.equal(groupRunHistory([
    run('a', 0, { intent_id: '' }), run('b', 1, { intent_id: '' }),
  ]).length, 2)
})

test('group selection follows the latest on refresh while explicit historical links stay pinned', () => {
  const initial = groupRunHistory([run('old', 0)])
  const refreshed = groupRunHistory([run('old', 0), run('new', 10)])
  const key = initial[0].key
  assert.equal(selectRunHistory(initial, null, null).run?.id, 'old')
  assert.equal(selectRunHistory(refreshed, null, key).run?.id, 'new')
  const pinned = selectRunHistory(refreshed, 'old', 'purpose:commander')
  assert.equal(pinned.run?.id, 'old')
  assert.equal(pinned.group?.key, key)
  const withAnotherExecutor = groupRunHistory([run('old', 0), run('new', 10), run('other', 20, { intent_id: 'intent-b' })])
  assert.equal(selectRunHistory(withAnotherExecutor, null, key).run?.id, 'new')
})

test('missing explicit links never silently display an unrelated run', () => {
  const groups = groupRunHistory([run('a', 0)])
  assert.equal(selectRunHistory(groups, 'missing', null).run, null)
  assert.equal(selectRunHistory(groups, null, 'intent:missing').run, null)
  assert.equal(selectRunHistory([], null, null).run, null)
})

const people: Participant[] = [
  { id: 'commander', kind: 'commander', title: '指挥官', contactable: true },
  { id: 'pt_in_a484fc1d', kind: 'intent', intent_id: 'in_a484fc1d', title: '侦察与资产测绘：portal.example', contactable: false },
]

const intent: Intent = {
  id: 'in_a484fc1d',
  title: '侦察与资产测绘：portal.example',
  description: '',
  status: 'done',
  attempts: 1,
  created_at: '2026-11-11T00:00:00Z',
  updated_at: '2026-11-11T00:00:00Z',
}

test('the runs list names the executor the commander dispatched to, with participant and session ids', () => {
  const executor = runExecutor(
    run('r_fad3362e', 0, { intent_id: 'in_a484fc1d', session_id: '9da4e9bb76501b6b364c7345664e6c5a' }),
    intent,
    participantsByIntent(people),
  )
  assert.equal(executor.kind, 'executor')
  assert.equal(executor.label, '侦察与资产测绘：portal.example')
  assert.equal(executor.participantID, 'pt_in_a484fc1d')
  assert.equal(executor.sessionID, '9da4e9bb76501b6b364c7345664e6c5a')
  assert.equal(executor.detail, '执行者 pt_in_a484fc1d · 会话 9da4e9bb76…')
  // Participants may load after runs: the executor id is still derivable from
  // the intent id, and the missing title falls back honestly.
  const early = runExecutor(run('r2', 0, { intent_id: 'in_a484fc1d', session_id: '' }), undefined, new Map())
  assert.equal(early.participantID, 'pt_in_a484fc1d')
  assert.equal(early.label, 'in_a484fc1d')
  assert.equal(early.detail, '执行者 pt_in_a484fc1d')
})

test('commander and support runs are named by role, not by a synthetic intent', () => {
  const commander = runExecutor(
    run('r_cmd', 0, { purpose: 'commander', intent_id: '', session_id: '194adc7aca2b87015f6d4c5c472b242e' }),
    undefined,
    new Map(),
  )
  assert.equal(commander.kind, 'commander')
  assert.equal(commander.label, '指挥官会话')
  assert.equal(commander.detail, '会话 194adc7aca…')
  const verifier = runExecutor(run('r_v', 0, { purpose: 'verifier' }), undefined, new Map())
  assert.equal(verifier.kind, 'verifier')
  assert.equal(verifier.participantID, '')
  assert.equal(verifier.detail, '复核运行')
})

test('participants are indexed by the intent they execute', () => {
  const byIntent = participantsByIntent(people)
  assert.equal(byIntent.get('in_a484fc1d')?.id, 'pt_in_a484fc1d')
  assert.equal(byIntent.size, 1, 'the commander has no intent and is not an executor entry')
})
