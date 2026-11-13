// Offline project-deletion regression. All requests use synthetic fixtures;
// no Tianwu service, real projects, tools or credentials are touched.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '../..');
const webRoot = path.resolve(process.env.TW_PROJECTS_WEB || path.join(root, 'internal/web/static'));
const out = path.join(root, '.build/ui-qa'); fs.mkdirSync(out, { recursive: true });
const profile = fs.mkdtempSync(path.join(out, 'projects-chrome-'));
const chrome = spawn(process.env.TW_CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe', ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1', '--user-data-dir=' + profile, 'about:blank'], { windowsHide: true, stdio: 'ignore' });
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const projects = ['第一个项目', '第二个项目'].map((name, i) => ({ id: 'p' + i, name, running: 0, intents: 0, facts: 0, evidence: 0, created: '2026-11-12', max_runs_effective: 4 }));
let holdReads = false, reads = [], deletes = [], failures = false;
async function api(url, request) {
  if (url.pathname === '/api/projects') {
    const snapshot = structuredClone(projects);
    if (holdReads) return new Promise(resolve => reads.push({ snapshot, resolve }));
    return { projects: snapshot };
  }
  if (request.method === 'DELETE' && url.pathname.startsWith('/api/projects/')) {
    const id = url.pathname.split('/').at(-1);
    assert.equal(url.searchParams.get('confirm'), id);
    await new Promise(resolve => deletes.push({ id, resolve }));
    if (failures) return { code: 409, value: { error: '还有运行没收尾，稍后重试' } };
    projects.splice(projects.findIndex(p => p.id === id), 1);
    return { deleted: id, vulns_kept: 0, projects: projects.length };
  }
  return {};
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
        const { requestId, request } = msg.params, url = new URL(request.url); let body, type = 'application/json', code = 200;
        try {
          if (url.pathname.startsWith('/static/')) {
            const file = path.join(webRoot, url.pathname.slice('/static/'.length));
            body = fs.readFileSync(file); type = file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'application/octet-stream';
          } else if (url.pathname.includes('/api/')) { const result = await api(url, request); code = result.code || 200; body = Buffer.from(JSON.stringify(result.value || result)); }
          else { body = fs.readFileSync(path.join(webRoot, 'index.html')); type = 'text/html'; }
          await call('Fetch.fulfillRequest', { requestId, responseCode: code, responseHeaders: [{ name: 'Content-Type', value: type }], body: body.toString('base64') });
        } catch (e) { errors.push(e.message); }
      }
    });
    await call('Page.enable'); await call('Runtime.enable'); await call('Fetch.enable', { patterns: [{ urlPattern: '*' }] });
    const evaluate = async expression => {
      const result = await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
      if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text);
      return result.result.value;
    };
    const wait = async expression => { for (let n = 0; n < 100; n++) { if (await evaluate(expression)) return; await pause(50); } throw new Error('Timed out: ' + expression + '\n' + await evaluate('document.body.innerText')); };
    const text = value => wait('document.body.innerText.includes(' + JSON.stringify(value) + ')');
    const click = async label => evaluate(`(()=>{const b=[...document.querySelectorAll('button')].find(b=>b.getClientRects().length&&(b.textContent.trim()===${JSON.stringify(label)}||b.getAttribute('aria-label')===${JSON.stringify(label)}));if(!b)throw Error('Missing button '+${JSON.stringify(label)});b.click()})()`);
    const check = (name, passed, actual) => { checks.push({ name, passed, ...(actual === undefined ? {} : { actual }) }); assert.ok(passed, name + ': ' + JSON.stringify(actual)); };
    const shot = async name => { const s = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false }); fs.writeFileSync(path.join(out, name + '.png'), Buffer.from(s.data, 'base64')); };
    const rowExists = id => `!![...document.querySelectorAll('tbody tr')].find(r=>r.textContent.includes('${id}'))`;
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
    await call('Page.navigate', { url: 'http://tianwu.test/g/?view=projects' }); await text('第一个项目');
    await click('删除项目「第一个项目」'); await text('确认删除');
    check('confirmation starts on cancel and sends no deletion', deletes.length === 0 && await evaluate(`document.activeElement.id==='cancel-project-deletion'`));
    check('shared tool lifetime is explained', await evaluate(`document.querySelector('[role=dialog]').textContent.includes('全局安装的共享工具会保留')`));
    const cancelPosition = await evaluate(`(()=>{const r=document.getElementById('cancel-project-deletion').getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2}})()`);
    await call('Input.dispatchMouseEvent', { type: 'mouseMoved', ...cancelPosition });
    await call('Input.dispatchMouseEvent', { type: 'mousePressed', button: 'left', clickCount: 1, ...cancelPosition });
    await pause(130);
    check('buttons animate a press without shifting layout', await evaluate(`(()=>{const s=getComputedStyle(document.getElementById('cancel-project-deletion'));return Number(s.scale)<1&&s.transitionProperty.includes('scale')})()`));
    await call('Input.dispatchMouseEvent', { type: 'mouseReleased', button: 'left', clickCount: 1, ...cancelPosition });
    await wait(`!document.querySelector('[role=dialog]')`); check('cancel preserves project', await evaluate(rowExists('p0')));

    holdReads = true; await click('刷新项目列表');
    await wait(`document.querySelector('button[aria-label="刷新项目列表"]').disabled`);
    check('refresh has immediate busy feedback', await evaluate(`document.querySelector('button[aria-label="刷新项目列表"] svg').classList.contains('motion-safe:animate-spin')`));
    await click('删除项目「第一个项目」'); await text('确认删除');
    await evaluate(`(()=>{const b=[...document.querySelectorAll('[role=dialog] button')].find(b=>b.textContent==='确认删除');b.click();b.click()})()`);
    await text('正在删除项目…');
    await pause(100); check('rapid clicks send one mutation', deletes.length === 1, deletes.length);
    check('pending progress is indeterminate and animated', await evaluate(`(()=>{const p=document.querySelector('[role=progressbar]');return !!p&&!p.hasAttribute('aria-valuenow')&&getComputedStyle(p.firstElementChild).animationName==='tw-progress'})()`));
    await call('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Escape', code: 'Escape', windowsVirtualKeyCode: 27 });
    await call('Input.dispatchMouseEvent', { type: 'mousePressed', button: 'left', clickCount: 1, x: 10, y: 10 });
    await call('Input.dispatchMouseEvent', { type: 'mouseReleased', button: 'left', clickCount: 1, x: 10, y: 10 });
    check('pending cannot be dismissed by Escape or backdrop', await evaluate(`!!document.querySelector('[role=progressbar]')&&getComputedStyle(document.querySelector('[role=dialog]>button:last-child')).display==='none'`));
    await shot('projects-delete-progress-desktop');
    deletes[0].resolve(); await text('项目已删除');
    check('confirmed deletion updates table before refresh resolves', !await evaluate(rowExists('p0')) && await evaluate(rowExists('p1')));
    check('sidebar immediately moves to remaining project', await evaluate(`[...document.querySelectorAll('button')].some(b=>b.textContent.includes('进入项目 · 第二个项目'))`));
    check('success waits for the server and replaces the progress bar', !await evaluate(`!!document.querySelector('[role=progressbar]')`));
    reads[0].resolve({ projects: reads[0].snapshot }); await pause(100);
    check('late pre-delete response cannot restore the deleted row', !await evaluate(rowExists('p0')));
    for (let i = 0; i < 100 && reads.length < 2; i++) await pause(50);
    reads[1].resolve({ code: 503, value: { error: '列表暂时不可用' } }); await text('列表暂时不可用');
    check('failed background refresh preserves confirmed deletion', !await evaluate(rowExists('p0')) && await evaluate(rowExists('p1')));
    await shot('projects-delete-success'); await click('完成');

    failures = true; holdReads = false;
    await call('Emulation.setDeviceMetricsOverride', { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
    await click('删除项目「第二个项目」'); await click('确认删除'); await text('正在删除项目…');
    await call('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-motion', value: 'reduce' }] });
    check('reduced motion keeps a static visible progress bar', await evaluate(`getComputedStyle(document.querySelector('[role=progressbar]>div')).animationName==='none'`));
    check('mobile progress dialog fits the viewport', await evaluate(`(()=>{const r=document.querySelector('[role=dialog]').getBoundingClientRect();return r.left>=0&&r.right<=innerWidth&&document.documentElement.scrollWidth<=innerWidth})()`));
    await shot('projects-delete-progress-mobile'); deletes[1].resolve(); await text('删除失败：');
    check('failed deletion preserves row and offers retry', await evaluate(rowExists('p1')) && await evaluate(`document.querySelector('[role=dialog] [role=alert]').textContent.includes('还有运行没收尾')`));
    await shot('projects-delete-error-mobile'); failures = false;
    await click('重试删除'); await text('正在删除项目…'); deletes[2].resolve(); await text('项目已删除');
    check('retry succeeds without a page reload', !await evaluate(rowExists('p1')) && deletes.length === 3);
    await click('完成'); await text('还没有项目。');
    check('last project deletion updates empty state and sidebar', await evaluate(`document.querySelectorAll('tbody tr').length===0&&document.body.innerText.includes('还没有项目')`));
    check('no browser runtime exceptions', errors.length === 0, errors);
    process.stdout.write(JSON.stringify({ checks, passed: checks.every(c => c.passed) }) + '\n');
  } finally { if (ws) ws.close(); chrome.kill(); }
})().catch(e => { process.stderr.write(e.stack + '\n'); process.exitCode = 1; });
