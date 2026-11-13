import { test } from 'node:test'
import assert from 'node:assert/strict'
import { scrollRunStreamToLatest } from '../src/lib/run-stream.ts'

test('jump to latest scrolls only the log viewport and uses its current content height', () => {
  const scrolls: ScrollToOptions[] = []
  const viewport = { scrollHeight: 1200, scrollTo: (options: ScrollToOptions) => scrolls.push(options) }
  const stream = {
    closest(selector: string) {
      assert.equal(selector, '[data-radix-scroll-area-viewport]')
      return viewport
    },
  } as unknown as HTMLElement

  scrollRunStreamToLatest(stream)
  viewport.scrollHeight = 2400
  scrollRunStreamToLatest(stream)
  assert.deepEqual(scrolls, [
    { top: 1200, behavior: 'smooth' },
    { top: 2400, behavior: 'smooth' },
  ])
})

test('jump to latest is safe when the stream has unmounted or has no log viewport', () => {
  scrollRunStreamToLatest(null)
  scrollRunStreamToLatest({ closest: () => null } as unknown as HTMLElement)
})
