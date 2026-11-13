// Intent context editing: the pure selection helpers plus the one write the
// editor performs.
//
// The selection is the *whole* explicit context of a direction — references,
// pins and interest entities — and it tags along with the context_version the
// editor read. The server applies it under that version (PUT
// /api/intents/{id}/context), so a stale editor gets 409 instead of silently
// overwriting another edit, and a foreign participant gets 403. Hard
// prerequisites (intent.from_fact_ids) are deliberately absent here: a reference
// is a reading choice, a premise is a gate.
//
// The console keeps all HTTP in lib/api.ts, but that file is owned by the
// phase-1B routing work while this editor lands. The write therefore lives here
// next to its version/conflict handling, with the same ApiError status/code
// carry-through so callers can render 409/403 without parsing prose.
import { ApiError, projectID } from './api.ts'
import type { Intent, IntentContextRequest } from './types.ts'

/** The selection an editor opened: exactly the fields the PUT replaces. */
export interface IntentContextSelection {
  version: number
  references: string[]
  pins: string[]
  entities: string[]
}

/** Reads the selection (and the version the editor will send) off an intent row. */
export function intentContextSelection(intent: Intent): IntentContextSelection {
  return {
    version: intent.context_version ?? 0,
    references: [...(intent.context_fact_ids ?? [])],
    pins: [...(intent.pinned_fact_ids ?? [])],
    entities: [...(intent.interest_entity_refs ?? [])],
  }
}

function sameList(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((value, i) => value === b[i])
}

/** Order matters: the store keeps the operator's order, not a ranking. */
export function sameContextSelection(a: IntentContextSelection, b: IntentContextSelection): boolean {
  return (
    sameList(a.references, b.references) &&
    sameList(a.pins, b.pins) &&
    sameList(a.entities, b.entities)
  )
}

/** Add when absent, remove when present; keeps the caller's order. */
export function toggleContextID(ids: string[], id: string): string[] {
  return ids.includes(id) ? ids.filter((value) => value !== id) : [...ids, id]
}

/** Draft text for interest entities: one recorded identity per line. */
export function parseEntityRefs(text: string): string[] {
  const out: string[] = []
  for (const line of text.split(/[\n,，、]/)) {
    const ref = line.trim()
    if (ref && !out.includes(ref)) out.push(ref)
  }
  return out
}

export function formatEntityRefs(entities: string[]): string {
  return entities.join('\n')
}

/** Local fact-picker filter: id or title, case-insensitive, stable order. */
export function filterContextFacts<T extends { id: string; title: string }>(
  facts: T[],
  query: string,
  limit = 20,
): T[] {
  const needle = query.trim().toLowerCase()
  if (!needle) return facts.slice(0, limit)
  const matches = facts.filter(
    (fact) =>
      fact.id.toLowerCase().includes(needle) ||
      (fact.title ?? '').toLowerCase().includes(needle),
  )
  return matches.slice(0, limit)
}

/** Stable text label for a selected fact whose row is not in the loaded page. */
export function contextFactLabel(id: string, facts: { id: string; title: string }[]): string {
  return facts.find((fact) => fact.id === id)?.title ?? '事实不在最近列表中（id 仍会提交）'
}

export type ContextErrorKind = 'conflict' | 'forbidden' | 'missing' | 'invalid' | 'unknown'

/** Maps the server's status/code to the editor's three distinct outcomes. */
export function contextErrorKind(error: unknown): ContextErrorKind {
  if (error instanceof ApiError) {
    if (error.status === 409) return 'conflict'
    if (error.status === 403) return 'forbidden'
    if (error.status === 404) return 'missing'
    if (error.status === 400) return 'invalid'
    if (error.code === 'context_denied') return 'forbidden'
  }
  return 'unknown'
}

/**
 * 409 must read "someone else changed it; refresh and retry", not be retried
 * blindly. 403 is a boundary denial (participant editing a foreign direction),
 * not a concurrent edit. Anything else keeps the server's own reason.
 */
export function contextErrorMessage(error: unknown): string {
  switch (contextErrorKind(error)) {
    case 'conflict':
      return '已被他人修改，刷新后重试'
    case 'forbidden':
      return '只能更新自己的方向（边界拒绝）'
    case 'missing':
      return '意图不存在或已删除'
    case 'invalid':
      return error instanceof Error ? error.message : '上下文请求无效'
    default:
      return error instanceof Error ? error.message : String(error)
  }
}

/**
 * PUT the whole selection with the version the editor displayed. 401 mirrors
 * lib/api.ts: the console redirects to the login form and comes back to this
 * page, because an expired session is not an editor error.
 */
export async function putIntentContext(
  intentID: string,
  body: IntentContextRequest,
): Promise<Intent> {
  const project = projectID()
  const base = project ? `/p/${encodeURIComponent(project)}` : ''
  const res = await fetch(`${base}/api/intents/${encodeURIComponent(intentID)}/context`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const text = await res.text()
  let payload: unknown = null
  if (text) {
    try {
      payload = JSON.parse(text)
    } catch {
      payload = text
    }
  }
  if (!res.ok) {
    if (res.status === 401) {
      const next = encodeURIComponent(
        window.location.pathname + window.location.search + window.location.hash,
      )
      window.location.assign(`/login?next=${next}`)
      throw new ApiError(401, '需要登录')
    }
    const message =
      payload && typeof payload === 'object' && 'error' in payload
        ? String((payload as { error: unknown }).error)
        : `HTTP ${res.status}`
    const code =
      payload && typeof payload === 'object' && typeof (payload as { code?: unknown }).code === 'string'
        ? (payload as { code: string }).code
        : undefined
    throw new ApiError(res.status, message, code)
  }
  return payload as Intent
}
