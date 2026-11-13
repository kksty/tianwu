import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  frontierFactList,
  frontierMarkers,
  frontierPremiseChangedText,
} from '../src/lib/format.ts'
import type { FrontierItem } from '../src/lib/types.ts'

function frontier(over: Partial<FrontierItem> = {}): FrontierItem {
  return {
    id: 'in_example',
    title: '示例方向',
    ready: true,
    ...over,
  }
}

test('a frontier row from an older board yields no invented markers', () => {
  // The new fields are optional on the wire: absent means "server did not say",
  // not "zero premises" or "no descendants".
  assert.deepEqual(frontierMarkers(frontier()), [])
  assert.deepEqual(frontierMarkers(undefined), [])
  assert.equal(frontierPremiseChangedText(frontier()), '')
})

test('a direction created without hard premises is marked, and context does not clear it', () => {
  const markers = frontierMarkers(frontier({ prerequisites: 0, no_premise: true }))
  assert.deepEqual(markers.map((m) => m.key), ['no_premise'])
  assert.equal(markers[0].label, '无前提')
  // The explanation must not present a reference/pin as a premise.
  assert.match(markers[0].detail, /引用\/固定/)
  assert.match(markers[0].detail, /不是前提/)
  // prerequisites: 0 alone (without no_premise) never invents a "无前提" badge.
  assert.deepEqual(frontierMarkers(frontier({ prerequisites: 0 })), [])
})

test('changed premise ids are counted, listed in the tooltip, and capped readably', () => {
  const markers = frontierMarkers(frontier({ premise_changed: ['f_a', 'f_b'] }))
  assert.equal(markers.length, 1)
  assert.equal(markers[0].key, 'premise_changed')
  assert.equal(markers[0].label, '前提已变 2')
  assert.match(markers[0].detail, /f_a/)
  assert.match(markers[0].detail, /f_b/)
  assert.match(markers[0].detail, /断言/)
  assert.equal(frontierPremiseChangedText(frontier({ premise_changed: ['f_a', 'f_b'] })), '前提已变：f_a、f_b')

  const five = ['f_1', 'f_2', 'f_3', 'f_4', 'f_5']
  const longTooltip = frontierMarkers(frontier({ premise_changed: five }))[0]
  assert.match(longTooltip.detail, /f_1/)
  assert.match(longTooltip.detail, /等 5 条/)
  assert.doesNotMatch(longTooltip.detail, /f_5/)
  // The inline line caps at three ids and still reports the total.
  assert.equal(frontierPremiseChangedText(frontier({ premise_changed: five })), '前提已变：f_1、f_2、f_3 等 5 条')
})

test('the prerequisite count is shown only when the direction actually has premises', () => {
  const marked = frontierMarkers(frontier({ prerequisites: 3 }))
  assert.deepEqual(marked.map((m) => m.key), ['prerequisites'])
  assert.equal(marked[0].label, '前提 3')
  assert.match(marked[0].detail, /from_fact_ids/)
  assert.deepEqual(frontierMarkers(frontier({ prerequisites: 0 })), [])
})

test('open descendants are surfaced, zero descendants stays quiet', () => {
  const marked = frontierMarkers(frontier({ descendants: 2 }))
  assert.deepEqual(marked.map((m) => m.key), ['descendants'])
  assert.equal(marked[0].label, '后代 2')
  assert.match(marked[0].detail, /未结束/)
  assert.deepEqual(frontierMarkers(frontier({ descendants: 0 })), [])
})

test('a mixed row keeps a stable, urgency-first badge order', () => {
  const markers = frontierMarkers(
    frontier({ prerequisites: 2, premise_changed: ['f_a'], descendants: 4 }),
  )
  assert.deepEqual(
    markers.map((m) => m.key),
    ['premise_changed', 'prerequisites', 'descendants'],
  )
  assert.deepEqual(
    markers.map((m) => m.label),
    ['前提已变 1', '前提 2', '后代 4'],
  )
  // Tones separate urgent revision from neutral counts.
  assert.equal(markers[0].tone, 'warning')
  assert.equal(markers[1].tone, 'muted')
  assert.equal(markers[2].tone, 'secondary')
})

test('fact lists stay compact and never print an empty separator', () => {
  assert.equal(frontierFactList(undefined), '')
  assert.equal(frontierFactList([]), '')
  assert.equal(frontierFactList(['f_a']), 'f_a')
  assert.equal(frontierFactList(['f_a', 'f_b']), 'f_a、f_b')
  assert.equal(frontierFactList(['f_a', 'f_b', 'f_c', 'f_d'], 2), 'f_a、f_b 等 4 条')
})
