import { test } from 'node:test'
import assert from 'node:assert/strict'
import { chatIncludesExecutors, chatSummary } from '../src/lib/chat.ts'
import type { ChatResult } from '../src/lib/types.ts'

test('only the "all" target includes executors', () => {
  assert.equal(chatIncludesExecutors('commander'), false)
  assert.equal(chatIncludesExecutors('all'), true)
})

test('a commander-only turn reports the row and makes no executor claim', () => {
  const summary = chatSummary({
    to: 'commander',
    commander: { message_id: 'm_1', status: 'queued' },
    delivered: [],
    skipped: [],
  })
  assert.match(summary.commander, /指挥官：排队，指挥官下一轮可见（m_1）/)
  assert.equal(summary.executors, '')
  assert.equal(summary.skipped, '')
})

test('an all turn splits live deliveries from queued ones and names skipped directions', () => {
  const result: ChatResult = {
    to: 'all',
    commander: { message_id: 'm_1', status: 'accepted' },
    delivered: [
      { intent_id: 'in_a', participant_id: 'pt_in_a', title: '侦察方向', status: 'accepted' },
      { intent_id: 'in_b', participant_id: 'pt_in_b', title: '应用层方向', status: 'queued' },
    ],
    skipped: [{ intent_id: 'in_c', participant_id: 'pt_in_c', title: '组件方向', status: 'parked' }],
  }
  const summary = chatSummary(result)
  assert.match(summary.commander, /已送入指挥官现场/)
  assert.match(summary.executors, /执行者：1 个已送达，1 个排队（下一轮可见）/)
  assert.match(summary.skipped, /跳过 1 个已结束\/停放方向：组件方向（停放）/)
})

test('an all turn with no eligible executors says so', () => {
  const summary = chatSummary({
    to: 'all',
    commander: { message_id: 'm_2', status: 'queued' },
    delivered: [],
    skipped: [],
  })
  assert.equal(summary.executors, '执行者：当前没有可接收的执行者')
})

test('the skipped list is clipped to three names', () => {
  const result: ChatResult = {
    to: 'all',
    commander: { message_id: 'm_3', status: 'queued' },
    delivered: [],
    skipped: ['a', 'b', 'c', 'd'].map((id) => ({
      intent_id: `in_${id}`,
      participant_id: `pt_in_${id}`,
      title: `方向${id}`,
      status: 'done',
    })),
  }
  const summary = chatSummary(result)
  assert.match(summary.skipped, /…$/)
  assert.equal((summary.skipped.match(/方向[a-d]/g) ?? []).length, 3)
})
