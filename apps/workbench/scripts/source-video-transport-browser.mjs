import assert from 'node:assert/strict';
import { until, withLocalBrowser } from './local-browser.mjs';

const env = process.env;
const url = env.SOURCE_PROBE_ORIGIN;
const selected = JSON.stringify([env.SOURCE_PROBE_ASSET, 1]);
const launchEnv = {};
for (const key of ['SystemRoot','WINDIR','PATH','TEMP','TMP','TMPDIR','HOME','USERPROFILE','LOCALAPPDATA','APPDATA','LANG','LC_ALL','XDG_RUNTIME_DIR']) {
  if (env[key]) launchEnv[key] = env[key];
}

// Executed in the actual target page before application code. Forward the
// original arguments, receiver and Promise unchanged, including on failure.
function observePageFetch() {
  const nativeFetch = window.fetch;
  const calls = { posts: 0, receipts: 0 };
  Object.defineProperty(window, '__sourceTransportCounts', {value: calls});
  window.fetch = function (input, init) {
    try {
      const target = new URL(input instanceof Request ? input.url : input, location.href);
      const method = String(init?.method || (input instanceof Request && input.method) || 'GET').toUpperCase();
      if (target.origin === location.origin) {
        if (method === 'POST' && target.pathname === '/api/v1/tasks') calls.posts++;
        if (method === 'GET' && target.pathname === '/api/v1/tasks/receipt') calls.receipts++;
      }
    } catch {}
    return Reflect.apply(nativeFetch, this, arguments);
  };
}

async function traffic() {
  const response = await fetch(new URL('/__source-transport/counts', url), {redirect:'error'});
  assert.equal(response.status, 200);
  return response.json();
}

let stage = 'login';
try {
  const result = await withPage();
  console.log(JSON.stringify(result));
} catch {
  console.error('transport step ' + stage + ' failed');
  process.exitCode = 1;
}

async function withPage() {
  let result;
  await withLocalBrowser({url, onlyOrigin:true, launchEnv, chromePath:env.WORKFLOW_CHROME}, async open => {
    const page = await open({width:1440, height:1080, language:'zh-CN', deferNavigation:true});
    await page.command('Page.addScriptToEvaluateOnNewDocument', {source:'('+observePageFetch.toString()+')()'});
    await page.command('Page.navigate',{url});
    await page.fill('#auth-email',env.SOURCE_PROBE_EMAIL);
    await page.fill('#auth-password',env.SOURCE_PROBE_PASSWORD);
    await page.button('进入创作空间',true);
    stage = 'source';
    await page.button('视频工作室');
    await page.button('延长原视频',true);
    const sourceField = '.source-video-fields label:first-of-type select';
    const directionField = '.source-video-fields label:nth-of-type(2) select';
    await until(() => page.evaluate((field, value) =>
      [...document.querySelector(field)?.options || []].some(o => o.value === value && o.text.includes('修订 1')), sourceField,selected),'approved source');
    await page.fill(sourceField,selected);
    await page.fill(directionField,'forward');
    await page.fill('#creation-prompt','video_extend fixture');
    await until(() => page.evaluate((s,d,v) =>
      document.querySelector(s)?.value === v && document.querySelector(d)?.value === 'forward' &&
      document.querySelector('#creation-prompt')?.value === 'video_extend fixture' &&
      document.querySelector('.composer .submit-button')?.disabled === false, sourceField,directionField,selected),'selected current revision');
    stage = 'quote';
    await page.button('查看本次报价',true);
    await until(() => page.evaluate(asset => {
      const d = document.querySelector('.quote-confirmation');
      return !!d?.querySelector('button.primary:not(:disabled)') && d.textContent.includes('0.35') &&
        d.textContent.includes('向后延长') && d.textContent.includes(asset + ' · 修订 1');
    }, env.SOURCE_PROBE_ASSET),'bound extension quote');
    assert.deepEqual(await traffic(),{quotes:1,posts:0,receiptGets:0,replayed:0});
    stage = 'submit';
    await page.button('确认额度并提交',true);
    stage = 'outcome';
    const outcome = await until(async () => {
      const edge = await traffic();
      if (edge.posts === 2 && edge.replayed === 1) return 'transport_replay';
      if (edge.posts === 1 && await page.evaluate(() =>
        [...document.querySelectorAll('[role="alert"]')].some(el => el.textContent.includes('受理状态待确认')) &&
        [...document.querySelectorAll('button')].some(b => b.textContent.trim() === '查询原任务收据' && !b.disabled) &&
        document.querySelector('.submit-button')?.disabled)) return 'manual_receipt';
      return false;
    }, 'browser transport outcome', 60_000);
    if (outcome === 'manual_receipt') {
      stage = 'receipt';
      await page.button('查询原任务收据',true);
    }
    stage = 'delivery';
    await until(() => page.evaluate(() =>
      [...document.querySelectorAll('.task-card')].some(c => c.querySelector('.task-prompt')?.textContent === 'video_extend fixture' &&
        c.querySelector('.task-kind')?.textContent.includes('延长原视频') && !!c.querySelector('.state-succeeded')) &&
      ![...document.querySelectorAll('button')].some(b => b.textContent.trim() === '查询原任务收据')), 'original delivered task', 90_000);
    stage = 'counts';
    const calls = await page.evaluate(() => window.__sourceTransportCounts);
    assert.deepEqual(calls,{posts:1,receipts:outcome === 'manual_receipt' ? 1 : 0});
    assert.deepEqual(await traffic(),{quotes:1,posts:outcome === 'manual_receipt' ? 1 : 2,receiptGets:calls.receipts,replayed:outcome === 'manual_receipt' ? 0 : 1});
    result = {pageTaskPosts:calls.posts,pageReceiptGets:calls.receipts,outcome};
    await page.close();
  });
  return result;
}
