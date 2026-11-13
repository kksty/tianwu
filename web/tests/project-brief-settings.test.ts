import { test } from 'node:test'
import assert from 'node:assert/strict'
import { api } from '../src/lib/api.ts'
import {
  mergeProjectSettings,
  projectSettingsDraft,
  projectSettingsPatch,
} from '../src/lib/settings.ts'
import type { ProjectInfo } from '../src/lib/types.ts'

function project(over: Partial<ProjectInfo> = {}): ProjectInfo {
  return {
    id: 'p_example',
    name: '示例项目',
    scope: '评估 example.com 的外网门户',
    description: '只读优先',
    created: '2026-11-12T00:00:00Z',
    intents: 0,
    facts: 0,
    evidence: 0,
    running: 0,
    max_runs: null,
    max_runs_effective: 0,
    verify_auto: null,
    verify_auto_effective: false,
    premise_policy: 'standard',
    ...over,
  }
}

test('the settings draft round-trips the brief and the constraint document', () => {
  const draft = projectSettingsDraft(project())
  assert.equal(draft.scope, '评估 example.com 的外网门户')
  assert.equal(draft.description, '只读优先')
  // The premise policy is part of the same project settings form; an older row
  // without the field reads as the effective default, not as blank.
  assert.equal(draft.premise_policy, 'standard')
  assert.equal(projectSettingsDraft(project({ premise_policy: 'verified_only' })).premise_policy, 'verified_only')
  // A project created before the constraint document existed reads as empty,
  // not as a guessed default.
  const legacy = projectSettingsDraft(project({ scope: undefined, description: undefined }))
  assert.equal(legacy.scope, '')
  assert.equal(legacy.description, '')
})

test('saving the brief or constraints sends only what changed, including an explicit clear', () => {
  const previous = projectSettingsDraft(project())
  assert.deepEqual(projectSettingsPatch(previous, previous), {})

  const edited = { ...previous, scope: '评估 example.com 与登录口', description: '只读优先，不做写操作' }
  assert.deepEqual(projectSettingsPatch(previous, edited), {
    scope: '评估 example.com 与登录口',
    description: '只读优先，不做写操作',
  })

  // Cancelling the built-in document is an explicit empty description.
  const cancelled = { ...previous, description: '' }
  assert.deepEqual(projectSettingsPatch(previous, cancelled), { description: '' })

  // Editing the brief must not drag an untouched description along.
  const briefOnly = { ...previous, scope: '新的简报' }
  assert.deepEqual(projectSettingsPatch(previous, briefOnly), { scope: '新的简报' })

  // The premise policy is saved with the other project defaults, and only when
  // it actually changed.
  const verified = { ...previous, premise_policy: 'verified_only' }
  assert.deepEqual(projectSettingsPatch(previous, verified), { premise_policy: 'verified_only' })
  assert.deepEqual(projectSettingsPatch(previous, { ...previous, premise_policy: 'standard' }), {})
})

test('a ledger refresh preserves an edited brief while updating untouched fields', () => {
  const previous = projectSettingsDraft(project())
  const incoming = projectSettingsDraft(project({ scope: '服务端的新简报', max_runs: 8 }))
  const edited = { ...previous, scope: '操作员正在写的简报', description: '操作员改过的约束' }
  const merged = mergeProjectSettings(previous, incoming, edited)
  assert.equal(merged.scope, '操作员正在写的简报', 'an unsaved brief is never clobbered by polling')
  assert.equal(merged.description, '操作员改过的约束')
  assert.equal(merged.max_runs, '8', 'an untouched field follows the ledger')
})

test('creating a project sends the operator-defined start and end, with no quantified criterion', async () => {
  const originalFetch = globalThis.fetch
  let sent: { url?: string; method?: string; body?: unknown } = {}
  globalThis.fetch = async (url, init) => {
    sent = {
      url: String(url),
      method: init?.method,
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    }
    return new Response(
      JSON.stringify(project({ id: 'p-a1b2c3d4', name: '评估 example.com 的外网门户' })),
      { status: 201 },
    )
  }
  try {
    const created = await api.createProject(
      '评估 example.com 的外网门户',
      '起点：首页与登录口，已有一个测试账号',
      '终点：证明存在未授权访问并留下原始响应',
      '只读优先',
    )
    assert.equal(sent.url, '/api/projects')
    assert.equal(sent.method, 'POST')
    assert.deepEqual(sent.body, {
      name: '评估 example.com 的外网门户',
      scope: '起点：首页与登录口，已有一个测试账号',
      goal: '终点：证明存在未授权访问并留下原始响应',
      description: '只读优先',
    })
    assert.ok(!('quantified_target' in (sent.body as Record<string, unknown>)))
    // The directory id is decoupled from the name; the console navigates by id.
    assert.equal(created.id, 'p-a1b2c3d4')
    assert.equal(created.name, '评估 example.com 的外网门户')
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('updating project settings can edit the brief and clear the constraints explicitly', async () => {
  const originalFetch = globalThis.fetch
  let sent: { url?: string; method?: string; body?: unknown } = {}
  globalThis.fetch = async (url, init) => {
    sent = {
      url: String(url),
      method: init?.method,
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    }
    return new Response(JSON.stringify(project({ description: '' })))
  }
  try {
    await api.updateProject('p_example', {
      scope: '新的项目简报',
      description: '',
      premise_policy: 'verified_only',
    })
    assert.equal(sent.url, '/api/projects/p_example')
    assert.equal(sent.method, 'PUT')
    // '' is sent (cancel), not omitted (keep the old document). The premise
    // policy travels in the same project-settings request.
    assert.deepEqual(sent.body, { scope: '新的项目简报', description: '', premise_policy: 'verified_only' })
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('creating a goal sends the operator-defined start and end', async () => {
  const originalFetch = globalThis.fetch
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: { location: { pathname: '/p/p_example/', search: '', hash: '' } },
  })
  let sent: { url?: string; method?: string; body?: unknown } = {}
  globalThis.fetch = async (url, init) => {
    sent = { url: String(url), method: init?.method, body: init?.body ? JSON.parse(String(init.body)) : undefined }
    return new Response(
      JSON.stringify({ id: 'g_1', statement: '终点', start: '起点', met: false, created_at: '2026-11-12T00:00:00Z' }),
      { status: 201 },
    )
  }
  try {
    await api.createGoal('终点', '起点')
    assert.equal(sent.url, '/p/p_example/api/goals')
    assert.equal(sent.method, 'POST')
    assert.deepEqual(sent.body, { statement: '终点', start: '起点' })
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})
