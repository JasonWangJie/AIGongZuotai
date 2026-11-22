import assert from 'node:assert/strict';
import { setTimeout as pause } from 'node:timers/promises';
import { until, withLocalBrowser } from './local-browser.mjs';

// Aggregate only. The private URL, session and resource identities never
// leave this process or enter the JSON report.
let phase = 'auth';
let step = 0, observed = 0;
const deadline = AbortSignal.timeout(260_000);
const twoFrames = (page) => page.evaluate(() => new Promise(resolve =>
  requestAnimationFrame(() => requestAnimationFrame(resolve))));

function installObservers() {
  const streams = new Map(), players = [], images = new Set();
  const counters = { starts: 0, closes: 0, maxPlaying: 0, imageErrors: 0 };
  let initialScreen = true;
  const Native = window.EventSource;
  window.EventSource = new Proxy(Native, { construct(target, args) {
    const source = Reflect.construct(target, args);
    const u = new URL(args[0], location.origin);
    if (u.origin === location.origin && /^\/api\/v1\/workspaces\/[^/]+\/events$/.test(u.pathname)) {
      const n = ++counters.starts;
      streams.set(n, { project: u.searchParams.get('project_id'), ref: new WeakRef(source) });
      const close = source.close.bind(source);
      source.close = () => { if (streams.delete(n)) counters.closes++; return close(); };
    }
    return source;
  } });
  const playerCount = () => players.reduce((n, ref) => {
    const v = ref.deref();
    return n + Number(Boolean(v && !v.paused && !v.ended && v.readyState >= 2));
  }, 0);
  document.addEventListener('playing', e => {
    if (!(e.target instanceof HTMLVideoElement)) return;
    if (!players.some(ref => ref.deref() === e.target)) players.push(new WeakRef(e.target));
    counters.maxPlaying = Math.max(counters.maxPlaying, playerCount());
  }, true);
  document.addEventListener('load', e => {
    if (initialScreen && e.target instanceof HTMLImageElement && e.target.closest('.library-card')) images.add(e.target.currentSrc);
  }, true);
  document.addEventListener('error', e => {
    if (e.target instanceof HTMLImageElement && e.target.closest('.library-card')) counters.imageErrors++;
  }, true);
  window.libraryObservation = { counters, streams, players, images, playerCount, endFirstScreen() {initialScreen=false;images.clear();} };
}

function observe(project) {
  const { streams, players, images, counters, playerCount } = window.libraryObservation;
  const cards = [...document.querySelectorAll('button.library-card')];
  const imgs = cards.map(c => c.querySelector('img')).filter(Boolean);
  const inProject = raw => {
    const u = new URL(raw, location.href);
    return u.origin === location.origin && /^\/api\/v1\/library\/lib_[a-f0-9]{64}\/thumbnail$/.test(u.pathname)
      && u.searchParams.get('project_id') === project;
  };
  return {
    cards: cards.length, imageNodes: imgs.length, decoded: imgs.filter(i => i.complete && i.naturalWidth > 0).length,
    wrongScope: imgs.filter(i => !inProject(i.src)).length, distinctLoadedFirstImages: images.size,
    videos: document.querySelectorAll('video').length, playing: playerCount(),
    unreleasedPlayers: players.filter(ref => {const v=ref.deref(); return v && !v.isConnected && (!v.paused || v.hasAttribute('src'));}).length,
    projectOpen: [...streams.values()].filter(x => x.project===project && x.ref.deref()?.readyState===EventSource.OPEN).length,
    otherSubscriptions: [...streams.values()].filter(x => x.project!==project).length,
    subscriptions: streams.size, ...counters,
  };
}

async function sample(page, project) {
  await page.command('HeapProfiler.collectGarbage');
  const heap = await page.command('Runtime.getHeapUsage');
  const dom = await page.command('Memory.getDOMCounters');
  const s = await page.evaluate(observe,project);
  return { js_heap_used_bytes_after_gc:heap.usedSize, dom_nodes_after_gc:dom.nodes,
    documents_after_gc:dom.documents, mounted_cards:s.cards, mounted_images:s.imageNodes,
    attached_videos:s.videos, active_project_streams:s.subscriptions };
}

function anchor() {
  const main=document.querySelector('main').getBoundingClientRect();
  const card=[...document.querySelectorAll('button.library-card')].find(c=>{
    const r=c.getBoundingClientRect(); return r.bottom>main.top+1 && r.top<main.bottom-1;
  });
  return card && {id:card.dataset.assetId,top:card.getBoundingClientRect().top};
}

async function run() {
  let input='';
  for await(const chunk of process.stdin) { input+=chunk; assert.ok(input.length<32768); }
  const cfg=JSON.parse(input), {url,gallery,projects,thumbs,videos}=cfg;
  assert.equal(new Set([gallery,...projects]).size,31);
  assert.equal(new Set(thumbs).size,30); assert.equal(new Set(videos).size,2);
  let first=true, firstResponses=0, firstFailures=0, lastFirstResponse=Date.now();
  const mediaResponses=new Set(), firstURLs=new Set();
  await withLocalBrowser({url,onlyOrigin:true,chromePath:process.env.WORKFLOW_CHROME,signal:deadline,onResponse:r=>{
    const u=new URL(r.url);
    if (u.origin!==new URL(url).origin || !u.pathname.startsWith('/api/v1/library/')) return;
    if (first && u.pathname.endsWith('/thumbnail')) {
      firstResponses++; lastFirstResponse=Date.now(); firstURLs.add(u.pathname);
      if (r.status!==200 || !r.mimeType.startsWith('image/') || u.searchParams.get('project_id')!==gallery) firstFailures++;
    }
    if (u.pathname.endsWith('/content') && [200,206].includes(r.status) && r.mimeType==='video/mp4') mediaResponses.add(u.pathname);
  }},async open=>{
    const page=await open({width:1280,height:900,deferNavigation:true});
    await page.command('Page.addScriptToEvaluateOnNewDocument',{source:'('+installObservers.toString()+')()'});
    await page.command('Page.navigate',{url});
    await page.fill('#auth-email',cfg.email); await page.fill('#auth-password',cfg.password);
    await page.button('进入创作空间',true);
    const selected=await until(()=>page.evaluate(()=>document.querySelector('#project-select')?.value),'current project');
    if(selected!==gallery) await page.fill('#project-select',gallery);
    await page.button('作品素材',true);
    phase='first-screen';
    const loaded=n=>until(()=>page.evaluate(n=>new RegExp('已加载\\s+'+n+'\\s+项').test(document.querySelector('.library-list-summary')?.textContent||''),n),'page size');
    await loaded(24);
    const firstScreen=await until(async()=>{
      const s=await page.evaluate(observe,gallery);
      return s.decoded>0 && s.projectOpen===1 && firstResponses>0 && Date.now()-lastFirstResponse>=350 && s;
    },'decoded first screen and active private stream');
    step=1; observed=firstResponses; first=false;
    await page.evaluate(()=>window.libraryObservation.endFirstScreen());
    assert.ok(firstScreen.imageNodes<=30 && firstScreen.distinctLoadedFirstImages<=30 && firstURLs.size<=30 && firstResponses<=30);
    assert.equal(firstFailures+firstScreen.imageErrors+firstScreen.wrongScope,0);
    assert.equal(firstScreen.videos,0);
    const measurements={
      viewport_css_px:[1280,900], chromium_product:(await page.command('Browser.getVersion')).product,
      first_screen:{page_items:24, cards:firstScreen.cards, image_nodes:firstScreen.imageNodes,
        decoded_images:firstScreen.decoded, thumbnail_http_responses:firstResponses, distinct_thumbnail_responses:firstURLs.size},
      samples:{first_screen:await sample(page,gallery)}
    };
    const topIdentity=await page.evaluate(()=>document.querySelector('button.library-card[data-index="0"]')?.dataset.assetId);
    assert.ok(topIdentity);
    async function play(id) {
      await until(()=>page.evaluate(id=>{
        const b=document.querySelector('button.library-card[data-asset-id="'+id+'"]');
        if(!b) return false; b.click(); return true;
      },id),'video card');
      await page.trustedButton('播放视频');
      await until(()=>page.evaluate(id=>{
        const v=document.querySelector('dialog video');
        return v && !v.paused && !v.ended && v.videoWidth>0 && v.currentTime>0 &&
          new URL(v.currentSrc).pathname==='/api/v1/library/'+id+'/content';
      },id),'verified moving authorized video');
    }
    phase='media';
    await play(videos[0]);
    step=2;
    await page.evaluate(()=>document.querySelector('button[aria-label^="关闭资产详情"]').click());
    await until(async()=>{const s=await page.evaluate(observe,gallery);return s.videos===0 && s.unreleasedPlayers===0;},'first player exit');
    phase='deep-pages';
    let maxAnchorDelta=0, maxDeepCards=0, highIndex=0;
    for(let i=2;i<=20;i++){
      step=i;
      await page.evaluate(()=>document.querySelector('.library-paging button').scrollIntoView({block:'end',behavior:'instant'}));
      await twoFrames(page);
      const before=await page.evaluate(anchor); assert.ok(before?.id);
      await page.evaluate(()=>document.querySelector('.library-paging button').click());
      await loaded(i*24); await twoFrames(page);
      const pos=await page.evaluate(id=>{
        const e=document.querySelector('button.library-card[data-asset-id="'+id+'"]');
        return e?e.getBoundingClientRect().top:null;
      },before.id);
      assert.notEqual(pos,null); maxAnchorDelta=Math.max(maxAnchorDelta,Math.abs(pos-before.top)); observed=Math.ceil(maxAnchorDelta);
      assert.ok(maxAnchorDelta<=2,'visible asset moved after append');
      const deep=await page.evaluate(()=>({count:document.querySelectorAll('.library-card').length,
        last:Math.max(...[...document.querySelectorAll('button.library-card')].map(x=>Number(x.dataset.index)))}));
      if(i>=4) {assert.ok(deep.count>0 && deep.count<=30);maxDeepCards=Math.max(maxDeepCards,deep.count);}
      highIndex=Math.max(highIndex,deep.last);
    }
    assert.ok(highIndex>=431);
    measurements.deep_pages={loaded_records:480,pages:20,max_mounted_cards_after_page_four:maxDeepCards,
      highest_mounted_index:highIndex,max_append_anchor_shift_css_px:maxAnchorDelta};
    measurements.samples.deep_gallery=await sample(page,gallery);
    await page.evaluate(()=>document.querySelector('main').scrollTo({top:0,behavior:'instant'}));
    await until(()=>page.evaluate(id=>document.querySelector('button.library-card[data-index="0"]')?.dataset.assetId===id,topIdentity),'same first identity');
    phase='handoff'; step=1;
    await play(videos[1]);
    assert.equal(mediaResponses.size,2);
    // Change project while the second player still owns a decoded stream.
    // DOM select dispatch follows the same React change handler even if a
    // native modal makes the toolbar inert; close through Escape first for a
    // native modal and assert explicit release separately.
    const modal=await page.evaluate(()=>Boolean(document.querySelector('dialog:modal')));
    if (modal) {
      await page.command('Input.dispatchKeyEvent',{type:'keyDown',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
      await page.command('Input.dispatchKeyEvent',{type:'keyUp',key:'Escape',code:'Escape',windowsVirtualKeyCode:27});
      await until(()=>page.evaluate(()=>!document.querySelector('dialog[open]')),'closed modal');
    }
    phase='project-cycle';
    let peakSubscriptions=0,maxOldSubscriptions=0,maxOldPlayers=0,final;
    for(let i=0;i<30;i++){
      step=i+1; observed=0;
      const project=projects[i], asset=thumbs[i];
      await page.fill('#project-select',project);
      await loaded(1);
      const stable=await until(async()=>{
        const s=await page.evaluate(observe,project);
        const correct=await page.evaluate((p,id)=>document.querySelector('#project-select')?.value===p &&
          document.querySelector('button.library-card')?.dataset.assetId===id,project,asset);
        return correct && s.cards===1 && s.decoded===1 && s.wrongScope===0 && s.projectOpen===1 &&
          s.subscriptions===1 && s.otherSubscriptions===0 && s.videos===0 && s.unreleasedPlayers===0 && s;
      },'expected private thumbnail and old resources released');
      peakSubscriptions=Math.max(peakSubscriptions,stable.subscriptions);
      maxOldSubscriptions=Math.max(maxOldSubscriptions,stable.otherSubscriptions);
      maxOldPlayers=Math.max(maxOldPlayers,stable.unreleasedPlayers);
      if([0,9,19,29].includes(i)) measurements.samples['switch_'+(i+1)]=await sample(page,project);
      final=stable;
    }
    assert.equal(final.maxPlaying,1);assert.equal(final.playing,0);assert.equal(final.imageErrors,0);
    assert.ok(final.starts>=31 && final.closes===final.starts-1);
    step=31; observed=measurements.samples.switch_30.dom_nodes_after_gc-measurements.samples.deep_gallery.dom_nodes_after_gc;
    assert.ok(measurements.samples.switch_30.dom_nodes_after_gc < measurements.samples.deep_gallery.dom_nodes_after_gc,'mounted and detached page resources failed to fall');
    measurements.post_gc_change_from_deep_to_final={
      dom_nodes:observed,
      js_heap_used_bytes:measurements.samples.switch_30.js_heap_used_bytes_after_gc-measurements.samples.deep_gallery.js_heap_used_bytes_after_gc
    };
    measurements.project_switches={distinct_projects:30,verified_decoded_private_thumbnails:30,
      max_active_subscriptions_at_settle:peakSubscriptions,max_previous_subscriptions_at_settle:maxOldSubscriptions,
      max_unreleased_old_players_at_settle:maxOldPlayers,project_subscriptions_opened:final.starts,project_subscriptions_closed_before_tab_exit:final.closes};
    measurements.video={decoded_distinct_video_sources:2,private_video_http_sources:mediaResponses.size,max_observed_playing:final.maxPlaying,
      after_final_switch:final.playing,second_preview_closed_by_modal:modal};
    phase='report';await page.close();
    process.stdout.write(JSON.stringify(measurements));
  });
}

try { await run(); } catch {
  process.stderr.write(JSON.stringify({phase,step,observed})+'\n');
  process.exitCode=1;
}
