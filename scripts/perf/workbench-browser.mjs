import { withLocalBrowser, until } from '../../apps/workbench/scripts/local-browser.mjs';

// Install before the document starts. Renderer timestamps include app work and
// layout; the Node/CDP polling interval never enters a measured duration.
function observe(title) {
  const metrics = {};
  window.__workbenchBenchmark = metrics;
  const visible = (el) => {
    if (!el || el.disabled || !el.getClientRects().length) {
      return false;
    }
    const rect = el.getBoundingClientRect();
    const x = (Math.max(0, rect.left) + Math.min(innerWidth, rect.right)) / 2;
    const y = (Math.max(0, rect.top) + Math.min(innerHeight, rect.bottom)) / 2;
    if (x < 0 || y < 0 || x >= innerWidth || y >= innerHeight) {
      return false;
    }
    const hit = document.elementFromPoint(x, y);
    return hit === el || el.contains(hit);
  };
  document.addEventListener(
    'click',
    (ev) => {
      if (!ev.isTrusted || !(ev.target instanceof Element)) {
        return;
      }
      if (ev.target.closest('.new-project')) {
        metrics.moduleClick = performance.now();
      }
      if (
        ev.target.closest(
          '.conversation-create button[type=submit],.conversation-create button:not([type])'
        )
      ) {
        metrics.createClick = performance.now();
      }
    },
    true
  );
  function frame() {
    const now = performance.now();
    if (!metrics.ready && visible(document.querySelector('.refresh-button'))) {
      metrics.ready = now;
    }
    if (
      metrics.moduleClick &&
      !metrics.module &&
      visible(document.querySelector('.chat-heading h1'))
    ) {
      metrics.module = now - metrics.moduleClick;
    }
    if (metrics.createClick && !metrics.created) {
      const targets = document.querySelectorAll(
        '.conversation-item.active strong,.conversation-topbar h2'
      );
      if ([...targets].some((el) => el.textContent === title && visible(el))) {
        metrics.created = now - metrics.createClick;
      }
    }
    if (!metrics.created) {
      requestAnimationFrame(frame);
    }
  }
  requestAnimationFrame(frame);
}

// Use Chromium's physical input dispatch. Scrolling and target discovery end
// before the trusted click event; the DOM observer timestamps that event.
async function click(page, selector) {
  const pos = await until(
    () =>
      page.evaluate((selector) => {
        const el = document.querySelector(selector);
        if (!el || el.disabled || !el.getClientRects().length) {
          return false;
        }
        el.scrollIntoView({ block: 'center', inline: 'nearest', behavior: 'instant' });
        const r = el.getBoundingClientRect();
        const x = r.left + r.width / 2;
        const y = r.top + r.height / 2;
        return el.contains(document.elementFromPoint(x, y)) ? { x, y } : false;
      }, selector),
    'reachable benchmark control',
    8000
  );
  await page.command('Input.dispatchMouseEvent', {
    type: 'mousePressed',
    button: 'left',
    clickCount: 1,
    ...pos,
  });
  await page.command('Input.dispatchMouseEvent', {
    type: 'mouseReleased',
    button: 'left',
    clickCount: 1,
    ...pos,
  });
}

async function waitFor(page, field) {
  return until(
    () => page.evaluate((field) => window.__workbenchBenchmark?.[field] || false, field),
    field + ' milestone',
    8000
  );
}

export async function measureBrowser({ origin, width, height, warm, chromePath, signal }) {
  let sample;
  let browser;
  let creates = 0;
  let foreign = false;
  const title = '本机浏览器基准会话';
  const onResponse = ({ url, status }) => {
    if (!/^https?:/.test(url)) {
      return;
    }
    const addr = new URL(url);
    if (addr.origin !== origin) {
      foreign = true;
    }
    if (addr.origin === origin && addr.pathname === '/api/v1/conversations' && status === 201) {
      creates++;
    }
  };
  await withLocalBrowser(
    { url: origin, chromePath, onResponse, onlyOrigin: true, signal },
    async (open) => {
      const page = await open({ width, height, deferNavigation: true });
      const version = await page.command('Browser.getVersion');
      browser = {
        product: version.product,
        protocol: version.protocolVersion,
        js_engine: version.jsVersion,
      };
      await page.command('Page.addScriptToEvaluateOnNewDocument', {
        source: '(' + observe.toString() + ')(' + JSON.stringify(title) + ')',
      });
      await page.command('Page.bringToFront');
      const navigate = async () => {
        const result = await page.command('Page.navigate', { url: origin });
        if (result.errorText) {
          const code = /^net::ERR_[A-Z_]+$/.test(result.errorText)
            ? result.errorText
            : 'load error';
          throw Error('Owned workbench navigation failed: ' + code);
        }
        await waitFor(page, 'ready');
      };
      if (warm) {
        await navigate();
        await page.command('Page.navigate', { url: 'about:blank' });
        await until(() => page.evaluate("location.href==='about:blank'"), 'blank document', 5000);
      }
      await navigate();
      if (
        (await page.evaluate(() => innerWidth)) !== width ||
        (await page.evaluate(() => innerHeight)) !== height
      ) {
        throw Error('Viewport mismatch.');
      }
      if (width < 600) {
        await click(page, 'button[aria-label="打开创作导航"]');
      }
      await click(page, '.new-project');
      await waitFor(page, 'module');
      if (
        !(await page.evaluate(
          () => document.querySelector('#conversation-title')?.getClientRects().length > 0
        ))
      ) {
        await click(page, '.conversation-toggle');
      }
      await page.fill('#conversation-title', title);
      await click(page, '.conversation-create button');
      await waitFor(page, 'created');
      sample = await page.evaluate(() => {
        const nav = performance.getEntriesByType('navigation')[0];
        const fcp = performance.getEntriesByName('first-contentful-paint')[0];
        const assets = performance
          .getEntriesByType('resource')
          .filter((e) => new URL(e.name).pathname.startsWith('/assets/'));
        const m = window.__workbenchBenchmark;
        return {
          dom_content_loaded_ms: nav.domContentLoadedEventEnd,
          first_contentful_paint_ms: fcp?.startTime,
          ready_control_ms: m.ready,
          open_chat_ms: m.module,
          create_to_visible_ms: m.created,
          asset_requests: assets.length,
          cached_asset_requests: assets.filter(
            (e) => e.transferSize === 0 && e.decodedBodySize > 0
          ).length,
        };
      });
      if (
        creates !== 1 ||
        foreign ||
        sample.asset_requests === 0 ||
        !sample.first_contentful_paint_ms
      ) {
        throw Error('Browser result did not match the isolated workbench scenario.');
      }
      if (
        (warm && sample.cached_asset_requests < 1) ||
        (!warm && sample.cached_asset_requests !== 0)
      ) {
        throw Error('Observed browser cache did not match the selected condition.');
      }
      await page.close();
    }
  );
  return { browser, sample };
}
