// Live UI acceptance for the 运行 page (?view=runs) against the preview proxy
// (.build/preview.mjs serves the current build and proxies /api, /p/<id>/api and
// /login to the running 8080 console). Read-only: one POST /login for the
// console cookie, then the page's own GETs. The only clicks are the left-column
// group rows (pure front-end selection); pause/resume/rerun are never clicked.
// Run with node scripts/acceptance/runs-ui.cjs; TW_PREVIEW_URL / TW_PROJECT /
// TW_CHROME override the preview origin, the project route and Chrome.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');

const root = path.resolve(__dirname, '../..');
const out = path.join(root, '.build/ui-qa');
fs.mkdirSync(out, { recursive: true });
const previewURL = (process.env.TW_PREVIEW_URL || 'http://127.0.0.1:5173').replace(/\/$/, '');
// No project id is committed here; name the live project via TW_PROJECT.
const projectID = process.env.TW_PROJECT || 'fixture';
const pageURL = previewURL + '/p/' + projectID + '/?view=runs';
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

function pngSize(buffer) {
  if (buffer.length < 24 || buffer.toString('ascii', 1, 4) !== 'PNG') return null;
  return { width: buffer.readUInt32BE(16), height: buffer.readUInt32BE(20) };
}

(async () => {
  const checks = [];
  const check = (name, passed, expected, actual) =>
    checks.push({ name, passed: Boolean(passed), expected, actual });

  const tokenFile = path.join(root, 'data/console.token');
  if (!fs.existsSync(tokenFile)) throw new Error('missing data/console.token');
  const token = fs.readFileSync(tokenFile, 'utf8').trim();

  // One login POST through the proxy; the console names its session cookie
  // tw_session (cmd/tw/auth.go) and sets it with Path=/; HttpOnly.
  const login = await fetch(previewURL + '/login', {
    method: 'POST',
    redirect: 'manual',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: 'token=' + encodeURIComponent(token),
  });
  const rawCookies =
    typeof login.headers.getSetCookie === 'function'
      ? login.headers.getSetCookie()
      : [login.headers.get('set-cookie')].filter(Boolean);
  const pairs = rawCookies.map((c) => c.split(';')[0]);
  const pair = pairs.find((p) => p.startsWith('tw_session=')) || pairs.find((p) => p.includes('=')) || '';
  if (!pair) throw new Error('login returned no console cookie (status ' + login.status + ')');
  const cookieName = pair.slice(0, pair.indexOf('='));
  const cookieValue = pair.slice(pair.indexOf('=') + 1);

  const profile = fs.mkdtempSync(path.join(out, 'chrome-runs-'));
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

  let ws;
  try {
    const portFile = path.join(profile, 'DevToolsActivePort');
    for (let n = 0; n < 150 && !fs.existsSync(portFile); n++) await pause(100);
    if (!fs.existsSync(portFile)) throw new Error('chrome did not open a DevTools port');
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
        errors.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
      }
    });
    const evalJS = async (expression) =>
      (await call('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })).result.value;
    const waitJS = async (expression, label) => {
      for (let i = 0; i < 240; i++) {
        if (await evalJS(expression)) return;
        await pause(100);
      }
      throw new Error('Timed out waiting for ' + label + ' / ' + (await evalJS('document.body.innerText')).slice(0, 600));
    };
    const shot = async (name) => {
      const data = await call('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false });
      const buffer = Buffer.from(data.data, 'base64');
      fs.writeFileSync(path.join(out, name), buffer);
      return { path: '.build/ui-qa/' + name, bytes: buffer.length, size: pngSize(buffer) };
    };

    await call('Page.enable');
    await call('Runtime.enable');
    await call('Network.enable');
    const set = await call('Network.setCookie', { name: cookieName, value: cookieValue, domain: '127.0.0.1', path: '/', httpOnly: true, sameSite: 'Lax' });
    const cookieOk = Boolean(set && set.success);
    await call('Emulation.setDeviceMetricsOverride', { width: 1440, height: 1000, deviceScaleFactor: 1, mobile: false });
    await call('Page.navigate', { url: pageURL });

    // The left panel's grouping header is the marker that the new view is up.
    await waitJS('document.body.innerText.includes("执行者与角色")', '执行者与角色');
    await waitJS('document.body.innerText.includes("次运行")', '次运行');
    await waitJS(
      `(() => { const p = [...document.querySelectorAll('section.tw-panel')].find((s) => s.innerText.includes('执行者与角色')); return Boolean(p && p.querySelectorAll('button').length); })()`,
      '左列分组行',
    );
    // The project is paused by the auto-pause rule; the header control must show 继续项目.
    await waitJS(
      `[...document.querySelectorAll('button')].some((b) => ['继续项目', '暂停项目'].includes(b.textContent.trim()))`,
      '项目控制按钮',
    );
    await pause(1800); // runs(4s)/intents(6s)/participants(8s)/board(5s) settle

    // Left rows are <button>s inside the 执行者与角色 panel; capture the three
    // lines that matter for an operator (direction / identity / latest run).
    const snap = await evalJS(`(() => {
      const panels = [...document.querySelectorAll('section.tw-panel')];
      const left = panels.find((s) => s.innerText.includes('执行者与角色')) || null;
      const right = panels.find((s) => s.innerText.includes('事件流')) || null;
      const linesOf = (el) => el.innerText.split('\\n').map((s) => s.trim()).filter(Boolean);
      const rows = left ? [...left.querySelectorAll('button')].map((b) => {
        const lines = linesOf(b);
        return {
          lines,
          text: lines.join(' '),
          direction: lines.find((l) => l.includes('→') || l.includes('指挥官会话')) || '',
          identity: lines.find((l) => /执行者 pt_in_|^会话 [0-9a-f]{10}…|复核运行|报告运行/.test(l)) || '',
          latest: lines.find((l) => l.includes('最新运行 ·')) || '',
        };
      }) : [];
      const buttons = [...document.querySelectorAll('button')];
      return {
        bodyText: document.body.innerText,
        leftTitle: left && left.querySelector('header') ? linesOf(left.querySelector('header')).join(' ') : '',
        leftRowCount: rows.length,
        rows,
        searchPlaceholder: document.querySelector('input[placeholder]')?.getAttribute('placeholder') || '',
        projectButton: (buttons.find((b) => ['继续项目', '暂停项目'].includes(b.textContent.trim())) || {}).textContent?.trim() || '',
        rightText: right ? right.innerText : '',
        href: location.href,
      };
    })()`);

    // ---- assertions (a)-(e), (g), (h) ------------------------------------
    check('a. 左列面板标题含「执行者与角色」', snap.leftTitle.includes('执行者与角色'), '标题含「执行者与角色」', snap.leftTitle);
    check('b. 左列分组行 = 9（指挥官 1 + 执行者 8）', snap.leftRowCount === 9, 9, snap.leftRowCount);
    check(
      'c. 至少一行含「指挥官 → 」+「执行者 pt_in_」+「会话 」',
      snap.rows.some((r) => r.text.includes('指挥官 → ') && r.text.includes('执行者 pt_in_') && r.text.includes('会话 ')),
      '存在同时含三者的行',
      snap.rows.filter((r) => r.text.includes('指挥官 → ') && r.text.includes('执行者 pt_in_')).map((r) => r.direction),
    );
    check('d. 至少一行含「指挥官会话」', snap.rows.some((r) => r.text.includes('指挥官会话')), '存在含「指挥官会话」的行', snap.rows.filter((r) => r.text.includes('指挥官会话')).map((r) => r.direction));
    check('e. 搜索框 placeholder 含「执行者」', snap.searchPlaceholder.includes('执行者'), 'placeholder 含「执行者」', snap.searchPlaceholder);
    check('g. 顶部按钮显示「继续项目」', snap.projectButton === '继续项目', '继续项目', snap.projectButton);
    // Bare intent ids (in_…) must not be the visible identity in the left list;
    // pt_in_… contains the substring, so require a non-word char before in_.
    const bareID = /(^|[^a-z_])in_[0-9a-f]{8}/;
    const offenders = snap.rows.map((r, i) => ({ row: i + 1, direction: r.direction, match: (r.text.match(bareID) || [])[0] || '' })).filter((x) => x.match);
    check('h. 左列行文本不出现裸 in_ 意图 id', offenders.length === 0, '0 处裸 in_ id', offenders);
    // (2) The commander row's metric spans must stay whole after the flex-wrap/
    // whitespace-nowrap fix: no more "39 / 事件" broken across wrapped lines.
    const commanderRow = snap.rows[0] || { lines: [], text: '' };
    const commanderMetrics = ['39 事件', '0 事实', '1,650,369 tok'];
    check(
      '2. 指挥官组指标行完整（39 事件 / 0 事实 / 1,650,369 tok）',
      commanderMetrics.every((s) => commanderRow.text.includes(s)),
      '含「39 事件」「0 事实」「1,650,369 tok」',
      commanderRow.lines,
    );
    check(
      '2b. 指挥官组指标无孤立断行',
      !commanderRow.lines.includes('事件') && !commanderRow.lines.includes('事实') && !commanderRow.lines.includes('tok'),
      '无单独成行的「事件/事实/tok」',
      commanderRow.lines,
    );

    const afterShot = await shot('runs-after.png');

    // ---- (f) click one executor group; the right detail must name it ------
    const executorRow = snap.rows.find((r) => r.text.includes('执行者 pt_in_'));
    if (!executorRow) throw new Error('no executor row to click');
    const participantID = (/执行者 (pt_in_[0-9a-z]+)/.exec(executorRow.text) || [])[1] || '';
    const clicked = await evalJS(`(() => {
      const left = [...document.querySelectorAll('section.tw-panel')].find((s) => s.innerText.includes('执行者与角色'));
      if (!left) return false;
      const row = [...left.querySelectorAll('button')].find((b) => b.innerText.includes(${JSON.stringify(participantID)}));
      if (!row) return false;
      row.click();
      return true;
    })()`);
    if (!clicked) throw new Error('executor group row not clickable: ' + participantID);
    await waitJS(
      `(() => { const p = [...document.querySelectorAll('section.tw-panel')].find((s) => s.innerText.includes('事件流')); return Boolean(p && p.innerText.includes(${JSON.stringify(participantID)})); })()`,
      '详情显示 ' + participantID,
    );
    await pause(700);
    const detail = await evalJS(`(() => {
      const right = [...document.querySelectorAll('section.tw-panel')].find((s) => s.innerText.includes('事件流'));
      return right ? { text: right.innerText, href: location.href } : null;
    })()`);
    check('f. 点执行者组后详情显示「执行者」标签与 pt_in_', Boolean(detail && detail.text.includes('执行者') && detail.text.includes(participantID)), '详情含「执行者」与 ' + participantID, detail && detail.text.split('\n').filter((l) => l.includes('执行者') || l.includes(participantID)).slice(0, 4));
    check('f2. 点击后 URL 记录选中的组', Boolean(detail && /run_group=/.test(detail.href)), 'URL 含 run_group=', detail && detail.href);
    // (1) The executor detail line must now carry a literal space before the
    // separator: `pt_in_xxx · 会话 9e8106504a…`, not `pt_in_xxx· 会话`.
    const detailLines = detail ? detail.text.split('\n').map((l) => l.trim()) : [];
    const executorLine = detailLines.find((l) => /pt_in_[0-9a-z]+/.test(l) && l.includes('会话')) || '';
    check(
      '1. 详情「执行者」行含字面空格（pt_in_xxx · 会话 …）',
      /pt_in_[0-9a-z]+ · 会话 [0-9a-f]{10}…/.test(executorLine),
      '形如 pt_in_xxx · 会话 9e8106504a…',
      executorLine,
    );
    const detailShot = await shot('runs-detail-after.png');
    check('i. 无浏览器运行时异常', errors.length === 0, [], errors);
    check(
      '两张截图为 1440x1000 且非空白',
      [afterShot, detailShot].every((s) => s.size && s.size.width === 1440 && s.size.height === 1000 && s.bytes > 20000),
      '1440x1000 / > 20KB',
      [afterShot, detailShot].map((s) => [s.size, s.bytes]),
    );

    // ---- artifacts -------------------------------------------------------
    const dump = [];
    dump.push('运行页「执行者与角色」DOM 快照（headless Chrome 1440x1000，活数据 5173 预览）');
    dump.push('URL: ' + pageURL);
    dump.push('登录: POST /login → ' + login.status + '，cookie ' + cookieName + (cookieOk ? '（CDP 写入成功）' : '（CDP 写入失败）'));
    dump.push('面板标题: ' + snap.leftTitle);
    dump.push('共 ' + snap.leftRowCount + ' 组 / 页脚: ' + (snap.bodyText.match(/共 \d+ 次运行/) || ['?'])[0]);
    dump.push('搜索框 placeholder: ' + snap.searchPlaceholder);
    dump.push('顶部项目按钮: ' + snap.projectButton);
    dump.push('');
    dump.push('左列每行 3 行文本（方向 / 副标识 / 最新运行）:');
    snap.rows.forEach((r, i) => {
      dump.push('[' + (i + 1) + '] ' + [r.direction, r.identity, r.latest].join('  ⏎  '));
    });
    dump.push('');
    dump.push('点击执行者组: ' + (participantID || '?') + ' → 详情文本（前 300 字）:');
    dump.push((detail ? detail.text : '').slice(0, 300).replace(/\n/g, ' ⏎ '));
    dump.push('详情「执行者」行: ' + (executorLine || '（未找到）'));
    dump.push('');
    dump.push('指挥官组（第 1 行）指标行: ' + commanderRow.lines.filter((l) => /事件|事实|tok|in\/out/.test(l)).join('  ⏎  '));
    fs.writeFileSync(path.join(out, 'runs-after-dom.txt'), dump.join('\n') + '\n', 'utf8');

    const result = {
      pageURL,
      login: { status: login.status, cookie: cookieName, cookieSet: cookieOk },
      checks,
      passed: checks.every((c) => c.passed),
      screenshots: [afterShot, detailShot],
      left: {
        title: snap.leftTitle,
        rowCount: snap.leftRowCount,
        rows: snap.rows.map((r) => ({ direction: r.direction, identity: r.identity, latest: r.latest })),
        commanderMetrics: commanderRow.lines,
        searchPlaceholder: snap.searchPlaceholder,
        projectButton: snap.projectButton,
      },
      clickedExecutor: participantID,
      executorLine,
      detailPreview: detail ? detail.text.split('\n').slice(0, 12) : null,
      domDump: '.build/ui-qa/runs-after-dom.txt',
      runtimeErrors: errors,
    };
    const lines = [
      '页面: ' + pageURL,
      ...checks.map((c) => (c.passed ? 'PASS ' : 'FAIL ') + c.name),
      '',
      '左列 ' + snap.leftRowCount + ' 组 / 顶部按钮 ' + snap.projectButton,
      '截图:',
      ...result.screenshots.map((s) => '  ' + s.path + ' ' + (s.size ? s.size.width + 'x' + s.size.height : '?') + ' ' + s.bytes + 'B'),
      'DOM: .build/ui-qa/runs-after-dom.txt',
    ];
    process.stdout.write(lines.join('\n') + '\n\n');
    process.stdout.write(JSON.stringify(result, null, 2) + '\n');
    assert.ok(result.passed, '运行页验收未通过: ' + JSON.stringify(checks.filter((c) => !c.passed)));
  } finally {
    if (ws) ws.close();
    stopChrome(chrome);
  }
})().catch((e) => {
  process.stderr.write(e.stack + '\n');
  process.exitCode = 1;
});
