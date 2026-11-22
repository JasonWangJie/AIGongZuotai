import assert from 'node:assert/strict';
import { mkdir, mkdtemp, readdir, copyFile, writeFile, rm, stat } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { withLocalBrowser, until } from './local-browser.mjs';
const env = process.env,
  required = (k) => {
    if (!env[k]) throw Error('Missing ' + k);
    return env[k];
  };
const url = required('PRESENTATION_BROWSER_URL'),
  out = resolve(required('PRESENTATION_BROWSER_OUTPUT')),
  project = required('PRESENTATION_PROJECT_ID'),
  asset = required('PRESENTATION_ASSET_ID'),
  hash = required('PRESENTATION_ASSET_SHA256');
assert.match(hash, /^[0-9a-f]{64}$/);
const name = '海岸观察手记',
  v1 = [
    {
      title: '沿岸观察',
      body: '用潮位、植被与步道记录海岸变化。',
      notes: '开场先说明记录的时间范围。',
      layout: 'title',
      image: false,
    },
    {
      title: '潮间带样本',
      body: '项目图片保留采样现场的颜色与边界。',
      notes: '请带听众观察图中边缘。',
      layout: 'image',
      image: true,
    },
    {
      title: '下一次踏查',
      body: '复核潮位基准，补齐两处缺失记录。',
      notes: '结束前确认下一次踏查时间。',
      layout: 'content',
      image: false,
    },
  ],
  v2 = [v1[0], { ...v1[2], title: '复核计划', body: '下次在同一位置记录晨潮与晚潮。' }, v1[1]];
await mkdir(out, { recursive: true });
const counts = new Map();
async function login(page) {
  await page.fill('#auth-email', required('PRESENTATION_EMAIL'));
  await page.fill('#auth-password', required('PRESENTATION_PASSWORD'));
  await page.button('进入创作空间', true);
  await page.button('演示文稿', true);
  await until(
    () =>
      page.evaluate(async (expected) => {
        const r = await fetch('/api/v1/bootstrap');
        if (!r.ok) return false;
        const b = await r.json();
        return (
          b.mode === 'demo' &&
          b.storage === 'postgres' &&
          b.auth_enabled &&
          b.project.id === expected &&
          !!document.querySelector('.pd-controls')
        );
      }, project),
    'authorized local editor',
  );
}
async function selectSlide(p, n) {
  await until(
    () =>
      p.evaluate((n) => {
        const b = document.querySelectorAll('.pd-rail li button')[n];
        if (!b) return false;
        b.click();
        return true;
      }, n),
    'select slide',
  );
}
async function current(p, version) {
  return until(
    () =>
      p.evaluate((n) => document.querySelector('[aria-label="固定版本"]')?.value.endsWith(':' + n), version),
    'saved version ' + version,
  );
}
async function capture(p, label, file) {
  const dir = await mkdtemp(join(out, '.browser-download-'));
  try {
    await p.downloads(dir);
    await p.button(label, true);
    const ext = file.split('.').pop();
    const item = await until(
      async () => {
        const names = await readdir(dir);
        if (names.some((v) => v.endsWith('.crdownload'))) return null;
        const n = names.find((v) => v.endsWith('.' + ext));
        return n && (await stat(join(dir, n))).size > 100 ? n : null;
      },
      'authorized ' + ext + ' download',
    );
    await copyFile(join(dir, item), join(out, file));
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}
async function loseSave(p) {
  await p.evaluate(() => {
    const before = window.fetch;
    window.fetch = async (...args) => {
      const r = await before(...args);
      if (
        r.ok &&
        args[1]?.method === 'POST' &&
        new URL(String(args[0]), location.href).pathname.endsWith('/presentations')
      ) {
        window.fetch = before;
        throw TypeError('Local fixture lost acknowledgement');
      }
      return r;
    };
  });
}
await withLocalBrowser(
  {
    url,
    out,
    chromePath: env.WORKFLOW_CHROME,
    onResponse: (r) => {
      const path = new URL(r.url).pathname;
      if (path.includes('/presentations')) {
        const label =
          (path.endsWith('/image')
            ? 'image'
            : path.endsWith('/receipt')
              ? 'receipt'
              : path.endsWith('/export')
                ? 'export'
                : 'presentations') +
          ':' +
          r.status;
        counts.set(label, (counts.get(label) || 0) + 1);
      }
    },
  },
  async (open) => {
    let stage = 'login',
      active;
    try {
      const p = (active = await open());
      await login(p);
      stage = 'outline and photo';
      await p.fill('[aria-label="文稿名称"]', name);
      await p.button('从材料生成大纲', true);
      await p.fill('[aria-label="材料或大纲"]', v1.map((s) => '# ' + s.title + '\n' + s.body).join('\n\n'));
      await p.button('生成可编辑页面', true);
      for (const [index, s] of v1.entries()) {
        await selectSlide(p, index);
        await p.fill('[aria-label="演讲备注"]', s.notes);
        if (s.image) {
          await p.fill('[aria-label="页面版式"]', 'image');
          await p.button('选择项目图片', true);
          await until(
            () =>
              p.evaluate((id) => {
                const b = [...document.querySelectorAll('.pd-picks button')].find(
                  (x) => x.dataset.asset === id,
                );
                if (!b) return false;
                b.click();
                return true;
              }, asset),
            'project fixture image',
          );
        }
      }
      await selectSlide(p, 1);
      await until(
        () =>
          p.evaluate(() => {
            const i = document.querySelector('.pd-board img');
            return i?.complete && i.naturalWidth > 0;
          }),
        'authorized private image preview',
      );
      await p.evaluate(() => document.querySelector('.pd-editor').scrollIntoView({ block: 'start' }));
      await p.screenshot('presentation-editor-desktop');
      stage = 'save and read receipt';
      await loseSave(p);
      await p.button('保存固定版本', true);
      await p.button('只读核对原保存回执', true);
      await current(p, 1);
      const id = await p.evaluate(() => document.querySelector('[aria-label="项目文稿"]').value);
      stage = 'fixed version formats';
      await capture(p, '下载 PowerPoint', 'presentation-v1.pptx');
      await capture(p, '下载离线 HTML', 'presentation-v1.html');
      await p.button('放映与打印', true);
      await until(() => p.evaluate(() => !!document.querySelector('.pd-show[open]')), 'show');
      await p.button('下一页', true);
      await until(
        () =>
          p.evaluate(() => {
            const img = document.querySelector('.pd-current img');
            return (
              !!img?.complete &&
              img.naturalWidth > 0 &&
              [...document.querySelectorAll('.pd-print img')].every((i) => i.complete && i.naturalWidth > 0)
            );
          }),
        'print image ready',
      );
      await p.screenshot('presentation-show-desktop');
      await p.printPDF(join(out, 'presentation-v1.pdf'));
      await p.button('结束放映', true);
      const other = (active = await open());
      stage = 'stale editor';
      await login(other);
      await until(
        () =>
          other.evaluate(
            (id) =>
              [...document.querySelectorAll('[aria-label="项目文稿"] option')].some((v) => v.value === id),
            id,
          ),
        'document in second session',
      );
      await other.fill('[aria-label="项目文稿"]', id);
      await current(other, 1);
      await other.fill('[aria-label="页正文"]', '第二窗口未保存的编辑');
      active = p;
      stage = 'edit and reorder';
      await selectSlide(p, 2);
      await p.fill('[aria-label="页标题"]', v2[1].title);
      await p.fill('[aria-label="页正文"]', v2[1].body);
      await p.button('上移页面', true);
      await p.fill('[aria-label="演示主题"]', 'mint');
      await p.button('保存固定版本', true);
      await current(p, 2);
      await capture(p, '下载 PowerPoint', 'presentation-v2.pptx');
      active = other;
      stage = 'version conflict';
      await other.button('刷新目录', true);
      assert.equal(
        await other.evaluate(() => document.querySelector('[aria-label="页正文"]').value),
        '第二窗口未保存的编辑',
      );
      await other.button('保存固定版本', true);
      await until(
        () => other.evaluate(() => document.body.textContent.includes('服务器版本已变化，当前编辑保留。')),
        'retained conflict',
      );
      assert.equal(
        await other.evaluate(() => document.querySelector('[aria-label="页正文"]').value),
        '第二窗口未保存的编辑',
      );
      active = p;
      stage = 'old version and mobile';
      await p.fill('[aria-label="固定版本"]', id + ':1');
      await current(p, 1);
      await capture(p, '下载 PowerPoint', 'presentation-v1-reopened.pptx');
      await p.size(390, 844);
      await selectSlide(p, 1);
      await p.evaluate(() => document.querySelector('.pd-editor').scrollIntoView({ block: 'start' }));
      assert.ok(
        await p.evaluate(
          () =>
            document.documentElement.scrollWidth <= innerWidth &&
            document.querySelector('.pd-board').getBoundingClientRect().width > 200,
        ),
        'mobile width',
      );
      await p.screenshot('presentation-editor-mobile');
      await writeFile(
        join(out, 'presentation-expected.json'),
        JSON.stringify(
          {
            asset_sha256: hash,
            versions: [
              { file: 'presentation-v1.pptx', title: name, theme: 'paper', slides: v1 },
              { file: 'presentation-v2.pptx', title: name, theme: 'mint', slides: v2 },
              { file: 'presentation-v1-reopened.pptx', title: name, theme: 'paper', slides: v1 },
            ],
          },
          null,
          2,
        ),
      );
      await other.close();
      await p.close();
      console.log(
        'PASS: outline, private image, receipt, stale edits, native PPTX versions, offline HTML, printable layout and mobile viewport.',
      );
    } catch (e) {
      const state =
        active &&
        (await active
          .evaluate(() => ({
            work: !!document.querySelector('.pd-work'),
            form: !!document.querySelector('.pd-fields'),
            slides: document.querySelectorAll('.pd-rail li').length,
            errors: document.querySelectorAll('.pd-alert').length,
            show: !!document.querySelector('.pd-show[open]'),
          }))
          .catch(() => null));
      console.error(
        'Presentation browser stage',
        stage,
        'DOM',
        JSON.stringify(state),
        'HTTP',
        JSON.stringify(Object.fromEntries(counts)),
      );
      throw e;
    }
  },
);
