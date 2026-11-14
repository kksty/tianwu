// The explicit "追加目标并继续" action (internal/api/goal_continue.go): one
// idempotency key per submitted draft, kept through retries, replaced when the
// content changes; the exact server error copy plus the retry/human distinction;
// and the success receipt. Pure helpers only — the dialog wiring is React.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  appendGoalFailure,
  appendGoalFingerprint,
  appendGoalKeyFor,
  appendGoalOutcome,
  appendGoalRequest,
  pauseKindLabel,
  randomAppendGoalKey,
  loadPendingAppend,
  savePendingAppend,
  type AppendGoalDraft,
} from '../src/lib/append-goal.ts'
import { ApiError } from '../src/lib/api.ts'
import type { AppendGoalResult } from '../src/lib/types.ts'

function draft(over: Partial<AppendGoalDraft> = {}): AppendGoalDraft {
  return { start: '', statement: '证明后台存在未授权访问', ...over }
}

test('a retry reuses the same idempotency key; editing the content makes a new one', () => {
  let generated = 0
  const generate = () => `k${++generated}`
  const first = appendGoalKeyFor(null, draft(), generate)
  assert.equal(first.key, 'k1')
  assert.equal(generated, 1)
  // Retrying the same (even whitespace-padded) draft is the same request.
  const retry = appendGoalKeyFor(first, draft({ statement: '  证明后台存在未授权访问  ' }), generate)
  assert.equal(retry.key, 'k1')
  assert.equal(generated, 1, 'a retry must not mint a new key')
  // Editing the endpoint is a NEW goal; reusing the key would answer 409
  // goal_idempotency_conflict server-side.
  const edited = appendGoalKeyFor(retry, draft({ statement: '证明后台存在未授权访问（补充条件）' }), generate)
  assert.equal(edited.key, 'k2')
  assert.notEqual(edited.fingerprint, retry.fingerprint)
  // The start point is part of the submitted content too.
  const started = appendGoalKeyFor(edited, draft({ statement: '证明后台存在未授权访问（补充条件）', start: '已有测试账号' }), generate)
  assert.equal(started.key, 'k3')
})

test('the request body is trimmed and omits an empty start', () => {
  assert.deepEqual(appendGoalRequest(draft({ statement: '  终点  ', start: '   ' }), 'key-1'), {
    statement: '终点',
    idempotency_key: 'key-1',
  })
  assert.deepEqual(
    appendGoalRequest(draft({ statement: '终点', start: ' 起点状态 ' }), 'key-1'),
    { statement: '终点', start: '起点状态', idempotency_key: 'key-1' },
  )
  // Fingerprint follows the same normalisation.
  assert.equal(appendGoalFingerprint(draft({ statement: ' 目标 ' })), appendGoalFingerprint(draft({ statement: '目标' })))
})

test('random keys are prefixed, hex and unique', () => {
  const a = randomAppendGoalKey()
  const b = randomAppendGoalKey()
  assert.match(a, /^ag_[0-9a-f]{32}$/)
  assert.notEqual(a, b)
})

test('a provider fault failure needs a human and keeps the server reason', () => {
  const failure = appendGoalFailure(
    new ApiError(409, '项目因模型/凭据故障而暂停：先修复 key 或切换 profile', 'project_fault_paused', {
      needs_human: true,
      pause_reason: 'key 无效',
      retryable: false,
    }),
  )
  assert.equal(failure.code, 'project_fault_paused')
  assert.match(failure.message, /模型服务故障.*暂停/)
  assert.equal(failure.needsHuman, true)
  assert.equal(failure.pauseReason, 'key 无效')
  assert.equal(failure.retryable, false)
  assert.match(failure.hint ?? '', /安全重试/)
})

test('a failed resume is retryable with the same key and the goal already exists', () => {
  const failure = appendGoalFailure(
    new ApiError(500, '目标已追加，但无法保存继续状态，项目保持暂停', 'resume_failed', {
      retryable: true,
      pause_reason: 'manifest write failed',
    }),
  )
  assert.equal(failure.code, 'resume_failed')
  assert.equal(failure.retryable, true)
  assert.equal(failure.needsHuman, false)
  assert.equal(failure.pauseReason, 'manifest write failed')
  assert.match(failure.hint ?? '', /同一个目标/)
})

test('an idempotency conflict is not retried with the same content', () => {
  const failure = appendGoalFailure(
    new ApiError(409, 'idempotency_key 已对应另一个目标', 'goal_idempotency_conflict'),
  )
  assert.equal(failure.code, 'goal_idempotency_conflict')
  assert.equal(failure.retryable, false)
  assert.match(failure.hint ?? '', /核对内容/)
  // The server's message is the exact copy shown to the operator.
  assert.match(failure.message, /上次提交不同/)
})

test('validation failures and unknown failures stay visible, not swallowed', () => {
  const invalid = appendGoalFailure(new ApiError(400, 'statement 不能为空：目标要能判断真假', 'goal_invalid'))
  assert.equal(invalid.message, 'statement 不能为空：目标要能判断真假')
  assert.equal(invalid.needsHuman, false)
  assert.equal(invalid.retryable, false)
  const plain = appendGoalFailure(new Error('网络中断'))
  assert.equal(plain.message, '网络中断')
  assert.equal(plain.code, '')
  assert.equal(plain.retryable, false)
})

test('the success receipt distinguishes duplicate, resumed and woken outcomes', () => {
  const base: AppendGoalResult = {
    goal: { id: 'g_new', statement: '新目标', met: false, created_at: '2026-11-12T00:00:00Z' },
    created: true,
    resumed: true,
    resumed_from: 'completion_standby',
    decider_woken: true,
    paused_before: true,
    pause_kind_before: 'completion_standby',
    paused_after: false,
    pause_kind_after: 'running',
    retryable: false,
    note: '已追加目标并显式恢复派发；旧目标的完成收据保持不变。',
  }
  const resumed = appendGoalOutcome(base)
  assert.equal(resumed.title, '目标已追加')
  assert.equal(resumed.resumed, true)
  assert.equal(resumed.resumedFrom, 'completion_standby')
  assert.equal(resumed.deciderWoken, true)
  assert.equal(resumed.pausedAfter, false)
  assert.equal(resumed.note, base.note)

  const duplicate = appendGoalOutcome({ ...base, created: false, resumed: false, resumed_from: '', decider_woken: false })
  assert.equal(duplicate.title, '目标已存在（未重复追加）')
  assert.equal(duplicate.resumed, false)

  const stillPaused = appendGoalOutcome({ ...base, resumed: false, paused_after: true, pause_kind_after: 'provider_fault' })
  assert.equal(stillPaused.pausedAfter, true)
  assert.equal(pauseKindLabel(stillPaused.pauseKindAfter), '模型/凭据故障暂停')
})

test('pause kinds map to explicit labels and unknown codes stay literal', () => {
  assert.equal(pauseKindLabel('completion_standby'), '完成待命')
  assert.equal(pauseKindLabel('manual_pause'), '人工暂停')
  assert.equal(pauseKindLabel('commander_standby'), '指挥官请求停止')
  assert.equal(pauseKindLabel('waiting_human'), '等待人工')
  assert.equal(pauseKindLabel('provider_fault'), '模型/凭据故障暂停')
  assert.equal(pauseKindLabel('some_new_state'), 'some_new_state')
  assert.equal(pauseKindLabel(undefined), '未知状态')
})

test('a pending append survives reload, stays project-scoped and clears after confirmation', () => {
  const values = new Map<string, string>()
  const storage = { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => { values.set(key, value) }, removeItem: (key: string) => { values.delete(key) } }
  const submitted = draft()
  const identity = appendGoalKeyFor(null, submitted, () => 'stable-operation')
  savePendingAppend(storage, 'project-a', { draft: submitted, key: identity })
  const restored = loadPendingAppend(storage, 'project-a')!
  assert.equal(appendGoalKeyFor(restored.key, restored.draft, () => { throw new Error('retry generated a new operation') }).key, identity.key)
  assert.equal(loadPendingAppend(storage, 'project-b'), null)
  savePendingAppend(storage, 'project-a', null)
  assert.equal(loadPendingAppend(storage, 'project-a'), null)
})

test('a partially successful append exposes the created goal for a list refresh', () => {
  const failure = appendGoalFailure(new ApiError(500, 'resume failed', 'resume_failed', { goal: { id: 'g_saved' }, retryable: true }))
  assert.equal(failure.goalId, 'g_saved')
  assert.equal(failure.retryable, true)
  assert.doesNotMatch(failure.hint ?? '', /idempotency_key/)
})
