// Offline browser acceptance for the chat composer in its new home: the 图谱
// page's right dock ([data-graph-chat]), plus the read-only 通信 page. Every HTTP
// response is mocked (fixtures from .build/ui-qa/messages-fixture, static files
// from internal/web/static), so the console is never POSTed to. The only POST in
// this run is the mocked POST /p/fixture/api/chat.
// Run with node scripts/acceptance/chat-ui.cjs; TW_CHROME overrides Chrome.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');

const root = path.resolve(__dirname, '../..');
const out = path.join(root, '.build/ui-qa');
const fixtures = path.join(out, 'messages-fixture');
const staticRoot = path.join(root, 'internal/web');
fs.mkdirSync(out, { recursive: true });

const graphURL = 'http://tianwu.test/p/fixture/?view=graph';
const messagesURL = 'http://tianwu.test/p/fixture/?view=messages';
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
const chatResultAll = {
  to: 'all',
  commander: { message_id: 'm_chat_cmd_1', status: 'queued' },
  delivered: [
    { intent_id: 'in_a484fc1d', participant_id: 'pt_in_a484fc1d', title: '侦察方向', status: 'accepted', message_id: 'm_chat_1' },
  ],
  skipped: [
    { intent_id: 'in_9464dff6', participant_id: 'pt_in_9464dff6', title: '组件方向', status: 'parked', reason: '已结束/停放的方向不会收到' },
  ],
};
const chatResultCommander = {
  to: 'commander',
  commander: { message_id: 'm_chat_cmd_2', status: 'accepted' },
  delivered: [],
  skipped: [],
};
const chatCalls = [];

function mockedResponse(url, request) {
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
  if (url.pathname === '/p/fixture/api/chat' && request.method === 'POST') {
    let body = null;
    try {
      body = JSON.parse(request.postData || '{}');
    } catch {
      body = { parse_error: request.postData };
    }
    chatCalls.push({ method: request.method, path: url.pathname, body });
    const answer = body && body.to === 'all' ? chatResultAll : chatResultCommander;
    return { code: 202, type: 'application/json; charset=utf-8', body: Buffer.from(JSON.stringify(answer)) };
  }
  const fixture = apiRoutes.get(url.pathname);
  if (fixture) {
    return {
      code: 200,
      type: 'application/json; charset=utf-8',
      body: fs.readFileSync(path.join(fixtures, fixture)),
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
          response = mockedResponse(new URL(request.url), request);
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
      throw new Error('Timed out waiting for ' + label + ' / ' + (await evalJS('document.body.innerText')).slice(0, 800));
    };
    const waitText = (text) => waitJS('document.body.innerText.includes(' + JSON.stringify(text) + ')', text);
    const shot = async (name) => {
      const data = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
      const buffer = Buffer.from(data.data, 'base64');
      fs.writeFileSync(path.join(out, name), buffer);
      return { path: '.build/ui-qa/' + name, bytes: buffer.length, size: pngSize(buffer) };
    };
    const readDock = () =>
      evalJS(`(() => {
        const dock = document.querySelector('[data-graph-chat]');
        if (!dock) return null;
        const rect = dock.getBoundingClientRect();
        const receiver = dock.querySelector('[role="group"][aria-label="消息接收方"]');
        const area = dock.querySelector('textarea[aria-label="下发内容"]');
        const receipt = dock.querySelector('[data-chat-receipt]');
        const close = dock.querySelector('button[title="收下方面板"]');
        const send = [...dock.querySelectorAll('button')].find((b) => b.textContent.trim() === '发送');
        return {
          text: dock.innerText,
          rect: { width: Math.round(rect.width), right: Math.round(innerWidth - rect.right), top: Math.round(rect.top) },
          hasTitle: dock.innerText.includes('下发'),
          hasClose: Boolean(close),
          hasRecent: dock.innerText.includes('最近对话'),
          hasOpenConversation: dock.innerText.includes('打开通信页'),
          recentRows: dock.querySelectorAll('p.line-clamp-2').length,
          receivers: receiver ? [...receiver.querySelectorAll('button')].map((b) => b.textContent.trim()) : [],
          hasTextarea: Boolean(area),
          textareaValue: area ? area.value : null,
          textareaMaxLength: area ? area.maxLength : null,
          sendFound: Boolean(send),
          receiptText: receipt ? receipt.innerText : '',
        };
      })()`);
    const clickDockButton = async (label) => {
      const clicked = await evalJS(`(() => {
        const dock = document.querySelector('[data-graph-chat]');
        if (!dock) return false;
        const b = [...dock.querySelectorAll('button')].find((x) => x.textContent.trim() === ${JSON.stringify(label)});
        if (!b) return false;
        b.click();
        return true;
      })()`);
      if (!clicked) throw new Error('dock button not found: ' + label);
    };
    const clickReceiver = async (label) => {
      const clicked = await evalJS(`(() => {
        const g = document.querySelector('[data-graph-chat] [role="group"][aria-label="消息接收方"]');
        const b = g && [...g.querySelectorAll('button')].find((x) => x.textContent.trim() === ${JSON.stringify(label)});
        if (!b) return false;
        b.click();
        return true;
      })()`);
      if (!clicked) throw new Error('receiver not found: ' + label);
    };
    const setTextarea = (value) =>
      evalJS(`(() => {
        const ta = document.querySelector('[data-graph-chat] textarea[aria-label="下发内容"]');
        if (!ta) return null;
        const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set;
        setter.call(ta, ${JSON.stringify(value)});
        ta.dispatchEvent(new Event('input', { bubbles: true }));
        return ta.value;
      })()`);
    const enterSend = () =>
      evalJS(`(() => {
        const ta = document.querySelector('[data-graph-chat] textarea[aria-label="下发内容"]');
        ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', code: 'Enter', bubbles: true, cancelable: true }));
        return true;
      })()`);

    // ---- A1. graph page: dock closed, toolbar button opens it -------------
    await call('Page.navigate', { url: graphURL });
    await waitJS('Boolean(document.querySelector(\'button[title^="下发"]\'))', '右上角下发按钮');
    await pause(400);
    check('初始 [data-graph-chat] 不存在', !(await evalJS('Boolean(document.querySelector("[data-graph-chat]"))')), '不存在', await evalJS('Boolean(document.querySelector("[data-graph-chat]"))'));
    const toggleTitle = await evalJS('document.querySelector(\'button[title^="下发"]\').getAttribute("title")');
    check('右上角有 title 含「下发」的按钮', Boolean(toggleTitle && toggleTitle.includes('下发')), 'title 含「下发」', toggleTitle);
    await evalJS('document.querySelector(\'button[title^="下发"]\').click()');
    await waitJS('Boolean(document.querySelector("[data-graph-chat]"))', '图谱右侧 dock');
    await pause(600);

    const dock = await readDock();
    check('dock 含「下发」「收下方面板」「最近对话」「打开通信页」', Boolean(dock && dock.hasTitle && dock.hasClose && dock.hasRecent && dock.hasOpenConversation), '四个文案齐全', dock && { title: dock.hasTitle, close: dock.hasClose, recent: dock.hasRecent, openConversation: dock.hasOpenConversation });
    check('dock 宽度 340px、贴右', Boolean(dock && dock.rect.width === 340), 'width 340', dock && dock.rect);
    check(
      'dock 含发给/两个接收方/textarea/发送',
      Boolean(dock && dock.receivers.length === 2 && dock.receivers[0] === '仅指挥官' && dock.receivers[1] === '指挥官 + 全体执行者' && dock.hasTextarea && dock.textareaMaxLength === 4000 && dock.sendFound),
      '接收方 2 个 + textarea(4000) + 发送',
      dock && { receivers: dock.receivers, textarea: dock.hasTextarea, maxLength: dock.textareaMaxLength, send: dock.sendFound },
    );
    check('「最近对话」展示最近 6 条', Boolean(dock && dock.recentRows === 6), 6, dock && dock.recentRows);

    // ---- A2. all scope: type + Enter sends --------------------------------
    await clickReceiver('指挥官 + 全体执行者');
    await pause(150);
    const messageAll = '请优先验证 /api/search 的 JSON 接口与 /search 回显是否可复现，并附证据；内网 10.0.0.10 不在授权范围，不要触碰。';
    const typedAll = await setTextarea(messageAll);
    check('输入内容生效（约 60 字，含 URL）', typedAll === messageAll, messageAll, typedAll);
    await enterSend();
    await waitJS('Boolean(document.querySelector("[data-graph-chat] [data-chat-receipt]"))', 'dock 回执 [data-chat-receipt]');
    await pause(400);
    const afterAll = await readDock();
    const sentAll = chatCalls[0] || { body: {} };
    check('a1. 捕获请求 to=all', sentAll.body.to === 'all', 'all', sentAll.body.to);
    check('a2. 捕获请求 text 一致', sentAll.body.text === messageAll, messageAll, sentAll.body.text);
    check('a3. 捕获请求 idempotency_key 非空', typeof sentAll.body.idempotency_key === 'string' && sentAll.body.idempotency_key.length > 0, '非空字符串', sentAll.body.idempotency_key);
    check('Enter 发送后 textarea 清空', afterAll.textareaValue === '', '', afterAll.textareaValue);
    const receiptAll = afterAll.receiptText;
    const allWanted = ['指挥官：排队', 'm_chat_cmd_1', '执行者：1 个已送达', '跳过 1 个已结束/停放方向', '组件方向（停放）'];
    check('a4. 回执含排队/消息id/执行者送达/跳过明细', allWanted.every((s) => receiptAll.includes(s)), '含 ' + JSON.stringify(allWanted), receiptAll.split('\n').slice(0, 6));
    const dockShot = await shot('graph-chat-dock.png');

    // ---- A3. close via 收下方面板, reopen, commander-only send -------------
    const closed = await evalJS(`(() => {
      const b = document.querySelector('[data-graph-chat] button[title="收下方面板"]');
      if (!b) return false;
      b.click();
      return true;
    })()`);
    check('点「收下方面板」按钮', Boolean(closed), '找到并点击', closed);
    await waitJS('!document.querySelector("[data-graph-chat]")', 'dock 关闭');
    check('收起后 [data-graph-chat] 消失', !(await evalJS('Boolean(document.querySelector("[data-graph-chat]"))')), '不存在', await evalJS('Boolean(document.querySelector("[data-graph-chat]"))'));
    await evalJS('document.querySelector(\'button[title^="下发"]\').click()');
    await waitJS('Boolean(document.querySelector("[data-graph-chat]"))', 'dock 重新打开');
    await pause(500);
    await clickReceiver('仅指挥官');
    await pause(150);
    const messageCommander = '收到，先只做只读确认；/api 白名单与 XFF 暂不再试探。';
    await setTextarea(messageCommander);
    await clickDockButton('发送');
    await waitJS('(() => { const r = document.querySelector("[data-graph-chat] [data-chat-receipt]"); return Boolean(r && r.innerText.includes("m_chat_cmd_2")); })()', '仅指挥官回执');
    await pause(300);
    const afterCommander = await readDock();
    const sentCommander = chatCalls[1] || { body: {} };
    check('b1. 捕获请求 to=commander', sentCommander.body.to === 'commander', 'commander', sentCommander.body.to);
    check('b2. 捕获请求 text 一致', sentCommander.body.text === messageCommander, messageCommander, sentCommander.body.text);
    check('b3. 仅指挥官回执含指挥官行和消息 id', afterCommander.receiptText.includes('指挥官：') && afterCommander.receiptText.includes('m_chat_cmd_2'), '指挥官行 + m_chat_cmd_2', afterCommander.receiptText);
    check('b4. 仅指挥官回执没有「执行者：」行', !afterCommander.receiptText.includes('执行者：'), '不含「执行者：」', afterCommander.receiptText);
    check('b5. 两次发送共 2 个 mock POST', chatCalls.filter((c) => c.path === '/p/fixture/api/chat').length === 2, 2, chatCalls.length);

    // ---- A4. the 通信 page is read-only now -------------------------------
    await call('Page.navigate', { url: messagesURL });
    await waitText('参与者');
    await waitJS('document.querySelectorAll("article[data-message-id]").length === 11', '通信页消息行');
    await pause(500);
    const messagesPage = await evalJS(`(() => {
      const tabs = [...document.querySelectorAll('[role="group"][aria-label="通信记录来源"] button')].map((b) => b.textContent.trim());
      return {
        bodyText: document.body.innerText,
        hasComposer: Boolean(document.querySelector('[aria-label="下发内容"]')),
        tabs,
        messageRows: document.querySelectorAll('article[data-message-id]').length,
        nativeCards: document.querySelectorAll('article[data-ioa-seq]').length,
      };
    })()`);
    check('通信页不再有输入框（无 aria-label=下发内容）', !messagesPage.hasComposer, '无 textarea', messagesPage.hasComposer);
    check('通信页说明含「下发入口已移到「图谱」右侧」', messagesPage.bodyText.includes('下发入口已移到「图谱」右侧'), '含提示文案', (messagesPage.bodyText.match(/下发入口已移到[^\n]*/) || [])[0] || '');
    check('通信页两个页签仍在（对话 / 原生 IOA）', messagesPage.tabs.length === 2 && messagesPage.tabs[0].startsWith('对话') && messagesPage.tabs[1] === '原生 IOA', ['对话 11', '原生 IOA'], messagesPage.tabs);
    check('通信页消息行仍渲染（11 条只读）', messagesPage.messageRows === 11, 11, messagesPage.messageRows);
    const messagesShot = await shot('messages-readonly.png');

    check('无浏览器运行时异常', errors.length === 0, [], errors);
    check(
      '两张截图 1440x1000 且非空白',
      [dockShot, messagesShot].every((s) => s.size && s.size.width === 1440 && s.size.height === 1000 && s.bytes > 20000),
      '1440x1000 / > 20KB',
      [dockShot, messagesShot].map((s) => [s.size, s.bytes]),
    );

    // ---- artifacts -------------------------------------------------------
    const dump = [];
    dump.push('图谱右侧「下发」dock 离线 mock 快照（headless Chrome 1440x1000）');
    dump.push('图谱 URL: ' + graphURL);
    dump.push('通信 URL: ' + messagesURL);
    dump.push('dock: width=' + (dock ? dock.rect.width : '?') + 'px, right=' + (dock ? dock.rect.right : '?') + ', top=' + (dock ? dock.rect.top : '?') + '；最近对话 ' + (dock ? dock.recentRows : '?') + ' 条');
    dump.push('dock 文案: ' + (dock ? dock.text.split('\n').slice(0, 3).join(' | ') : '?'));
    dump.push('');
    dump.push('mock 捕获的 POST /api/chat:');
    chatCalls.forEach((c, i) => dump.push('[' + (i + 1) + '] ' + JSON.stringify(c.body)));
    dump.push('');
    dump.push('to=all 回执:');
    receiptAll.split('\n').forEach((l) => dump.push('  ' + l));
    dump.push('');
    dump.push('to=commander 回执:');
    afterCommander.receiptText.split('\n').forEach((l) => dump.push('  ' + l));
    dump.push('');
    dump.push('通信页: 页签=' + messagesPage.tabs.join(' / ') + '；输入框=' + (messagesPage.hasComposer ? '有' : '无') + '；消息行=' + messagesPage.messageRows);
    fs.writeFileSync(path.join(out, 'graph-chat-dom.txt'), dump.join('\n') + '\n', 'utf8');

    const result = {
      graphURL,
      messagesURL,
      checks,
      passed: checks.every((c) => c.passed),
      screenshots: [dockShot, messagesShot],
      chatCalls: chatCalls.map((c) => c.body),
      receiptAll,
      receiptCommander: afterCommander.receiptText,
      dock: dock ? { rect: dock.rect, recentRows: dock.recentRows, receivers: dock.receivers } : null,
      messagesPage: { tabs: messagesPage.tabs, hasComposer: messagesPage.hasComposer, messageRows: messagesPage.messageRows },
      domDump: '.build/ui-qa/graph-chat-dom.txt',
      runtimeErrors: errors,
    };
    const lines = [
      '图谱: ' + graphURL,
      ...checks.map((c) => (c.passed ? 'PASS ' : 'FAIL ') + c.name),
      '',
      '截图:',
      ...result.screenshots.map((s) => '  ' + s.path + ' ' + (s.size ? s.size.width + 'x' + s.size.height : '?') + ' ' + s.bytes + 'B'),
      'DOM: .build/ui-qa/graph-chat-dom.txt',
    ];
    process.stdout.write(lines.join('\n') + '\n\n');
    process.stdout.write(JSON.stringify(result, null, 2) + '\n');
    assert.ok(result.passed, '图谱下发 dock 验收未通过: ' + JSON.stringify(checks.filter((c) => !c.passed)));
  } finally {
    if (ws) ws.close();
    stopChrome(chrome);
  }
})().catch((e) => {
  process.stderr.write(e.stack + '\n');
  process.exitCode = 1;
});
