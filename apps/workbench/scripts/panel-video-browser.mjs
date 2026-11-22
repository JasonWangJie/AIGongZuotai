import assert from 'node:assert/strict';
import { copyFile, mkdir, mkdtemp, readdir, stat, writeFile } from 'node:fs/promises';
import { isAbsolute, join } from 'node:path';
import { until, withLocalBrowser } from './local-browser.mjs';

function required(key) {
  const value = process.env['PANEL_PROBE_' + key];
  if (!value) throw Error('Missing local panel browser setting ' + key);
  return value;
}
const url = required('ORIGIN'), project = required('PROJECT'), timeline = required('TIMELINE');
const out = required('OUTPUT'), receipts = required('RECEIPTS');
if (!isAbsolute(out) || !isAbsolute(receipts)) throw Error('Expected owned absolute artifact paths');
async function control(page, name) {
  return until(() => page.evaluate(name => {
    const element = [...document.querySelectorAll('select,input')].find(node =>
      node.getAttribute('aria-label') === name || [...node.labels || []].some(label =>
        [...label.childNodes].filter(n => n.nodeType === Node.TEXT_NODE).map(n => n.textContent).join('').trim() === name));
    if (!element) return false;
    element.dataset.panelProbeControl = name;
    return '[data-panel-probe-control="' + CSS.escape(name) + '"]';
  }, name), 'visible form control ' + name);
}
const field = async (p, label, value) => p.fill(await control(p, label), String(value));
async function hasOption(p, name, value) {
  const selector = await control(p, name);
  return until(() => p.evaluate(([s, v]) => [...document.querySelector(s)?.options || []].some(o => o.value === v),
    [selector, value]), 'selection data ' + name);
}
async function capture(p, version) {
  const dir = await mkdtemp(join(out, 'download-v' + version + '-'));
  await p.downloads(dir);
  await p.button('导出 MP4', true);
  const file = await until(async () => {
    const entries = await readdir(dir);
    if (entries.some(n => n.endsWith('.crdownload'))) return false;
    const name = entries.find(n => n.endsWith('.mp4'));
    return name && (await stat(join(dir, name))).size > 100 ? name : false;
  }, 'authenticated MP4 download', 90_000);
  await copyFile(join(dir, file), join(out, 'timeline-v' + version + '.mp4'));
}

await mkdir(out, { recursive: true });
const launchEnv = {};
for (const name of ['SystemRoot','WINDIR','PATH','TEMP','TMP','TMPDIR','HOME','USERPROFILE','LOCALAPPDATA','APPDATA','LANG','LC_ALL','XDG_RUNTIME_DIR']) {
  if (process.env[name]) launchEnv[name] = process.env[name];
}
let stage = 'open';
try {
  await withLocalBrowser({ url, out, onlyOrigin: true, launchEnv, chromePath: process.env.WORKFLOW_CHROME }, async open => {
    const page = await open();
    await page.fill('#auth-email', required('EMAIL'));
    await page.fill('#auth-password', required('PASSWORD'));
    await page.button('进入创作空间', true);
    await page.button('漫剧分镜', true);
    stage = 'confirmed original version';
    await hasOption(page, '分镜版本', '1');
    await field(page, '分镜版本', 1);
    await until(() => page.evaluate(() => !!document.querySelector('[aria-label="单镜视频"]')), stage);
    await page.evaluate(() => document.querySelector('[aria-label="打开镜头 2"]').click());
    await until(() => page.evaluate(() => document.querySelector('.cb-panel-art h2')?.textContent.includes('归船靠岸') ||
      [...document.querySelectorAll('h2')].some(e => e.textContent === '归船靠岸')), 'selected second storyboard panel');
    await hasOption(page, '视频模型', 'studio-video');
    await field(page, '视频模型', 'studio-video');
    // Lose only the successful acknowledgements of these two real writes.
    // A later read-only receipt must recover each original resource.
    await page.evaluate(() => {
      const send = window.fetch;
      const proof = window.panelBrowserEvidence = { generate: [], replace: [], generateReads: 0, replaceReads: 0 };
      window.fetch = async (input, init) => {
        const path = new URL(input instanceof Request ? input.url : input, location.href).pathname;
        const method = String(init?.method || (input instanceof Request ? input.method : 'GET')).toUpperCase();
        const response = await send.call(window, input, init);
        if (method === 'GET' && response.ok && path.endsWith('/videos/receipt')) proof.generateReads++;
        if (method === 'GET' && response.ok && path.endsWith('/video-timelines/receipt')) proof.replaceReads++;
        const kind = method === 'POST' && path.endsWith('/panels/arrival/videos') ? 'generate' :
          method === 'POST' && path.endsWith('/replace-from-panel') ? 'replace' : '';
        if (kind) {
          const key = new Headers(init?.headers || (input instanceof Request ? input.headers : undefined)).get('Idempotency-Key');
          const row = { key, status: response.status };
          proof[kind].push(row);
          if (response.ok && proof[kind].length === 1) {
            const data = await response.clone().json();
            row.resource = kind === 'generate' ? data.task_id : data.id;
            row.version = data.version;
            throw TypeError('local fixture lost the response');
          }
        }
        return response;
      };
    });
    stage = 'review frozen prompt and price';
    await page.button('预览提示词与报价', true);
    await until(() => page.evaluate(() => {
      const text = document.querySelector('[aria-label="单镜视频"]')?.textContent || '';
      return text.includes('旧港') && text.includes('守灯人') && text.includes('提灯招手') && !text.includes('新草案中的动作') && text.includes('0.2');
    }), stage);
    stage = 'confirm and read original generated task receipt';
    await page.button('确认生成此镜视频', true);
    await page.button('按原键核对生成', true);
    await until(() => page.evaluate(() => window.panelBrowserEvidence.generateReads === 1), stage);
    await until(() => page.evaluate(async project => {
      const id = window.panelBrowserEvidence.generate[0]?.resource;
      if (!id) return false;
      const r = await fetch('/api/v1/tasks/' + encodeURIComponent(id), {headers:{'X-Project-ID':project}});
      if (!r.ok) return false;
      const t = await r.json();
      return t.status === 'SUCCEEDED' && t.moderation?.state === 'ALLOWED' && t.artifacts?.length === 1;
    }, project), 'approved actual task delivery', 60_000);
    await page.button('刷新视频尝试', true);
    await page.button('读取该次交付成片', true);
    stage = 'choose one approved video and original clip';
    const assetControl = await control(page, '交付成片');
    const asset = await until(() => page.evaluate(selector => {
      const options = document.querySelector(selector)?.options || [];
      const available = [...options].filter(o => o.value);
      return available.length === 1 && available[0].value;
    }, assetControl), 'one registered video in this attempt');
    await field(page, '交付成片', asset);
    await hasOption(page, '目标剪辑稿', timeline);
    await field(page, '目标剪辑稿', timeline);
    await hasOption(page, '目标镜头', 'shot2');
    await field(page, '目标镜头', 'shot2');
    await field(page, '新入点（毫秒）', 500);
    await field(page, '新出点（毫秒）', 1750);
    await page.screenshot('panel-clip-before-save');
    stage = 'replace selected clip and read original timeline receipt';
    await page.button('替换此镜头', true);
    await page.button('按原键核对替换', true);
    await until(() => page.evaluate(() => window.panelBrowserEvidence.replaceReads === 1), stage);
    const facts = await page.evaluate(() => window.panelBrowserEvidence);
    assert.equal(facts.generate.length, 1);
    assert.equal(facts.replace.length, 1);
    assert.equal(facts.generate[0].status, 201);
    assert.equal(facts.replace[0].status, 201);
    assert.equal(facts.replace[0].resource, timeline);
    assert.equal(facts.replace[0].version, 2);
    assert.ok(facts.generate[0].key && facts.replace[0].key && facts.generate[0].key !== facts.replace[0].key);
    stage = 'two private exports from saved revisions';
    await page.button('视频剪辑', true);
    await hasOption(page, '项目剪辑稿', timeline);
    await field(page, '项目剪辑稿', timeline);
    for (const version of [2, 1]) {
      await hasOption(page, '剪辑固定版本', String(version));
      await field(page, '剪辑固定版本', version);
      await until(() => page.evaluate(n => document.querySelector('[aria-label="剪辑固定版本"]')?.value === String(n) &&
        document.querySelectorAll('.vt-reel li button').length === 2, version), 'saved two-clip revision');
      await capture(page, version);
    }
    await writeFile(receipts, JSON.stringify({Generate:facts.generate[0].key,Replace:facts.replace[0].key}),{mode:0o600,flag:'wx'});
    await page.close();
  });
  console.log('confirmed source prompt and price; original task and replacement recovered through receipts; two authenticated MP4 downloads');
} catch {
  console.error('Panel video browser failed during ' + stage);
  process.exitCode = 1;
}
