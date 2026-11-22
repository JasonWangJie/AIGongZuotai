import assert from 'node:assert/strict';
import { until, withLocalBrowser } from './local-browser.mjs';
const env = process.env,
  required = (k) => {
    if (!env[k]) throw Error('Missing local fixture setting ' + k);
    return env[k];
  };
const actor = (role) => ({
  email: required('RISK_' + role + '_EMAIL'),
  password: required('RISK_' + role + '_PASSWORD'),
});
const owner = actor('USER'),
  reviewer = actor('REVIEWER'),
  ws = required('RISK_WORKSPACE_ID'),
  url = required('RISK_BROWSER_URL');
const counts = new Map();
async function login(p, a) {
  await p.fill('#auth-email', a.email);
  await p.fill('#auth-password', a.password);
  await p.button('进入创作空间', true);
}
async function lost(p, suffix) {
  await p.evaluate((suffix) => {
    const old = window.fetch;
    window.fetch = async (...args) => {
      const response = await old(...args);
      if (
        response.ok &&
        args[1]?.method === 'POST' &&
        new URL(String(args[0]), location.href).pathname.endsWith(suffix)
      ) {
        window.fetch = old;
        throw TypeError('local fixture lost acknowledgement');
      }
      return response;
    };
  }, suffix);
}
await withLocalBrowser(
  {
    url,
    out: env.RISK_BROWSER_SCREENSHOTS,
    chromePath: env.WORKFLOW_CHROME,
    onResponse: (r) => {
      const p = new URL(r.url).pathname;
      if (p.includes('/risk') || p.endsWith('/tasks') || p.endsWith('/billing/quotes')) {
        const label =
          (p.endsWith('/billing/quotes')
            ? 'quote'
            : p.endsWith('/tasks')
            ? 'task'
            : p.endsWith('/receipt')
              ? 'receipt'
              : p.endsWith('/execute')
                ? 'execute'
                : 'risk') +
          ':' +
          r.status;
        counts.set(label, (counts.get(label) || 0) + 1);
      }
    },
  },
  async (open) => {
    let stage = 'owner login',
      active;
    try {
      const u = (active = await open());
      await login(u, owner);
      await u.button('图像工作室');
      await u.button('文字生成', true);
      await until(() => u.evaluate(() =>
        document.querySelector('#model-select')?.value === 'studio-image' &&
        document.querySelector('#parameter-preset')?.value === 'preset-1' &&
        document.querySelector('#result-count')?.textContent === '1'
      ), 'fixture model, first preset and one requested image');
      assert.ok(
        await u.evaluate(async (target) => {
          const r = await fetch('/api/v1/bootstrap');
          if (!r.ok) return false;
          const b = await r.json();
          return (
            b.mode === 'demo' &&
            b.storage === 'postgres' &&
            b.auth_enabled &&
            b.billing?.enabled &&
            b.workspace.id === target
          );
        }, ws),
        'isolated demo identity and funded fixture',
      );
      await u.evaluate((expectedWorkspace) => {
        const fetchBefore = window.fetch;
        window.riskFixtureProof = { attempts: 0, identical: true, firstKey: '', firstBody: '' };
        window.riskQuoteProof = { sent: 0, returned: false, status: 0, pricedInput: false, exactAmount: false, valid: false, issue: '' };
        window.fetch = async (...args) => {
          const init = args[1],
            p = new URL(String(args[0]), location.href).pathname;
          if (p === '/api/v1/billing/quotes' && init?.method === 'POST') {
            const q = window.riskQuoteProof;
            const input = JSON.parse(init.body)?.task_input;
            q.sent++;
            q.pricedInput = input?.kind === 'image' && input?.model_id === 'studio-image' &&
              input?.operation === 'generate' && input?.parameters?.aspect_ratio === '16:9' &&
              input?.parameters?.count === 1 && !!input?.model_version;
            try {
              const response = await fetchBefore(...args);
              q.status = response.status;
              const result = await response.clone().json().catch(() => null);
              q.exactAmount = result?.reserve_micros === '200000' && result?.requested_units === 1;
              q.valid = !!result?.id && result.provider_mode === 'demo' && result.resource_type === 'task' &&
                result.workspace_id === expectedWorkspace && result.project_id === input?.project_id &&
                result.model_id === input?.model_id && result.model_version === input?.model_version &&
                result.unit === 'CREDIT' && result.scale === 6 && Date.parse(result.expires_at) > Date.now();
              q.issue = result === null ? 'INVALID_RESPONSE' :
                /^[A-Z][A-Z0-9_]{0,63}$/.test(result.error?.code ?? '') ? result.error.code : '';
              return response;
            } catch (err) {
              q.issue = err?.name === 'AbortError' ? 'ABORTED' : 'NETWORK_OR_INVALID_RESPONSE';
              throw err;
            } finally {
              q.returned = true;
            }
          }
          if (p === '/api/v1/tasks' && init?.method === 'POST') {
            const x = window.riskFixtureProof,
              key = new Headers(init.headers).get('Idempotency-Key');
            if (!x.attempts) {
              x.firstKey = key;
              x.firstBody = init.body;
            } else x.identical = x.identical && key === x.firstKey && init.body === x.firstBody;
            x.attempts++;
          }
          return fetchBefore(...args);
        };
      }, ws);
      stage = 'request the priced original';
      await u.fill('#creation-prompt', '星图本机风控验收图片');
      await u.button('查看本次报价', true);
      const priced = await until(() => u.evaluate(() =>
        window.riskQuoteProof.returned ? window.riskQuoteProof : null
      ), 'one visible quotation request and its HTTP response');
      assert.ok(priced.sent === 1 && priced.status === 201 && priced.pricedInput && priced.exactAmount && priced.valid,
        'the requested preset must have its actual unexpired 200000-micro quote');
      stage = 'confirm the real original quote';
      await u.button('确认额度并提交', true);
      await u.button('查看原报价风控', true);
      await until(
        () =>
          u.evaluate(() =>
            document.querySelector('.appeal-compose')?.textContent.includes('工作区冻结额度上限'),
          ),
        'server rule for original quote',
      );
      assert.ok(
        await u.evaluate(
          () => !document.querySelector('.appeal-page')?.textContent.includes('星图本机风控验收图片'),
        ),
        'no private generation prompt in evidence',
      );
      await u.fill('.appeal-compose textarea', '请核对本次工作区额度限制。');
      await lost(u, '/risk/appeals');
      await u.button('提交额度风控复核', true);
      await u.button('只读核对原回执', true);
      await until(() => u.evaluate(() => !!document.querySelector('.appeal-detail')), 'own appeal receipt');
      await u.screenshot('risk-owner-desktop');
      const op = (active = await open());
      stage = 'independent risk reviewer';
      await login(op, reviewer);
      await op.button('运营核实');
      await until(
        () => op.evaluate(() => document.querySelector('.appeal-page')?.textContent.includes('额度风控审议')),
        'restricted reviewer panel',
      );
      await op.button('待复核 · 0.2 创作点');
      await op.fill('.appeal-compose select', 'approve');
      await op.fill('.appeal-compose input', 'ticket:risk-review');
      await op.fill('.appeal-compose textarea', '批准本次原报价单次许可。');
      await op.button('预演额度风控处置', true);
      await until(
        () =>
          op.evaluate(() => {
            const e = document.querySelector('.appeal-preview input[type=checkbox]');
            if (!e) return false;
            if (!e.checked) e.click();
            return e.checked;
          }),
        'confirm plan',
      );
      await op.evaluate(() => document.querySelector('.appeal-preview').scrollIntoView({ block: 'center' }));
      await op.screenshot('risk-reviewer-plan');
      await op.fill('.appeal-preview input[type=password]', reviewer.password);
      await lost(op, '/execute');
      await op.button('确认执行风控处置', true);
      await op.button('只读核对原回执', true);
      await until(
        () => op.evaluate(() => document.body.textContent.includes('已核对风控处置收据')),
        'official receipt',
      );
      active = u;
      stage = 'original explicit retry';
      await u.button('刷新复核', true);
      await until(
        () =>
          u.evaluate(() =>
            [...document.querySelectorAll('button')].some(
              (b) => !b.disabled && b.textContent === '按原报价确认提交原请求',
            ),
          ),
        'approval and valid original',
      );
      assert.equal(
        await u.evaluate(() => window.riskFixtureProof.attempts),
        1,
        'no automatic submission after approval',
      );
      await u.size(390, 844);
      await u.evaluate(() => document.querySelector('.appeal-preview').scrollIntoView({ block: 'center' }));
      await u.screenshot('risk-original-mobile');
      assert.ok(
        await u.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
        'mobile layout fits',
      );
      await u.button('按原报价确认提交原请求', true);
      await until(
        () =>
          u.evaluate(() => document.querySelector('.appeal-preview')?.textContent.includes('原请求已受理')),
        'single admitted request',
      );
      assert.ok(
        await u.evaluate(() => window.riskFixtureProof.attempts === 2 && window.riskFixtureProof.identical),
        'same key body and quote retained',
      );
      await u.close();
      await op.close();
      console.log(
        'PASS: genuine risk refusal, owner and reviewer receipts, explicit single same-key same-body accepted creation.',
      );
    } catch (e) {
      const state =
        active &&
        (await active
          .evaluate(() => ({
            panel: !!document.querySelector('.appeal-page'),
            form: !!document.querySelector('.appeal-compose'),
            detail: !!document.querySelector('.appeal-detail'),
            unknown: !!document.querySelector('.appeal-notice[role=status]'),
            quote: window.riskQuoteProof,
            quoteSection: !!document.querySelector('.quote-confirmation'),
            confirmationEnabled: [...document.querySelectorAll('.quote-actions button')].some(b =>
              b.textContent.trim() === '确认额度并提交' && !b.disabled),
            creatorReady: !!document.querySelector('.composer button[type=submit]:not(:disabled)'),
          }))
          .catch(() => null));
      console.error(
        'Risk browser stage',
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
