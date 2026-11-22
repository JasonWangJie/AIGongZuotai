import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { copyFile, mkdir, mkdtemp, readFile, readdir, rm } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { until, withLocalBrowser } from './local-browser.mjs';
const variable = (key) => {
  const v = process.env[key];
  if (!v) throw Error('Missing ' + key);
  return v;
};
const url = variable('ACCOUNT_EXPORT_BROWSER_URL'),
  out = resolve(variable('ACCOUNT_EXPORT_BROWSER_OUTPUT'));
const passwordFor = (who) => variable('ACCOUNT_EXPORT_' + who + '_PASSWORD');
const passwordBox = '#account-export-password';
async function enter(page, who) {
  await page.fill('#auth-email', variable('ACCOUNT_EXPORT_' + who + '_EMAIL'));
  await page.fill('#auth-password', passwordFor(who));
  await page.button('进入创作空间', true);
  await page.button('账户安全');
  await until(
    () => page.evaluate(() => !!document.querySelector('#account-export-section')),
    'account security export form',
  );
  return await page.evaluate(async (restricted) => {
    const r = await fetch('/api/v1/auth/session'),
      s = await r.json();
    if (!r.ok || !s.auth_enabled || !!s.account_restricted !== restricted || !s.user?.id)
      throw Error('Incorrect fixture session');
    window.exportRequests = 0;
    const original = window.fetch;
    window.fetch = (...args) => {
      if (
        new URL(String(args[0]), location.href).pathname === '/api/v1/auth/data-export' &&
        args[1]?.method === 'POST'
      )
        window.exportRequests++;
      return original(...args);
    };
    return s.user.id;
  }, who === 'RESTRICTED');
}
async function capture(page, who, section, number, file) {
  const dir = await mkdtemp(join(out, '.account-download-'));
  try {
    await page.downloads(dir);
    await page.fill(passwordBox, passwordFor(who));
    await page.button('验证密码并下载本页', true);
    const entry = await until(async () => {
      const files = await readdir(dir);
      if (files.some((n) => n.endsWith('.crdownload'))) return false;
      return files.find((n) => n === 'account-' + section + '-p' + number + '.json') || false;
    }, 'verified JSON download');
    const bytes = await readFile(join(dir, entry));
    assert.ok(bytes.length > 0 && bytes.length <= 512 * 1024, 'bounded attachment');
    const sha = createHash('sha256').update(bytes).digest('hex');
    await until(
      () =>
        page.evaluate(
          (sha) =>
            document.querySelector('[data-export-sha]')?.dataset.exportSha === sha &&
            document.querySelector('#account-export-password')?.value === '',
          sha,
        ),
      'matching page checksum and cleared password',
    );
    const json = JSON.parse(bytes.toString('utf8'));
    assert.ok(
      json.format === 'studio.account-export.v1' && json.section === section && Array.isArray(json.items),
      'file format and section',
    );
    await copyFile(join(dir, entry), join(out, file));
    return json;
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}
async function focusForm(page) {
  await page.evaluate(() =>
    document.querySelector('.account-export').scrollIntoView({ block: 'center', behavior: 'instant' }),
  );
}
await mkdir(out, { recursive: true });
await withLocalBrowser({ url, out, chromePath: process.env.WORKFLOW_CHROME }, async (open) => {
  const user = await open(),
    restricted = await open();
  try {
    const owner = await enter(user, 'USER'),
      other = await enter(restricted, 'RESTRICTED');
    assert.notEqual(owner, other, 'independent accounts');
    const bad = await mkdtemp(join(out, '.account-denied-'));
    try {
      await user.downloads(bad);
      await user.fill(passwordBox, 'invalid-' + crypto.randomUUID());
      await user.button('验证密码并下载本页', true);
      await until(
        () =>
          user.evaluate(
            () =>
              document.querySelector('.account-export-error')?.textContent.includes('密码不正确') &&
              document.querySelector('#account-export-password')?.value === '',
          ),
        'wrong password clears form and refuses',
      );
      assert.equal((await readdir(bad)).length, 0, 'wrong credentials yield no file');
    } finally {
      await rm(bad, { recursive: true, force: true });
    }
    const profile = await capture(user, 'USER', 'profile', 1, 'profile.json');
    assert.ok(
      profile.account_id === owner &&
        profile.items.length === 1 &&
        profile.items[0].id === owner &&
        profile.items[0].email === variable('ACCOUNT_EXPORT_USER_EMAIL'),
      'own profile fields',
    );
    await user.fill('#account-export-section', 'security_events');
    const first = await capture(user, 'USER', 'security_events', 1, 'security-events-page-1.json');
    assert.ok(
      first.account_id === owner && first.items.length === 100 && typeof first.next_cursor === 'string',
      'real second page required',
    );
    await focusForm(user);
    await user.screenshot('account-export-desktop');
    // Verify the server binds a real, unmodified continuation to the original account.
    const rejected = await restricted.evaluate(
      async (cursor, password) => {
        const r = await fetch('/api/v1/auth/data-export', {
          method: 'POST',
          credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ section: 'security_events', password, cursor }),
        });
        const attachment = r.headers.has('Content-Disposition') || r.headers.has('X-Content-SHA256');
        const code = (await r.json()).error?.code;
        return { status: r.status, code, attachment };
      },
      first.next_cursor,
      passwordFor('RESTRICTED'),
    );
    assert.deepEqual(rejected, { status: 400, code: 'INVALID_EXPORT_CURSOR', attachment: false });
    await user.button('准备下一页', true);
    assert.equal(
      await user.evaluate(() => window.exportRequests),
      3,
      'selecting continuation performs no POST',
    );
    const second = await capture(user, 'USER', 'security_events', 2, 'security-events-page-2.json');
    assert.ok(
      second.account_id === owner &&
        first.created_through === second.created_through &&
        second.items.length > 0,
      'same account and cutoff',
    );
    const firstIDs = new Set(first.items.map((r) => r.id));
    assert.ok(!second.items.some((r) => firstIDs.has(r.id)), 'nonoverlapping pages');
    await restricted.fill('#account-export-section', 'account_appeals');
    const appeals = await capture(restricted, 'RESTRICTED', 'account_appeals', 1, 'restricted-appeals.json');
    assert.ok(
      appeals.account_id === other &&
        appeals.items.length > 0 &&
        appeals.items.every((r) => r.restriction_id && typeof r.statement === 'string'),
      'restricted owner appeal export',
    );
    await restricted.size(390, 844);
    await focusForm(restricted);
    assert.ok(
      await restricted.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
      '390px viewport',
    );
    await restricted.screenshot('account-export-mobile');
    assert.equal(await user.evaluate(() => window.exportRequests), 4);
    assert.equal(await restricted.evaluate(() => window.exportRequests), 2);
    console.log(
      'PASS two accounts, current password, actual JSON and checksums, first and continued pages, foreign cursor denied, restricted account appeal, desktop and 390px.',
    );
  } finally {
    await user.close();
    await restricted.close();
  }
});
