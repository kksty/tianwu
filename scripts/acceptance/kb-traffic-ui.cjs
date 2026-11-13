// Offline browser acceptance: mocked HTTP only, no application/model startup.
// Run with node scripts/acceptance/kb-traffic-ui.cjs; TW_CHROME overrides Chrome.
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '../..');
const out = path.join(root, '.build/ui-qa');
fs.mkdirSync(out, { recursive: true });
const profile = fs.mkdtempSync(path.join(out, 'chrome-'));
const chrome = spawn(process.env.TW_CHROME || 'C:/Program Files/Google/Chrome/Application/chrome.exe', ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', '--remote-debugging-address=127.0.0.1', '--user-data-dir=' + profile, 'about:blank'], { windowsHide: true, stdio: 'ignore' });
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const now = '2026-11-07T00:00:00Z';
const project = { id: 'fixture', name: '本地界面回归', scope: '本地界面测试', paused: true, max_runs_effective: 0 };
const board = {"generated_at":"2026-11-07T00:00:00Z","project_paused":true,"intents":{"total":0,"running":0,"pending":0,"done":0,"failed":0,"parked":0,"held":0,"claimed":0},"facts":{"total":0,"with_evidence":0,"verified":0,"unverified":0,"rejected":0,"with_refs":0},"evidence":0,"runs":{"total":0,"running":0,"done":0,"failed":0,"canceled":0},"goals":{"total":0,"met":0,"open":[]},"open_intents":[],"running_intents":[],"recent_facts":[],"done_intents":[],"playbooks":{"total":1,"confirmed":1,"candidates":0},"redispatch":{"total":0,"directions":0},"usage":{"total_tokens":0,"input_tokens":0,"output_tokens":0,"cached_input_tokens":0,"requests":0,"missing_usage":0,"unknown_usage_runs":0},"decider":{"enabled":true,"paused":true,"model":"fixture","rounds":0,"tokens_total":0,"stalled":0,"activity":{"phase":"paused","detail":"已暂停"},"heartbeat_seconds":300}};

const entry={id:'world-existing',title:'已有通用事实',description:'协议条件',detail:'初始事实正文',source:'公开规范',tags:['http']};
let revision='revision-1',posted,flowRequests=0,activeLists=0,maxActiveLists=0;
const drafts=[];
const record=id=>({ref:'r_fixture/'+id,run_id:'r_fixture',intent_id:'in_fixture',session_id:'s_fixture',event_id:'e'+id,flow_id:String(id),method:'GET',url:'https://example.test/packet-'+id,status:200,at:now,complete:true,request_bytes:0,response_bytes:8});

let humanPublished=false,humanDraft,playbookDeleted=false,deletedRevision='',publishCalls=0;
const playbook={id:'pb_fixture',title:'项目战场样例',when:'现场条件',doc:'项目专属操作，不应自动复制到世界事实',origin:'executor',status:'confirmed',tags:['http'],updated_at:now,created_at:now};
async function api(url,request){
  if(url.pathname.endsWith('/api/playbooks'))return {playbooks:playbookDeleted?[]:[playbook]};
  if(url.pathname==='/p/fixture/api/playbooks/pb_fixture'){
    if(request.method==='DELETE'){playbookDeleted=true;return {deleted:true};}return playbook;
  }
  if(url.pathname==='/p/fixture/api/playbooks/pb_fixture/kb-candidate'){
    humanDraft={...JSON.parse(request.postData),id:'kbc-human',version:1,origin:'operator project:fixture playbook:pb_fixture',created_at:now,updated_at:now,published:false};drafts.push(humanDraft);return humanDraft;
  }
  if(url.pathname==='/api/kb/candidates/kbc-human/publish'){publishCalls++;humanPublished=true;humanDraft.published=true;return humanDraft.entry;}
  if(url.pathname==='/api/kb/entries/world-human'&&request.method==='DELETE'){humanPublished=false;humanDraft.published=false;deletedRevision='deleted-1';return {deleted:true};}

  if(url.pathname==='/api/projects')return {projects:[project]};
  if(url.pathname.endsWith('/api/board'))return board;
  if(url.pathname==='/api/fixture/advance'){revision='revision-2';entry.detail='外部发布的新正文';return {ok:true};}
  if(url.pathname==='/api/kb')return {entries:humanPublished?[entry,humanDraft.entry]:[entry],revisions:{[entry.id]:revision,'world-human':'human-revision'},deleted_revisions:deletedRevision?{'world-human':deletedRevision}:{}};
  if(url.pathname==='/api/kb/candidates'){
    if(request.method==='POST'){posted=JSON.parse(request.postData);await pause(900);const c={...posted,id:'kbc-fixture',version:1,origin:'operator',created_at:now,updated_at:now,published:false};drafts.push(c);return c;}
    return {candidates:drafts};
  }
  if(url.pathname.endsWith('/api/traffic/flows')){
    const n=++flowRequests;activeLists++;maxActiveLists=Math.max(activeLists,maxActiveLists);
    try{if(n===2)await pause(6000);if(url.searchParams.get('offset')==='100')return {flows:[record(3)],total:201};return {flows:[record(n===1?1:2)],total:201};}finally{activeLists--;}
  }
  if(url.pathname.endsWith('/api/traffic/flow')){const id=Number(url.searchParams.get('ref').split('/').pop());return {record:record(id),flow:{request:{method:'GET',url:record(id).url},response:{status_code:200,body:Buffer.from('packet-body-'+id).toString('base64')}}};}
  return {facts:[],intents:[],goals:[],runs:[],profiles:[],nodes:[],playbooks:[],decisions:[],requests:[],evidence:[],state:'',configured:true,model_configured:true};
}
(async () => {
  let ws;
  try {
    const portFile = path.join(profile, 'DevToolsActivePort');
    for(let n=0; n<150 && !fs.existsSync(portFile); n++) await pause(100);
    const port = Number(fs.readFileSync(portFile,'utf8').split('\n')[0]);
    const pages = await (await fetch('http://127.0.0.1:'+port+'/json/list')).json();
    ws = new WebSocket(pages.find(x=>x.type==='page').webSocketDebuggerUrl);
    await new Promise((resolve,reject)=>{ws.addEventListener('open',resolve,{once:true});ws.addEventListener('error',reject,{once:true})});
    let seq=0; const pending=new Map(), errors=[];
    const call=(method,params={})=>new Promise((resolve,reject)=>{const id=++seq;const timer=setTimeout(()=>{pending.delete(id);reject(new Error('CDP timeout '+method))},8000);pending.set(id,{resolve:v=>{clearTimeout(timer);resolve(v)},reject:e=>{clearTimeout(timer);reject(e)}});ws.send(JSON.stringify({id,method,params}));});
    ws.addEventListener('message', async evt => {
      const msg=JSON.parse(evt.data);
      if(msg.id){const p=pending.get(msg.id);if(p){pending.delete(msg.id);msg.error?p.reject(new Error(JSON.stringify(msg.error))):p.resolve(msg.result)};return;}
      if(msg.method==='Runtime.exceptionThrown') errors.push(msg.params.exceptionDetails.exception?.description || msg.params.exceptionDetails.text);
      if(msg.method==='Fetch.requestPaused') {
        const {requestId,request}=msg.params; const url=new URL(request.url); let body, type='application/json', code=200;
        if(url.pathname.startsWith('/static/')) {
          const file=path.join(root,'internal/web',url.pathname);
          if(fs.existsSync(file)){body=fs.readFileSync(file);type=file.endsWith('.js')?'text/javascript':file.endsWith('.css')?'text/css':file.endsWith('.svg')?'image/svg+xml':'application/octet-stream'} else {body=Buffer.from('not found');code=404;}
        } else if(url.pathname.includes('/api/')) body=Buffer.from(JSON.stringify(await api(url,request)));
        else { body=fs.readFileSync(path.join(root,'internal/web/static/index.html'));type='text/html'; }
        try { await call('Fetch.fulfillRequest',{requestId,responseCode:code,responseHeaders:[{name:'Content-Type',value:type}],body:body.toString('base64')}) } catch(e) { errors.push(e.message); }
      }
    });
    await call('Page.enable'); await call('Runtime.enable'); await call('Fetch.enable',{patterns:[{urlPattern:'*'}]});
    const evalJS=async expression=>(await call('Runtime.evaluate',{expression,returnByValue:true,awaitPromise:true})).result.value;
    const waitText=async text=>{for(let i=0;i<80;i++){if(await evalJS('document.body.innerText.includes('+JSON.stringify(text)+')'))return;await pause(100)}throw new Error('Missing text: '+text+' / '+await evalJS('document.body.innerText'))};

    const checks=[];
    const click=async label=>evalJS('[...document.querySelectorAll("button")].find(b=>b.textContent.trim()==='+JSON.stringify(label)+').click()');
    const fill=async(label,value)=>evalJS('(()=>{const l=[...document.querySelectorAll("label")].find(l=>l.childNodes[0]?.textContent.trim()==='+JSON.stringify(label)+');const e=l.querySelector("input,textarea");Object.getOwnPropertyDescriptor(e.tagName==="INPUT"?HTMLInputElement.prototype:HTMLTextAreaElement.prototype,"value").set.call(e,'+JSON.stringify(value)+');e.dispatchEvent(new Event("input",{bubbles:true}));})()');
    await call('Emulation.setDeviceMetricsOverride',{width:1440,height:1000,deviceScaleFactor:1,mobile:false});
    await call('Page.navigate',{url:'http://tianwu.test/g/?view=kb'});await waitText('已有通用事实');
    await click('编辑候选');await waitText('为什么可以跨项目复用');
    await fill('为什么可以跨项目复用','协议事实与目标无关');
    await evalJS("fetch('/api/fixture/advance').then(r=>r.json())");await click('刷新列表');await pause(200);
    await click('保存候选');await pause(100);
    checks.push({name:'pending save locks the editor and navigation',passed:await evalJS('document.querySelector("form input").matches(":disabled") && [...document.querySelectorAll("button")].find(b=>b.textContent.trim()==="已发布知识").disabled')});
    await waitText('候选已保存');
    checks.push({name:'edit carries the version originally read',passed:posted.base_revision==='revision-1',actual:posted.base_revision});
    await call('Page.navigate',{url:'http://tianwu.test/p/fixture/?view=traffic'});await waitText('packet-body-1');
    await pause(12200);
    checks.push({name:'slow polling joins the existing request',passed:maxActiveLists===1,actual:maxActiveLists});
    checks.push({name:'new traffic does not replace selected packet',passed:await evalJS("Array.from(document.links).some(a=>new URL(a.href).searchParams.get('ref')==='r_fixture/1')")});
    await click('下一页');await waitText('packet-body-3');
    checks.push({name:'page navigation selects its own packet',passed:await evalJS("Array.from(document.links).some(a=>new URL(a.href).searchParams.get('ref')==='r_fixture/3')")});

    await call('Page.navigate',{url:'http://tianwu.test/p/fixture/?view=playbooks'});await waitText('项目战场样例');
    await click('提名世界知识');await waitText('为什么可以跨项目复用');
    checks.push({name:'playbook promotion leaves the world fact body blank for a human',passed:await evalJS('[...document.querySelectorAll("label")].find(l=>l.childNodes[0]?.textContent.trim()==="事实正文").querySelector("textarea").value===""')});
    await fill('稳定 ID','world-human');await fill('标题','人工提取世界事实');await fill('摘要','通用条件');await fill('事实正文','人工核对的事实');await fill('来源说明','规范来源');await fill('为什么可以跨项目复用','不依赖现场');
    await click('保存候选');await waitText('候选已保存');
    checks.push({name:'human source nomination does not automatically publish',passed:publishCalls===0&&!humanPublished});
    checks.push({name:'source version travels with the human nomination',passed:humanDraft.source_updated_at===now});
    await click('发布到世界 KB');await waitText('已发布，所有项目');
    checks.push({name:'explicit publish installs the world fact',passed:publishCalls===1&&humanPublished});
    await click('已发布知识');
    await evalJS('[...document.querySelectorAll("button")].find(b=>b.textContent.includes("人工提取世界事实")).click()');await waitText('人工核对的事实');
    await click('删除知识');await waitText('删除这条世界知识？');
    await evalJS("[...document.querySelectorAll('[role=dialog] button,[role=alertdialog] button')].find(b=>b.textContent.trim()==='删除').click()");await waitText('知识已从当前实例移除');
    checks.push({name:'human world deletion removes the published entry',passed:!humanPublished&&deletedRevision==='deleted-1'});
    const shot=await call('Page.captureScreenshot',{format:'png',captureBeyondViewport:false});fs.writeFileSync(path.join(out,'knowledge-curation-desktop.png'),Buffer.from(shot.data,'base64'));
    await call('Page.navigate',{url:'http://tianwu.test/p/fixture/?view=playbooks'});await waitText('项目战场样例');
    await call('Emulation.setDeviceMetricsOverride',{width:390,height:844,deviceScaleFactor:1,mobile:true});await pause(100);
    const mobile=await evalJS('({width:innerWidth,scroll:document.documentElement.scrollWidth})');checks.push({name:'playbook curation fits a mobile viewport',passed:mobile.scroll<=mobile.width});
    await click('删除');await waitText('删除这条战场知识？');
    await evalJS("[...document.querySelectorAll('[role=dialog] button,[role=alertdialog] button')].find(b=>b.textContent.trim()==='删除').click()");await waitText('还没有战场知识');
    checks.push({name:'human can delete battlefield knowledge',passed:playbookDeleted});

    checks.push({name:'no browser runtime exceptions throughout curation',passed:errors.length===0,actual:errors});
    process.stdout.write(JSON.stringify({checks,passed:checks.every(c=>c.passed)})+'\n');
    assert.ok(checks.every(c=>c.passed),'UI regressions: '+JSON.stringify(checks.filter(c=>!c.passed)));
  } finally { if(ws)ws.close(); chrome.kill(); }
})().catch(e=>{process.stderr.write(e.stack+'\n');process.exitCode=1});
