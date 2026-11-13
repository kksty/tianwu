import { test } from 'node:test'
import assert from 'node:assert/strict'
import { api } from '../src/lib/api.ts'

test('native IOA project and instance views use distinct read-only endpoints', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  const calls: { url: string; method: string }[] = []
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/fixture/' } } })
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), method: init?.method ?? 'GET' })
    return new Response('{"records":[],"sessions":{}}')
  }
  try {
    await api.ioaObservations()
    await api.instanceIOAObservations()
    assert.deepEqual(calls, [
      { url: '/p/fixture/api/ioa/events', method: 'GET' },
      { url: '/api/ioa/events', method: 'GET' },
    ])
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})

test('expired login preserves the selected view, record and graph selection', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  let redirect = ''
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: {
    pathname: '/p/example/', search: '?view=runs&run=r_example', hash: '#selection',
    assign: (url: string) => { redirect = url },
  } } })
  globalThis.fetch = async () => new Response('{}', { status: 401 })
  try {
    await assert.rejects(api.health(), /需要登录/)
    assert.equal(new URL(redirect, 'http://localhost').searchParams.get('next'), '/p/example/?view=runs&run=r_example#selection')
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})

test('unavailable human action is sent as a distinct receipt, not completed login', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  let sent: { url?: string; body?: unknown } = {}
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/fixture/' } } })
  globalThis.fetch = async (url, init) => {
    sent = {url: String(url), body: JSON.parse(String(init?.body))}
    return new Response('{"accepted":true}')
  }
  try {
    await api.completeOperator('in_example', 'op_example', '没有账号，本次仅测未登录面', 'unavailable')
    assert.equal(sent.url, '/p/fixture/api/intents/in_example/operator/complete')
    assert.deepEqual(sent.body, {request_id:'op_example', response:'没有账号，本次仅测未登录面', resolution:'unavailable'})
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})

test('the review switch is project-scoped and distinct from the project pause', async () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalFetch = globalThis.fetch
  const calls: { url: string; method: string }[] = []
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname: '/p/acme/' } } })
  globalThis.fetch = async (url, init) => {
    calls.push({ url: String(url), method: init?.method ?? 'GET' })
    return new Response('{"paused":false,"enabled":true}', { status: 200 })
  }
  try {
    await api.verifierStatus()
    await api.pauseReviews()
    await api.resumeReviews()
    // /api/project/pause stays a different endpoint: pausing reviews never
    // resumes or pauses the engagement (D137).
    assert.deepEqual(calls, [
      { url: '/p/acme/api/verifier', method: 'GET' },
      { url: '/p/acme/api/verifier/pause', method: 'POST' },
      { url: '/p/acme/api/verifier/resume', method: 'POST' },
    ])
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})
