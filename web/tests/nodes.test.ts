import { test } from 'node:test'
import assert from 'node:assert/strict'
import { hostPresentation } from '../src/lib/nodes.ts'

const host = { endpoint:'http://127.0.0.1:18766', pid:100, online:true, ready:true, restarts:0, failed_ready:0, unhealthy:false }

test('execution status separates model readiness from terminal availability', () => {
  assert.equal(hostPresentation(host).status, 'online')
  const failed = hostPresentation({...host, ready:false, last_error:'Insufficient Balance'})
  assert.equal(failed.status, 'warning')
  assert.equal(failed.terminal, true)
  assert.equal(hostPresentation({...host, online:false, ready:false}).terminal, false)
  assert.equal(hostPresentation().label, '状态未知')
})
