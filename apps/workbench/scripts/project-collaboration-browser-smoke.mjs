import assert from 'node:assert/strict';
import { mkdir } from 'node:fs/promises';
import { resolve } from 'node:path';
import { until, withLocalBrowser } from './local-browser.mjs';
const required = (n) => {
  if (!process.env[n]) throw Error('Missing ' + n);
  return process.env[n];
};
const url = required('COLLAB_BROWSER_URL'),
  out = resolve(required('COLLAB_BROWSER_OUTPUT')),
  project = required('COLLAB_PROJECT_ID'),
  sibling = required('COLLAB_SIBLING_PROJECT_ID'),
  personal = required('COLLAB_MEMBER_PROJECT_ID');
const fields = (p, l, v) => p.fill('[aria-label="' + l + '"]', v);
async function login(p, identity, scope) {
  await p.fill('#auth-email', required('COLLAB_' + identity + '_EMAIL'));
  await p.fill('#auth-password', required('COLLAB_' + identity + '_PASSWORD'));
  await p.button('进入创作空间', true);
  await p.button('项目协作', true);
  await until(
    () => p.evaluate(() => !!document.querySelector('[data-collab-user]')),
    'authenticated collaboration page',
  );
  if ((await p.evaluate(() => document.querySelector('#project-select')?.value)) !== scope)
    await p.fill('#project-select', scope);
  await until(
    () =>
      p.evaluate(
        (scope) =>
          document.querySelector('#project-select')?.value === scope && !!document.querySelector('.co-owner'),
        scope,
      ),
    'current personal workspace',
  );
}
async function ownerPanel(p) {
  await until(
    () =>
      p.evaluate(() => {
        const d = document.querySelector('.co-owner');
        if (!d) return false;
        d.open = true;
        return true;
      }),
    'owner controls',
  );
}
async function lose(p, path) {
  await p.evaluate((path) => {
    const original = window.fetch;
    window.fetch = async (...args) => {
      const r = await original(...args);
      if (
        r.ok &&
        ['POST', 'PUT'].includes(args[1]?.method) &&
        new URL(String(args[0]), location.href).pathname.endsWith(path)
      ) {
        window.fetch = original;
        throw TypeError('Local acknowledgement lost');
      }
      return r;
    };
  }, path);
}
async function refresh(p) {
  await p.button('刷新协作权限', true);
}
async function invite(owner, id, role, lost = false) {
  await ownerPanel(owner);
  await fields(owner, '对方账户编号', id);
  await fields(owner, '邀请权限', role);
  await owner.button('邀请协作者', true);
  if (lost) await lose(owner, '/collaboration/invitations');
  await owner.button('确认邀请', true);
  if (lost) await owner.button('只读核对原成员收据', true);
  await until(
    () =>
      owner.evaluate(() =>
        document.querySelector('.co-member[data-member-id]')?.textContent.includes('待接受'),
      ),
    'pending invitation',
  );
}
async function accept(member) {
  await refresh(member);
  await member.button('接受邀请', true);
  await member.button('确认接受邀请', true);
  await until(
    () => member.evaluate(() => !!document.querySelector('[data-shared-project]')),
    'confirmed membership',
  );
}
async function change(owner, label, state) {
  await ownerPanel(owner);
  await refresh(owner);
  await owner.button(label, true);
  await owner.button('确认' + label, true);
  await until(
    () =>
      owner.evaluate(
        (state) => document.querySelector('.co-member[data-member-id]')?.textContent.includes(state),
        state,
      ),
    'owner updated access',
  );
}
async function shared(p) {
  await refresh(p);
  await p.button('打开共享草稿', true);
  await until(
    () => p.evaluate(() => !!document.querySelector('.co-editor .co-history')),
    'shared draft loaded',
  );
}
function saved(p, n) {
  return until(
    () => p.evaluate((n) => document.querySelector('.co-editor header')?.textContent.includes('r' + n), n),
    'draft revision ' + n,
  );
}
async function unchanged(p) {
  assert.ok(
    await p.evaluate(
      (personal, project, sibling) => {
        const select = document.querySelector('#project-select');
        return (
          select.value === personal &&
          ![...select.options].some((o) => [project, sibling].includes(o.value)) &&
          !document.querySelector('[data-shared-project="' + sibling + '"]')
        );
      },
      personal,
      project,
      sibling,
    ),
    'personal project and sibling isolation',
  );
}
const counts = new Map();
await mkdir(out, { recursive: true });
await withLocalBrowser(
  {
    url,
    out,
    chromePath: process.env.WORKFLOW_CHROME,
    onResponse: (r) => {
      const pathname = new URL(r.url).pathname;
      if (!pathname.includes('/collaboration')) return;
      const kind = pathname.endsWith('/receipt')
        ? 'receipt'
        : pathname.endsWith('/draft')
          ? 'draft'
          : pathname.endsWith('/accept')
            ? 'accept'
            : pathname.endsWith('/revoke')
              ? 'revoke'
              : pathname.endsWith('/downgrade')
                ? 'downgrade'
                : 'member';
      const k = kind + ':' + r.status;
      counts.set(k, (counts.get(k) || 0) + 1);
    },
  },
  async (open) => {
    let phase = 'identities',
      active;
    try {
      const owner = (active = await open());
      await login(owner, 'OWNER', project);
      const member = (active = await open());
      await login(member, 'MEMBER', personal);
      const other = await open();
      await login(other, 'MEMBER', personal);
      const target = await member.evaluate(
        () => document.querySelector('[data-collab-user]').dataset.collabUser,
      );
      assert.match(target, /^usr_[a-f0-9]{32}$/);
      if (process.env.COLLAB_MEMBER_ID) assert.equal(target, process.env.COLLAB_MEMBER_ID);
      active = owner;
      phase = 'owner draft';
      await ownerPanel(owner);
      await owner.button('打开本项目草稿', true);
      await owner.button('添加共享节点', true);
      await fields(owner, '节点标题 1', '海岸踏查');
      await fields(owner, '节点正文 1', '两位创作者共同梳理的现场节点。');
      await owner.button('保存共享草稿', true);
      await saved(owner, 1);
      phase = 'withdraw then viewer invitation';
      await invite(owner, target, 'viewer', true);
      await refresh(member);
      await until(
        () =>
          member.evaluate(() =>
            [...document.querySelectorAll('.co-panel button')].some((b) => b.textContent === '接受邀请'),
          ),
        'viewer invitation delivered',
      );
      await change(owner, '撤回邀请', '邀请已撤回');
      await refresh(member);
      await until(
        () =>
          member.evaluate(
            () =>
              ![...document.querySelectorAll('.co-panel button')].some((b) => b.textContent === '接受邀请'),
          ),
        'withdrawn invitation hidden',
      );
      await invite(owner, target, 'viewer');
      await accept(member);
      active = member;
      await shared(member);
      await unchanged(member);
      assert.ok(
        await member.evaluate(
          () =>
            document.querySelector('.co-editor').textContent.includes('海岸踏查') &&
            !document.querySelector('.co-editor textarea'),
        ),
        'viewer reads without edit',
      );
      await member.size(390, 844);
      await member.evaluate(() => document.querySelector('.co-editor').scrollIntoView({ block: 'start' }));
      assert.ok(
        await member.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
        'mobile fit',
      );
      await member.screenshot('collaboration-viewer-mobile');
      await member.size(1280, 900);
      active = owner;
      phase = 'revocation and editor invite';
      await change(owner, '收回权限', '权限已收回');
      await refresh(member);
      await until(
        () =>
          member.evaluate(
            () => !document.querySelector('.co-editor') && !document.querySelector('[data-shared-project]'),
          ),
        'old access removed',
      );
      await invite(owner, target, 'editor');
      await accept(member);
      await shared(member);
      await shared(other);
      active = other;
      phase = 'independent editors';
      await fields(other, '节点正文 1', '第二窗口调整镜头顺序。');
      await fields(member, '节点正文 1', '第一窗口标记潮汐时刻。');
      await lose(member, '/draft');
      await member.button('保存共享草稿', true);
      await member.button('只读核对原草稿收据', true);
      await saved(member, 2);
      await other.button('保存共享草稿', true);
      await until(
        () =>
          other.evaluate(() => document.querySelector('.co-editor')?.textContent.includes('本页输入仍保留')),
        'shared CAS',
      );
      assert.equal(
        await other.evaluate(() => document.querySelector('[aria-label="节点正文 1"]').value),
        '第二窗口调整镜头顺序。',
      );
      await other.evaluate(() => document.querySelector('.co-editor').scrollIntoView({ block: 'start' }));
      await other.screenshot('collaboration-conflict-desktop');
      await other.button('保留本页并基于最新修订', true);
      await other.button('保存共享草稿', true);
      await saved(other, 3);
      await other.evaluate(() => (document.querySelector('.co-history').open = true));
      await other.button('读取 r1 · 1 个节点', true);
      await until(
        () =>
          other.evaluate(() =>
            document
              .querySelector('.co-history-view')
              ?.textContent.includes('两位创作者共同梳理的现场节点。'),
          ),
        'original database revision',
      );
      active = owner;
      phase = 'read-only downgrade';
      await change(owner, '降为只读', '只读');
      await refresh(member);
      await refresh(other);
      for (const p of [member, other])
        await until(
          () =>
            p.evaluate(
              () =>
                document.querySelector('.co-editor header')?.textContent.includes('只读') &&
                !document.querySelector('.co-editor textarea'),
            ),
          'all sessions downgraded',
        );
      await owner.evaluate(() => document.querySelector('.co-owner').scrollIntoView({ block: 'start' }));
      await owner.screenshot('collaboration-members-desktop');
      phase = 'revoked project exit';
      await change(owner, '收回权限', '权限已收回');
      await refresh(member);
      await refresh(other);
      for (const p of [member, other]) {
        await until(
          () =>
            p.evaluate(
              () => !document.querySelector('.co-editor') && !document.querySelector('[data-shared-project]'),
            ),
          'both sessions released',
        );
        await unchanged(p);
      }
      const rights = await member.evaluate(
        async (project, sibling) => {
          const urls = [
            '/api/v1/collaboration/projects/' + project + '/draft',
            '/api/v1/collaboration/projects/' + sibling + '/draft',
            '/api/v1/projects/' + project + '/draft',
          ];
          return Promise.all(urls.map(async (u) => (await fetch(u)).status));
        },
        project,
        sibling,
      );
      assert.deepEqual(rights, [404, 404, 404]);
      assert.ok((counts.get('receipt:200') || 0) >= 2);
      assert.ok((counts.get('draft:409') || 0) >= 1);
      await owner.close();
      await member.close();
      await other.close();
      console.log(
        'PASS two accounts, standing sessions, original receipts, viewer/editor, shared CAS, revision history, sibling isolation, downgrade and revoked access.',
      );
    } catch (e) {
      try {
        await active?.screenshot('collaboration-failure');
      } catch {}
      console.error('Collaboration browser check failed at ' + phase, Object.fromEntries(counts));
      throw e;
    }
  },
);
