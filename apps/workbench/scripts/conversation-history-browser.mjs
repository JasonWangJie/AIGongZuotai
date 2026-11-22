import assert from 'node:assert/strict';
import { until, withLocalBrowser } from './local-browser.mjs';

let phase = 'auth',
  step = 0;
let loadingState;
const signal = AbortSignal.timeout(190_000);

// The delayed branch holds the bytes of a completed, authenticated GET. The
// application still observes its own AbortSignal and selection generation.
// No response body, identity, session or cursor is exported in the report.
function instrument() {
  const nativeFetch = window.fetch.bind(window);
  const state = { pages: [], posts: 0, hold: '', held: false, released: false, aborted: false, resume: null };
  window.historyProbe = state;
  window.fetch = async (resource, init) => {
    const u = new URL(resource instanceof Request ? resource.url : resource, location.href);
    const method = init?.method || (resource instanceof Request ? resource.method : 'GET');
    if (u.origin === location.origin && method === 'POST' && /\/conversations\/[^/]+\/runs$/.test(u.pathname))
      state.posts++;
    const response = await nativeFetch(resource, init);
    const match = /^\/api\/v1\/conversations\/([^/]+)\/messages$/.exec(u.pathname);
    if (u.origin !== location.origin || !match || !response.ok || method !== 'GET') return response;
    const delayed = state.hold === match[1] && u.searchParams.has('before');
    const bytes = await response.clone().arrayBuffer();
    const body = JSON.parse(new TextDecoder().decode(bytes));
    state.pages.push({
      id: match[1],
      limit: u.searchParams.get('limit'),
      cursor: u.searchParams.get('before') || '',
      rows: body.items.map((m) => ({ id: m.id, role: m.role, content: m.content })),
      more: body.has_more,
    });
    if (!delayed) return response;
    state.hold = '';
    state.held = true;
    await new Promise((resolve) => {
      state.resume = resolve;
    });
    state.aborted = Boolean((init?.signal || resource?.signal)?.aborted);
    state.released = true;
    return new Response(bytes, { status: response.status, headers: response.headers });
  };
}

const frames = (page) =>
  page.evaluate(
    () =>
      new Promise((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => requestAnimationFrame(resolve))),
      ),
  );
const key = async (page, name, code, vk) => {
  await page.command('Input.dispatchKeyEvent', {
    type: name === 'Enter' ? 'keyDown' : 'rawKeyDown',
    ...(name === 'Enter' ? { text: '\r', unmodifiedText: '\r' } : {}),
    key: name,
    code,
    windowsVirtualKeyCode: vk,
  });
  await page.command('Input.dispatchKeyEvent', { type: 'keyUp', key: name, code, windowsVirtualKeyCode: vk });
};

function snapshot() {
  const timeline = document.querySelector('.chat-timeline[role=region]');
  if (!timeline || timeline.querySelector('[data-body-pending]')) return null;
  const top = timeline.getBoundingClientRect().top + timeline.clientTop;
  const rows = [...timeline.querySelectorAll('[data-message-id][data-index]')].map((e) => {
    const box = e.getBoundingClientRect();
    return {
      id: e.dataset.messageId,
      index: Number(e.dataset.index),
      count: Number(e.getAttribute('aria-setsize')),
      position: Number(e.getAttribute('aria-posinset')),
      content: e.querySelector('.chat-message-content')?.textContent,
      role: e.querySelector('.chat-message.user')
        ? 'user'
        : e.querySelector('.chat-message.assistant')
          ? 'assistant'
          : '',
      top: box.top - top,
      bottom: box.bottom - top,
    };
  });
  return {
    rows,
    height: timeline.clientHeight,
    top: timeline.scrollTop,
    max: timeline.scrollHeight - timeline.clientHeight,
    draft: document.querySelector('#chat-input')?.value,
    anchor: rows.find((r) => r.bottom > 0 && r.top < timeline.clientHeight),
  };
}

// Evicted rows keep their identity and height while the original authorized
// page is read again. Count a body only after that request has completed.
const readSnapshot = page => until(() => page.evaluate(snapshot), 'mounted message bodies');

async function run(cfg) {
  const { main, sibling } = cfg;
  const draft = '草稿在翻页与项目切换后仍须保留';
  let peak = 0,
    errorPx = 0,
    earlyPages = 0,
    firstMount = 0;
  let readsOnMain = 0;
  let widthErrorPx = 0,
    chromiumProduct;
  const seen = new Set();
  const shape = (state, list, count) => {
    assert.ok(state?.rows.length > 0 && state.rows.length <= 24, 'bounded mounted window');
    assert.ok(count >= state.rows.length);
    const base = list.length - count;
    for (const row of state.rows) {
      const expect = list[base + row.index];
      assert.ok(expect && row.count === count && row.position === row.index + 1);
      assert.equal(row.id, expect.id);
      assert.equal(row.role, expect.role);
      assert.equal(row.content, expect.content);
      if (list === main.messages && row.bottom > 0 && row.top < state.height) seen.add(row.id);
    }
    peak = Math.max(peak, state.rows.length);
  };
  await withLocalBrowser(
    { url: cfg.url, chromePath: process.env.WORKFLOW_CHROME, onlyOrigin: true, signal },
    async (open) => {
      const page = await open({ width: 1280, height: 900, deferNavigation: true });
      chromiumProduct = (await page.command('Browser.getVersion')).product;
      await page.command('Page.addScriptToEvaluateOnNewDocument', {
        source: '(' + instrument.toString() + ')()',
      });
      await page.command('Page.navigate', { url: cfg.url });
      await page.fill('#auth-email', cfg.email);
      await page.fill('#auth-password', cfg.password);
      await page.button('进入创作空间', true);
      await until(
        () => page.evaluate(() => Boolean(document.querySelector('#project-select')?.value)),
        'login',
      );
      const selectProject = async (project) => {
        if ((await page.evaluate(() => document.querySelector('#project-select').value)) !== project)
          await page.fill('#project-select', project);
        await until(
          () => page.evaluate((id) => document.querySelector('#project-select')?.value === id, project),
          'project',
        );
      };
      const selectTree = async (tree) => {
        await selectProject(tree.project);
        await page.button('灵感对话');
        await page.button(tree.title);
      };
      const count = async (n) => {
        try {
          await until(
            () =>
              page.evaluate(
                (n) => Number(document.querySelector('.chat-window-row')?.getAttribute('aria-setsize')) === n,
                n,
              ),
            'loaded page',
          );
        } catch (err) {
          loadingState = await page.evaluate(() => ({
            count: Number(document.querySelector('.chat-window-row')?.getAttribute('aria-setsize') || 0),
            reads: window.historyProbe.pages.length,
            olderReads: window.historyProbe.pages.filter((p) => p.cursor).length,
            focused: document.activeElement === document.querySelector('.timeline-more'),
            busy: document.querySelector('.timeline-more')?.getAttribute('aria-disabled') === 'true',
          }));
          throw err;
        }
        await frames(page);
      };
      const scroll = async (move, value = 0) => {
        await page.evaluate(
          (mode, n) => {
            const el = document.querySelector('.chat-timeline');
            el.scrollTop = mode === 'end' ? el.scrollHeight : mode === 'by' ? el.scrollTop + n : n;
          },
          move,
          value,
        );
        await frames(page);
      };
      const chooseEarlier = async () => {
        await page.evaluate(() => document.querySelector('.timeline-more').focus({ preventScroll: true }));
        await key(page, 'Enter', 'Enter', 13);
      };
      phase = 'first-page';
      await selectTree(main);
      await count(20);
      const first = await readSnapshot(page);
      shape(first, main.messages, 20);
      firstMount = first.rows.length;
      await page.evaluate(() => document.querySelector('#chat-input').focus());
      await page.command('Input.insertText', { text: draft });

      phase = 'prepend';
      for (let loaded = 20; loaded < 128; loaded = Math.min(128, loaded + 20)) {
        step++;
        await scroll('to', 105);
        const prior = await readSnapshot(page);
        shape(prior, main.messages, loaded);
        assert.equal(prior.draft, draft);
        assert.ok(prior.anchor);
        await chooseEarlier();
        const total = Math.min(128, loaded + 20);
        await count(total);
        const after = await readSnapshot(page);
        shape(after, main.messages, total);
        assert.equal(after.draft, draft);
        const same = after.rows.find((r) => r.id === prior.anchor.id);
        assert.ok(same, 'prepend retained anchor');
        const delta = Math.abs(same.top - prior.anchor.top);
        errorPx = Math.max(errorPx, delta);
        assert.ok(delta <= 1, 'stable visual offset');
        earlyPages++;
      }
      assert.equal(await page.evaluate(() => Boolean(document.querySelector('.timeline-more'))), false);
      const pages = await page.evaluate(
        (id) => window.historyProbe.pages.filter((p) => p.id === id),
        main.conversation,
      );
      const originals = new Map();
      for (const pageRead of pages) {
        const original = originals.get(pageRead.cursor);
        if (original) assert.deepEqual(pageRead, original, 'a repeated cursor returns its original page');
        else originals.set(pageRead.cursor, pageRead);
      }
      assert.equal(originals.size, 7);
      assert.equal(originals.keys().next().value, '');
      let end = 128;
      for (const p of originals.values()) {
        assert.equal(p.limit, '20');
        const start = Math.max(0, end - 20);
        assert.deepEqual(p.rows, main.messages.slice(start, end));
        assert.equal(p.more, start > 0);
        end = start;
      }
      assert.equal(end, 0);
      // Visit the full loaded timeline through the real scroll container. A
      // detached row cannot count as visibly read.
      await scroll('to', 0);
      for (let i = 0; i < 400; i++) {
        const view = await readSnapshot(page);
        shape(view, main.messages, 128);
        if (view.top >= view.max - 1) break;
        await scroll('by', Math.max(30, Math.floor(view.height / 2)));
      }
      assert.equal(seen.size, 128, 'all historical messages actually visible');

      phase = 'resize';
      await scroll('to', (await readSnapshot(page)).max / 2);
      const desktop = await readSnapshot(page);
      const beforeWidth = await page.evaluate(() => document.querySelector('.chat-timeline').clientWidth);
      await page.size(390, 844);
      await frames(page);
      await frames(page);
      const narrow = await readSnapshot(page);
      shape(narrow, main.messages, 128);
      assert.ok(
        (await page.evaluate(() => document.querySelector('.chat-timeline').clientWidth)) < beforeWidth,
      );
      const sameRow = narrow.rows.find((row) => row.id === desktop.anchor?.id);
      assert.ok(sameRow, 'narrow viewport retained the visible message');
      widthErrorPx = Math.abs(sameRow.top - desktop.anchor.top);
      assert.ok(widthErrorPx <= 1, 'narrow viewport retained the reading position');
      assert.equal(narrow.draft, draft);
      await page.size(1280, 900);
      await frames(page);
      await frames(page);

      phase = 'keyboard';
      await scroll('to', 0);
      await readSnapshot(page);
      await page.evaluate(() => {
        const row = document.querySelector('[data-index="0"]');
        row.querySelector('button:last-of-type').focus({ preventScroll: true });
      });
      await scroll('end');
      assert.ok(
        await page.evaluate(
          () =>
            document.activeElement?.closest('[data-index]')?.dataset.index === '0' &&
            !document.querySelector('[data-index="1"]'),
        ),
      );
      await key(page, 'Tab', 'Tab', 9);
      await until(
        () => page.evaluate(() => document.activeElement?.closest('[data-index]')?.dataset.index === '1'),
        'keyboard neighbor',
      );
      assert.equal((await readSnapshot(page)).draft, draft);

      const readbacks = await page.evaluate(
        id => window.historyProbe.pages.filter(p => p.id === id),
        main.conversation,
      );
      for (const pageRead of readbacks)
        assert.deepEqual(pageRead, originals.get(pageRead.cursor), 'refilled body must match original authorized page');
      readsOnMain = readbacks.length;

      phase = 'scope-switch';
      await selectTree(sibling);
      await count(2);
      shape(await readSnapshot(page), sibling.messages, 2);
      await selectTree(main);
      await count(20);
      assert.equal((await readSnapshot(page)).draft, draft);
      await page.evaluate((id) => {
        window.historyProbe.hold = id;
      }, main.conversation);
      await chooseEarlier();
      await until(() => page.evaluate(() => window.historyProbe.held), 'completed GET held');
      await selectTree(sibling);
      await count(2);
      await page.evaluate(() => window.historyProbe.resume());
      await until(() => page.evaluate(() => window.historyProbe.released), 'released GET');
      await frames(page);
      const separate = await readSnapshot(page);
      shape(separate, sibling.messages, 2);
      assert.equal(separate.draft, '');
      assert.equal(await page.evaluate(() => window.historyProbe.aborted), true);
      await selectTree(main);
      await count(20);
      const restored = await readSnapshot(page);
      shape(restored, main.messages, 20);
      assert.equal(restored.draft, draft);
      assert.equal(await page.evaluate(() => window.historyProbe.posts), 0);
      await page.close();
    },
  );
  phase = 'report';
  return {
    schema_version: 1,
    fixture: 'authenticated_pg_035_branch_128',
    history_rows_seen: seen.size,
    first_page_received: 20,
    chromium_product: chromiumProduct,
    viewport_css_px: [1280, 900],
    narrow_viewport_css_px: [390, 844],
    narrow_anchor_displacement_css_px: widthErrorPx,
    first_page_mounted: firstMount,
    authenticated_browser_pages: earlyPages + 1,
    original_branch_page_reads_before_switch: readsOnMain,
    repeated_original_cursor_reads: readsOnMain - (earlyPages + 1),
    prepend_operations: earlyPages,
    max_sampled_mounted_rows: peak,
    max_anchor_displacement_css_px: errorPx,
    real_tab_to_unmounted_neighbor: true,
    original_draft_restored: true,
    late_response_isolated: true,
    browser_generation_posts: 0,
  };
}

try {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  const report = await run(JSON.parse(Buffer.concat(chunks).toString('utf8')));
  process.stdout.write(JSON.stringify(report) + '\n');
} catch (error) {
  // Include only an owned source line, never raw browser errors or fixture text.
  const match = String(error?.stack).match(/conversation-history-browser\.mjs:(\d+):\d+/);
  process.stderr.write(
    JSON.stringify({ phase, step, line: match ? Number(match[1]) : 0, loadingState }) + '\n',
  );
  process.exitCode = 1;
}
