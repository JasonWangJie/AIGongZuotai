import assert from 'node:assert/strict';
import { until, withLocalBrowser } from './local-browser.mjs';

const env = process.env;
const url = env.SOURCE_PROBE_ORIGIN, source = env.SOURCE_PROBE_ASSET;
if (!url || !source || !env.SOURCE_PROBE_EMAIL || !env.SOURCE_PROBE_PASSWORD) {
  throw Error('Missing required local browser fixture settings');
}
const labels = { video_edit: '编辑原视频', video_extend: '延长原视频' };
const sourceSelect = '.source-video-fields label:first-of-type select';
const directionSelect = '.source-video-fields label:nth-of-type(2) select';
const launchEnv = {};
for (const key of ['SystemRoot','WINDIR','PATH','TEMP','TMP','TMPDIR','HOME','USERPROFILE','LOCALAPPDATA','APPDATA','LANG','LC_ALL','XDG_RUNTIME_DIR']) {
  if (env[key]) launchEnv[key] = env[key];
}

async function assertTraffic(quotes, posts, receipts) {
  const r = await fetch(new URL('/__source-video-probe/counts', url), {redirect:'error'});
  assert.equal(r.status, 200);
  assert.deepEqual(await r.json(), {quotes, posts, receipts});
}

async function inspectQuote(page, operation) {
  const label = labels[operation];
  checkpoint = 'operation';
  await page.button(label, true);
  const value = JSON.stringify([source, 1]);
  checkpoint = 'source-list';
  await until(() => page.evaluate((selector, value) =>
    [...document.querySelector(selector)?.options || []].some(o => o.value === value && o.text.includes('修订 1')),
    sourceSelect, value), 'approved original revision');
  await page.fill(sourceSelect, value);
  if (operation === 'video_extend') await page.fill(directionSelect, 'forward');
  await page.fill('#creation-prompt', operation + ' fixture');
  checkpoint = 'form-ready';
  await until(() => page.evaluate((selector, value, op) =>
    document.querySelector(selector)?.value === value &&
    (op !== 'video_extend' || document.querySelector('.source-video-fields label:nth-of-type(2) select')?.value === 'forward') &&
    document.querySelector('.composer .submit-button')?.disabled === false &&
    document.querySelector('#creation-prompt')?.value === op + ' fixture',
    sourceSelect, value, operation), 'authorized revision ready for its first quote');
  checkpoint = 'request-quote';
  await page.button('查看本次报价', true);
  checkpoint = 'price';
  await until(() => page.evaluate((op, label, source) => {
    const dialog = document.querySelector('.quote-confirmation');
    const text = dialog?.textContent || '';
    return !!dialog?.querySelector('button.primary:not(:disabled)') &&
      text.includes(label) && text.includes(source + ' · 修订 1') &&
      text.includes(op === 'video_edit' ? '0.2' : '0.35') &&
      (op === 'video_edit' || text.includes('向后延长'));
  }, operation, label, source), 'quote for selected source and operation');
}

async function assertNarrowSourceQuote(page, assetID) {
  const layout = await page.evaluate(id => {
    const quote = document.querySelector('.quote-confirmation');
    if (!quote) return null;
    const sourceLine = [...quote.querySelectorAll('p')].find(p => p.textContent.includes(id));
    const text = sourceLine && [...sourceLine.childNodes].find(n => n.nodeType === Node.TEXT_NODE && n.textContent.includes(id));
    if (!text) return null;
    const start = text.textContent.indexOf(id), range = new Range();
    range.setStart(text, start);
    range.setEnd(text, start + id.length);
    const bounds = sourceLine.getBoundingClientRect(), lines = [...range.getClientRects()];
    return {
      width: document.documentElement.clientWidth,
      lines: lines.length,
      fits: sourceLine.scrollWidth <= sourceLine.clientWidth + 1 &&
        lines.every(line => line.left >= bounds.left - 1 && line.right <= bounds.right + 1),
    };
  }, assetID);
  assert.ok(layout && layout.width === 390 && layout.fits && layout.lines >= 2,
    'the full approved asset identifier must wrap within the narrow quote');
}

async function deliveredCard(page, op) {
  await until(() => page.evaluate((prompt, label) =>
    [...document.querySelectorAll('.task-card')].some(card =>
      card.querySelector('.task-prompt')?.textContent === prompt &&
      card.querySelector('.task-kind')?.textContent.includes(label) &&
      !!card.querySelector('.state-succeeded')), op + ' fixture', labels[op]), 'approved task in workbench', 90_000);
}

let stage = 'login';
let checkpoint = 'navigation';
try {
  await withLocalBrowser({ url, onlyOrigin: true, launchEnv, chromePath: env.WORKFLOW_CHROME }, async open => {
    const page = await open({width: 1440, height: 1080, language: 'zh-CN'});
    await page.fill('#auth-email', env.SOURCE_PROBE_EMAIL);
    await page.fill('#auth-password', env.SOURCE_PROBE_PASSWORD);
    await page.button('进入创作空间', true);
    stage = 'open video studio';
    await page.button('视频工作室');
    stage = 'edit quote and confirmation';
    await inspectQuote(page, 'video_edit');
    checkpoint = 'traffic';
    await assertTraffic(1, 0, 0);
    checkpoint = 'confirm';
    await page.button('确认额度并提交', true);
    checkpoint = 'delivery';
    await deliveredCard(page, 'video_edit');
    stage = 'extension quote and confirmation';
    await page.size(390, 844);
    await inspectQuote(page, 'video_extend');
    await assertNarrowSourceQuote(page, source);
    checkpoint = 'traffic';
    await assertTraffic(2, 1, 0);
    checkpoint = 'confirm';
    await page.button('确认额度并提交', true);
    stage = 'lost response and original receipt';
    checkpoint = 'unknown';
    await until(() => page.evaluate(() =>
      [...document.querySelectorAll('[role="alert"]')].some(el => el.textContent.includes('受理状态待确认')) &&
      [...document.querySelectorAll('button')].some(b => b.textContent.trim() === '查询原任务收据' && !b.disabled) &&
      document.querySelector('.submit-button')?.disabled), stage);
    await assertTraffic(2, 2, 0);
    checkpoint = 'receipt';
    await page.button('查询原任务收据', true);
    await until(() => page.evaluate(() =>
      ![...document.querySelectorAll('button')].some(b => b.textContent.trim() === '查询原任务收据')), 'receipt matched original request');
    await deliveredCard(page, 'video_extend');
    checkpoint = 'traffic';
    await assertTraffic(2, 2, 1);
    assert.ok(await page.evaluate(() => [...document.querySelectorAll('.task-card .state-succeeded')].length >= 2));
    await page.close();
  });
  console.log(JSON.stringify({workbench:'视频工作室', operations:Object.values(labels),
    confirmation:'确认额度并提交', lostResponse:'受理状态待确认', recovery:'查询原任务收据', deliveredCards:2}));
} catch {
  console.error('Source-video browser failed during ' + stage + ' / ' + checkpoint);
  process.exitCode = 1;
}
