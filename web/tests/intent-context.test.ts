import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ApiError } from '../src/lib/api.ts'
import {
  contextErrorMessage,
  contextErrorKind,
  contextFactLabel,
  filterContextFacts,
  formatEntityRefs,
  intentContextSelection,
  parseEntityRefs,
  putIntentContext,
  sameContextSelection,
  toggleContextID,
} from '../src/lib/context.ts'
import type { Intent } from '../src/lib/types.ts'

function intent(over: Partial<Intent> = {}): Intent {
  return {
    id: 'in_example',
    title: '方向',
    description: '',
    status: 'pending',
    attempts: 0,
    context_version: 3,
    context_fact_ids: ['f_ref'],
    pinned_fact_ids: ['f_pin'],
    interest_entity_refs: ['host:10.0.0.5'],
    created_at: '2026-11-11T00:00:00Z',
    updated_at: '2026-11-11T00:00:00Z',
    ...over,
  }
}

test('the editor opens the whole selection with the version it displayed', () => {
  const selection = intentContextSelection(intent())
  assert.deepEqual(selection, {
    version: 3,
    references: ['f_ref'],
    pins: ['f_pin'],
    entities: ['host:10.0.0.5'],
  })
  // A direction created without context starts at version 0 and empty lists.
  assert.deepEqual(intentContextSelection(intent({ context_version: undefined, context_fact_ids: undefined, pinned_fact_ids: undefined, interest_entity_refs: undefined })), {
    version: 0,
    references: [],
    pins: [],
    entities: [],
  })
})

test('dirty detection compares the whole selection, including order', () => {
  const base = intentContextSelection(intent())
  assert.equal(sameContextSelection(base, { ...base }), true)
  assert.equal(
    sameContextSelection(base, { ...base, references: ['f_other'] }),
    false,
    'a changed reference must mark the draft dirty',
  )
  assert.equal(
    sameContextSelection(base, { ...base, references: ['f_other', 'f_ref'] }),
    false,
    'reordering is still an edit; the store keeps the operator order',
  )
  assert.equal(
    sameContextSelection(base, { ...base, entities: [] }),
    false,
    'clearing interest entities is an edit',
  )
})

test('a stale version is a conflict the editor must surface as refresh-and-retry', () => {
  const conflict = new ApiError(409, '任务上下文版本或运行所有权已变化')
  assert.equal(contextErrorKind(conflict), 'conflict')
  assert.equal(contextErrorMessage(conflict), '已被他人修改，刷新后重试')
  // The message never reads as a generic failure: the operator has a next step.
  assert.doesNotMatch(contextErrorMessage(conflict), /HTTP 409/)
})

test('a foreign direction is a boundary denial, not a concurrent edit', () => {
  assert.equal(contextErrorKind(new ApiError(403, '只能更新自己的方向', 'context_denied')), 'forbidden')
  assert.equal(contextErrorMessage(new ApiError(403, '只能更新自己的方向', 'context_denied')), '只能更新自己的方向（边界拒绝）')
})

test('an unknown reference keeps the server reason instead of guessing', () => {
  const invalid = new ApiError(400, '引用事实不存在：f_missing')
  assert.equal(contextErrorKind(invalid), 'invalid')
  assert.equal(contextErrorMessage(invalid), '引用事实不存在：f_missing')
})

test('toggling a fact keeps order and removes only the chosen id', () => {
  assert.deepEqual(toggleContextID(['f_a', 'f_b'], 'f_c'), ['f_a', 'f_b', 'f_c'])
  assert.deepEqual(toggleContextID(['f_a', 'f_b', 'f_c'], 'f_b'), ['f_a', 'f_c'])
})

test('interest entity refs are trimmed, de-duplicated, and accept line or comma input', () => {
  assert.deepEqual(parseEntityRefs('host:10.0.0.5\n host:10.0.0.5 ,\n\nservice:web'), ['host:10.0.0.5', 'service:web'])
  assert.deepEqual(parseEntityRefs('  '), [])
  assert.equal(formatEntityRefs(['host:10.0.0.5', 'service:web']), 'host:10.0.0.5\nservice:web')
})

test('the fact picker matches on id or title and respects its bound', () => {
  const facts = [
    { id: 'f_admin', title: '/admin 越权' },
    { id: 'f_login', title: '登录口弱口令' },
    { id: 'f_other', title: '其他' },
  ]
  assert.deepEqual(filterContextFacts(facts, 'admin').map((f) => f.id), ['f_admin'])
  assert.deepEqual(filterContextFacts(facts, '登录').map((f) => f.id), ['f_login'])
  assert.deepEqual(filterContextFacts(facts, 'f_').length, 3)
  assert.deepEqual(filterContextFacts(facts, '', 2).map((f) => f.id), ['f_admin', 'f_login'])
  // A selected id that is not in the loaded page is still identified honestly.
  assert.equal(contextFactLabel('f_admin', facts), '/admin 越权')
  assert.match(contextFactLabel('f_gone', facts), /不在最近列表/)
})

test('the PUT carries the displayed version and the whole selection (no delta)', async () => {
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
    return new Response(JSON.stringify({ ...intent(), context_version: 4 }))
  }
  try {
    const updated = await putIntentContext('in_example', {
      version: 3,
      references: ['f_ref', 'f_pin'],
      pins: [],
      entities: ['host:10.0.0.5'],
    })
    assert.equal(sent.url, '/p/fixture/api/intents/in_example/context')
    assert.equal(sent.method, 'PUT')
    assert.deepEqual(sent.body, {
      version: 3,
      references: ['f_ref', 'f_pin'],
      pins: [],
      entities: ['host:10.0.0.5'],
    })
    assert.equal(updated.context_version, 4)
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})
