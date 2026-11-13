// Behaviour tests for the execution review projection. node:test + type
// stripping, no DOM: the review decides what the run page reports about tool
// outcomes, elapsed time and model cost — including the case where the journal
// has no call ids, because those logs must not read as "every tool failed".
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  buildRunReview,
  cacheHitPercent,
  fmtReviewElapsed,
  orderReviewEntries,
} from '../src/lib/run-review.ts'
import type { Event } from '../src/lib/types.ts'

/** A fixed clock; only relative order and deltas matter. */
function at(seconds: number): string {
  return new Date(Date.UTC(2026, 10, 9, 0, 0, 0) + seconds * 1000).toISOString()
}

function ev(kind: string, seconds: number, over: Partial<Event> = {}): Event {
  return { kind, at: at(seconds), ...over }
}

test('tool results pair with calls by call id even when results arrive out of order', () => {
  const events: Event[] = [
    ev('tool_call', 0, { tool: 'bash', call_id: 'call-a' }),
    ev('tool_call', 1, { tool: 'read', call_id: 'call-b' }),
    ev('tool_result', 2, { tool: 'read', call_id: 'call-b', ok: true, text: 'file body', duration_ms: 40 }),
    ev('tool_result', 3, { tool: 'bash', call_id: 'call-a', ok: true, text: 'command output', duration_ms: 120 }),
  ]
  const review = buildRunReview(events)
  const tools = review.entries.filter((entry) => entry.kind === 'tool')

  // Time order is the result order, but each label/evidence belongs to its own
  // call: the read result must not be attributed to the bash call.
  assert.deepEqual(tools.map((entry) => entry.label), ['read', 'bash'])
  assert.deepEqual(tools.map((entry) => entry.evidence), ['file body', 'command output'])
  assert.deepEqual(tools.map((entry) => entry.elapsedMs), [40, 120])
  assert.deepEqual(tools.map((entry) => entry.eventIndex), [2, 3])
  assert.deepEqual(review.counts, { completed: 2, failed: 0, running: 0, interrupted: 0 })
})

test('a journal written before call ids were lifted still pairs by the recorded protocol id', () => {
  // Older lines carry the ids only inside `raw` (the recorded protocol payload).
  // That is the same recorded identity, so parallel calls must still pair
  // exactly — the run history already on disk stays reviewable.
  const events: Event[] = [
    ev('tool_call', 0, { tool: 'bash', raw: { tool_call: { id: 'call-a', name: 'bash' } } }),
    ev('tool_call', 1, { tool: 'read', raw: { tool_call: { id: 'call-b', name: 'read' } } }),
    ev('tool_result', 2, {
      tool: 'read',
      ok: true,
      text: 'file body',
      raw: { tool_result: { call_id: 'call-b' } },
    }),
    ev('tool_result', 3, {
      tool: 'bash',
      ok: true,
      text: 'command output',
      raw: { tool_result: { call_id: 'call-a' } },
    }),
  ]
  const tools = buildRunReview(events).entries.filter((entry) => entry.kind === 'tool')

  assert.deepEqual(tools.map((entry) => entry.label), ['read', 'bash'])
  assert.deepEqual(tools.map((entry) => entry.evidence), ['file body', 'command output'])
})

test('legacy results without call ids pair by tool name and recorded order', () => {
  // Journals written before the call id was captured have only names and
  // order. Two parallel bash calls must still pair with the two bash results
  // in the order they were recorded.
  const events: Event[] = [
    ev('tool_call', 0, { tool: 'bash' }),
    ev('tool_call', 5, { tool: 'bash' }),
    ev('tool_result', 10, { tool: 'bash', ok: true, text: 'first command' }),
    ev('tool_result', 20, { tool: 'bash', ok: true, text: 'second command' }),
  ]
  const review = buildRunReview(events)
  const tools = review.entries.filter((entry) => entry.kind === 'tool')

  assert.deepEqual(tools.map((entry) => entry.evidence), ['first command', 'second command'])
  // With no reported duration the elapsed time is the call→result delta.
  assert.deepEqual(tools.map((entry) => entry.elapsedMs), [10_000, 15_000])
  assert.equal(review.counts.completed, 2)
})

test('a legacy result pairs with its own tool, not the oldest open call', () => {
  const events: Event[] = [
    ev('tool_call', 0, { tool: 'bash' }),
    ev('tool_call', 1, { tool: 'read' }),
    ev('tool_result', 2, { tool: 'read', ok: true, text: 'file body' }),
  ]
  const review = buildRunReview(events)
  const tool = review.entries.find((entry) => entry.kind === 'tool' && entry.evidence)

  assert.equal(tool?.label, 'read')
  assert.equal(tool?.elapsedMs, 1000)
  // The bash call has no result and no turn_ended: the turn may still be live.
  assert.equal(review.counts.running, 1)
})

test('a reported duration wins over the call→result timestamp delta', () => {
  const events: Event[] = [
    ev('tool_call', 0, { tool: 'bash', call_id: 'c1' }),
    ev('tool_result', 10, { tool: 'bash', call_id: 'c1', ok: true, duration_ms: 250 }),
  ]
  assert.equal(buildRunReview(events).entries[0].elapsedMs, 250)
})

test('a journal written before duration was lifted still reads the recorded measurement', () => {
  // Old lines carry the harness duration only inside `raw`, as the protojson
  // uint64 string. The elapsed column must show that measurement instead of the
  // call→result wall clock, which includes transport latency.
  const events: Event[] = [
    ev('tool_call', 0, { tool: 'bash', raw: { tool_call: { id: 'c1', name: 'bash' } } }),
    ev('tool_result', 10, {
      tool: 'bash',
      ok: true,
      text: 'done',
      raw: { tool_result: { call_id: 'c1', duration_ms: '52' } },
    }),
  ]
  assert.equal(buildRunReview(events).entries[0].elapsedMs, 52)
})

test('an unreported duration and a missing call stay unknown, not zero', () => {
  const review = buildRunReview([ev('tool_result', 0, { tool: 'bash', ok: true, text: 'orphan' })])
  assert.equal(review.entries[0].elapsedMs, undefined)
  assert.equal(fmtReviewElapsed(undefined), '')
  assert.equal(fmtReviewElapsed(0), '0ms')
  assert.equal(fmtReviewElapsed(250), '250ms')
  assert.equal(fmtReviewElapsed(1400), '1.4s')
})

test('token sorting puts the most expensive model call first and keeps step numbers', () => {
  const events: Event[] = [
    ev('usage', 1, { tokens: 100, input_tokens: 90, output_tokens: 10 }),
    ev('usage', 2, { tokens: 500, input_tokens: 400, output_tokens: 100 }),
    ev('tool_call', 3, { tool: 'bash' }),
    ev('tool_result', 4, { tool: 'bash', ok: true, text: 'done' }),
  ]
  const review = buildRunReview(events)
  assert.equal(review.totalTokens, 600)
  assert.equal(review.reportedCalls, 2)

  const byTokens = orderReviewEntries(review.entries, 'tokens')
  assert.deepEqual(byTokens.map((entry) => entry.totalTokens), [500, 100, 0])
  // The time-axis position survives the token view: step 3 is still the tool,
  // even though it is now the last row.
  assert.equal(byTokens[2].kind, 'tool')
  assert.equal(byTokens[2].order, 3)

  const byTime = orderReviewEntries(review.entries, 'time')
  assert.deepEqual(byTime.map((entry) => entry.eventIndex), [0, 1, 3])
})

test('the review is ordered by the recorded timestamp, not by arrival order', () => {
  const review = buildRunReview([
    ev('usage', 5, { tokens: 10 }),
    ev('usage', 1, { tokens: 20 }),
  ])
  assert.deepEqual(review.entries.map((entry) => entry.totalTokens), [20, 10])
})

test('cache hit is the reported cached share of input, clamped to 100%', () => {
  assert.equal(cacheHitPercent(1000, 250), 25)
  // A prefix cache can report more reads than the input side; never show >100%.
  assert.equal(cacheHitPercent(1000, 2000), 100)
  assert.equal(cacheHitPercent(0, 10), undefined)
  assert.equal(cacheHitPercent(undefined, 10), undefined)
  assert.equal(cacheHitPercent(100, undefined), undefined)

  const review = buildRunReview([
    ev('usage', 0, { input_tokens: 1000, output_tokens: 50, cached_input_tokens: 250, tokens: 1050 }),
  ])
  assert.equal(review.entries[0].cacheHitPercent, 25)
  assert.equal(review.entries[0].totalTokens, 1050)
})

test('a model call that needed more than one request is visible as a retry', () => {
  const review = buildRunReview([
    ev('usage', 0, { input_tokens: 10, output_tokens: 2, usage_requests: 3 }),
  ])
  assert.equal(review.entries[0].requests, 3)
})

test('a missed result is interrupted only when a turn_ended proves the turn closed', () => {
  const closed = buildRunReview([
    ev('tool_call', 0, { tool: 'bash' }),
    ev('turn_ended', 1, { stop_reason: 'completed', ok: true }),
  ])
  assert.equal(closed.entries[0].state, 'interrupted')
  assert.equal(closed.counts.interrupted, 1)

  // A live tail has no turn_ended yet: the call may still be running.
  const live = buildRunReview([ev('tool_call', 0, { tool: 'bash' })])
  assert.equal(live.entries[0].state, 'running')
  assert.equal(live.counts.running, 1)
})

test('a result without ok is the harness is_error and counts as failed', () => {
  const review = buildRunReview([
    ev('tool_call', 0, { tool: 'bash', call_id: 'c1' }),
    ev('tool_result', 1, { tool: 'bash', call_id: 'c1', text: 'operation start failed' }),
  ])
  assert.equal(review.entries[0].state, 'failed')
  assert.equal(review.entries[0].evidence, 'operation start failed')
  assert.equal(review.counts.failed, 1)
})

test('evidence is folded to one line and clipped at 180 characters', () => {
  const text = 'line one\n\n line   two ' + 'x'.repeat(300)
  const review = buildRunReview([ev('tool_result', 0, { tool: 'bash', ok: true, text })])
  const evidence = review.entries[0].evidence ?? ''

  assert.equal(evidence.length, 180)
  assert.ok(evidence.startsWith('line one line two xxx'))
})
