// Graph input filters and deterministic ordering.
//
// Everything here is a pure function of the snapshot arrays: filtering happens
// on the *inputs* of buildGraph, never on the built model. That is the contract
// from docs/33 §4.6/§4.7 — a hidden kind or a time window changes what is drawn,
// never what the ledger knows, and search/focus keep seeing the full model.
//
// The only subtlety is dangling references: buildGraph turns a from_fact_ids
// entry with no matching fact into a boundary node ("expand"). A fact hidden by
// a *display* filter is not missing data, so the filtered intent copy drops the
// reference instead of drawing a fake boundary. An intent whose every premise
// was filtered then starts at the project brief, which is the honest picture.

import type { Fact, Goal, Intent } from '@/lib/types'
import type { GraphEdge, GraphModel, GraphNode } from '@/graph/model'

/** Time window for facts. `all` is the neutral default (no time filtering). */
export type GraphTimeRange = 'all' | '1h' | '24h' | '7d' | '30d'

/** Select options for the filters card; `ms` is the look-back window. */
export const GRAPH_TIME_RANGES: readonly { id: GraphTimeRange; label: string; ms: number }[] = [
  { id: 'all', label: '全部时间', ms: 0 },
  { id: '1h', label: '近 1 小时', ms: 60 * 60 * 1000 },
  { id: '24h', label: '近 24 小时', ms: 24 * 60 * 60 * 1000 },
  { id: '7d', label: '近 7 天', ms: 7 * 24 * 60 * 60 * 1000 },
  { id: '30d', label: '近 30 天', ms: 30 * 24 * 60 * 60 * 1000 },
]

/** Look-back window in ms; unknown/empty values mean "no time filtering". */
export function timeRangeMs(range: GraphTimeRange | undefined): number {
  return GRAPH_TIME_RANGES.find((item) => item.id === range)?.ms ?? 0
}

export interface GraphFilterInput {
  facts: Fact[]
  intents: Intent[]
  goals: Goal[]
}

export interface GraphFilterOptions {
  /** Fact kinds to hide (the chips in the filters card). */
  hiddenKinds?: ReadonlySet<string> | readonly string[]
  /** Fact created_at window; only facts are filtered (steps/goals carry time). */
  timeRange?: GraphTimeRange
  /** Hide finished directions and the facts they produced. */
  hideDone?: boolean
  /** Draw only the focused path; needs a non-null `focusSet` to take effect. */
  focusOnly?: boolean
  /** Node ids on the focused path (`f:`/`s:`/`w:`/`g:` prefixed). */
  focusSet?: ReadonlySet<string> | null
  /** Clock for the time window (tests inject it). */
  now?: number
}

export interface GraphFilterResult {
  facts: Fact[]
  intents: Intent[]
  goals: Goal[]
}

/**
 * A fact is in range when it has no usable timestamp (unknown time must never
 * hide a fact) or when `created_at >= now - window`. The upper bound is not
 * enforced: a clock-skewed future timestamp is not an *old* fact, and hiding
 * rows because a writer's clock is ahead would lose current work.
 */
export function factInTimeRange(createdAt: string | undefined, windowMs: number, now: number): boolean {
  if (windowMs <= 0) return true
  if (!createdAt) return true
  const at = Date.parse(createdAt)
  if (!Number.isFinite(at)) return true
  return at >= now - windowMs
}

function keepOnly(ids: string[] | undefined, keep: ReadonlySet<string>): string[] | undefined {
  if (!ids) return undefined
  const next = ids.filter((id) => keep.has(id))
  return next.length === ids.length ? ids : next
}

/**
 * Drop references to facts that display filters removed. Returns the original
 * object when nothing changed, so untouched intents keep reference identity.
 */
function pruneIntentRefs(intent: Intent, factIDs: ReadonlySet<string>): Intent {
  const from = keepOnly(intent.from_fact_ids, factIDs)
  const context = keepOnly(intent.context_fact_ids, factIDs)
  const pinned = keepOnly(intent.pinned_fact_ids, factIDs)
  if (from === intent.from_fact_ids && context === intent.context_fact_ids && pinned === intent.pinned_fact_ids) {
    return intent
  }
  return {
    ...intent,
    from_fact_ids: from,
    context_fact_ids: context,
    pinned_fact_ids: pinned,
  }
}

/**
 * Filter the graph *inputs* for drawing. Never mutates the snapshot arrays and
 * never drops data anywhere but the returned copies.
 */
export function filterGraphInputs(
  input: GraphFilterInput,
  options: GraphFilterOptions = {},
): GraphFilterResult {
  const hiddenKinds = new Set<string>(options.hiddenKinds ?? [])
  const windowMs = timeRangeMs(options.timeRange)
  const now = options.now ?? Date.now()
  const doneIntentIDs = options.hideDone
    ? new Set(input.intents.filter((intent) => intent.status === 'done').map((intent) => intent.id))
    : null
  // focusOnly without a focus set has nothing to keep, so it is a no-op rather
  // than "hide everything" — an accidental empty focus cannot blank the map.
  const focusSet = options.focusOnly ? (options.focusSet ?? null) : null

  const facts = input.facts.filter((fact) => {
    if (fact.kind && hiddenKinds.has(fact.kind)) return false
    if (!factInTimeRange(fact.created_at, windowMs, now)) return false
    if (doneIntentIDs && fact.intent_id && doneIntentIDs.has(fact.intent_id)) return false
    if (focusSet && !focusSet.has(`f:${fact.id}`)) return false
    return true
  })
  const factIDs = new Set(facts.map((fact) => fact.id))

  const intents = input.intents
    .filter((intent) => {
      if (doneIntentIDs?.has(intent.id)) return false
      if (focusSet && !focusSet.has(`s:${intent.id}`) && !focusSet.has(`w:${intent.id}`)) return false
      return true
    })
    .map((intent) => pruneIntentRefs(intent, factIDs))

  const goals = input.goals.filter((goal) => !focusSet || focusSet.has(`g:${goal.id}`))

  return { facts, intents, goals }
}

/**
 * Deterministic list/keyboard order: shallow first, then top-to-bottom, then
 * left-to-right, then id. The id tie-break matters when nodes share a cell
 * (a step and its ghost ring) — the order must be stable across rebuilds.
 */
export function graphNodeOrder(nodes: readonly GraphNode[]): GraphNode[] {
  return [...nodes].sort((a, b) => {
    if (a.depth !== b.depth) return a.depth - b.depth
    if (a.y !== b.y) return a.y - b.y
    if (a.x !== b.x) return a.x - b.x
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0
  })
}

/** The model slice a breadcrumb needs: the built model also carries `incoming`. */
export type BreadcrumbModel = Pick<GraphModel, 'nodes' | 'edges'> & {
  byId?: ReadonlyMap<string, GraphNode>
  incoming?: ReadonlyMap<string, string[]>
}

function incomingOf(model: BreadcrumbModel): ReadonlyMap<string, string[]> {
  if (model.incoming) return model.incoming
  const byId = model.byId ?? new Map(model.nodes.map((node) => [node.id, node]))
  const incoming = new Map<string, string[]>()
  for (const edge of model.edges) {
    if (!byId.has(edge.source) || !byId.has(edge.target)) continue
    const list = incoming.get(edge.target)
    if (list) list.push(edge.source)
    else incoming.set(edge.target, [edge.source])
  }
  return incoming
}

/**
 * The breadcrumb from the project brief to `id`: shortest upstream path, last
 * node is `id`. Returns [] when the id is unknown or no project is reachable;
 * a cycle cannot loop forever because the BFS keeps a visited set.
 */
export function breadcrumbPath(model: BreadcrumbModel, id: string | null | undefined): GraphNode[] {
  if (!id) return []
  const byId = model.byId ?? new Map(model.nodes.map((node) => [node.id, node]))
  if (!byId.has(id)) return []
  if (byId.get(id)?.type === 'project') return [byId.get(id)!]
  const incoming = incomingOf(model)
  // BFS backwards from the node. `downstream` maps parent id → child id along
  // the BFS tree, so the found project can be followed down to the target.
  const downstream = new Map<string, string>()
  const seen = new Set<string>([id])
  const queue: string[] = [id]
  let projectID: string | null = null
  while (queue.length > 0 && !projectID) {
    const current = queue.shift()!
    const parents = [...(incoming.get(current) ?? [])].sort()
    for (const parent of parents) {
      if (seen.has(parent)) continue
      seen.add(parent)
      downstream.set(parent, current)
      if (byId.get(parent)?.type === 'project') {
        projectID = parent
        break
      }
      queue.push(parent)
    }
  }
  if (!projectID) return []
  const path: GraphNode[] = []
  let cursor: string | undefined = projectID
  while (cursor) {
    const node = byId.get(cursor)
    if (node) path.push(node)
    cursor = downstream.get(cursor)
  }
  return path
}
