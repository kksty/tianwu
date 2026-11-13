// Read-only types and pure helpers for the focus projection. They mirror
// internal/retrieval/focus.go: the projection is task-scoped (hot/warm computed
// from recorded references) while the cold layer is the project's remaining
// facts and stays queryable. Nothing here writes or reclassifies: the server
// owns the classification, the console only renders and pages it.

/** Server layer values. "focus" is the default bounded index (hot + warm). */
export type FocusLayer = 'focus' | 'hot' | 'warm' | 'cold'
export type FocusTemperature = 'hot' | 'warm' | 'cold'

export interface FocusFact {
  id: string
  kind?: string
  title?: string
  summary?: string
  /** recorded fact status, or "missing"/"rejected" for an absent/rejected premise */
  status: string
  temperature: FocusTemperature
  /** recorded reasons; one row can carry several */
  reasons: string[]
  claim_version?: number
  /** absent means "version unknown", never a fabricated 0 */
  record_version?: number
  source_run_id?: string
  observed_at?: string
  /** read path for the full record, e.g. /api/facts/<id> */
  query: string
}

export interface FocusPage {
  intent_id: string
  layer: FocusLayer
  facts: FocusFact[]
  hot: number
  warm: number
  cold: number
  index_query: string
  cold_query: string
  fact_query: string
  premises: string
  experience_query: string
  next_cursor?: string
  has_more: boolean
  total: number
  snapshot_version: { epoch: string; seq: number }
}

export interface FocusRequest {
  intent: string
  layer?: FocusLayer
  cursor?: string
  limit?: number
}

/** The layer switch: default index first, cold last. */
export const FOCUS_LAYERS: readonly { id: FocusLayer; label: string; hint: string }[] = [
  { id: 'focus', label: '焦点', hint: '热 + 温：本次派发的有界索引' },
  { id: 'hot', label: '热', hint: '必要前提、固定、参考' },
  { id: 'warm', label: '温', hint: '来源链、同一记录实体的跨分支观察' },
  { id: 'cold', label: '冷', hint: '未被本任务引用的其余事实，始终可检索' },
]

const REASON_LABELS: Record<string, string> = {
  required: '必要前提',
  pinned: '固定',
  reference: '参考',
  provenance: '来源链',
  same_recorded_entity: '同实体（跨分支）',
}

const STATUS_LABELS: Record<string, string> = {
  unverified: '未复核',
  verified: '已复核',
  rejected: '已否定',
  missing: '缺失',
}

/** Human label for a recorded classification reason; unknown reasons stay raw. */
export function focusReasonLabel(reason: string): string {
  return REASON_LABELS[reason] ?? reason
}

/** Human label for a fact/premise state; "missing" and "rejected" stay distinct. */
export function focusStatusLabel(status: string): string {
  return STATUS_LABELS[status] ?? status
}

export function focusLayerLabel(layer: string): string {
  return FOCUS_LAYERS.find((item) => item.id === layer)?.label ?? layer
}

export function focusLayerHint(layer: string): string {
  return FOCUS_LAYERS.find((item) => item.id === layer)?.hint ?? ''
}

/** Query params for one page. The default layer is omitted so the URL stays the
 * same index the prompt names; cursor and limit are transport, not identity. */
export function focusSearchParams(request: FocusRequest): URLSearchParams {
  const params = new URLSearchParams()
  params.set('intent', request.intent)
  if (request.layer && request.layer !== 'focus') params.set('layer', request.layer)
  if (request.cursor) params.set('cursor', request.cursor)
  params.set('limit', String(request.limit ?? 50))
  return params
}

/** Append a page to the loaded list. A new intent, layer or snapshot replaces
 * the list: mixing two frozen snapshots would show duplicate or shifted rows. */
export function mergeFocusPages(previous: FocusPage | undefined, next: FocusPage): FocusPage {
  if (
    !previous ||
    previous.intent_id !== next.intent_id ||
    previous.layer !== next.layer ||
    previous.snapshot_version?.seq !== next.snapshot_version?.seq
  ) {
    return next
  }
  return { ...next, facts: [...previous.facts, ...next.facts] }
}

/** Project-scoped href for a row's read path. The server returns an API path
 * without the project prefix; the console lives under /p/<project>/. */
export function focusFactQueryHref(query: string, project: string): string {
  if (!query) return ''
  if (!project) return query
  return `/p/${encodeURIComponent(project)}${query}`
}

/** One line naming the frozen snapshot the page was cut from. */
export function focusSnapshotLabel(page: FocusPage | undefined): string {
  if (!page) return ''
  return `快照 ${page.snapshot_version.epoch.slice(-8)} #${page.snapshot_version.seq}`
}

/** The count strip: the whole frozen projection, not just this page. */
export function focusCounts(page: FocusPage | undefined): { hot: number; warm: number; cold: number } {
  return { hot: page?.hot ?? 0, warm: page?.warm ?? 0, cold: page?.cold ?? 0 }
}
