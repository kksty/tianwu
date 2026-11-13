// Offline instruction acceptance: Chrome receives only synthetic API responses
// and the built static files. No control-plane service or user data is accessed.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '../..');
const webRoot = path.resolve(process.env.TW_INSTRUCTIONS_WEB || path.join(root, 'internal/web/static'));
const out = path.join(root, '.build/ui-qa'); fs.mkdirSync(out, { recursive: true });
const profile = fs.mkdtempSync(path.join(out, 'instructions-chrome-'));
const chrome = spawn(process.env.TW_CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe', ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1', '--user-data-dir=' + profile, 'about:blank'], { windowsHide: true, stdio: 'ignore' });
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const at = '2026-11-12T10:00:00+08:00';
const rows = [{ id: 'instruction_old', text: '初始范围约束', version: 1, state: 'active', created_by: 'operator', created_at: at, updated_at: at }];
const history = new Map([[rows[0].id, [{ version: 1, at, value: { ...rows[0] } }]]]);
const writes = [];
const page = instructions => ({ instructions, total: instructions.length, has_more: false, snapshot_version: { epoch: 'fixture', seq: 1 } });

async function api(url, req) {
  const body = req.postData ? JSON.parse(req.postData) : {};
  if (url.pathname === '/api/projects') return { projects: [{ id: 'fixture', name: '编排验收', scope: '隔离测试', paused: true, created: at }] };
  if (url.pathname.endsWith('/api/board')) return { scope: '隔离测试', project_paused: true, decider: {paused: true, enabled: true, paused_reason: '隔离验收暂停'}, running_intents: [], intents: { total: 1, running: 0 }, facts: { total: 0 }, goals: { total: 0, met: 0 }, runs: { total: 0 }, recent_facts: [], open: [], running: [], frontier: [], pulse: [], alerts: [] };
  if (url.pathname.endsWith('/api/intents')) return { intents: [{ id: 'in_parent', title: '应用方向', status: 'pending' }], has_more: false };
  if (url.pathname.endsWith('/api/participants')) return { participants: [] };
  if (url.pathname.endsWith('/api/messages')) return { messages: [] };
  if (url.pathname.endsWith('/api/directives')) {
    assert.ok(body.idempotency_key); writes.push(body);
    assert.equal(body.persistent, true, 'acceptance creates a persistent directive');
    const row = { id: 'instruction_' + writes.length, text: body.text, intent_id: body.target === 'intent' ? body.intent_id : '', version: 1, state: 'active', created_by: 'operator', created_at: at, updated_at: at };
    rows.push(row); history.set(row.id, [{ version: 1, at, value: { ...row } }]);
    return { kind: 'instruction', target: body.target, intent_id: row.intent_id, persistent: true, created: true, instruction: row };
  }
  if (url.pathname.endsWith('/api/instructions')) {
    if (req.method !== 'POST') return page(rows.map(r => ({ ...r })));
    assert.ok(body.idempotency_key); writes.push(body);
    const row = { id: 'instruction_' + writes.length, text: body.text, intent_id: body.intent_id, version: 1, state: 'active', created_by: 'operator', created_at: at, updated_at: at };
    rows.push(row); history.set(row.id, [{ version: 1, at, value: { ...row } }]);
    return { instruction: row, created: true };
  }
  const match = /\/api\/instructions\/([^/]+)(\/history)?$/.exec(url.pathname);
  if (match) {
    const row = rows.find(r => r.id === match[1]);
    if (match[2]) return { history: history.get(row.id), total: history.get(row.id).length, has_more: false };
    if (req.method === 'PUT') {
      writes.push(body);
      if (row.version !== body.version) return { code: 409, value: { error: '指令版本冲突，请刷新后重新打开' } };
      Object.assign(row, { text: body.text, state: body.state, version: row.version + 1 });
      history.get(row.id).push({ version: row.version, at, value: { ...row } });
    }
    return { ...row };
  }
  if (url.pathname.endsWith('/api/graph')) return { facts: [], intents: [], goals: [], edges: [], boundary: [], total: 0, has_more: false };
  if (url.pathname.includes('/api/')) return { records: [], sessions: {}, paused: true, configured: true, participants: [], profiles: [], runs: [], intents: [] };
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
    let seq = 0; const pending = new Map(), errors = [], checks = [];
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
        let body, type = 'application/json', code = 200;
        try {
          if (url.pathname.startsWith('/static/')) {
            const file = path.join(webRoot, url.pathname.slice('/static/'.length));
            body = fs.readFileSync(file); type = file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'application/octet-stream';
          } else if (url.pathname.includes('/api/')) {
            const result = await api(url, request); code = result?.code || 200; body = Buffer.from(JSON.stringify(result?.value || result || {}));
          } else { body = fs.readFileSync(path.join(webRoot, 'index.html')); type = 'text/html'; }
          await call('Fetch.fulfillRequest', { requestId, responseCode: code, responseHeaders: [{ name: 'Content-Type', value: type }], body: body.toString('base64') });
        } catch (e) { errors.push(e.message); await call('Fetch.failRequest', {requestId, errorReason: 'Failed'}); }
      }
    });
    await call('Page.enable'); await call('Runtime.enable'); await call('Fetch.enable', { patterns: [{ urlPattern: '*' }] });
    const evaluate = async expression => {
      const result = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
      if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text);
      return result.result.value;
    };
    const wait = async expression => { for (let n = 0; n < 100; n++) { if (await evaluate(expression)) return; await pause(75); } throw new Error('Timed out: ' + expression + '\n' + await evaluate('document.body.innerText') + '\n' + JSON.stringify(errors)); };
    const text = value => wait('document.body.innerText.includes(' + JSON.stringify(value) + ')');
    const click = label => evaluate(`(()=>{const b=[...document.querySelectorAll('button')].find(b=>b.getClientRects().length&&b.textContent.trim()===${JSON.stringify(label)});if(!b)throw Error('Missing button '+${JSON.stringify(label)});b.click()})()`);
    const fill = value => evaluate(`(()=>{const e=document.querySelector('textarea[aria-label="持久指令正文"]');Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,'value').set.call(e,${JSON.stringify(value)});e.dispatchEvent(new Event('input',{bubbles:true}))})()`);
    const fillComposer = value => evaluate(`(()=>{const e=document.querySelector('[data-directive-composer] textarea');if(!e)throw Error('Missing directive composer');Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,'value').set.call(e,${JSON.stringify(value)});e.dispatchEvent(new Event('input',{bubbles:true}))})()`);
    const openRow = value => evaluate(`(()=>{const b=[...document.querySelectorAll('[data-instructions-panel] button')].find(b=>b.textContent.includes(${JSON.stringify(value)}));if(!b)throw Error('Missing row');b.click()})()`);
    const check = (name, passed) => { checks.push({ name, passed }); assert.ok(passed, name); };
    const shot = async name => { const result = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }); fs.writeFileSync(path.join(out, name + '.png'), Buffer.from(result.data, 'base64')); };
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
    await call('Page.navigate', { url: 'http://tianwu.test/p/fixture/?view=messages' });
    await text('持久指令'); await click('持久指令'); await text('初始范围约束');
    await click('持续到撤销');
    await fillComposer('后续任务也须遵守的约束');
    await click('创建指令'); await text('持久指令已保存'); await text('后续任务也须遵守的约束');
    check('new persistent instruction goes through the single directive entry with a retry key', writes[0].target === 'project' && writes[0].persistent === true && !!writes[0].idempotency_key);
    await openRow('初始范围约束'); await fill('尚未保存的更正');
    rows[0].text = '另一端已更正'; rows[0].version++;
    history.get(rows[0].id).push({version: 2, at, value: {...rows[0]}});
    await click('刷新列表'); await text('另一端已更正');
    check('refresh preserves the operator draft', await evaluate(`document.querySelector('textarea[aria-label="持久指令正文"]').value==='尚未保存的更正'`));
    await click('保存更正'); await text('指令版本冲突');
    check('a stale editor sends its original version', writes[1].version === 1 && rows[0].text === '另一端已更正');
    await openRow('另一端已更正'); await fill('更正后的持久约束'); await click('保存更正'); await text('更正后的持久约束');
    await openRow('更正后的持久约束'); await click('撤销指令'); await text('已撤销');
    check('revocation remains visible and preserves the text', rows[0].state === 'revoked' && rows[0].text === '更正后的持久约束');
    await openRow('更正后的持久约束'); await click('修订历史'); await text('初始范围约束');
    check('revision history includes original and revoked versions', history.get(rows[0].id).length === 4);
    await shot('instructions-desktop');
    await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
    await pause(200);
    check('instruction page fits a narrow viewport', await evaluate('document.documentElement.scrollWidth<=innerWidth'));
    await shot('instructions-mobile');
    check('no browser runtime exceptions', errors.length === 0);
    process.stdout.write(JSON.stringify({ checks, passed: checks.every(c => c.passed), errors }) + '\n');
  } finally { if (ws) ws.close(); chrome.kill(); }
})().catch(e => { process.stderr.write(e.stack + '\n'); process.exitCode = 1; });

