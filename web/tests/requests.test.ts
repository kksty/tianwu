import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createRequestGate } from '../src/lib/requests.ts'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
function screen<T>() {
  const values: T[] = [], errors: unknown[] = []
  let starts = 0, finishes = 0
  return { values, errors, get starts() { return starts }, get finishes() { return finishes }, handlers: {
    start: () => { starts++ }, value: (v: T) => { values.push(v) },
    error: (e: unknown) => { errors.push(e) }, finish: () => { finishes++ },
  } }
}
test('polling joins the initial or manual request instead of racing it', async () => {
  const gate = createRequestGate<number>(), pending = deferred<number>(), ui = screen<number>()
  const first = gate.run(() => pending.promise, ui.handlers, true)
  const poll = gate.run(() => Promise.resolve(99), ui.handlers)
  assert.equal(first, poll)
  pending.resolve(1)
  await first
  assert.deepEqual(ui.values, [1])
  assert.equal(ui.starts, 1)
})
test('an older response cannot overwrite a new search or manual refresh', async () => {
  const gate = createRequestGate<number>(), old = deferred<number>(), ui = screen<number>()
  const first = gate.run(() => old.promise, ui.handlers)
  await gate.run(() => Promise.resolve(2), ui.handlers, true)
  old.resolve(1)
  await first
  assert.deepEqual(ui.values, [2])
  assert.equal(ui.finishes, 1)
})
test('a failed poll surfaces its error while preserving the last value, then recovers', async () => {
  const gate = createRequestGate<number>(), ui = screen<number>(), error = new Error('offline')
  await gate.run(() => Promise.resolve(1), ui.handlers)
  await gate.run(() => Promise.reject(error), ui.handlers)
  assert.deepEqual(ui.values, [1])
  assert.deepEqual(ui.errors, [error])
  await gate.run(() => Promise.resolve(2), ui.handlers)
  assert.deepEqual(ui.values, [1, 2])
})
test('unmount or dependency invalidation ignores a late error and completion', async () => {
  const gate = createRequestGate<number>(), old = deferred<number>(), ui = screen<number>()
  const first = gate.run(() => old.promise, ui.handlers)
  gate.invalidate()
  old.reject(new Error('old request'))
  await first
  assert.deepEqual(ui.errors, [])
  assert.equal(ui.finishes, 0)
  await gate.run(() => Promise.resolve(2), ui.handlers)
  assert.deepEqual(ui.values, [2])
})
test('synchronous loader failures also reach the error state', async () => {
  const gate = createRequestGate<number>(), ui = screen<number>()
  await gate.run(() => { throw new Error('loader failed') }, ui.handlers)
  assert.equal(ui.errors.length, 1)
  assert.equal(ui.finishes, 1)
})
