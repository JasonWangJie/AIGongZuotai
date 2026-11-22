import assert from 'node:assert/strict';
import { resolve } from 'node:path';
import { until, withLocalBrowser } from './local-browser.mjs';
const required = (k) => {
  const v = process.env[k];
  if (!v) throw Error('Missing ' + k);
  return v;
};
const url = required('SUPPORT_BROWSER_URL'),
  out = resolve(required('SUPPORT_BROWSER_OUTPUT')),
  workspace = required('SUPPORT_WORKSPACE_ID');
const base = '/api/v1/support/tickets',
  staffBase = '/api/v1/operations/support/tickets';
const subject = '导出资料时的操作问题',
  initial = '已下载账户资料，希望确认文件包含的范围。',
  followup = '我已找到第二页，请说明下一步。',
  reply = '请逐页保存并核对每个文件的摘要。';
async function enter(p, role) {
  await p.fill('#auth-email', required('SUPPORT_' + role + '_EMAIL'));
  await p.fill('#auth-password', required('SUPPORT_' + role + '_PASSWORD'));
  await p.button('进入创作空间', true);
  await p.button(role === 'AGENT' ? '客服收件箱' : '我的客服工单', true);
  await until(() => p.evaluate(() => !!document.querySelector('.support-index')), 'support workspace');
  if (role === 'AGENT') {
    assert.equal(await p.evaluate(() => document.querySelector('.support-scope select')?.value), workspace);
  }
  await p.evaluate(() => {
    window.supportRequests = [];
    const original = window.fetch;
    window.fetch = async (...args) => {
      const path = new URL(String(args[0]), location.href).pathname;
      const r = await original(...args);
      if (path.includes('/support/tickets') && args[1]?.method === 'POST')
        window.supportRequests.push({
          op: path.split('/').at(-1) === 'tickets' ? 'create' : path.split('/').at(-1),
          status: r.status,
        });
      return r;
    };
  });
}
async function lose(p, path) {
  await p.evaluate((path) => {
    const original = window.fetch;
    window.fetch = async (...args) => {
      const r = await original(...args);
      if (r.ok && args[1]?.method === 'POST' && new URL(String(args[0]), location.href).pathname === path) {
        window.fetch = original;
        throw TypeError('Fixture acknowledgement lost');
      }
      return r;
    };
  }, path);
}
async function state(p, version, status = 'OPEN') {
  return await until(
    () =>
      p.evaluate(
        (version, status) => {
          const t = document.querySelector('[data-support-ticket]');
          return t?.dataset.supportVersion === String(version) && t.dataset.supportState === status
            ? t.dataset.supportTicket
            : null;
        },
        version,
        status,
      ),
    'current ticket version ' + version,
  );
}
async function field(p, label, v) {
  await p.fill('[aria-label="' + label + '"]', v);
}
async function clickTicket(p, id) {
  await until(
    () =>
      p.evaluate((id) => {
        const b = [...document.querySelectorAll('[data-support-item]')].find(
          (x) => x.dataset.supportItem === id,
        );
        if (!b || b.disabled) return false;
        b.click();
        return true;
      }, id),
    'open authorized ticket',
  );
}
async function recover(p) {
  await p.button('只读核对原工单收据', true);
}
await withLocalBrowser({ url, out, chromePath: process.env.WORKFLOW_CHROME }, async (open) => {
  const own = await open(),
    agent = await open(),
    other = await open();
  try {
    await enter(own, 'OWNER');
    await enter(agent, 'AGENT');
    await enter(other, 'OTHER');
    assert.equal(
      await other.evaluate(() =>
        [...document.querySelectorAll('nav.side-nav button')].some((b) =>
          b.textContent.includes('客服收件箱'),
        ),
      ),
      false,
    );
    await own.evaluate(() => (document.querySelector('.support-compose').open = true));
    await field(own, '工单主题', subject);
    await field(own, '问题说明', initial);
    await lose(own, base);
    await own.button('提交工单', true);
    await until(
      () => own.evaluate(() => !!document.querySelector('.support-notice[role=status]')),
      'unknown creation',
    );
    const key = await own.evaluate(() => {
      const slot = Object.keys(sessionStorage).find((k) => k.startsWith('xingtu-support-action:'));
      const data = JSON.parse(sessionStorage.getItem(slot));
      return data.key;
    });
    await recover(own);
    const id = await state(own, 1);
    const isolation = await other.evaluate(
      async (id, workspace, key) => {
        const get = async (path, headers) => fetch(path, { headers });
        const root = '/api/v1/support/tickets',
          ops = '/api/v1/operations/support/tickets';
        const list = await get(root).then((r) => r.json()),
          detail = await get(root + '/' + id),
          receipt = await get(root + '/receipt?operation=create', { 'Idempotency-Key': key }),
          outside = await get(ops + '/' + id + '?workspace_id=' + encodeURIComponent(workspace));
        return {
          absent: !list.items.some((t) => t.id === id),
          statuses: [detail.status, receipt.status, outside.status],
        };
      },
      id,
      workspace,
      key,
    );
    assert.deepEqual(isolation, { absent: true, statuses: [404, 404, 404] });
    await agent.button('刷新工单', true);
    await clickTicket(agent, id);
    await state(agent, 1);
    assert.ok(
      await agent.evaluate(
        (raw) =>
          !document.querySelector('.support-index').textContent.includes(raw) &&
          document.querySelector('.support-thread').textContent.includes(raw),
        initial,
      ),
    );
    await field(agent, '客服答复', reply);
    await field(own, '用户追问', followup);
    await own.button('提交追问', true);
    await state(own, 2);
    await field(agent, '客服当前密码', required('SUPPORT_AGENT_PASSWORD'));
    await agent.button('确认发送客服答复', true);
    await state(agent, 2);
    await until(
      () =>
        agent.evaluate(
          () =>
            !![...document.querySelectorAll('button')].find((b) => b.textContent === '基于当前版本继续编辑'),
        ),
      'visible CAS conflict',
    );
    assert.ok(
      await agent.evaluate(
        (text) =>
          document.querySelector('[aria-label="客服答复"]').value === text &&
          !document.querySelector('[aria-label="客服当前密码"]').value,
        reply,
      ),
    );
    await agent.button('基于当前版本继续编辑', true);
    await field(agent, '客服当前密码', 'invalid-' + crypto.randomUUID());
    await agent.button('确认发送客服答复', true);
    await until(
      () =>
        agent.evaluate(
          () =>
            document.querySelector('[role="alert"]')?.textContent.includes('当前客服账户的密码') &&
            document.querySelector('[aria-label="客服当前密码"]').value === '',
        ),
      'incorrect operator password',
    );
    await field(agent, '客服当前密码', required('SUPPORT_AGENT_PASSWORD'));
    await lose(agent, staffBase + '/' + id + '/reply');
    await agent.button('确认发送客服答复', true);
    await recover(agent);
    await state(agent, 3);
    await agent.evaluate(() =>
      document.querySelector('.support-detail').scrollIntoView({ block: 'center', behavior: 'instant' }),
    );
    await agent.screenshot('support-agent-desktop');
    await own.button('刷新工单', true);
    await state(own, 3);
    assert.ok(
      await own.evaluate(
        (text) => document.querySelector('[data-message-kind="reply"]').textContent.includes(text),
        reply,
      ),
    );
    await own.button('关闭工单', true);
    await lose(own, base + '/' + id + '/close');
    await own.button('确认关闭工单', true);
    await recover(own);
    await state(own, 4, 'CLOSED');
    await own.size(390, 844);
    await own.evaluate(() =>
      document.querySelector('.support-detail').scrollIntoView({ block: 'center', behavior: 'instant' }),
    );
    assert.ok(
      await own.evaluate(
        () =>
          document.documentElement.scrollWidth <= innerWidth &&
          !document.querySelector('[aria-label="用户追问"]'),
      ),
    );
    await own.screenshot('support-owner-mobile');
    assert.deepEqual(await own.evaluate(() => window.supportRequests.map((r) => r.op)), [
      'create',
      'followup',
      'close',
    ]);
    assert.deepEqual(
      await agent.evaluate(() => window.supportRequests.map((r) => r.status)),
      [409, 403, 200],
    );
    console.log(
      'PASS real browser support path: three accounts, metadata-only index, create/followup, conflict, password verification, private reply, original receipts and owner closure.',
    );
  } finally {
    await own.close();
    await agent.close();
    await other.close();
  }
});
