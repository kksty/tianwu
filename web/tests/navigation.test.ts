import { test } from 'node:test'
import assert from 'node:assert/strict'
import { decodeLocationPart, locationPath, navigate, subscribeLocation } from '../src/lib/navigation.ts'

test('malformed URL escapes do not prevent the console opening', () => {
  assert.equal(decodeLocationPart('%'), '%')
  assert.equal(decodeLocationPart('%E4%B8'), '%E4%B8')
  assert.equal(decodeLocationPart('in_%E6%B5%8B%E8%AF%95'), 'in_测试')
})

test('navigation adds history and notifies readers on navigation, Back and hash changes', () => {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const events = new EventTarget()
  const location = { pathname: '/p/fixture/', search: '?view=runs&run=r_one', hash: '#selection' }
  const calls: {mode: string; url: string}[] = []
  function update(mode: string, url: string) {
    calls.push({mode, url})
    const next = new URL(url, 'http://localhost')
    Object.assign(location, {pathname: next.pathname, search: next.search, hash: next.hash})
  }
  Object.defineProperty(globalThis, 'window', {configurable: true, value: {
    location,
    history: {pushState: (_s: unknown, _t: string, url: string) => update('push', url), replaceState: (_s: unknown, _t: string, url: string) => update('replace', url)},
    addEventListener: events.addEventListener.bind(events),
    removeEventListener: events.removeEventListener.bind(events),
    dispatchEvent: events.dispatchEvent.bind(events),
  }})
  let changed = 0
  const unsubscribe = subscribeLocation(() => changed++)
  try {
    assert.equal(locationPath(), '/p/fixture/?view=runs&run=r_one#selection')
    navigate('/p/fixture/?view=memory')
    assert.equal(changed, 1)
    navigate('/p/fixture/?view=memory')
    assert.equal(changed, 1, 'clicking the active view must not add a duplicate history entry')
    navigate('/p/fixture/?view=graph#in_one', true)
    assert.deepEqual(calls, [
      {mode:'push',url:'/p/fixture/?view=memory'},
      {mode:'replace',url:'/p/fixture/?view=graph#in_one'},
    ])
    events.dispatchEvent(new Event('popstate'))
    events.dispatchEvent(new Event('hashchange'))
    assert.equal(changed, 4)
    unsubscribe()
    events.dispatchEvent(new Event('popstate'))
    assert.equal(changed, 4, 'unmounted readers must be detached')
  } finally {
    unsubscribe()
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})
