// Offline commander attention acceptance. Every HTTP response is synthetic;
// no Tianwu service, saved project, model or credential is used.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '../..');
const webRoot = path.join(root, 'internal/web/static');
const out = path.join(root, '.build/ui-qa');
fs.mkdirSync(out, { recursive: true });
const profile = fs.mkdtempSync(path.join(out, 'attention-chrome-'));
const chrome = spawn(process.env.TW_CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe', ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1', '--user-data-dir=' + profile, 'about:blank'], { windowsHide: true, stdio: 'ignore' });
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const reason = { code: 'unowned_result', kind: 'intent', id: 'in_branch', intent_id: 'in_branch', seq: 42, version: 3, detail: '方向已结束，没有局部承接者；需要全局判断' };
const board = {
  generated_at: '2026-11-13T08:00:00+08:00', case: 'fixture',
  intents: { total: 1, pending: 0, claimed: 0, running: 0, waiting: 0, done: 1, failed: 0, parked: 0 },
  facts: { total: 0, with_evidence: 0, verified: 0, by_kind: {}, by_origin: {} }, evidence: 0,
  runs: { total: 1, running: 0, done: 0, failed: 1, canceled: 0 },
  goals: { total: 1, met: 0, open: [{ id: 'g_fixture', statement: '完成授权范围验收' }] },
  open_intents: [], waiting_intents: [], running_intents: [], recent_facts: [], done_intents: ['in_branch'], frontier: [],
  project_paused: true,
  decider: { enabled: true, model: 'fixture-model', rounds: 2, tokens_total: 80, stalled: 0, paused: true, paused_reason: 'API 暂不可用，等待恢复', last_error: 'fixture API unavailable', pending_attention: [reason], last_attention: [reason], attention_delivered: false, activity: { phase: 'paused', detail: 'API 暂不可用，等待恢复' } },
};
const decisions = [
  { round: 1, at: '2026-11-13T07:50:00+08:00', wake: 'operator', ms: 20, tokens: 40, attention: [{ ...reason, detail: '操作员明确请求一次全局判断' }], attention_delivered: true },
  { round: 2, at: '2026-11-13T08:00:00+08:00', wake: 'intent_done', ms: 30, tokens: 40, error: 'fixture API unavailable', attention: [reason], attention_delivered: false },
];
function api(url) {
  const suffix = url.pathname.split('/api/')[1];
  switch (suffix) {
    case 'projects': return { projects: [{ id: 'fixture', name: '事件路由验收', intents: 1, facts: 0, evidence: 0, running: 0, created: '2026-11-13', max_runs_effective: 4 }] };
    case 'board': return board;
    case 'decisions': return { decisions };
    case 'goals': return { goals: [{ id: 'g_fixture', statement: '完成授权范围验收', met: false, created_at: '2026-11-13T08:00:00+08:00' }] };
    case 'runs': return { runs: [] };
    case 'nodes': return { nodes: [], default: 'local' };
    case 'state': return { state: '', path: 'fixture/state.md' };
    case 'playbooks': return { playbooks: [], counts: {}, has_more: false, total: 0 };
    case 'operator-requests': return { requests: [] };
    case 'participants': return { participants: [] };
    case 'messages': return { messages: [{ id: 'm_local', seq: 1, project_id: 'fixture', from_participant_id: 'ledger', to_participant_id: 'commander', kind: 'task_notice', text: '局部任务已结束，等待承接者核对。', notice_intent_id: 'in_branch', notice_run_id: 'r_child', status: 'queued', created_at: '2026-11-13T08:00:00+08:00', updated_at: '2026-11-13T08:00:00+08:00' }] };
    case 'communication': return { queues: [], max_messages: 50, max_bytes: 65536, paused: true };
    case 'settings': return { configured: true, model_configured: true };
    default: return {};
  }
}
(async () => {
  let ws;
  try {
    const portFile = path.join(profile, 'DevToolsActivePort');
    for (let i = 0; i < 100 && !fs.existsSync(portFile); i++) await pause(100);
    const port = Number(fs.readFileSync(portFile, 'utf8').split('\n')[0]);
    const pages = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
    ws = new WebSocket(pages.find(p => p.type === 'page').webSocketDebuggerUrl);
    await new Promise((resolve, reject) => { ws.addEventListener('open', resolve, { once: true }); ws.addEventListener('error', reject, { once: true }); });
    let seq = 0;
    const pending = new Map(), errors = [], checks = [];
    const call = (method, params = {}) => new Promise((resolve, reject) => {
      const id = ++seq, timer = setTimeout(() => { pending.delete(id); reject(new Error('CDP timeout ' + method)); }, 8000);
      pending.set(id, { resolve: value => { clearTimeout(timer); resolve(value); }, reject }); ws.send(JSON.stringify({ id, method, params }));
    });
    ws.addEventListener('message', async event => {
      const msg = JSON.parse(event.data);
      if (msg.id) { const p = pending.get(msg.id); if (p) { pending.delete(msg.id); msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result); } return; }
      if (msg.method === 'Runtime.exceptionThrown') errors.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
      if (msg.method === 'Fetch.requestPaused') {
        const { requestId, request } = msg.params, url = new URL(request.url);
        try {
          let body, type = 'application/json';
          if (url.pathname.startsWith('/static/')) {
            const file = path.join(webRoot, url.pathname.slice('/static/'.length));
            body = fs.readFileSync(file); type = file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'application/octet-stream';
          } else if (url.pathname.includes('/api/')) body = Buffer.from(JSON.stringify(api(url)));
          else { body = fs.readFileSync(path.join(webRoot, 'index.html')); type = 'text/html'; }
          await call('Fetch.fulfillRequest', { requestId, responseCode: 200, responseHeaders: [{ name: 'Content-Type', value: type }], body: body.toString('base64') });
        } catch (e) { errors.push(e.message); }
      }
    });
    await call('Page.enable'); await call('Runtime.enable'); await call('Fetch.enable', { patterns: [{ urlPattern: '*' }] });
    const evaluate = async expression => {
      const result = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
      if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text);
      return result.result.value;
    };
    const wait = async expression => { for (let n = 0; n < 100; n++) { if (await evaluate(expression)) return; await pause(75); } throw new Error('Timed out: ' + expression + '\n' + await evaluate('document.body.innerText')); };
    const check = async (name, expression) => { const passed = await evaluate(expression); checks.push({ name, passed }); assert.ok(passed, name); };
    const shot = async name => { const s = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }); fs.writeFileSync(path.join(out, name + '.png'), Buffer.from(s.data, 'base64')); };
    const visit = async view => call('Page.navigate', { url: 'http://tianwu.test/p/fixture/?view=' + view });
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1050, deviceScaleFactor: 1, mobile: false });
    await visit('commander&tab=decisions');
    await wait('document.querySelectorAll("[data-attention-reasons]").length===2');
    await check('rounds explain delivery and retained retry separately', `(()=>{const rows=[...document.querySelectorAll('[data-attention-reasons]')];return rows.some(r=>r.textContent.includes('本轮事件已交付'))&&rows.some(r=>r.textContent.includes('保留原记录等待恢复')&&r.textContent.includes('in_branch'))})()`);
    await shot('attention-decisions');
    await visit('overview');
    await wait('document.querySelector("[data-commander-attention]")!==null');
    await check('paused commander still shows pending attention', `(()=>{const t=document.querySelector('[data-commander-attention]').textContent;return t.includes('等待全局判断')&&t.includes('没有局部承接者')&&t.includes('仍保留')})()`);
    await evaluate(`document.querySelector('[data-commander-attention]').scrollIntoView({block:'center'})`);
    await shot('attention-paused');
    await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
    await visit('commander&tab=decisions');
    await wait('document.querySelectorAll("[data-attention-reasons]").length===2');
    await check('attention reasons stay readable on narrow viewport', `(()=>{const r=document.querySelector('[data-attention-reasons]').getBoundingClientRect();return r.width>120&&r.left>=0&&r.right<=innerWidth+1})()`);
    await shot('attention-mobile');
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1050, deviceScaleFactor: 1, mobile: false });
    await visit('messages');
    await wait('document.querySelector("[data-task-notice]")!==null');
    await check('task result shows source identity and premise boundary', `document.body.innerText.includes('局部任务结果')&&document.querySelector('[data-task-notice]').textContent.includes('r_child')&&document.querySelector('[data-task-notice]').textContent.includes('不改写')`);
    assert.deepEqual(errors, []);
    fs.writeFileSync(path.join(out, 'attention-checks.json'), JSON.stringify({ checks, errors }, null, 2));
    console.log(JSON.stringify({ passed: checks.length, errors }, null, 2));
  } finally {
    if (ws) ws.close();
    if (process.platform === 'win32') spawn('taskkill', ['/pid', String(chrome.pid), '/T', '/F'], { windowsHide: true, stdio: 'ignore' });
    else chrome.kill();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
