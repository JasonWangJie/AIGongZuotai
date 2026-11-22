import assert from 'node:assert/strict';
import { copyFile, mkdir, mkdtemp, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { until, withLocalBrowser } from './local-browser.mjs';
function required(name) {
  const value = process.env[name];
  if (!value) throw Error('Missing ' + name);
  return value;
}
const url = required('VIDEO_EDIT_BROWSER_URL'),
  out = resolve(required('VIDEO_EDIT_BROWSER_OUTPUT')),
  project = required('VIDEO_EDIT_PROJECT_ID'),
  red = required('VIDEO_EDIT_RED_ASSET_ID'),
  blue = required('VIDEO_EDIT_BLUE_ASSET_ID');
const v1 = [
  { source: 'red', asset: red, title: '晨光开场', in_ms: 200, out_ms: 1400, tone_hz: 440 },
  { source: 'blue', asset: blue, title: '暮色转场', in_ms: 400, out_ms: 2000, tone_hz: 880 },
];
const v2 = [
  { ...v1[1], in_ms: 600 },
  { ...v1[0], in_ms: 400 },
];
const field = (p, name, value) => p.fill('[aria-label="' + name + '"]', String(value));
const counts = new Map();
async function login(page) {
  await page.fill('#auth-email', required('VIDEO_EDIT_EMAIL'));
  await page.fill('#auth-password', required('VIDEO_EDIT_PASSWORD'));
  await page.button('进入创作空间', true);
  await page.button('视频剪辑', true);
  await until(
    () =>
      page.evaluate(async (project) => {
        const r = await fetch('/api/v1/bootstrap');
        if (!r.ok) return false;
        const b = await r.json();
        return (
          b.mode === 'demo' &&
          b.auth_enabled &&
          b.storage === 'postgres' &&
          b.project.id === project &&
          !!document.querySelector('.vt-work')
        );
      }, project),
    'project-scoped authenticated editor',
  );
}
async function current(page, n) {
  await until(
    () => page.evaluate((n) => document.querySelector('[aria-label="剪辑固定版本"]')?.value === String(n), n),
    'fixed version ' + n,
  );
}
async function selectClip(p, n) {
  await until(
    () =>
      p.evaluate((n) => {
        const b = document.querySelectorAll('.vt-reel li button')[n - 1];
        if (!b) return false;
        b.click();
        return true;
      }, n),
    'select saved clip',
  );
}
async function pick(p, s) {
  await p.button('添加项目视频', true);
  await until(
    () =>
      p.evaluate((id) => {
        const b = [...document.querySelectorAll('[data-video-asset]')].find(
          (x) => x.dataset.videoAsset === id,
        );
        if (!b) return false;
        b.click();
        return true;
      }, s.asset),
    'authorized project video',
  );
  await field(p, '镜头名称', s.title);
  await field(p, '入点毫秒', s.in_ms);
  await field(p, '出点毫秒', s.out_ms);
}
async function preview(p, s) {
  await p.button('定位入点', true);
  await until(
    () =>
      p.evaluate((ms) => {
        const v = document.querySelector('.vt-screen video');
        return !!v && v.duration >= 2 && Math.abs(v.currentTime * 1000 - ms) < 90;
      }, s.in_ms),
    'seek original',
  );
  await p.trustedButton('预览此镜头');
  await until(
    () =>
      p.evaluate((ms) => {
        const v = document.querySelector('.vt-screen video');
        return !!v && !v.paused && v.currentTime * 1000 > ms + 70;
      }, s.in_ms),
    'playing private clip',
  );
  await until(
    () =>
      p.evaluate((ms) => {
        const v = document.querySelector('.vt-screen video');
        return !!v && v.paused && Math.abs(v.currentTime * 1000 - ms) < 130;
      }, s.out_ms),
    'preview stopped at cut',
  );
}
async function loseSave(p) {
  await p.evaluate(() => {
    const f = window.fetch;
    window.fetch = async (...args) => {
      const r = await f(...args);
      if (
        r.ok &&
        args[1]?.method === 'POST' &&
        new URL(String(args[0]), location.href).pathname.endsWith('/video-timelines')
      ) {
        window.fetch = f;
        throw TypeError('Local fixture lost acknowledgement');
      }
      return r;
    };
  });
}
async function capture(p, file) {
  const dir = await mkdtemp(join(out, '.mp4-download-'));
  try {
    await p.downloads(dir);
    await p.button('导出 MP4', true);
    const name = await until(
      async () => {
        const files = await readdir(dir);
        if (files.some((n) => n.endsWith('.crdownload'))) return false;
        const f = files.find((n) => n.endsWith('.mp4'));
        return f && (await stat(join(dir, f))).size > 100 ? f : false;
      },
      'download verified MP4',
      90000,
    );
    await copyFile(join(dir, name), join(out, file));
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}
async function value(p, label) {
  return p.evaluate((label) => document.querySelector('[aria-label="' + label + '"]')?.value, label);
}
async function waitShots(p, expected) {
  await until(
    () =>
      p.evaluate(
        (expected) =>
          JSON.stringify(
            [...document.querySelectorAll('.vt-reel li button span')].map((s) => s.firstChild.textContent),
          ) === JSON.stringify(expected),
        expected.map((s) => s.title),
      ),
    'ordered shots',
  );
}
await mkdir(out, { recursive: true });
await withLocalBrowser(
  {
    url,
    out,
    chromePath: process.env.WORKFLOW_CHROME,
    onResponse: (r) => {
      const path = new URL(r.url).pathname;
      if (path.includes('/video-timelines')) {
        const what = path.endsWith('/source')
          ? 'source'
          : path.endsWith('/export')
            ? 'export'
            : path.endsWith('/receipt')
              ? 'receipt'
              : 'timeline';
        const k = what + ':' + r.status;
        counts.set(k, (counts.get(k) || 0) + 1);
      }
    },
  },
  async (open) => {
    let phase = 'authenticate',
      active;
    try {
      const p = (active = await open());
      await login(p);
      phase = 'select and trim';
      await field(p, '剪辑稿名称', '晨暮镜头剪辑');
      for (const s of v1) await pick(p, s);
      await waitShots(p, v1);
      await preview(p, v1[1]);
      phase = 'read save receipt';
      await loseSave(p);
      await p.button('保存剪辑版本', true);
      await p.button('只读核对原保存回执', true);
      await current(p, 1);
      const id = await value(p, '项目剪辑稿');
      assert.ok(id);
      await preview(p, v1[0]);
      await p.evaluate(() => document.querySelector('.vt-grid').scrollIntoView({ block: 'start' }));
      await p.screenshot('timeline-editor-desktop');
      phase = 'download v1';
      await capture(p, 'timeline-v1.mp4');
      phase = 'second editor';
      const other = (active = await open());
      await login(other);
      await until(
        () =>
          other.evaluate(
            (id) =>
              [...document.querySelector('[aria-label="项目剪辑稿"]').options].some((o) => o.value === id),
            id,
          ),
        'previously saved timeline',
      );
      await field(other, '项目剪辑稿', id);
      await current(other, 1);
      await field(other, '镜头名称', '第二窗口的未保存镜头');
      active = p;
      phase = 'v2 reorder and trim';
      await selectClip(p, 2);
      await p.button('上移镜头', true);
      await field(p, '入点毫秒', v2[0].in_ms);
      await selectClip(p, 2);
      await field(p, '入点毫秒', v2[1].in_ms);
      await waitShots(p, v2);
      await p.button('保存剪辑版本', true);
      await current(p, 2);
      await capture(p, 'timeline-v2.mp4');
      active = other;
      phase = 'save conflict';
      await other.button('刷新剪辑目录', true);
      assert.equal(await value(other, '镜头名称'), '第二窗口的未保存镜头');
      await other.button('保存剪辑版本', true);
      await until(
        () => other.evaluate(() => document.body.textContent.includes('本页输入仍保留')),
        'conflict retained',
      );
      assert.equal(await value(other, '镜头名称'), '第二窗口的未保存镜头');
      active = p;
      phase = 'compare and restore';
      await field(p, '剪辑固定版本', 1);
      await current(p, 1);
      await waitShots(p, v1);
      await p.button('与最新版比较', true);
      await until(
        () => p.evaluate(() => document.querySelector('.vt-compare')?.textContent.includes('顺序')),
        'compare versions',
      );
      await p.size(390, 844);
      await preview(p, v1[0]);
      await p.evaluate(() => document.querySelector('.vt-monitor').scrollIntoView({ block: 'start' }));
      assert.ok(
        await p.evaluate(
          () =>
            document.documentElement.scrollWidth <= innerWidth &&
            document.querySelector('.vt-screen').getBoundingClientRect().width > 220,
        ),
        'mobile fit',
      );
      await p.screenshot('timeline-editor-mobile');
      await p.button('将此版恢复为新版本', true);
      await p.button('确认保存恢复版本', true);
      await current(p, 3);
      await waitShots(p, v1);
      assert.equal(counts.get('export:200'), 2);
      assert.ok(counts.get('receipt:200'));
      assert.ok((counts.get('source:200') || 0) + (counts.get('source:206') || 0) > 0);
      await writeFile(
        join(out, 'video-edit-expected.json'),
        JSON.stringify(
          {
            versions: [
              { file: 'timeline-v1.mp4', version: 1, duration_ms: 2800, clips: v1 },
              { file: 'timeline-v2.mp4', version: 2, duration_ms: 2400, clips: v2 },
            ],
            restored_version: 3,
          },
          null,
          2,
        ),
      );
      await other.close();
      await p.close();
      console.log(
        'PASS authenticated private MP4 preview, exact seek, two exports, original receipt, stale input, retained versions and 390px',
      );
    } catch (e) {
      try {
        await active?.screenshot('timeline-failure');
      } catch {}
      console.error('Video editor check failed at ' + phase, Object.fromEntries(counts));
      throw e;
    }
  },
);
