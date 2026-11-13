// One thin client over the HTTP API. Two namespaces, exactly as the server
// mounts them: instance-level endpoints (/api/projects, /api/vulns, …) and the
// project namespace (/p/<id>/api/…). See cmd/tw/hub.go and internal/api/api.go.

import { decodeLocationPart } from './navigation.ts'
import { focusSearchParams } from './focus.ts'
import type { FocusPage, FocusRequest } from './focus.ts'

import type {
  ExternalAction,
  AccessEntry, BrowserLease, OperatorRequest, NativeArtifact,
  AppendGoalRequest, AppendGoalResult,
  AssetHost,
  BoardResponse,
  ChatResult,
  ChatTarget,
  ChildIntent,
  CommunicationQueue,
  CommunicationStatus,
  ContinueIntentResult,
  CreateChildIntentBody,
  CreateChildIntentResult,
  Decision,
  DeciderStatus,
  DirectiveRequest,
  DirectiveResponse,
  Evidence,
  Event,
  Fact,
  FleetResponse,
  Goal,
  ImportResult,
  IOAObservations,
  Intent,
  Instruction,
  KBResponse,
  HTTPFlow,
  HTTPFlowRecord,
  KBEntry,
  KBCandidate,
  KBCandidateInput,
  KillWorkerResponse,
  Message,
  ModelCatalogue,
  ModelProfile,
  ModelProfileCatalog,
  ModelProfilesResponse,
  NodesResponse,
  Participant,
  PeerPolicy,
  PeerPolicyResponse,
  Playbook,
  PlaybooksResponse,
  Poc,
  PremisePolicy,
  PremisePolicyResponse,
  ProfileProbe,
  ProjectInfo,
  ReportMeta,
  ReportPrecheck,
  Review,
  RetryPreview,
  RetryResult,
  Run,
  RunContext,
  RuntimeSettings,
  SettingsView,
  SpawnRequest,
  SpawnResponse,
  ToolsResponse,
  RuntimeToolsResponse,
  UsageSummary,
  VerifierStatus,
  VerifyPendingResult,
  VulnEntry,
  WorkerView,
  WorkersResponse,
} from './types'

export interface ReadPage { next_cursor?: string; has_more: boolean; total: number; snapshot_version: {epoch: string; seq: number} }

/** Per-fact counters carried with a projected page (the fact body omits
 *  evidence_ids/refs there, so the counts are the only honest source). */
export interface GraphFactMeta {
  id: string
  needs_review: boolean
  reviewed_claim_version: number
  evidence_count: number
  ref_count: number
}

export interface GraphRead extends ReadPage {
  facts: Fact[]
  intents: Intent[]
  goals: Goal[]
  edges: { source: string; target: string; kind: string }[]
  boundary: { id: string; state: string }[]
  details?: { id: string; query: string; omitted: string[] }[]
  /** Only covers the facts returned on this page. */
  fact_meta?: GraphFactMeta[]
  intents_total?: number
  facts_total?: number
  goals_total?: number
  edges_total?: number
  boundary_total?: number
}

/** GET /api/changes: the journal window after a snapshot watermark, used to
 *  tell the operator "there are new changes, refresh" without polling the graph. */
export interface ChangePage {
  changes: { seq: number; kind: string; id: string; at: string }[]
  next_cursor?: string
  has_more: boolean
  high_watermark: { epoch: string; seq: number }
  pending: number
}

export interface FactRead extends ReadPage { facts: Fact[] }

// ---- communication backlog (GET /api/communication) ----
//
// The wire shapes live in types.ts (one-to-one with store.QueueStats); this
// section only adds the small read-only helpers the Messages page needs. The
// console shows pressure but never interprets queued as delivered: a queued
// notice reaches the next prompt, and only a live session consumes it now.

export interface QueueLimits {
  max_messages: number
  max_bytes: number
}

/** The tighter of the two declared bounds, clamped to 0..1 for the meter. */
export function queueLoad(q: CommunicationQueue, limits: QueueLimits): number {
  const byCount = limits.max_messages > 0 ? q.pending / limits.max_messages : 0
  const byBytes = limits.max_bytes > 0 ? q.bytes / limits.max_bytes : 0
  return Math.min(1, Math.max(byCount, byBytes))
}

/** At 80% the queue is visibly close to refusing new work; deferred already did. */
const queuePressureThreshold = 0.8

export function queueUnderPressure(q: CommunicationQueue, limits: QueueLimits): boolean {
  return q.deferred || queueLoad(q, limits) >= queuePressureThreshold
}

export interface BacklogSummary {
  queued: number
  bytes: number
  deferred: number
  recipients: number
}

export function backlogSummary(status: CommunicationStatus | undefined): BacklogSummary {
  const out: BacklogSummary = { queued: 0, bytes: 0, deferred: 0, recipients: 0 }
  for (const q of status?.queues ?? []) {
    out.queued += q.pending
    out.bytes += q.bytes
    if (q.deferred) out.deferred++
    if (q.pending > 0 || q.deferred) out.recipients++
  }
  return out
}

export interface FactNotice {
  factId: string
  version: number
  coalesced: number
}

/** The mechanical sender is the control plane's router, not a participant: it
 *  must never read as the commander or the operator speaking. */
export function isMechanicalSender(id: string | undefined): boolean {
  return id === 'ledger'
}

/** A routed notice points at the record by id + version; its body is never the
 *  fact text. Returns null for any row that is not one. */
export function readFactNotice(m: Message): FactNotice | null {
  if (!isMechanicalSender(m.from_participant_id) || !m.notice_fact_id) return null
  return { factId: m.notice_fact_id, version: m.notice_version ?? 0, coalesced: m.coalesced ?? 0 }
}


export class ApiError extends Error {
  status: number
  /** Structured error code from the server (`{"error": "...", "code": "peer_denied"}`). */
  code?: string
  /** Parsed JSON body, for endpoints whose errors carry more than a message/code
   *  (append-goal: needs_human / retryable / pause_reason). */
  body?: unknown
  constructor(status: number, message: string, code?: string, body?: unknown) {
    super(message)
    this.status = status
    this.code = code
    this.body = body
  }
}

/** The project this page is looking at: /p/<id>/…, or "" before one is chosen. */
export function projectID(): string {
  const m = /^\/p\/([^/]+)/.exec(window.location.pathname)
  return m ? decodeLocationPart(m[1]) : ''
}

/** Base of the project namespace, "" when the URL carries no project. */
function pbase(): string {
  const id = projectID()
  return id ? `/p/${encodeURIComponent(id)}` : ''
}

async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers ?? {}) },
  })
  const text = await res.text()
  let body: unknown = null
  if (text) {
    try {
      body = JSON.parse(text)
    } catch {
      body = text
    }
  }
  if (!res.ok) {
    // A expired login is not an error to render: send the browser to the form
    // and come back to the page it was on.
    if (res.status === 401) {
      const next = encodeURIComponent(window.location.pathname + window.location.search + window.location.hash)
      window.location.assign(`/login?next=${next}`)
      throw new ApiError(401, '需要登录')
    }
    const msg =
      body && typeof body === 'object' && 'error' in body
        ? String((body as { error: unknown }).error)
        : `HTTP ${res.status}`
    // The collaboration endpoints attach a machine-readable code (peer_denied,
    // worker_forbidden, ...) so the console can show the reason instead of
    // guessing from the prose.
    const code =
      body && typeof body === 'object' && typeof (body as { code?: unknown }).code === 'string'
        ? (body as { code: string }).code
        : undefined
    throw new ApiError(res.status, msg, code, body)
  }
  return body as T
}

const get = <T>(url: string) => request<T>(url)
const post = <T>(url: string, body?: unknown) =>
  request<T>(url, { method: 'POST', body: body === undefined ? undefined : JSON.stringify(body) })
const put = <T>(url: string, body?: unknown) =>
  request<T>(url, { method: 'PUT', body: JSON.stringify(body) })
const del = <T>(url: string) => request<T>(url, { method: 'DELETE' })

// ---- instance: projects, vuln library, health ----

export const api = {
  externalActions: () => get<{actions: ExternalAction[]}>(`${pbase()}/api/actions`),
  resolveAction: (id: string, body: {version:number;state:'completed'|'unknown';result:string;result_ref?:string}) =>
    post<ExternalAction>(`${pbase()}/api/actions/${encodeURIComponent(id)}/result`, body),
  access: () => get<{ access: AccessEntry[] }>(`${pbase()}/api/access`).then(r => r.access),
  createAccess: (body: {name?: string; kind: string; origin?: string; note?: string; cdp_url?: string; intent_id?: string; request_id?: string; material?: unknown}) => post<AccessEntry>(`${pbase()}/api/access`, body),
  deleteAccess: (id: string) => del<{deleted: boolean}>(`${pbase()}/api/access/${encodeURIComponent(id)}`),
  browserAction: (id: string, action: 'open' | 'inspect' | 'save' | 'detach', lease_id: string) => post<{returned: boolean; output: string; verified: boolean; access?: AccessEntry}>(`${pbase()}/api/access/${encodeURIComponent(id)}/browser`, {action, lease_id}),
  browserLease: (id: string, body: {action: 'acquire' | 'release' | 'recover'; lease_id?: string; note?: string; confirmed_idle?: boolean}) => post<{lease?: BrowserLease; released?: boolean}>(`${pbase()}/api/access/${encodeURIComponent(id)}/lease`, body),
  operatorRequests: () => get<{requests: OperatorRequest[]}>(`${pbase()}/api/operator-requests`).then(r => r.requests),
  completeOperator: (intent: string, request_id: string, response: string, resolution: 'completed' | 'unavailable' = 'completed') => post<{accepted: boolean}>(`${pbase()}/api/intents/${encodeURIComponent(intent)}/operator/complete`, {request_id, response, resolution}),
  artifacts: (query = '') => get<{artifacts: NativeArtifact[]}>(`${pbase()}/api/artifacts?q=${encodeURIComponent(query)}&limit=100`).then(r => r.artifacts),
  artifactURL: (ref: string) => `${pbase()}/api/artifacts/data?ref=${encodeURIComponent(ref)}`,
  health: () => get<{ ok: boolean; executor: string }>('/api/health'),
  version: () => get<{ version: string }>('/api/version'),

  // The offline knowledge library is instance-level: every project reads the
  // same world knowledge, so it is not under /p/<id>/ either.
  kb: () => get<KBResponse>('/api/kb'),
  kbCandidates: () => get<{candidates: KBCandidate[]}>('/api/kb/candidates').then(r => r.candidates),
  saveKBCandidate: (body: KBCandidateInput) => body.id
    ? put<KBCandidate>(`/api/kb/candidates/${encodeURIComponent(body.id)}`, body)
    : post<KBCandidate>('/api/kb/candidates', body),
  publishKB: (id: string, version: number) => post<KBEntry>(`/api/kb/candidates/${encodeURIComponent(id)}/publish`, {version}),
  reloadKB: () => post<{entries: KBEntry[]}>('/api/kb/reload'),
  deleteKB: (id: string, revision: string) => request<{deleted: boolean}>(`/api/kb/entries/${encodeURIComponent(id)}`, {method:'DELETE', body:JSON.stringify({revision})}),
  deleteKBCandidate: (id: string, version: number) => request<{deleted: boolean}>(`/api/kb/candidates/${encodeURIComponent(id)}`, {method:'DELETE', body:JSON.stringify({version})}),
  playbookSource: (project: string, id: string) => get<Playbook>(`/p/${encodeURIComponent(project)}/api/playbooks/${encodeURIComponent(id)}`),
  nominatePlaybookKB: (project: string, id: string, body: KBCandidateInput & {source_updated_at: string}) => post<KBCandidate>(`/p/${encodeURIComponent(project)}/api/playbooks/${encodeURIComponent(id)}/kb-candidate`, body),

  // The LLM settings are instance-level: the decider and every local harness
  // run read them, so they are not under /p/<id>/.
  settings: () => get<SettingsView>('/api/settings'),
  saveSettings: (body: Partial<SettingsView>) => put<SettingsView>('/api/settings', body),
  // The instance-level gate shared by verifier and report runs. It is not a
  // project setting: one value bounds every project's auxiliary calls.
  runtimeSettings: () => get<RuntimeSettings>('/api/runtime-settings'),
  saveRuntimeSettings: (auxiliary_concurrency: number) =>
    put<RuntimeSettings>('/api/runtime-settings', { auxiliary_concurrency }),
  // Ask the configured endpoint what it serves; the controller makes the call so
  // the key stays on the server.
  models: () =>
    get<{ models: string[]; limits?: Record<string, number>; base_url?: string; provider?: string }>(
      '/api/models',
    ),
  // The instance's saved model configurations (the harness's llm.providers).
  // A named one runs on its own local host, so two intents can use two APIs
  // without sharing a process configuration.
  profiles: () => get<ModelProfileCatalog>('/api/model-profiles'),
  createProfile: (body: Partial<ModelProfile> & { id: string; api_key?: string }) =>
    post<ModelProfile>('/api/model-profiles', body),
  updateProfile: (id: string, body: Partial<ModelProfile> & { api_key?: string }) =>
    put<ModelProfile>(`/api/model-profiles/${encodeURIComponent(id)}`, body),
  deleteProfile: (id: string) =>
    del<{ deleted: string }>(`/api/model-profiles/${encodeURIComponent(id)}`),
  // Make a profile the instance default. This restarts the default host (the
  // same effect as saving the settings form), it is not a live model switch.
  activateProfile: (id: string) =>
    post<ModelProfile>(`/api/model-profiles/${encodeURIComponent(id)}/activate`),
  pauseProfile: (id: string, paused: boolean) =>
    post<Pick<ModelProfile, 'id' | 'paused' | 'runnable'>>(`/api/model-profiles/${encodeURIComponent(id)}/pause`, { paused }),
  // Force a fresh endpoint catalogue probe (ignores the 5-minute cache) so a
  // corrected URL/key/model shows its verdict immediately. Never returns keys.
  probeProfile: (id: string) =>
    post<ProfileProbe>(`/api/model-profiles/${encodeURIComponent(id)}/probe`),
  // "检测选取模型": ask the *draft* binding's endpoint which models it serves, so
  // the editor can offer exact model ids before saving. The transient key is sent
  // once, used for one GET server-side, and never echoed back.
  modelCatalogue: (body: { base_url: string; model?: string; api_key?: string; id?: string }) =>
    post<ModelCatalogue>('/api/model-catalogue', body),

  // The instance default for --max-runs rides along so the project-concurrency
  // form can show "inherit N" instead of guessing; round_timeout_seconds is the
  // same thing for --round-timeout (0 = unlimited), and verify_auto_default for
  // --verify-auto (D100).
  projectIndex: () =>
    get<{
      projects: ProjectInfo[]
      max_runs: number
      round_timeout_seconds: number
      verify_auto_default: boolean
    }>('/api/projects'),
  projects: () => api.projectIndex().then((r) => r.projects),
  // The 态势 page: one instance-wide snapshot instead of N board requests.
  fleet: () => get<FleetResponse>('/api/fleet'),
  // An operator-authored engagement: 起点 becomes the project brief (scope),
  // 终点 becomes the single endpoint goal. The commander does not derive either.
  createProject: (name: string, start: string, end = '', description = '') =>
    post<ProjectInfo>('/api/projects', { name, scope: start, goal: end, description }),
  // Every field is optional server-side: omitted keeps the current value, and
  // max_runs: null restores "inherit the instance default". worker_profile /
  // strong_profile use "" to clear the project default (inherit the instance
  // active profile). round_timeout: "" inherits --round-timeout, "15m"/"0" set
  // the project's own commander round wall clock (0 = unlimited).
  // verify_auto: null restores "inherit --verify-auto", true/false pins the
  // project's own automatic-verification policy (D100). premise_policy is the
  // project's standard/verified_only admission mode: it is saved with the other
  // project defaults, not on the execution page. Legacy manifests that still
  // say strict are read as verified_only.
  updateProject: (
    id: string,
    body: {
      scope?: string
      description?: string
      max_runs?: number | null
      round_timeout?: string
      worker_profile?: string
      strong_profile?: string
      verify_auto?: boolean | null
      premise_policy?: PremisePolicy
    },
  ) => put<ProjectInfo>(`/api/projects/${encodeURIComponent(id)}`, body),
  deleteProject: (id: string) =>
    del<{ deleted: string; vulns_kept: number; projects: number }>(
      `/api/projects/${encodeURIComponent(id)}?confirm=${encodeURIComponent(id)}`,
    ),

  // The instance library. `total` is the whole library; `matched` is how many
  // rows satisfy the current project/text filter (ignoring limit). The project
  // page reads the same list with a fixed project. Additive, so an older server
  // without `matched` still renders (see Vulns.tsx).
  vulns: (params: { project?: string; q?: string; limit?: number } = {}) => {
    const q = new URLSearchParams()
    if (params.project) q.set('project', params.project)
    if (params.q) q.set('q', params.q)
    if (params.limit) q.set('limit', String(params.limit))
    const suffix = q.toString() ? `?${q}` : ''
    return get<{ vulns: VulnEntry[]; total: number; matched?: number }>(`/api/vulns${suffix}`)
  },
  vulnEvidenceURL: (path: string) => `/api/vulns/evidence/${path.split('/').map(encodeURIComponent).join('/')}`,
  // Saving a library evidence copy is an explicit second request: the plain URL
  // answers inline (preview), this one asks the server for an attachment.
  vulnEvidenceDownloadURL: (path: string) => `${api.vulnEvidenceURL(path)}?download=1`,

  // ---- project: execution locations and their binding (docs/14-nodes.md) ----

  nodes: () => get<NodesResponse>(`${pbase()}/api/nodes`),
  setNodes: (nodes: string[], def: string) =>
    put<NodesResponse>(`${pbase()}/api/nodes`, { nodes, default: def }),
  // The project view of the same catalogue: what a direction may choose.
  modelProfiles: () => get<ModelProfilesResponse>(`${pbase()}/api/model-profiles`),

  // ---- project: collaboration (workers/spawn/kill + peer policy) ----
  //
  // A worker is an intent with a run; the operator sees the whole project's
  // roster and may kill anything still active. Spawn is idempotent: retrying
  // with the same key returns the same worker instead of creating a second one.

  workers: () => get<WorkersResponse>(`${pbase()}/api/workers`),
  spawnWorker: (body: SpawnRequest) => post<SpawnResponse>(`${pbase()}/api/spawn`, body),
  killWorker: (id: string) =>
    post<KillWorkerResponse>(`${pbase()}/api/workers/${encodeURIComponent(id)}/kill`),
  // Who may message whom. Open is the default; allowlist is directional.
  peers: () => get<PeerPolicyResponse>(`${pbase()}/api/peers`),
  savePeers: (body: PeerPolicy) => put<PeerPolicyResponse>(`${pbase()}/api/peers`, body),

  // Premise policy: how hard a direction's hard premises must be. standard is
  // the historical rule (the from_fact exists and is not rejected);
  // verified_only requires every necessary premise to be a reviewable kind with
  // a current-claim independent proof and intact originals. Legacy strict
  // manifests read as verified_only. Operator-only. The response carries the
  // stored choice plus the effective one so the console never presents an
  // assumed state as saved.
  premisePolicy: () => get<PremisePolicyResponse>(`${pbase()}/api/premise-policy`),
  savePremisePolicy: (premise_policy: PremisePolicy) =>
    put<PremisePolicyResponse>(`${pbase()}/api/premise-policy`, { premise_policy }),

  // ---- project namespace ----

  board: () => get<BoardResponse>(`${pbase()}/api/board`),

  // The task-scoped hot/warm/cold projection. It is an index, not a second
  // ledger: cold rows stay readable through layer="cold" and the premise
  // directory in the run's case remains complete. The server scopes a
  // participant to its own direction and refuses a different intent.
  focus: (request: FocusRequest) =>
    get<FocusPage>(`${pbase()}/api/focus?${focusSearchParams(request).toString()}`),

  intents: async () => {
    const out: Intent[] = []; let cursor = ''; const seen = new Set<string>()
    do { const page = await get<ReadPage & {intents: Intent[]}>(`${pbase()}/api/intents?limit=500${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`)
      out.push(...(page.intents ?? [])); if (!page.has_more) break
      if (!page.next_cursor || seen.has(page.next_cursor)) throw new Error('意图分页没有推进')
      cursor = page.next_cursor; seen.add(cursor)
    } while (cursor)
    return out
  },
  intent: (id: string) =>
    get<{ intent: Intent; facts: Fact[] }>(`${pbase()}/api/intents/${encodeURIComponent(id)}`),
  children: (id: string) =>
    get<{ children: ChildIntent[] }>(`${pbase()}/api/intents/${encodeURIComponent(id)}/children`).then(
      (r) => r.children ?? [],
    ),
  // Delegate one piece of work under a live direction. The parent keeps running;
  // the child gets its own run and starts as soon as capacity allows. The console
  // sends the idempotency key it generated, so a retry finds the same child.
  createChildIntent: (parentId: string, body: CreateChildIntentBody) =>
    post<CreateChildIntentResult>(
      `${pbase()}/api/intents/${encodeURIComponent(parentId)}/children`,
      body,
    ),
  // Continue a finished direction on the harness session its last run left
  // behind: the brief is not re-sent, only how the previous turn ended. 409 with
  // code continue_unavailable means there is no session left and a fresh retry
  // is the way forward (ApiError.code carries it through).
  continueIntent: (id: string, note?: string) => {
    const text = note?.trim()
    return post<ContinueIntentResult>(
      `${pbase()}/api/intents/${encodeURIComponent(id)}/continue`,
      text ? { note: text } : {},
    )
  },
  intentUsage: (id: string) =>
    get<{ direct: UsageSummary; subtree: UsageSummary }>(
      `${pbase()}/api/intents/${encodeURIComponent(id)}/usage`,
    ),
  intentReadiness: (id: string) => get<{
    intent_id: string; policy: string; ready: boolean; integrity_checked?: boolean;
    premises: { id: string; title?: string; claim_version?: number; status: string; usable: boolean; reason?: string; review_id?: string; verifier_run?: string; integrity: string }[]
  }>(`${pbase()}/api/intents/${encodeURIComponent(id)}/readiness`),
  handoffIntent: (id: string, profile: string, note: string, expectedVersion: number) =>
    post<{ intent: Intent; run_id: string }>(
      `${pbase()}/api/intents/${encodeURIComponent(id)}/handoff`,
      { profile, note, expected_version: expectedVersion },
    ),
  createIntent: (body: {
    title: string
    description: string
    doc?: string
    from_fact_ids?: string[]
    skills?: string[]
    node?: string
    profile?: string
  }) => post<Intent>(`${pbase()}/api/intents`, body),
  runIntent: (id: string) =>
    post<{ run_id: string; intent_id: string }>(`${pbase()}/api/intents/${encodeURIComponent(id)}/run`),
  // steer cancels the current run and resumes the same trajectory as a new
  // turn. It is the advanced "stop, then redirect" path; say is the default.
  steerIntent: (id: string, note: string, refFact = '') =>
    post<{ run_id: string; intent_id: string; resumed: boolean }>(
      `${pbase()}/api/intents/${encodeURIComponent(id)}/steer`,
      { note, ref_fact: refFact },
    ),
  // say delivers into the live run through the host's IOA bridge. It does not
  // cancel or replace the run; accepted means IOA took the message, not that
  // the model has already read it (D69).
  sayIntent: (id: string, text: string, interrupt = false, refFact = '') =>
    post<{ message_id: string; intent_id: string; accepted: boolean }>(
      `${pbase()}/api/intents/${encodeURIComponent(id)}/say`,
      { text, interrupt, ref_fact: refFact },
    ),
  // chat is the operator side of the project conversation, typed in the 通信
  // page: one sentence to the commander alone, or to the commander plus every
  // executor that can still receive it. The commander's next round reads it and
  // finished directions are skipped instead of revived.
  chat: (body: { to: ChatTarget; text: string; idempotency_key?: string }) =>
    post<ChatResult>(`${pbase()}/api/chat`, body),
  // One operator directive entry: a one-shot message, a persistent instruction,
  // or a steer. The server maps (target, persistent, steer) to the effect and
  // validates combinations; the console never composes invalid ones.
  directive: (body: DirectiveRequest) =>
    post<DirectiveResponse>(`${pbase()}/api/directives`, body),
  instructions: (cursor = '', state = 'all') =>
    get<ReadPage & {instructions: Instruction[]}>(`${pbase()}/api/instructions?state=${encodeURIComponent(state)}&limit=50${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`),
  createInstruction: (body: {text: string; intent_id?: string; source_message_id?: string; idempotency_key: string}) =>
    post<{instruction: Instruction; created: boolean}>(`${pbase()}/api/instructions`, body),
  updateInstruction: (id: string, body: {version: number; text: string; state: Instruction['state']}) =>
    put<Instruction>(`${pbase()}/api/instructions/${encodeURIComponent(id)}`, body),
  instructionHistory: (id: string, cursor = '') =>
    get<ReadPage & {history: {version: number; at: string; value: Instruction}[]}>(`${pbase()}/api/instructions/${encodeURIComponent(id)}/history?limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`),
  parkIntent: (id: string, reason: string) =>
    post<Intent>(`${pbase()}/api/intents/${encodeURIComponent(id)}/park`, { reason }),
  reopenIntent: (id: string) =>
    post<Intent>(`${pbase()}/api/intents/${encodeURIComponent(id)}/reopen`),

  // Retry has two shapes, chosen from a preview: resume the failed run's harness
  // session (memory kept) or start a fresh session (memory dropped). Both
  // re-inject unconfirmed messages; the preview says how many each would redeliver.
  retryPreview: (id: string) =>
    get<RetryPreview>(`${pbase()}/api/intents/${encodeURIComponent(id)}/retry-preview`),
  retryIntent: (id: string, opts: { note?: string; resume: boolean }) =>
    post<RetryResult>(`${pbase()}/api/intents/${encodeURIComponent(id)}/retry`, {
      note: opts.note || undefined,
      resume: opts.resume,
    }),

  retrievalStatus: () => get<{configured:boolean;model?:string}>(`${pbase()}/api/retrieval/status`),
  indexFacts: (cursor = '') => post<ReadPage & {indexed_this_page:number;remaining_to_visit:number}>(`${pbase()}/api/retrieval/index`,{cursor}),
  // Light journal probe: the page is a fixed snapshot, so the console compares
  // high_watermark with data.snapshot_version instead of merging live writes in.
  // `since` asks "did any ledger *content* change after this seq?" (run/message
  // bookkeeping advances the durable seq without a versions row, so a raw
  // watermark comparison would report changes that never touched the graph).
  changes: (params: {cursor?:string;limit?:number;since?:number} = {}) => {
    const q = new URLSearchParams(); for (const [key,value] of Object.entries(params)) if(value !== undefined && value !== '') q.set(key,String(value))
    return get<ChangePage>(`${pbase()}/api/changes?${q}`)
  },
  // order/projection are explicit optimizations for the batched first screen
  // (structural ids + graph projection); omitting them keeps the legacy page.
  graph: (params: {root?:string;cursor?:string;limit?:number;depth?:number;order?:'structural'|'recent';projection?:'full'|'graph'} = {}) => {
    const q = new URLSearchParams(); for (const [key,value] of Object.entries(params)) if(value !== undefined && value !== '') q.set(key,String(value))
    return get<GraphRead>(`${pbase()}/api/graph?${q}`)
  },
  factsPage: (params: {mode?:string;q?:string;kind?:string;intent?:string;status?:string;tag?:string;cursor?:string;limit?:number} = {}) => {
    const q = new URLSearchParams(); for (const [key,value] of Object.entries(params)) if(value !== undefined && value !== '') q.set(key,String(value))
    return get<FactRead>(`${pbase()}/api/facts?${q}`)
  },
  facts: (params: { q?: string; kind?: string; intent?: string; limit?: number } = {}) => {
    const q = new URLSearchParams()
    if (params.q) q.set('q', params.q)
    if (params.kind) q.set('kind', params.kind)
    if (params.intent) q.set('intent', params.intent)
    if (params.limit) q.set('limit', String(params.limit))
    const suffix = q.toString() ? `?${q}` : ''
    return get<{ facts: Fact[] }>(`${pbase()}/api/facts${suffix}`).then((r) => r.facts ?? [])
  },
  fact: (id: string) =>
    get<{ fact: Fact; evidence: Evidence[]; reviews: Review[] }>(
      `${pbase()}/api/facts/${encodeURIComponent(id)}`,
    ),
  verifyFact: (id: string) =>
    post<{ queued: boolean; fact_id: string }>(`${pbase()}/api/facts/${encodeURIComponent(id)}/verify`),
  // The vulnerability library is instance-level, so its rows carry project_id
  // and the URL has no /p/ prefix: these two take the project explicitly instead
  // of reading it from the address bar.
  verifyFactInProject: (projectId: string, factId: string) =>
    post<{ queued: boolean; fact_id: string }>(
      `/p/${encodeURIComponent(projectId)}/api/facts/${encodeURIComponent(factId)}/verify`,
    ),
  // Queue a verifier run for every eligible unverified vuln/cred of the project.
  // 202 and {queued, skipped}; 409 when a batch is already running, 503 when no
  // verifier is configured.
  verifyPendingFacts: (projectId: string) =>
    post<VerifyPendingResult>(
      `/p/${encodeURIComponent(projectId)}/api/facts/verify-pending`,
    ),
  // The review switch is per project and independent of the project pause (D137):
  // pausing reviews never resumes the commander, and a paused project can still
  // re-prove a finding. Project-scoped, like the batch endpoint.
  verifierStatus: () => get<VerifierStatus>(`${pbase()}/api/verifier`),
  pauseReviews: () =>
    post<{ paused: boolean; canceled: number }>(`${pbase()}/api/verifier/pause`),
  resumeReviews: () => post<{ paused: boolean }>(`${pbase()}/api/verifier/resume`),
  // A human verdict: reject the conclusion, or put it back to unverified. There
  // is deliberately no "confirm" — that is what a verifier rerun is for.
  reviewFact: (id: string, verdict: 'reject' | 'needs_evidence', notes: string) =>
    post<{ review: Review }>(`${pbase()}/api/facts/${encodeURIComponent(id)}/review`, { verdict, notes }),
  reviews: (fact?: string) =>
    get<{ reviews: Review[] }>(
      `${pbase()}/api/reviews${fact ? `?fact=${encodeURIComponent(fact)}` : ''}`,
    ).then((r) => r.reviews ?? []),

  // Project knowledge, not facts: an executor declares the tactic in the run
  // that used it (confirmed); the commander can only nominate a candidate, and
  // a same-titled declaration promotes it (D70). There is no create here on
  // purpose — a human writes through a run, not by typing a recipe.
  playbooksPage: (params: {q?:string;tag?:string;status?:string;archived?:string;cursor?:string}={}) => {
    const query=new URLSearchParams({limit:'50'});for(const [k,v] of Object.entries(params))if(v)query.set(k,v)
    return get<ReadPage & PlaybooksResponse & {counts:Record<string,number>}>(`${pbase()}/api/playbooks?${query}`)
  },
  archivePlaybook: (p:Playbook,archived:boolean) => post<Playbook>(`${pbase()}/api/playbooks/${encodeURIComponent(p.id)}/archive`,{updated_at:p.updated_at,archived}),
  playbookHistory: (id:string,cursor='') => get<ReadPage & {history:{seq:number;version:number;operation:string;at:string;value?:Playbook}[]}>(`${pbase()}/api/playbooks/${encodeURIComponent(id)}/history?limit=20${cursor?`&cursor=${encodeURIComponent(cursor)}`:''}`),
  retirePlaybook: (id: string) =>
    post<Playbook>(`${pbase()}/api/playbooks/${encodeURIComponent(id)}/retire`),
  deletePlaybook: (id: string, updated_at: string) => request<{deleted: boolean}>(`${pbase()}/api/playbooks/${encodeURIComponent(id)}`, {method:'DELETE', body:JSON.stringify({updated_at})}),

  evidence: () => get<{ evidence: Evidence[] }>(`${pbase()}/api/evidence`).then((r) => r.evidence ?? []),
  // Opening this URL previews (Content-Disposition: inline); saving the original
  // is a separate, explicit request. The distinction matters for binary captures
  // that the browser would otherwise dump straight into the download tray.
  evidenceURL: (id: string) => `${pbase()}/api/evidence/${encodeURIComponent(id)}`,
  evidenceDownloadURL: (id: string) =>
    `${pbase()}/api/evidence/${encodeURIComponent(id)}?download=1`,

  goals: () => get<{ goals: Goal[] }>(`${pbase()}/api/goals`).then((r) => r.goals ?? []),
  // Operator-defined endpoint: 起点 (where we start) + 终点 (falsifiable end
  // state). There is no quantified-criterion input any more.
  createGoal: (statement: string, start: string) =>
    post<Goal>(`${pbase()}/api/goals`, { statement, start }),
  // A seal is an argument, not a checkbox: the note says why, the evidence id
  // points at the proof, the server keeps both on the goal.
  metGoal: (id: string, evidence_id: string, note: string) =>
    post<Goal>(`${pbase()}/api/goals/${encodeURIComponent(id)}/met`, { evidence_id, note }),

  state: () => get<{ state: string; path: string }>(`${pbase()}/api/state`),
  putState: (state: string) => put<{ saved: boolean; bytes: number }>(`${pbase()}/api/state`, { state }),

  decisions: (limit = 50) =>
    get<{ decisions: Decision[] }>(`${pbase()}/api/decisions?limit=${limit}`).then(
      (r) => r.decisions ?? [],
    ),
  decide: (reason = 'operator') => post<{ queued: boolean }>(`${pbase()}/api/decide`, { reason }),
  pauseDecider: () => post<DeciderStatus>(`${pbase()}/api/decider/pause`),
  resumeDecider: () => post<DeciderStatus>(`${pbase()}/api/decider/resume`),
  // The whole-project switch: pause the commander and cancel every live run.
  // Canceled directions go back to pending, so Resume lets the commander pick
  // them up again.
  pauseProject: () =>
    post<{ paused: boolean; canceled: number }>(`${pbase()}/api/project/pause`),
  resumeProject: () => post<{ paused: boolean }>(`${pbase()}/api/project/resume`),
  // The explicit operator action for a finished or parked engagement: append a
  // new independent goal and, when the project is in a resumable stand-by
  // (completion or manual pause), clear the pause and wake the commander. It is
  // NOT ordinary chat: a plain message never resumes a paused project. The
  // idempotency_key is required so a retry (lost response / failed resume) finds
  // the goal it already created instead of appending a second one.
  appendGoal: (body: AppendGoalRequest) =>
    post<AppendGoalResult>(`${pbase()}/api/project/append-goal`, body),

  runs: () => get<{ runs: Run[] }>(`${pbase()}/api/runs`).then((r) => r.runs ?? []),

  // Participant conversation. Messages are records, not facts: the console shows
  // the delivery state ("accepted" is not "read") and never feeds them to the
  // graph.
  messages: (limit = 200) =>
    get<{ messages: Message[] }>(`${pbase()}/api/messages?limit=${limit}`).then((r) => r.messages ?? []),

  // Per-recipient queue pressure + routing checkpoints. The operator sees every
  // queue; a participant credential is narrowed to its own by the server.
  communication: (recipient = '') => {
    const query = new URLSearchParams()
    if (recipient) query.set('recipient', recipient)
    const suffix = query.toString() ? `?${query}` : ''
    return get<CommunicationStatus>(`${pbase()}/api/communication${suffix}`)
  },

  ioaObservations: () => get<IOAObservations>(`${pbase()}/api/ioa/events`),
  instanceIOAObservations: () => get<IOAObservations>('/api/ioa/events'),
  participants: () =>
    get<{ participants: Participant[] }>(`${pbase()}/api/participants`).then(
      (r) => r.participants ?? [],
    ),

  // The executor's view: the brief, the graph it was handed, and what it
  // declared/asked. Read from the case dir, nothing extra is stored.
  runContext: (id: string) =>
    get<RunContext>(`${pbase()}/api/runs/${encodeURIComponent(id)}/context`),

  // Tool-call leaderboard + installed arsenal binaries (api/tools.go).
  tools: () => get<ToolsResponse>(`${pbase()}/api/tools`),
  runtimeTools: () => get<RuntimeToolsResponse>(`${pbase()}/api/tools/runtime`),
  events: (id: string, limit = 300) =>
    get<{ events: Event[] }>(
      `${pbase()}/api/runs/${encodeURIComponent(id)}/events?limit=${limit}`,
    ).then((r) => r.events ?? []),
  cancelRun: (id: string) =>
    post<{ canceled: string }>(`${pbase()}/api/runs/${encodeURIComponent(id)}/cancel`),

  reports: () => get<{ reports: ReportMeta[] }>(`${pbase()}/api/reports`).then((r) => r.reports ?? []),
  // Advisory precheck run before a report is queued: how many conclusions would
  // land, how many are still unverified (they can only be candidates), how many
  // are rejected, and whether a report is already being written (D100).
  reportPrecheck: () => get<ReportPrecheck>(`${pbase()}/api/reports/precheck`),
  createReport: () => post<{ queued: boolean }>(`${pbase()}/api/reports`),
  report: (id: string) => get<ReportMeta>(`${pbase()}/api/reports/${encodeURIComponent(id)}`),
  reportFileText: async (id: string, path: string) => {
    const url = `${pbase()}/api/reports/${encodeURIComponent(id)}/file?path=${encodeURIComponent(path)}`
    const res = await fetch(url)
    if (!res.ok) throw new ApiError(res.status, `无法读取 ${path}`)
    return res.text()
  },
  reportFileURL: (id: string, path: string) =>
    `${pbase()}/api/reports/${encodeURIComponent(id)}/file?path=${encodeURIComponent(path)}`,

  // The template library is read server-side filtered/paged: total is the whole
  // library, matched is how many rows satisfy q (before offset/limit). Omit
  // every param to get the whole list, which is what older callers expect.
  pocs: (params: { q?: string; offset?: number; limit?: number } = {}) => {
    const query = new URLSearchParams()
    if (params.q) query.set('q', params.q)
    if (params.offset) query.set('offset', String(params.offset))
    if (params.limit) query.set('limit', String(params.limit))
    const suffix = query.toString() ? `?${query}` : ''
    return get<{ dir: string; pocs: Poc[] | null; total?: number; matched?: number }>(
      `/api/pocs${suffix}`,
    )
  },
  importPocs: (path: string) => post<ImportResult>('/api/pocs/import', { path }),
  importTraffic: (path: string) => post<ImportResult>(`${pbase()}/api/traffic/import`, { path }),
  httpFlows: (q = '', offset = 0) => get<{flows: HTTPFlowRecord[]; total: number}>(`${pbase()}/api/traffic/flows?q=${encodeURIComponent(q)}&offset=${offset}&limit=100`),
  httpFlow: (ref: string) => get<HTTPFlow>(`${pbase()}/api/traffic/flow?ref=${encodeURIComponent(ref)}`),
  httpFlowURL: (ref: string) => `${pbase()}/api/traffic/flow?ref=${encodeURIComponent(ref)}&download=1`,
  importAssets: (path: string) => post<ImportResult>(`${pbase()}/api/assets/import`, { path }),

  assets: () => get<{ hosts: AssetHost[] }>(`${pbase()}/api/assets`).then((r) => r.hosts ?? []),

  exportBundle: () =>
    post<{ name: string; path: string; size: number; files: number; missing: number }>(
      `${pbase()}/api/bundles/export`,
    ),
  bundleURL: (name: string) => `/api/bundles/${encodeURIComponent(name)}`,
}

/** Switch the page to another project. A full navigation keeps one source of
 *  truth: the project lives in the URL, not in client state. */
export function gotoProject(id: string) {
  window.location.assign(`/p/${encodeURIComponent(id)}/`)
}
