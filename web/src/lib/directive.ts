// Pure helpers for the unified 下发 (directive) box.
//
// One entry point carries three effects, chosen by (target, persistent, steer):
//   persistent instruction  — project-wide or a direction subtree, revocable;
//   one-shot message        — commander or one direction, durable, no instruction;
//   steer                   — stop a running direction's turn and continue with
//                             the note (no durable message).
// Keeping the mapping, retry key and receipt text here means the component and
// node:test agree on what is sent and what the operator is told.
import { ApiError } from './api.ts'
import type {
  DirectiveKind,
  DirectiveRequest,
  DirectiveResponse,
  DirectiveTarget,
  Intent,
} from './types.ts'

/** A settled direction cannot receive a one-shot prompt (server 409). */
const FINISHED_STATUSES = new Set(['done', 'failed', 'parked'])

export function isDirectionFinished(status?: string): boolean {
  return Boolean(status && FINISHED_STATUSES.has(status))
}

export interface DirectiveDraft {
  text: string
  persistent: boolean
  target: DirectiveTarget
  intentID: string
  steer: boolean
}

/**
 * The exact request body. The UI cannot produce the two invalid combinations
 * (persistent+commander, one-shot+project), but the builder enforces the
 * steer rule too: a persistent instruction is never also a steer.
 */
export function directiveRequest(draft: DirectiveDraft, idempotencyKey: string): DirectiveRequest {
  const body: DirectiveRequest = {
    text: draft.text.trim(),
    target: draft.target,
    persistent: draft.persistent,
    steer: draft.persistent ? false : draft.steer,
    idempotency_key: idempotencyKey,
  }
  if (draft.target === 'intent') body.intent_id = draft.intentID.trim()
  return body
}

/** Same content + same choices = same directive; any change needs a new key. */
export function directiveFingerprint(draft: DirectiveDraft): string {
  return JSON.stringify([
    draft.text.trim(),
    draft.persistent,
    draft.target,
    draft.target === 'intent' ? draft.intentID.trim() : '',
    draft.persistent ? false : draft.steer,
  ])
}

export interface DirectiveKeyState {
  fingerprint: string
  key: string
}

/** Reuse the key for an unchanged directive so a transport retry cannot create
 *  a second row; regenerate as soon as content or choices change. */
export function directiveKeyFor(
  state: DirectiveKeyState,
  fingerprint: string,
  generate: () => string,
): DirectiveKeyState {
  if (state.key && state.fingerprint === fingerprint) return state
  return { fingerprint, key: generate() }
}

/** Directions a one-shot prompt may still reach (not done/failed/parked). */
export function oneShotDirections(intents: Pick<Intent, 'id' | 'title' | 'status'>[]) {
  return intents.filter((intent) => !isDirectionFinished(intent.status))
}

/** Persistent instructions may scope to any direction, settled or not. */
export function persistentDirections(intents: Pick<Intent, 'id' | 'title' | 'status'>[]) {
  return intents
}

/** "立即停并换向" is only offered for a direction whose turn is live: the server
 *  accepts running or claimed and 409s (`directive_steer_unavailable`) otherwise. */
export function canSteer(intent?: Pick<Intent, 'status'>): boolean {
  return intent?.status === 'running' || intent?.status === 'claimed'
}

const DELIVERY_TEXT: Record<string, string> = {
  queued: '已排队，对方下一次运行可见',
  sending: '投递中（不能撤回）',
  accepted: '宿主已接收（未确认已读）',
  confirmed: '对方已确认',
  unknown: '投递结果未知，不要自动重发',
}

function scopeText(result: DirectiveResponse, kind: DirectiveKind): string {
  if (result.target === 'project') return '整个项目'
  if (result.target === 'commander') return '指挥官'
  // An instruction covers the direction and its descendants; a one-shot message
  // reaches only that direction's own participant.
  return kind === 'instruction'
    ? `方向 ${result.intent_id || '（未记录）'} 及其后代`
    : `方向 ${result.intent_id || '（未记录）'}`
}

/** One concise receipt line for the operator: what was created and what it affects. */
export function directiveReceipt(result: DirectiveResponse): string {
  const kind: DirectiveKind = result.kind
  if (kind === 'steer') {
    return `已换向：当前 run 已停，新 run ${result.run_id || '（未记录）'} 继续同一轨迹（不建立持久指令）。`
  }
  if (kind === 'instruction') {
    const version = result.instruction?.version
    const dedupe = result.created === false ? '（重复提交，已复用同一条记录）' : ''
    return `持久指令已保存（v${version ?? '…'}，${scopeText(result, kind)}），后续新任务会继承，直到撤销。${dedupe}`
  }
  const delivery = result.delivery ? (DELIVERY_TEXT[result.delivery] ?? result.delivery) : '已入队'
  const dedupe = result.created === false ? '（重复提交，已复用同一条记录）' : ''
  return `一次性提示已发给${scopeText(result, kind)}：${delivery}；不建立持久指令，也不会注入后续提示词。${dedupe}`
}

/**
 * 409 means "that effect is unavailable now, pick the other one" and carries a
 * machine code: `directive_steer_unavailable` (not running/claimed) or
 * `directive_target_finished` (one-shot to a settled direction). 429 is
 * backpressure: the bytes were not dropped, so retrying the same content is safe.
 */
export function directiveErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    switch (error.code) {
      case 'directive_steer_unavailable':
        return '不能换向：方向不在运行中（或没有可续的会话）。改为不勾选「立即停并换向」，按一次性提示排到它下一轮。'
      case 'directive_target_finished':
        return '方向已结束：一次性提示不能复活它；持续指令仍可创建，或先显式 retry/reopen 再下发。'
      case 'directive_target_missing':
        return '方向不存在：可能已被删除，刷新方向列表后重选。'
      case 'directive_invalid':
        return error.message
      case 'directive_idempotency_conflict':
        return '同一幂等键已用于不同内容：修改正文或对象后重试。'
      case 'message_backpressure':
        return '收件队列已满（message_backpressure）：服务端没有丢弃这次下发，稍后重试同一内容即可。'
    }
    if (error.status === 409) {
      return '这个动作现在不可用：勾选「立即停并换向」只适用于正在跑的方向；已结束的方向不能收一次性提示（持续指令仍可创建）。'
    }
    if (error.status === 429) {
      return '收件队列已满（message_backpressure）：服务端没有丢弃这次下发，稍后重试同一内容即可。'
    }
    if (error.status === 404) return '方向不存在：可能已被删除，刷新方向列表后重选。'
    return error.message
  }
  return error instanceof Error ? error.message : String(error)
}

/** "在跑方向 N 个" hint for the composer footer; never a delivery guarantee. */
export function runningDirectionsHint(executors: number): string {
  return executors > 0 ? `当前在跑执行者 ${executors} 个` : '当前没有在跑的执行者'
}
