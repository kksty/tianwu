// Pure page algebra for the graph snapshot reader.
//
// Kept free of React and of the `api` runtime module so `node --test` can load
// it directly (Node strips the type-only import). The hook in graph-data.ts
// owns effects and requests; everything that can be decided from page data
// alone lives here and is tested in tests/graph-data.test.ts.

import type { GraphFactMeta, GraphRead } from './api.ts'

/** Rows one page carries; the batched first screen uses the same size. */
export const GRAPH_PAGE_LIMIT = 200
/** Hard caps for the batched first screen (docs/33 §4.1: 6 pages / 1200 nodes). */
export const GRAPH_BATCH_MAX_PAGES = 6
export const GRAPH_BATCH_MAX_NODES = 1200
/** A deep link is located with at most two pages, not a full batch. */
export const GRAPH_FOCUS_MAX_PAGES = 2

/** Merge rows by id; the later page wins (same snapshot, refreshed row). */
function mergeByID<T extends { id: string }>(a: T[], b: T[]): T[] {
  const out = new Map<string, T>()
  for (const row of a) out.set(row.id, row)
  for (const row of b) out.set(row.id, row)
  return [...out.values()]
}

function mergeOptionalByID<T extends { id: string }>(a?: T[], b?: T[]): T[] | undefined {
  if (!a && !b) return undefined
  return mergeByID(a ?? [], b ?? [])
}

/** Edges are identified by the full relation, so a pin and a reference between
 *  the same two nodes both survive. */
const edgeKey = (e: { source: string; target: string; kind: string }) =>
  `${e.source}/${e.target}/${e.kind}`

function mergeEdges(
  a: { source: string; target: string; kind: string }[],
  b: { source: string; target: string; kind: string }[],
): { source: string; target: string; kind: string }[] {
  const out = new Map<string, { source: string; target: string; kind: string }>()
  for (const e of a) out.set(edgeKey(e), e)
  for (const e of b) out.set(edgeKey(e), e)
  return [...out.values()]
}

/**
 * Append `b` (a later page of the same fixed snapshot) to `a`.
 *
 * The four streams the graph is built from all merge by identity; the envelope
 * (cursor, totals, snapshot version) comes from the later page because that is
 * the one the reader must continue from. Totals are kept when a page omits
 * them so an older server still answers a usable envelope.
 */
export function mergeGraphPages(a: GraphRead, b: GraphRead): GraphRead {
  return {
    ...b,
    facts: mergeByID(a.facts, b.facts),
    intents: mergeByID(a.intents, b.intents),
    goals: mergeByID(a.goals, b.goals),
    edges: mergeEdges(a.edges, b.edges),
    boundary: mergeByID(a.boundary, b.boundary),
    details: mergeOptionalByID(a.details, b.details),
    fact_meta: mergeOptionalByID<GraphFactMeta>(a.fact_meta, b.fact_meta),
    intents_total: b.intents_total ?? a.intents_total,
    facts_total: b.facts_total ?? a.facts_total,
    goals_total: b.goals_total ?? a.goals_total,
    edges_total: b.edges_total ?? a.edges_total,
    boundary_total: b.boundary_total ?? a.boundary_total,
  }
}

/** Snapshot identity of a page: pages of one snapshot share this key. */
export function graphPageKey(page: Pick<GraphRead, 'snapshot_version'>): string {
  return `${page.snapshot_version.epoch}:${page.snapshot_version.seq}`
}

/** Nodes a graph page contributes: facts + intents + goals (steps are derived). */
export function graphNodeCount(
  page: Pick<GraphRead, 'facts' | 'intents' | 'goals'>,
): number {
  return page.facts.length + page.intents.length + page.goals.length
}

/**
 * Whether the batched first screen may stop after `pages` pages.
 *
 * All stop conditions are about honesty, not speed: stop when the snapshot is
 * exhausted, when the bounded batch is spent, when the node budget is reached,
 * or when every intent has arrived (the structural order puts intents first, so
 * the whole direction tree is already on screen).
 *
 * "Every intent arrived" alone is not enough for a readable first screen: a
 * batch that stops before any fact arrived draws each premise as a boundary
 * ("expand") node, i.e. a skeleton with dangles. Only stop there once at least
 * one fact is on hand, or when the snapshot really has no facts at all.
 */
export function batchComplete(page: GraphRead, pages: number): boolean {
  if (pages < 1) return false
  if (!page.has_more) return true
  if (pages >= GRAPH_BATCH_MAX_PAGES) return true
  if (graphNodeCount(page) >= GRAPH_BATCH_MAX_NODES) return true
  if (page.intents_total !== undefined && page.intents.length >= page.intents_total) {
    return page.facts.length > 0 || (page.facts_total ?? 0) === 0
  }
  return false
}

/**
 * Whether a deep-link root (`fact:<id>` / `intent:<id>` / `goal:<id>`) is on
 * the merged pages. Used to stop the two-page focus fetch as soon as the target
 * has been located.
 */
export function graphHasRoot(
  page: Pick<GraphRead, 'facts' | 'intents' | 'goals' | 'boundary'>,
  root: string,
): boolean {
  const sep = root.indexOf(':')
  if (sep <= 0) return root ? page.boundary.some((b) => b.id === root) : false
  const kind = root.slice(0, sep)
  const id = root.slice(sep + 1)
  if (!id) return false
  if (kind === 'fact' && page.facts.some((f) => f.id === id)) return true
  if (kind === 'intent' && page.intents.some((it) => it.id === id)) return true
  if (kind === 'goal' && page.goals.some((g) => g.id === id)) return true
  // Boundary references keep the wire form (`fact:old`), which is also a root
  // the operator can deep-link to.
  return page.boundary.some((b) => b.id === root)
}
