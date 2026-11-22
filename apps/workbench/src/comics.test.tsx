// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, it, expect, vi } from 'vitest';
import { ApiError } from './api';
import { ComicWorkbench } from './ComicWorkbench';
import {
  comicApi,
  comicProblem,
  comicSlot,
  comicImagePath,
  matchesComic,
  usableComicImage,
  writableComic,
  readComicTicket,
} from './comics';
import type { Comic, ComicVersion, ComicManifest } from './comics';
import { comicPowerPoint, paginateComic } from './comicExport';
import * as ppt from './presentationExport';
import type { Slide } from './presentations';
import type { AssetEntry } from './libraryState';
const noop = () => {},
  props = { owner: 'u', project: 'p', enabled: true, visible: true, onDirty: noop, onExpired: noop };
const hash = 'a'.repeat(64);
const doc = (): Comic => ({
  title: '剧本',
  story: '故事',
  characters: [{ id: 'star', name: '星星', description: '' }],
  scenes: [{ id: 'sea', name: '海', description: '' }],
  panels: [
    {
      id: 'first',
      title: '出发',
      action: '起身',
      dialogue: '好',
      scene_id: 'sea',
      character_ids: ['star'],
      asset_id: 'lib',
      asset_revision: 1,
      content_version: hash,
      asset_available: true,
    },
  ],
});
const v = (n = 1, confirmed = false): ComicVersion => ({
  project_id: 'p',
  version: n,
  created_at: 't',
  confirmed,
  document: doc(),
});
const summary = (val: ComicVersion) => ({
  project_id: val.project_id,
  version: val.version,
  title: val.document.title,
  panel_count: val.document.panels.length,
  confirmed: val.confirmed,
  created_at: val.created_at,
});
const slide = (): Slide => ({
  id: 'first',
  layout: 'image',
  title: '出发',
  body: '场景：海\n角色：星星\n动作：起身\n对白：好',
  notes: '故事梗概：故事',
  asset_id: 'lib',
  asset_revision: 1,
  content_version: hash,
  asset_available: true,
});
const manifest = (): ComicManifest => ({
  source: 'comic',
  project_id: 'p',
  version: 1,
  created_at: 't',
  document: { title: '剧本', theme: 'paper', slides: [slide()] },
  image_urls: { first: comicImagePath('p', 1, 'first') },
});
let root: ReturnType<typeof createRoot>, view: HTMLDivElement;
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  sessionStorage.clear();
  view = document.createElement('div');
  document.body.append(view);
  root = createRoot(view);
});
afterEach(async () => {
  await act(() => root.unmount());
  view.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
const render = () => act(() => root.render(<ComicWorkbench {...props} />));
const button = (s: string) => Array.from(view.querySelectorAll('button')).find((b) => b.textContent === s);
async function click(s: string) {
  const b = button(s);
  expect(b, s).toBeDefined();
  expect(b!.disabled).toBe(false);
  await act(() => b!.click());
}
async function field(name: string, val: string) {
  const el = view.querySelector('[aria-label="' + name + '"]') as
    HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;
  expect(el, name).toBeDefined();
  await act(() => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), 'value')!.set!.call(el, val);
    el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }));
  });
}
function setup(val = v()) {
  vi.spyOn(comicApi, 'current').mockResolvedValue(val);
  vi.spyOn(comicApi, 'history').mockResolvedValue({ items: [summary(val)] });
}
it('freezes shared role, scene and ordered image identity while stripping read-only projections', () => {
  const d = doc();
  d.panels.push({ ...d.panels[0], id: 'next' });
  expect(comicProblem(d)).toBe('');
  const copy = writableComic(d);
  expect(copy.panels[1].asset_id).toBe('lib');
  expect(copy.panels[1]).not.toHaveProperty('asset_available');
  expect(copy.panels[1]).not.toHaveProperty('content_version');
  copy.characters[0].name = '新名称';
  copy.panels[0].character_ids.length = 0;
  expect(d.characters[0].name).toBe('星星');
  expect(d.panels[0].character_ids).toHaveLength(1);
});
it.each(['scene', 'cast', 'duplicate', 'images', 'text'])('validates %s before a save', (what) => {
  const d = doc(),
    p = d.panels[0];
  if (what === 'scene') p.scene_id = 'other';
  if (what === 'cast') p.character_ids = ['other'];
  if (what === 'duplicate') p.character_ids = ['star', 'star'];
  if (what === 'images') d.panels = Array.from({ length: 5 }, (_, i) => ({ ...p, id: 'c' + i }));
  if (what === 'text') p.action = '字'.repeat(1001);
  expect(comicProblem(d)).not.toBe('');
});
it('filters project scope, real image MIME and moderation', () => {
  const a = {
    project_id: 'p',
    state: 'active',
    kind: 'image',
    content_url: '/safe',
    content_type: 'image/png',
    revision: 1,
  } as AssetEntry;
  expect(usableComicImage(a, 'p')).toBe(true);
  expect(usableComicImage(a, 'q')).toBe(false);
  expect(usableComicImage({ ...a, content_type: 'image/svg+xml' }, 'p')).toBe(false);
  expect(usableComicImage({ ...a, content_url: null }, 'p')).toBe(false);
  expect(matchesComic(v(), 'q', 1)).toBe(false);
});
it('paginates dense CJK, long titles and forced line breaks without dropping a grapheme or duplicating images', () => {
  const original = {
    ...slide(),
    title: '长标题'.repeat(40),
    body: '中文动作。'.repeat(150) + '\n'.repeat(240) + '对白：🇨🇳收尾。',
    notes: '完整原备注',
  };
  const later = {
    ...slide(),
    id: 'continuation_0_2',
    layout: 'content' as const,
    title: '后镜',
    body: '短文',
    notes: '末镜',
  };
  const pages = paginateComic(
    [original, later],
    (s) => [...s.title].length <= 28 && [...s.body].length <= 35 && s.body.split('\n').length <= 5,
  );
  expect(pages.length).toBeGreaterThan(30);
  expect(pages.filter((p) => p.layout === 'image')).toHaveLength(1);
  expect(pages[0].notes).toBe(original.notes);
  expect(
    pages
      .slice(0, -1)
      .map((p) => p.body)
      .join(''),
  ).toBe(original.title + '\n' + original.body);
  expect(pages.at(-1)).toEqual(later);
  expect(new Set(pages.map((p) => p.id)).size).toBe(pages.length);
  expect(pages.some((p) => p.body.includes('🇨🇳'))).toBe(true);
});
it('keeps a short panel as a single original native slide', () => {
  const s = slide();
  expect(paginateComic([s], () => true)).toEqual([s]);
});
it('separates original save and author-confirmation receipts and never POSTs on re-entry', async () => {
  const key = 'original-key-123';
  sessionStorage.setItem(comicSlot('u', 'p'), JSON.stringify({ operation: 'confirm', expected: 1, key }));
  sessionStorage.setItem(
    comicSlot('u', 'q'),
    JSON.stringify({ operation: 'save', expected: 0, key: 'other-key-123456' }),
  );
  setup();
  const confirm = vi.spyOn(comicApi, 'confirm'),
    save = vi.spyOn(comicApi, 'save'),
    r = vi
      .spyOn(comicApi, 'receipt')
      .mockResolvedValueOnce({ ...v(1, true), operation: 'save' })
      .mockResolvedValueOnce({ ...v(1, true), operation: 'confirm' });
  await render();
  await click('只读核对原确认回执');
  expect(readComicTicket(comicSlot('u', 'p'))?.key).toBe(key);
  await click('只读核对原确认回执');
  expect(r).toHaveBeenCalledWith('p', 'confirm', key);
  expect(readComicTicket(comicSlot('u', 'p'))).toBeNull();
  expect(readComicTicket(comicSlot('u', 'q'))).not.toBeNull();
  expect(confirm).not.toHaveBeenCalled();
  expect(save).not.toHaveBeenCalled();
});
it('retains unsaved input on refresh, stale CAS and original read, with an explicit rebase', async () => {
  setup();
  const s = vi
    .spyOn(comicApi, 'save')
    .mockRejectedValueOnce(new ApiError('版本冲突', 409, false, 'COMIC_VERSION_CONFLICT'))
    .mockResolvedValueOnce(v(3));
  await render();
  await field('镜头动作', '旧窗口动作');
  await click('刷新版本');
  await click('保存分镜版本');
  expect(view.textContent).toContain('本页输入仍保留');
  vi.mocked(comicApi.current).mockResolvedValue(v(2));
  await click('核对服务器最新版');
  await click('保留本页并基于此版编辑');
  expect((view.querySelector('[aria-label="镜头动作"]') as HTMLTextAreaElement).value).toBe('旧窗口动作');
  await click('保存分镜版本');
  expect(s.mock.calls.map((c) => c[1])).toEqual([1, 2]);
});
it('requires an explicit second action before confirmation and never exports an unconfirmed version', async () => {
  setup();
  const confirm = vi.spyOn(comicApi, 'confirm').mockResolvedValue(v(1, true));
  await render();
  expect(button('下载分镜 PowerPoint')).toBeUndefined();
  await click('作者确认此版');
  expect(confirm).not.toHaveBeenCalled();
  await click('确认此分镜版本');
  expect(confirm).toHaveBeenCalledTimes(1);
  expect(button('下载分镜 PowerPoint')).toBeDefined();
});
it.each(['scope', 'revision', 'url', 'unconfirmed'])(
  'rejects wrong %s before any asset request',
  async (what) => {
    const m = manifest();
    if (what === 'scope') m.project_id = 'q';
    if (what === 'url') m.image_urls.first = 'https://wrong.invalid/photo';
    if (what === 'revision') m.document.slides[0].asset_revision = 2;
    vi.spyOn(comicApi, 'manifest').mockResolvedValue(m);
    const f = vi.fn();
    vi.stubGlobal('fetch', f);
    await expect(
      comicPowerPoint(v(1, what !== 'unconfirmed'), new AbortController().signal),
    ).rejects.toThrow();
    expect(f).not.toHaveBeenCalled();
  },
);
it('re-reads the authorized raw manifest after serializing the PowerPoint and blocks a revoked image', async () => {
  const m = manifest();
  const request = vi
    .spyOn(comicApi, 'manifest')
    .mockResolvedValueOnce(m)
    .mockRejectedValueOnce(new ApiError('配图被回收', 409));
  vi.spyOn(ppt, 'withLayoutProbe').mockImplementation((_v, fn) => fn(() => true));
  vi.spyOn(ppt, 'loadVersionSlides').mockResolvedValue({ version: m, images: {} });
  const native = vi.spyOn(ppt, 'nativePptx').mockResolvedValue(new Blob(['native']));
  await expect(comicPowerPoint(v(1, true), new AbortController().signal)).rejects.toThrow('配图被回收');
  expect(native).toHaveBeenCalledTimes(1);
  expect(request).toHaveBeenCalledTimes(2);
});
