import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  api,
  backlogSummary,
  isMechanicalSender,
  queueLoad,
  queueUnderPressure,
  readFactNotice,
} from '../src/lib/api.ts'
import type { CommunicationQueue, CommunicationStatus, Message } from '../src/lib/types.ts'

function queue(patch: Partial<CommunicationQueue> = {}): CommunicationQueue {
  return { recipient: 'pt_in_a', pending: 0, bytes: 0, deferred: false, cursor: 7, ...patch }
}

function message(patch: Partial<Message> = {}): Message {
  return {
    id: 'm_notice',
    seq: 12,
    project_id: 'p_fixture',
    from_participant_id: 'ledger',
    to_participant_id: 'pt_in_a',
    text: '共享发现变化：事实 f_1 的记录版本 v3（update）。',
    status: 'queued',
    created_at: '2026-11-12T10:00:00+08:00',
    updated_at: '2026-11-12T10:00:00+08:00',
    ...patch,
  }
}

function withProjectWindow<T>(pathname: string, fn: () => Promise<T>): Promise<T> {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { location: { pathname } } })
  const done = () => {
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
  return fn().finally(done)
}

test('communication backlog is read from the project namespace with an optional recipient', async () => {
  const originalFetch = globalThis.fetch
  const calls: string[] = []
  globalThis.fetch = async (url) => {
    calls.push(String(url))
    return new Response('{"queues":[],"max_messages":512,"max_bytes":2097152,"paused":false}')
  }
  try {
    await withProjectWindow('/p/fixture/', async () => {
      const status = await api.communication()
      assert.deepEqual(status.queues, [])
      assert.equal(status.max_messages, 512)
      assert.equal(status.max_bytes, 2 << 20)
      await api.communication('pt_in_a b')
    })
    assert.deepEqual(calls, [
      '/p/fixture/api/communication',
      '/p/fixture/api/communication?recipient=pt_in_a+b',
    ])
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('the mechanical ledger sender is distinct from every participant', () => {
  assert.equal(isMechanicalSender('ledger'), true)
  assert.equal(isMechanicalSender('commander'), false)
  assert.equal(isMechanicalSender('operator'), false)
  assert.equal(isMechanicalSender('pt_in_a'), false)
  assert.equal(isMechanicalSender(undefined), false)
})

test('a notice exposes the fact pointer, version and coalesced count, never a body', () => {
  const notice = readFactNotice(message({ kind: 'fact_notice', notice_fact_id: 'f_1', notice_version: 3, coalesced: 2 }))
  assert.deepEqual(notice, { factId: 'f_1', version: 3, coalesced: 2 })
  assert.equal(readFactNotice(message()), null, 'a ledger row without a notice pointer is not a notice')
  assert.equal(readFactNotice(message({ from_participant_id: 'commander', notice_fact_id: 'f_1' })), null)
  // The body is the shared notification text, not the fact's own title/summary.
  const noticeText = message({ kind: 'fact_notice', notice_fact_id: 'f_1', notice_version: 1 }).text
  assert.ok(noticeText.includes('f_1'))
})

test('queue pressure uses the tighter of count and byte bounds', () => {
  const limits = { max_messages: 100, max_bytes: 1000 }
  assert.equal(queueLoad(queue({ pending: 10, bytes: 10 }), limits), 0.1)
  assert.equal(queueLoad(queue({ pending: 10, bytes: 900 }), limits), 0.9)
  assert.equal(queueLoad(queue({ pending: 200, bytes: 10 }), limits), 1)
  assert.equal(queueUnderPressure(queue({ pending: 10, bytes: 10 }), limits), false)
  assert.equal(queueUnderPressure(queue({ pending: 80, bytes: 10 }), limits), true)
  // A deferred change is pressure by itself: the router is retrying, not losing.
  assert.equal(queueUnderPressure(queue({ deferred: true }), limits), true)
})

test('backlog summary counts queued work and deferred queues only', () => {
  const status: CommunicationStatus = {
    max_messages: 512,
    max_bytes: 2 << 20,
    paused: true,
    queues: [
      queue({ recipient: 'pt_a', pending: 3, bytes: 120 }),
      queue({ recipient: 'pt_b', pending: 0, bytes: 0, deferred: true }),
      queue({ recipient: 'pt_c', pending: 0, bytes: 0 }),
    ],
  }
  const summary = backlogSummary(status)
  assert.equal(summary.queued, 3)
  assert.equal(summary.bytes, 120)
  assert.equal(summary.deferred, 1)
  assert.equal(summary.recipients, 2)
  assert.deepEqual(backlogSummary(undefined), { queued: 0, bytes: 0, deferred: 0, recipients: 0 })
})
