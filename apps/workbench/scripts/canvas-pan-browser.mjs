import assert from 'node:assert/strict';
import { cpus, totalmem, platform, arch } from 'node:os';
import { setTimeout as delay } from 'node:timers/promises';
import { until, withLocalBrowser } from './local-browser.mjs';
import { classifyRenderer, frameStats } from './canvas-pan-samples.mjs';

let phase = 'setup';
let caseIndex = 0;
const view = '.free-canvas-viewport';

// Installed before navigation. Count actual workbench generation fetches, even
// if rejected. The Go fixture independently counts its upstream POSTs.
function observeRequests() {
  window.canvasGenerationPosts = 0;
  const original = window.fetch.bind(window);
  window.fetch = (input, init) => {
    const req = input instanceof Request;
    const path = new URL(req ? input.url : input, location.href);
    if ((init?.method || (req && input.method) || 'GET').toUpperCase() === 'POST' &&
        path.origin === location.origin &&
        /\/(?:runs|tasks|workflow-runs|execute)$/.test(path.pathname)) window.canvasGenerationPosts++;
    return original(input, init);
  };
}

function checkSources(config) {
  const el = document.querySelector('.free-canvas-viewport');
  if (!el || Number(el.dataset.nodeCount) !== config.nodes || Number(el.dataset.edgeCount) !== config.nodes * 2) return false;
  const node = suffix => el.querySelector('[data-node-id="' + config.prefix + suffix + '"]');
  const img = node('-0000')?.querySelector('img');
  const run = node('-0001')?.querySelector('.free-canvas-run');
  const viewport = el.getBoundingClientRect();
  function visible(child) {
    const r = child?.getBoundingClientRect();
    if (!r) return false;
    const x = (r.left + r.right) / 2, y = (r.top + r.bottom) / 2;
    return r.width > 0 && r.height > 0 && x >= viewport.left && x < viewport.right &&
      y >= viewport.top && y < viewport.bottom && y < innerHeight && y >= 0 &&
      child.closest('button')?.contains(document.elementFromPoint(x, y));
  }
  if (!img?.complete || !(img.naturalWidth > 0) || run?.dataset.status !== 'RUNNING' || !visible(img) || !visible(run)) return false;
  const url = new URL(img.currentSrc);
  return url.origin === location.origin && url.pathname.includes('/projects/' + config.project + '/canvas/revisions/') &&
    url.searchParams.get('project_id') === config.project && !el.querySelector('[data-node-id^="' + (config.prefix === 'normal' ? 'stress' : 'normal') + '-"]');
}

async function verifyPrivateImage(config) {
  const el = document.querySelector('[data-node-id="' + config.prefix + '-0000"] img');
  await el.decode();
  const response = await fetch(el.currentSrc, { cache: 'no-store', credentials: 'same-origin' });
  if (!response.ok || !response.headers.get('content-type')?.startsWith('image/png') ||
      !response.headers.get('cache-control')?.includes('private')) return false;
  const data = await response.arrayBuffer();
  if (data.byteLength > 2 << 20) return false;
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', data));
  return Array.from(digest, b => b.toString(16).padStart(2, '0')).join('') === config.thumbnail_digest;
}

// Executed in the renderer. rAF timestamps and trusted pointer samples share
// the page clock. Sampling ends on the first actual frame crossing the target.
function preparePan(duration, config, verifySource) {
  const el = document.querySelector('.free-canvas-viewport');
  const canvas = el.querySelector('canvas');
  const box = el.getBoundingClientRect();
  const state = { intervals: [], seconds: [], done: false, invalid: false, moves: 0, changeFrames: 0, maxDetailed: 0, maxOverview: 0 };
  window.canvasPan = state;
  let start, prev, previousCamera, lastSample = -Infinity, lastMoves = 0, down = false, previousTrusted;
  state.maxTrustedGap = 0;
  const onDown = e => { if (e.isTrusted && e.button === 0) down = true; };
  const onMove = e => {
    if (!down || !e.isTrusted || !(e.buttons & 1) || state.done) return;
    const now = performance.now();
    if (previousTrusted !== undefined) state.maxTrustedGap = Math.max(state.maxTrustedGap, now - previousTrusted);
    previousTrusted = now;
    state.moves++;
  };
  const onUp = () => { down = false; };
  el.addEventListener('pointerdown', onDown);
  el.addEventListener('pointermove', onMove);
  el.addEventListener('pointerup', onUp);
  function frame(mark) {
    if (!down || state.moves < 1) {
      requestAnimationFrame(frame);
      return;
    }
    if (start === undefined) start = mark;
    if (prev !== undefined) state.intervals.push(mark - prev);
    prev = mark;
    const age = mark - start;
    const camera = el.dataset.panX + '/' + el.dataset.panY;
    if (previousCamera !== undefined && camera !== previousCamera) state.changeFrames++;
    previousCamera = camera;
    const detailed = Number(el.dataset.detailedCount);
    state.maxDetailed = Math.max(state.maxDetailed, detailed);
    state.maxOverview = Math.max(state.maxOverview, Number(canvas.dataset.overviewNodes));
    if (detailed < 2 || detailed > (config.nodes === 500 ? 150 : 100) || Number(canvas.dataset.visibleEdges) < 1 || document.hidden) state.invalid = true;
    if (age - lastSample >= 1000 || age >= duration) {
      const visible = verifySource(config);
      const count = el.querySelectorAll('button.free-canvas-card').length;
      state.seconds.push({ elapsed_ms: age, trusted_moves: state.moves - lastMoves,
        camera_x: Number(el.dataset.panX), camera_y: Number(el.dataset.panY), detailed: count,
        visible_edges: Number(canvas.dataset.visibleEdges), overview_nodes: Number(canvas.dataset.overviewNodes), active_and_decoded: Boolean(visible) });
      if (!visible || count !== detailed) state.invalid = true;
      lastSample = age;
      lastMoves = state.moves;
    }
    if (age < duration && state.intervals.length < 65536) {
      requestAnimationFrame(frame);
      return;
    }
    state.done = true;
    el.removeEventListener('pointerdown', onDown);
    el.removeEventListener('pointermove', onMove);
    el.removeEventListener('pointerup', onUp);
  }
  requestAnimationFrame(frame);
  // At 100% these coordinates lie in the horizontal gutter between the first
  // and second rows. Validate the actual hit target before pressing.
  const x = box.left + Number(el.dataset.panX) + 97;
  const y = box.top + Number(el.dataset.panY) + 110;
  const target = document.elementFromPoint(x,y);
  if (!target || !el.contains(target) || target.closest('.free-canvas-card')) throw Error('missing blank canvas gutter');
  return { x, y };
}

async function measurePan(page, c, ms) {
  const source = '(' + preparePan.toString() + ')(' + ms + ',' + JSON.stringify(c) + ',' + checkSources.toString() + ')';
  const origin = await page.evaluate(source);
  const send = (type, x, y, buttons) => page.command('Input.dispatchMouseEvent', {
    type, x, y, button: type === 'mouseMoved' ? 'none' : 'left', buttons, clickCount: type === 'mouseMoved' ? 0 : 1,
  });
  await send('mousePressed', origin.x, origin.y, 1);
  const start = performance.now();
  let pos = origin;
  try {
    let tick = 0;
    for (;;) {
      const elapsed = performance.now() - start;
      if (elapsed > ms + 15000) throw Error('renderer never finished the real pan');
      const angle = elapsed / 240;
      pos = { x: origin.x + 22 * Math.sin(angle), y: origin.y + 9 * Math.sin(angle * .7) };
      await send('mouseMoved', pos.x, pos.y, 1);
      if (elapsed >= ms && tick % 4 === 0 && await page.evaluate(() => window.canvasPan.done)) break;
      // Actual input rate is recorded. Elapsed time, not iteration count,
      // determines both movement and the 60-second measurement window.
      tick++;
      await delay(Math.max(0, Math.min(12, start + tick * 16 - performance.now())));
    }
  } finally {
    await send('mouseReleased', pos.x, pos.y, 0);
  }
  const state = await page.evaluate(() => window.canvasPan);
  const stats = frameStats(state.intervals);
  assert.ok(!state.invalid && stats.total_ms >= ms && state.moves > ms / 100 && state.changeFrames > ms / 100);
  assert.ok(state.maxTrustedGap < 2000 && state.seconds.slice(1, -1).every(row => row.trusted_moves > 0));
  assert.ok(state.seconds.length >= Math.floor(ms / 1100));
  if (c.nodes === 2000) assert.ok(state.maxOverview > 0 && state.maxDetailed === 100);
  return { intervals_ms: state.intervals, ...stats, trusted_moves: state.moves, changed_camera_frames: state.changeFrames,
    max_trusted_input_gap_ms: state.maxTrustedGap, max_detailed: state.maxDetailed, max_overview: state.maxOverview, source_and_input_samples: state.seconds };
}

// Only installed outside the timing window. Observe actual 2-D draw calls in
// world coordinates to verify that the moved source endpoint connects to its
// original adjacent node; counting edges cannot prove that property.
function observeAdjacentLine() {
  const ctx = document.querySelector('canvas.free-canvas-lines').getContext('2d');
  const move = ctx.moveTo, line = ctx.lineTo;
  let from;
  window.canvasAdjustedLine = false;
  ctx.moveTo = function(x,y) { from = [x,y]; return move.call(this,x,y); };
  ctx.lineTo = function(x,y) {
    if (from?.[0] === 112 && from[1] === 60 && x === 308 && y === 48) window.canvasAdjustedLine = true;
    return line.call(this,x,y);
  };
}

async function editAndReopen(page, c) {
  await page.evaluate(observeAdjacentLine);
  const selector = '[data-node-id="' + c.prefix + '-0000"]';
  await page.evaluate(s => document.querySelector(s).focus({ preventScroll: true }), selector);
  await page.command('Input.dispatchKeyEvent', {
    type: 'keyDown', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13, text: '\r', unmodifiedText: '\r',
  });
  await page.command('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 });
  await page.fill('#free-node-title', '已移动的画布节点');
  await page.fill('#free-node-x', '24');
  await page.fill('#free-node-y', '12');
  await until(() => page.evaluate(() => window.canvasAdjustedLine), 'incident edge rerender');
  phase = 'fold';
  await page.fill('#free-group-select', 'layer');
  await page.button('折叠分组', true);
  await until(() => page.evaluate(() => !!document.querySelector('.free-canvas-card[data-group-id="layer"]')), 'folded group proxy');
  await page.button('展开分组', true);
  await until(() => page.evaluate(() => !document.querySelector('.free-canvas-card[data-group-id="layer"]')), 'expanded group');
  phase = 'edit';
  await page.button('保存画布', true);
  await until(() => page.evaluate(async config => {
    const path = '/api/v1/projects/' + config.project + '/canvas/revisions/2';
    const r = await fetch(path, { headers: { 'X-Project-ID': config.project }, cache: 'no-store' });
    if (!r.ok) return false;
    const v = await r.json(), n = v.document.nodes[0];
    return v.revision === 2 && n.x === 24 && n.y === 12 && n.title === '已移动的画布节点' && v.document.edges.length === config.nodes * 2;
  }, c), 'persisted second revision');
  await until(() => page.evaluate(config => {
    const img = document.querySelector('[data-node-id="' + config.prefix + '-0000"] img');
    return img?.complete && img.naturalWidth > 0 && new URL(img.currentSrc).pathname.includes('/revisions/2/');
  }, c), 'editor installed the new revision');
  phase = 'overview';
  await page.button('适应全图', true);
  return until(() => page.evaluate(config => {
    const v = document.querySelector('.free-canvas-viewport');
    const lines = v.querySelector('canvas');
    const markers = Number(lines.dataset.overviewNodes), edges = Number(lines.dataset.visibleEdges);
    const detailed = v.querySelectorAll('.free-canvas-card').length;
    if (Number(v.dataset.zoom) >= .45 || markers < 2 || edges <= 0 || detailed > (config.nodes === 500 ? 150 : 100)) return null;
    return { markers, edges, detailed, zoom: Number(v.dataset.zoom) };
  }, c), 'fit-to-graph overview');
}

async function run(config) {
  assert.ok(config.cases.length === 2 && config.cases[0].nodes === 500 && config.cases[1].nodes === 2000);
  const ms = config.mode === 'full' ? 60_000 : 3500;
  const report = { mode: config.mode, requested_gpu: config.gpu, headless: true, fresh_browser_profile: true,
    cpu_model: cpus()[0]?.model, logical_cpus: cpus().length, memory_bytes: totalmem(), system: platform() + '-' + arch(),
    viewport: [1920,1080], device_scale_factor: 1, emulated_network: { latency_ms: 80, download_mbps: 20, upload_mbps: 20 },
    cases: [], generation_posts: 0 };
  await withLocalBrowser({url: config.url, chromePath: process.env.WORKFLOW_CHROME, onlyOrigin: true,
    out: process.env.CANVAS_BROWSER_CAPTURE_DIR, allowGPU: config.gpu === 'hardware', signal: AbortSignal.timeout(6 * 60_000)}, async open => {
    const page = await open({width: 1920, height: 1080, deferNavigation: true});
    try { report.renderer = await page.renderingInfo(); }
    catch { report.renderer = { product: 'unknown', renderer: '', compositing: 'unknown', canvas2d: 'unknown' }; }
    report.renderer.observed = classifyRenderer(report.renderer);
    await page.command('Network.emulateNetworkConditions', { offline: false, latency: 80, downloadThroughput: 2_500_000, uploadThroughput: 2_500_000 });
    await page.command('Page.addScriptToEvaluateOnNewDocument', { source: '(' + observeRequests.toString() + ')()' });
    phase = 'login';
    await page.command('Page.navigate', { url: config.url });
    await page.fill('#auth-email', config.email);
    await page.fill('#auth-password', config.password);
    await page.button('进入创作空间', true);
    await until(() => page.evaluate(() => !!document.querySelector('#project-select')), 'authenticated studio');
    await page.fill('#project-select', config.cases[0].project);
    await page.button('创作草稿');
    await page.button('自由画布', true);
    for (const [i,c] of config.cases.entries()) {
      caseIndex = i + 1;
      phase = 'switch';
      if (i > 0) await page.fill('#project-select', c.project);
      phase = 'open';
      await until(() => page.evaluate(selector => {
        const el = document.querySelector(selector);
        if (!el) return false;
        el.scrollIntoView({block: 'center', behavior: 'instant'});
        return true;
      }, view), 'graph viewport');
      phase = 'sources';
      await until(() => page.evaluate(checkSources,c), 'displayed approved private thumbnail and actual active Run');
      assert.equal(await page.evaluate(verifyPrivateImage,c),true);
      if (i === 1) {
        phase = 'fold';
        await page.fill('#free-group-select', 'layer');
        await page.button('折叠分组', true);
        await until(() => page.evaluate(() => !!document.querySelector('[data-group-id="layer"]')), 'stress layer collapsed');
        await page.evaluate(s => document.querySelector(s).scrollIntoView({block:'center',behavior:'instant'}),view);
        await until(() => page.evaluate(checkSources,c), 'stress sources after folding');
      }
      if (process.env.CANVAS_BROWSER_CAPTURE_DIR) await page.screenshot(i === 0 ? 'regular-graph' : 'dense-graph');
      phase = 'pan';
      const sample = await measurePan(page,c,ms);
      if (i === 1) {
        phase = 'fold';
        await page.button('展开分组', true);
      }
      phase = 'edit';
      const overview = await editAndReopen(page,c);
      const target = i === 0 ? 20 : 25;
      report.cases.push({ nodes: c.nodes, edges: c.nodes * 2, target_p95_ms: target, passed: sample.p95_ms <= target,
        private_image_digest_matches: true, revised_adjacent_edge_drawn: true, folded_and_restored: true, overview, ...sample });
    }
    // Real project switches remount from persistent revision 2 after both
    // edits. Check the correct private thumbnails and no prior project cards.
    phase = 'switch';
    for (const c of config.cases) {
      await page.fill('#project-select', c.project);
      await until(() => page.evaluate((selector, config) => {
        const el = document.querySelector(selector);
        if (!el || Number(el.dataset.nodeCount) !== config.nodes) return false;
        el.scrollIntoView({ block: 'center', behavior: 'instant' });
        return true;
      }, view, c), 'new project viewport');
      await until(() => page.evaluate(checkSources,c), 'reopened project canvas');
      assert.ok(await page.evaluate(config => {
        const card = document.querySelector('[data-node-id="' + config.prefix + '-0000"]');
        return card?.querySelector('strong')?.textContent === '已移动的画布节点' &&
          new URL(card.querySelector('img').currentSrc).pathname.includes('/revisions/2/');
      },c));
    }
    report.generation_posts = await page.evaluate(() => window.canvasGenerationPosts);
    assert.equal(report.generation_posts,0);
    report.within_local_frame_targets = report.cases.every(c => c.passed);
    if (process.env.CANVAS_BROWSER_CAPTURE_DIR) {
      await page.size(390,844);
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(()=>requestAnimationFrame(resolve))));
      await page.evaluate(s => document.querySelector(s)?.scrollIntoView({block:'center',behavior:'instant'}),view);
      await page.screenshot('narrow-graph');
    }
    await page.close();
  });
  phase = 'report';
  process.stdout.write(JSON.stringify(report));
}

try {
  let input = '';
  for await (const part of process.stdin) {
    input += part;
    if (input.length > 12000) throw Error('fixture exceeds driver budget');
  }
  await run(JSON.parse(input));
 } catch (error) {
  const line = Number(String(error?.stack).match(/canvas-pan-browser\.mjs:(\d+):\d+/)?.[1] || 0);
  process.stderr.write(JSON.stringify({ phase, case: caseIndex, line }));
  process.exitCode = 1;
}

