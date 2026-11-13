// Offline access/handoff acceptance. All page HTTP is fulfilled from fixtures;
// no Tianwu service, model provider, project data or saved credentials are used.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '../..');
const webRoot = path.resolve(process.env.TW_ACCESS_WEB || path.join(root, 'internal/web/static'));
const out = path.join(root, '.build/ui-qa'); fs.mkdirSync(out, { recursive: true });
const chromeProfile = fs.mkdtempSync(path.join(out, 'access-chrome-'));
const chrome = spawn(process.env.TW_CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe', ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1', '--user-data-dir=' + chromeProfile, 'about:blank'], { windowsHide: true, stdio: 'ignore' });
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const now = '2026-11-07T00:00:00Z';
const project = { id: 'fixture', name: '本地界面回归', scope: '本地界面测试', paused: true, max_runs_effective: 0 };
const board = {"generated_at":"2026-11-07T00:00:00Z","project_paused":true,"intents":{"total":0,"running":0,"pending":0,"done":0,"failed":0,"parked":0,"held":0,"claimed":0},"facts":{"total":0,"with_evidence":0,"verified":0,"unverified":0,"rejected":0,"with_refs":0},"evidence":0,"runs":{"total":0,"running":0,"done":0,"failed":0,"canceled":0},"goals":{"total":0,"met":0,"open":[]},"open_intents":[],"running_intents":[],"recent_facts":[],"done_intents":[],"playbooks":{"total":1,"confirmed":1,"candidates":0},"redispatch":{"total":0,"directions":0},"usage":{"total_tokens":0,"input_tokens":0,"output_tokens":0,"cached_input_tokens":0,"requests":0,"missing_usage":0,"unknown_usage_runs":0},"decider":{"enabled":true,"paused":true,"model":"fixture","rounds":0,"tokens_total":0,"stalled":0,"activity":{"phase":"paused","detail":"已暂停"},"heartbeat_seconds":300}};


const entries = [], requests = [{intent_id:'in_login', title:'登录后继续核对', status:'waiting', run_id:'r_worker', profile:'named-worker', request:{id:'op_login',request:'请扫码，当前页面等待手机确认',access_ids:[]}}];
const writes = [], order = [];
let artifactCalls = 0, failOpen = false;
async function api(url, request) {
  const body = request.postData ? JSON.parse(request.postData) : {};
  if(url.pathname==='/api/projects') return {projects:[project]};
  if(url.pathname.endsWith('/api/board')) return board;
  if(url.pathname.endsWith('/api/operator-requests')) return {requests};
  if(url.pathname.endsWith('/api/artifacts')) {artifactCalls++; return {artifacts:[]};}
  if(url.pathname.endsWith('/api/access')) {
    if(request.method==='POST') {
      writes.push(body); await pause(200);
      const e={id:'ac_'+writes.length,name:body.name||'浏览器 '+writes.length,kind:body.kind,origin:body.origin||'',url:body.origin||'',created_at:now};
      entries.push(e);
      if(body.request_id) requests[0].request.access_ids.push(e.id);
      return e;
    }
    return {access:entries};
  }
  const hit=/\/api\/access\/([^/]+)\/(lease|browser)$/.exec(url.pathname);
  if(hit) {
    const e=entries.find(e=>e.id===hit[1]); order.push(body.action);
    if(hit[2]==='lease') {
      if(body.action==='acquire') {e.lease={id:'bl_'+e.id,owner:'operator',state:'held',acquired_at:now};return {lease:e.lease};}
      delete e.lease; return {released:true};
    }
    if(failOpen) return {code:409,value:{error:'测试连接暂时不可用'}};
    return {returned:true,output:'https://example.test/captcha?keep-page=true',verified:false};
  }
  if(url.pathname.endsWith('/operator/complete')) {
    order.push('complete');
    assert.ok(entries.every(e=>!requests[0].request.access_ids.includes(e.id)||!e.lease),'completed before lease was returned');
    requests[0].request.completed_at=now; return {accepted:true};
  }
  return {facts:[],intents:[],goals:[],runs:[],profiles:[],nodes:[],playbooks:[],decisions:[],requests:[],evidence:[],state:'',configured:true,model_configured:true};
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

    const field = async(label,value) => evaluate(`(()=>{const l=[...document.querySelectorAll('label')].find(e=>e.querySelector('span')?.textContent===${JSON.stringify(label)});if(!l)throw Error('Missing field');const e=l.querySelector('input');Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value').set.call(e,${JSON.stringify(value)});e.dispatchEvent(new Event('input',{bubbles:true}))})()`);
    await call('Emulation.setDeviceMetricsOverride',{width:1440,height:1050,deviceScaleFactor:1,mobile:false});
    await call('Page.navigate',{url:'http://tianwu.test/p/fixture/?view=access'}); await text('准备登录浏览器');
    check('page omits identity fields and native results',await evaluate(`!document.body.innerText.includes('原生工具结果') && ![...document.querySelectorAll('label')].some(e=>['测试账号','角色','租户','浏览器所在模型宿主'].includes(e.querySelector('span')?.textContent))`));
    await click('准备登录浏览器'); await text('为「登录后继续核对」准备登录现场');
    check('request preparation only needs a target URL',await evaluate(`document.querySelectorAll('form input:required').length===1 && document.querySelector('form input:required').placeholder==='https://app.example/login'`));
    await click('连接已有浏览器'); await field('CDP 地址','http://127.0.0.1:9222');
    check('CDP mode has one required field',await evaluate(`document.querySelectorAll('form input:required').length===1 && !document.querySelector('form input[placeholder="https://app.example/login"]')`));
    await shot('access-cdp-desktop');
    await click('连接并打开'); await text('https://example.test/captcha?keep-page=true');
    check('browser creation inherits exact request without client-selected host',writes.length===1 && writes[0].intent_id==='in_login' && writes[0].request_id==='op_login' && !('profile' in writes[0]) && !('account' in writes[0]),writes);
    check('connect automatically acquires before opening',order.join(',')==='acquire,open',order);
    await shot('access-handoff-desktop');
    await click('已完成，继续'); await text('人工回执已提交');
    check('completion automatically returns operator browser before resuming',order.slice(-2).join(',')==='release,complete',order);
    failOpen=true;
    await click('连接浏览器'); await field('CDP 地址','http://127.0.0.1:9223'); await click('连接并打开'); await text('测试连接暂时不可用');
    check('failed connection keeps registered browser for retry',writes.length===2 && await evaluate(`document.body.innerText.includes('浏览器 2') && !document.querySelector('form')`));
    await call('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true});
    await click('连接浏览器'); await pause(200);
    check('narrow layout has no horizontal overflow',await evaluate('document.documentElement.scrollWidth<=innerWidth'));
    await shot('access-cdp-mobile');
    check('native artifact list is never requested',artifactCalls===0,artifactCalls);
    check('no browser runtime exceptions',errors.length===0,errors);
    process.stdout.write(JSON.stringify({checks,passed:checks.every(c=>c.passed)})+'\n');
  } finally {if(ws)ws.close();chrome.kill();}
})().catch(e=>{process.stderr.write(e.stack+'\n');process.exitCode=1;});
