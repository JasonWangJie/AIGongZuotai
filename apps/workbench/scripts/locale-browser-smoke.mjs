import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { withLocalBrowser, until } from './local-browser.mjs';

const url = process.env.LOCALE_BROWSER_URL;
const expected = { 'zh-CN': '图像工作室' };
const emailLabels = { 'zh-CN': '邮箱地址' };
for (const lang of ['zh-TW', 'en', 'ja']) {
  const catalog = JSON.parse(await readFile(new URL('../src/locales/' + lang + '.json', import.meta.url), 'utf8'));
  expected[lang] = catalog['图像工作室'];
  emailLabels[lang] = catalog['邮箱地址'];
}
const regions = [
  ['114.114.114.114', 'zh-CN'], ['203.198.7.66', 'zh-TW'], ['168.95.1.1', 'zh-TW'],
  ['202.175.3.8', 'zh-TW'], ['2001:240::1', 'ja'], ['8.8.8.8', 'en'],
];

await withLocalBrowser({url, out: process.env.LOCALE_SCREENSHOTS, onlyOrigin: true, chromePath: process.env.WORKFLOW_CHROME}, async open => {
  for (const [ip, lang] of regions) {
    const page = await open({deferNavigation: true, language: null, width: lang === 'ja' ? 390 : 1366});
    try {
      const select = '.topbar .language-picker select';
      const matches = async code => until(() => page.evaluate((code, text) =>
        document.documentElement.lang === code &&
        document.querySelector('.breadcrumbs strong')?.textContent === text,
      code, expected[code]), 'workbench in ' + code).catch(async error => {
        const visible = await page.evaluate(() => [document.documentElement.lang, document.querySelector('.breadcrumbs strong')?.textContent]);
        throw new Error(error.message + ' (rendered ' + JSON.stringify(visible) + ')');
      });
      await page.command('Network.setExtraHTTPHeaders', {headers: {'X-Forwarded-For': ip}});
      await page.command('Page.navigate', {url});
      await matches(lang);
      assert.equal(await page.evaluate(s => document.querySelector(s).value, select), 'auto');
      if (lang === 'en' || lang === 'ja') await page.screenshot(lang + '-workbench');
      if (lang !== 'ja') continue;

      const original = '图像工作室 · 日本語 · {0}';
      await page.fill('.composer textarea', original);
      for (const choice of ['en', 'zh-TW', 'zh-CN']) {
        await page.fill(select, choice);
        await matches(choice);
        assert.equal(await page.evaluate(() => document.querySelector('.composer textarea').value), original);
      }
      const bounds = await page.evaluate(s => {
        const rect = document.querySelector(s).getBoundingClientRect();
        return [rect.left, rect.right, innerWidth];
      }, select);
      assert.ok(bounds[0] >= 0 && bounds[1] <= bounds[2], 'language menu fits on mobile');
      await page.command('Page.reload');
      await matches('zh-CN');
      assert.equal(await page.evaluate(() => performance.getEntriesByType('resource')
        .filter(r => new URL(r.name).pathname === '/api/locale').length), 0);
      await page.fill(select, 'auto');
      await matches('ja');
      assert.equal(await page.evaluate(() => localStorage.getItem('xingtu.language.v1')), null);
    } finally { await page.close(); }
  }
  // A signed-out fixture supplies only the session response. The region service,
  // production bundle and browser storage still run through the normal entry.
  const login = await open({deferNavigation: true, language: null, width: 390});
  try {
    await login.command('Page.addScriptToEvaluateOnNewDocument', {source: `{
      const original = window.fetch.bind(window);
      window.fetch = (input, options) => new URL(input, location.href).pathname === '/api/v1/auth/session'
        ? Promise.resolve(Response.json({auth_enabled:true, user:null}))
        : original(input, options);
    }`});
    await login.command('Network.setExtraHTTPHeaders', {headers: {'X-Forwarded-For': '133.242.0.1'}});
    await login.command('Page.navigate', {url});
    await until(() => login.evaluate(label => document.querySelector('label[for=auth-email]')?.textContent === label, emailLabels.ja), 'Japanese login');
    await login.fill('#auth-email', 'sample@example.invalid');
    await login.fill('.auth-top select', 'en');
    await until(() => login.evaluate(label => document.querySelector('label[for=auth-email]')?.textContent === label, emailLabels.en), 'English login');
    assert.equal(await login.evaluate(() => document.querySelector('#auth-email').value), 'sample@example.invalid');
    await login.evaluate(() => document.querySelector('.forgot-password-link').click());
    await login.fill('.account-recovery-page select', 'zh-TW');
    await until(() => login.evaluate(() => document.documentElement.lang === 'zh-TW' && !!document.querySelector('#recovery-email')), 'Traditional Chinese recovery');
  } finally { await login.close(); }
});
console.log('Passed six regional first visits, all four languages, mobile menu, draft preservation, saved preference, return to automatic detection, and signed-out login/recovery controls.');
