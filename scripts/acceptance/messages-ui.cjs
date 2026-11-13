// Offline browser acceptance for the 通信 page (?view=messages) AFTER the display
// fix: every HTTP response is mocked (fixtures from .build/ui-qa/messages-fixture,
// static files from internal/web/static), so no console, model or live data is
// touched. The run asserts the final DOM contract and writes the messages-after-*
// screenshots under .build/ui-qa.
//
// The pre-fix baseline (messages-before-native.png / messages-before-project.png,
// messages-before-dom.txt) was produced by the same harness against the previous
// build; this script only writes the after artifacts, so those stay intact.
// Run with node scripts/acceptance/messages-ui.cjs; TW_CHROME overrides Chrome.
// A live-data shot is opt-in: start `node scripts/acceptance/preview-proxy.mjs`
// (serves internal/web/static, proxies to the running console) and set
// TW_PREVIEW_URL=http://127.0.0.1:5173; TW_PREVIEW_PROJECT names the project
// route for that shot (default `fixture`). Without it the run stays fully offline.
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

/** Kill Chrome together with its helper processes: on Windows the crashpad and
 *  utility children outlive the main process, so plain kill() leaks them. */
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

// ---- mocked HTTP -----------------------------------------------------------
// The payloads were captured from a live console, and every occurrence of its
// project id was rewritten to `fixture`; the mock serves the fixture project
// route verbatim, and the page itself asks for /p/fixture/.
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
  // Other polls of the shell (health, operator requests, profiles) are not part
  // of this page's contract; an empty object keeps them from failing loudly.
  if (url.pathname.includes('/api/')) {
    return { code: 200, type: 'application/json; charset=utf-8', body: Buffer.from('{}') };
  }
  // Everything else is the console shell itself (client-side routing).
  return {
    code: 200,
    type: 'text/html; charset=utf-8',
    body: fs.readFileSync(path.join(staticRoot, 'static/index.html')),
  };
}

/** Width/height straight from the PNG IHDR, so a wrong/blank shot is caught. */
function pngSize(buffer) {
  if (buffer.length < 24 || buffer.toString('ascii', 1, 4) !== 'PNG') return null;
  return { width: buffer.readUInt32BE(16), height: buffer.readUInt32BE(20) };
}

const count = (text, re) => (text.match(re) || []).length;

/** Expected labels are read from the mocked fixtures, so this script never
 *  embeds the captured project's own titles. */
const fixtureLabels = (() => {
  const read = (name) => {
    try {
      return JSON.parse(fs.readFileSync(path.join(fixtures, name), 'utf8'));
    } catch {
      return {};
    }
  };
  const ioa = read('ioa.json');
  const participants = read('participants.json');
  const sessionTitles = ioa.sessions || {};
  const byID = new Map((participants.participants || []).map((p) => [p.id, p.title || '']));
  return {
    session: (id) => sessionTitles[id] || '',
    participant: (id) => byID.get(id) || '',
  };
})();

/**
 * Live-data shot through the preview proxy (.build/preview.mjs): it serves the
 * new build and forwards /api, /p/<id>/api and /login to the running console, so
 * this is the one place that reads real data. It performs a single POST /login
 * (the proxy's auth endpoint, not an application command) to obtain the console
 * cookie, then only lets the page issue its own read-only GETs and screenshots
 * the result. Nothing is clicked and nothing is written.
 */
async function verifyLivePreview() {
  const previewURL = process.env.TW_PREVIEW_URL || '';
  const liveProject = process.env.TW_PREVIEW_PROJECT || 'fixture';
  const result = { skipped: true, previewURL, login: null, snapshot: null, screenshot: null, checks: [], runtimeErrors: [], error: '' };
  if (!previewURL) return result;
  const check = (name, passed, expected, actual) => result.checks.push({ name, passed: Boolean(passed), expected, actual });
  const tokenFile = path.join(root, 'data/console.token');
  if (!fs.existsSync(tokenFile)) {
    result.error = 'missing data/console.token';
    return result;
  }
  const token = fs.readFileSync(tokenFile, 'utf8').trim();
  const profile = fs.mkdtempSync(path.join(out, 'chrome-live-'));
  const proc = spawn(
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
  let ws;
  try {
    let login;
    try {
      login = await fetch(previewURL + '/login', {
        method: 'POST',
        redirect: 'manual',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: 'token=' + encodeURIComponent(token),
      });
    } catch (e) {
      result.error = 'POST /login failed: ' + e.message;
      return result;
    }
    const rawCookies =
      typeof login.headers.getSetCookie === 'function'
        ? login.headers.getSetCookie()
        : [login.headers.get('set-cookie')].filter(Boolean);
    const pairs = rawCookies.map((c) => c.split(';')[0]);
    // The console names its session cookie tw_session (cmd/tw/auth.go). Accept
    // whichever cookie the proxy forwards and prefer that one, so a rename does
    // not break the live shot.
    const pair = pairs.find((p) => p.startsWith('tw_session=')) || pairs.find((p) => p.includes('=')) || '';
    const cookieName = pair.slice(0, pair.indexOf('='));
    const cookieValue = pair.slice(pair.indexOf('=') + 1);
    result.login = {
      status: login.status,
      location: login.headers.get('location'),
      gotCookie: Boolean(pair),
      cookie: cookieName,
      cookies: rawCookies.map((c) => c.split('=')[0]),
    };
    if (!pair) {
      result.error = 'login returned no console cookie (status ' + login.status + ')';
      return result;
    }

    const portFile = path.join(profile, 'DevToolsActivePort');
    for (let n = 0; n < 150 && !fs.existsSync(portFile); n++) await pause(100);
    if (!fs.existsSync(portFile)) throw new Error('live chrome did not open a DevTools port');
    const port = Number(fs.readFileSync(portFile, 'utf8').split('\n')[0]);
    const pages = await (await fetch('http://127.0.0.1:' + port + '/json/list')).json();
    ws = new WebSocket(pages.find((x) => x.type === 'page').webSocketDebuggerUrl);
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
    ws.addEventListener('message', (evt) => {
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
        result.runtimeErrors.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
      }
    });
    const evalJS = async (expression) =>
      (await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })).result.value;
    const waitJS = async (expression, label) => {
      for (let i = 0; i < 240; i++) {
        if (await evalJS(expression)) return;
        await pause(100);
      }
      throw new Error('Timed out waiting for ' + label + ' / ' + (await evalJS('document.body.innerText')).slice(0, 500));
    };

    await call('Page.enable');
    await call('Runtime.enable');
    await call('Network.enable');
    const set = await call('Network.setCookie', { name: cookieName, value: cookieValue, domain: '127.0.0.1', path: '/', httpOnly: true, sameSite: 'Lax' });
    let cookieOk = Boolean(set && set.success);
    if (!cookieOk) {
      const retry = await call('Network.setCookie', { name: cookieName, value: cookieValue, url: previewURL + '/' });
      cookieOk = Boolean(retry && retry.success);
    }
    result.login.cookieSet = cookieOk;
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
    await call('Page.navigate', { url: previewURL + '/p/' + liveProject + '/?view=messages' });
    await waitJS('document.body.innerText.includes("原生 IOA 通信")', 'live 原生 IOA 通信');
    await waitJS('document.querySelectorAll("article[data-ioa-seq]").length >= 1', 'live IOA 卡片');
    await pause(1800);

    const snapshot = await evalJS(`(() => {
      const bodyText = document.body.innerText;
      const el = document.querySelector('article[data-ioa-seq="1"]') || document.querySelector('article[data-ioa-seq]');
      const text = (node, selector) => { const n = node && node.querySelector(selector); return n ? n.innerText : null; };
      return {
        bodyText,
        cardCount: document.querySelectorAll('article[data-ioa-seq]').length,
        passwordInputs: document.querySelectorAll('input[type="password"]').length,
        formCount: document.querySelectorAll('form').length,
        firstCard: el
          ? {
              seq: el.getAttribute('data-ioa-seq'),
              source: text(el, '[data-ioa-endpoint="source"]'),
              target: text(el, '[data-ioa-endpoint="target"]'),
              sourceNote: text(el, '[data-ioa-source-note]'),
              targetNote: text(el, '[data-ioa-target-note]'),
              body: text(el, '[data-ioa-body]'),
            }
          : null,
      };
    })()`);
    result.snapshot = {
      cardCount: snapshot.cardCount,
      passwordInputs: snapshot.passwordInputs,
      formCount: snapshot.formCount,
      firstCard: snapshot.firstCard,
    };
    const data = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
    const buffer = Buffer.from(data.data, 'base64');
    fs.writeFileSync(path.join(out, 'messages-live-preview.png'), buffer);
    result.screenshot = { path: '.build/ui-qa/messages-live-preview.png', bytes: buffer.length, size: pngSize(buffer) };

    check('已登录（无密码框/无登录表单）', snapshot.passwordInputs === 0 && snapshot.formCount === 0 && !snapshot.bodyText.includes('令牌'), '0 密码框 / 0 表单 / 无「令牌」', [snapshot.passwordInputs, snapshot.formCount, snapshot.bodyText.includes('令牌')]);
    check('页面含「原生 IOA 通信」', snapshot.bodyText.includes('原生 IOA 通信'), '含「原生 IOA 通信」', snapshot.bodyText.includes('原生 IOA 通信'));
    check('原生 IOA 卡片 ≥ 1', snapshot.cardCount >= 1, '>= 1', snapshot.cardCount);
    check('正文不含「未提供地址」', !snapshot.bodyText.includes('未提供地址'), '不含「未提供地址」', snapshot.bodyText.includes('未提供地址'));
    check('正文不含原始 [collab] 头部', !snapshot.bodyText.includes('[collab] from='), '不含 "[collab] from="', count(snapshot.bodyText, /\[collab\] from=/g));
    check('无浏览器运行时异常', result.runtimeErrors.length === 0, [], result.runtimeErrors);
    check(
      '截图 1440x1000 且非空白',
      Boolean(result.screenshot.size && result.screenshot.size.width === 1440 && result.screenshot.size.height === 1000 && result.screenshot.bytes > 20000),
      '1440x1000 / > 20KB',
      [result.screenshot.size, result.screenshot.bytes],
    );
    return result;
  } catch (e) {
    result.error = e.message;
    return result;
  } finally {
    if (ws) ws.close();
    stopChrome(proc);
  }
}

(async () => {
  let ws;
  try {
    const portFile = path.join(profile, 'DevToolsActivePort');
    for (let n = 0; n < 150 && !fs.existsSync(portFile); n++) await pause(100);
    if (!fs.existsSync(portFile)) throw new Error('Chrome did not open a DevTools port: ' + portFile);
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
    await call('Emulation.setDeviceMetricsOverride', {
      width: 1440,
      height: 1000,
      deviceScaleFactor: 1,
      mobile: false,
    });

    const evalJS = async (expression) =>
      (await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })).result.value;
    const waitJS = async (expression, label) => {
      for (let i = 0; i < 120; i++) {
        if (await evalJS(expression)) return;
        await pause(100);
      }
      throw new Error('Timed out waiting for ' + label + ' / ' + (await evalJS('document.body.innerText')).slice(0, 800));
    };
    const waitText = (text) => waitJS('document.body.innerText.includes(' + JSON.stringify(text) + ')', text);
    const clickText = async (text) => {
      const clicked = await evalJS(
        '(() => { const b = [...document.querySelectorAll("button")].find(b => b.textContent.trim().startsWith(' +
          JSON.stringify(text) +
          ')); if (!b) return false; b.click(); return true })()',
      );
      if (!clicked) throw new Error('Button not found: ' + text);
    };
    const clickSelector = async (selector) => {
      const clicked = await evalJS(
        '(() => { const el = document.querySelector(' + JSON.stringify(selector) + '); if (!el) return false; el.click(); return true })()',
      );
      if (!clicked) throw new Error('Selector not found: ' + selector);
    };
    const clickSummary = async (text) => {
      const clicked = await evalJS(
        '(() => { const s = [...document.querySelectorAll("summary")].find(s => s.textContent.includes(' +
          JSON.stringify(text) +
          ')); if (!s) return false; s.click(); return true })()',
      );
      if (!clicked) throw new Error('Summary not found: ' + text);
    };
    const shot = async (name) => {
      const data = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
      const buffer = Buffer.from(data.data, 'base64');
      fs.writeFileSync(path.join(out, name), buffer);
      return { path: '.build/ui-qa/' + name, bytes: buffer.length, size: pngSize(buffer) };
    };
    /** One native card, read through the final data-* contract. */
    const readCard = (recordSeq) =>
      evalJS(`(() => {
        const el = document.querySelector('article[data-ioa-seq="${recordSeq}"]');
        if (!el) return null;
        const text = (selector) => { const node = el.querySelector(selector); return node ? node.innerText : null; };
        return {
          seq: el.getAttribute('data-ioa-seq'),
          event: el.getAttribute('data-ioa-event'),
          source: text('[data-ioa-endpoint="source"]'),
          target: text('[data-ioa-endpoint="target"]'),
          sourceNote: text('[data-ioa-source-note]'),
          targetNote: text('[data-ioa-target-note]'),
          body: text('[data-ioa-body]'),
          text: el.innerText,
        };
      })()`);
    const explainerText = (summary) =>
      evalJS(`(() => {
        const el = [...document.querySelectorAll('details')].find((d) => d.innerText.includes(${JSON.stringify(summary)}));
        return el ? el.innerText : '';
      })()`);

    const checks = [];
    const check = (name, passed, expected, actual) =>
      checks.push({ name, passed: Boolean(passed), expected, actual });

    // ---- render the 通信 page as it is built right now ---------------------
    // The read-only 对话 tab is the default since the composer moved to the
    // graph dock; the native IOA cards live behind the 原生 IOA tab.
    await call('Page.navigate', { url: pageURL });
    await waitText('参与者');
    await clickText('原生 IOA');
    await waitJS('document.querySelectorAll("article[data-ioa-seq]").length === 18', '18 条原生 IOA 卡片');
    await pause(1500); // let the 4s/8s polls settle on their first response

    const nativeCount = await evalJS('document.querySelectorAll("article[data-ioa-seq]").length');
    const initialText = await evalJS('document.body.innerText');
    const card1 = await readCard(1);
    const card6 = await readCard(6);
    const card9 = await readCard(9);
    // Capture the native cards while the native tab is the one on screen (the
    // dump below is written after switching tabs, so each side is collected at
    // its own moment).
    const nativeDump = await evalJS(`(() => [...document.querySelectorAll('article[data-ioa-seq]')].map((el) => {
      const text = (selector) => { const node = el.querySelector(selector); return node ? node.innerText : ''; };
      return [
        el.getAttribute('data-ioa-seq'),
        text('[data-ioa-endpoint="source"]') + ' → ' + text('[data-ioa-endpoint="target"]'),
        text('[data-ioa-body]').split('\\n')[0].slice(0, 70),
      ];
    }))()`);

    // Native tab in its default (details collapsed) state: comparison shot.
    const nativeShot = await shot('messages-after-native.png');

    // ---- 1. the raw prelude and the placeholder are gone from the body ----
    check('正文不含原始 [collab] 头部', !initialText.includes('[collab] from='), '不含 "[collab] from="', count(initialText, /\[collab\] from=/g));
    check('正文不含「未提供地址」', !initialText.includes('未提供地址'), '不含「未提供地址」', count(initialText, /未提供地址/g));

    // ---- 2. one card per IOA record ---------------------------------------
    check('原生 IOA 卡片数 = 18', nativeCount === 18, 18, nativeCount);

    // ---- 3. the first commander message -----------------------------------
    // The first record targets a session id; its readable label comes from the
    // mocked session map (a fixture), never from a title hard-coded here.
    const surveyTitle = fixtureLabels.session('9da4e9bb76501b6b364c7345664e6c5a');
    check('seq=1 来源是「指挥官」', Boolean(card1 && card1.source && card1.source.includes('指挥官')), '含「指挥官」', card1 && card1.source);
    check('seq=1 目标是完整会话标题', Boolean(card1 && surveyTitle && card1.target === surveyTitle), surveyTitle, card1 && card1.target);
    check('seq=1 来源说明含「控制台直投」', Boolean(card1 && card1.sourceNote && card1.sourceNote.includes('控制台直投')), '含「控制台直投」', card1 && card1.sourceNote);
    check('seq=1 目标说明含「会话 9da4e9bb76」', Boolean(card1 && card1.targetNote && card1.targetNote.includes('会话 9da4e9bb76')), '含「会话 9da4e9bb76」', card1 && card1.targetNote);
    check('seq=1 正文以「指挥官现场提示」开头', Boolean(card1 && card1.body && card1.body.trimStart().startsWith('指挥官现场提示')), '以「指挥官现场提示」开头', card1 && card1.body && card1.body.slice(0, 40));
    check('seq=1 正文不含 message_id=', Boolean(card1 && card1.body && !card1.body.includes('message_id=')), '不含 message_id=', card1 && card1.body && count(card1.body, /message_id=/g));
    check('seq=1 正文不含消息编号 m_20f911f4', Boolean(card1 && card1.body && !card1.body.includes('m_20f911f4')), '不含 m_20f911f4', card1 && card1.body && card1.body.includes('m_20f911f4'));
    check('seq=1 卡片元数据仍显示消息编号', Boolean(card1 && card1.text.includes('m_20f911f4')), '卡片文本含 m_20f911f4', card1 && card1.text.includes('m_20f911f4'));

    // ---- 4. a worker reply comes back to the control plane ----------------
    check('seq=9 目标是「控制台」', Boolean(card9 && card9.target === '控制台'), '控制台', card9 && card9.target);
    check('seq=9 目标说明含「控制台节点 cd3ecb7f65」', Boolean(card9 && card9.targetNote && card9.targetNote.includes('控制台节点 cd3ecb7f65')), '含「控制台节点 cd3ecb7f65」', card9 && card9.targetNote);

    // ---- 5. a participant-addressed record resolves to its title ----------
    const peerTitle = fixtureLabels.participant('pt_in_fb5e6d72');
    check('seq=6 目标是参与者可读标题', Boolean(card6 && peerTitle && card6.target && card6.target.includes(peerTitle)), '含「' + peerTitle + '」', card6 && card6.target);

    // ---- 6. per-record details expand to the raw ids + sender ------------
    const detailsSummary = await evalJS(`document.querySelector('article[data-ioa-seq="1"] [data-ioa-details] summary')?.innerText ?? null`);
    check('记录详情 summary 文案', detailsSummary === '记录详情（原始 id 与时间）', '记录详情（原始 id 与时间）', detailsSummary);
    await clickSelector('article[data-ioa-seq="1"] [data-ioa-details] summary');
    await pause(150);
    const detailsText = await evalJS(`document.querySelector('article[data-ioa-seq="1"] [data-ioa-details]')?.innerText ?? ''`);
    check('展开详情含 IOA 消息 id', detailsText.includes('575284f6c09bcc91ce10a4d9c292fc26'), '含 575284f6c09bcc91ce10a4d9c292fc26', detailsText.split('\n').slice(0, 6));
    check('展开详情含「发送节点」', detailsText.includes('发送节点'), '含「发送节点」', detailsText.includes('发送节点'));
    check('记录详情标签为「事件 id」', detailsText.includes('事件 id'), '含「事件 id」', detailsText.split('\n').slice(0, 8));

    // ---- 7. the "how to read" explainer ----------------------------------
    await clickSummary('这些记录怎么读？');
    await pause(150);
    const nativeExplainer = await explainerText('这些记录怎么读？');
    check('说明区块含「控制台直投」', nativeExplainer.includes('控制台直投'), '含「控制台直投」', nativeExplainer.slice(0, 80));
    check('说明区块含「会话短串」', nativeExplainer.includes('会话短串'), '含「会话短串」', nativeExplainer.includes('会话短串'));
    check('说明区块含「不是」', nativeExplainer.includes('不是'), '含「不是」', nativeExplainer.includes('不是'));

    // ---- 8. project-message tab ------------------------------------------
    // The tab label is 对话 {N} now (the 项目消息 wording is gone).
    await clickText('对话');
    await waitJS('document.querySelectorAll("article[data-message-id]").length === 11', '11 条对话消息');
    await waitText('投递状态怎么读？');
    await pause(500);
    const projectShot = await shot('messages-after-project.png');
    const project = await evalJS(`(() => {
      const rows = [...document.querySelectorAll('article[data-message-id]')];
      const first = rows[0];
      return {
        count: rows.length,
        firstText: first ? first.innerText : '',
        // Badge is a div, not a span: read every leaf element's text + title so
        // the short label and its full explanation are both checked.
        firstBadges: first
          ? [...first.querySelectorAll('div, span')]
              .filter((el) => el.children.length === 0)
              .map((el) => ({ text: el.textContent.trim(), title: el.getAttribute('title') }))
              .filter((b) => b.text)
          : [],
        // Direction lines per row: [from, →, to] as actually rendered, so a raw
        // id in the direction is caught without scanning the whole page (the
        // explainer text may mention ids legitimately).
        directions: rows.map((el) => ({
          id: el.getAttribute('data-message-id'),
          lines: el.innerText.split('\\n').map((s) => s.trim()).filter(Boolean).slice(0, 3),
        })),
        bodyText: document.body.innerText,
      };
    })()`);
    const projectDump = await evalJS(`(() => [...document.querySelectorAll('article[data-message-id]')].map((el) => [
      el.getAttribute('data-message-id'),
      el.innerText.split('\\n').slice(0, 3).join(' | ').slice(0, 160),
    ]))()`);
    check('对话 article[data-message-id] 数 = 11', project.count === 11, 11, project.count);
    check('第一条消息来源是「指挥官」', project.firstText.includes('指挥官'), '含「指挥官」', project.firstText.split('\n').slice(0, 4));
    // The project-message target is a participant id; its readable label also
    // comes from the mocked participant list.
    const projectTarget = fixtureLabels.participant('pt_in_a484fc1d');
    check('第一条消息目标是可读标题', Boolean(projectTarget) && project.firstText.includes(projectTarget), '含「' + projectTarget + '」', project.firstText.includes(projectTarget));
    check(
      '第一条消息状态 Badge 为「已接收」',
      project.firstBadges.some((b) => b.text === '已接收' && b.title === '宿主已接收（未确认已读）'),
      'text=已接收 / title=宿主已接收（未确认已读）',
      project.firstBadges.filter((b) => ['已接收', '已确认', '排队', '投递中', '结果未知'].includes(b.text)),
    );
    check('第一条消息含消息编号 m_20f911f4', project.firstText.includes('m_20f911f4'), '含 m_20f911f4', project.firstText.includes('m_20f911f4'));
    check('「现场发言」出现 11 次', count(project.bodyText, /现场发言/g) === 11, 11, count(project.bodyText, /现场发言/g));
    // The operator message must read 操作员 in BOTH tabs; a bare `operator` in a
    // direction line is the regression this catches (whole-page innerText is not
    // a valid probe here because the explainer may name ids deliberately).
    const operatorRow = project.directions.find((d) => d.id === 'm_221ec659');
    check('m_221ec659 方向显示「操作员」', Boolean(operatorRow && operatorRow.lines[0] === '操作员'), '方向首行「操作员」', operatorRow && operatorRow.lines);
    check('项目消息方向行不再出现裸 operator', project.directions.every((d) => d.lines[0] !== 'operator'), '所有行首行 ≠ operator', project.directions.filter((d) => d.lines[0] === 'operator'));

    // ---- 9. delivery-state explainer -------------------------------------
    await clickSummary('投递状态怎么读？');
    await pause(150);
    const projectExplainer = await explainerText('投递状态怎么读？');
    check('投递状态说明含「宿主收下了」', projectExplainer.includes('宿主收下了'), '含「宿主收下了」', projectExplainer.includes('宿主收下了'));
    check('投递状态说明含「不要自动重发」', projectExplainer.includes('不要自动重发'), '含「不要自动重发」', projectExplainer.includes('不要自动重发'));

    // ---- 900px project messages tab --------------------------------------
    await call('Emulation.setDeviceMetricsOverride', { width: 900, height: 1000, deviceScaleFactor: 1, mobile: false });
    await pause(600);
    const projectCompact = await evalJS(`(() => {
      const rows = [...document.querySelectorAll('article[data-message-id]')];
      const first = rows[0];
      return {
        width: innerWidth,
        scrollWidth: document.documentElement.scrollWidth,
        rowCount: rows.length,
        firstDirection: first ? first.innerText.split('\\n').map((s) => s.trim()).filter(Boolean).slice(0, 3) : [],
      };
    })()`);
    const projectCompactShot = await shot('messages-after-project-compact.png');
    check('900px 对话无横向溢出', projectCompact.scrollWidth <= projectCompact.width + 1, '<= 901', [projectCompact.width, projectCompact.scrollWidth]);
    check('900px 对话仍为 11 条', projectCompact.rowCount === 11, 11, projectCompact.rowCount);
    check('900px 对话首行方向可读', projectCompact.firstDirection[0] === '指挥官', '指挥官', projectCompact.firstDirection);

    // ---- compact viewport: the cards must still be readable at 900px ------
    await clickText('原生 IOA');
    await waitJS('document.querySelectorAll("article[data-ioa-seq]").length === 18', '窄屏前的原生卡片');
    await call('Emulation.setDeviceMetricsOverride', { width: 900, height: 1000, deviceScaleFactor: 1, mobile: false });
    await pause(600);
    const compact = await evalJS(`(() => {
      const card = document.querySelector('article[data-ioa-seq="1"]');
      const body = card ? card.querySelector('[data-ioa-body]') : null;
      const rect = body ? body.getBoundingClientRect() : null;
      return {
        width: innerWidth,
        scrollWidth: document.documentElement.scrollWidth,
        cardCount: document.querySelectorAll('article[data-ioa-seq]').length,
        bodyWidth: rect ? Math.round(rect.width) : 0,
        bodyHeight: rect ? Math.round(rect.height) : 0,
        bodyText: body ? body.innerText.slice(0, 40) : '',
      };
    })()`);
    const compactShot = await shot('messages-after-native-compact.png');
    check('900px 下无横向溢出', compact.scrollWidth <= compact.width + 1, '<= 901', [compact.width, compact.scrollWidth]);
    check('900px 下卡片仍为 18 条', compact.cardCount === 18, 18, compact.cardCount);
    check('900px 下正文块可读', compact.bodyWidth > 240 && compact.bodyHeight > 20, '> 240px 宽 / 20px 高', [compact.bodyWidth, compact.bodyHeight]);

    // ---- live console through the preview proxy (real data, read-only) ----
    // Opt-in: scripts/acceptance/preview-proxy.mjs serves the new build and
    // proxies to the running 8080; this proves the new UI works against real
    // data, not only the fixtures. Unset TW_PREVIEW_URL keeps the run offline.
    const live = await verifyLivePreview();
    live.checks.forEach((c) => check('活数据：' + c.name, c.passed, c.expected, c.actual));
    if (live.error) check('活数据：预览页可登录并截图', false, '登录并渲染出原生 IOA 卡片', live.error);

    // ---- artifacts -------------------------------------------------------
    const dump = [];
    dump.push('通信页修改后 DOM 快照（headless Chrome 1440x1000，离线 mock，无控制台/模型）');
    dump.push('URL: ' + pageURL);
    dump.push('数据: .build/ui-qa/messages-fixture（从运行中的控制台只读抓取，项目 id 已改为 fixture）');
    dump.push('');
    dump.push('== 原生 IOA 页签 ==');
    dump.push('article[data-ioa-seq] 数: ' + nativeCount);
    dump.push('[seq] 来源 → 目标 | 正文首行（截断 70 字）:');
    nativeDump.forEach((row) => dump.push('[' + row[0] + '] ' + row[1] + ' | ' + row[2]));
    dump.push('');
    dump.push('== 对话页签 ==');
    dump.push('article[data-message-id] 数: ' + project.count);
    dump.push('[data-message-id] 头部 3 行:');
    projectDump.forEach((row) => dump.push('[' + row[0] + '] ' + row[1]));
    fs.writeFileSync(path.join(out, 'messages-after-dom.txt'), dump.join('\n') + '\n', 'utf8');

    // ---- runtime exceptions ---------------------------------------------
    check('无浏览器运行时异常', errors.length === 0, [], errors);

    const screenshots = [nativeShot, projectShot, projectCompactShot, compactShot];
    check(
      '四张截图为 1440/1440/900/900 x 1000',
      screenshots.every((s, i) => s.size && s.size.height === 1000 && s.size.width === (i < 2 ? 1440 : 900)),
      '[1440x1000, 1440x1000, 900x1000, 900x1000]',
      screenshots.map((s) => s.size),
    );
    check('四张截图非空白（> 20KB）', screenshots.every((s) => s.bytes > 20000), '> 20480 字节', screenshots.map((s) => s.bytes));
    const allScreenshots = [...screenshots, ...(live.screenshot ? [live.screenshot] : [])];

    const result = {
      pageURL,
      checks,
      passed: checks.every((c) => c.passed),
      screenshots: allScreenshots,
      live,
      after: {
        native: { articleCount: nativeCount, card1, card6, card9 },
        project: { articleCount: project.count, firstMessage: project.firstText.split('\n').slice(0, 5), deliveryBadges: project.firstBadges.map((b) => b.text).slice(0, 8), operatorDirection: operatorRow ? operatorRow.lines : null },
        projectCompact,
        compact,
      },
      domDump: '.build/ui-qa/messages-after-dom.txt',
      runtimeErrors: errors,
    };

    const lines = [
      '页面: ' + pageURL,
      ...checks.map((c) => (c.passed ? 'PASS ' : 'FAIL ') + c.name),
      '',
      '活数据预览: ' + (live.skipped ? 'SKIP（设置 TW_PREVIEW_URL 后运行）' : live.error ? 'FAIL ' + live.error : 'PASS ' + live.previewURL + ' · 卡片 ' + live.snapshot.cardCount + ' 张'),
      '截图:',
      ...allScreenshots.map((s) => '  ' + s.path + ' ' + (s.size ? s.size.width + 'x' + s.size.height : '?') + ' ' + s.bytes + 'B'),
      'DOM: .build/ui-qa/messages-after-dom.txt',
    ];
    process.stdout.write(lines.join('\n') + '\n\n');
    process.stdout.write(JSON.stringify(result, null, 2) + '\n');
    assert.ok(result.passed, '通信页验收未通过: ' + JSON.stringify(checks.filter((c) => !c.passed)));
  } finally {
    if (ws) ws.close();
    stopChrome(chrome);
  }
})().catch((e) => {
  process.stderr.write(e.stack + '\n');
  process.exitCode = 1;
});
