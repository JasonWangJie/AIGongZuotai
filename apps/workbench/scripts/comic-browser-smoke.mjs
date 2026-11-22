import assert from 'node:assert/strict';
import { copyFile, mkdir, mkdtemp, readdir, rm, stat, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { withLocalBrowser, until } from './local-browser.mjs';
function required(name) {
  const v = process.env[name];
  if (!v) throw Error('Missing ' + name);
  return v;
}
const url = required('COMIC_BROWSER_URL'),
  out = resolve(required('COMIC_BROWSER_OUTPUT')),
  project = required('COMIC_PROJECT_ID'),
  asset = required('COMIC_ASSET_ID'),
  sha = required('COMIC_ASSET_SHA256');
assert.match(sha, /^[a-f0-9]{64}$/);
const title = '潮声邮局',
  story = '信使在潮汐间送还一封旧信。',
  scene = '潮间站台',
  scenedesc = '月色下的旧站台。',
  castdesc = '携带黄铜邮筒的信使。';
const first = [
  { title: '抵达站台', action: '将邮筒放上长椅。', dialogue: '我找到地址了。' },
  { title: '寄出回信', action: '把旧信投入邮筒。', dialogue: '明天见。' },
];
const second = [{ ...first[0], title: '晨潮抵达', action: '提起邮筒走向站台。' }, first[1]];
const originalName = '阿岚',
  revisedName = '岚舟';
function expected(panels, name) {
  return panels.map((p) => ({
    title: p.title,
    body: ['场景：' + scene, '角色：' + name, '动作：' + p.action, '对白：' + p.dialogue].join('\n'),
    notes: ['故事梗概：' + story, '场景设定：' + scenedesc, '角色设定：' + name + '｜' + castdesc].join('\n'),
    image: true,
  }));
}
const field = (p, label, val) => p.fill('[aria-label="' + label + '"]', val);
async function login(p) {
  await p.fill('#auth-email', required('COMIC_EMAIL'));
  await p.fill('#auth-password', required('COMIC_PASSWORD'));
  await p.button('进入创作空间', true);
  await p.button('漫剧分镜', true);
  await until(
    () =>
      p.evaluate(async (project) => {
        const resp = await fetch('/api/v1/bootstrap');
        if (!resp.ok) return false;
        const b = await resp.json();
        return (
          b.mode === 'demo' &&
          b.storage === 'postgres' &&
          b.auth_enabled &&
          b.project.id === project &&
          !!document.querySelector('.cb-work')
        );
      }, project),
    'authenticated comic editor',
  );
}
const current = (p, n) =>
  until(
    () => p.evaluate((n) => document.querySelector('[aria-label="分镜版本"]')?.value === String(n), n),
    'fixed comic version ' + n,
  );
async function choose(p, n) {
  await until(
    () =>
      p.evaluate((n) => {
        const b = document.querySelector('[aria-label="打开镜头 ' + n + '"]');
        if (!b) return false;
        b.click();
        return true;
      }, n),
    'select comic panel',
  );
}
async function assign(p, v) {
  await field(p, '镜头标题', v.title);
  await field(p, '镜头动作', v.action);
  await field(p, '镜头对白', v.dialogue);
  await until(
    () =>
      p.evaluate(() => {
        const c = document.querySelector('.cb-cast input');
        if (!c || c.disabled) return false;
        if (!c.checked) c.click();
        return c.checked;
      }),
    'assign shared character',
  );
  await p.button('选择项目图片', true);
  await until(
    () =>
      p.evaluate((id) => {
        const b = [...document.querySelectorAll('[data-comic-asset]')].find(
          (x) => x.dataset.comicAsset === id,
        );
        if (!b) return false;
        b.click();
        return true;
      }, asset),
    'choose private project image',
  );
}
async function capture(p, file) {
  const dir = await mkdtemp(join(out, '.comic-download-'));
  try {
    await p.downloads(dir);
    await p.button('下载分镜 PowerPoint', true);
    const f = await until(
      async () => {
        const files = await readdir(dir);
        if (files.some((n) => n.endsWith('.crdownload'))) return false;
        const f = files.find((n) => n.endsWith('.pptx'));
        return f && (await stat(join(dir, f))).size > 100 ? f : false;
      },
      'editable PowerPoint download',
      45000,
    );
    await copyFile(join(dir, f), join(out, file));
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}
async function lose(p, op) {
  await p.evaluate((op) => {
    const previous = window.fetch;
    window.fetch = async (...args) => {
      const r = await previous(...args),
        path = new URL(String(args[0]), location.href).pathname;
      if (
        r.ok &&
        args[1]?.method === 'POST' &&
        (op === 'save' ? path.endsWith('/comic') : path.endsWith('/confirm'))
      ) {
        window.fetch = previous;
        throw TypeError('Local acknowledgement lost');
      }
      return r;
    };
  }, op);
}
async function confirm(p) {
  await p.button('作者确认此版', true);
  await p.button('确认此分镜版本', true);
}
const responses = new Map();
await mkdir(out, { recursive: true });
await withLocalBrowser(
  {
    url,
    out,
    chromePath: process.env.WORKFLOW_CHROME,
    onResponse: (r) => {
      const path = new URL(r.url).pathname;
      if (path.includes('/comic')) {
        const label =
          (path.endsWith('/image')
            ? 'image'
            : path.endsWith('/receipt')
              ? 'receipt'
              : path.endsWith('/export')
                ? 'export'
                : path.endsWith('/confirm')
                  ? 'confirm'
                  : 'comic') +
          ':' +
          r.status;
        responses.set(label, (responses.get(label) || 0) + 1);
      }
    },
  },
  async (open) => {
    let phase = 'login',
      active;
    try {
      const p = (active = await open());
      await login(p);
      await field(p, '分镜本名称', title);
      await field(p, '故事梗概', story);
      phase = 'linked cards';
      await p.button('角色与场景', true);
      await field(p, '角色名称 1', originalName);
      await field(p, '角色设定 1', castdesc);
      await field(p, '场景名称 1', scene);
      await field(p, '场景设定 1', scenedesc);
      await p.button('镜头编排', true);
      await assign(p, first[0]);
      await p.button('添加镜头', true);
      await assign(p, first[1]);
      await choose(p, 1);
      phase = 'save receipt';
      await lose(p, 'save');
      await p.button('保存分镜版本', true);
      await p.button('只读核对原保存回执', true);
      await current(p, 1);
      assert.equal(
        await p.evaluate(() =>
          [...document.querySelectorAll('.cb-footer button')].some(
            (b) => b.textContent === '下载分镜 PowerPoint',
          ),
        ),
        false,
      );
      phase = 'author confirmation receipt';
      await lose(p, 'confirm');
      await confirm(p);
      await p.button('只读核对原确认回执', true);
      await until(
        () =>
          p.evaluate(() => {
            const img = document.querySelector('.cb-preview img');
            return (
              img?.complete &&
              img.naturalWidth > 0 &&
              document.querySelector('.cb-footer')?.textContent.includes('作者已确认')
            );
          }),
        'authorized original illustration and confirmed version',
      );
      await p.evaluate(() => document.querySelector('.cb-panels').scrollIntoView({ block: 'start' }));
      await p.screenshot('comic-editor-desktop');
      await capture(p, 'comic-v1.pptx');
      phase = 'stale editor';
      const other = (active = await open());
      await login(other);
      await current(other, 1);
      await field(other, '镜头动作', '第二窗口未保存的动作。');
      active = p;
      phase = 'new version';
      await p.button('角色与场景', true);
      await field(p, '角色名称 1', revisedName);
      await p.button('镜头编排', true);
      await choose(p, 1);
      await field(p, '镜头标题', second[0].title);
      await field(p, '镜头动作', second[0].action);
      await choose(p, 2);
      assert.ok(
        await p.evaluate(
          (name) => document.querySelector('.cb-caption').textContent.includes(name),
          revisedName,
        ),
      );
      await p.button('保存分镜版本', true);
      await current(p, 2);
      await confirm(p);
      await capture(p, 'comic-v2.pptx');
      active = other;
      phase = 'CAS retains input';
      await other.button('刷新版本', true);
      await other.button('保存分镜版本', true);
      await until(
        () => other.evaluate(() => document.body.textContent.includes('本页输入仍保留')),
        'stale CAS',
      );
      assert.equal(
        await other.evaluate(() => document.querySelector('[aria-label="镜头动作"]').value),
        '第二窗口未保存的动作。',
      );
      active = p;
      phase = 'fixed historical cast and file';
      await field(p, '分镜版本', '1');
      await current(p, 1);
      await choose(p, 1);
      assert.equal(
        await p.evaluate(() => document.querySelector('[aria-label="镜头标题"]').value),
        first[0].title,
      );
      assert.ok(
        await p.evaluate(
          (name) => document.querySelector('.cb-caption').textContent.includes(name),
          originalName,
        ),
      );
      await capture(p, 'comic-v1-reopened.pptx');
      await p.size(390, 844);
      await p.evaluate(() => document.querySelector('.cb-preview').scrollIntoView({ block: 'start' }));
      assert.ok(
        await p.evaluate(
          () =>
            document.documentElement.scrollWidth <= innerWidth &&
            document.querySelector('.cb-preview').getBoundingClientRect().width > 220,
        ),
        '390px fit',
      );
      await p.screenshot('comic-editor-mobile');
      assert.ok((responses.get('image:200') || 0) >= 6, 'two real images per native export');
      assert.ok((responses.get('export:200') || 0) >= 6, 'manifest checked twice per native export');
      assert.ok((responses.get('receipt:200') || 0) >= 2);
      await writeFile(
        join(out, 'comic-expected.json'),
        JSON.stringify(
          {
            asset_sha256: sha,
            versions: [
              { file: 'comic-v1.pptx', slides: expected(first, originalName) },
              { file: 'comic-v2.pptx', slides: expected(second, revisedName) },
              { file: 'comic-v1-reopened.pptx', slides: expected(first, originalName) },
            ],
          },
          null,
          2,
        ),
      );
      if (process.env.COMIC_BROWSER_DENSE === '1') {
        phase = 'long native continuation pages';
        await field(p, '分镜版本', '2');
        await current(p, 2);
        await choose(p, 2);
        await p.button('移除此镜头',true);
        await choose(p, 1);
        const dense = {
          title: '潮汐来信',
          action: Array.from({ length: 35 }, (_, i) => '镜中潮汐第' + (i + 1) + '段。').join('\n'),
          dialogue: '回信寄向远方。'.repeat(100),
        };
        await field(p, '镜头标题', dense.title);
        await field(p, '镜头动作', dense.action);
        await field(p, '镜头对白', dense.dialogue);
        await p.button('保存分镜版本', true);
        await current(p, 3);
        await confirm(p);
        await capture(p, 'comic-dense.pptx');
        await writeFile(
          join(out, 'comic-dense-expected.json'),
          JSON.stringify({ title: dense.title, body: expected([dense],revisedName)[0].body, asset_sha256: sha }, null, 2),
        );
      }
      await other.close();
      await p.close();
      console.log(
        'PASS two private illustrated shots, shared cast, separate save/approval receipts, stale edits, historical and current native PowerPoint and 390px',
      );
    } catch (e) {
      try {
        await active?.screenshot('comic-failure');
      } catch {}
      console.error('Comic browser check failed at ' + phase, Object.fromEntries(responses));
      throw e;
    }
  },
);
