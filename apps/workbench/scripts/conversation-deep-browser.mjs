import assert from 'node:assert/strict';
import { until, withLocalBrowser } from './local-browser.mjs';

let phase = 'setup';
let step = 0;

// Counts and SHA comparisons only. Even on a full pass this observer retains
// no message bodies. The digest table was calculated independently by Go
// from the real registered user's history and its original accepted turns.
function observe(config) {
  const fetchOriginal = window.fetch.bind(window);
  const state = { hits: Array(config.digests.length).fill(0), cursors: [], invalid: false, posts: 0 };
  window.deepHistoryProbe = state;
  window.fetch = async (path, init) => {
    const url = new URL(path instanceof Request ? path.url : path, location.href);
    const method = init?.method || (path instanceof Request ? path.method : 'GET');
    const target = '/api/v1/conversations/' + config.conversation;
    if (url.origin === location.origin && method === 'POST' && url.pathname === target + '/runs') state.posts++;
    const response = await fetchOriginal(path, init);
    if (url.origin !== location.origin || url.pathname !== target + '/messages' || method !== 'GET') return response;
    try {
      const raw = await response.clone().arrayBuffer();
      if (!response.ok || raw.byteLength > (1 << 20)) throw Error();
      const page = JSON.parse(new TextDecoder().decode(raw));
      if (page.leaf_message_id !== config.leaf || page.conversation_version !== config.turns + 1 ||
          page.items.length !== 20 || (url.searchParams.get('leaf_id') || config.leaf) !== config.leaf) throw Error();
      const last = /^历史答复 (\d{4})$/.exec(page.items.at(-1).content);
      const index = last ? (config.turns - Number(last[1])) / 10 : -1;
      if (!Number.isInteger(index) || index < 0 || index >= config.digests.length) throw Error();
      const before = url.searchParams.get('before') || '';
      if (Boolean(before) !== (index > 0) || (state.hits[index] && state.cursors[index] !== before) ||
          page.has_more !== (index < config.digests.length - 1) || Boolean(page.next_cursor) !== page.has_more) throw Error();
      let text = '';
      for (const m of page.items) {
        if (m.conversation_id !== config.conversation || m.status !== 'COMPLETED') throw Error();
        text += m.id + '\t' + (m.parent_id || '') + '\t' + m.role + '\t' + m.content + '\n';
      }
      const hash = new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(text)));
      const hex = Array.from(hash, b => b.toString(16).padStart(2, '0')).join('');
      if (hex !== config.digests[index]) throw Error();
      state.hits[index]++;
      state.cursors[index] = before;
    } catch {
      // Cancelled stale-window reads can abort body consumption. They do not
      // count as a successful fetch and the winning window still has to pass.
      if (!(init?.signal || (path instanceof Request ? path.signal : null))?.aborted) state.invalid = true;
    }
    return response;
  };
}

function sample() {
  const list = document.querySelector('.chat-window-list');
  if (!list) return null;
  return {
    indexed: Number(list.dataset.indexedCount),
    resident: Number(list.dataset.residentCount),
    bytes: Number(list.dataset.residentBytes),
    mounted: list.querySelectorAll('.chat-window-row[data-index]').length,
    invalid: window.deepHistoryProbe.invalid,
  };
}

// Position the real scrolling element. The layout updates measured heights
// as rows mount, so use the observed indices to correct the initial estimate.
function approach(index, total) {
  const el = document.querySelector('.chat-timeline');
  const row = el.querySelector('.chat-window-row[data-index="' + index + '"]');
  const frame = el.getBoundingClientRect();
  if (row) {
    const box = row.getBoundingClientRect();
    if (box.top >= frame.top && box.bottom <= frame.bottom) {
      return row.hasAttribute('data-body-pending') ? null : row.querySelector('.chat-message-content')?.textContent;
    }
    el.scrollTop += (box.top + box.bottom - frame.top - frame.bottom) / 2;
    return null;
  }
  const visible = [...el.querySelectorAll('.chat-window-row[data-index]')].filter(item => {
    const box = item.getBoundingClientRect();
    return box.bottom > frame.top && box.top < frame.bottom;
  });
  if (!visible.length) {
    el.scrollTop = (el.scrollHeight - el.clientHeight) * index / Math.max(1, total - 1);
  } else {
    const middle = Number(visible[Math.floor(visible.length / 2)].dataset.index);
    el.scrollTop += (index - middle) * Math.max(1, el.scrollHeight / total);
  }
  return null;
}

async function run(config) {
  const started = performance.now();
  assert.ok([150, 5000].includes(config.turns) && config.digests.length === config.turns / 10);
  assert.ok(config.digests.every(digest => /^[a-f0-9]{64}$/.test(digest)));
  const total = config.turns * 2;
  const report = {
    unique_authenticated_pages: 0,
    indexed_nodes: 0,
    max_sampled_mounted_nodes: 0,
    max_sampled_resident_bodies: 0,
    max_sampled_resident_bytes: 0,
    evicted_page_revisits: 0,
    generation_posts: 0,
    elapsed_ms: 0,
  };
  const record = value => {
    assert.ok(value && !value.invalid && value.mounted > 0 && value.mounted <= 24);
    assert.ok(value.resident > 0 && value.resident <= 120 && value.bytes > 0 && value.bytes <= (8 << 20));
    report.max_sampled_mounted_nodes = Math.max(report.max_sampled_mounted_nodes, value.mounted);
    report.max_sampled_resident_bodies = Math.max(report.max_sampled_resident_bodies, value.resident);
    report.max_sampled_resident_bytes = Math.max(report.max_sampled_resident_bytes, value.bytes);
  };
  await withLocalBrowser({
    url: config.url,
    chromePath: process.env.WORKFLOW_CHROME,
    onlyOrigin: true,
    signal: AbortSignal.timeout(14 * 60_000),
  }, async open => {
    const page = await open({ width: 1280, height: 900, deferNavigation: true });
    const observerInput = { conversation: config.conversation, leaf: config.leaf, turns: config.turns, digests: config.digests };
    await page.command('Page.addScriptToEvaluateOnNewDocument', {
      source: '(' + observe.toString() + ')(' + JSON.stringify(observerInput) + ')',
    });
    phase = 'login';
    await page.command('Page.navigate', { url: config.url });
    await page.fill('#auth-email', config.email);
    await page.fill('#auth-password', config.password);
    await page.button('进入创作空间', true);
    await until(() => page.evaluate(project => document.querySelector('#project-select')?.value === project, config.project), 'authenticated fixture');
    await page.button('灵感对话');
    await page.button(config.title);
    phase = 'pages';
    for (let loaded = 20; loaded <= total; loaded += 20) {
      step = loaded / 20;
      if (loaded > 20) {
        await until(() => page.evaluate(() => {
          const more = document.querySelector('.timeline-more');
          if (!more || more.disabled) return false;
          more.click();
          return true;
        }), 'earlier action');
      }
      const state = await until(async () => {
        const value = await page.evaluate(sample);
        if (value?.invalid) throw Error('page contents differ');
        return value?.indexed === loaded ? value : null;
      }, 'fixed history page');
      record(state);
    }
    report.indexed_nodes = total;
    const verified = await page.evaluate(() => window.deepHistoryProbe.hits.filter(Boolean).length);
    assert.equal(verified, config.digests.length);
    report.unique_authenticated_pages = verified;
    assert.equal(await page.evaluate(() => Boolean(document.querySelector('.timeline-more'))), false);

    phase = 'revisit';
    const show = async index => {
      const expected = (index % 2 ? '历史答复 ' : '历史问题 ') + String(Math.floor(index / 2) + 1).padStart(4, '0');
      await until(async () => (await page.evaluate(approach, index, total)) === expected, 'visible original message');
      record(await page.evaluate(sample));
    };
    // Visit nine separate pages away from each target to evict that body's
    // 20-row page under the 120-body policy. The actual GET for its original
    // cursor, followed by its original visible text, is required evidence.
    // The final assistant is pinned for replies. Choose another row on its page.
    for (const [n, index] of [1, Math.floor(total / 2) + 3, total - 14].entries()) {
      step = n + 1;
      const targetPage = Math.floor(index / 20);
      const otherPages = Array.from({ length: total / 20 }, (_, i) => i).filter(i => Math.abs(i - targetPage) >= 2);
      assert.ok(otherPages.length >= 9);
      for (let j = 0; j < 9; j++) {
        const at = otherPages[Math.floor(j * (otherPages.length - 1) / 8)];
        await show(at * 20 + 10);
      }
      const wireIndex = total / 20 - 1 - targetPage;
      const before = await page.evaluate(i => window.deepHistoryProbe.hits[i], wireIndex);
      await show(index);
      const after = await page.evaluate(i => window.deepHistoryProbe.hits[i], wireIndex);
      assert.ok(before >= 1 && after > before, 'evicted page was read again');
      report.evicted_page_revisits++;
    }
    const observations = await page.evaluate(() => ({ invalid: window.deepHistoryProbe.invalid, posts: window.deepHistoryProbe.posts }));
    assert.equal(observations.invalid, false);
    assert.equal(observations.posts, 0);
    report.generation_posts = observations.posts;
    await page.close();
  });
  phase = 'report';
  report.elapsed_ms = Math.round(performance.now() - started);
  return report;
}

try {
  const chunks = [];
  let size = 0;
  for await (const chunk of process.stdin) {
    size += chunk.length;
    if (size > (64 << 10)) throw Error('fixture input too large');
    chunks.push(chunk);
  }
  const config = JSON.parse(Buffer.concat(chunks).toString('utf8'));
  process.stdout.write(JSON.stringify(await run(config)) + '\n');
} catch {
  process.stderr.write(JSON.stringify({ phase, step }) + '\n');
  process.exitCode = 1;
}
