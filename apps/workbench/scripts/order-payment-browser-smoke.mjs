import assert from 'node:assert/strict';
import { resolve } from 'node:path';
import { withLocalBrowser, until } from './local-browser.mjs';

function env(name) {
  const value = process.env['ORDER_PAYMENT_' + name];
  if (!value) throw Error('Missing ORDER_PAYMENT_' + name);
  return value;
}

const paid = env('PAID_ORDER_ID');
const refunded = env('REFUNDED_ORDER_ID');
assert.ok(/^ord_[a-f0-9]{32}$/.test(paid) && /^ord_[a-f0-9]{32}$/.test(refunded) && paid !== refunded);

async function login(page, actor) {
  await page.fill('#auth-email', env(actor + '_EMAIL'));
  await page.fill('#auth-password', env(actor + '_PASSWORD'));
  await page.button('进入创作空间', true);
  await page.button('本人订单', true);
  await until(() => page.evaluate(() => !!document.querySelector('[data-intent-product]')), 'order catalog');
}

async function selectOrder(page, order) {
  await until(
    () =>
      page.evaluate((id) => {
        const button = document.querySelector('[data-intent-row="' + id + '"] button');
        if (!button || button.disabled) return false;
        button.click();
        return true;
      }, order),
    'open own order',
  );
  await until(
    () =>
      page.evaluate(
        (id) => document.querySelector('[data-selected-order]')?.dataset.selectedOrder === id,
        order,
      ),
    'current order',
  );
}

async function records(page, order, expected) {
  await page.button('查看资金记录', true);
  await until(
    () =>
      page.evaluate(
        (id) => document.querySelector('[data-payment-order]')?.dataset.paymentOrder === id,
        order,
      ),
    'scoped payment records',
  );
  const actual = await page.evaluate(() => {
    const panel = document.querySelector('[data-payment-order]');
    return {
      payment: panel.querySelector('[data-payment-state]').dataset.paymentState,
      refund: panel.querySelector('[data-refund-state]').dataset.refundState,
      credit: panel.querySelector('[data-credit-state]').dataset.creditState,
      cashText: panel.querySelector('[data-payment-state]').textContent,
      refundText: panel.querySelector('[data-refund-state]').textContent,
      creditText: panel.querySelector('[data-credit-state]').textContent,
      text: panel.textContent,
    };
  });
  assert.deepEqual([actual.payment, actual.refund, actual.credit], expected.states);
  assert.ok(
    actual.cashText.includes(expected.paid) && actual.refundText.includes(expected.refunded),
    'cash and refunds',
  );
  assert.ok(
    actual.creditText.includes('已入账 ' + expected.granted + ' CREDIT') &&
      actual.creditText.includes('已冲正 0.000000 CREDIT'),
    'independent CREDIT posting',
  );
  assert.ok(actual.text.includes(expected.label), 'payment evidence label');
}

await withLocalBrowser(
  {
    url: env('BROWSER_URL'),
    out: resolve(env('BROWSER_OUTPUT')),
    chromePath: process.env.WORKFLOW_CHROME,
    onlyOrigin: true,
  },
  async (open) => {
    const buyer = await open();
    const other = await open();
    try {
      await login(buyer, 'BUYER');
      await login(other, 'OTHER');
      // Count writes only, without collecting headers, payment IDs or request bodies.
      await buyer.evaluate(() => {
        window.orderPagePosts = 0;
        const original = window.fetch;
        window.fetch = (...args) => {
          if (
            new URL(String(args[0]), location.href).pathname.startsWith('/api/v1/order-intents') &&
            (args[1]?.method || 'GET') !== 'GET'
          )
            window.orderPagePosts++;
          return original(...args);
        };
      });
      await buyer.button('登记购买意向', true);
      await buyer.button('确认创建意向', true);
      const fresh = await until(
        () => buyer.evaluate(() => document.querySelector('[data-selected-order]')?.dataset.selectedOrder),
        'new intent',
      );
      assert.ok(fresh !== paid && fresh !== refunded);
      await records(buyer, fresh, {
        states: ['UNBOUND', 'NONE', 'NOT_ISSUED'],
        paid: 'CNY 0.00',
        refunded: 'CNY 0.00',
        granted: '0.000000',
        label: '无已核实资金记录',
      });
      await selectOrder(buyer, paid);
      await records(buyer, paid, {
        states: ['RECEIVED', 'NONE', 'ISSUED'],
        paid: 'CNY 12.00',
        refunded: 'CNY 0.00',
        granted: '10.000000',
        label: '测试环境记录',
      });
      await buyer.evaluate(() =>
        document.querySelector('[aria-label="资金和权益记录"]').scrollIntoView({ block: 'center' }),
      );
      await buyer.screenshot('order-payment-desktop');
      await selectOrder(buyer, refunded);
      await records(buyer, refunded, {
        states: ['RECEIVED', 'PARTIAL', 'REVIEW'],
        paid: 'CNY 12.00',
        refunded: 'CNY 3.00',
        granted: '10.000000',
        label: '测试环境记录',
      });
      assert.equal(
        await buyer.evaluate(() =>
          [...document.querySelectorAll('[data-selected-order] button')].some(
            (button) => button.textContent === '取消这笔意向',
          ),
        ),
        false,
        'confirmed funds prevent a new cancellation',
      );
      await buyer.button('重新读取记录', true);
      await until(
        () => buyer.evaluate(() => !!document.querySelector('[data-credit-state="REVIEW"]')),
        'refreshed review',
      );
      const privacy = await other.evaluate(
        async (ids) => {
          const list = await fetch('/api/v1/order-intents').then((r) => r.json());
          const codes = [];
          for (const id of ids) {
            const response = await fetch('/api/v1/order-intents/' + id + '/payment-status', {
              cache: 'no-store',
            });
            codes.push(response.status);
          }
          return {
            codes,
            absent: ids.every(
              (id) => !list.items.some((o) => o.id === id) && !document.body.textContent.includes(id),
            ),
          };
        },
        [fresh, paid, refunded],
      );
      assert.deepEqual(privacy, { codes: [404, 404, 404], absent: true });
      assert.equal(
        await buyer.evaluate(() => window.orderPagePosts),
        1,
        'one explicit create and all subsequent reads',
      );
      assert.equal(
        await buyer.evaluate(() =>
          [...document.querySelectorAll('.intent-work button')].some((button) =>
            /立即支付|申请退款|发放权益|确认收款/.test(button.textContent),
          ),
        ),
        false,
      );
      await buyer.size(390, 844);
      await buyer.evaluate(() =>
        document.querySelector('[aria-label="资金和权益记录"]').scrollIntoView({ block: 'center' }),
      );
      assert.ok(
        await buyer.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
        '390px without horizontal overflow',
      );
      await buyer.screenshot('order-payment-mobile');
      console.log(
        'PASS owner payment/refund/CREDIT evidence, original amounts, test environment, second-account isolation, read-only refresh and 390px layout.',
      );
    } finally {
      await buyer.close();
      await other.close();
    }
  },
);
