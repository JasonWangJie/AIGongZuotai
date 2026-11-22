// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { beforeEach, afterEach, it, expect, vi } from 'vitest';
import { ApiError } from './api';
import { PresentationWorkbench } from './PresentationWorkbench';
import {
  blank,
  deckApi,
  deckProblem,
  imagePath,
  outlineDocument,
  readTicket,
  sameVersion,
  saveSlot,
  writable,
} from './presentations';
import type { Deck, DeckVersion, DeckSummary } from './presentations';
import { layoutProblem, loadDelivery, offlineHTML } from './presentationExport';
const noop = () => {},
  props = { owner: 'u', project: 'p', enabled: true, visible: true, onDirty: noop, onExpired: noop };
const d = (): Deck => ({
  title: '计划',
  theme: 'mint',
  slides: [{ id: 'first', title: '目标', layout: 'title', body: '一行正文', notes: '讲者备注' }],
});
const v = (changes: Partial<DeckVersion> = {}): DeckVersion => ({
  id: 'pres',
  project_id: 'p',
  version: 1,
  created_at: 'today',
  document: d(),
  ...changes,
});
const summary = (value: DeckVersion): DeckSummary => ({
  id: value.id,
  project_id: value.project_id,
  version: value.version,
  title: value.document.title,
  slide_count: value.document.slides.length,
  created_at: 'today',
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
const render = (node: React.ReactNode) => act(() => root.render(node));
async function click(label: string) {
  const b = [...view.querySelectorAll('button')].find((e) => e.textContent === label);
  expect(b, label).toBeTruthy();
  expect(b!.disabled).toBe(false);
  await act(() => b!.click());
}
async function field(label: string, value: string) {
  const e = view.querySelector('[aria-label="' + label + '"]') as
    HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;
  expect(e, label).toBeTruthy();
  await act(() => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(e), 'value')!.set!.call(e, value);
    e.dispatchEvent(new Event(e instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }));
  });
}
function setup(...records: DeckVersion[]) {
  vi.spyOn(deckApi, 'list').mockResolvedValue({ items: records.map(summary) });
  vi.spyOn(deckApi, 'versions').mockResolvedValue({ items: records.map(summary) });
  vi.spyOn(deckApi, 'version').mockImplementation(async (_p, id, n) =>
    records.find((x) => x.id === id && x.version === n)!,
  );
}
function measured() {
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    return {
      top: this.classList.contains('pd-body') ? 300 : 100,
      bottom: this.classList.contains('pd-body') ? 500 : 145,
      width: 500,
      height: 45,
      left: 0,
      right: 500,
      x: 0,
      y: 100,
      toJSON: () => ({}),
    } as DOMRect;
  });
}
const img = () => {
  const doc = d();
  doc.slides[0] = {
    ...doc.slides[0],
    layout: 'image',
    asset_id: 'asset',
    asset_revision: 3,
    asset_available: true,
    content_version: 'a'.repeat(64),
  };
  return v({ document: doc });
};
it('derives editable text pages from headings and blank lines, keeping every original paragraph', () => {
  const next = outlineDocument('# 一\r\n甲\r\n## 二\n乙\n\n三\n丙', 'paper', '');
  expect(next.title).toBe('一');
  expect(next.slides.map((s) => [s.title, s.body])).toEqual([
    ['一', '甲'],
    ['二', '乙'],
    ['三', '丙'],
  ]);
  expect(new Set(next.slides.map((s) => s.id)).size).toBe(3);
  const long = '甲'.repeat(160);
  expect(outlineDocument(long, 'paper', '新稿').slides[0].body).toBe(long);
  expect(() =>
    outlineDocument(Array.from({ length: 31 }, () => '# 分页').join('\n'), 'midnight', ''),
  ).toThrow('30');
});
it('strips frozen read-only fields on subsequent edits while preserving original asset revision', () => {
  const original = img();
  expect(writable(original.document).slides[0]).toEqual({
    ...d().slides[0],
    layout: 'image',
    asset_id: 'asset',
    asset_revision: 3,
  });
  expect(original.document.slides[0].content_version).toBeTruthy();
});
it.each([
  ['notes', '超'.repeat(2001)],
  ['body', '超'.repeat(4001)],
  ['title', '多\n行'],
  ['body', '正文\u202e'],
  ['id', 'bad/id'],
])('rejects invalid %s fields without truncating existing input', (k, s) => {
  const doc = d();
  Object.assign(doc.slides[0], { [k]: s });
  expect(deckProblem(doc)).not.toBe('');
  expect(doc.slides[0][k as 'title']).toBe(s);
});
it('enforces four image pages and rejects duplicate slide IDs', () => {
  const a = img();
  a.document.slides = Array.from({ length: 5 }, (_, i) => ({ ...a.document.slides[0], id: 'i' + i }));
  expect(deckProblem(a.document)).toContain('4');
  a.document.slides = a.document.slides.slice(0, 2);
  a.document.slides[1].id = a.document.slides[0].id;
  expect(deckProblem(a.document)).toContain('编号');
});
it.each(['字'.repeat(2500), Array.from({ length: 150 }, () => '条').join('\n')])(
  'keeps dense drafts editable and refuses all delivery before fetching any private images',
  async (body) => {
    const a = img();
    a.document.slides[0].body = body;
    expect(deckProblem(a.document)).toBe('');
    expect(layoutProblem(a)).toContain('第 1 页');
    vi.spyOn(deckApi, 'export').mockResolvedValue({
      ...a,
      image_urls: { first: imagePath('p', 'pres', 1, 'first') },
    });
    const f = vi.fn();
    vi.stubGlobal('fetch', f);
    await expect(loadDelivery('p', 'pres', 1, new AbortController().signal)).rejects.toThrow('拆分');
    expect(f).not.toHaveBeenCalled();
  },
);
it('escapes offline audience text, keeps original notes private, and embeds only provided data images', () => {
  const a = img();
  a.document.title = '<script>alert(1)</script>';
  a.document.slides[0].body = '<img src=x onerror=boom>';
  const html = offlineHTML({
    version: a,
    images: { first: { data: 'data:image/png;base64,AAAA', width: 1, height: 1 } },
  });
  expect(html).toContain('&lt;script&gt;');
  expect(html).toContain('&lt;img');
  expect(html).not.toMatch(/<script|<img src=x|讲者备注|https?:\/\//);
  expect(html).toContain('src="data:image/png;base64,AAAA"');
  expect(html).toContain('Content-Security-Policy');
});
it.each([
  'https://elsewhere.example/image',
  '/api/v1/projects/other/presentations/pres/versions/1/slides/first/image',
  '/api/v1/projects/p/presentations/pres/versions/2/slides/first/image',
])('rejects a nonmatching delivery route %s without following it', async (url) => {
  measured();
  const a = img();
  vi.spyOn(deckApi, 'export').mockResolvedValue({ ...a, image_urls: { first: url } });
  const f = vi.fn();
  vi.stubGlobal('fetch', f);
  await expect(loadDelivery('p', 'pres', 1, new AbortController().signal)).rejects.toThrow('不可用于');
  expect(f).not.toHaveBeenCalled();
});
it.each([
  ['X-Image-Width', '99999999', '像素'],
  ['Content-Length', '10000000', '大小'],
  ['Content-Type', 'image/svg+xml', '大小'],
])('validates %s before image decoding', async (key, value, hint) => {
  measured();
  const a = img();
  vi.spyOn(deckApi, 'export').mockResolvedValue({
    ...a,
    image_urls: { first: imagePath('p', 'pres', 1, 'first') },
  });
  const decode = vi.fn();
  vi.stubGlobal('createImageBitmap', decode);
  vi.stubGlobal(
    'fetch',
    vi
      .fn()
      .mockResolvedValue(
        new Response(new Uint8Array([1, 2, 3]), {
          headers: {
            'Content-Type': 'image/png',
            'X-Image-Width': '10',
            'X-Image-Height': '10',
            [key]: value,
          },
        }),
      ),
  );
  await expect(loadDelivery('p', 'pres', 1, new AbortController().signal)).rejects.toThrow(hint);
  expect(decode).not.toHaveBeenCalled();
});
it('checks project and exact requested version even when the ID is valid', () => {
  expect(sameVersion(v(), 'p', 'pres', 1)).toBe(true);
  expect(sameVersion(v(), 'other', 'pres', 1)).toBe(false);
  expect(sameVersion(v(), 'p', 'pres', 2)).toBe(false);
  expect(sameVersion(v({ document: blank() }), 'p')).toBe(false);
});
it('keeps account-disabled or memory mode free from document calls', async () => {
  const list = vi.spyOn(deckApi, 'list');
  await render(<PresentationWorkbench {...props} enabled={false} />);
  expect(view.textContent).toContain('PostgreSQL');
  expect(list).not.toHaveBeenCalled();
});
it('leaves dirty fields intact on refresh or cancelling document navigation', async () => {
  setup(v());
  await render(<PresentationWorkbench {...props} />);
  await field('项目文稿', 'pres');
  await field('页正文', '本页未保存');
  await click('刷新目录');
  expect(view.querySelector<HTMLTextAreaElement>('[aria-label="页正文"]')?.value).toBe('本页未保存');
  await click('新建文稿');
  await click('保留当前编辑');
  expect(view.querySelector<HTMLTextAreaElement>('[aria-label="页正文"]')?.value).toBe('本页未保存');
});
it('retains typed content on CAS failure and saves a new document only after explicit confirmation', async () => {
  setup(v());
  const save = vi
    .spyOn(deckApi, 'save')
    .mockRejectedValueOnce(new ApiError('冲突', 409, false, 'PRESENTATION_VERSION_CONFLICT'))
    .mockResolvedValue(v({ id: 'fork' }));
  await render(<PresentationWorkbench {...props} />);
  await field('项目文稿', 'pres');
  await field('演讲备注', '局部修订');
  await click('保存固定版本');
  expect(view.textContent).toContain('当前编辑保留');
  expect(save).toHaveBeenCalledTimes(1);
  expect(view.querySelector<HTMLTextAreaElement>('[aria-label="演讲备注"]')?.value).toBe('局部修订');
  await click('将当前编辑另存为新文稿');
  expect(save.mock.calls[1].slice(1, 3)).toEqual([undefined, 0]);
  expect(save.mock.calls[1][3].slides[0].notes).toBe('局部修订');
});
it('recovers a lost save by GET of its original key, scoped to the same account and project', async () => {
  const a = v();
  setup(a);
  const save = vi.spyOn(deckApi, 'save').mockRejectedValue(new ApiError('未确认', 0, true));
  const read = vi.spyOn(deckApi, 'receipt').mockResolvedValue(v({ version: 2 }));
  await render(<PresentationWorkbench {...props} />);
  await field('项目文稿', 'pres');
  await field('页正文', '私有正文不写恢复存储');
  await click('保存固定版本');
  const slot = saveSlot('u', 'p'),
    t = readTicket(slot)!;
  expect(t.key).toBe(save.mock.calls[0][4]);
  expect(sessionStorage.getItem(slot)).not.toContain('私有正文');
  await render(null);
  await render(<PresentationWorkbench {...props} />);
  await click('只读核对原保存回执');
  expect(read).toHaveBeenCalledWith('p', t.key);
  expect(save).toHaveBeenCalledTimes(1);
  expect(readTicket(slot)).toBeNull();
});
it('rejects a mismatched document receipt and retains the original ticket', async () => {
  setup(v());
  const t = { id: 'pres', expected: 1, key: 'original-save-key' };
  sessionStorage.setItem(saveSlot('u', 'p'), JSON.stringify(t));
  vi.spyOn(deckApi, 'receipt').mockResolvedValue(v({ id: 'unrelated', version: 2 }));
  await render(<PresentationWorkbench {...props} />);
  await click('只读核对原保存回执');
  expect(view.textContent).toContain('回执与原文稿版本不符');
  expect(readTicket(saveSlot('u', 'p'))).toEqual(t);
});
