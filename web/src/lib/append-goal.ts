// Pure helpers for the explicit "追加目标并继续" operator action
// (POST /p/<project>/api/project/append-goal, internal/api/goal_continue.go).
//
// Why the action is separate from chat: ordinary messages never resume a paused
// project. This endpoint appends a NEW independent goal and, only when the
// project is parked in a resumable stand-by (completion or a manual pause), clears
// that pause and wakes the commander. A provider fault is refused until a human
// repairs the key/profile, and waiting work is not a pause at all. The console
// must show exactly those distinctions instead of guessing from prose.
//
// The idempotency key belongs to one submitted draft: generated on the first
// submit, reused by retries after a lost response / failed resume, regenerated
// only when the operator edits the content (the server answers 409
// goal_idempotency_conflict for the same key + a different statement).

import { ApiError } from './api.ts'
import type {
  AppendGoalErrorBody,
  AppendGoalRequest,
  AppendGoalResult,
} from './types.ts'

export interface AppendGoalDraft {
  statement: string
  quantified_target: string
  note: string
}

/** Machine pause kinds → console labels. Unknown codes stay visible, never
 *  guessed into a friendlier state. */
export const PAUSE_KIND_LABEL: Readonly<Record<string, string>> = {
  running: '运行中',
  waiting_human: '等待人工',
  waiting_children: '等待子任务',
  waiting_action: '等待外部结果',
  waiting_resume: '等待恢复执行',
  completion_standby: '完成待命',
  manual_pause: '人工暂停',
  provider_fault: '模型/凭据故障暂停',
}

export function pauseKindLabel(kind?: string): string {
  if (!kind) return '未知状态'
  return PAUSE_KIND_LABEL[kind] ?? kind
}

/** Content identity of a draft, normalised the same way the server trims it. */
export function appendGoalFingerprint(draft: AppendGoalDraft): string {
  return JSON.stringify([
    draft.statement.trim(),
    draft.quantified_target.trim(),
    draft.note.trim(),
  ])
}

export interface AppendGoalKey {
  fingerprint: string
  key: string
}

export interface PendingAppendGoal { draft: AppendGoalDraft; key: AppendGoalKey; goalId?: string }
type DraftStorage = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>
const pendingName = (project: string) => `tw:pending-goal:${project}`

export function loadPendingAppend(storage: DraftStorage, project: string): PendingAppendGoal | null {
  try {
    const value = JSON.parse(storage.getItem(pendingName(project)) ?? 'null') as PendingAppendGoal | null
    if (!value || typeof value.draft?.statement !== 'string' || typeof value.draft?.quantified_target !== 'string' || typeof value.draft?.note !== 'string' || typeof value.key?.key !== 'string') return null
    return value.key.fingerprint === appendGoalFingerprint(value.draft) ? value : null
  } catch { return null }
}

export function savePendingAppend(storage: DraftStorage, project: string, value: PendingAppendGoal | null): void {
  try {
    if (value) storage.setItem(pendingName(project), JSON.stringify(value))
    else storage.removeItem(pendingName(project))
  } catch { /* Browser storage may be disabled; in-memory retries still work. */ }
}

/**
 * One key per submitted content. Same draft → same key (a retry must find the
 * goal it already created); edited draft → a fresh key (a new goal must not
 * collide with the old key).
 */
export function appendGoalKeyFor(
  prev: AppendGoalKey | null,
  draft: AppendGoalDraft,
  generate: () => string,
): AppendGoalKey {
  const fingerprint = appendGoalFingerprint(draft)
  if (prev && prev.fingerprint === fingerprint) return prev
  return { fingerprint, key: generate() }
}

/** A random key in the same style as the console's other idempotency keys. */
export function randomAppendGoalKey(): string {
  const words = crypto.getRandomValues(new Uint32Array(4))
  return `ag_${Array.from(words, (n) => n.toString(16).padStart(8, '0')).join('')}`
}

/** Request body: empty optional fields are omitted, the statement is trimmed. */
export function appendGoalRequest(draft: AppendGoalDraft, key: string): AppendGoalRequest {
  const body: AppendGoalRequest = {
    statement: draft.statement.trim(),
    idempotency_key: key,
  }
  const quantified = draft.quantified_target.trim()
  if (quantified) body.quantified_target = quantified
  const note = draft.note.trim()
  if (note) body.note = note
  return body
}

export interface AppendGoalFailure {
  goalId?: string
  /** Machine code (`goal_invalid`, `project_fault_paused`, …); '' when unknown. */
  code: string
  /** The server's own message, shown verbatim. */
  message: string
  /** What the operator can do next; absent when the message already says it. */
  hint?: string
  /** A human must repair the key/profile before a retry can succeed. */
  needsHuman: boolean
  /** A retry with the same idempotency key is meaningful (the goal may exist). */
  retryable: boolean
  /** Server-supplied pause reason, when the failure is a pause state. */
  pauseReason?: string
}

/**
 * Map an append-goal failure onto what the dialog shows. The server's message is
 * kept verbatim (exact error copy); the hint only explains the retry contract.
 */
export function appendGoalFailure(err: unknown): AppendGoalFailure {
  const apiErr = err instanceof ApiError ? err : null
  const body =
    apiErr?.body && typeof apiErr.body === 'object'
      ? (apiErr.body as AppendGoalErrorBody)
      : undefined
  const code = apiErr?.code ?? body?.code ?? ''
  const message = err instanceof Error ? err.message : String(err)
  switch (code) {
    case 'project_fault_paused':
      return {
        code,
        message: body?.goal ? '目标已创建，但模型故障阻止项目恢复。' : '模型服务故障，项目保持暂停。',
        goalId: body?.goal?.id,
        hint: '先修复模型连接，再安全重试；已创建的目标会保留。',
        needsHuman: body?.needs_human ?? true,
        retryable: Boolean(body?.retryable),
        pauseReason: body?.pause_reason,
      }
    case 'resume_failed':
      return {
        code,
        message: '目标已创建，但项目尚未恢复。',
        goalId: body?.goal?.id,
        hint: '请安全重试继续；系统会恢复同一个目标。',
        needsHuman: false,
        // The contract says resume_failed is retryable even if the body omits it.
        retryable: body?.retryable ?? true,
        pauseReason: body?.pause_reason,
      }
    case 'goal_idempotency_conflict':
      return {
        code,
        message: '本次目标与上次提交不同，请核对后重新提交。',
        hint: '本次提交与之前的目标不一致，本次没有新建目标。请核对内容后重新提交。',
        needsHuman: false,
        retryable: false,
      }
    case 'goal_invalid':
      return { code, message, needsHuman: false, retryable: false }
    case 'goal_append_failed':
      return { code, message, needsHuman: false, retryable: Boolean(body?.retryable) }
    default:
      return {
        code,
        message,
        needsHuman: Boolean(body?.needs_human),
        retryable: Boolean(body?.retryable),
        pauseReason: body?.pause_reason,
      }
  }
}

/** The success receipt the dialog shows: the server's `note` plus the structured
 *  facts (created vs duplicate, resumed from which pause, commander woken). */
export interface AppendGoalOutcome {
  title: string
  note: string
  created: boolean
  resumed: boolean
  resumedFrom?: string
  deciderWoken: boolean
  pausedAfter: boolean
  pauseKindAfter?: string
}

export function appendGoalOutcome(result: AppendGoalResult): AppendGoalOutcome {
  return {
    title: result.created ? '目标已追加' : '目标已存在（未重复追加）',
    note: result.note ?? '',
    created: result.created,
    resumed: Boolean(result.resumed),
    resumedFrom: result.resumed_from || undefined,
    deciderWoken: Boolean(result.decider_woken),
    pausedAfter: Boolean(result.paused_after),
    pauseKindAfter: result.pause_kind_after,
  }
}
