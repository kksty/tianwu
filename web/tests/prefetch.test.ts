// Behaviour tests for the rail's chunk prefetch: one download per panel even
// when hover/focus/click arrive together, and a failed preload stays retryable
// instead of breaking the panel's real lazy import.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createPrefetchRegistry } from '../src/lib/prefetch.ts'

/** Let every already-queued promise handler run. */
function flush(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0))
}

function deferred() {
  let resolve!: () => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<void>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

test('a prefetched page loads at most once', async () => {
  const registry = createPrefetchRegistry()
  const chunk = deferred()
  let loads = 0
  registry.register('graph', () => {
    loads++
    return chunk.promise
  })

  registry.prefetch('graph')
  registry.prefetch('graph')
  assert.equal(loads, 1, 'the second intent must not start a second download')

  chunk.resolve()
  await flush()
  registry.prefetch('graph')
  assert.equal(loads, 1, 'a chunk already in the module cache is not fetched again')
  assert.equal(registry.isStarted('graph'), true)
})

test('concurrent prefetches share one in-flight request', async () => {
  const registry = createPrefetchRegistry()
  const chunk = deferred()
  let loads = 0
  registry.register('overview', () => {
    loads++
    return chunk.promise
  })

  registry.prefetch('overview')
  registry.prefetch('overview')
  registry.prefetch('overview')
  assert.equal(loads, 1)

  chunk.resolve()
  await flush()
  assert.equal(loads, 1)
})

test('a failed prefetch is swallowed and can be retried', async () => {
  const registry = createPrefetchRegistry()
  let loads = 0
  registry.register('assets', () => {
    loads++
    return loads === 1 ? Promise.reject(new Error('offline')) : Promise.resolve()
  })

  assert.doesNotThrow(() => registry.prefetch('assets'))
  await flush()
  assert.equal(loads, 1)
  assert.equal(registry.isStarted('assets'), true, 'started means launched, not succeeded')

  registry.prefetch('assets')
  await flush()
  assert.equal(loads, 2, 'a failure is not cached as success')

  registry.prefetch('assets')
  await flush()
  assert.equal(loads, 2, 'a successful retry is cached')
})

test('a loader that throws synchronously does not escape the intent', () => {
  const registry = createPrefetchRegistry()
  let loads = 0
  registry.register('terminal', () => {
    loads++
    throw new Error('broken chunk')
  })

  assert.doesNotThrow(() => registry.prefetch('terminal'))
  assert.equal(registry.isStarted('terminal'), true)

  registry.prefetch('terminal')
  assert.equal(loads, 2, 'a synchronous failure stays retryable')
})

test('prefetching an unknown id is a no-op', () => {
  const registry = createPrefetchRegistry()
  assert.doesNotThrow(() => registry.prefetch('missing'))
  assert.equal(registry.isStarted('missing'), false)
})

test('an id becomes usable once registered', async () => {
  const registry = createPrefetchRegistry()
  registry.prefetch('nodes')
  assert.equal(registry.isStarted('nodes'), false)

  let loads = 0
  registry.register('nodes', async () => {
    loads++
  })
  registry.prefetch('nodes')
  await flush()

  assert.equal(loads, 1)
  assert.equal(registry.isStarted('nodes'), true)
})

test('re-registering an id swaps the loader without rerunning the old request', async () => {
  const registry = createPrefetchRegistry()
  let oldLoads = 0
  let newLoads = 0
  registry.register('runs', async () => {
    oldLoads++
  })
  registry.register('runs', async () => {
    newLoads++
  })

  registry.prefetch('runs')
  await flush()

  assert.equal(oldLoads, 0, 'the replaced loader must not run')
  assert.equal(newLoads, 1, 'the latest loader is the one that runs')
})

test('a re-register does not restart a request already in flight', async () => {
  const registry = createPrefetchRegistry()
  const chunk = deferred()
  let loads = 0
  registry.register('workers', () => {
    loads++
    return chunk.promise
  })
  registry.prefetch('workers')

  registry.register('workers', () => {
    loads++
    return Promise.resolve()
  })
  chunk.resolve()
  await flush()

  assert.equal(loads, 1)
})

test('isStarted only turns true when a prefetch is launched', () => {
  const registry = createPrefetchRegistry()
  registry.register('pocs', async () => {})
  assert.equal(registry.isStarted('pocs'), false, 'register alone does not start')

  registry.prefetch('pocs')
  assert.equal(registry.isStarted('pocs'), true)

  registry.prefetch('pocs')
  assert.equal(registry.isStarted('pocs'), true)
})
