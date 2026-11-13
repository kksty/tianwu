// The execution review projects one run's journal into the rows an operator
// audits: what every tool invocation did and how long it took, and what every
// reported model call cost. It is a pure projection of the events the server
// already records; a value the harness did not report stays absent instead of
// becoming a measured zero.
//
// The journal is a tail of the run, so "no result" is not proof that a tool did
// not run. An unpaired call is called interrupted only when a later turn_ended
// row proves the turn closed before the result arrived.

import type { Event } from './types'

export type ReviewKind = 'tool' | 'model'
export type ReviewState = 'completed' | 'failed' | 'running' | 'interrupted'
export type ReviewOrder = 'time' | 'tokens'

export interface ReviewEntry {
  /** Unique within one review; also the list key. */
  key: string
  /** 1-based position on the run's time axis; token sorting keeps it stable. */
  order: number
  kind: ReviewKind
  label: string
  state: ReviewState
  /** Event row the entry points at: the result when paired, else the call/usage. */
  eventIndex: number
  at: string
  /** Whitespace-folded and clipped tool output, when the result carried text. */
  evidence?: string
  /** Harness-measured duration when reported, else call→result wall clock. */
  elapsedMs?: number
  inputTokens?: number
  outputTokens?: number
  cachedInputTokens?: number
  /** Share of the input side that came from cache (0–100). */
  cacheHitPercent?: number
  /** Provider requests this model call needed; >1 means retries. */
  requests?: number
  totalTokens: number
}

export interface RunReview {
  entries: ReviewEntry[]
  /** Tool outcomes only: a model call has no tool lifecycle state. */
  counts: Record<ReviewState, number>
  /** Sum of the usage the entries reported; partial when the tail is partial. */
  totalTokens: number
  /** Usage events the review could read (model calls that reported usage). */
  reportedCalls: number
}

/** The reference clips a tool result to 180 characters; keep the same bound. */
export const REVIEW_EVIDENCE_LIMIT = 180

/** The model row label; the journal has no per-call model name. */
export const REVIEW_MODEL_LABEL = '模型调用'

function timestamp(iso?: string): number {
  const value = iso ? Date.parse(iso) : NaN
  return Number.isNaN(value) ? 0 : value
}

export function evidenceText(text?: string): string {
  return (text ?? '').replace(/\s+/g, ' ').trim().slice(0, REVIEW_EVIDENCE_LIMIT)
}

/**
 * Cached input as a share of reported input. Clamped: a provider can report
 * more cache reads than input tokens when only a prefix was cached, and a
 * review that renders 120% is worse than one that renders 100%.
 */
export function cacheHitPercent(inputTokens?: number, cachedInputTokens?: number): number | undefined {
  if (!inputTokens || inputTokens <= 0) return undefined
  if (cachedInputTokens === undefined || cachedInputTokens < 0) return undefined
  return Math.min(100, (cachedInputTokens / inputTokens) * 100)
}

/** "250ms" / "1.4s"; an unreported duration renders as nothing, never 0ms. */
export function fmtReviewElapsed(ms?: number): string {
  if (ms === undefined || ms < 0) return ''
  if (ms < 1000) return `${Math.round(ms)}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

type Call = { index: number; event: Event; paired: boolean }

/** The protocol payload shapes a journal line records in `raw`. */
type RawToolIdentity = {
  tool_call?: { id?: unknown }
  tool_result?: { call_id?: unknown; duration_ms?: unknown }
}

/**
 * The call identity as the journal recorded it. New lines put it on the event
 * itself (call_id); lines written before that capture still carry the same
 * string inside the recorded protocol payload. Reading it is the same recorded
 * identity, not a guess, and it is what keeps parallel calls correctly paired
 * on the run history the operator already has.
 */
function protocolCallID(event: Event): string | undefined {
  if (event.call_id) return event.call_id
  const raw = event.raw
  if (!raw || typeof raw !== 'object') return undefined
  const payload = raw as RawToolIdentity
  const value = event.kind === 'tool_call' ? payload.tool_call?.id : payload.tool_result?.call_id
  return typeof value === 'string' && value.length > 0 ? value : undefined
}

/**
 * The harness-measured duration as the journal recorded it. New lines put it on
 * the event itself (duration_ms); lines written before that capture still carry
 * it inside the recorded protocol payload, where protojson encodes uint64 as a
 * string. It is the same measurement, not a derived guess.
 */
function protocolDurationMs(event: Event): number | undefined {
  if (event.duration_ms !== undefined && event.duration_ms > 0) return event.duration_ms
  const raw = event.raw
  if (!raw || typeof raw !== 'object') return undefined
  const value = (raw as RawToolIdentity).tool_result?.duration_ms
  const parsed = typeof value === 'number' ? value : typeof value === 'string' ? Number(value) : NaN
  return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined
}

function toolElapsed(call: Call | undefined, result: Event): number | undefined {
  const reported = protocolDurationMs(result)
  if (reported !== undefined) return reported
  if (!call) return undefined
  const start = timestamp(call.event.at)
  const end = timestamp(result.at)
  if (!start || !end || end < start) return undefined
  return end - start
}

function matchCall(
  calls: readonly Call[],
  callsByID: ReadonlyMap<string, Call>,
  callID: string | undefined,
  result: Event,
): Call | undefined {
  // Identity first: a result with a call id pairs only with that call. When the
  // call is outside the tail, the result stays its own unpaired row instead of
  // silently attaching to a different invocation.
  if (callID) {
    const call = callsByID.get(callID)
    return call && !call.paired ? call : undefined
  }
  // Older journals kept no call ids: pair with the earliest unpaired call of
  // the same tool, else the earliest unpaired call for a result that did not
  // repeat the name. Parallel calls of one tool therefore keep their recorded
  // order, which is the only identity such a journal has.
  for (const call of calls) if (!call.paired && call.event.tool === result.tool) return call
  for (const call of calls) if (!call.paired) return call
  return undefined
}

export function buildRunReview(events: readonly Event[]): RunReview {
  const entries: ReviewEntry[] = []
  const calls: Call[] = []
  const callsByID = new Map<string, Call>()
  // turn_ended proves the turn closed: a call recorded before it that never got
  // a result was interrupted. A call after the last turn_ended (or in a tail
  // with none) belongs to a turn that is still running.
  let lastTurnEnd = -1
  for (let index = 0; index < events.length; index++) {
    if (events[index].kind === 'turn_ended') lastTurnEnd = index
  }

  for (let index = 0; index < events.length; index++) {
    const event = events[index]
    if (event.kind === 'tool_call') {
      const call: Call = { index, event, paired: false }
      calls.push(call)
      // The first call wins a duplicate id: a later result must not steal an
      // earlier call's identity.
      const callID = protocolCallID(event)
      if (callID && !callsByID.has(callID)) callsByID.set(callID, call)
      continue
    }
    if (event.kind === 'tool_result') {
      const call = matchCall(calls, callsByID, protocolCallID(event), event)
      if (call) call.paired = true
      entries.push({
        key: `tool:${call ? call.index : `result:${index}`}`,
        order: 0,
        kind: 'tool',
        label: event.tool || call?.event.tool || '工具',
        // ok is written only for a successful result (ok,omitempty): an absent
        // ok is the harness's is_error=true, not an unknown outcome.
        state: event.ok === true ? 'completed' : 'failed',
        eventIndex: index,
        at: event.at || call?.event.at || '',
        evidence: evidenceText(event.text) || undefined,
        elapsedMs: toolElapsed(call, event),
        totalTokens: 0,
      })
      continue
    }
    if (event.kind === 'usage') {
      const input = event.input_tokens
      const output = event.output_tokens
      entries.push({
        key: `usage:${index}`,
        order: 0,
        kind: 'model',
        label: REVIEW_MODEL_LABEL,
        // A usage record is a call that answered. Calls that reported nothing
        // are counted in usage_missing on the turn, not invented into rows.
        state: 'completed',
        eventIndex: index,
        at: event.at,
        inputTokens: input,
        outputTokens: output,
        cachedInputTokens: event.cached_input_tokens,
        cacheHitPercent: cacheHitPercent(input, event.cached_input_tokens),
        requests: event.usage_requests,
        totalTokens: event.tokens ?? (input ?? 0) + (output ?? 0),
      })
    }
  }

  for (const call of calls) {
    if (call.paired) continue
    entries.push({
      key: `tool:${call.index}`,
      order: 0,
      kind: 'tool',
      label: call.event.tool || '工具',
      state: call.index < lastTurnEnd ? 'interrupted' : 'running',
      eventIndex: call.index,
      at: call.event.at,
      totalTokens: 0,
    })
  }

  // Both passes above produced entries in event order; a stable sort by the
  // recorded timestamp keeps that order when timestamps tie or are missing.
  entries.sort((a, b) => timestamp(a.at) - timestamp(b.at) || a.eventIndex - b.eventIndex)
  const counts: Record<ReviewState, number> = { completed: 0, failed: 0, running: 0, interrupted: 0 }
  let totalTokens = 0
  let reportedCalls = 0
  entries.forEach((entry, index) => {
    entry.order = index + 1
    if (entry.kind === 'tool') counts[entry.state] += 1
    else reportedCalls += 1
    totalTokens += entry.totalTokens
  })
  return { entries, counts, totalTokens, reportedCalls }
}

/**
 * Token order is a view, not a renumbering: each row keeps the `order` it had
 * on the time axis so the operator can still point back at the step.
 */
export function orderReviewEntries(entries: readonly ReviewEntry[], order: ReviewOrder): ReviewEntry[] {
  if (order === 'time') return [...entries]
  return [...entries].sort((a, b) => b.totalTokens - a.totalTokens || a.order - b.order)
}
