// The pure cache core behind the graph model hook (docs/33 §4.6): a
// signature-memoized buildGraph over the ledger snapshot, with the drawing-only
// filters applied on the input side. Kept free of React so the exact cache rules
// (unchanged poll → same model object; a corrected title/summary/claim version or
// goal note rebuilds) are testable without a renderer.
//
// The signature must describe every field the picture and its inspectors read,
// not just the row identities. A correction can change the display title of a
// fact without changing its status, and an assertion correction can bump the
// claim version while the fact stays unverified; either one must rebuild the
// model (F13, docs/35). Conversely the ledger poll returns fresh arrays every
// 6 s, so an unchanged poll must produce the identical key or the canvas
// re-lays out and the operator's view jumps.

import { buildGraph, focusPath, type GraphModel } from '../graph/model.ts'
import { filterGraphInputs, type GraphTimeRange } from './graph-filter.ts'
import type { GraphRead } from '@/lib/api'
import type { Fact, Goal, Intent, ProjectInfo, Run } from '@/lib/types'

/** Drawing-only filters. They never mutate the snapshot; the model is rebuilt
 *  from a filtered projection, so hidden nodes cannot be selected or searched. */
export interface GraphFiltersState {
  /** Fact kinds whose nodes are not drawn (the chips in GraphFilters). */
  hiddenKinds: Set<string>
  /** Recency window over fact.created_at; 'all' keeps everything. */
  timeRange: GraphTimeRange
  /** Hide finished directions (and the output that only they carried). */
  hideDone: boolean
  /** Keep only the chain around the current selection (focusPath). */
  focusOnly: boolean
}

export function defaultGraphFilters(): GraphFiltersState {
  return { hiddenKinds: new Set(), timeRange: 'all', hideDone: false, focusOnly: false }
}

export interface GraphModelSource {
  data?: GraphRead
  project?: ProjectInfo
  runs: Run[]
  profileMeta: Record<string, { tier?: string; model?: string }>
  defaultModel: string
  /** Current graph selection (`f:`/`s:`/`g:`/edge id); only used by focusOnly. */
  selected: string | null
}

/** One cached model plus the key it was built from. */
export interface GraphModelMemo {
  sig: string
  model: GraphModel
}

/** FNV-1a: a cheap deterministic fingerprint. Used only for the legacy rows that
 *  carry no claim version, and for long prose whose full text should not sit in
 *  the cache key; equality of the text is what matters, not the hash strength. */
function digest(text: string): string {
  let h = 0x811c9dc5
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i)
    h = Math.imul(h, 0x01000193)
  }
  return (h >>> 0).toString(16)
}

/**
 * Everything buildGraph, GraphNodePanel and GraphInspector read off a fact.
 *
 * The wire carries the real claim identity: `claim_version` increments whenever
 * the assertion (summary/doc/evidence/tags/proof/context/observation time)
 * changes, so long prose does not need to enter the key. The display title and
 * the review status are deliberately *not* part of the claim (see
 * internal/model.Fact), which is why they are always listed here: a correction
 * that only retitles a fact must still rebuild the model.
 */
function factSignature(f: Fact): string {
  const claim =
    f.claim_version && f.claim_version > 0
      ? `v${f.claim_version}`
      : // Legacy rows predate claim versions: fingerprint the same claim fields
        // rather than trusting an absent version.
        `h${digest(
          [
            f.summary ?? '',
            f.doc ?? '',
            (f.evidence_ids ?? []).join(','),
            (f.tags ?? []).join(','),
            f.proof ?? '',
            f.observed_at ?? '',
            f.execution_context_ref ?? '',
          ].join('\u0000'),
        )}`
  return JSON.stringify([
    f.id,
    f.intent_id,
    claim,
    f.title,
    f.kind,
    f.status,
    f.confidence,
    f.proof ?? '',
    f.entity_ref ?? '',
    f.origin ?? '',
    f.source_ref ?? '',
    f.claim_hash ?? '',
    f.created_at,
    // Relation inputs: goal-evidence ownership is resolved from evidence ids,
    // and the inspector falls back to these arrays when fact_meta is absent.
    f.evidence_ids ?? [],
    (f.refs ?? []).map((r) => `${r.scheme}:${r.locator}`),
    f.tags ?? [],
  ])
}

/** Everything the step pill, the goal card and the panels render off a goal. */
function goalSignature(g: Goal): string {
  return JSON.stringify([
    g.id,
    g.statement,
    g.met,
    g.note ?? '',
    g.evidence_id ?? '',
    g.from ?? [],
    g.start ?? '',
    g.quantified_target ?? '',
    g.completed_at ?? '',
    g.created_at,
    g.receipt_stale ? 1 : 0,
    g.receipt_changed ?? [],
  ])
}

/** Everything the step node and its panel read off a direction. */
function intentSignature(i: Intent): string {
  return JSON.stringify([
    i.id,
    i.title,
    i.description,
    i.doc ?? '',
    i.eval ?? '',
    i.status,
    i.attempts,
    i.created_at,
    i.updated_at,
    i.parent_intent_id ?? '',
    i.created_by_participant_id ?? '',
    i.delegated_by_run_id ?? '',
    i.profile ?? '',
    i.context_version ?? 0,
    i.handoff_version ?? 0,
    i.run_id ?? '',
    i.park_reason ?? '',
    i.steer_count ?? 0,
    i.say_count ?? 0,
    i.hold ?? false,
    i.aborted ?? false,
    i.from_fact_ids ?? [],
    i.context_fact_ids ?? [],
    i.pinned_fact_ids ?? [],
    i.interest_entity_refs ?? [],
    // Retry notes are rendered by the panel; fingerprint their text so a note
    // edit rebuilds without carrying the whole prose in the key.
    (i.notes ?? []).map((n) => `${n.at}:${n.kind}:${n.text}`),
    (i.interjections ?? []).map((n) => `${n.at}:${n.text}`),
  ])
}

/** The run fields buildGraph consumes (latest run, model pill, warnings badge). */
function runSignature(r: Run): string {
  return JSON.stringify([
    r.id,
    r.intent_id,
    r.status,
    r.model ?? '',
    r.resolved_profile ?? '',
    r.started_at ?? '',
    r.continued_from ?? '',
    r.warnings ?? [],
  ])
}

/**
 * A stable string describing everything the built model and its inspectors
 * render. Pure: the same snapshot produces the same key, so a poll with no
 * changes reuses the previous GraphModel object and the canvas does not move.
 */
export function graphModelSignature(
  source: GraphModelSource,
  inputs: { facts: Fact[]; intents: Intent[]; goals: Goal[] },
): string {
  const { data, project, runs, profileMeta, defaultModel } = source
  return JSON.stringify([
    [
      project?.id ?? '',
      project?.name ?? '',
      project?.scope ?? '',
      project?.description ?? '',
      project?.intents ?? 0,
      project?.facts ?? 0,
    ],
    inputs.facts.map(factSignature),
    inputs.intents.map(intentSignature),
    inputs.goals.map(goalSignature),
    runs.map(runSignature),
    Object.entries(profileMeta).map(([id, m]) => [id, m.tier ?? '', m.model ?? '']),
    defaultModel,
    (data?.edges ?? []).map((e) => [e.source, e.target, e.kind]),
    (data?.boundary ?? []).map((b) => [b.id, b.state]),
    // The server's omission contract: only (id, omitted) shape the model, but the
    // query is short and keeps the row honest.
    (data?.details ?? []).map((d) => [d.id, d.query, d.omitted]),
    // Review/evidence counters that change without the fact row changing (a
    // verifier review bumps fact_meta only).
    (data?.fact_meta ?? []).map((m) => [
      m.id,
      m.needs_review ? 1 : 0,
      m.reviewed_claim_version,
      m.evidence_count,
      m.ref_count,
    ]),
  ])
}

/** Map a wire node id (`fact:`/`intent:`/`goal:`) to its render node id. */
function renderNodeID(raw: string): string {
  if (raw.startsWith('fact:')) return `f:${raw.slice(5)}`
  if (raw.startsWith('intent:')) return `s:${raw.slice(7)}`
  if (raw.startsWith('goal:')) return `g:${raw.slice(5)}`
  return raw
}

/**
 * Filter the snapshot's edges/boundary references through the same filters as
 * the nodes. Without this a hidden fact would reappear as a "展开" boundary
 * ghost: buildGraph turns every edge endpoint it does not have into a boundary
 * node. Genuinely missing references (not in this snapshot at all) are kept:
 * that is the honest "the ledger has more, expand it" affordance.
 *
 * With `focusSet` (focusOnly), edges and boundaries outside the drawn chain are
 * dropped too: buildGraph re-adds a boundary node for every edge endpoint, so
 * keeping an outside edge would resurrect the very nodes the filter hid.
 */
function filterGraphLinks(
  data: GraphRead | undefined,
  inputs: { facts: Fact[]; intents: Intent[]; goals: Goal[] },
  focusSet?: ReadonlySet<string> | null,
): { edges: { source: string; target: string; kind: string }[]; boundary: { id: string; state: string }[] } {
  if (!data) return { edges: [], boundary: [] }
  const rawFacts = new Set((data.facts ?? []).map((f) => `f:${f.id}`))
  const rawIntents = new Set((data.intents ?? []).map((i) => `s:${i.id}`))
  const rawGoals = new Set((data.goals ?? []).map((g) => `g:${g.id}`))
  const keptFacts = new Set(inputs.facts.map((f) => `f:${f.id}`))
  const keptIntents = new Set(inputs.intents.map((i) => `s:${i.id}`))
  const keptGoals = new Set(inputs.goals.map((g) => `g:${g.id}`))
  const filteredOut = (raw: string): boolean => {
    const id = renderNodeID(raw)
    if (rawFacts.has(id)) return !keptFacts.has(id)
    if (rawIntents.has(id)) return !keptIntents.has(id)
    if (rawGoals.has(id)) return !keptGoals.has(id)
    return false
  }
  const onChain = (raw: string): boolean => !focusSet || focusSet.has(renderNodeID(raw))
  return {
    edges: (data.edges ?? []).filter(
      (e) => !filteredOut(e.source) && !filteredOut(e.target) && onChain(e.source) && onChain(e.target),
    ),
    boundary: (data.boundary ?? []).filter((b) => !filteredOut(b.id) && onChain(b.id)),
  }
}

function build(
  source: GraphModelSource,
  inputs: { facts: Fact[]; intents: Intent[]; goals: Goal[] },
  links: { edges: { source: string; target: string; kind: string }[]; boundary: { id: string; state: string }[] },
): GraphModel {
  return buildGraph(
    inputs.facts,
    inputs.intents,
    inputs.goals,
    source.project,
    source.runs,
    source.profileMeta,
    source.defaultModel,
    links.boundary,
    links.edges,
    source.data?.details ?? [],
    source.data?.fact_meta ?? [],
  )
}

export interface ResolvedGraphModel {
  /** The model without the focusOnly filter (the chain below is cut from it). */
  base: GraphModelMemo
  /** The focusOnly model, when that filter is on and a chain exists. */
  chain: GraphModelMemo | null
  /** What the page draws: the chain when present, else the base model. */
  model: GraphModel
}

/**
 * The pure core of the hook. Two caches, keyed by the signature above:
 *
 * focusOnly derives its chain from the model built *without* focusOnly: deriving
 * the chain from its own filtered output would make each rebuild shrink the
 * chain further (the predecessors of a node already dropped are not there to
 * walk back along). Two builds only happen while that filter is on.
 *
 * Exported so the cache rules (unchanged poll → same model object; corrected
 * title/summary/claim version → rebuilt model) are testable without React.
 */
export function resolveGraphModelMemo(
  prevBase: GraphModelMemo | null,
  prevChain: GraphModelMemo | null,
  source: GraphModelSource,
  filters: GraphFiltersState,
  now: number,
): ResolvedGraphModel {
  const raw = {
    facts: source.data?.facts ?? [],
    intents: source.data?.intents ?? [],
    goals: source.data?.goals ?? [],
  }
  const baseInputs = filterGraphInputs(raw, {
    hiddenKinds: filters.hiddenKinds,
    timeRange: filters.timeRange,
    hideDone: filters.hideDone,
    focusOnly: false,
    now,
  })
  const baseSig = graphModelSignature(source, baseInputs)
  const base: GraphModelMemo =
    prevBase?.sig === baseSig
      ? prevBase
      : { sig: baseSig, model: build(source, baseInputs, filterGraphLinks(source.data, baseInputs)) }
  if (!filters.focusOnly || !source.selected) return { base, chain: null, model: base.model }
  const path = focusPath(base.model, source.selected)
  if (!path) return { base, chain: null, model: base.model }
  const chainSig = `${baseSig}|focus:${source.selected}|${[...path].sort().join('|')}`
  if (prevChain?.sig === chainSig) return { base, chain: prevChain, model: prevChain.model }
  const focused = filterGraphInputs(raw, {
    hiddenKinds: filters.hiddenKinds,
    timeRange: filters.timeRange,
    hideDone: filters.hideDone,
    focusOnly: true,
    focusSet: path,
    now,
  })
  const chain: GraphModelMemo = {
    sig: chainSig,
    model: build(source, focused, filterGraphLinks(source.data, focused, path)),
  }
  return { base, chain, model: chain.model }
}
