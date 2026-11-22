// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { beforeEach, afterEach, vi, it, expect } from 'vitest';
import { api, ApiError } from './api';
import type { AssetEntry } from './libraryState';
import { FreeCanvasWorkbench } from './FreeCanvasWorkbench';
import { emptyCanvas, freeCanvasApi } from './freeCanvas';
import { applyLanguage, uiText } from './locale';
import type { CanvasVersion, CanvasSources } from './freeCanvas';
const noop = () => {};
const base = {
  owner: 'user',
  project: 'p',
  visible: true,
  enabled: true,
  refreshVersion: 0,
  sourceVersion: 0,
  assetVersion: 0,
  onDirty: noop,
  onExpired: noop,
  runs: [],
};
const original: CanvasVersion = {
  project_id: 'p',
  revision: 1,
  created_at: '',
  sources: {},
  document: {
    ...emptyCanvas(),
    nodes: [{ id: 'n', title: '原节点', note: '原始备注', x: 0, y: 0 }],
    title: '初稿',
  },
};
let root: ReturnType<typeof createRoot>, el: HTMLDivElement;
const render = async (p: Partial<typeof base> = {}) =>
  act(() =>
    root.render(
      <FreeCanvasWorkbench
        key={(p.owner || base.owner) + ' ' + (p.project || base.project)}
        {...base}
        {...p}
      />,
    ),
  );
function button(label: string) {
  const b = [...el.querySelectorAll('button')].find((b) => b.textContent === label);
  expect(b, label).toBeTruthy();
  return b!;
}
const click = async (label: string) => {
  const b = button(label);
  expect(b.disabled, label).toBe(false);
  await act(() => b.click());
};
const field = (id: string) => el.querySelector<HTMLInputElement>('#' + id)!;
async function type(id: string, value: string) {
  const f = field(id);
  await act(() => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(f), 'value')!.set!.call(f, value);
    f.dispatchEvent(new Event('input', { bubbles: true }));
  });
}
function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { resolve, promise };
}
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  sessionStorage.clear();
  el = document.createElement('div');
  document.body.append(el);
  root = createRoot(el);
  vi.spyOn(HTMLElement.prototype, 'clientWidth', 'get').mockReturnValue(640);
  vi.spyOn(HTMLElement.prototype, 'clientHeight', 'get').mockReturnValue(480);
  vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue(null);
  vi.spyOn(freeCanvasApi, 'current').mockResolvedValue(original);
  vi.spyOn(freeCanvasApi, 'sources').mockResolvedValue({ project_id: 'p', revision: 1, sources: {} });
  vi.spyOn(freeCanvasApi, 'revisions').mockResolvedValue({
    items: [{ revision: 1, title: '初稿', node_count: 1, edge_count: 0, created_at: '' }],
  });
});
afterEach(async () => {
  await act(() => root.unmount());
  el.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
async function chooseNode() {
  await act(() => el.querySelector<HTMLButtonElement>('[data-node-id="n"]')!.click());
}
it('preserves the original save key across an unconfirmed response and recovers exclusively by GET', async () => {
  const save = vi.spyOn(freeCanvasApi, 'save').mockRejectedValue(new ApiError('回执未知', 503, true));
  const receipt = vi.spyOn(freeCanvasApi, 'receipt').mockResolvedValue({ ...original, revision: 2 });
  await render();
  await chooseNode();
  await type('free-node-note', '修订备注');
  await click('保存画布');
  expect(save.mock.calls[0][2].nodes[0].note).toBe('修订备注');
  expect(button('保存画布').disabled).toBe(true);
  const key = save.mock.calls[0][3];
  expect(sessionStorage.getItem('studio-free-canvas-pending-1')).not.toContain('修订备注');
  await render({ visible: false });
  await render();
  expect(save).toHaveBeenCalledTimes(1);
  await click('核对原保存回执');
  expect(receipt).toHaveBeenCalledWith('p', key, expect.any(AbortSignal));
  expect(el.textContent).toContain('已确认画布修订 v2');
  expect(save).toHaveBeenCalledTimes(1);
});
it('keeps local edits across CAS, history and invalidations until the user explicitly discards them', async () => {
  vi.spyOn(freeCanvasApi, 'save').mockRejectedValue(new ApiError('冲突', 409, false, 'CANVAS_CONFLICT'));
  const newHead = { ...original, revision: 2, document: { ...original.document, title: '云端修订' } };
  await render();
  await chooseNode();
  await type('free-node-title', '尚未保存');
  vi.mocked(freeCanvasApi.revisions).mockResolvedValue({
    items: [{ revision: 2, title: '云端修订', node_count: 1, edge_count: 0, created_at: '' }],
  });
  await click('保存画布');
  await render({ refreshVersion: 1 });
  expect(field('free-node-title').value).toBe('尚未保存');
  expect(button('保存画布').disabled).toBe(true);
  await click('读取最新修订');
  await click('保留本页编辑');
  expect(field('free-node-title').value).toBe('尚未保存');
  vi.mocked(freeCanvasApi.current).mockResolvedValue(newHead);
  await click('读取最新修订');
  await click('放弃编辑并读取');
  expect(el.querySelector('h2')?.textContent).toBe('云端修订');
});
it('retains the original empty merge base when the interface language changes before the first save', async () => {
  vi.mocked(freeCanvasApi.current).mockRejectedValueOnce(new ApiError('missing', 404, false, 'CANVAS_NOT_FOUND'));
  vi.mocked(freeCanvasApi.revisions).mockResolvedValue({ items: [], next_before_revision: undefined });
  const save = vi.spyOn(freeCanvasApi, 'save');
  await render();
  await click('添加节点');
  const initialTitle = el.querySelector('h2')?.textContent;
  try {
    await act(() => applyLanguage('en', false));
    expect(emptyCanvas().title).not.toBe(initialTitle);
    vi.mocked(freeCanvasApi.revisions).mockResolvedValue({
      items: [{ revision: 1, title: original.document.title, node_count: 1, edge_count: 0, created_at: '' }],
    });
    await render({ refreshVersion: 1 });
    await click(uiText('核对并合并本页编辑'));
    await click(uiText('采用合并草稿'));
    expect(el.querySelector('h2')?.textContent).toBe(original.document.title);
    expect(el.querySelectorAll('[data-node-id]')).toHaveLength(2);
    expect(save).not.toHaveBeenCalled();
  } finally {
    await act(() => applyLanguage('zh-CN', false));
  }
});
it('merges independently edited fields on different records and saves only after a fresh click with the new CAS', async () => {
  const save = vi
    .spyOn(freeCanvasApi, 'save')
    .mockRejectedValueOnce(new ApiError('冲突', 409, false, 'CANVAS_CONFLICT'));
  await render();
  await chooseNode();
  await type('free-node-note', '本页备注');
  vi.mocked(freeCanvasApi.revisions).mockResolvedValue({
    items: [{ revision: 2, title: '远端改名', node_count: 1, edge_count: 0, created_at: '' }],
  });
  await click('保存画布');
  const v2 = { ...original, revision: 2, document: { ...original.document, title: '远端改名' } };
  vi.mocked(freeCanvasApi.current).mockResolvedValue(v2);
  await click('核对并合并本页编辑');
  await click('采用合并草稿');
  expect(save).toHaveBeenCalledTimes(1);
  expect(el.querySelector('h2')?.textContent).toBe('远端改名');
  expect(field('free-node-note').value).toBe('本页备注');
  save.mockResolvedValue({ ...v2, revision: 3 });
  await click('保存画布');
  expect(save.mock.calls[1][1]).toBe(2);
  expect(save.mock.calls[1][2].nodes[0].note).toBe('本页备注');
});
it('rechecks private image permissions without reading the full graph and ignores old scope reads', async () => {
  const path = '/api/v1/projects/p/canvas/revisions/1/nodes/n/thumbnail?project_id=p';
  const img = { available: true, content_version: 'a'.repeat(64), thumbnail_url: path };
  const bound = {
    ...original,
    document: {
      ...original.document,
      nodes: [{ ...original.document.nodes[0], asset_id: 'a', asset_revision: 3 }],
    },
  };
  vi.mocked(freeCanvasApi.current).mockResolvedValue(bound);
  vi.mocked(freeCanvasApi.sources).mockResolvedValue({ project_id: 'p', revision: 1, sources: { n: img } });
  await render();
  expect(el.querySelector('img')?.getAttribute('src')).toBe(path);
  const old = deferred<{ project_id: string; revision: number; sources: CanvasSources }>();
  vi.mocked(freeCanvasApi.sources).mockReturnValue(old.promise);
  await render({ sourceVersion: 1 });
  expect(el.querySelector('img')?.getAttribute('src')).toBe(path);
  await render({ sourceVersion: 1, assetVersion: 1 });
  expect(el.querySelector('img')).toBeNull();
  expect(freeCanvasApi.current).toHaveBeenCalledTimes(1);
  vi.mocked(freeCanvasApi.current).mockResolvedValue({
    ...original,
    project_id: 'q',
    document: { ...emptyCanvas(), title: '另一个项目' },
  });
  await render({ project: 'q' });
  expect(vi.mocked(freeCanvasApi.sources).mock.lastCall![2]?.aborted).toBe(true);
  await act(() => old.resolve({ project_id: 'p', revision: 1, sources: { n: img } }));
  expect(el.querySelector('img')).toBeNull();
  expect(el.querySelector('h2')?.textContent).toBe('另一个项目');
});
it('moves the selected node with a keyboard, groups nodes and releases the hidden viewport', async () => {
  vi.mocked(freeCanvasApi.current).mockResolvedValue({
    ...original,
    document: {
      ...original.document,
      nodes: [{ ...original.document.nodes[0], group_id: 'g' }],
      groups: [{ id: 'g', title: '分组一', collapsed: false }],
    },
  });
  await render();
  await chooseNode();
  const viewport = el.querySelector('.free-canvas-viewport')!;
  await act(() =>
    viewport.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'ArrowRight', shiftKey: true, bubbles: true }),
    ),
  );
  expect(field('free-node-x').value).toBe('8');
  const select = el.querySelector<HTMLSelectElement>('#free-group-select')!;
  await act(() => {
    select.value = 'g';
    select.dispatchEvent(new Event('change', { bubbles: true }));
  });
  await click('折叠分组');
  expect(el.querySelector('[data-group-id="g"]')).toBeTruthy();
  expect(el.querySelector('[data-node-id="n"]')).toBeNull();
  await click('展开分组');
  expect(el.querySelector('[data-node-id="n"]')).toBeTruthy();
  await render({ visible: false });
  expect(el.querySelector('.free-canvas-viewport')).toBeNull();
  await render();
  await chooseNode();
  expect(field('free-node-x').value).toBe('8');
});
it('prevents memory-mode actions and excludes receipts from other accounts', async () => {
  await render({ enabled: false });
  expect(freeCanvasApi.current).not.toHaveBeenCalled();
  expect(el.querySelector('.free-canvas-viewport')).toBeNull();
  expect(el.textContent).toContain('PostgreSQL');
  sessionStorage.setItem(
    'studio-free-canvas-pending-1',
    JSON.stringify({ [JSON.stringify(['other', 'p'])]: 'other-key' }),
  );
  await render();
  expect(el.textContent).not.toContain('核对原保存回执');
});
it('only offers readable approved images in this project and submits their metadata revision', async () => {
  const image: AssetEntry = {
    id: 'ref',
    name: '已审核上传',
    project_id: 'p',
    kind: 'image',
    source: 'reference',
    state: 'active',
    revision: 4,
    tags: [],
    created_at: '',
    updated_at: '',
    deleted_at: null,
    byte_size: 12,
    content_type: 'image/png',
    content_url: '/own',
    thumbnail_url: '/own/thumbnail',
    moderation: { state: 'ALLOWED', phase: 'input', version: 1, policy_id: 'p', policy_version: '1' },
  };
  const choices = [
    image,
    { ...image, id: 'other', name: '其他项目', project_id: 'q' },
    { ...image, id: 'old', name: '未经审核', moderation: undefined },
    { ...image, id: 'huge', name: '太大', byte_size: 9 * 1024 * 1024 },
  ];
  const list = vi
    .spyOn(api, 'library')
    .mockResolvedValue({ items: choices, has_more: false, limit: 20, next_cursor: null });
  const save = vi.spyOn(freeCanvasApi, 'save').mockResolvedValue({ ...original, revision: 2 });
  await render();
  await chooseNode();
  await click('选择项目图片');
  expect(list).toHaveBeenCalledWith(
    'p',
    expect.objectContaining({ kind: 'image', source: '' }),
    undefined,
    expect.any(AbortSignal),
  );
  expect(el.textContent).not.toMatch(/其他项目|未经审核|太大/);
  await click('已审核上传 · v4');
  await click('保存画布');
  expect(save.mock.calls[0][2].nodes[0]).toMatchObject({ asset_id: 'ref', asset_revision: 4 });
  expect(save.mock.calls[0][2].nodes[0]).not.toHaveProperty('thumbnail_url');
});
it.each([403, 404])('clears the graph when rechecking its read permission returns %i', async (status) => {
  const v = {
    ...original,
    document: { ...original.document, nodes: [{ ...original.document.nodes[0], run_id: 'run' }] },
  };
  vi.mocked(freeCanvasApi.current).mockResolvedValue(v);
  vi.mocked(freeCanvasApi.sources).mockResolvedValue({
    project_id: 'p',
    revision: 1,
    sources: { n: { available: true, status: 'COMPLETED', seq: 7 } },
  });
  await render();
  expect(el.querySelector('.free-canvas-run')?.getAttribute('data-status')).toBe('COMPLETED');
  vi.mocked(freeCanvasApi.sources).mockRejectedValue(
    new ApiError('无权读取', status, false, status === 404 ? 'CANVAS_NOT_FOUND' : undefined),
  );
  await render({ sourceVersion: 1 });
  expect(el.querySelector('.free-canvas-viewport')).toBeNull();
  expect(el.textContent).not.toContain('原节点');
  expect(el.textContent).toContain('已失去');
});

it('coalesces Run hints on a slow fixed revision, and never republishes an invalidated image', async () => {
  const path = '/api/v1/projects/p/canvas/revisions/1/nodes/n/thumbnail?project_id=p';
  const img = { available: true, content_version: 'a'.repeat(64), thumbnail_url: path };
  const run = (status: string, seq: number) => ({ available: true, status, seq });
  const result = (status: string, seq: number, includeImage = true) => ({
    project_id: 'p',
    revision: 1,
    sources: { n: includeImage ? img : { available: false }, r: run(status, seq) },
  });
  vi.mocked(freeCanvasApi.current).mockResolvedValue({
    ...original,
    ...result('RUNNING', 1),
    document: {
      ...original.document,
      nodes: [
        { ...original.document.nodes[0], asset_id: 'a', asset_revision: 3 },
        { id: 'r', title: '已有对话', run_id: 'run', x: 200, y: 0 },
      ],
    },
  });
  const first = deferred<ReturnType<typeof result>>(),
    second = deferred<ReturnType<typeof result>>();
  const reads = vi
    .mocked(freeCanvasApi.sources)
    .mockReturnValueOnce(first.promise)
    .mockReturnValueOnce(second.promise);
  await render();
  expect(el.querySelector('img')?.getAttribute('src')).toBe(path);
  const signal = reads.mock.calls[0][2]!;
  for (const sourceVersion of [1, 2, 3]) await render({ sourceVersion });
  expect(reads).toHaveBeenCalledTimes(1);
  expect(signal.aborted).toBe(false);
  const reload = vi.spyOn(freeCanvasApi, 'version').mockResolvedValue({
    ...original,
    ...result('RUNNING', 1),
    document: {
      ...original.document,
      nodes: [
        { ...original.document.nodes[0], asset_id: 'a', asset_revision: 3 },
        { id: 'r', title: '已有对话', run_id: 'run', x: 200, y: 0 },
      ],
    },
  });
  await click('修订历史');
  await click('v1 · 初稿 · 1 个节点');
  expect(reload).toHaveBeenCalledTimes(1);
  expect(signal.aborted).toBe(false);
  await render({ sourceVersion: 3, assetVersion: 1 });
  expect(el.querySelector('img')).toBeNull();
  await render({ sourceVersion: 4, assetVersion: 1 });
  expect(reads).toHaveBeenCalledTimes(1);
  await act(() => first.resolve(result('COMPLETED', 5)));
  expect(el.querySelector('.free-canvas-run')?.getAttribute('data-status')).toBe('COMPLETED');
  expect(el.querySelector('img')).toBeNull();
  expect(reads).toHaveBeenCalledTimes(2);
  await act(() => second.resolve(result('COMPLETED', 5, false)));
  expect(reads).toHaveBeenCalledTimes(2);
  expect(el.querySelector('img')).toBeNull();
  expect(freeCanvasApi.current).toHaveBeenCalledTimes(1);
});

it('waits for a new permission read if an image was invalidated during the initial canvas GET', async () => {
  const path = '/api/v1/projects/p/canvas/revisions/1/nodes/n/thumbnail?project_id=p';
  const source = { available: true, content_version: 'a'.repeat(64), thumbnail_url: path };
  const originalRead = deferred<CanvasVersion>();
  const next = deferred<{ project_id: string; revision: number; sources: CanvasSources }>();
  vi.mocked(freeCanvasApi.current).mockReturnValue(originalRead.promise);
  vi.mocked(freeCanvasApi.sources).mockReturnValue(next.promise);
  await render();
  await render({ assetVersion: 1 });
  await act(() =>
    originalRead.resolve({
      ...original,
      sources: { n: source },
      document: {
        ...original.document,
        nodes: [{ ...original.document.nodes[0], asset_id: 'a', asset_revision: 3 }],
      },
    }),
  );
  expect(el.querySelector('.free-canvas-viewport')).toBeTruthy();
  expect(el.querySelector('img')).toBeNull();
  await act(() => next.resolve({ project_id: 'p', revision: 1, sources: { n: source } }));
  expect(el.querySelector('img')?.getAttribute('src')).toBe(path);
});

it.each([false, true])(
  'removes the discarded edit warning on denied access and retains an actual receipt (%s)',
  async (unknown) => {
    const warning = vi.fn();
    vi.spyOn(freeCanvasApi, 'save').mockRejectedValue(new ApiError('回执未知', 503, true));
    await render({ onDirty: warning });
    await chooseNode();
    await type('free-node-title', '我的画布编辑');
    if (unknown) await click('保存画布');
    expect(warning).toHaveBeenLastCalledWith(true);
    vi.mocked(freeCanvasApi.revisions).mockRejectedValue(
      new ApiError('不可读取', 404, false, 'CANVAS_NOT_FOUND'),
    );
    await render({ onDirty: warning, refreshVersion: 1 });
    expect(el.querySelector('.free-canvas-viewport')).toBeNull();
    expect(el.textContent).not.toContain('我的画布编辑');
    expect(warning).toHaveBeenLastCalledWith(unknown);
    expect(el.textContent?.includes('核对原保存回执')).toBe(unknown);
  },
);

it('uses the bounded in-page receipt map when reads succeed and storage writes exceed quota', async () => {
  const storageKey = 'studio-free-canvas-pending-1',
    p = JSON.stringify(['user', 'p']);
  const old = Object.fromEntries(
    Array.from({ length: 15 }, (_, i) => [JSON.stringify(['user', 'saved' + i]), 'old' + i]),
  );
  sessionStorage.setItem(storageKey, JSON.stringify(old));
  const store = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new DOMException('full', 'QuotaExceededError');
  });
  const save = vi.spyOn(freeCanvasApi, 'save').mockRejectedValue(new ApiError('回执未知', 503, true));
  const receipt = vi.spyOn(freeCanvasApi, 'receipt').mockResolvedValue({ ...original, revision: 2 });
  vi.mocked(freeCanvasApi.current).mockImplementation(async (project) => ({
    ...original,
    project_id: project,
  }));
  await render();
  await chooseNode();
  await type('free-node-title', '待核对编辑');
  await click('保存画布');
  const key = save.mock.calls[0][3];
  expect(JSON.parse(sessionStorage.getItem(storageKey)!)[p]).toBeUndefined();
  await render({ project: 'q' });
  await chooseNode();
  await type('free-node-title', '另一项目');
  await click('保存画布');
  expect(el.textContent).toContain('请先核对其他项目');
  expect(save).toHaveBeenCalledTimes(1);
  await render();
  await click('核对原保存回执');
  expect(receipt).toHaveBeenCalledWith('p', key, expect.any(AbortSignal));
  // Filling the sixteenth slot succeeds after the ACK cleared its old slot.
  store.mockRestore();
  await render({ project: 'q' });
  await chooseNode();
  await type('free-node-title', '新编辑');
  await click('保存画布');
  expect(save).toHaveBeenCalledTimes(2);
  expect(Object.keys(JSON.parse(sessionStorage.getItem(storageKey)!))).toHaveLength(16);
  expect(JSON.parse(sessionStorage.getItem(storageKey)!)[p]).toBeUndefined();
});

it('does not restore a confirmed receipt from stale disk when deleting its key exceeded the quota', async () => {
  const storageKey = 'studio-free-canvas-pending-1',
    p = JSON.stringify(['user', 'p']);
  sessionStorage.setItem(storageKey, JSON.stringify({ [p]: 'original-saved-key' }));
  vi.mocked(freeCanvasApi.current).mockImplementation(async (project) => ({
    ...original,
    project_id: project,
  }));
  const receipt = vi.spyOn(freeCanvasApi, 'receipt').mockResolvedValue({ ...original, revision: 2 });
  const save = vi.spyOn(freeCanvasApi, 'save').mockRejectedValue(new ApiError('校验失败', 422));
  const store = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
    throw new DOMException('full', 'QuotaExceededError');
  });
  await render();
  await click('核对原保存回执');
  expect(receipt).toHaveBeenCalledWith('p', 'original-saved-key', expect.any(AbortSignal));
  expect(JSON.parse(sessionStorage.getItem(storageKey)!)[p]).toBe('original-saved-key');
  await render({ project: 'q' });
  await render();
  expect(el.textContent).not.toContain('核对原保存回执');
  store.mockRestore();
  await chooseNode();
  await type('free-node-title', '新的明确修改');
  await click('保存画布');
  expect(save).toHaveBeenCalledTimes(1);
  expect(JSON.parse(sessionStorage.getItem(storageKey)!)).toEqual({});
});
