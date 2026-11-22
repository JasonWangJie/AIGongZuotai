import assert from 'node:assert/strict';
import { until, withLocalBrowser } from './local-browser.mjs';

const env = process.env,
  required = (k) => {
    if (!env[k]) throw Error('Missing local fixture setting ' + k);
    return env[k];
  };
const url = required('ACCOUNT_GOVERNANCE_BROWSER_URL'),
  target = required('ACCOUNT_GOVERNANCE_TARGET_ID');
const actor = (role) => ({
  email: required('ACCOUNT_GOVERNANCE_' + role + '_EMAIL'),
  password: required('ACCOUNT_GOVERNANCE_' + role + '_PASSWORD'),
});
const user = actor('USER'),
  operator = actor('OPERATOR'),
  statement = '请复核本人本轮账户限制。',
  answer = '已核对本轮申请，维持原限制。',
  draft = '本页尚未提交的本地草稿';
const counts = new Map();
async function login(p, who) {
  await p.fill('#auth-email', who.email);
  await p.fill('#auth-password', who.password);
  await p.button('进入创作空间', true);
}
async function resume(page, restricted) {
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));
  const state = await until(
    () =>
      page.evaluate((flag) => {
        if (document.querySelector('#auth-email')) return 'signin';
        return document.querySelector(flag ? '.ag-restricted' : '.studio-shell') ? 'ready' : '';
      }, restricted),
    'revoked session or refreshed account scope',
  );
  if (state === 'signin') await login(page, user);
  await until(
    () =>
      page.evaluate(
        (flag) => !!document.querySelector(flag ? '.ag-restricted' : '.studio-shell'),
        restricted,
      ),
    'reauthenticated account scope',
  );
}
async function checkbox(p, selector) {
  await until(
    () =>
      p.evaluate((sel) => {
        const e = document.querySelector(sel);
        if (!e || e.disabled) return false;
        if (!e.checked) e.click();
        return e.checked;
      }, selector),
    'bound confirmation',
  );
}
async function loseResponse(p, suffix) {
  await p.evaluate((suffix) => {
    const send = window.fetch;
    window.fetch = async (...args) => {
      const r = await send(...args);
      const path = new URL(args[0] instanceof Request ? args[0].url : String(args[0]), location.href)
        .pathname;
      if (r.ok && args[1]?.method === 'POST' && path.endsWith(suffix)) {
        window.fetch = send;
        throw new TypeError('fixture acknowledgement lost');
      }
      return r;
    };
  }, suffix);
}
async function plan(p, action, reply, lose = false) {
  await p.fill('.ag-page .appeal-compose select', action);
  await p.fill('input[aria-label="账户处置证据编号"]', 'ticket:account-fixture');
  await p.fill('textarea[aria-label="账户处置公开说明"]', reply);
  await p.button('预演账户处置', true);
  await checkbox(p, '.appeal-preview input[type=checkbox]');
  await p.fill('.appeal-preview input[type=password]', operator.password);
  if (lose) await loseResponse(p, '/execute');
  await p.button('确认执行账户处置', true);
  if (lose) await p.button('只读核对原回执', true);
  await until(
    () =>
      p.evaluate(
        () =>
          !document.querySelector('.appeal-preview') &&
          document.body.textContent.includes('已核对账户处置收据'),
      ),
    'confirmed account operation',
  );
}
await withLocalBrowser(
  {
    url,
    out: env.ACCOUNT_GOVERNANCE_BROWSER_SCREENSHOTS,
    chromePath: env.WORKFLOW_CHROME,
    onResponse: (r) => {
      const p = new URL(r.url).pathname;
      if (p.includes('account-targets') || p.includes('/account/')) {
        const key =
          (p.endsWith('execute')
            ? 'execute'
            : p.endsWith('receipt')
              ? 'receipt'
              : p.endsWith('plans')
                ? 'preview'
                : 'read') +
          ':' +
          r.status;
        counts.set(key, (counts.get(key) || 0) + 1);
      }
    },
  },
  async (open) => {
    let stage = 'initial identity',
      active;
    try {
      const creator = (active = await open());
      await login(creator, user);
      await creator.button('图像工作室');
      await creator.fill('#creation-prompt', draft);
      assert.ok(
        await creator.evaluate(async () => {
          const r = await fetch('/api/v1/bootstrap');
          if (!r.ok) return false;
          const b = await r.json();
          return b.mode === 'demo' && b.storage === 'postgres' && b.auth_enabled;
        }),
        'isolated fixture',
      );
      await until(
        () => creator.evaluate((text) => {
          const bytes = new TextEncoder().encode(text).length;
          return document.querySelector('#creation-prompt')?.value === text &&
            document.querySelector('.composer-label')?.textContent.includes(bytes + ' / ');
        }, draft),
        'original project and React composer state are ready before suspension',
      );
      const op = (active = await open());
      await login(op, operator);
      await op.button('账户治理', true);
      await until(
        () => op.evaluate((t) => document.querySelector('.ag-target select')?.value === t, target),
        'explicit target grant',
      );
      stage = 'suspend';
      await plan(op, 'suspend', '临时限制账户工作台访问，请按本轮申请复核。', true);
      active = creator;
      await resume(creator, true);
      assert.ok(
        await creator.evaluate(() => !document.querySelector('.studio-shell,.sidebar,.composer,.inspector')),
        'ordinary and ops shell absent',
      );
      const claimant = (active = await open());
      await login(claimant, user);
      await until(() => claimant.evaluate(() => !!document.querySelector('.ag-held')), 'restricted login');
      await claimant.button('账户安全', true);
      await until(
        () => claimant.evaluate(() => !!document.querySelector('.security-page')),
        'account security',
      );
      await claimant.button('我的账户状态', true);
      stage = 'original appeal receipt';
      await claimant.fill('.ag-restricted .appeal-compose textarea', statement);
      await loseResponse(claimant, '/account/appeals');
      await claimant.button('提交账户申诉', true);
      await claimant.button('只读核对原回执', true);
      await until(
        () =>
          claimant.evaluate(
            (s) => document.querySelector('.ag-history .appeal-detail')?.textContent.includes(s),
            statement,
          ),
        'owner receipt and statement',
      );
      await claimant.size(390, 844);
      await claimant.evaluate(() => document.querySelector('.ag-status').scrollIntoView({ block: 'center' }));
      assert.ok(
        await claimant.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
        'restricted layout fits phone',
      );
      await claimant.screenshot('account-restricted-mobile');
      stage = 'uphold';
      active = op;
      await op.button('刷新目标与申请', true);
      await op.button('待审议');
      await until(
        () =>
          op.evaluate(
            (s) => document.querySelector('.ag-history .appeal-detail')?.textContent.includes(s),
            statement,
          ),
        'target appeal',
      );
      await plan(op, 'uphold', answer);
      await until(
        () => op.evaluate(() => document.querySelector('.ag-history')?.textContent.includes('维持本轮限制')),
        'upheld',
      );
      stage = 'lift';
      await plan(op, 'lift', '已完成独立核对，解除当前轮次限制。');
      await op.evaluate(() => document.querySelector('.ag-status').scrollIntoView({ block: 'center' }));
      await op.screenshot('account-governance-desktop');
      stage = 'return to workbench';
      active = creator;
      await resume(creator, false);
      await until(
        () => creator.evaluate(() => !!document.querySelector('#creation-prompt')),
        'restored normal workspace',
      );
      await until(
        () => creator.evaluate((text) => document.querySelector('#creation-prompt')?.value === text, draft),
        'same owner and project restore their original unsent draft',
      );
      await creator.button('账户状态与申诉', true);
      await creator.button('维持本轮限制');
      await until(
        () =>
          creator.evaluate(
            (s) => document.querySelector('.ag-history .appeal-detail')?.textContent.includes(s),
            answer,
          ),
        'original appeal history on normal account',
      );
      await claimant.close();
      await creator.close();
      await op.close();
      console.log(
        'PASS: target-scoped suspension, restricted login and appeal, original receipts, upheld reply, lift and preserved unsent draft.',
      );
    } catch (e) {
      if (active) {
        const snapshot = await active.evaluate((original) => {
          const field = document.querySelector('#creation-prompt');
          const counter = document.querySelector('.composer-label span:last-child');
          return {
            creatorVisible: !!field?.getClientRects().length,
            creatorDisabled: field?.disabled,
            expectedBytes: new TextEncoder().encode(original).length,
            actualBytes: field ? new TextEncoder().encode(field.value).length : null,
            counter: counter ? Number((counter.textContent.match(/[0-9,]+/)?.[0] || '').replaceAll(',', '')) : null,
            originalMatches: field?.value === original,
            imageView: !!document.querySelector('.image-operation-tabs'),
            authenticated: !!document.querySelector('.studio-shell'),
            restricted: !!document.querySelector('.ag-restricted'),
            duplicateComposer: document.querySelectorAll('#creation-prompt').length > 1,
          };
        }, draft).catch(() => ({}));
        console.error('Browser form diagnostics', JSON.stringify(snapshot));
        const form = await active.evaluate(() => !!document.querySelector('.ag-page')).catch(() => false);
        if (form) {
          await active
            .evaluate(() =>
              document
                .querySelectorAll('input[type=password],input[type=email]')
                .forEach((el) => (el.style.visibility = 'hidden')),
            )
            .catch(() => {});
          await active.screenshot('account-governance-failure').catch(() => {});
        }
      }
      console.error(
        'Account governance browser stage',
        stage,
        'HTTP',
        JSON.stringify(Object.fromEntries(counts)),
      );
      throw e;
    }
  },
);
