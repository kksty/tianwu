import { test } from 'node:test'
import assert from 'node:assert/strict'
import { browserOccupancyLabel, canOperateBrowser } from '../src/lib/access.ts'
import type { AccessEntry, BrowserLease } from '../src/lib/types.ts'

const entry: AccessEntry = { id: 'ac_fixture', name: '现场', kind: 'browser', origin: 'https://example.test', url: 'https://example.test', created_at: '2026-11-06' }
const lease: BrowserLease = { id: 'bl_fixture', owner: 'operator', state: 'held', acquired_at: '2026-11-06' }

test('browser controls require an operator lease with no active operation', () => {
  assert.equal(canOperateBrowser(entry), false)
  assert.equal(canOperateBrowser({ ...entry, lease }), true)
  for (const change of [{owner:'pt_a',run_id:'r_a'}, {state:'recovery' as const}, {busy:true}]) {
    assert.equal(canOperateBrowser({ ...entry, lease: { ...lease, ...change } }), false)
  }
})

test('occupancy labels distinguish idle, worker ownership, active operations and recovery', () => {
  assert.equal(browserOccupancyLabel(entry), '空闲')
  assert.equal(browserOccupancyLabel({...entry, lease}), '操作员占用')
  assert.equal(browserOccupancyLabel({...entry, lease:{...lease,owner:'pt_a'}}), '执行者占用')
  assert.equal(browserOccupancyLabel({...entry, lease:{...lease,busy:true}}), '操作进行中')
  assert.equal(browserOccupancyLabel({...entry, lease:{...lease,state:'recovery'}}), '待确认恢复')
})
