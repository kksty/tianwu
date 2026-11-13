// Offline browser acceptance for the merged project rail (docs/34): 12 items in
// 3 groups, old `?view=` ids resolving to their merged group + tab, and the tab
// strip rendering the canonical URL. Every HTTP response is mocked (fixtures
// from .build/ui-qa/messages-fixture, static files from internal/web/static):
// no console, model or live data is touched.
// Run with node scripts/acceptance/nav-ui.cjs; TW_CHROME overrides Chrome.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');

const root = path.resolve(__dirname, '../..');
const out = path.join(root, '.build/ui-qa');
const fixtures = path.join(out, 'messages-fixture');
const staticRoot = path.join(root, 'internal/web');
fs.mkdirSync(out, { recursive: true });

const pageURL = 'http://tianwu.test/p/fixture/?view=messages';
const profile = fs.mkdtempSync(path.join(out, 'chrome-'));
const chrome = spawn(
  process.env.TW_CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe',
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

/** Kill Chrome together with its helper processes (Windows leaks crashpad kids). */
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

function pngSize(buffer) {
  if (buffer.length < 24 || buffer.toString('ascii', 1, 4) !== 'PNG') return null;
  return { width: buffer.readUInt32BE(16), height: buffer.readUInt32BE(20) };
}

// ---- mocked HTTP -----------------------------------------------------------
const apiRoutes = new Map([
  ['/api/projects', 'projects.json'],
  ['/p/fixture/api/board', 'board.json'],
  ['/p/fixture/api/participants', 'participants.json'],
  ['/p/fixture/api/ioa/events', 'ioa.json'],
  ['/api/ioa/events', 'ioa.json'],
  ['/p/fixture/api/messages', 'messages.json'],
]);
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

// Minimal valid shapes for the pages the merged-nav aliases open. The fixture
// files above cover the messages page; these endpoints are otherwise answered
// with `{}`, which is not a valid body for a page that renders it directly
// (e.g. Workers reads peer_policy.allow on mount).
const INLINE_ROUTES = {
  '/p/fixture/api/peers': { mode: 'open', peer_policy: { allow: [] } },
  '/p/fixture/api/premise-policy': { premise_policy: 'standard', effective: 'standard' },
  '/p/fixture/api/focus': {
    intent_id: '',
    layer: 'focus',
    facts: [],
    hot: 0,
    warm: 0,
    cold: 0,
    index_query: '',
    cold_query: '',
    fact_query: '',
    premises: '',
    experience_query: '',
    total: 0,
    has_more: false,
    snapshot_version: { epoch: 'fixture', seq: 7 },
  },
  '/p/fixture/api/intents': { intents: [], total: 0, has_more: false, snapshot_version: { epoch: 'fixture', seq: 7 } },
  // Enough evidence rows to make the evidence page taller than the viewport so
  // the scroll-memory check has something to scroll.
  '/p/fixture/api/evidence': {
    evidence: Array.from({ length: 80 }, (_, i) => ({
      id: 'ev_' + String(i).padStart(3, '0'),
      uri: 'evidence/fixture/file-' + String(i).padStart(3, '0') + '.txt',
      name: 'file-' + String(i).padStart(3, '0') + '.txt',
      mime: 'text/plain',
      size: 1024 + i,
      intent_id: 'intent_in_1',
      created_at: '2026-11-12T10:' + String(i % 60).padStart(2, '0') + ':00+08:00',
    })),
  },
};

function mockedResponse(url) {
  if (url.pathname.startsWith('/static/')) {
    const file = path.join(staticRoot, decodeURIComponent(url.pathname));
    if (!fs.existsSync(file) || !fs.statSync(file).isFile()) {
      return { code: 404, type: 'text/plain; charset=utf-8', body: Buffer.from('not found') };
    }
    return {
      code: 200,
      type: CONTENT_TYPES[path.extname(file)] || 'application/octet-stream',
      body: fs.readFileSync(file),
    };
  }
  const fixture = apiRoutes.get(url.pathname);
  if (fixture) {
    return {
      code: 200,
      type: 'application/json; charset=utf-8',
      body: fs.readFileSync(path.join(fixtures, fixture)),
    };
  }
  if (INLINE_ROUTES[url.pathname]) {
    return {
      code: 200,
      type: 'application/json; charset=utf-8',
      body: Buffer.from(JSON.stringify(INLINE_ROUTES[url.pathname])),
    };
  }
  if (url.pathname.includes('/api/')) {
    return { code: 200, type: 'application/json; charset=utf-8', body: Buffer.from('{}') };
  }
  return {
    code: 200,
    type: 'text/html; charset=utf-8',
    body: fs.readFileSync(path.join(staticRoot, 'static/index.html')),
  };
}

(async () => {
  const checks = [];
  const check = (name, passed, expected, actual) =>
    checks.push({ name, passed: Boolean(passed), expected, actual });

  let ws;
  try {
    const portFile = path.join(profile, 'DevToolsActivePort');
    for (let n = 0; n < 150 && !fs.existsSync(portFile); n++) await pause(100);
    if (!fs.existsSync(portFile)) throw new Error('Chrome did not open a DevTools port');
    const port = Number(fs.readFileSync(portFile, 'utf8').split('\n')[0]);
    const pages = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
    ws = new WebSocket(pages.find((x) => x.type === 'page').webSocketDebuggerUrl);
    await new Promise((resolve, reject) => {
      ws.addEventListener('open', resolve, { once: true });
      ws.addEventListener('error', reject, { once: true });
    });

    let seq = 0;
    const pending = new Map();
    const errors = [];
    const call = (method, params = {}) =>
      new Promise((resolve, reject) => {
        const id = ++seq;
        const timer = setTimeout(() => {
          pending.delete(id);
          reject(new Error('CDP timeout ' + method));
        }, 8000);
        pending.set(id, {
          resolve: (v) => {
            clearTimeout(timer);
            resolve(v);
          },
          reject: (e) => {
            clearTimeout(timer);
            reject(e);
          },
        });
        ws.send(JSON.stringify({ id, method, params }));
      });
    ws.addEventListener('message', async (evt) => {
      const msg = JSON.parse(evt.data);
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
      }
      if (msg.method === 'Fetch.requestPaused') {
        const { requestId, request } = msg.params;
        let response;
        try {
          response = mockedResponse(new URL(request.url));
        } catch (e) {
          errors.push('mock failed for ' + request.url + ': ' + e.message);
          response = { code: 500, type: 'text/plain', body: Buffer.from('mock failed') };
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

    const evalJS = async (expression) =>
      (await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })).result.value;
    const waitJS = async (expression, label) => {
      for (let i = 0; i < 150; i++) {
        if (await evalJS(expression)) return;
        await pause(100);
      }
      throw new Error('Timed out waiting for ' + label + ' / errors=' + JSON.stringify(errors.slice(-4)) + ' / body=' + (await evalJS('document.body.innerText')).slice(0, 300));
    };
    const shot = async (name) => {
      const data = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
      const buffer = Buffer.from(data.data, 'base64');
      fs.writeFileSync(path.join(out, name), buffer);
      return { path: '.build/ui-qa/' + name, bytes: buffer.length, size: pngSize(buffer) };
    };

    // ---- the rail ---------------------------------------------------------
    await call('Page.navigate', { url: pageURL });
    await waitJS('Boolean(document.querySelector(\'nav[aria-label="项目导航"]\'))', '项目导航 nav');
    await waitJS('document.querySelectorAll(\'nav[aria-label="项目导航"] button\').length >= 12', '侧栏按钮');
    await pause(500);

    const rail = await evalJS(`(() => {
      const nav = document.querySelector('nav[aria-label="项目导航"]');
      const groups = [...nav.children].map((group) => {
        const labelNode = group.querySelector(':scope > div:first-child');
        const items = [...group.querySelectorAll('button')].map((b) => {
          const span = b.querySelector('span');
          return {
            // Accessible name = the label span (a badge would be a separate span).
            name: (span ? span.innerText : b.innerText).trim(),
            text: b.innerText.trim(),
            current: b.getAttribute('aria-current') === 'page',
          };
        });
        return { label: labelNode ? labelNode.innerText.trim() : '', items };
      });
      return {
        groups,
        bodyText: document.body.innerText,
        active: (nav.querySelector('button[aria-current="page"] span') || {}).innerText || '',
      };
    })()`);

    const groupLabels = rail.groups.map((g) => g.label);
    const section = (label) => {
      const g = rail.groups.find((x) => x.label === label);
      return g ? g.items.map((i) => i.name) : [];
    };
    const taskScene = section('任务现场');
    const allItems = rail.groups.flatMap((g) => g.items.map((i) => i.name));

    check(
      '3 个分组顺序 = 任务现场 / 发现与证据 / 指挥与运行',
      JSON.stringify(groupLabels) === JSON.stringify(['任务现场', '发现与证据', '指挥与运行']),
      ['任务现场', '发现与证据', '指挥与运行'],
      groupLabels,
    );
    check(
      '侧栏 12 项（21 → 12）',
      allItems.length === 12,
      12,
      allItems,
    );
    check(
      '「任务现场」顺序 = 概览/图谱/状态/通信/运行/访问与交接',
      JSON.stringify(taskScene) === JSON.stringify(['概览', '图谱', '状态', '通信', '运行', '访问与交接']),
      ['概览', '图谱', '状态', '通信', '运行', '访问与交接'],
      taskScene,
    );
    const ip = (label) => taskScene.indexOf(label);
    check('「通信」紧跟「状态」之后', ip('通信') === ip('状态') + 1, '通信 = 状态 + 1', { state: ip('状态'), messages: ip('通信') });
    check('「访问与交接」是分组最后一项', ip('访问与交接') === taskScene.length - 1, 'index ' + (taskScene.length - 1), ip('访问与交接'));
    check(
      '「发现与证据」= 证据/漏洞/战场知识/报告',
      JSON.stringify(section('发现与证据')) === JSON.stringify(['证据', '漏洞', '战场知识', '报告']),
      ['证据', '漏洞', '战场知识', '报告'],
      section('发现与证据'),
    );
    check(
      '「指挥与运行」= 决策/执行环境',
      JSON.stringify(section('指挥与运行')) === JSON.stringify(['决策', '执行环境']),
      ['决策', '执行环境'],
      section('指挥与运行'),
    );
    const merged = ['协作', '外部动作', '资产', 'HTTP 流量', '执行', '工具与能力', '终端', 'REPL', '记忆'];
    check(
      '被合并的旧页面不再出现在侧栏',
      merged.every((name) => !allItems.includes(name)),
      '不含 ' + merged.join('/'),
      allItems.filter((name) => merged.includes(name)),
    );
    check('当前页「通信」在侧栏高亮', rail.active === '通信', '通信', rail.active);

    const navShot = await shot('nav-order.png');
    check('截图 1440x1000 且非空白', Boolean(navShot.size && navShot.size.width === 1440 && navShot.size.height === 1000 && navShot.bytes > 20000), '1440x1000 / > 20KB', [navShot.size, navShot.bytes]);

    // ---- old deep links resolve to the merged group + tab -------------------
    const checkAlias = async (path, expected) => {
      try {
        await call('Page.navigate', { url: 'http://tianwu.test/p/fixture/' + path });
        await waitJS('Boolean(document.querySelector(\'[data-testid="view-tabs"]\'))', 'view tabs ' + path);
        await waitJS(
          'new URL(location.href).searchParams.get("view") === ' + JSON.stringify(expected.view),
          'canonical view ' + path,
        );
        await pause(250);
        const state = await evalJS(`(() => {
          const u = new URL(location.href);
          const nav = document.querySelector('nav[aria-label="项目导航"]');
          const active = (nav.querySelector('button[aria-current="page"] span') || {}).innerText || '';
          const tab = document.querySelector('[data-testid="view-tabs"] [aria-selected="true"]');
          return { view: u.searchParams.get('view'), tab: u.searchParams.get('tab'), active, tabId: tab ? tab.getAttribute('data-testid') : null };
        })()`);
        check(
          `旧深链 ${path} → ${expected.view} 的 ${expected.tab} 标签`,
          state.view === expected.view &&
            state.tab === expected.tab &&
            state.active === expected.active &&
            state.tabId === 'tab-' + expected.tab,
          { view: expected.view, tab: expected.tab, active: expected.active, tabId: 'tab-' + expected.tab },
          state,
        );
      } catch (e) {
        check(
          `旧深链 ${path} → ${expected.view} 的 ${expected.tab} 标签`,
          false,
          { view: expected.view, tab: expected.tab, active: expected.active },
          { error: e.message, runtimeErrors: errors.slice(-5), body: await evalJS('document.body.innerText.slice(0, 200)') },
        );
      }
    };
    await checkAlias('?view=workers', { view: 'runs', tab: 'workers', active: '运行' });
    await checkAlias('?view=traffic', { view: 'evidence', tab: 'traffic', active: '证据' });
    await checkAlias('?view=focus', { view: 'state', tab: 'focus', active: '状态' });

    // ---- 跳转跳回：标签记忆 + Page 滚动位置 ---------------------------------
    const base = 'http://tianwu.test/p/fixture/';
    const clickRail = async (label) => {
      await evalJS(`(() => { const nav = document.querySelector('nav[aria-label="项目导航"]'); const b = [...nav.querySelectorAll('button')].find((x) => x.textContent.trim() === ${JSON.stringify(label)}); if (!b) throw Error('missing rail ' + ${JSON.stringify(label)}); b.click(); })()`);
    };
    await call('Page.navigate', { url: base + '?view=runs&tab=workers' });
    await waitJS('Boolean(document.querySelector(\'[data-testid="tab-workers"][aria-selected="true"]\'))', 'runs/workers tab');
    await clickRail('图谱');
    await waitJS('Boolean(document.querySelector(\'[data-testid="graph-canvas"]\'))', 'graph canvas');
    await clickRail('运行');
    await waitJS('Boolean(document.querySelector(\'[data-testid="view-tabs"]\'))', 'runs tabs after return');
    const rememberedTab = await evalJS(
      '(document.querySelector(\'[data-testid="view-tabs"] [aria-selected="true"]\') || {}).getAttribute ? document.querySelector(\'[data-testid="view-tabs"] [aria-selected="true"]\').getAttribute("data-testid") : null',
    );
    check(
      '跳转离开再回到「运行」保留上次标签（执行者）',
      rememberedTab === 'tab-workers',
      'tab-workers',
      rememberedTab,
    );

    await call('Page.navigate', { url: base + '?view=evidence&tab=evidence' });
    await waitJS('Boolean(document.querySelector(\'[data-testid="page-scroll"]\'))', 'evidence scroll body');
    const scrollInfo = await evalJS(`(() => {
      const el = document.querySelector('[data-testid="page-scroll"]');
      const before = { clientHeight: el.clientHeight, scrollHeight: el.scrollHeight };
      el.scrollTop = 420;
      el.dispatchEvent(new Event('scroll'));
      return { ...before, set: el.scrollTop };
    })()`);
    await pause(250);
    await call('Page.navigate', { url: base + '?view=messages' });
    await waitJS('Boolean(document.querySelector(\'nav[aria-label="项目导航"]\'))', 'messages nav');
    await pause(200);
    await call('Page.navigate', { url: base + '?view=evidence&tab=evidence' });
    await waitJS('Boolean(document.querySelector(\'[data-testid="page-scroll"]\'))', 'evidence scroll body again');
    await pause(400);
    const restored = await evalJS('document.querySelector(\'[data-testid="page-scroll"]\').scrollTop');
    check(
      '跳转离开再回到「证据」恢复滚动位置',
      scrollInfo.scrollHeight > scrollInfo.clientHeight + 40 && restored > 200,
      '返回后 scrollTop > 200（页面可滚动）',
      { ...scrollInfo, restored },
    );
    check('无浏览器运行时异常', errors.length === 0, [], errors);

    const dump = [];
    dump.push('项目侧栏顺序离线 mock 快照（headless Chrome 1440x1000）');
    dump.push('URL: ' + pageURL);
    dump.push('');
    rail.groups.forEach((g) => {
      dump.push(g.label + '（' + g.items.length + '）: ' + g.items.map((i) => i.name).join(' / '));
    });
    fs.writeFileSync(path.join(out, 'nav-order-dom.txt'), dump.join('\n') + '\n', 'utf8');

    const result = {
      pageURL,
      checks,
      passed: checks.every((c) => c.passed),
      screenshots: [navShot],
      groups: rail.groups.map((g) => ({ label: g.label, items: g.items.map((i) => i.name) })),
      active: rail.active,
      domDump: '.build/ui-qa/nav-order-dom.txt',
      runtimeErrors: errors,
    };
    const lines = [
      '页面: ' + pageURL,
      ...checks.map((c) => (c.passed ? 'PASS ' : 'FAIL ') + c.name),
      '',
      ...rail.groups.map((g) => g.label + ': ' + g.items.map((i) => i.name).join(' / ')),
      '',
      '截图:',
      ...result.screenshots.map((s) => '  ' + s.path + ' ' + (s.size ? s.size.width + 'x' + s.size.height : '?') + ' ' + s.bytes + 'B'),
      'DOM: .build/ui-qa/nav-order-dom.txt',
    ];
    process.stdout.write(lines.join('\n') + '\n\n');
    process.stdout.write(JSON.stringify(result, null, 2) + '\n');
    assert.ok(result.passed, '侧栏顺序验收未通过: ' + JSON.stringify(checks.filter((c) => !c.passed)));
  } finally {
    if (ws) ws.close();
    stopChrome(chrome);
  }
})().catch((e) => {
  process.stderr.write(e.stack + '\n');
  process.exitCode = 1;
});
