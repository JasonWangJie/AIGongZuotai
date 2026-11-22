import assert from 'node:assert/strict';
import { resolve } from 'node:path';
import { withLocalBrowser, until } from './local-browser.mjs';

function required(name) {
  if (!process.env[name]) throw Error('Missing ' + name);
  return process.env[name];
}
const url = required('ORDER_INTENT_BROWSER_URL'),
  out = resolve(required('ORDER_INTENT_BROWSER_OUTPUT'));
async function login(page, actor) {
  await page.fill('#auth-email', required('ORDER_INTENT_' + actor + '_EMAIL'));
  await page.fill('#auth-password', required('ORDER_INTENT_' + actor + '_PASSWORD'));
  await page.button('进入创作空间', true);
  await page.button('本人订单', true);
  await until(
    () => page.evaluate(() => !!document.querySelector('[data-intent-product]')),
    'actual catalog entry',
  );
}
async function loseAck(page, path) {
  await page.evaluate((path) => {
    const original = window.fetch;
    window.fetch = async (...args) => {
      const response = await original(...args);
      if (
        response.ok &&
        args[1]?.method === 'POST' &&
        new URL(String(args[0]), location.href).pathname === path
      ) {
        window.fetch = original;
        throw TypeError('Local acknowledgement lost');
      }
      return response;
    };
  }, path);
}
function amount(value, scale) {
  if (!scale) return value;
  const raw = value.padStart(scale + 1, '0');
  return raw.slice(0, -scale) + '.' + raw.slice(-scale);
}
async function recovered(page, op, state) {
  await page.button('核对原操作收据', true);
  return await until(
    () =>
      page.evaluate(
        (op, state) => {
          const receipt = document.querySelector('[data-receipt-operation]');
          const current = document.querySelector('[data-selected-order]');
          return receipt?.dataset.receiptOperation === op &&
            receipt.dataset.receiptState === state &&
            current?.dataset.state === state
            ? current.dataset.selectedOrder
            : null;
        },
        op,
        state,
      ),
    'original ' + op + ' receipt and current order',
  );
}
await withLocalBrowser({ url, out, chromePath: process.env.WORKFLOW_CHROME }, async (open) => {
  const buyer = await open(),
    other = await open();
  try {
    await login(buyer, 'BUYER');
    await login(other, 'OTHER');
    const product = await buyer.evaluate(async (id) => {
      const get = async (path) => {
        const r = await fetch('/api/v1/' + path);
        if (!r.ok) throw Error('Fixture read failed');
        return r.json();
      };
      const boot = await get('bootstrap');
      if (boot.storage !== 'postgres' || !boot.auth_enabled)
        throw Error('Authenticated PostgreSQL fixture required');
      const c = await get('order-intents/catalog');
      if (!c.enabled || !c.can_create || !c.can_manage) return null;
      return c.items.find((p) => p.id === id) || (!id && c.items[0]);
    }, process.env.ORDER_INTENT_PRODUCT_ID || '');
    assert.ok(product, 'configured product');
    // Count account-scoped HTTP writes without capturing request keys, credentials or request bodies.
    await buyer.evaluate(() => {
      window.intentWrites = [];
      const original = window.fetch;
      window.fetch = (...args) => {
        const path = new URL(String(args[0]), location.href).pathname;
        if (path.startsWith('/api/v1/order-intents') && args[1]?.method === 'POST')
          window.intentWrites.push(path.endsWith('/cancel') ? 'cancel' : 'create');
        return original(...args);
      };
    });
    await buyer.evaluate(
      (id) => document.querySelector('[data-intent-product="' + id + '"] button').click(),
      product.id,
    );
    const spec = await buyer.evaluate(
      () => document.querySelector('[aria-label="确认意向"]')?.textContent || '',
    );
    assert.ok(
      spec.includes(product.currency + ' ' + amount(product.amount_minor, product.currency_exponent)) &&
        spec.includes(amount(product.credit_micros, 6) + ' CREDIT'),
      'distinct amount and intended credits',
    );
    await buyer.screenshot('orders-desktop');
    await loseAck(buyer, '/api/v1/order-intents');
    await buyer.button('确认创建意向', true);
    const order = await recovered(buyer, 'create', 'PENDING');
    await other.button('刷新订单', true);
    const privacy = await other.evaluate(async (id) => {
      const raw = await fetch('/api/v1/order-intents/' + encodeURIComponent(id));
      const page = await fetch('/api/v1/order-intents').then((r) => r.json());
      return {
        status: raw.status,
        absent: !page.items.some((o) => o.id === id) && !document.body.textContent.includes(id),
      };
    }, order);
    assert.deepEqual(privacy, { status: 404, absent: true }, 'second account privacy');
    await buyer.button('取消这笔意向', true);
    await loseAck(buyer, '/api/v1/order-intents/' + order + '/cancel');
    await buyer.button('确认取消意向', true);
    assert.equal(await recovered(buyer, 'cancel', 'CANCELLED'), order);
    await buyer.button('查看上次创建收据', true);
    await until(
      () =>
        buyer.evaluate(
          () =>
            document.querySelector('[data-receipt-state="PENDING"]') &&
            document.querySelector('[data-selected-order]')?.dataset.state === 'CANCELLED',
        ),
      'immutable creation receipt and current cancellation',
    );
    assert.deepEqual(await buyer.evaluate(() => window.intentWrites), ['create', 'cancel']);
    assert.equal(
      await buyer.evaluate(
        () =>
          !![...document.querySelectorAll('.intent-work button')].find((b) =>
            /支付|付款|退款|充值/.test(b.textContent),
          ),
      ),
      false,
    );
    await buyer.size(390, 844);
    await buyer.evaluate(() =>
      document.querySelector('[data-selected-order]').scrollIntoView({ block: 'center' }),
    );
    assert.ok(
      await buyer.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
      '390px layout',
    );
    await buyer.screenshot('orders-mobile');
    console.log(
      'PASS order intent catalog, separate monetary units, original create/cancel receipts, two-account isolation, immutable snapshot, desktop and 390px layout.',
    );
  } catch (e) {
    await buyer.screenshot('orders-failure');
    throw e;
  } finally {
    await buyer.close();
    await other.close();
  }
});
