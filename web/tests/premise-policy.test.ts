import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ApiError, api } from '../src/lib/api.ts'
import { premisePolicyHint, premisePolicyLabel } from '../src/lib/format.ts'

function withFixture(
  run: (calls: { url: string; method: string; body?: unknown }[]) => Promise<void>,
  response: (calls: { url: string; method: string; body?: unknown }[]) => Response,
) {
  return async () => {
    const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
    const originalFetch = globalThis.fetch
    const calls: { url: string; method: string; body?: unknown }[] = []
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: { location: { pathname: '/p/fixture/' } },
    })
    globalThis.fetch = async (url, init) => {
      calls.push({
        url: String(url),
        method: init?.method ?? 'GET',
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      })
      return response(calls)
    }
    try {
      await run(calls)
    } finally {
      globalThis.fetch = originalFetch
      if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
      else Reflect.deleteProperty(globalThis, 'window')
    }
  }
}

test(
  'premise policy is read and written on the project-scoped endpoint',
  withFixture(
    async (calls) => {
      const read = await api.premisePolicy()
      assert.deepEqual(read, { premise_policy: 'standard', effective: 'standard' })
      const saved = await api.savePremisePolicy('verified_only')
      assert.deepEqual(saved, { premise_policy: 'verified_only', effective: 'verified_only' })
      assert.deepEqual(calls, [
        { url: '/p/fixture/api/premise-policy', method: 'GET', body: undefined },
        {
          url: '/p/fixture/api/premise-policy',
          method: 'PUT',
          body: { premise_policy: 'verified_only' },
        },
      ])
    },
    (calls) =>
      new Response(
        JSON.stringify(
          calls.length === 1
            ? { premise_policy: 'standard', effective: 'standard' }
            : { premise_policy: 'verified_only', effective: 'verified_only' },
        ),
      ),
  ),
)

test(
  'an invalid policy is a 400 premise_policy_invalid, not an assumed saved value',
  withFixture(
    async () => {
      await assert.rejects(api.savePremisePolicy('sometimes'), (error: unknown) => {
        assert.ok(error instanceof ApiError)
        assert.equal(error.status, 400)
        assert.equal(error.code, 'premise_policy_invalid')
        assert.match(error.message, /standard 或 verified_only/)
        return true
      })
    },
    () =>
      new Response(
        JSON.stringify({
          error: 'premise_policy 只能是 standard 或 verified_only（收到 "sometimes"）',
          code: 'premise_policy_invalid',
        }),
        { status: 400 },
      ),
  ),
)

test(
  'an unbound project is a 409 premise_policy_unavailable, distinct from a bad value',
  withFixture(
    async () => {
      await assert.rejects(api.savePremisePolicy('verified_only'), (error: unknown) => {
        assert.ok(error instanceof ApiError)
        assert.equal(error.status, 409)
        assert.equal(error.code, 'premise_policy_unavailable')
        return true
      })
    },
    () =>
      new Response(
        JSON.stringify({
          error: '项目未绑定：不能在实例级设置前提政策',
          code: 'premise_policy_unavailable',
        }),
        { status: 409 },
      ),
  ),
)

test('the policy labels and explanations keep the two remaining modes distinct', () => {
  assert.equal(premisePolicyLabel('standard'), '标准')
  assert.equal(premisePolicyLabel('verified_only'), '证明优先')
  assert.equal(premisePolicyLabel(undefined), '—')
  assert.equal(premisePolicyLabel('something-else'), 'something-else')

  const standard = premisePolicyHint('standard')
  const verified = premisePolicyHint('verified_only')
  assert.match(standard, /存在且未被否决/)
  assert.match(verified, /必要前提/)
  assert.match(verified, /核验/)
  assert.notEqual(standard, verified)
  // An unknown/empty policy has no invented explanation.
  assert.equal(premisePolicyHint(undefined), '')
})
