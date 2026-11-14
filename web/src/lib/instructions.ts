// Pure helpers for the persistent-instruction surface.
//
// A persistent instruction is operator-owned state, not a chat turn: one row,
// versioned, revocable, and rendered into every later prompt that its scope
// applies to. The console therefore treats the set as a list to manage, not as
// a message log: creation, correction (v+1 replaces the text) and revocation
// all happen against one identified row. Appending a second sentence about the
// same topic is what makes an executor read two hard constraints at once.
import { ApiError } from './api.ts'
import type { Instruction, Message } from './types.ts'

export type InstructionFilter = 'active' | 'all'

/** How the operator reads a scope in a list: project-wide or a direction subtree. */
export function instructionScopeLabel(row: Pick<Instruction, 'intent_id'>): string {
  return row.intent_id ? `方向 ${row.intent_id} 及后代` : '整个项目'
}

/**
 * Active first, then most recently updated first. The current set reads
 * top-down, so an older sentence that a newer one replaced is never above it.
 */
export function sortInstructions(rows: Instruction[]): Instruction[] {
  return [...rows].sort((a, b) => {
    if ((a.state === 'active') !== (b.state === 'active')) return a.state === 'active' ? -1 : 1
    return b.updated_at.localeCompare(a.updated_at)
  })
}

export function activeInstructions(rows: Instruction[]): Instruction[] {
  return rows.filter(row => row.state === 'active')
}

/** One line that says how big the current hard-constraint set is. */
export function instructionSummary(rows: Instruction[]): string {
  const active = activeInstructions(rows).length
  if (active === 0) return '当前没有有效持久指令'
  const revoked = rows.length - active
  return revoked > 0 ? `当前生效 ${active} 条，另有 ${revoked} 条已撤销` : `当前生效 ${active} 条`
}

/** Short preview for a list row or the composer's inline set. */
export function instructionPreview(text: string, max = 120): string {
  const flat = text.replace(/\s+/g, ' ').trim()
  return flat.length > max ? `${flat.slice(0, max)}…` : flat
}

/**
 * An instruction-change delivery notice. It is a pointer ("instruction X is now
 * vN"), not the constraint: the body lives in the instruction row, which the
 * 持久指令 page owns. The conversation therefore folds these out of the stream
 * instead of pretending they are dialogue — the raw delivery stays visible under
 * 原生 IOA.
 */
export function isInstructionNotice(m: Pick<Message, 'kind'>): boolean {
  return m.kind === 'instruction'
}

/**
 * How many distinct instruction revisions the notices announce. One revision
 * fans out to several recipients (commander plus every live direction), so
 * counting rows would overstate it: 一次变更 = 一个 id@version。
 */
export function instructionNoticeChanges(
  messages: Pick<Message, 'kind' | 'instruction_id' | 'instruction_version'>[],
): number {
  const seen = new Set<string>()
  let unnamed = 0
  for (const m of messages) {
    if (!isInstructionNotice(m)) continue
    if (!m.instruction_id) { unnamed++; continue }
    seen.add(`${m.instruction_id}@${m.instruction_version ?? 0}`)
  }
  return seen.size + unnamed
}

/** Newest notice first by ledger time, so the folded row can say when it changed. */
export function newestInstructionNotice<T extends Pick<Message, 'kind' | 'created_at'>>(
  messages: T[],
): T | undefined {
  let newest: T | undefined
  for (const m of messages) {
    if (!isInstructionNotice(m)) continue
    if (!newest || m.created_at > newest.created_at) newest = m
  }
  return newest
}

export interface CreateInstructionDraft {
  text: string
  intentID: string
}

/**
 * POST /api/instructions body. An empty intent id means project scope; the
 * server validates the direction exists and that the caller is the operator.
 */
export function createInstructionRequest(
  draft: CreateInstructionDraft,
  idempotencyKey: string,
): { text: string; intent_id?: string; idempotency_key: string } {
  const body: { text: string; intent_id?: string; idempotency_key: string } = {
    text: draft.text.trim(),
    idempotency_key: idempotencyKey,
  }
  const intentID = draft.intentID.trim()
  if (intentID) body.intent_id = intentID
  return body
}

/** Same text + same scope = the same create request; a retry must not duplicate. */
export function createInstructionFingerprint(draft: CreateInstructionDraft): string {
  return JSON.stringify([draft.text.trim(), draft.intentID.trim()])
}

/** A create request may be retried only against the version it was opened at. */
export function instructionStaleError(): string {
  return '这条指令已被别处更正（版本冲突）：刷新列表后重新打开，再保存。'
}

/**
 * 409 is a stale version (someone else corrected the row) or an idempotency
 * clash; 400 keeps the server's own reason (empty text, bad scope).
 */
export function instructionErrorMessage(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 409) {
      return error.message
        ? `版本冲突：${error.message}；刷新列表后重新打开再保存，不要覆盖别人的更正。`
        : instructionStaleError()
    }
    return error.message
  }
  return error instanceof Error ? error.message : String(error)
}
