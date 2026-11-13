import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  LIVE_RELOAD_DELAY_MS,
  LIVE_RELOAD_MIN_MS,
  liveReloadDelay,
  hasNewContent,
  shouldLiveReload,
} from '../src/lib/graph-live.ts'

const snapshot = { epoch: 'ledger_a', seq: 7 }

function input(over = {}) {
  return {
    live: true,
    hidden: false,
    loading: false,
    busy: false,
    hasNewChanges: true,
    ...over,
  }
}

test('hasNewContent requires a content row, not just a moved watermark', () => {
  assert.equal(hasNewContent(snapshot, { epoch: 'ledger_a', seq: 7 }), false)
  assert.equal(hasNewContent(snapshot, { epoch: 'ledger_a', seq: 6 }), false, 'older watermark')
  assert.equal(
    hasNewContent(snapshot, { epoch: 'ledger_a', seq: 9 }),
    false,
    'bookkeeping advances seq without a versions row: nothing to reload',
  )
  assert.equal(hasNewContent(snapshot, { epoch: 'ledger_a', seq: 9 }, 2), true, 'two content rows since the snapshot')
  assert.equal(hasNewContent(snapshot, { epoch: 'ledger_a', seq: 9 }, 0), false, 'seq moved but no content rows')
  assert.equal(hasNewContent(snapshot, { epoch: 'ledger_b', seq: 1 }), true, 'a new ledger epoch restarts seq')
  assert.equal(hasNewContent(undefined, { epoch: 'ledger_a', seq: 8 }, 1), false, 'no snapshot yet')
  assert.equal(hasNewContent(snapshot, undefined, 1), false, 'no probe yet')
  assert.equal(hasNewContent(snapshot, { epoch: '', seq: 0 }), false, 'placeholder probe before the snapshot lands')
})

test('shouldLiveReload only when live, visible, idle and content changed', () => {
  assert.equal(shouldLiveReload(input()), true)
  assert.equal(shouldLiveReload(input({ live: false })), false, 'operator turned 实时 off')
  assert.equal(shouldLiveReload(input({ hidden: true })), false, 'hidden tab')
  assert.equal(shouldLiveReload(input({ loading: true })), false, 'request in flight')
  assert.equal(shouldLiveReload(input({ busy: true })), false, '加载更多 in flight')
  assert.equal(shouldLiveReload(input({ hasNewChanges: false })), false, 'nothing new')
})

test('liveReloadDelay settles bursts and throttles reloads', () => {
  const now = 100_000
  assert.equal(liveReloadDelay(now, 0), LIVE_RELOAD_DELAY_MS, 'a quiet period waits only the settle window')
  assert.equal(liveReloadDelay(now, now - 10_000), LIVE_RELOAD_DELAY_MS, 'the interval already elapsed')
  assert.equal(
    liveReloadDelay(now, now - (LIVE_RELOAD_MIN_MS - LIVE_RELOAD_DELAY_MS / 2)),
    LIVE_RELOAD_DELAY_MS,
    'settle still applies when the interval is nearly elapsed',
  )
  assert.equal(liveReloadDelay(now, now), LIVE_RELOAD_MIN_MS, 'back-to-back changes wait the full interval')
})
