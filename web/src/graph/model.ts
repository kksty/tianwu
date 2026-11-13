// The engagement graph: facts are nodes, intents are steps, goals are nodes.
//
// An intent is the work between what the engagement knew and what it produced:
//   context fact(s) ─▶ step(intent) ─▶ produced fact
// When the run has not produced a fact yet, the step points at a ghost ring; the
// ring is replaced by the fact node the moment the ledger receives it. Initial
// intents have no context fact, so their step starts at the project node (the
// operator's brief) instead.
//
// Steps are real graph nodes rather than labels on an edge: that is what makes
// "what did we know → what did this direction produce → what came next" a chain
// instead of a fan of lines from the brief. The step is still one per intent, so
// a direction with several produced facts funnels them through one point.
//
// Every node carries a structural `depth` (BFS hops from the project brief). It
// is derived from from_facts/parent_intent, never from a hardcoded phase name:
// the doctrine that names stages is a content asset, not layout code.
//
// Everything here is a pure function of the ledger snapshot, so the canvas can be
// rebuilt on every poll without keeping state of its own.

import type { Fact, Goal, Intent, ProjectInfo, Run } from '@/lib/types'

export type NodeType = 'boundary' | 'fact' | 'intent' | 'step' | 'goal' | 'project' | 'ghost'

export interface GraphNode {
  boundary?: string
  /** Why a boundary node is not a real node (internal/retrieval/graph_page.go):
   *  `not_loaded` means the ledger has it but this page/snapshot does not, so it
   *  can be expanded; `missing` means the ledger no longer has the referenced id;
   *  `rejected` means it exists but was explicitly negated. The three must stay
   *  distinguishable on the map: expanding a missing reference would promise
   *  content that does not exist. */
  boundaryState?: string
  id: string
  type: NodeType
  label: string
  sub?: string
  kind?: string // fact kind
  status?: string // intent status / goal met|open
  x: number
  y: number
  vx: number
  vy: number
  degree: number
  /** Rendered half-width/half-height, set once the label is known. */
  w: number
  h: number
  /**
   * The pill label wrapped to its measured box (2–3 lines), set by the canvas's
   * measure pass together with w/h. Absent on fact/ghost dots and on models that
   * were never measured (tests): the painter then falls back to one fitted line.
   */
  labelLines?: string[]
  /** Hops from the project brief (project = 0). Set by buildGraph. */
  depth: number
  /** Claim count / attempts for a step; >1 means the direction was retried. */
  attempts?: number
  /** The latest run continued an earlier session (resume retry / steer / wake). */
  retried?: boolean
  /** Deterministic ordering inside a layer (creation time). */
  orderTime?: string
  /** Non-fatal run warnings carried by this intent (and the facts it produced). */
  warnings?: string[]
  /** Routing tier of the profile this intent ran on ("worker" / "strong"). */
  tier?: string
  /** The model this direction actually ran on (run's resolved model, else the
   *  profile's configured model). Shown on the step pill in the chain-tree view:
   *  the tier alone does not tell the operator which model produced a fact. */
  model?: string
  /** The profile the run actually resolved to (may differ from intent.profile). */
  resolvedProfile?: string
  /** The claim asserted a newer version than the latest review proved. */
  needsReview?: boolean
  /** Latest proved claim version; 0 = never reviewed (fact_meta contract). */
  reviewedClaimVersion?: number
  /** Number of evidence rows (survives projection, which clears evidence_ids). */
  evidenceCount?: number
  /** Number of openable refs (survives projection, which clears refs). */
  refCount?: number
  meta?: { fact?: Fact; intent?: Intent; goal?: Goal; project?: ProjectInfo }
}

/**
 * What an edge means, not merely where it goes:
 *   intent    — a step's produced fact / ghost ring (status colour carries it)
 *   premise   — a hard prerequisite (from_fact_ids): the step cannot be read
 *               without this fact; a missing/rejected premise blocks dispatch
 *   reference — an explicit reading choice (context_fact_ids); context, never a
 *               gate, and must not look like a premise
 *   pin       — an explicitly pinned read (pinned_fact_ids); still context, but
 *               the operator asked to see it first
 *   supports  — a fact that backs an achieved goal's evidence
 *   spawn     — parent direction delegated the child direction
 *   brief/host — project boundary and service-on-host grouping
 */
export type EdgeKind =
  | 'intent'
  | 'premise'
  | 'reference'
  | 'pin'
  | 'supports'
  | 'brief'
  | 'host'
  | 'goal'
  | 'spawn'

/** One-word relation label for hover/legend; the produced-fact fan has none. */
export function edgeKindLabel(kind: EdgeKind): string {
  switch (kind) {
    case 'premise':
      return '必要前提'
    case 'reference':
      return '引用'
    case 'pin':
      return '固定'
    case 'supports':
      return '支持目标'
    case 'spawn':
      return '子任务委派'
    case 'brief':
      return '项目范围'
    case 'host':
      return '宿主机'
    case 'goal':
      return '目标证据'
    default:
      return ''
  }
}

export interface GraphEdge {
  id: string
  source: string
  target: string
  kind: EdgeKind
  /** Present on intent/spawn edges: the work this edge stands for. */
  intent?: Intent
  /** On spawn edges: the parent intent that created the child (血缘分叉). */
  parentIntent?: string
  label?: string
  status?: string
}

/** Per-fact counters the page carries when the fact body is projected away. */
export interface FactMeta {
  id: string
  needs_review: boolean
  reviewed_claim_version: number
  evidence_count: number
  ref_count: number
}

export interface GraphModel {
  nodes: GraphNode[]
  edges: GraphEdge[]
  byId: Map<string, GraphNode>
  maxDepth: number
  /** edge.id → edge; selection/inspector lookup without scanning the list. */
  edgesById: Map<string, GraphEdge>
  /** intent.id → its step node; the step is the direction's node on the map. */
  stepByIntent: Map<string, GraphNode>
  /** fact.id (unprefixed) → the fact body; the vision for detail re-fetch. */
  factsById: Map<string, Fact>
  /** node id → upstream neighbour ids (edge sources); null-safe BFS lookup. */
  incoming: Map<string, string[]>
  /** node id → downstream neighbour ids (edge targets). */
  outgoing: Map<string, string[]>
}

/** Deterministic pseudo-random from an id, so a reload does not reshuffle. */
function hash(s: string): number {
  let h = 2166136261
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return (h >>> 0) / 4294967295
}

function place(n: GraphNode, i: number, total: number) {
  const angle = (i / Math.max(total, 1)) * Math.PI * 2
  const ring = 150 + Math.sqrt(i + 1) * 74
  const jitter = (hash(n.id + 'x') - 0.5) * 90
  const jitter2 = (hash(n.id + 'y') - 0.5) * 90
  n.x = Math.cos(angle) * ring + jitter
  n.y = Math.sin(angle) * ring + jitter2
  n.vx = 0
  n.vy = 0
}

export const stepID = (intentID: string) => `s:${intentID}`
export const ghostID = (intentID: string) => `w:${intentID}`

export function buildGraph(
  facts: Fact[],
  intents: Intent[],
  goals: Goal[],
  project?: ProjectInfo,
  runs: Run[] = [],
  profileMeta: Record<string, { tier?: string; model?: string }> = {},
  defaultModel = '',
  boundaries: {id:string;state:string}[] = [],
  sourceEdges: {source:string;target:string;kind:string}[] = [],
  details: {id:string;omitted:string[]}[] = [],
  factMeta: FactMeta[] = [],
): GraphModel {
  const nodes: GraphNode[] = []
  const edges: GraphEdge[] = []
  const byId = new Map<string, GraphNode>()

  // Runs are the only place warnings are recorded. Aggregate them per intent so
  // every node that belongs to that direction can carry the same badge.
  const warningsByIntent = new Map<string, string[]>()
  const latestRun = new Map<string, Run>()
  // The model is a property of the executor run, not of the latest run: verifier
  // and report runs hang off the same intent but never report a model, so the
  // pill must read the newest run that actually has one.
  const modeledRun = new Map<string, Run>()
  for (const r of runs) {
    if (!r.intent_id) continue
    const prev = latestRun.get(r.intent_id)
    if (!prev || (r.started_at ?? '') > (prev.started_at ?? '')) latestRun.set(r.intent_id, r)
    if ((r.model ?? '').trim() || r.resolved_profile) {
      const mprev = modeledRun.get(r.intent_id)
      if (!mprev || (r.started_at ?? '') > (mprev.started_at ?? '')) modeledRun.set(r.intent_id, r)
    }
    for (const w of r.warnings ?? []) {
      const list = warningsByIntent.get(r.intent_id) ?? []
      if (!list.includes(w)) list.push(w)
      warningsByIntent.set(r.intent_id, list)
    }
  }
  const metaOf = (profile?: string) => (profile ? (profileMeta[profile] ?? {}) : {})

  const addNode = (n: GraphNode) => {
    nodes.push(n)
    byId.set(n.id, n)
  }

  const projectID = project ? `p:${project.id}` : 'p:project'
  addNode({
    id: projectID,
    type: 'project',
    label: project?.name ?? '项目',
    sub: project?.scope || project?.description,
    status: project ? 'project' : undefined,
    x: 0,
    y: 0,
    vx: 0,
    vy: 0,
    degree: 0,
    w: 86,
    h: 20,
    depth: 0,
    meta: { project },
  })

  const factsOutByIntent = new Map<string, Fact[]>()
  for (const f of facts) {
    if (!f.intent_id) continue
    const list = factsOutByIntent.get(f.intent_id) ?? []
    list.push(f)
    factsOutByIntent.set(f.intent_id, list)
  }
  const factIDs = new Set(facts.map((f) => f.id))
  const intentByID = new Map(intents.map((it) => [it.id, it]))
  const factMetaByID = new Map(factMeta.map((m) => [m.id, m]))
  // A details row says the server omitted a field from an intent node. The
  // premise source list is asked once per intent; index the rows once instead
  // of scanning all details inside sourcesOf (was O(intents × details)).
  const omittedFromFacts = new Set(
    details.filter((d) => d.omitted.includes('from_fact_ids')).map((d) => d.id),
  )
  // Chronological order is the production order: a child intent is created after
  // its parent, so one pass can resolve depths without guessing.
  const ordered = [...intents].sort((a, b) => {
    const at = a.created_at ?? ''
    const bt = b.created_at ?? ''
    if (at !== bt) return at < bt ? -1 : 1
    return a.id < b.id ? -1 : a.id > b.id ? 1 : 0
  })

  /** Where an intent's work starts: its context facts, else the brief. */
  const sourcesOf = (it: Intent): string[] => {
    const context = (it.from_fact_ids ?? [])
      .map((id) => `f:${id}`)

    if (omittedFromFacts.has(`intent:${it.id}`)) return context
    return context.length ? context : [projectID]
  }

  // ---- depth (structural, not a phase name) ----
  const factDepth = new Map<string, number>()
  const stepDepth = new Map<string, number>()
  for (const it of ordered) {
    const sources = sourcesOf(it)
    let depth = 1
    for (const s of sources) {
      const srcDepth = s.startsWith('f:') ? factDepth.get(s.slice(2)) ?? 1 : 0
      depth = Math.max(depth, srcDepth + 1)
    }
    const parent = it.parent_intent_id ? intentByID.get(it.parent_intent_id) : undefined
    if (parent) depth = Math.max(depth, (stepDepth.get(parent.id) ?? 0) + 1)
    stepDepth.set(it.id, depth)
    for (const f of factsOutByIntent.get(it.id) ?? []) {
      if (!factDepth.has(f.id)) factDepth.set(f.id, depth + 1)
    }
  }

  // ---- nodes ----
  for (const f of facts) {
    // The graph projection clears evidence_ids/refs from the fact body, so the
    // page carries their counts and the review state separately. Merge by id:
    // no meta row (e.g. a full projection) leaves the node's counters absent.
    const fm = factMetaByID.get(f.id)
    addNode({
      id: `f:${f.id}`,
      type: 'fact',
      label: f.title,
      sub: f.summary,
      kind: f.kind,
      status: f.status,
      x: 0,
      y: 0,
      vx: 0,
      vy: 0,
      degree: 0,
      w: 6,
      h: 6,
      depth: factDepth.get(f.id) ?? 1,
      orderTime: f.created_at,
      warnings: f.intent_id ? warningsByIntent.get(f.intent_id) : undefined,
      ...(fm
        ? {
            needsReview: fm.needs_review,
            reviewedClaimVersion: fm.reviewed_claim_version,
            evidenceCount: fm.evidence_count,
            refCount: fm.ref_count,
          }
        : {}),
      meta: { fact: f },
    })
  }

  const stepByIntent = new Map<string, GraphNode>()
  for (const it of ordered) {
    const depth = stepDepth.get(it.id) ?? 1
    const run = latestRun.get(it.id)
    const modelRun = modeledRun.get(it.id)
    const factsOut = factsOutByIntent.get(it.id) ?? []
    const requested = metaOf(it.profile)
    const resolved = metaOf(modelRun?.resolved_profile)
    // The executor run's own model wins (a handoff can move a direction to a
    // different profile mid-run); a fresh intent with no run yet names the
    // instance default model, so every pill has an answer.
    const ranModel = (modelRun?.model ?? '').trim()
    const step: GraphNode = {
      id: stepID(it.id),
      type: 'step',
      label: it.title,
      sub: it.description,
      status: it.status,
      x: 0,
      y: 0,
      vx: 0,
      vy: 0,
      degree: 0,
      w: 7,
      h: 7,
      depth,
      attempts: it.attempts,
      retried: Boolean(run?.continued_from) || (it.attempts ?? 0) > 1,
      orderTime: it.created_at,
      warnings: warningsByIntent.get(it.id),
      tier: requested.tier || resolved.tier,
      model: ranModel || resolved.model || requested.model || defaultModel,
      resolvedProfile: modelRun?.resolved_profile,
      meta: { intent: it },
    }
    addNode(step)
    stepByIntent.set(it.id, step)
    if (factsOut.length === 0) {
      addNode({
        id: ghostID(it.id),
        type: 'ghost',
        label: it.title,
        sub: it.description,
        status: it.status,
        x: 0,
        y: 0,
        vx: 0,
        vy: 0,
        degree: 0,
        w: 9,
        h: 9,
        depth: depth + 1,
        orderTime: it.created_at,
        warnings: warningsByIntent.get(it.id),
        tier: step.tier,
        model: step.model,
        meta: { intent: it },
      })
    }
  }

  const maxFactDepth = factDepth.size ? Math.max(...factDepth.values()) : 0
  const goalDepth = facts.length ? maxFactDepth + 1 : 1
  // Only a completed goal is a graph node: it is anchored to the fact that
  // carries its evidence. An unmet goal has no completed work to stand on, so it
  // is shown in the graph page's 终点 card instead of being drawn as a loose or
  // guessed node.
  for (const g of goals) {
    if (!g.met) continue
    addNode({
      id: `g:${g.id}`,
      type: 'goal',
      label: g.statement,
      sub: g.start || g.quantified_target,
      status: 'met',
      x: 0,
      y: 0,
      vx: 0,
      vy: 0,
      degree: 0,
      w: 70,
      h: 16,
      depth: goalDepth,
      orderTime: (g as Goal & { created_at?: string }).created_at,
      meta: { goal: g },
    })
  }

  const boundaryState = new Map(boundaries.map(b => [b.id,b.state]))
  // A boundary leaf is a reference whose entity the snapshot does not carry. The
  // ledger's boundary stream can also name ids that ARE in the snapshot (for
  // example an open goal, which is deliberately not a map node). Drawing those
  // as "未载入引用" would invent an orphan ghost for a row the map already has,
  // so loaded rows are never turned into boundary leaves.
  const loadedRefIDs = new Set<string>([
    ...facts.map(f => `fact:${f.id}`),
    ...intents.map(it => `intent:${it.id}`),
    ...goals.map(g => `goal:${g.id}`),
  ])
  const pendingRefs = new Set<string>()
  for (const b of boundaries) if (!loadedRefIDs.has(b.id)) pendingRefs.add(b.id)
  for(const edge of sourceEdges){pendingRefs.add(edge.source);pendingRefs.add(edge.target)}
  for (const it of intents) for(const id of it.from_fact_ids ?? []) if(!factIDs.has(id)) pendingRefs.add(`fact:${id}`)
  for (const f of facts) if(f.intent_id && !intentByID.has(f.intent_id)) pendingRefs.add(`intent:${f.intent_id}`)
  const nodeID = (id:string) => id.startsWith('fact:') ? `f:${id.slice(5)}` : id.startsWith('intent:') ? stepID(id.slice(7)) : id.startsWith('goal:') ? `g:${id.slice(5)}` : id
  for (const id of pendingRefs) {
    if (loadedRefIDs.has(id)) continue
    if(!byId.has(nodeID(id))) {
      const state = boundaryState.get(id)
      // "The ledger cannot show this row here" is a different fact from "the
      // ledger says this id is gone". Only the first is an expandable leaf.
      const why = state === 'missing' ? '引用缺失' : state === 'rejected' ? '已否定' : state === 'not_loaded' ? '未载入' : '展开'
      addNode({id:nodeID(id),type:'boundary',label:`${id} · ${why}`,boundary:id,boundaryState:state,x:0,y:0,vx:0,vy:0,degree:0,w:9,h:9,depth:1})
    }
  }

  // ---- edges ----
  const link = (
    source: string,
    target: string,
    kind: EdgeKind,
    intent?: Intent,
    label?: string,
    parentIntent?: string,
  ) => {
    const a = byId.get(source)
    const b = byId.get(target)
    if (!a || !b || source === target) return
    edges.push({
      id: `${source}->${target}:${kind}:${intent?.id ?? ''}`,
      source,
      target,
      kind,
      intent,
      parentIntent,
      label,
      status: intent?.status,
    })
    a.degree++
    b.degree++
  }

  for (const it of ordered) {
    const sid = stepID(it.id)
    // A direction's own premise list is the hard prerequisite relation; it must
    // stay visually distinct from the reference/pin reads added below.
    for (const src of sourcesOf(it)) link(src, sid, 'premise', it)
    const factsOut = factsOutByIntent.get(it.id) ?? []
    if (factsOut.length === 0) {
      link(sid, ghostID(it.id), 'intent', it)
    } else {
      for (const f of factsOut) link(sid, `f:${f.id}`, 'intent', it)
    }
  }

  // Relation kinds the ledger can prove, mapped to their display meaning. The
  // console never invents a relation: 'produces' is the intent fan, and an
  // unknown kind stays the neutral intent edge it used to be.
  const sourceKind = (kind: string): EdgeKind => {
    switch (kind) {
      case 'delegates':
        return 'spawn'
      case 'premise':
        return 'premise'
      case 'reference':
        return 'reference'
      case 'pin':
        return 'pin'
      case 'supports':
        return 'supports'
      default:
        return 'intent'
    }
  }
  const intentAt = (nodeID: string): Intent | undefined => {
    const prefix = 'intent:'
    return nodeID.startsWith(prefix) ? intentByID.get(nodeID.slice(prefix.length)) : undefined
  }
  // Dedupe by the full relation, not just the endpoint pair: a fact can be both
  // a reference and a pin of the same direction, and neither may swallow the
  // other. The synthesized intent/premise edges still dedupe by pair+kind.
  const linkedEdges = new Set(edges.map(e => `${e.source}/${e.target}/${e.kind}`))
  for (const e of sourceEdges) {
    const source = nodeID(e.source), target = nodeID(e.target)
    const kind = sourceKind(e.kind)
    if (!linkedEdges.has(`${source}/${target}/${kind}`)) {
      // An edge selection opens the direction panel: carry the intent for any
      // edge whose endpoint is a step. A supports edge (fact→goal) has none.
      link(source, target, kind, intentAt(e.target) ?? intentAt(e.source), undefined, kind === 'spawn' ? intentAt(e.source)?.id : undefined)
      linkedEdges.add(`${source}/${target}/${kind}`)
    }
  }

  // Spawn lineage: a delegated child hangs off its parent's output anchor. The
  // child step is one layer deeper — that is the visible "branch" of the tree.
  const anchorOf = (it: Intent): string => {
    const out = factsOutByIntent.get(it.id) ?? []
    if (out.length > 0) return `f:${out[0].id}`
    if (byId.has(ghostID(it.id))) return ghostID(it.id)
    return stepID(it.id)
  }
  for (const it of intents) {
    const parentID = it.parent_intent_id
    if (!parentID) continue
    const parent = intentByID.get(parentID)
    if (!parent) continue
    link(anchorOf(parent), stepID(it.id), 'spawn', it, 'spawn', parentID)
  }

  // service "10.0.0.5:8080" sits on host "10.0.0.5".
  // Index hosts by title once (first fact wins, the old facts.find semantics):
  // the pairing was O(F²) when every service scanned the whole fact list.
  const hostByTitle = new Map<string, Fact>()
  for (const f of facts) {
    if (f.kind === 'host' && !hostByTitle.has(f.title)) hostByTitle.set(f.title, f)
  }
  for (const f of facts) {
    if (f.kind !== 'service') continue
    const host = f.title.includes(':') ? f.title.slice(0, f.title.lastIndexOf(':')) : ''
    const hostFact = host ? hostByTitle.get(host) : undefined
    if (hostFact) link(`f:${hostFact.id}`, `f:${f.id}`, 'host')
  }

  // A met goal points at the fact that carries its evidence. That is the line
  // the map draws for a completed goal: short, on the evidence it rests on. The
  // brief edge stays only as a fallback for a goal with no evidence fact, so a
  // goal is never a single line dragged across the whole map. The owner map is
  // built once (first fact wins) instead of scanning the facts per goal.
  const evidenceOwner = new Map<string, Fact>()
  for (const f of facts) {
    for (const ev of f.evidence_ids ?? []) {
      if (!evidenceOwner.has(ev)) evidenceOwner.set(ev, f)
    }
  }
  // An unmet goal is deliberately not linked: the editor's 终点 card explains the
  // target, and a line would pretend the goal already stands on current work.
  for (const g of goals) {
    if (!g.met) continue
    if (g.evidence_id) {
      const owner = evidenceOwner.get(g.evidence_id)
      if (owner) link(`f:${owner.id}`, `g:${g.id}`, 'goal')
    }
    if (!edges.some((e) => e.kind === 'goal' && e.target === `g:${g.id}`)) {
      link(projectID, `g:${g.id}`, 'brief')
    }
  }

  // Force-mode initial placement; the layered layout overrides these positions.
  const factsN = nodes.filter((n) => n.type === 'fact')
  const goalsN = nodes.filter((n) => n.type === 'goal')
  const ghostsN = nodes.filter((n) => n.type === 'ghost')
  const stepsN = nodes.filter((n) => n.type === 'step')
  factsN.forEach((n, i) => place(n, i, factsN.length))
  goalsN.forEach((n, i) => place(n, i, goalsN.length))
  ghostsN.forEach((n, i) => place(n, i, ghostsN.length))
  stepsN.forEach((n, i) => place(n, i, stepsN.length))

  let maxDepth = 0
  for (const n of nodes) maxDepth = Math.max(maxDepth, n.depth)

  // Indexes the view layer reuses instead of rescanning nodes/edges for every
  // lookup: selection, inspector, fact detail and neighbourhood focus.
  const edgesById = new Map(edges.map((e) => [e.id, e]))
  const factsById = new Map(facts.map((f) => [f.id, f]))
  const incoming = new Map<string, string[]>()
  const outgoing = new Map<string, string[]>()
  for (const e of edges) {
    if (!byId.has(e.source) || !byId.has(e.target)) continue
    outgoing.set(e.source, [...(outgoing.get(e.source) ?? []), e.target])
    incoming.set(e.target, [...(incoming.get(e.target) ?? []), e.source])
  }
  return { nodes, edges, byId, maxDepth, edgesById, stepByIntent, factsById, incoming, outgoing }
}

/**
 * Nodes on the path through a selected/hovered item: every ancestor back to the
 * brief, the item itself and every descendant. Reading "how did we get here and
 * what did this open up" is the whole point of the graph; one-hop focus was too
 * shallow now that steps are nodes.
 */
export function focusPath(
  // The built model carries the adjacency indexes. Tests and small callers may
  // pass the minimal {nodes, edges} shape, which falls back to building them.
  g: Pick<GraphModel, 'nodes' | 'edges'> &
    Partial<Pick<GraphModel, 'incoming' | 'outgoing'>>,
  id: string | null,
): Set<string> | null {
  if (!id) return null
  const byId = new Map(g.nodes.map((n) => [n.id, n]))
  let incoming = g.incoming
  let outgoing = g.outgoing
  if (!incoming || !outgoing) {
    const inMap = new Map<string, string[]>()
    const outMap = new Map<string, string[]>()
    for (const e of g.edges) {
      if (!byId.has(e.source) || !byId.has(e.target)) continue
      outMap.set(e.source, [...(outMap.get(e.source) ?? []), e.target])
      inMap.set(e.target, [...(inMap.get(e.target) ?? []), e.source])
    }
    incoming = incoming ?? inMap
    outgoing = outgoing ?? outMap
  }
  // An edge selection focuses both endpoints (and their paths).
  const edge = g.edges.find((e) => e.id === id)
  const seeds = edge
    ? [edge.source, edge.target]
    : byId.has(id)
      ? [id]
      : []
  if (seeds.length === 0) return null
  const out = new Set<string>()
  const walk = (start: string, next: Map<string, string[]>) => {
    const queue = [start]
    while (queue.length && out.size < 400) {
      const cur = queue.shift()!
      if (out.has(cur) && cur !== start) continue
      out.add(cur)
      if (byId.get(cur)?.type === 'project') continue
      for (const nxt of next.get(cur) ?? []) {
        if (!out.has(nxt)) queue.push(nxt)
      }
    }
  }
  for (const s of seeds) {
    walk(s, incoming)
    walk(s, outgoing)
  }
  return out
}
