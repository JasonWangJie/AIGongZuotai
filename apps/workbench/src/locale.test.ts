// @vitest-environment jsdom
import { afterEach, expect, it, vi } from 'vitest';
import { applyLanguage, currentLocale, preferenceKey, savedPreference, uiText } from './locale';
import en from './locales/en.json';
import ja from './locales/ja.json';
import traditional from './locales/zh-TW.json';

afterEach(async () => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  localStorage.clear();
  await applyLanguage('zh-CN', false);
});

it('keeps all catalogs aligned, including repeated and reordered substitutions', () => {
  const keys = Object.keys(en).sort();
  const fields = (text: string) => [...text.matchAll(/\{\d+\}/g)].map(m => m[0]).sort();
  for (const dictionary of [en, ja, traditional] as Record<string, string>[]) {
    expect(Object.keys(dictionary).sort()).toEqual(keys);
    for (const [source, translation] of Object.entries(dictionary)) {
      expect(translation.trim(), source).not.toBe('');
      expect(fields(translation), source).toEqual(fields(source));
    }
  }
});

it('uses English if the region request fails or returns an unsupported locale', async () => {
  const request = vi.fn().mockRejectedValueOnce(Error()).mockResolvedValueOnce(Response.json({locale: 'de'}));
  vi.stubGlobal('fetch', request);
  localStorage.setItem(preferenceKey, 'toString');
  expect(savedPreference()).toBe('auto');
  for (let i = 0; i < 2; i++) {
    await applyLanguage('auto');
    expect(currentLocale()).toBe('en');
    expect(document.documentElement.lang).toBe('en');
    expect(uiText('图像工作室')).toBe(en['图像工作室']);
    expect(localStorage.getItem(preferenceKey)).toBeNull();
  }
});

it('gives a manual choice priority over a pending lookup and persists only that choice', async () => {
  let answer!: (response: Response) => void;
  const request = vi.fn(() => new Promise<Response>(resolve => { answer = resolve; }));
  vi.stubGlobal('fetch', request);
  const detection = applyLanguage('auto');
  await applyLanguage('ja');
  answer(Response.json({locale: 'zh-TW'}));
  await detection;
  expect(currentLocale()).toBe('ja');
  await applyLanguage(savedPreference(), false);
  expect(request).toHaveBeenCalledTimes(1);
  expect(savedPreference()).toBe('ja');
});

it('retains a page-local choice when browser storage is unavailable', async () => {
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw Error(); });
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw Error(); });
  expect(savedPreference()).toBe('auto');
  await applyLanguage('zh-TW');
  expect(currentLocale()).toBe('zh-TW');
  expect(uiText('编辑第 {0} 镜', '{1}')).toContain('{1}');
});
