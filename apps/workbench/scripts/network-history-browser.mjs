import assert from 'node:assert/strict';
import { arch, cpus, platform, release, totalmem } from 'node:os';
import { setTimeout as pause } from 'node:timers/promises';
import { artifact } from '../../../scripts/perf/frontend.mjs';
import { withLocalBrowser, until } from './local-browser.mjs';
import { classifyRenderer } from './canvas-pan-samples.mjs';
import { calibrationResult, observedRanks, matchScriptTransfers } from './network-history-measure.mjs';

let stage = 'startup', sampleNo = 0;
let currentReport;
let currentOrigin;
let currentTripEvidence;
let step = 'initialization', seriesIndex = 0, sourceLine = 0;
function point(label, index = 0) {
  step = label;
  seriesIndex = index;
  const lines = [...(new Error().stack || '').matchAll(/network-history-browser\.mjs:(\d+):\d+/g)];
  sourceLine = Number(lines[1]?.[1] || 0);
}
const profile = { offline:false, latency:80, downloadThroughput:2_500_000, uploadThroughput:2_500_000 };

function positiveControl(config) {
  // A real registered session is injected into Chromium's HttpOnly cookie jar
  // before navigation. Clear only this throwaway profile's UI/pending state so
  // each measured trip requires the same explicit project and chat selection.
  if (location.protocol !== 'http:') return;
  sessionStorage.clear();
  localStorage.clear();
  performance.setResourceTimingBufferSize(1600);
  const s = window.netHistory = { generated: 0, overflow: false };
  performance.addEventListener('resourcetimingbufferfull', () => { s.overflow = true; });
  const findNav = () => [...document.querySelectorAll('.sidebar button')].find(b => b.textContent.includes('灵感对话'));
  const hit = el => {
    if (!el || el.disabled || !el.getClientRects().length) return false;
    const r = el.getBoundingClientRect();
    const x = (Math.max(0,r.left)+Math.min(innerWidth,r.right))/2;
    const y = (Math.max(0,r.top)+Math.min(innerHeight,r.bottom))/2;
    const top = document.elementFromPoint(x,y);
    return top && el.contains(top);
  };
  const original = window.fetch.bind(window);
  window.fetch = (input, init) => {
    const req = input instanceof Request, url = new URL(req ? input.url : input, location.href);
    const method = init?.method || (req && input.method) || 'GET';
    if (url.origin === location.origin && method.toUpperCase() === 'POST' && /\/(?:runs|tasks|execute)$/.test(url.pathname)) s.generated++;
    return original(input,init);
  };
  document.addEventListener('click',e => {
    const b = e.target.closest?.('button');
    if (e.isTrusted && b) {
      if (b === findNav()) s.nav_click = e.timeStamp;
      if (b.matches('.conversation-item') && b.querySelector('strong')?.textContent === config.title) s.select_click = e.timeStamp;
    }
  },true);
  document.addEventListener('input',e => {
    if (e.isTrusted && e.target.id === 'chat-input') s.trusted_composer_events = (s.trusted_composer_events || 0)+1;
    if (e.isTrusted && e.target.id === 'chat-input' && e.target.value === '网络输入核验') s.input_event = e.timeStamp;
  },true);
  function frame() {
    const now = performance.now();
    if (!s.first_interactive && document.querySelector('#project-select')?.value === config.project && hit(findNav())) s.first_interactive = now;
    if (s.nav_click && !s.list_ready && [...document.querySelectorAll('.conversation-item')].some(b =>
      b.querySelector('strong')?.textContent === config.title && hit(b))) s.list_ready = now;
    if (s.select_click && !s.history_visible) {
      const list = document.querySelector('.chat-window-list');
      const read = id => list?.querySelector('[data-message-id="' + id + '"] .chat-message-content');
      const question = read(config.user_node), answer = read(config.leaf);
      if (list?.dataset.indexedCount === '20' && question?.textContent === config.expected_question &&
          answer?.textContent === config.expected_answer && hit(answer) &&
          document.querySelector('.conversation-topbar')?.textContent.includes(config.turns*2+' 节点') &&
          document.querySelector('.conversation-topbar h2')?.textContent === config.title) {
        s.history_visible = now;
        s.resident_count = Number(list.dataset.residentCount);
        s.resident_bytes = Number(list.dataset.residentBytes);
        s.mounted_count = list.querySelectorAll('[data-message-id]').length;
      }
    }
    if (s.input_event && document.querySelector('#chat-input')?.value === '网络输入核验') {
      requestAnimationFrame(() => {s.input_presented = performance.now();});
      return;
    }
    requestAnimationFrame(frame);
  }
  requestAnimationFrame(frame);
}

function resources(start, end) {
  const list = [...performance.getEntriesByType('navigation'), ...performance.getEntriesByType('resource')];
  if (list.length > 1600 || window.netHistory.overflow) throw Error('resource buffer overflow');
  const safe = [];
  for (const e of list) {
    if (e.startTime < start || e.startTime >= end) continue;
    const u = new URL(e.name);
    if (u.origin !== location.origin || !e.responseEnd) continue;
    const pathname = u.pathname;
    const kind = e.entryType === 'navigation' ? 'document' : /\.js$/.test(pathname) ? 'js' :
      /\.css$/.test(pathname) ? 'css' : pathname.startsWith('/api/') ?
        (pathname.includes('/conversations') ? 'history_api' : 'other_api') : 'other_static';
    const name = kind === 'js' ? pathname.split('/').at(-1) : '';
    if (kind === 'js' && (u.search || !/^\/assets\/[A-Za-z0-9_.-]+\.js$/.test(pathname))) throw Error('unexpected script identity');
    const receipt = Number(e.serverTiming?.find(m=>m.name==='benchscript')?.description || 0);
    safe.push({ kind, name, receipt, encoded:e.encodedBodySize, decoded:e.decodedBodySize, transfer:e.transferSize });
  }
  return safe;
}

async function trustedClick(page, area, text) {
  const pos = await until(() => page.evaluate((area,text) => {
    const el = [...document.querySelectorAll(area)].find(e => e.textContent.includes(text) && !e.disabled && e.getClientRects().length);
    if (!el) return null;
    el.scrollIntoView({block:'nearest',behavior:'instant'});
    const r = el.getBoundingClientRect(), p = {x:r.left+r.width/2,y:r.top+r.height/2};
    return el.contains(document.elementFromPoint(p.x,p.y)) ? p : null;
  },area,text),'visible real action');
  await page.command('Input.dispatchMouseEvent',{type:'mousePressed',button:'left',buttons:1,clickCount:1,...pos});
  await page.command('Input.dispatchMouseEvent',{type:'mouseReleased',button:'left',buttons:0,clickCount:1,...pos});
}

function takeHeaders(map, streams, response, origin) {
  const u = new URL(response.url);
  if (u.origin !== origin) return;
  const h = Object.fromEntries(Object.entries(response.headers).map(([k,v]) => [k.toLowerCase(),v]));
  if (h['content-type']?.includes('text/event-stream')) streams.count++;
  if (!/^\/assets\/[A-Za-z0-9_.-]+\.js$/.test(u.pathname)) return;
  const name = u.pathname.split('/').at(-1);
  const header = {file:name,status:response.status,encoding:h['content-encoding'],vary:/(^|,\s*)accept-encoding(\s*,|$)/i.test(h.vary || ''),
    length:Number(h['content-length']),protocol:response.protocol};
  const id = /benchscript;desc="(\d+)"/.exec(h['server-timing'] || '');
  if (id) map.set(Number(id[1]),header);
  map.set(name,header);
}

async function waitStatic(page) {
  // Wait until the requested script and stylesheet bodies have complete
  // ResourceTiming entries and no new static resource has completed for
  // 350ms. Timings are captured separately in the renderer before this wait.
  await until(() => page.evaluate(() => {
    const nav = performance.getEntriesByType('navigation')[0];
    const items = performance.getEntriesByType('resource').filter(e => /\.(js|css)$/.test(new URL(e.name).pathname));
    return nav?.loadEventEnd > 0 && items.length > 0 &&
      performance.now() - Math.max(nav.loadEventEnd,...items.map(e => e.responseEnd)) >= 350;
  }), 'completed static bodies');
}

async function runTrip(page, config, headers, streams, measured) {
  const origin = config.url;
  currentTripEvidence = {kind:measured?'measured':'warmup'};
  const counts = async after => {
    const r = await fetch(origin+'/__network_benchmark/counts'+(after === undefined ? '' : '?after='+after));
    if (!r.ok) throw Error('missing server transfer witness');
    return r.json();
  };
  const initial = await counts();
  headers.clear();
  streams.count = 0;
  stage = 'interactive';
  point('navigate_project');
  await page.command('Page.navigate',{url:origin});
  point('authenticated_project_control');
  await until(() => page.evaluate(() => window.netHistory?.first_interactive), 'authenticated project interaction');
  await waitStatic(page);
  const streamsAtHome = streams.count;
  stage = 'history';
  point('click_project_chat');
  await trustedClick(page,'.sidebar button','灵感对话');
  await until(() => page.evaluate(() => window.netHistory.list_ready), 'selectable saved project conversation');
  await trustedClick(page,'.conversation-item',config.title);
  point('latest_history_visible');
  await until(() => page.evaluate(() => window.netHistory.history_visible), 'actual final answer and original user message');
  stage = 'input';
  point('trusted_composer_input');
  currentTripEvidence.initial_input = await page.evaluate(() => {
    const el=document.querySelector('#chat-input');
    el?.focus();
    return {present:!!el,disabled:!!el?.disabled,focused:el===document.activeElement,length:el?.value?.length??-1};
  });
  assert.deepEqual(currentTripEvidence.initial_input,{present:true,disabled:false,focused:true,length:0});
  point('dispatch_screen_input');
  await page.command('Input.insertText',{text:'网络输入核验'});
  currentTripEvidence.after_input = await page.evaluate(() => {
    const el=document.querySelector('#chat-input');
    return {focused:el===document.activeElement,length:el?.value?.length??-1,
      expected_value:el?.value==='网络输入核验',trusted_events:window.netHistory.trusted_composer_events||0};
  });
  point('wait_input_paint');
  await until(() => page.evaluate(() => window.netHistory.input_presented), 'trusted input in the real composer');
  currentTripEvidence.input_painted = true;
  if (!measured) {
    // The app has a real beforeunload guard for an unsent draft. Clear only
    // the fixture's own, verified text through normal trusted editing.
    point('clear_verified_warmup_draft');
    await page.evaluate(() => {
      const el=document.querySelector('#chat-input');
      if (!el || el.value!=='网络输入核验') throw Error('unexpected warm-up draft');
      el.focus();
      el.select();
    });
    await page.command('Input.dispatchKeyEvent',{type:'keyDown',key:'Backspace',code:'Backspace',windowsVirtualKeyCode:8});
    await page.command('Input.dispatchKeyEvent',{type:'keyUp',key:'Backspace',code:'Backspace',windowsVirtualKeyCode:8});
    await until(() => page.evaluate(() => document.querySelector('#chat-input')?.value==='' && window.netHistory.trusted_composer_events>=2),
      'empty warm-up draft after trusted deletion');
    currentTripEvidence.warmup_draft_cleared = true;
    return;
  }
  stage = 'transfer';
  point('read_completed_transfers');
  await waitStatic(page);
  const markers = await page.evaluate(() => window.netHistory);
  assert.equal(markers.generated,0);
  assert.ok(markers.mounted_count > 0 && markers.mounted_count <= 24 && markers.resident_count <= 20 && markers.resident_count > 0 &&
    markers.resident_bytes > 0 && markers.resident_bytes <= (8 << 20));
  // Completion of a server write precedes the browser's completed timing
  // entry. Give an observed in-flight receipt a bounded window to arrive.
  // At expiry its unmatched identity remains in the reported failure.
  let transfers;
  const deadline=performance.now()+2500;
  for (;;) {
    const initialResources=await page.evaluate(resources,0,markers.nav_click);
    const historyResources=await page.evaluate(resources,markers.nav_click,1e12);
    const final=await counts(initial.head);
    transfers=matchScriptTransfers({startup:initialResources,history:historyResources},headers,final.receipts);
    if (transfers.total.matched_requests===final.receipts.length && final.receipts.every(r=>r.status>0) ||
        performance.now()>=deadline) break;
    await pause(50);
  }
  const {startup,history,total}=transfers;
  return { first_interactive_ms: markers.first_interactive, project_click_to_history_ms: markers.history_visible-markers.nav_click,
    chat_click_to_list_ms:markers.list_ready-markers.nav_click, select_click_to_messages_ms:markers.history_visible-markers.select_click,
    input_to_next_paint_opportunity_ms:markers.input_presented-markers.input_event,
    expected_node_count:config.turns*2,indexed_latest_page:20,mounted:markers.mounted_count,resident:markers.resident_count,
    resident_bytes:markers.resident_bytes,completed_startup_transfers:startup,completed_history_transfers:history,
    total_script_transfers:total,input_evidence:currentTripEvidence,
    sse_response_headers_at_home:streamsAtHome,sse_response_headers_over_trip:streams.count,
    generation_posts:markers.generated };
}

async function calibrate(page, config, raw) {
  stage = 'calibration';
  point('navigate_calibration');
  await page.command('Page.navigate',{url:config.url+'/__network_benchmark/blank'});
  await until(() => page.evaluate(() => document.title === 'Local calibration'), 'calibration origin');
  // Separate CDP commands keep each call below the bounded DevTools timeout.
  async function small() {
    return page.evaluate(async () => {
      const start = performance.now(), r = await fetch('/__network_benchmark/rtt',{cache:'no-store'});
      await r.arrayBuffer();
      if (r.status !== 204) throw Error('invalid RTT probe');
      return performance.now()-start;
    });
  }
  point('rtt_warmup');
  raw.warmup_ms = await small();
  for (let i=0;i<7;i++) {
    point('rtt',i+1);
    raw.rtt_ms.push(await small());
  }
  for (let i=0;i<3;i++) {
    point('download',i+1);
    const down = await page.evaluate(async () => {
      const start = performance.now(), r = await fetch('/__network_benchmark/down',{cache:'no-store'});
      // Send the native backing Blob, including the entire verified original
      // body; a 4-MiB renderer ArrayBuffer request severed CDP in Chrome 157.
      const blob = await r.blob(), elapsed = performance.now()-start;
      const digest = new Uint8Array(await crypto.subtle.digest('SHA-256',await blob.arrayBuffer()));
      const validHash = Array.from(digest,b=>b.toString(16).padStart(2,'0')).join('') === r.headers.get('x-benchmark-sha256');
      window.networkPayload = blob;
      return {ms:elapsed,bytes:blob.size,status:r.status,identity_encoding:!r.headers.get('content-encoding'),sha256_ok:validHash};
    });
    raw.download_receipts.push(down);
    raw.down_ms.push(down.ms);
    assert.ok(down.status === 200 && down.bytes === raw.bytes && down.identity_encoding && down.sha256_ok);
    point('upload',i+1);
    const up = await page.evaluate(async () => {
      const start = performance.now(), r = await fetch('/__network_benchmark/up',{method:'POST',body:window.networkPayload,cache:'no-store'});
      await r.arrayBuffer();
      return {ms:performance.now()-start,status:r.status,bytes:window.networkPayload.size};
    });
    raw.upload_receipts.push(up);
    raw.up_ms.push(up.ms);
    assert.ok(up.status === 204 && up.bytes === raw.bytes);
  }
  Object.assign(raw,calibrationResult(raw));
}

async function main(config) {
  assert.ok(['full','pilot','calibration'].includes(config.mode));
  if (config.mode !== 'calibration') assert.equal(config.turns,config.mode === 'full' ? 5000 : 150);
  currentOrigin = config.url;
  const samples = config.mode === 'full' ? 20 : 2;
  const identity = async () => {
    const {server_sha256,dist_sha256,dist_files,index_sha256} = await artifact(config.binary,config.dist);
    return {test_binary_sha256:server_sha256,dist_sha256,dist_files,index_sha256};
  };
  const report = {mode:config.mode, samples_per_condition:samples, cpu_model:cpus()[0].model,logical_cpus:cpus().length,
    ram_bytes:totalmem(),os:platform()+' '+release(),arch:arch(),http:'loopback plaintext',
    cdp:{latency_ms:80,down_bytes_per_second:2_500_000,up_bytes_per_second:2_500_000,viewport:[1920,1080],dpr:1},
    artifact:config.mode === 'calibration' ? null : await identity(), calibration:[], cold:[], warm:[]};
  currentReport = report;
  const browser = async (run,headers,streams) => {
    await withLocalBrowser({url:config.url,chromePath:process.env.WORKFLOW_CHROME,onlyOrigin:true,allowGPU:true,
      onResponse:r=>takeHeaders(headers,streams,r,config.url),signal:AbortSignal.timeout(100_000)}, async open => {
      const page = await open({width:1920,height:1080,deferNavigation:true});
      point('apply_network_rules');
      const { offline, ...limits } = profile;
      await page.command('Network.emulateNetworkConditionsByRule',{offline,matchedNetworkConditions:[{urlPattern:'',...limits}]});
      await page.command('Network.overrideNetworkState',profile);
      await page.command('Page.bringToFront');
      await run(page);
      await page.close();
    });
  };
  const calibration = async () => {
    const raw = {after_measured_samples:sampleNo,bytes:4194304,rtt_ms:[],down_ms:[],up_ms:[],download_receipts:[],upload_receipts:[]};
    report.calibration.push(raw);
    await browser(async page => {
      raw.browser = (await page.command('Browser.getVersion')).product;
      await calibrate(page,config,raw);
    },new Map(),{count:0});
  };
  await calibration();
  if (config.mode === 'calibration') {
    await calibration();
    await calibration();
    report.passed = report.calibration.every(r=>r.passed);
    process.stdout.write(JSON.stringify(report));
    return;
  }
  const setup = {project:config.project,title:config.title,leaf:config.leaf,user_node:config.user_node,turns:config.turns,
    expected_question:'历史问题 '+String(config.turns).padStart(4,'0'),expected_answer:'历史答复 '+String(config.turns).padStart(4,'0')};
  for (let round=0;round<samples;round++) {
    if (round === Math.floor(samples/2)) await calibration();
    for (const condition of round % 2 ? ['warm','cold'] : ['cold','warm']) {
      sampleNo++;
      const headers = new Map(),streams = {count:0};
      await browser(async page => {
        stage='prepare';
        let gpu;
        try {gpu = await page.renderingInfo();} catch {gpu={product:'unknown',renderer:'',compositing:'unknown',canvas2d:'unknown'};}
        gpu.observed = classifyRenderer(gpu);
        // Do not infer an integrated adapter from a merely hardware-accelerated path.
        gpu.integrated = gpu.observed === 'hardware' && /intel/i.test(gpu.renderer) &&
          /iris|uhd|\bhd graphics|intel\(r\) graphics|intel graphics/i.test(gpu.renderer) ? 'identified' : 'unconfirmed';
        const {success} = await page.command('Network.setCookie',{name:config.session_name,value:config.session_value,url:config.url,
          httpOnly:true,sameSite:'Strict',path:'/'});
        assert.equal(success,true);
        await page.command('Page.addScriptToEvaluateOnNewDocument',{source:'('+positiveControl.toString()+')('+JSON.stringify(setup)+')'});
        let warmupEvidence;
        if (condition === 'warm') {
          await runTrip(page,config,headers,streams,false);
          warmupEvidence=currentTripEvidence;
          point('leave_empty_warmup_document');
          await page.command('Page.navigate',{url:'about:blank'});
          await until(() => page.evaluate(() => location.href === 'about:blank'), 'new document between warm-up and measurement');
          warmupEvidence.left_document = true;
        }
        const measured = await runTrip(page,config,headers,streams,true);
        measured.renderer = gpu;
        if (warmupEvidence) measured.warmup_input_evidence = warmupEvidence;
        measured.sample = sampleNo;
        report[condition].push(measured);
      },headers,streams);
    }
  }
  await calibration();
  stage='report';
  assert.deepEqual(await identity(),report.artifact);
  report.summary={};
  for (const condition of ['cold','warm']) {
    const rows=report[condition];
    report.summary[condition]={};
    for (const k of ['first_interactive_ms','project_click_to_history_ms','select_click_to_messages_ms','input_to_next_paint_opportunity_ms']) {
      report.summary[condition][k]=observedRanks(rows.map(r=>r[k]));
    }
  }
  // Cold transfers are required to carry actual gzip bytes. A hot reload must
  // demonstrate real cache reuse on at least one known startup script.
  report.passed = report.calibration.every(r=>r.passed) && [...report.cold,...report.warm].every(r =>
    r.total_script_transfers.valid &&
    r.completed_startup_transfers.wire_js_body_bytes <= 300*1024) &&
    report.cold.every(r=>r.completed_startup_transfers.wire_js_body_bytes>0) &&
    report.warm.every(r=>r.completed_startup_transfers.scripts.some(s=>s.cache_hit));
  report.integrated_graphics_identified_for_all_samples = [...report.cold,...report.warm].every(r =>
    r.renderer.observed === 'hardware' && r.renderer.integrated === 'identified');
  report.full_calibrated_integrated_gpu_run = config.mode === 'full' && report.passed &&
    report.integrated_graphics_identified_for_all_samples;
  process.stdout.write(JSON.stringify(report));
}
try {
  let raw='';
  for await (const piece of process.stdin) {raw+=piece;if (raw.length>12000) throw Error('fixture budget');}
  await main(JSON.parse(raw));
} catch (err) {
  const fault = {stage,step,series_index:seriesIndex,sample:sampleNo,source_line:sourceLine};
  const reason = String(err?.message || '');
  fault.reason = /connection closed|browser.*stopped/i.test(reason) ? 'browser_closed' :
    /timed out|timeout/i.test(reason) ? 'deadline' : err?.code === 'ERR_ASSERTION' ? 'evidence_mismatch' : 'driver_error';
  if (currentReport) {
    // The parent reports failure after retaining every completed calibration
    // and sample, without including fixture data or arbitrary CDP messages.
    currentReport.passed = false;
    currentReport.stopped_at = fault;
    if (currentTripEvidence) currentReport.partial_trip = currentTripEvidence;
    try {
      const r = await fetch(currentOrigin+'/__network_benchmark/progress',{signal:AbortSignal.timeout(1000)});
      if (r.ok) currentReport.probe_totals_at_failure = await r.json();
    } catch {}
    process.stdout.write(JSON.stringify(currentReport));
  } else {
    process.stderr.write(JSON.stringify(fault));
    process.exitCode=1;
  }
}

