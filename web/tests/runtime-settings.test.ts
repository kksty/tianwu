import { test } from 'node:test'
import assert from 'node:assert/strict'
import { api } from '../src/lib/api.ts'

test('the auxiliary review/report concurrency is read and written on the instance endpoint', async () => {
  const originalFetch = globalThis.fetch
  const calls: { url: string; method: string; body?: unknown }[] = []
  globalThis.fetch = async (url, init) => {
    calls.push({
      url: String(url),
      method: init?.method ?? 'GET',
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    })
    const value = calls.length === 1 ? 1 : 3
    return new Response(
      JSON.stringify({ auxiliary_concurrency: value, max_auxiliary_concurrency: 8 }),
    )
  }
  try {
    const read = await api.runtimeSettings()
    assert.deepEqual(read, { auxiliary_concurrency: 1, max_auxiliary_concurrency: 8 })
    const saved = await api.saveRuntimeSettings(3)
    assert.deepEqual(saved, { auxiliary_concurrency: 3, max_auxiliary_concurrency: 8 })
    assert.deepEqual(calls, [
      { url: '/api/runtime-settings', method: 'GET', body: undefined },
      {
        url: '/api/runtime-settings',
        method: 'PUT',
        body: { auxiliary_concurrency: 3 },
      },
    ])
  } finally {
    globalThis.fetch = originalFetch
  }
})
