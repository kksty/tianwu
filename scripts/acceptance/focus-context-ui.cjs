// Offline browser acceptance for the orchestration-foundation UI surfaces:
//   * 焦点 (focus) panel in GraphView (hot/warm/cold counts, reasons, layer
//     switch, frozen-snapshot pagination, "open on graph" deep link)
//   * intent context editor (refs/pins/entities, context_version CAS, 409, 403)
//   * fact claim detail (assertion version vs the version each review proved)
//   * graph relation distinction (premise vs reference in the legend/inspector)
//   * run evidence view (frozen premise/context versions)
//
// The harness is the same one as scripts/acceptance/instructions-ui.cjs: a
// temporary headless Chrome over CDP, every request intercepted, `/static/*`
// served from the built internal/web/static and every /api response synthesized
// in this file. No Tianwu console/harness, no project data and no network
// beyond localhost static/CDP are touched.
//
// Canvas relation edges are pixels, not DOM: the script locates a relation edge
// by sampling the canvas for the colour render.ts paints it with, moves the
// headless pointer onto that pixel and then asserts the *DOM* inspector's
// relation label (必要前提 / 引用/固定). The stroke colour/dash pattern itself
// is not an assertion target (see the `unverified` list in the JSON result).
//
// Run: node scripts/acceptance/focus-context-ui.cjs
// Screenshots: .build/ui-qa/focus-*, graph-*, context-editor-*, claim-*.png
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');

const root = path.resolve(__dirname, '../..');
const webRoot = path.resolve(process.env.TW_FOCUS_WEB || path.join(root, 'internal/web/static'));
const out = path.join(root, '.build/ui-qa');
fs.mkdirSync(out, { recursive: true });

const chromePath = process.env.TW_CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe';
if (!fs.existsSync(chromePath)) {
  // Only a genuinely missing browser is skipped; every other failure below is a
  // failed run. Exit 0 so a caller can treat "no Chrome on this box" as N/A.
  process.stdout.write(
    JSON.stringify(
      {
        skipped: true,
        reason: 'skipped: no chrome',
        chrome: chromePath,
        passed: false,
        checks: [],
        note: 'Skipped only because the headless Chrome binary was not found; every other harness failure is a failed run.',
      },
      null,
      2,
    ) + '\n',
  );
  process.exit(0);
}

const pageURL = 'http://tianwu.test/p/fixture/';
const profile = fs.mkdtempSync(path.join(out, 'focus-context-chrome-'));
const chrome = spawn(
  chromePath,
  [
    '--headless=new',
    '--disable-gpu',
    '--no-first-run',
    '--no-default-browser-check',
    '--remote-debugging-port=0',
    '--remote-debugging-address=127.0.0.1',
    '--user-data-dir=' + profile,
    'about:blank',
  ],
  { windowsHide: true, stdio: 'ignore' },
);
const pause = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

/** On Windows crashpad/utility children outlive the main process; kill the tree. */
function stopChrome(proc) {
  if (!proc) return;
  try {
    if (process.platform === 'win32') {
      spawn('taskkill', ['/pid', String(proc.pid), '/T', '/F'], { windowsHide: true, stdio: 'ignore' });
    } else {
      proc.kill();
    }
  } catch {
    proc.kill();
  }
}

const at = '2026-11-12T10:00:00+08:00';
const snapshot = { epoch: 'fixture-snapshot-7', seq: 7 };
const snapshotLabel = '快照 ' + snapshot.epoch.slice(-8) + ' #' + snapshot.seq; // mirrors lib/focus.ts

// ---- synthesized ledger --------------------------------------------------
// One intent, a hard premise (from_fact_ids), an explicit reference/pin pair,
// facts produced by the direction, a stale-claim fact and a verified fact.
function fact(id, kind, title, extra = {}) {
  return {
    id,
    intent_id: 'intent_in_1',
    kind,
    title,
    summary: extra.summary ?? '隔离验收事实：' + title,
    doc: '',
    confidence: 'observed',
    origin: 'declared',
    proof: 'dynamic',
    status: extra.status ?? 'unverified',
    created_at: at,
    evidence_ids: extra.evidence_ids ?? [],
    refs: [],
    claim_version: extra.claim_version ?? 1,
    declaration_id: extra.declaration_id ?? 'decl_' + id,
    claim_hash: extra.claim_hash ?? 'hash-' + id + '-0123456789abcdef',
    observed_at: at,
    execution_context_ref: 'run_1/' + id,
    ...extra,
  };
}

const factsById = {
  f_premise: fact('f_premise', 'note', '硬前提：登录口可达', {
    intent_id: '', // from an earlier direction; this is a hard prerequisite, not its output
    summary: '侦察阶段记录的登录入口，是本次派发的硬前提',
    claim_version: 2,
    declaration_id: 'decl_login_gate',
    claim_hash: '9c1f2a7b3d4e5f60718293a4b5c6d7e8f9012345',
    execution_context_ref: 'run_0/step_2',
  }),
  f_ref_a: fact('f_ref_a', 'note', '参考：历史登录尝试', {
    intent_id: '', // a reading reference, not produced by this direction
    summary: '被引用阅读的旧记录',
  }),
  f_pin_a: fact('f_pin_a', 'loot', '固定：配置备份清单', {
    summary: '操作员固定的上下文观察',
    status: 'unverified',
  }),
  f_out: fact('f_out', 'vuln', '越权读取成立', {
    summary: '本次方向产出的结论',
    evidence_ids: ['ev_1'],
    claim_version: 1,
  }),
  f_stale: fact('f_stale', 'vuln', '旧断言复核样例', {
    summary: '复核通过的是 v1，当前断言已是 v2',
    claim_version: 2,
    declaration_id: 'decl_stale_case',
    claim_hash: 'aaaa1111bbbb2222cccc3333dddd4444eeee5555',
  }),
  f_verified: fact('f_verified', 'vuln', '当前断言已复核样例', {
    summary: '当前断言的复核结论就是通过',
    status: 'verified',
    claim_version: 1,
    declaration_id: 'decl_verified_case',
  }),
  f_hot_1: fact('f_hot_1', 'note', '热行：必要前提事实', {
    summary: '由 from_fact_ids 记录为必要前提',
    claim_version: 2,
    record_version: 2,
    source_run_id: 'run_1',
  }),
  f_hot_2: fact('f_hot_2', 'loot', '热行：固定事实', {
    summary: '由 pinned_fact_ids 记录为固定',
    claim_version: 1,
  }),
  f_warm_1: fact('f_warm_1', 'host', '温行：来源链事实', {
    summary: '来自来源链，并且记录实体相同',
    status: 'verified',
    claim_version: 1,
    record_version: 3,
    source_run_id: 'run_1',
    observed_at: '2026-11-12T09:30:00+08:00',
  }),
  f_cold_1: fact('f_cold_1', 'note', '冷行：旁站资产笔记', { summary: '未被本任务引用的其余事实' }),
  f_cold_2: fact('f_cold_2', 'host', '冷行：历史端口', { summary: '冷层仍可检索' }),
  f_cold_3: fact('f_cold_3', 'note', '冷行：旧观察', { summary: '冷层第三条' }),
};
// The 焦点 counts describe the whole projection, not one page.
factsById.f_hot_1.record_version = 2;
factsById.f_hot_2.record_version = undefined; // absent => UI must say 记录版本未知
factsById.f_warm_1.record_version = 3;

const graphFactIDs = [
  'f_premise', 'f_ref_a', 'f_pin_a', 'f_out', 'f_stale', 'f_verified',
  'f_hot_1', 'f_hot_2', 'f_warm_1',
];
const allFacts = [...graphFactIDs, 'f_cold_1', 'f_cold_2', 'f_cold_3'].map((id) => factsById[id]);

const reviewsByFact = {
  f_stale: [
    {
      id: 'rev_stale_v1',
      fact_id: 'f_stale',
      verdict: 'pass',
      claim_version: 1,
      claim_hash: 'stale-v1-hash',
      notes: 'v1 已复核：复现成功',
      verifier_run: 'run_v1',
      created_at: '2026-11-12T09:00:00+08:00',
    },
  ],
  f_verified: [
    {
      id: 'rev_verified_v1',
      fact_id: 'f_verified',
      verdict: 'pass',
      claim_version: 1,
      claim_hash: 'verified-v1-hash',
      notes: '当前断言复核通过',
      verifier_run: 'run_v2',
      created_at: at,
    },
  ],
};

const run1 = {
  id: 'run_1',
  intent_id: 'intent_in_1',
  node: 'local',
  status: 'done',
  started_at: '2026-11-12T09:10:00+08:00',
  ended_at: '2026-11-12T09:40:00+08:00',
  events: 42,
  declared_facts: 3,
  model: 'mock-model',
  resolved_profile: 'mock-profile',
  // Frozen at dispatch (internal/store/input.go): premise assertion versions and
  // referenced-fact ledger record versions.
  premise_claim_versions: { f_premise: 2 },
  context_fact_versions: { f_ref_a: 3 },
  warnings: [],
};

const board = {
  generated_at: at,
  case: 'fixture',
  intents: { total: 1, pending: 1, claimed: 0, running: 0, waiting: 0, done: 0, failed: 0, parked: 0 },
  facts: { total: allFacts.length, with_evidence: 1, with_refs: 0, verified: 1, by_kind: { note: 6, loot: 2, vuln: 3, host: 1 }, by_origin: { declared: allFacts.length } },
  evidence: 1,
  runs: { total: 1, running: 0, done: 1, failed: 0, canceled: 0 },
  goals: { total: 1, met: 1, open: [] },
  playbooks: { total: 0, confirmed: 0, candidates: 0 },
  decider: { enabled: true, rounds: 0, tokens_total: 0, stalled: 0, paused: true, paused_reason: '隔离验收暂停' },
  project_paused: true,
  past_playbooks: [],
  handoff_warnings: [],
  usage: {
    total_tokens: 123456,
    input_tokens: 80000,
    output_tokens: 43456,
    cached_input_tokens: 20000,
    requests: 42,
    missing_usage: 3,
    unknown_usage_runs: 2,
  },
  open_intents: [],
  waiting_intents: [],
  running_intents: [],
  recent_facts: [],
  done_intents: [],
  frontier: [],
  ports: [],
  coverage: [],
  held_intents: [],
};

// Mutable server state for the context editor: version CAS, refs, pins, entities.
const state = {
  intent: {
    id: 'intent_in_1',
    title: '验证登录口越权读取',
    description: '在隔离靶场上确认越权读取是否成立',
    doc: '',
    status: 'pending',
    attempts: 1,
    node: 'local',
    profile: '',
    handoff_version: 0,
    say_count: 0,
    steer_count: 0,
    notes: [],
    interjections: [],
    from_fact_ids: ['f_premise'],
    created_at: at,
    updated_at: at,
  },
  contextVersion: 1,
  references: ['f_ref_a'],
  pins: ['f_pin_a'],
  entities: ['host:10.0.0.5'],
  // 'ok' | 'conflict' | 'forbidden' — selected per scenario.
  contextMode: 'ok',
  focusRequests: [],
  contextWrites: [],
  graphRequests: [],
};

function intentJSON() {
  return {
    ...state.intent,
    from_fact_ids: [...state.intent.from_fact_ids],
    context_fact_ids: [...state.references],
    pinned_fact_ids: [...state.pins],
    interest_entity_refs: [...state.entities],
    context_version: state.contextVersion,
  };
}

function focusRows() {
  return {
    hot: [factsById.f_hot_1, factsById.f_hot_2],
    warm: [factsById.f_warm_1],
    cold: [factsById.f_cold_1, factsById.f_cold_2, factsById.f_cold_3],
  };
}

const focusFact = (source, temperature, reasons, extra = {}) => ({
  id: source.id,
  kind: source.kind,
  title: source.title,
  summary: source.summary,
  status: source.status,
  temperature,
  reasons,
  claim_version: source.claim_version,
  record_version: source.record_version,
  source_run_id: source.source_run_id,
  observed_at: source.observed_at,
  query: '/api/facts/' + source.id,
  ...extra,
});

function focusPage(query) {
  const layer = query.get('layer') || 'focus';
  const cursor = query.get('cursor') || '';
  const intent = query.get('intent') || '';
  state.focusRequests.push({ intent, layer, cursor, limit: query.get('limit') || '' });
  const rows = focusRows();
  const base = {
    intent_id: 'intent_in_1',
    layer,
    hot: rows.hot.length,
    warm: rows.warm.length,
    cold: rows.cold.length,
    index_query: '/api/focus?intent=intent_in_1',
    cold_query: '/api/focus?intent=intent_in_1&layer=cold',
    fact_query: '/api/facts?intent=intent_in_1',
    premises: '.tw/context.json',
    experience_query: '/api/experience?intent=intent_in_1',
    total: 0,
    has_more: false,
    snapshot_version: { ...snapshot },
  };
  if (layer === 'cold') {
    return {
      ...base,
      facts: rows.cold.map((f) => focusFact(f, 'cold', [])),
      total: rows.cold.length,
    };
  }
  if (layer === 'hot') {
    return {
      ...base,
      facts: rows.hot.map((f, i) => focusFact(f, 'hot', i === 0 ? ['required'] : ['pinned'])),
      total: rows.hot.length,
    };
  }
  if (layer === 'warm') {
    return {
      ...base,
      facts: rows.warm.map((f) => focusFact(f, 'warm', ['provenance', 'same_recorded_entity'])),
      total: rows.warm.length,
    };
  }
  // Default bounded index: hot + warm, paged from one frozen snapshot. Page 1
  // stops after the two hot rows; page 2 is the warm row of the same snapshot.
  if (!cursor) {
    return {
      ...base,
      facts: [
        focusFact(rows.hot[0], 'hot', ['required']),
        focusFact(rows.hot[1], 'hot', ['pinned']),
      ],
      total: 3,
      has_more: true,
      next_cursor: 'cursor-focus-2',
    };
  }
  return {
    ...base,
    facts: [focusFact(rows.warm[0], 'warm', ['provenance', 'same_recorded_entity'])],
    total: 3,
    has_more: false,
  };
}

// A ~1100-row synthetic project for the large-graph smoke (docs/33 P0 acceptance):
// 100 directions chained one to the next, each producing 10 facts. It is a
// fixture, not a benchmark: the point is that opening the real page with the
// real renderer keeps the main thread free of long tasks and that the map,
// legend, minimap, list view and keyboard selection all still work.
const largeGraph = (() => {
  const intentCount = 100;
  const factsPerIntent = 10;
  const facts = [];
  const intents = [];
  const factById = {};
  const base = Date.parse('2026-11-12T08:00:00+08:00');
  const kinds = ['host', 'service', 'cred', 'vuln', 'loot', 'note'];
  for (let i = 0; i < intentCount; i++) {
    const id = 'in_l' + String(i).padStart(3, '0');
    intents.push({
      id,
      title: '大图方向 ' + i,
      description: '第 ' + i + ' 条链路方向（合成 fixture，用于大图冒烟）',
      doc: '',
      status: i === intentCount - 1 ? 'running' : 'done',
      attempts: 1,
      node: 'local',
      profile: '',
      handoff_version: 0,
      say_count: 0,
      steer_count: 0,
      notes: [],
      interjections: [],
      // Chain: this direction consumes the previous direction's first output.
      from_fact_ids: i === 0 ? [] : ['lf_' + String((i - 1) * factsPerIntent).padStart(4, '0')],
      context_fact_ids: i % 7 === 0 && i > 0 ? ['lf_' + String(i * factsPerIntent - 1).padStart(4, '0')] : [],
      pinned_fact_ids: i % 11 === 0 ? ['lf_' + String(i * factsPerIntent + 1).padStart(4, '0')] : [],
      interest_entity_refs: [],
      created_at: new Date(base + i * 60000).toISOString(),
      updated_at: new Date(base + i * 60000).toISOString(),
    });
    for (let j = 0; j < factsPerIntent; j++) {
      const idx = i * factsPerIntent + j;
      const fid = 'lf_' + String(idx).padStart(4, '0');
      const kind = kinds[idx % kinds.length];
      const row = fact(fid, kind, '大图事实 ' + idx, {
        intent_id: id,
        summary: '合成大图 fixture 的事实摘要 #' + idx,
        created_at: new Date(base + idx * 1000).toISOString(),
        claim_version: 1 + (idx % 3),
      });
      facts.push(row);
      factById[fid] = row;
    }
  }
  return { facts, intents, factById };
})();

function largeGraphPage() {
  return {
    facts: largeGraph.facts,
    intents: largeGraph.intents,
    goals: [],
    edges: [],
    boundary: [],
    details: [],
    fact_meta: largeGraph.facts.map((row) => ({
      id: row.id,
      needs_review: row.claim_version > 1 && row.id.endsWith('7'),
      reviewed_claim_version: row.claim_version > 1 && row.id.endsWith('7') ? row.claim_version - 1 : row.claim_version,
      evidence_count: 0,
      ref_count: 0,
    })),
    intents_total: largeGraph.intents.length,
    facts_total: largeGraph.facts.length,
    goals_total: 0,
    total: largeGraph.facts.length + largeGraph.intents.length,
    has_more: false,
    snapshot_version: { ...snapshot },
  };
}

function graphPage() {
  if (state.graphCase === 'large') return largeGraphPage();
  state.graphRequests.push({ root: '', limit: '200' });
  return {
    facts: graphFactIDs.map((id) => factsById[id]),
    intents: [intentJSON()],
    goals: [],
    // A reading reference the ledger proved: premise edge comes from
    // intent.from_fact_ids, this one from GET /api/graph edges[].kind=reference.
    edges: [{ source: 'fact:f_ref_a', target: 'intent:intent_in_1', kind: 'reference' }],
    boundary: [],
    details: [],
    total: graphFactIDs.length + 1,
    has_more: false,
    snapshot_version: { ...snapshot },
  };
}

function putContext(body) {
  state.contextWrites.push({
    version: body.version,
    references: [...(body.references ?? [])],
    pins: [...(body.pins ?? [])],
    entities: [...(body.entities ?? [])],
  });
  if (state.contextMode === 'conflict') {
    // Simulate another editor having committed after this editor opened: the
    // server is now one version ahead with a different reading selection.
    state.contextVersion = body.version + 1;
    state.references = ['f_ref_a', 'f_hot_1'];
    return { code: 409, value: { code: 'context_conflict', error: '上下文版本冲突：已被他人修改' } };
  }
  if (state.contextMode === 'forbidden') {
    return { code: 403, value: { code: 'context_denied', error: '只能更新自己持有的方向' } };
  }
  if (body.version !== state.contextVersion) {
    return { code: 409, value: { code: 'context_conflict', error: '上下文版本已过期' } };
  }
  state.contextVersion += 1;
  state.references = [...(body.references ?? [])];
  state.pins = [...(body.pins ?? [])];
  state.entities = [...(body.entities ?? [])];
  return intentJSON();
}

// ---- synthesized HTTP ----------------------------------------------------
const CONTENT_TYPES = {
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.json': 'application/json; charset=utf-8',
  '.html': 'text/html; charset=utf-8',
  '.woff2': 'font/woff2',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
};

const appendGoalWrites = [];
let appendedGoal;
const priorGoal = { id: 'g_prior_fixture', statement: '已完成的隔离目标', met: true, created_at: at };
async function api(url, req) {
  const p = url.pathname;
  const q = url.searchParams;
  const method = req.method || 'GET';
  const body = req.postData ? JSON.parse(req.postData) : {};
  if (p.endsWith('/api/project/append-goal') && method === 'POST') {
    appendGoalWrites.push(body);
    appendedGoal ||= { id: 'g_append_fixture', statement: body.statement, met: false, created_at: at };
    if (appendGoalWrites.length === 1) return { code: 500, value: { code: 'resume_failed', error: '目标已追加，恢复失败', goal: appendedGoal, retryable: true } };
    assert.equal(body.idempotency_key, appendGoalWrites[0].idempotency_key, 'reload must retain the original operation');
    return { goal: appendedGoal, created: false, resumed: true, resumed_from: 'manual_pause', decider_woken: true, decider_paused: false, paused_before: true, paused_after: false, pause_kind_before: 'manual_pause', pause_kind_after: 'running', retryable: false, note: '同一个目标已恢复继续' };
  }
  if (p.endsWith('/api/goals')) return { goals: appendedGoal ? [priorGoal, appendedGoal] : [priorGoal] };

  // Instance surface
  if (p === '/api/projects') {
    return {
      projects: [{ id: 'fixture', name: '编排验收', scope: '隔离测试范围', description: '离线 fixture', created: at, paused: true }],
      max_runs: 2,
      round_timeout_seconds: 0,
      verify_auto_default: false,
    };
  }
  if (p === '/api/model-profiles') return { profiles: [], active_profile: '', has_overrides: false };
  if (p === '/api/health') return { ok: true, executor: 'fixture' };

  // Project surface: shell chrome
  if (p.endsWith('/api/board')) return board;
  if (p.endsWith('/api/operator-requests')) return { requests: [] };
  if (p.endsWith('/api/model-profiles')) return { profiles: [], active: '', local_node: 'local' };
  if (p.endsWith('/api/retrieval/status')) return { configured: false };
  if (p.endsWith('/api/runs')) return { runs: [run1] };
  if (p.endsWith('/readiness')) return { intent_id: 'intent_in_1', policy: 'verified_only', ready: false, premises: [{ id: allFacts[0].id, title: allFacts[0].title, claim_version: 2, status: 'unverified', usable: false, reason: '断言 v2 尚未通过独立核验', integrity: 'unknown' }] };
  if (p.endsWith('/api/premise-policy')) return { premise_policy: 'verified_only', effective: 'verified_only' };

  // Primary timeline/graph read. A root focus request is answered with the same
  // fixture snapshot; the URL shape is asserted separately where it matters.
  if (p.endsWith('/api/graph')) return graphPage();

  // The fact picker (limit 500) and the ledger facts read.
  if (p.endsWith('/api/facts') && method === 'GET') {
    return { facts: allFacts, total: allFacts.length, has_more: false, snapshot_version: { ...snapshot } };
  }

  const factMatch = /\/api\/facts\/([^/]+)$/.exec(p);
  if (factMatch) {
    const id = decodeURIComponent(factMatch[1]);
    const row = factsById[id];
    if (!row) return { code: 404, value: { error: '事实不存在' } };
    return { fact: row, evidence: [], reviews: reviewsByFact[id] ?? [] };
  }

  // Focus projection.
  if (p.endsWith('/api/focus') && method === 'GET') return focusPage(q);

  // Intent list (focus panel direction picker).
  if (p.endsWith('/api/intents') && method === 'GET') {
    return {
      intents: [{ id: 'intent_in_1', title: '验证登录口越权读取', status: 'pending' }],
      total: 1,
      has_more: false,
      snapshot_version: { ...snapshot },
    };
  }

  const usageMatch = /\/api\/intents\/([^/]+)\/usage$/.exec(p);
  if (usageMatch) {
    return {
      direct: { total_tokens: 1200, hour_tokens: 0, input_tokens: 800, output_tokens: 400, cached_input_tokens: 0, cache_write_tokens: 0, requests: 3, missing: 0, known: true },
      subtree: { total_tokens: 1200, hour_tokens: 0, input_tokens: 800, output_tokens: 400, cached_input_tokens: 0, cache_write_tokens: 0, requests: 3, missing: 0, known: true, runs: 1, unknown_usage_runs: 0 },
    };
  }
  const childrenMatch = /\/api\/intents\/([^/]+)\/children$/.exec(p);
  if (childrenMatch) return { children: [] };

  const contextMatch = /\/api\/intents\/([^/]+)\/context$/.exec(p);
  if (contextMatch && method === 'PUT') return putContext(body);

  const intentMatch = /\/api\/intents\/([^/]+)$/.exec(p);
  if (intentMatch && method === 'GET') return { intent: intentJSON(), facts: allFacts };

  // Any other shell poll (health, decisions, …) is not this page's contract.
  if (p.includes('/api/')) return {};
  return undefined;
}

const webFile = (pathname) => {
  const rel = decodeURIComponent(pathname.slice('/static/'.length));
  const file = path.resolve(webRoot, rel);
  if (!file.startsWith(path.resolve(webRoot))) return null;
  if (!fs.existsSync(file) || !fs.statSync(file).isFile()) return null;
  return file;
};

// ---- browser harness -----------------------------------------------------
(async () => {
  let ws;
  const checks = [];
  const shots = [];
  const errors = [];
  const check = (name, passed, expected, actual) => {
    const row = { name, passed: Boolean(passed), expected, actual };
    checks.push(row);
    return row.passed;
  };
  let finalResult;

  try {
    const portFile = path.join(profile, 'DevToolsActivePort');
    for (let n = 0; n < 150 && !fs.existsSync(portFile); n++) await pause(100);
    if (!fs.existsSync(portFile)) throw new Error('Chrome did not open a DevTools port: ' + portFile);
    const port = Number(fs.readFileSync(portFile, 'utf8').split('\n')[0]);
    const pages = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
    const target = pages.find((x) => x.type === 'page');
    if (!target) throw new Error('headless Chrome has no page target');
    ws = new WebSocket(target.webSocketDebuggerUrl);
    await new Promise((resolve, reject) => {
      ws.addEventListener('open', resolve, { once: true });
      ws.addEventListener('error', reject, { once: true });
    });

    let seq = 0;
    const pending = new Map();
    const call = (method, params = {}) =>
      new Promise((resolve, reject) => {
        const id = ++seq;
        const timer = setTimeout(() => {
          pending.delete(id);
          reject(new Error('CDP timeout ' + method));
        }, 15000);
        pending.set(id, {
          resolve: (value) => {
            clearTimeout(timer);
            resolve(value);
          },
          reject: (error) => {
            clearTimeout(timer);
            reject(error);
          },
        });
        ws.send(JSON.stringify({ id, method, params }));
      });

    ws.addEventListener('message', async (event) => {
      const msg = JSON.parse(event.data);
      if (msg.id) {
        const p = pending.get(msg.id);
        if (p) {
          pending.delete(msg.id);
          msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result);
        }
        return;
      }
      if (msg.method === 'Runtime.exceptionThrown') {
        errors.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
        return;
      }
      if (msg.method === 'Fetch.requestPaused') {
        const { requestId, request } = msg.params;
        let response;
        try {
          const url = new URL(request.url);
          if (url.pathname.startsWith('/static/')) {
            const file = webFile(url.pathname);
            response = file
              ? { code: 200, type: CONTENT_TYPES[path.extname(file)] || 'application/octet-stream', body: fs.readFileSync(file) }
              : { code: 404, type: 'text/plain; charset=utf-8', body: Buffer.from('not found') };
          } else {
            const value = await api(url, request);
            if (value === undefined) {
              response = {
                code: 200,
                type: 'text/html; charset=utf-8',
                body: fs.readFileSync(path.join(webRoot, 'index.html')),
              };
            } else if (value && typeof value.code === 'number' && Object.prototype.hasOwnProperty.call(value, 'value')) {
              response = {
                code: value.code,
                type: 'application/json; charset=utf-8',
                body: Buffer.from(JSON.stringify(value.value)),
              };
            } else {
              response = {
                code: 200,
                type: 'application/json; charset=utf-8',
                body: Buffer.from(JSON.stringify(value)),
              };
            }
          }
        } catch (e) {
          errors.push('mock failed for ' + request.url + ': ' + e.message);
          response = { code: 500, type: 'text/plain; charset=utf-8', body: Buffer.from('mock failed') };
        }
        try {
          await call('Fetch.fulfillRequest', {
            requestId,
            responseCode: response.code,
            responseHeaders: [{ name: 'Content-Type', value: response.type }],
            body: response.body.toString('base64'),
          });
        } catch (e) {
          errors.push(e.message);
        }
      }
    });

    await call('Page.enable');
    await call('Runtime.enable');
    await call('Fetch.enable', { patterns: [{ urlPattern: '*' }] });
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });

    const evaluate = async (expression) => {
      const result = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
      if (result.exceptionDetails) {
        throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text);
      }
      return result.result.value;
    };
    const bodyText = () => evaluate('document.body.innerText');
    const wait = async (expression, tries = 140) => {
      for (let n = 0; n < tries; n++) {
        if (await evaluate(expression)) return true;
        await pause(75);
      }
      throw new Error(
        'Timed out: ' + expression + '\n' + (await bodyText()).slice(0, 700) + '\n' + JSON.stringify(errors.slice(-4)),
      );
    };
    const waitMaybe = async (expression, ms = 4000) => {
      const deadline = Date.now() + ms;
      while (Date.now() < deadline) {
        if (await evaluate(expression)) return true;
        await pause(80);
      }
      return false;
    };
    const waitText = (value) => wait('document.body.innerText.includes(' + JSON.stringify(value) + ')');
    const goto = async (url, marker) => {
      await call('Page.navigate', { url });
      const head = url.replace(/#.*$/, '');
      await wait('location.href.startsWith(' + JSON.stringify(head) + ') && (' + marker + ')');
    };
    const clickText = async (label, root = 'button') => {
      const ok = await evaluate(
        '(() => { const b = [...document.querySelectorAll(' + JSON.stringify(root) + ')].find((b) => b.textContent.trim() === ' +
          JSON.stringify(label) +
          ' && b.getClientRects().length); if (!b) return false; b.click(); return true })()',
      );
      if (!ok) throw new Error('Missing button: ' + label);
    };
    const clickTextIn = async (rootSelector, label) => {
      const ok = await evaluate(
        '(() => { const root = document.querySelector(' + JSON.stringify(rootSelector) + '); if (!root) return false;' +
          ' const b = [...root.querySelectorAll("button")].find((b) => b.textContent.trim() === ' + JSON.stringify(label) + '); if (!b) return false; b.click(); return true })()',
      );
      if (!ok) throw new Error('Missing button ' + label + ' in ' + rootSelector);
    };
    const setValue = async (selector, value) => {
      await evaluate(
        '(() => { const e = document.querySelector(' + JSON.stringify(selector) + '); if (!e) throw Error("missing " + ' +
          JSON.stringify(selector) +
          '); const proto = e.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;' +
          ' Object.getOwnPropertyDescriptor(proto, "value").set.call(e, ' + JSON.stringify(value) + ');' +
          ' e.dispatchEvent(new Event("input", { bubbles: true })); })()',
      );
    };
    const setSelect = async (selector, value) => {
      await evaluate(
        '(() => { const e = document.querySelector(' + JSON.stringify(selector) + '); if (!e) throw Error("missing " + ' +
          JSON.stringify(selector) +
          '); Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value").set.call(e, ' +
          JSON.stringify(value) +
          '); e.dispatchEvent(new Event("change", { bubbles: true })); })()',
      );
    };
    const readValue = (selector) =>
      evaluate('(() => { const e = document.querySelector(' + JSON.stringify(selector) + '); return e ? e.value : null })()');
    const shot = async (name, width, height) => {
      const data = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
      const buffer = Buffer.from(data.data, 'base64');
      fs.writeFileSync(path.join(out, name), buffer);
      const record = { path: '.build/ui-qa/' + name, bytes: buffer.length, size: pngSize(buffer), expected: { width, height } };
      shots.push(record);
      return record;
    };
    const clearHover = async () => {
      // The rail is not the canvas: moving there fires pointerleave and closes
      // the hover inspector without changing the camera or selection.
      await call('Input.dispatchMouseEvent', { type: 'mouseMoved', x: 100, y: 300, button: 'none', buttons: 0 });
      await pause(150);
    };

    // ---- 1. 焦点 view, opened from 状态 → 焦点 (docs/34 merged IA) --------
    await goto(pageURL + '?view=graph&case=focus', '!!document.querySelector("nav[aria-label=\\"项目导航\\"]")');
    await waitText('快照已载');
    await wait('[...document.querySelectorAll("button")].some(b => b.textContent.trim() === "追加目标并继续" && b.getClientRects().length)');
    await clickText('追加目标并继续');
    await wait('!!document.querySelector(\'[data-testid="append-goal-dialog"]\')');
    await setValue('[data-testid="append-goal-dialog"] textarea', '隔离续发目标');
    await clickTextIn('[data-testid="append-goal-dialog"]', '追加目标并继续');
    await waitText('目标已创建，但项目尚未恢复');
    const friendlyRetry = await evaluate('!document.querySelector(\'[data-testid="append-goal-dialog"]\').innerText.includes("idempotency_key")');
    await shot('append-goal-retry-desktop.png', 1440, 1000);
    await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
    await wait('window.innerWidth === 390');
    await evaluate('new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))');
    await shot('append-goal-retry-mobile.png', 390, 844);
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
    await evaluate('window.__appendReloadPending = true');
    await call('Page.reload');
    await wait('window.__appendReloadPending !== true && !!document.querySelector("nav[aria-label=\\"项目导航\\"]")');
    await wait('[...document.querySelectorAll("button")].some(b => b.textContent.trim() === "追加目标并继续" && b.getClientRects().length)');
    await clickText('追加目标并继续');
    await wait('document.querySelector(\'[data-testid="append-goal-dialog"] textarea\')?.value === "隔离续发目标"');
    await clickTextIn('[data-testid="append-goal-dialog"]', '重试继续');
    await wait('!!document.querySelector(\'[data-testid="append-goal-success"]\')');
    check('追加目标部分成功后刷新仍恢复同一提交，默认反馈不暴露协议术语', friendlyRetry && appendGoalWrites.length === 2 && appendGoalWrites[0].idempotency_key === appendGoalWrites[1].idempotency_key, '一次目标、同一操作身份、可安全继续', { friendlyRetry, writes: appendGoalWrites.length });
    await clickTextIn('[data-testid="append-goal-dialog"]', '完成');
    await evaluate(
      '(() => { const nav = document.querySelector("nav[aria-label=\\"项目导航\\"]"); if (!nav) throw Error("missing project nav");' +
        ' const b = [...nav.querySelectorAll("button")].find((b) => b.textContent.trim() === "状态"); if (!b) throw Error("missing 状态 rail item"); b.click(); })()',
    );
    await wait('!!document.querySelector(\'[data-testid="view-tabs"]\')');
    const stateTabs = await evaluate(
      '[...document.querySelectorAll(\'[data-testid="view-tabs"] [data-testid^="tab-"]\')].map((b) => ({ id: b.getAttribute("data-testid"), selected: b.getAttribute("aria-selected") }))',
    );
    check(
      '旧「焦点」页并入 状态，标签栏为 执行者/焦点',
      stateTabs.length === 2 &&
        stateTabs.some((t) => t.id === 'tab-state') &&
        stateTabs.some((t) => t.id === 'tab-focus'),
      'tab-state + tab-focus',
      stateTabs,
    );
    await evaluate('document.querySelector(\'[data-testid="tab-focus"]\').click()');
    await wait('!!document.querySelector(\'[data-testid="focus-panel"]\')');
    await wait('document.querySelectorAll(\'select[aria-label="焦点方向"] option\').length > 1');
    await setSelect('select[aria-label="焦点方向"]', 'intent_in_1');
    await waitText('热 2');

    const focusCounts = await evaluate('document.querySelector(\'[data-testid="focus-counts"]\').innerText');
    const focusRowsPage1 = await evaluate(
      '[...document.querySelectorAll(\'[data-testid="focus-fact"]\')].map((el) => ({ temperature: el.getAttribute("data-temperature"), status: el.getAttribute("data-status"), text: el.innerText }))',
    );
    check(
      '焦点视图从侧栏打开后显示冻结快照与热/温/冷计数',
      focusCounts.includes('热 2') && focusCounts.includes('温 1') && focusCounts.includes('冷 3') && focusCounts.includes(snapshotLabel) && focusCounts.includes('本页 焦点'),
      '含 热 2 / 温 1 / 冷 3 / ' + snapshotLabel + ' / 本页 焦点',
      focusCounts,
    );
    const hotRow = focusRowsPage1.find((row) => row.text.includes('f_hot_1'));
    const pinnedRow = focusRowsPage1.find((row) => row.text.includes('f_hot_2'));
    check(
      '热行显示记录的原因、断言版本与记录版本',
      Boolean(hotRow) &&
        hotRow.text.includes('必要前提') &&
        hotRow.text.includes('断言版本 v2') &&
        hotRow.text.includes('记录版本 v2') &&
        Boolean(pinnedRow) &&
        pinnedRow.text.includes('固定') &&
        pinnedRow.text.includes('记录版本未知'),
      'f_hot_1 含 必要前提 / 断言版本 v2 / 记录版本 v2；f_hot_2 含 固定 / 记录版本未知',
      focusRowsPage1.map((row) => row.text.split('\n').slice(0, 6)),
    );
    const queryHref = await evaluate(
      '(() => { const row = [...document.querySelectorAll(\'[data-testid="focus-fact"]\')].find((r) => r.innerText.includes("f_hot_1"));' +
        ' const a = row && row.querySelector("a[href]"); return a ? a.getAttribute("href") : null })()',
    );
    check(
      '焦点行提供按 id 查询的读路径',
      queryHref === '/p/fixture/api/facts/f_hot_1',
      '/p/fixture/api/facts/f_hot_1',
      queryHref,
    );

    // Pagination over one frozen snapshot: "继续加载" appends page 2.
    check('焦点默认页显示 已载入 2 / 3', (await bodyText()).includes('已载入 2 / 3'), '含 已载入 2 / 3', (await bodyText()).slice(0, 200));
    await clickText('继续加载');
    await waitText('已载入 3 / 3');
    const focusRowsPage2 = await evaluate(
      '[...document.querySelectorAll(\'[data-testid="focus-fact"]\')].map((el) => el.innerText)',
    );
    const lastFocusRequest = state.focusRequests[state.focusRequests.length - 1];
    check(
      '继续加载用冻结响应的游标翻页并合并（热+温 3 条）',
      focusRowsPage2.length === 3 &&
        focusRowsPage2.some((text) => text.includes('f_warm_1')) &&
        focusRowsPage2.some((text) => text.includes('f_hot_1')) &&
        lastFocusRequest.cursor === 'cursor-focus-2' &&
        lastFocusRequest.layer === 'focus' &&
        lastFocusRequest.limit === '50',
      '3 条（含 f_hot_1 与 f_warm_1）且翻页请求 cursor=cursor-focus-2 / layer=focus / limit=50',
      { rows: focusRowsPage2.length, request: lastFocusRequest },
    );

    // Layer switch: cold is a different list, hot titles disappear.
    await clickTextIn('[data-testid="focus-layer-switch"]', '冷');
    await waitText('本页 冷');
    await waitText('冷行：旁站资产笔记');
    const coldRows = await evaluate(
      '[...document.querySelectorAll(\'[data-testid="focus-fact"]\')].map((el) => ({ temperature: el.getAttribute("data-temperature"), text: el.innerText }))',
    );
    const coldRequest = state.focusRequests[state.focusRequests.length - 1];
    const focusPanelText = await evaluate('document.querySelector(\'[data-testid="focus-panel"]\').innerText');
    check(
      '切到冷层后列表换成冷层事实且热行消失',
      coldRows.length === 3 &&
        coldRows.every((row) => row.temperature === 'cold') &&
        coldRows.every((row) => row.text.includes('冷')) &&
        !focusPanelText.includes('热行：必要前提事实') &&
        coldRequest.layer === 'cold',
      '3 条 data-temperature=cold、热行标题不再出现、请求 layer=cold',
      { rows: coldRows.map((row) => row.temperature), request: coldRequest, hasHotTitle: focusPanelText.includes('热行：必要前提事实') },
    );

    // Back to the bounded index for the desktop shot and the deep link.
    await clickTextIn('[data-testid="focus-layer-switch"]', '焦点');
    await waitText('已载入 2 / 3');
    await waitText('热行：必要前提事实');
    await shot('focus-desktop.png', 1440, 1000);

    // 390px: the focus page must not scroll horizontally.
    await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
    await pause(400);
    const focusNarrow = await evaluate(
      '(() => { const panel = document.querySelector(\'[data-testid="focus-panel"]\'); return { innerWidth, docScroll: document.documentElement.scrollWidth, panelScroll: panel ? panel.scrollWidth : 0, panelClient: panel ? panel.clientWidth : 0 } })()',
    );
    check(
      '390px 焦点视图无横向溢出',
      focusNarrow.docScroll <= focusNarrow.innerWidth + 1 && focusNarrow.panelScroll <= focusNarrow.panelClient + 1,
      'documentElement.scrollWidth <= innerWidth+1 且面板 scrollWidth <= clientWidth+1',
      focusNarrow,
    );
    await shot('focus-mobile.png', 390, 844);
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
    await pause(400);
    // A viewport change saves the camera; drop it so the next full page opens
    // centred like a fresh console (the screenshots should show the whole map).
    await evaluate('localStorage.removeItem("tw.graph.camera:fixture")');

    // Deep link: "在图谱中打开" must land on the graph with that fact selected.
    await evaluate(
      '(() => { const row = [...document.querySelectorAll(\'[data-testid="focus-fact"]\')].find((r) => r.innerText.includes("f_hot_1"));' +
        ' if (!row) throw Error("missing focus row f_hot_1"); const b = [...row.querySelectorAll("button")].find((b) => b.textContent.trim() === "在图谱中打开");' +
        ' if (!b) throw Error("missing 在图谱中打开 button"); b.click(); })()',
    );
    const deepLinkView = await waitMaybe(
      'new URLSearchParams(location.search).get("view") === "graph"',
      4000,
    );
    const deepLinkState = await evaluate(
      '({ search: location.search, hash: decodeURIComponent(location.hash), title: document.title })',
    );
    const deepLinkFactTitle = '热行：必要前提事实'; // the title of f_hot_1 in the mocked ledger
    const panelOpened = await waitMaybe(
      '(() => { const panel = document.querySelector(\'[data-fact-claim-detail]\');' +
        ' return !!panel && panel.innerText.includes("断言 v2") && document.body.innerText.includes(' +
        JSON.stringify(deepLinkFactTitle) +
        ') })()',
      4000,
    );
    const deepLinkPanel = await evaluate(
      '(() => { const panel = document.querySelector(\'[data-fact-claim-detail]\'); return panel ? panel.innerText.slice(0, 160) : null })()',
    );
    check(
      '点击焦点事实切到图谱并携带该事实的深链',
      // The hash carries the graph node id (`f:<factID>`), the same identity the
      // canvas model and NodePanel use; the ledger's `fact:` prefix is API-only
      // and would select nothing in the graph.
      deepLinkView && deepLinkState.search.includes('view=graph') && deepLinkState.hash === '#f:f_hot_1',
      'view=graph 且 hash 为 #f:f_hot_1',
      deepLinkState,
    );
    check(
      '焦点事实深链在图谱中选中该事实节点（NodePanel 显示断言与复核）',
      panelOpened,
      '图谱 NodePanel 显示「' + deepLinkFactTitle + '」与断言与复核（断言 v2）',
      { panelOpened, panel: deepLinkPanel, url: deepLinkState },
    );
    await shot('focus-deeplink.png', 1440, 1000);

    // ---- 2. graph relation distinction: legend + inspector ----------------
    // The main canvas carries a test id: the minimap is a second canvas and the
    // first canvas in the DOM is no longer a safe way to find the graph.
    await goto(pageURL + '?view=graph&case=relations', '!!document.querySelector(\'[data-testid="graph-canvas"]\')');
    await waitText('快照已载');
    await pause(700);

    // Project token totals on the graph page (docs/34 §3): the single place the
    // console shows tokens now, with cached/input/output in the same line.
    const tokenChip = await evaluate(
      '(() => { const el = document.querySelector(\'[data-testid="project-tokens"]\'); return el ? { text: el.innerText, title: el.getAttribute("title") || "" } : null })()',
    );
    check(
      '图谱页显示项目总 token（总数/输入/输出/缓存）',
      Boolean(tokenChip) &&
        tokenChip.text.includes('123,456') &&
        tokenChip.text.includes('入 80,000') &&
        tokenChip.text.includes('出 43,456') &&
        tokenChip.text.includes('缓存 20,000') &&
        tokenChip.title.includes('缓存输入是输入 token 的子集'),
      'tokens 123,456 · 入 80,000 / 出 43,456 / 缓存 20,000，title 说明缓存口径',
      tokenChip,
    );

    // Locate an edge by the colour render.ts paints it, move the pointer onto
    // that pixel and assert the DOM inspector names the relation. Premise:
    // rgba(106,169,255,.9); reference: rgba(45,212,191,.6). No fixture node uses
    // those two colours (no host/service facts), so the hit is the edge itself.
    const scanCanvas = (kind) =>
      evaluate(`(() => {
        const canvas = document.querySelector('[data-testid="graph-canvas"]');
        if (!canvas) return { points: [], error: 'no canvas' };
        const ctx = canvas.getContext('2d');
        const w = canvas.width, h = canvas.height;
        let data;
        try { data = ctx.getImageData(0, 0, w, h).data; } catch (e) { return { points: [], error: String(e) }; }
        const rect = canvas.getBoundingClientRect();
        const dpr = w / Math.max(rect.width, 1);
        const buckets = new Map();
        for (let y = 0; y < h; y += 2) {
          for (let x = 0; x < w; x += 2) {
            const i = (y * w + x) * 4;
            if (data[i + 3] < 50) continue;
            const r = data[i], g = data[i + 1], b = data[i + 2];
            const hit = ${kind === 'premise'
              ? 'Math.abs(r - 106) <= 14 && Math.abs(g - 169) <= 14 && b >= 238'
              : 'Math.abs(r - 45) <= 20 && Math.abs(g - 212) <= 18 && Math.abs(b - 191) <= 20'};
            if (!hit) continue;
            const key = Math.floor(x / 32) + ',' + Math.floor(y / 32);
            const cur = buckets.get(key) || { n: 0, x: 0, y: 0 };
            cur.n++; cur.x += x; cur.y += y;
            buckets.set(key, cur);
          }
        }
        const points = [...buckets.values()]
          .filter((bucket) => bucket.n >= 3)
          .sort((a, b) => b.n - a.n)
          .map((bucket) => ({ n: bucket.n, x: Math.round(bucket.x / bucket.n), y: Math.round(bucket.y / bucket.n) }))
          .map((point) => ({ n: point.n, cx: rect.left + point.x / dpr, cy: rect.top + point.y / dpr }))
          .filter((point) => document.elementFromPoint(point.cx, point.cy) === canvas)
          .slice(0, 20);
        return { points, buckets: buckets.size };
      })()`);

    // The inspector root is the smallest div that carries the footer *and* the
    // relation badge; its ancestors include the footer text too, so pick the
    // longest candidate below page size, not the shortest (the footer itself).
    const readInspector = () =>
      evaluate(
        '(() => { const divs = [...document.querySelectorAll("div")].filter((d) => d.innerText.includes("单击节点固定详情") && d.innerText.length < 1500);' +
          ' if (!divs.length) return null; divs.sort((a, b) => b.innerText.length - a.innerText.length); return divs[0].innerText })()',
      );
    const inspectorLabel = (label) =>
      `(() => [...document.querySelectorAll('div')].some((d) => d.innerText.includes('单击节点固定详情') && d.innerText.includes(${JSON.stringify(
        label,
      )})))()`;

    const hoverRelation = async (kind, label) => {
      const probes = [];
      for (let round = 0; round < 4; round++) {
        const scan = await scanCanvas(kind);
        probes.push({ round: round + 1, points: scan.points ? scan.points.length : -1, buckets: scan.buckets, error: scan.error });
        if (scan.points && scan.points.length > 0) {
          for (const point of scan.points) {
            await call('Input.dispatchMouseEvent', {
              type: 'mouseMoved',
              x: Math.round(point.cx),
              y: Math.round(point.cy),
              button: 'none',
              buttons: 0,
            });
            const found = await waitMaybe(inspectorLabel(label), 1200);
            if (found) return { found: true, point, attempt: round + 1, scanned: scan.points.length, probes };
            const hoverState = await evaluate(
              '(() => { const canvas = document.querySelector(\'[data-testid="graph-canvas"]\'); const divs = [...document.querySelectorAll("div")].filter((d) => d.innerText.includes("单击节点固定详情"));' +
                ' divs.sort((a, b) => a.innerText.length - b.innerText.length);' +
                ' return { cursor: canvas ? canvas.style.cursor : null, inspector: divs.length ? divs[0].innerText.slice(0, 120) : null } })()',
            );
            probes.push({ point, hoverState });
            if (probes.length > 40) return { found: false, probes };
          }
        }
        await pause(400);
      }
      return { found: false, probes };
    };

    const premiseHover = await hoverRelation('premise', '必要前提');
    const premiseInspector = await readInspector();
    check(
      '图谱检查器把必要前提边标注为「必要前提」',
      premiseHover.found && Boolean(premiseInspector && premiseInspector.includes('必要前提')),
      '悬停必要前提边时 DOM 检查器出现「必要前提」',
      { hover: premiseHover, inspector: premiseInspector },
    );
    await shot('graph-premise-inspector.png', 1440, 1000);

    await clearHover();
    const referenceHover = await hoverRelation('reference', '引用');
    const referenceInspector = await readInspector();
    check(
      '图谱检查器把引用边标注为「引用」而不是「必要前提」',
      referenceHover.found &&
        Boolean(referenceInspector && referenceInspector.includes('引用') && !referenceInspector.includes('必要前提')),
      '悬停引用边时 DOM 检查器出现「引用」且不出现「必要前提」',
      { hover: referenceHover, inspector: referenceInspector },
    );
    await shot('graph-reference-inspector.png', 1440, 1000);

    // Legend: the collapsed reference card must also name both relations and
    // their different meaning (a reference is a reading choice, not a gate).
    await clearHover();
    const legendClicked = await evaluate(
      '(() => { const b = [...document.querySelectorAll("button")].find((b) => b.textContent.includes("图例") && b.getClientRects().length); if (!b) return false; b.click(); return true })()',
    );
    if (!legendClicked) throw new Error('Missing 图例 button');
    await waitText('引用/固定是阅读选择，不是必须满足的前提');
    const legend = await evaluate(
      '(() => { const text = document.body.innerText; const tooltips = [...document.querySelectorAll("[title]")].map((el) => el.getAttribute("title")).filter(Boolean); return { text, tooltips } })()',
    );
    check(
      '图谱图例同时给出「必要前提」「引用」「固定」三个关系标签及区别说明',
      legend.text.includes('必要前提') &&
        legend.text.includes('引用（青色虚线）') &&
        legend.text.includes('固定（青色实线双线）') &&
        legend.text.includes('支持目标') &&
        legend.text.includes('引用/固定是阅读选择，不是必须满足的前提；缺前提挡派发，缺引用不挡。') &&
        legend.tooltips.some((tip) => tip.includes('from_fact_ids') && tip.includes('硬门槛')) &&
        legend.tooltips.some((tip) => tip.includes('context_fact_ids') && tip.includes('阅读选择')) &&
        legend.tooltips.some((tip) => tip.includes('pinned_fact_ids')),
      '图例含 必要前提 / 引用（青色虚线）/ 固定（青色实线双线）/ 支持目标 / 区别说明，且三个关系各带「硬门槛」「阅读选择」提示',
      {
        lines: legend.text.split('\n').filter((line) => line.includes('前提') || line.includes('引用') || line.includes('固定') || line.includes('支持目标')).slice(0, 12),
        relationTips: legend.tooltips.filter((tip) => tip.includes('from_fact_ids') || tip.includes('context_fact_ids') || tip.includes('pinned_fact_ids')).slice(0, 4),
      },
    );
    await shot('graph-legend.png', 1440, 1000);

    // ---- 3. intent context editor ----------------------------------------
    await goto(
      pageURL + '?view=graph&case=editor#s%3Aintent_in_1',
      'document.body.innerText.includes("上下文（引用/固定）")',
    );
    await waitText('引用 1 · 固定 1 · 实体 1');
    await waitText('暂不可执行：前提需要处理');
    const readinessText = await bodyText();
    check(
      '方向详情解释当前断言的核验阻塞，并区分图的历史快照',
      readinessText.includes('断言 v2 尚未通过独立核验') && readinessText.includes('图的历史快照与当前断言分别保留'),
      '待核验版本及当前/历史边界明确可见',
      readinessText.split('\n').filter(line => line.includes('核验') || line.includes('历史快照')).slice(0, 5),
    );
    const editorHeader = await evaluate('document.querySelector("[data-intent-context-editor]").innerText');
    check(
      '意图面板显示 context_version 与引用/固定/实体计数',
      editorHeader.includes('上下文 v1') && editorHeader.includes('引用 1') && editorHeader.includes('固定 1') && editorHeader.includes('实体 1'),
      '含 上下文 v1 / 引用 1 / 固定 1 / 实体 1',
      editorHeader.split('\n').slice(0, 3),
    );

    // Run evidence (frozen at dispatch).
    const runDetailsOpened = await evaluate(
      '(() => { const s = [...document.querySelectorAll("summary")].find((s) => s.textContent.includes("本次运行读到的版本")); if (!s) return false; s.click(); return true })()',
    );
    if (!runDetailsOpened) throw new Error('Missing 本次运行读到的版本 summary');
    await waitText('必要前提断言版本');
    const runEvidenceText = await evaluate(
      '(() => { const el = document.querySelector("[data-run-evidence]"); return el ? el.innerText : "" })()',
    );
    check(
      '运行证据视图显示派发时冻结的前提断言版本与上下文记录版本',
      runEvidenceText.includes('必要前提断言版本') &&
        runEvidenceText.includes('断言 v2') &&
        runEvidenceText.includes('上下文记录版本') &&
        runEvidenceText.includes('记录 r3') &&
        runEvidenceText.includes('硬前提：登录口可达') &&
        runEvidenceText.includes('参考：历史登录尝试'),
      '含 必要前提断言版本 / 断言 v2 / 上下文记录版本 / 记录 r3 与事实标题',
      runEvidenceText.split('\n').slice(0, 14),
    );

    await clickText('编辑');
    await waitText('保存上下文');
    // The fact picker loads asynchronously; a selected id's title resolves after
    // GET /api/facts, before that the editor renders "事实不在最近列表中".
    await waitText('固定：配置备份清单');
    await waitText('参考：历史登录尝试');
    await waitText('硬前提：登录口可达');
    const editorLoaded = await evaluate('document.querySelector("[data-intent-context-editor]").innerText');
    check(
      '上下文编辑器载入当前引用、固定、实体与硬前提（只读）',
      editorLoaded.includes('引用（1）') &&
        editorLoaded.includes('固定（1）') &&
        editorLoaded.includes('关注实体（1）') &&
        editorLoaded.includes('将以 v1 保存') &&
        editorLoaded.includes('参考：历史登录尝试') &&
        editorLoaded.includes('固定：配置备份清单') &&
        editorLoaded.includes('必要前提（from_fact_ids）') &&
        editorLoaded.includes('硬前提：登录口可达') &&
        editorLoaded.includes('硬门槛 · 只读'),
      '含 引用（1）/ 固定（1）/ 关注实体（1）/ 将以 v1 保存 / 两个事实标题 / 必要前提（from_fact_ids）',
      editorLoaded.split('\n').slice(0, 24),
    );
    await shot('context-editor-desktop.png', 1440, 1000);

    // Add one reference and one entity, then save the whole selection under the
    // version the editor displayed.
    await setValue('input[aria-label="搜索事实"]', 'f_out');
    await waitText('越权读取成立');
    const addReferenceClicked = await evaluate(
      '(() => { const ed = document.querySelector("[data-intent-context-editor]"); const b = [...ed.querySelectorAll("button")].find((b) => b.textContent.trim() === "+引用"); if (!b) return false; b.click(); return true })()',
    );
    if (!addReferenceClicked) throw new Error('Missing +引用 button');
    await waitText('引用（2）');
    await setValue('textarea[aria-label="关注实体"]', 'host:10.0.0.5\nhost:10.0.0.9');
    await waitText('未保存');
    await clickText('保存上下文');
    const savedShown = await waitMaybe('document.body.innerText.includes("上下文 v2")', 8000);
    const saveWrite = state.contextWrites[0];
    check(
      '保存上下文发送显示时的版本与完整选择（引用/固定/实体）',
      savedShown &&
        Boolean(saveWrite) &&
        saveWrite.version === 1 &&
        JSON.stringify(saveWrite.references) === JSON.stringify(['f_ref_a', 'f_out']) &&
        JSON.stringify(saveWrite.pins) === JSON.stringify(['f_pin_a']) &&
        JSON.stringify(saveWrite.entities) === JSON.stringify(['host:10.0.0.5', 'host:10.0.0.9']) &&
        (await bodyText()).includes('上下文 v2'),
      'PUT version=1 / references=[f_ref_a,f_out] / pins=[f_pin_a] / entities=[host:10.0.0.5,host:10.0.0.9]，之后显示 上下文 v2',
      { savedShown, write: saveWrite },
    );

    // Reopen the editor on the reloaded intent.
    const reopen = await waitMaybe(
      '(() => { const ed = document.querySelector("[data-intent-context-editor]"); return !!ed && !ed.innerText.includes("保存上下文") && [...ed.querySelectorAll("button")].some((b) => b.textContent.trim() === "编辑") })()',
      8000,
    );
    if (reopen) await clickText('编辑');
    await waitText('保存上下文');

    // 409: another editor committed; the UI must say refresh-and-retry and keep
    // the operator draft instead of silently overwriting.
    state.contextMode = 'conflict';
    await setValue('textarea[aria-label="关注实体"]', 'host:10.0.0.5\nhost:10.0.0.9\nhost:10.0.0.7');
    await clickText('保存上下文');
    const conflictShown = await waitText('已被他人修改，刷新后重试');
    const conflictEditorText = await evaluate('document.querySelector("[data-intent-context-editor]").innerText');
    const conflictDraft = await readValue('textarea[aria-label="关注实体"]');
    const conflictWrite = state.contextWrites[state.contextWrites.length - 1];
    check(
      '409 陈旧版本显示刷新后重试的冲突状态且不丢草稿',
      conflictShown &&
        conflictEditorText.includes('已被他人修改，刷新后重试') &&
        conflictEditorText.includes('冲突意味着这个版本已经不是最新的：载入最新版本后重试，草稿不会丢。') &&
        conflictEditorText.includes('载入最新版本（保留草稿）') &&
        conflictEditorText.includes('引用（2）') &&
        Boolean(conflictDraft && conflictDraft.includes('host:10.0.0.7') && conflictDraft.includes('host:10.0.0.9')),
      '冲突提示 + 冲突说明 + 载入最新版本（保留草稿）+ 草稿保留引用与实体',
      { shown: conflictShown, draft: conflictDraft, conflictWrite },
    );
    check(
      '冲突请求仍携带界面显示的版本与完整选择，未被静默覆盖',
      Boolean(conflictWrite) &&
        conflictWrite.version === 2 &&
        JSON.stringify(conflictWrite.references) === JSON.stringify(['f_ref_a', 'f_out']) &&
        JSON.stringify(conflictWrite.pins) === JSON.stringify(['f_pin_a']) &&
        JSON.stringify(conflictWrite.entities) === JSON.stringify(['host:10.0.0.5', 'host:10.0.0.9', 'host:10.0.0.7']),
      'PUT version=2 且 references/pins/entities 为完整选择',
      conflictWrite,
    );
    await shot('context-editor-conflict.png', 1440, 1000);

    // "载入最新版本（保留草稿）" is the advertised recovery from 409: it must
    // rebase the save version without discarding the draft.
    await clickText('载入最新版本（保留草稿）');
    const latestLoaded = await waitMaybe(
      '(() => { const ed = document.querySelector("[data-intent-context-editor]"); if (!ed) return false;' +
        ' const area = ed.querySelector("textarea[aria-label=\\"关注实体\\"]");' +
        ' return !!area && area.value.includes("host:10.0.0.7") && ed.innerText.includes("将以 v3 保存") })()',
      5000,
    );
    const latestState = await evaluate(
      '(() => { const ed = document.querySelector("[data-intent-context-editor]"); const area = ed ? ed.querySelector("textarea[aria-label=\\"关注实体\\"]") : null;' +
        ' return { editorOpen: !!ed, open: ed ? ed.innerText.includes("保存上下文") : false, draft: area ? area.value : null, text: ed ? ed.innerText.slice(0, 240) : null } })()',
    );
    check(
      '载入最新版本（保留草稿）把版本基准挪到 v3 且保留原草稿',
      latestLoaded && latestState.open && Boolean(latestState.draft && latestState.draft.includes('host:10.0.0.7')),
      '编辑器仍打开、草稿含 host:10.0.0.7、显示 将以 v3 保存',
      latestState,
    );
    if (latestLoaded) {
      state.contextMode = 'ok';
      await clickText('保存上下文');
      await waitMaybe('document.body.innerText.includes("上下文 v4")', 8000);
      const rebasedWrite = state.contextWrites[state.contextWrites.length - 1];
      check(
        '冲突恢复后以 v3 保存成功',
        Boolean(rebasedWrite) && rebasedWrite.version === 3 && (await bodyText()).includes('上下文 v4'),
        'PUT version=3 并显示 上下文 v4',
        rebasedWrite,
      );
    }

    // 403: a participant editing a foreign direction must read as a boundary
    // denial, not a concurrent edit.
    state.contextMode = 'forbidden';
    const editorOpenNow = await evaluate('!!document.querySelector("textarea[aria-label=\\"关注实体\\"]")');
    if (!editorOpenNow) {
      if (await waitMaybe('(() => [...document.querySelectorAll("button")].some((b) => b.textContent.trim() === "编辑"))()', 4000)) {
        await clickText('编辑');
        await waitText('保存上下文');
      }
    }
    const baseVersionMatch = /将以 v(\d+) 保存/.exec(await bodyText());
    const baseVersion = baseVersionMatch ? Number(baseVersionMatch[1]) : -1;
    await setValue('textarea[aria-label="关注实体"]', 'host:10.0.0.5\nhost:10.0.0.9\nhost:10.0.0.10');
    await clickText('保存上下文');
    const forbiddenShown = await waitText('只能更新自己的方向（边界拒绝）');
    const forbiddenDraft = await readValue('textarea[aria-label="关注实体"]');
    const forbiddenWrite = state.contextWrites[state.contextWrites.length - 1];
    check(
      '403 显示边界拒绝并且不丢草稿',
      forbiddenShown &&
        Boolean(forbiddenDraft && forbiddenDraft.includes('host:10.0.0.10')) &&
        Boolean(forbiddenWrite) &&
        forbiddenWrite.version === baseVersion,
      '显示 只能更新自己的方向（边界拒绝）、草稿保留、PUT 携带显示版本 v' + baseVersion,
      { shown: forbiddenShown, draft: forbiddenDraft, expectedVersion: baseVersion, write: forbiddenWrite },
    );

    // 390px: the editor must not force horizontal scrolling.
    await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
    await pause(500);
    const editorNarrow = await evaluate(
      '(() => { const ed = document.querySelector("[data-intent-context-editor]"); return { innerWidth, docScroll: document.documentElement.scrollWidth, editorScroll: ed ? ed.scrollWidth : 0, editorClient: ed ? ed.clientWidth : 0 } })()',
    );
    check(
      '390px 上下文编辑器无横向溢出',
      editorNarrow.docScroll <= editorNarrow.innerWidth + 1 && editorNarrow.editorScroll <= editorNarrow.editorClient + 1,
      'documentElement.scrollWidth <= innerWidth+1 且编辑器 scrollWidth <= clientWidth+1',
      editorNarrow,
    );
    await shot('context-editor-mobile.png', 390, 844);
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
    await pause(300);
    await evaluate('localStorage.removeItem("tw.graph.camera:fixture")');

    // ---- 4. fact claim detail: assertion version vs review binding -------
    await goto(pageURL + '?view=graph&case=claim-stale#f%3Af_stale', '!!document.querySelector("[data-fact-claim-detail]")');
    await waitText('断言与复核');
    const staleText = await evaluate('document.querySelector("[data-fact-claim-detail]").innerText');
    check(
      'claim_version 2 但复核证明 v1 时显示「复核的是旧断言」而不是已复核',
      staleText.includes('断言 v2') &&
        staleText.includes('复核的是旧断言') &&
        !staleText.includes('当前断言已复核通过') &&
        staleText.includes('旧断言') &&
        staleText.includes('证明 v1') &&
        staleText.includes('最近通过 v1') &&
        staleText.includes('复核通过的是 v1 的断言；当前断言已是 v2 且未复核，旧结论不能证明新内容。'),
      '含 断言 v2 / 复核的是旧断言 / 旧断言 / 证明 v1 / 最近通过 v1 / 旧结论说明，且不出现 当前断言已复核通过',
      staleText.split('\n').slice(0, 16),
    );
    await shot('claim-stale.png', 1440, 1000);

    await goto(pageURL + '?view=graph&case=claim-verified#f%3Af_verified', '!!document.querySelector("[data-fact-claim-detail]")');
    await waitText('断言与复核');
    const verifiedText = await evaluate('document.querySelector("[data-fact-claim-detail]").innerText');
    check(
      '当前断言的复核通过时显示已复核且不再标记旧断言',
      verifiedText.includes('断言 v1') &&
        verifiedText.includes('当前断言已复核通过') &&
        verifiedText.includes('证明 v1') &&
        !verifiedText.includes('复核的是旧断言') &&
        !verifiedText.includes('旧断言'),
      '含 断言 v1 / 当前断言已复核通过 / 证明 v1，且不出现 旧断言',
      verifiedText.split('\n').slice(0, 14),
    );
    await shot('claim-verified.png', 1440, 1000);

    // ---- 5. large-graph smoke (docs/33 P0 acceptance) ---------------------
    // A ~1100-row synthetic project opened through the real renderer: the first
    // frame must paint, the force warm must not block a long task, and the
    // accessible/aux surfaces (legend, minimap, keyboard, list view) must work.
    state.graphCase = 'large';
    await evaluate(
      '(() => { localStorage.setItem("tw.graph.layout", "force"); localStorage.removeItem("tw.graph.camera:fixture"); })()',
    );
    await call('Page.addScriptToEvaluateOnNewDocument', {
      source:
        'window.__longTasks = [];' +
        'try { new PerformanceObserver((list) => { for (const e of list.getEntries()) window.__longTasks.push(e.duration); }).observe({ type: "longtask", buffered: true }); } catch (e) {}',
    });
    const largeStart = Date.now();
    await goto(
      pageURL + '?view=graph&case=large',
      '!!document.querySelector(\'[data-testid="graph-canvas"]\')',
    );
    await waitText('快照已载');
    const largeLoaded = await waitMaybe(
      '(() => { const m = document.body.innerText.match(/快照已载 (\\d+)\\/(\\d+) 节点/); return !!m && m[1] === m[2] && Number(m[1]) === 1100 })()',
      15000,
    );
    const largeOpenMs = Date.now() - largeStart;
    await pause(1500);
    const largeState = await evaluate(
      '(() => { const canvas = document.querySelector(\'[data-testid="graph-canvas"]\');' +
        ' const ctx = canvas ? canvas.getContext("2d") : null; let painted = 0;' +
        ' if (ctx) { const data = ctx.getImageData(0, 0, canvas.width, canvas.height).data;' +
        '   for (let i = 3; i < data.length; i += 40) if (data[i] > 0) painted++; }' +
        ' return { hasCanvas: !!canvas, paintedSamples: painted, hasLegend: !!document.querySelector(\'[data-testid="graph-legend"]\'),' +
        '   hasMinimap: !!document.querySelector(\'[data-testid="graph-minimap"]\'),' +
        '   longTasks: (window.__longTasks || []).slice(), loaded: document.body.innerText.match(/快照已载 [0-9]+\\/[0-9]+ 节点/) ? document.body.innerText.match(/快照已载 [0-9]+\\/[0-9]+ 节点/)[0] : null } })()',
    );
    check(
      '1100 节点大图打开后画布有内容、图例与缩略图就位',
      Boolean(largeState.hasCanvas) && largeState.paintedSamples > 1000 && largeState.hasLegend && largeState.hasMinimap,
      '画布非空（>1000 采样点）、图例与 minimap 存在',
      { ...largeState, longTasks: undefined },
    );
    check(
      '大图首屏在 15s 内完整载入且节点计数诚实',
      Boolean(largeLoaded) && largeOpenMs < 15000,
      '快照已载 1100/1100 节点，且 < 15000ms',
      { largeLoaded, largeOpenMs, loaded: largeState.loaded },
    );
    // Force-mode opening settles through the rAF budget; no synchronous warm of
    // hundreds of ticks may appear as a main-thread long task.
    const maxLongTask = Math.max(0, ...largeState.longTasks);
    check(
      '大图打开/预热没有 >50ms 的主线程 long task',
      maxLongTask < 50,
      'max long task < 50ms',
      { maxLongTask, longTasks: largeState.longTasks },
    );
    // Keyboard: focus the canvas, move the deterministic selection, open it.
    await evaluate('document.querySelector(\'[data-testid="graph-canvas"]\').focus()');
    const key = async (name, code, vk) => {
      await call('Input.dispatchKeyEvent', {
        type: 'keyDown',
        key: name,
        code,
        windowsVirtualKeyCode: vk,
        nativeVirtualKeyCode: vk,
      });
      await call('Input.dispatchKeyEvent', {
        type: 'keyUp',
        key: name,
        code,
        windowsVirtualKeyCode: vk,
        nativeVirtualKeyCode: vk,
      });
    };
    await key('ArrowRight', 'ArrowRight', 39);
    await key('Enter', 'Enter', 13);
    const keyboardSelected = await waitMaybe('location.hash.length > 1', 4000);
    check(
      '画布可键盘选择节点（方向键 + Enter 打开详情深链）',
      keyboardSelected,
      'location.hash 非空',
      await evaluate('({ hash: decodeURIComponent(location.hash), active: document.activeElement && document.activeElement.tagName })'),
    );
    // List view opens from the toolbar and Esc returns to the canvas.
    await evaluate('(() => { const b = document.querySelector(\'button[aria-label="列表视图"]\'); if (!b) throw Error("missing 列表视图"); b.click(); })()');
    await wait('!!document.querySelector(\'[data-testid="graph-list-view"]\')');
    await key('Escape', 'Escape', 27);
    const listClosed = await waitMaybe('!document.querySelector(\'[data-testid="graph-list-view"]\')', 3000);
    check('列表视图可打开并用 Esc 关闭', listClosed, 'graph-list-view 打开后 Esc 关闭', listClosed);
    await shot('graph-large.png', 1440, 1000);
    state.graphCase = 'fixture';
    await evaluate('localStorage.setItem("tw.graph.layout", "layered")');

    // ---- artifacts / runtime checks --------------------------------------
    check('无浏览器运行时异常', errors.length === 0, [], errors);
    const expectedSizes = shots.map((shotRecord) => shotRecord.expected);
    check(
      '截图尺寸与预期视口一致',
      shots.every((shotRecord) => shotRecord.size && shotRecord.size.width === shotRecord.expected.width && shotRecord.size.height === shotRecord.expected.height),
      JSON.stringify(expectedSizes),
      shots.map((shotRecord) => ({ path: shotRecord.path, size: shotRecord.size })),
    );
    check(
      '截图为非空白 PNG（> 15KB）',
      shots.every((shotRecord) => shotRecord.bytes > 15000),
      '每张 > 15360 字节',
      shots.map((shotRecord) => ({ path: shotRecord.path, bytes: shotRecord.bytes })),
    );

    const result = {
      pageURL,
      passed: checks.every((row) => row.passed),
      checks,
      screenshots: shots,
      contextWrites: state.contextWrites,
      focusRequests: state.focusRequests,
      graphRequests: state.graphRequests,
      runtimeErrors: errors,
      unverified: [
        '画布边的描边颜色/虚线样式（必要前提=蓝色实线、引用=青色虚线）只用于让脚本在画布上定位边；断言对象是随后出现的 DOM 检查器关系文案，像素颜色本身不构成断言。',
        '409/403 由 mock 服务端合成（离线脚本不连真实控制面），验证的是 UI 对状态的呈现与请求体，不是服务端 CAS 实现；服务端 CAS 由 Go 单测覆盖。',
        '焦点面板「按 id 查询」只是 API 读路径链接；脚本断言 href 正确，未在离线环境打开该链接对应的真实事实页面。',
        '真实项目数据、真实登录态与真实并发编辑不在本脚本范围（全程 mock）。',
      ],
    };

    const lines = [
      '页面: ' + pageURL,
      ...checks.map((row) => (row.passed ? 'PASS ' : 'FAIL ') + row.name),
      '',
      '截图:',
      ...shots.map((shotRecord) => '  ' + shotRecord.path + ' ' + (shotRecord.size ? shotRecord.size.width + 'x' + shotRecord.size.height : '?') + ' ' + shotRecord.bytes + 'B'),
    ];
    process.stdout.write(lines.join('\n') + '\n\n');
    process.stdout.write(JSON.stringify(result, null, 2) + '\n');
    finalResult = result;
  } catch (error) {
    const result = {
      pageURL,
      passed: false,
      error: error.stack || String(error),
      checks,
      screenshots: shots,
      contextWrites: state.contextWrites,
      focusRequests: state.focusRequests,
      runtimeErrors: errors,
    };
    process.stdout.write(JSON.stringify(result, null, 2) + '\n');
    process.stderr.write((error.stack || String(error)) + '\n');
    finalResult = result;
  } finally {
    if (ws) ws.close();
    stopChrome(chrome);
  }
  return finalResult;
})().then((result) => {
  assert.ok(
    result && result.passed,
    '焦点/上下文/断言/关系界面验收未通过: ' +
      JSON.stringify(result ? result.checks.filter((row) => !row.passed).map((row) => row.name) : '无结果'),
  );
}).catch((error) => {
  process.stderr.write((error.stack || String(error)) + '\n');
  process.exitCode = 1;
});

/** Width/height straight from the PNG IHDR, so a blank/wrong shot is caught. */
function pngSize(buffer) {
  if (buffer.length < 24 || buffer.toString('ascii', 1, 4) !== 'PNG') return null;
  return { width: buffer.readUInt32BE(16), height: buffer.readUInt32BE(20) };
}
