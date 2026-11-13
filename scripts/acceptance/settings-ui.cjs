// Offline settings acceptance. All page HTTP is fulfilled from fixtures;
// no Tianwu service, model provider, project data or saved credentials are used.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '../..');
const webRoot = path.resolve(process.env.TW_SETTINGS_WEB || path.join(root, 'internal/web/static'));
const out = path.join(root, '.build/ui-qa'); fs.mkdirSync(out, { recursive: true });
const chromeProfile = fs.mkdtempSync(path.join(out, 'settings-chrome-'));
const chrome = spawn(process.env.TW_CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe', ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1', '--user-data-dir=' + chromeProfile, 'about:blank'], { windowsHide: true, stdio: 'ignore' });
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
let active = 'main', saves = [], deletes = [], projectSaves = [], catalogueCalls = 0;
const profiles = [
  { id: 'main', name: '日常模型', provider: 'openai', model: 'model-main', base_url: 'https://model.test/v1', proxy: 'http://proxy.test:8080', api_key_set: true, runnable: true, probed: true, available: true, revision: 'r1' },
  { id: 'backup', name: '备用模型', provider: 'openai', model: 'model-backup', base_url: 'http://localhost:11434/v1', api_key_set: true, runnable: true, probed: false, probe_error: '端点没有模型目录', revision: 'r2' },
];
const projects = ['Touch', '第二个项目'].map((name, i) => ({ id: 'p' + i, name, running: 0, intents: 0, facts: 0, evidence: 0, created: '2026-11-07', max_runs: null, max_runs_effective: 4, round_timeout: '', worker_profile: '', strong_profile: i ? 'backup' : '', verify_auto: null, verify_auto_effective: false }));
async function api(url, request) {
  const body = request.postData ? JSON.parse(request.postData) : {};
  if (url.pathname === '/api/settings') return { configured: true, api_key_set: true, model: 'model-main', active_profile: active, path: 'C:/fixture/.cyber/cyber.yaml' };
  if (url.pathname === '/api/projects') return { projects, max_runs: 4, round_timeout_seconds: 900, verify_auto_default: false };
  if (url.pathname.startsWith('/api/projects/') && request.method === 'PUT') {
    projectSaves.push(body); await pause(400);
    if (body.round_timeout === 'bad') return { code: 400, value: { error: '时长不合法' } };
    const project = projects.find(p => url.pathname.endsWith('/' + p.id)); Object.assign(project, body); return project;
  }
  if (url.pathname === '/api/model-catalogue') { catalogueCalls++; await pause(500); return { endpoint: body.base_url + '/models', probed: true, models: ['model-main', 'model-second'] }; }
  if (url.pathname === '/api/model-profiles') {
    if (request.method === 'POST') { saves.push(body); await pause(500); profiles.push({ ...body, api_key: undefined, api_key_set: true, runnable: true, revision: 'created' }); return profiles.at(-1); }
    return { profiles: profiles.map(p => ({ ...p, active: p.id === active })), active_profile: active, path: 'C:/fixture/.cyber/cyber.yaml', has_overrides: false };
  }
  const hit = /^\/api\/model-profiles\/([^/]+)(?:\/(probe|activate))?$/.exec(url.pathname);
  if (hit) {
    const p = profiles.find(p => p.id === hit[1]);
    if (hit[2] === 'probe') return { id: p.id, model: p.model, probed: false, available: false, probe_error: '端点没有模型目录' };
    if (hit[2] === 'activate') { active = p.id; return p; }
    if (request.method === 'DELETE') { deletes.push(p.id); profiles.splice(profiles.indexOf(p), 1); return { deleted: p.id }; }
    if (request.method === 'PUT') { saves.push(body); await pause(500); Object.assign(p, { ...body, api_key: undefined, api_key_set: true }); return p; }
  }
  return { profiles: [], projects: [], nodes: [], configured: true, model_configured: true };
}

(async () => {
  let ws;
  try {
    const portFile = path.join(chromeProfile, 'DevToolsActivePort');
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
        const { requestId, request } = msg.params, url = new URL(request.url); let body, type = 'application/json', code = 200;
        if (url.pathname.startsWith('/static/')) {
          const file = path.join(webRoot, url.pathname.slice('/static/'.length));
          if (fs.existsSync(file)) { body = fs.readFileSync(file); type = file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'application/octet-stream'; }
          else { body = Buffer.from('not found'); code = 404; errors.push('Missing asset ' + file); }
        } else if (url.pathname.includes('/api/')) { const result = await api(url, request); code = result.code || 200; body = Buffer.from(JSON.stringify(result.value || result)); }
        else { body = fs.readFileSync(path.join(webRoot, 'index.html')); type = 'text/html'; }
        try { await call('Fetch.fulfillRequest', { requestId, responseCode: code, responseHeaders: [{ name: 'Content-Type', value: type }], body: body.toString('base64') }); } catch (e) { errors.push(e.message); }
      }
    });
    await call('Page.enable'); await call('Runtime.enable'); await call('Fetch.enable', { patterns: [{ urlPattern: '*' }] });
    const evaluate = async expression => {
      const result = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
      if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text);
      return result.result.value;
    };
    const wait = async expression => { for (let n = 0; n < 100; n++) { if (await evaluate(expression)) return; await pause(75); } throw new Error('Timed out: ' + expression + '\n' + await evaluate('document.body.innerText')); };
    const text = value => wait('document.body.innerText.includes(' + JSON.stringify(value) + ')');
    const click = async label => evaluate(`(()=>{const b=[...document.querySelectorAll('button')].find(b=>b.getClientRects().length&&(b.textContent.trim()===${JSON.stringify(label)}||b.getAttribute('aria-label')===${JSON.stringify(label)}));if(!b)throw Error('Missing button '+${JSON.stringify(label)});b.click()})()`);
    const fill = async (label, value) => evaluate(`(()=>{const e=[...document.querySelectorAll('input')].find(e=>e.getAttribute('aria-label')===${JSON.stringify(label)});if(!e)throw Error('Missing input '+${JSON.stringify(label)});Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(e,${JSON.stringify(value)});e.dispatchEvent(new Event('input',{bubbles:true}))})()`);
    const select = async (label, option) => {
      await evaluate(`document.querySelector('[role=combobox][aria-label="${label}"]').click()`);
      await wait('document.querySelector("[role=option]")!==null');
      const rect = await evaluate(`(()=>{const e=[...document.querySelectorAll('[role=option]')].find(e=>e.textContent.includes(${JSON.stringify(option)}));if(!e)throw Error('Missing option');e.scrollIntoView({block:'center'});const r=e.getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2}})()`);
      await call('Input.dispatchMouseEvent', { type: 'mouseMoved', ...rect });
      await call('Input.dispatchMouseEvent', { type: 'mousePressed', button: 'left', clickCount: 1, ...rect });
      await call('Input.dispatchMouseEvent', { type: 'mouseReleased', button: 'left', clickCount: 1, ...rect });
      await pause(100);
    };
    const check = (name, passed, actual) => { checks.push({ name, passed, ...(actual === undefined ? {} : { actual }) }); assert.ok(passed, name + ': ' + JSON.stringify(actual)); };
    const shot = async name => { const s = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }); fs.writeFileSync(path.join(out, name + '.png'), Buffer.from(s.data, 'base64')); };
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1050, deviceScaleFactor: 1, mobile: false });
    await call('Page.navigate', { url: 'http://tianwu.test/g/?view=settings' }); await text('日常模型');
    check('single-model guidance is visible', await evaluate('document.body.innerText.includes("无需配置“小模型”")'));
    await shot('settings-models-desktop');
    await click('编辑 main'); await fill('API Key', 'fixture-new-key'); await fill('显示名称', '日常模型更新');
    check('key blur does not autosave', saves.length === 0);
    check('saved profile identity cannot be edited', await evaluate(`document.querySelector('input[aria-label="配置 ID"]').disabled`));
    await click('读取模型目录'); await pause(100);
    check('catalogue request locks draft and cancel', await evaluate(`document.querySelector('form input[aria-label="API 地址"]').matches(':disabled') && [...document.querySelectorAll('button')].find(b=>b.textContent.trim()==='取消编辑').disabled`));
    await text('model-second'); await click('model-second');
    await click('保存配置'); await pause(100);
    check('save locks editor and prevents duplicate writes', await evaluate(`document.querySelector('form input[aria-label="API Key"]').matches(':disabled')`));
    await text('已保存 main'); check('profile saved exactly once with proxy preserved', saves.length === 1 && saves[0].proxy === 'http://proxy.test:8080' && saves[0].model === 'model-second', saves);
    await click('新增配置'); await fill('配置 ID', 'new'); await fill('API 地址', 'http://localhost:8081/v1'); await fill('模型 ID', 'new-model'); await fill('API Key', 'fixture');
    await select('供应商预设', 'OpenCode Go');
    check('OpenCode Go preset selects subscription endpoint and OpenAI protocol', await evaluate(`document.querySelector('input[aria-label="API 地址"]').value==='https://opencode.ai/inference/go/openai/v1' && document.querySelector('[role=combobox][aria-label="API 协议"]').textContent.includes('OpenAI')`));
    await shot('settings-opencode-go-preset');
    await fill('API 地址', 'http://localhost:8081/v1');
    await select('供应商预设', '自定义网关');
    check('custom preset can be selected without changing endpoint', await evaluate(`document.querySelector('input[aria-label="API 地址"]').value==='http://localhost:8081/v1'`));
    await click('保存配置'); await text('已保存 new'); check('new profile uses explicit save', saves.length === 2);
    const probeButton = await evaluate("(()=>{const b=[...document.querySelectorAll('article')].find(e=>e.textContent.includes('备用模型')).querySelector('button');b.click();return true})()");
    await text('备用模型：待确认'); check('unsupported directory is not reported as inference failure', probeButton && await evaluate('document.body.innerText.includes("端点没有模型目录")'));
    await click('删除 backup'); await text('配置仍被项目引用'); check('referenced profile deletion is prevented', deletes.length === 0);
    await evaluate(`(()=>{const a=[...document.querySelectorAll('article')].find(a=>a.textContent.includes('ID · backup'));[...a.querySelectorAll('button')].find(b=>b.textContent.trim()==='设为默认').click()})()`);
    await text('将 备用模型 设为实例默认？');
    check('activation requires explicit confirmation', active === 'main');
    await evaluate(`[...document.querySelectorAll('[role=dialog] button,[role=alertdialog] button')].find(b=>b.textContent.trim()==='设为默认').click()`);
    await text('backup 已设为实例默认'); check('activation updates the selected default', active === 'backup');
    await click('删除 new'); await text('删除 new？');
    await evaluate(`[...document.querySelectorAll('[role=dialog] button,[role=alertdialog] button')].find(b=>b.textContent.trim()==='删除配置').click()`);
    await text('已删除 new'); check('unused profile deletion is explicit and succeeds', deletes.length === 1 && deletes[0] === 'new');
    await click('项目默认'); await text('Touch');
    await fill('Touch 的执行者并发', '3'); await fill('第二个项目 的执行者并发', '5');
    await select('Touch 的指挥官模型', '备用模型'); await click('执行者与指挥官相同');
    check('one profile can serve both responsibilities', await evaluate('document.querySelectorAll("#settings-panel-projects [role=combobox]")[0].textContent===document.querySelectorAll("#settings-panel-projects [role=combobox]")[1].textContent'));
    projects[0].round_timeout = '20m'; await click('刷新'); await pause(300);
    check('refresh preserves dirty fields and updates untouched fields', await evaluate(`document.querySelector('input[aria-label="Touch 的执行者并发"]').value==='3' && document.querySelector('input[aria-label="Touch 的指挥官单轮时限"]').value==='20m'`));
    await select('Touch 的自动复核', '仅手动复核');
    await click('保存项目设置'); await pause(100);
    check('project save locks only its own card', await evaluate(`document.querySelector('input[aria-label="Touch 的执行者并发"]').matches(':disabled') && !document.querySelector('input[aria-label="第二个项目 的执行者并发"]').matches(':disabled')`));
    await text('已保存。简报与约束用于之后的派发和决策');
    check('project save includes same-model defaults and explicit false', projectSaves[0].strong_profile === 'backup' && projectSaves[0].worker_profile === 'backup' && projectSaves[0].verify_auto === false && projectSaves[0].max_runs === 3 && !('round_timeout' in projectSaves[0]), projectSaves[0]);
    check('saving one project preserves another draft', await evaluate(`document.querySelector('input[aria-label="第二个项目 的执行者并发"]').value==='5'`));
    await fill('Touch 的指挥官单轮时限', 'bad'); await click('保存项目设置'); await text('时长不合法');
    check('server validation error keeps the failed draft', await evaluate(`document.querySelector('input[aria-label="Touch 的指挥官单轮时限"]').value==='bad'`));
    await click('撤销改动'); await pause(100); await shot('settings-projects-desktop');
    await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
    await pause(200); check('project settings fit narrow screens', await evaluate('document.documentElement.scrollWidth<=innerWidth'));
    await shot('settings-projects-mobile'); await click('模型配置'); await click('编辑 main'); await pause(100);
    check('model editor fits narrow screens', await evaluate('document.documentElement.scrollWidth<=innerWidth'));
    await evaluate('document.querySelector("form").scrollIntoView({block:"start"});window.scrollTo(0,0)');
    await shot('settings-editor-mobile');
    check('no browser runtime exceptions', errors.length === 0, errors);
    process.stdout.write(JSON.stringify({ checks, passed: checks.every(c => c.passed), catalogueCalls }) + '\n');
  } finally { if (ws) ws.close(); chrome.kill(); }
})().catch(e => { process.stderr.write(e.stack + '\n'); process.exitCode = 1; });
