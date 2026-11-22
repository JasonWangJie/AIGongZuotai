import { useSyncExternalStore } from 'react';

export const languageNames = { 'zh-CN': '简体中文', 'zh-TW': '繁體中文', en: 'English', ja: '日本語' } as const;
export type Locale = keyof typeof languageNames;
export type Preference = Locale | 'auto';
export const preferenceKey = 'xingtu.language.v1';
type Catalog = Readonly<Record<string, string>>;

const loaders = {
  en: () => import('./locales/en.json'),
  ja: () => import('./locales/ja.json'),
  'zh-TW': () => import('./locales/zh-TW.json'),
};
const listeners = new Set<() => void>();
let state: { locale: Locale; preference: Preference } = { locale: 'zh-CN', preference: 'auto' };
let catalog: Catalog = {};
let selection = 0;

export function isLocale(value: unknown): value is Locale {
  return typeof value === 'string' && Object.hasOwn(languageNames, value);
}

export function currentLocale() { return state.locale; }

export function catalogText(key: string, fallback: string): string {
  return Object.hasOwn(catalog, key) ? catalog[key] : fallback;
}

export function uiText(source: string, ...values: unknown[]): string {
  const pattern = catalogText(source, source);
  return values.length ? pattern.replace(/\{(\d+)\}/g, (original, index) =>
    Number(index) < values.length ? String(values[Number(index)]) : original) : pattern;
}

function subscribe(notify: () => void) {
  listeners.add(notify);
  return () => { listeners.delete(notify); };
}
export function useLanguage() { return useSyncExternalStore(subscribe, () => state, () => state); }

async function detectLocale(): Promise<Locale> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 2000);
  try {
    const response = await fetch('/api/locale', { credentials: 'omit', cache: 'no-store', signal: controller.signal });
    if (response.ok) {
      const data = await response.json();
      if (isLocale(data?.locale)) return data.locale;
    }
  } catch { /* An unavailable region service falls back to English. */ }
  finally { clearTimeout(timer); }
  return 'en';
}

export function savedPreference(): Preference {
  try {
    const saved = localStorage.getItem(preferenceKey);
    if (isLocale(saved)) return saved;
  } catch { /* The preference still works for the current page. */ }
  return 'auto';
}

const titles = { 'zh-CN': '星图 AI Studio', 'zh-TW': '星圖 AI Studio', en: 'Xingtu AI Studio', ja: '星図 AI Studio' };
const descriptions = {
  'zh-CN': '星图 AI Studio · 从灵感到作品的创作工作台',
  'zh-TW': '星圖 AI Studio · 從靈感到作品的創作工作台',
  en: 'Xingtu AI Studio · A creative workspace for turning ideas into finished work.',
  ja: '星図 AI Studio · アイデアを作品にするための制作ワークスペース',
};

export async function applyLanguage(preference: Preference, persist = true) {
  const attempt = ++selection;
  const locale = preference === 'auto' ? await detectLocale() : preference;
  if (attempt !== selection) return;
  const next: Catalog = locale === 'zh-CN' ? {} : (await loaders[locale]()).default;
  if (attempt !== selection) return;
  catalog = next;
  state = { locale, preference };
  document.documentElement.lang = locale;
  document.title = titles[locale];
  document.querySelector('meta[name="description"]')?.setAttribute('content', descriptions[locale]);
  if (persist) {
    try {
      if (preference === 'auto') localStorage.removeItem(preferenceKey);
      else localStorage.setItem(preferenceKey, preference);
    } catch { /* Storage can be disabled by the browser. */ }
  }
  for (const notify of listeners) notify();
}
