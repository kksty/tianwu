import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ApiError, api } from '../src/lib/api.ts'
import {
  canSteer,
  directiveErrorMessage,
  directiveFingerprint,
  directiveKeyFor,
  directiveReceipt,
  directiveRequest,
  isDirectionFinished,
  oneShotDirections,
  persistentDirections,
  runningDirectionsHint,
  type DirectiveDraft,
} from '../src/lib/directive.ts'
import type { DirectiveResponse, Intent } from '../src/lib/types.ts'

function draft(over: Partial<DirectiveDraft> = {}): DirectiveDraft {
  return {
    text: '下一步先补 /api 的授权模型',
    persistent: false,
    target: 'commander',
    intentID: '',
    steer: false,
    ...over,
  }
}

const directions = [
  { id: 'in_running', title: '在跑方向', status: 'running' },
  { id: 'in_claimed', title: '认领方向', status: 'claimed' },
  { id: 'in_pending', title: '待办方向', status: 'pending' },
  { id: 'in_waiting', title: '等待方向', status: 'waiting' },
  { id: 'in_done', title: '完成方向', status: 'done' },
  { id: 'in_failed', title: '失败方向', status: 'failed' },
  { id: 'in_parked', title: '停放方向', status: 'parked' },
] as Intent[]

test('the request builder maps target/persistent/steer onto the documented body', () => {
  assert.deepEqual(directiveRequest(draft(), 'key-1'), {
    text: '下一步先补 /api 的授权模型',
    target: 'commander',
    persistent: false,
    steer: false,
    idempotency_key: 'key-1',
  })
  assert.deepEqual(
    directiveRequest(draft({ target: 'intent', intentID: 'in_x', steer: true }), 'key-2'),
    {
      text: '下一步先补 /api 的授权模型',
      target: 'intent',
      intent_id: 'in_x',
      persistent: false,
      steer: true,
      idempotency_key: 'key-2',
    },
  )
  assert.deepEqual(directiveRequest(draft({ persistent: true, target: 'project' }), 'key-3'), {
    text: '下一步先补 /api 的授权模型',
    target: 'project',
    persistent: true,
    steer: false,
    idempotency_key: 'key-3',
  })
  // A persistent instruction is never also a steer, even if a stale draft says so.
  const persistentIntent = directiveRequest(
    draft({ persistent: true, target: 'intent', intentID: 'in_x', steer: true }),
    'key-4',
  )
  assert.equal(persistentIntent.steer, false)
  // Text is trimmed; intent ids are whitespace-normalised.
  assert.equal(directiveRequest(draft({ text: '  hi  ', target: 'intent', intentID: ' in_x ' }), 'k').text, 'hi')
  assert.equal(directiveRequest(draft({ target: 'intent', intentID: ' in_x ' }), 'k').intent_id, 'in_x')
})

test('the idempotency key follows content: same directive reuses it, any change regenerates', () => {
  const first = directiveFingerprint(draft())
  const same = directiveFingerprint(draft({ text: '  下一步先补 /api 的授权模型  ' }))
  assert.equal(first, same, 'whitespace-only edits are the same directive')
  assert.notEqual(first, directiveFingerprint(draft({ text: '换一句' })))
  assert.notEqual(first, directiveFingerprint(draft({ persistent: true, target: 'project' })))

  const kept = directiveKeyFor({ fingerprint: first, key: 'stable-key' }, first, () => 'unused')
  assert.equal(kept.key, 'stable-key')
  let calls = 0
  const regenerated = directiveKeyFor({ fingerprint: first, key: 'stable-key' }, directiveFingerprint(draft({ text: '换一句' })), () => {
    calls++
    return 'new-key'
  })
  assert.equal(calls, 1)
  assert.equal(regenerated.key, 'new-key')
})

test('one-shot directions exclude settled ones; persistent instructions may scope to any', () => {
  assert.deepEqual(
    oneShotDirections(directions).map((intent) => intent.id),
    ['in_running', 'in_claimed', 'in_pending', 'in_waiting'],
  )
  assert.equal(persistentDirections(directions).length, directions.length)
  assert.equal(isDirectionFinished('done'), true)
  assert.equal(isDirectionFinished('parked'), true)
  assert.equal(isDirectionFinished('running'), false)
})

test('steer is only offered for a live direction (running or claimed)', () => {
  assert.equal(canSteer({ status: 'running' }), true)
  assert.equal(canSteer({ status: 'claimed' }), true)
  assert.equal(canSteer({ status: 'pending' }), false)
  assert.equal(canSteer({ status: 'waiting' }), false)
  assert.equal(canSteer({ status: 'done' }), false)
  assert.equal(canSteer(undefined), false)
})

function result(over: Partial<DirectiveResponse>): DirectiveResponse {
  return {
    kind: 'message',
    target: 'commander',
    persistent: false,
    created: true,
    ...over,
  }
}

test('receipts say exactly what was created and what it affects', () => {
  const steer = directiveReceipt(result({ kind: 'steer', target: 'intent', intent_id: 'in_x', run_id: 'r_new' }))
  assert.match(steer, /已换向/)
  assert.match(steer, /r_new/)
  assert.match(steer, /不建立持久指令/)

  const projectInstruction = directiveReceipt(
    result({
      kind: 'instruction',
      target: 'project',
      persistent: true,
      instruction: { id: 'ins_1', text: 'x', version: 3, state: 'active', created_by: 'operator', created_at: '', updated_at: '' },
    }),
  )
  assert.match(projectInstruction, /v3/)
  assert.match(projectInstruction, /整个项目/)
  assert.match(projectInstruction, /直到撤销/)

  const intentInstruction = directiveReceipt(
    result({
      kind: 'instruction',
      target: 'intent',
      persistent: true,
      intent_id: 'in_x',
      instruction: { id: 'ins_2', text: 'x', version: 1, state: 'active', created_by: 'operator', created_at: '', updated_at: '' },
    }),
  )
  assert.match(intentInstruction, /方向 in_x 及其后代/)

  const commanderMessage = directiveReceipt(result({ delivery: 'queued' }))
  assert.match(commanderMessage, /一次性提示已发给指挥官/)
  assert.match(commanderMessage, /已排队/)
  assert.match(commanderMessage, /不会注入后续提示词/)
  assert.doesNotMatch(commanderMessage, /及其后代/)

  const intentMessage = directiveReceipt(
    result({ target: 'intent', intent_id: 'in_x', delivery: 'accepted', created: false }),
  )
  assert.match(intentMessage, /方向 in_x/)
  assert.doesNotMatch(intentMessage, /及其后代/, 'a one-shot message is not inherited by descendants')
  assert.match(intentMessage, /宿主已接收/)
  assert.match(intentMessage, /重复提交/)
})

test('server error codes map to actionable words, not generic failures', () => {
  assert.match(
    directiveErrorMessage(new ApiError(409, 'not running', 'directive_steer_unavailable')),
    /不能换向/,
  )
  assert.match(
    directiveErrorMessage(new ApiError(409, 'finished', 'directive_target_finished')),
    /一次性提示不能复活它/,
  )
  assert.match(
    directiveErrorMessage(new ApiError(404, 'missing', 'directive_target_missing')),
    /方向不存在/,
  )
  assert.match(
    directiveErrorMessage(new ApiError(429, 'full', 'message_backpressure')),
    /队列已满/,
  )
  // A 400 keeps the server's own reason (invalid combination / bad text).
  assert.equal(directiveErrorMessage(new ApiError(400, '一次性提示必须指定某个收件人', 'directive_invalid')), '一次性提示必须指定某个收件人')
  assert.equal(directiveErrorMessage(new Error('网络错误')), '网络错误')
})

test('the composer hint distinguishes running and idle projects', () => {
  assert.match(runningDirectionsHint(2), /在跑执行者 2 个/)
  assert.match(runningDirectionsHint(0), /没有在跑/)
})

test('api.directive posts the exact project-scoped body and returns the result kind', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  let sent: { url?: string; method?: string; body?: unknown } = {}
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: { location: { pathname: '/p/fixture/' } },
  })
  globalThis.fetch = async (url, init) => {
    sent = {
      url: String(url),
      method: init?.method,
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    }
    return new Response(
      JSON.stringify({
        kind: 'steer',
        target: 'intent',
        intent_id: 'in_x',
        persistent: false,
        created: true,
        run_id: 'r_new',
      }),
      { status: 202 },
    )
  }
  try {
    const body = directiveRequest(
      draft({ target: 'intent', intentID: 'in_x', steer: true }),
      'key-9',
    )
    const response = await api.directive(body)
    assert.equal(sent.url, '/p/fixture/api/directives')
    assert.equal(sent.method, 'POST')
    assert.deepEqual(sent.body, body)
    assert.equal(response.kind, 'steer')
    assert.equal(response.run_id, 'r_new')
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})
