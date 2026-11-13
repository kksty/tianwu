// Content-equality tests for the console data layer. Polling returns a fresh
// JSON object every few seconds; the hooks compare content so an unchanged
// ledger does not re-render the view. These tests pin the shape rules the
// comparison is allowed to rely on: JSON values only, order-significant arrays,
// exact key sets, and no "null looks like undefined".
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { deepEqual } from '../src/lib/equal.ts'

test('deepEqual compares primitives by value', () => {
  assert.equal(deepEqual(1, 1), true)
  assert.equal(deepEqual('board', 'board'), true)
  assert.equal(deepEqual(true, true), true)
  assert.equal(deepEqual(0, 1), false)
  assert.equal(deepEqual('1', 1), false)
  assert.equal(deepEqual(0, false), false)
  assert.equal(deepEqual('', null), false)
})

test('deepEqual walks nested JSON objects', () => {
  const left = { run: { id: 'r_1', warnings: [{ code: 'model_missing' }] }, ready: true }
  const same = { run: { id: 'r_1', warnings: [{ code: 'model_missing' }] }, ready: true }
  // Key order is not part of JSON data, so a re-serialised object still matches.
  const reordered = { ready: true, run: { warnings: [{ code: 'model_missing' }], id: 'r_1' } }
  const changed = { run: { id: 'r_1', warnings: [{ code: 'premise_broken' }] }, ready: true }

  assert.equal(deepEqual(left, same), true)
  assert.equal(deepEqual(left, reordered), true)
  assert.equal(deepEqual(left, changed), false)
})

test('deepEqual treats array order as significant', () => {
  assert.equal(deepEqual([1, 2, 3], [1, 2, 3]), true)
  assert.equal(deepEqual([1, 2, 3], [3, 2, 1]), false)
  assert.equal(deepEqual([{ id: 'a' }, { id: 'b' }], [{ id: 'b' }, { id: 'a' }]), false)
  assert.equal(deepEqual([[1, [2]], [3]], [[1, [2]], [3]]), true)
})

test('deepEqual rejects objects with different key sets', () => {
  assert.equal(deepEqual({ id: 'r_1', status: 'done' }, { id: 'r_1' }), false)
  assert.equal(deepEqual({ id: 'r_1' }, { id: 'r_1', status: 'done' }), false)
  assert.equal(deepEqual({ id: 'r_1' }, { id: 'r_2' }), false)
  // A key present but undefined is still a shape difference from a missing key.
  assert.equal(deepEqual({ id: undefined }, {}), false)
  assert.equal(deepEqual({ id: undefined }, { id: undefined }), true)
})

test('deepEqual keeps null and undefined apart', () => {
  assert.equal(deepEqual(null, null), true)
  assert.equal(deepEqual(undefined, undefined), true)
  assert.equal(deepEqual(null, undefined), false)
  assert.equal(deepEqual(undefined, null), false)
  assert.equal(deepEqual({ data: null }, { data: null }), true)
  assert.equal(deepEqual({ data: null }, { data: undefined }), false)
  assert.equal(deepEqual({ data: null }, {}), false)
  assert.equal(deepEqual(null, {}), false)
})

test('deepEqual rejects values of different types', () => {
  assert.equal(deepEqual([], {}), false)
  assert.equal(deepEqual({}, []), false)
  assert.equal(deepEqual('[]', []), false)
  assert.equal(deepEqual({ length: 0 }, []), false)
})

test('deepEqual treats NaN as equal to itself', () => {
  // JSON never carries NaN, but the hooks must stay stable if a numeric field is
  // derived in the browser instead of parsed.
  assert.equal(deepEqual(NaN, NaN), true)
  assert.equal(deepEqual({ score: NaN }, { score: NaN }), true)
  assert.equal(deepEqual({ score: NaN }, { score: 0 }), false)
})

test('deepEqual handles empty objects and arrays', () => {
  assert.equal(deepEqual({}, {}), true)
  assert.equal(deepEqual([], []), true)
  assert.equal(deepEqual({}, []), false)
  assert.equal(deepEqual({ list: [] }, { list: [] }), true)
  assert.equal(deepEqual({ list: [] }, { list: [1] }), false)
})

test('deepEqual does not mutate or depend on its inputs', () => {
  const left = { facts: [{ id: 'f_1' }], total: 1 }
  const right = { facts: [{ id: 'f_1' }], total: 1 }
  const leftSnapshot = JSON.stringify(left)

  assert.equal(deepEqual(left, right), true)
  assert.equal(JSON.stringify(left), leftSnapshot)
})
