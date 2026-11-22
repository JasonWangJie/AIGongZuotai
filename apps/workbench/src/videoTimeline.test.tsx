// @vitest-environment jsdom
import { Blob as NodeBlob } from 'node:buffer';
import { createHash, webcrypto } from 'node:crypto';
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { api, ApiError } from './api';
import type { AssetEntry } from './libraryState';
import { VideoTimelineWorkbench } from './VideoTimelineWorkbench';
import {
  changes,
  exportVideo,
  readOperation,
  slotFor,
  sourceURL,
  timelineApi,
  timelineProblem,
  usableVideo,
  versionMatches,
  writableTimeline,
} from './videoTimeline';
import type { Clip, TimelineVersion } from './videoTimeline';
const noop = () => {},
  props = { project: 'p', owner: 'user', enabled: true, visible: true, onDirty: noop, onExpired: noop };
const clip: Clip = {
  id: 'clip',
  title: '第一镜',
  asset_id: 'asset',
  asset_revision: 3,
  in_ms: 100,
  out_ms: 1300,
  asset_available: true,
  content_version: 'a'.repeat(64),
};
const ver: TimelineVersion = {
  id: 'vtl_a',
  project_id: 'p',
  version: 1,
  created_at: 'now',
  document: { title: '剪辑稿', clips: [clip] },
};
let root: ReturnType<typeof createRoot>, container: HTMLDivElement;
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  sessionStorage.clear();
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
  vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(noop);
});
afterEach(async () => {
  await act(() => root.unmount());
  container.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});
function setup(enabled = true) {
  const row = { id: ver.id, project_id: 'p', version: 1, title: '剪辑稿', clip_count: 1, created_at: 'now' };
  vi.spyOn(timelineApi, 'caps').mockResolvedValue({
    editable: true,
    export_enabled: enabled,
    max_clips: 8,
    max_duration_ms: 30000,
  });
  vi.spyOn(timelineApi, 'list').mockResolvedValue({ items: [row] });
  vi.spyOn(timelineApi, 'versions').mockResolvedValue({ items: [row] });
  vi.spyOn(timelineApi, 'version').mockResolvedValue(ver);
}
const render = () => act(() => root.render(<VideoTimelineWorkbench {...props} />));
function button(label: string) {
  const b = [...container.querySelectorAll('button')].find((b) => b.textContent === label);
  expect(b, label).toBeDefined();
  return b!;
}
async function click(label: string) {
  const b = button(label);
  expect(b.disabled, label).toBe(false);
  await act(() => b.click());
}
async function field(label: string, value: string) {
  const el = container.querySelector('[aria-label="' + label + '"]') as HTMLSelectElement | HTMLInputElement;
  expect(el, label).toBeTruthy();
  await act(() => {
    Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), 'value')!.set!.call(el, value);
    el.dispatchEvent(new Event(el instanceof HTMLSelectElement ? 'change' : 'input', { bubbles: true }));
  });
}
it('validates cumulative 24fps frame boundaries, sum, unique IDs and the 5 minute source bound', () => {
  const make = (cuts: number[]) => ({
    title: '试剪',
    clips: cuts.map((out_ms, i) => ({ ...clip, id: 'c' + i, in_ms: 0, out_ms })),
  });
  expect(timelineProblem(make([20]))).toContain('一帧');
  expect(timelineProblem(make([30, 30]))).toContain('第 2 镜');
  expect(timelineProblem(make([21, 42]))).toBe('');
  expect(timelineProblem(make([30000, 42]))).toContain('30000');
  expect(timelineProblem(make([300001]))).toContain('300000');
  expect(timelineProblem({ title: '剪辑稿', clips: [clip, clip] })).toContain('编号');
  expect(timelineProblem(make(Array(9).fill(300)))).toContain('1–8');
  expect(timelineProblem({ title: '剪辑稿', clips: [{ ...clip, in_ms: 299000, out_ms: 300000 }] })).toBe('');
});
it('strips read-only properties and binds the fixed original to the project, clip and version', () => {
  const value = writableTimeline(ver.document);
  expect(value.clips[0]).not.toHaveProperty('content_version');
  expect(value.clips[0]).not.toHaveProperty('asset_available');
  expect(value.clips[0].asset_revision).toBe(3);
  expect(versionMatches(ver, 'another')).toBe(false);
  expect(versionMatches(ver, 'p', 'vtl_a', 2)).toBe(false);
  expect(sourceURL(ver, clip)).toBe('/api/v1/projects/p/video-timelines/vtl_a/versions/1/clips/clip/source');
  expect(changes(ver.document, { ...value, clips: [{ ...value.clips[0], in_ms: 400 }] })).toEqual([
    '「第一镜」裁点变化',
  ]);
});
it('selects only available project MP4s; gallery URLs never control the actual player location', () => {
  const asset = {
    id: 'a',
    project_id: 'p',
    source: 'generated',
    state: 'active',
    kind: 'video',
    content_type: 'video/mp4',
    content_url: '/api/v1/library/a/content',
    revision: 1,
  } as AssetEntry;
  expect(usableVideo(asset, 'p')).toBe(true);
  for (const data of [
    { project_id: 'q' },
    { source: 'reference' },
    { content_type: 'video/webm' },
    { state: 'trashed' },
    { content_url: null },
    { moderation: { state: 'BLOCKED' } },
  ])
    expect(usableVideo({ ...asset, ...data } as AssetEntry, 'p')).toBe(false);
});
it('shows the approved original and editing without a configured export worker', async () => {
  setup(false);
  await render();
  await field('项目剪辑稿', ver.id);
  expect(container.querySelector('video')?.getAttribute('src')).toBe(sourceURL(ver, clip));
  expect(button('导出 MP4').disabled).toBe(true);
  expect(container.textContent).toContain('合成工作线程未启用');
  await field('镜头名称', '改名');
  expect(button('保存剪辑版本').disabled).toBe(false);
});
it('keeps stale input across a GET refresh and offers a new document with expected_version=0', async () => {
  setup();
  const save = vi
    .spyOn(timelineApi, 'save')
    .mockRejectedValueOnce(new ApiError('冲突', 409, false, 'VIDEO_TIMELINE_VERSION_CONFLICT'))
    .mockResolvedValueOnce({ ...ver, id: 'new', document: { ...ver.document, title: '新的名称' } });
  await render();
  await field('项目剪辑稿', ver.id);
  await field('剪辑稿名称', '新的名称');
  await click('刷新剪辑目录');
  await click('保存剪辑版本');
  expect((container.querySelector('[aria-label="剪辑稿名称"]') as HTMLInputElement).value).toBe('新的名称');
  expect(container.textContent).toContain('本页输入仍保留');
  await click('将剪辑另存新稿');
  expect(save.mock.calls.map((c) => c.slice(1, 3))).toEqual([
    [ver.id, 1],
    [undefined, 0],
  ]);
});
it('reads an original save receipt after an uncertain POST without resending the operation or storing content', async () => {
  setup();
  const save = vi.spyOn(timelineApi, 'save').mockRejectedValue(new ApiError('回执不明', 0, true));
  const receipt = vi.spyOn(timelineApi, 'receipt').mockResolvedValue({ ...ver, version: 2 });
  await render();
  await field('项目剪辑稿', ver.id);
  await field('镜头名称', '私有指令');
  await click('保存剪辑版本');
  const op = readOperation(slotFor('user', 'p'));
  expect(op?.version).toBe(1);
  expect(sessionStorage.getItem(slotFor('user', 'p'))).not.toContain('私有指令');
  await click('只读核对原保存回执');
  expect(save).toHaveBeenCalledTimes(1);
  expect(receipt).toHaveBeenCalledWith('p', op?.key);
});
it('cancels the original export, then only GETs the fixed version without auto-computing', async () => {
  setup();
  const f = vi
    .fn()
    .mockImplementation(
      (_url, init) =>
        new Promise((_resolve, reject) =>
          init.signal.addEventListener('abort', () => reject(Error('aborted'))),
        ),
    );
  vi.stubGlobal('fetch', f);
  await render();
  await field('项目剪辑稿', ver.id);
  await click('导出 MP4');
  expect(f).toHaveBeenCalledTimes(1);
  const [url, init] = f.mock.calls[0];
  expect(url).toMatch(/versions\/1\/export$/);
  expect(init.method).toBe('POST');
  expect(init.body).toBeUndefined();
  await click('取消本次导出');
  expect(init.signal.aborted).toBe(true);
  expect(button('导出 MP4').disabled).toBe(true);
  await click('只读核对原导出版本');
  expect(timelineApi.version).toHaveBeenCalledTimes(2);
  expect(f).toHaveBeenCalledTimes(1);
  expect(button('导出 MP4').disabled).toBe(false);
});
it('restores pending fixed-version scope after a refresh, without disclosing or reading another project', async () => {
  setup();
  sessionStorage.setItem(
    slotFor('user', 'other'),
    JSON.stringify({ kind: 'export', id: 'other', version: 2 }),
  );
  sessionStorage.setItem(slotFor('user', 'p'), JSON.stringify({ kind: 'export', id: ver.id, version: 1 }));
  const f = vi.fn();
  vi.stubGlobal('fetch', f);
  await render();
  expect(timelineApi.version).not.toHaveBeenCalled();
  await click('只读核对原导出版本');
  expect(timelineApi.version).toHaveBeenCalledWith('p', ver.id, 1);
  expect(readOperation(slotFor('user', 'other'))?.id).toBe('other');
  expect(f).not.toHaveBeenCalled();
});
it.each([
  ['Content-Length', '67108865'],
  ['X-Content-SHA256', 'bad'],
  ['X-Video-Duration-Ms', '2000'],
  ['Content-Type', 'text/html'],
  ['Content-Disposition', 'inline'],
])('rejects mismatched %s before accepting an output', async (k, val) => {
  const headers = {
    'Content-Type': 'video/mp4',
    'Content-Disposition': 'attachment',
    'X-Content-SHA256': 'a'.repeat(64),
    'X-Video-Duration-Ms': '1200',
    [k]: val,
  };
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(new Uint8Array(20), { headers })));
  await expect(exportVideo(ver, new AbortController().signal)).rejects.toThrow('回执');
});

it.each([true, false])('verifies all bytes before returning an MP4, good digest=%s', async (good) => {
  const bytes = Uint8Array.from([0, 0, 0, 16, 102, 116, 121, 112, 105, 115, 111, 109, 0, 0, 0, 1]);
  const hash = createHash('sha256').update(bytes).digest('hex');
  vi.stubGlobal('Blob', NodeBlob);
  vi.stubGlobal('crypto', webcrypto);
  const fetcher = vi.fn().mockResolvedValue(
    new Response(bytes, {
      headers: {
        'Content-Type': 'video/mp4',
        'Content-Disposition': 'attachment',
        'X-Video-Duration-Ms': '1200',
        'X-Content-SHA256': good ? hash : 'b'.repeat(64),
        'Content-Length': String(bytes.length),
      },
    }),
  );
  vi.stubGlobal('fetch', fetcher);
  const output = exportVideo(ver, new AbortController().signal);
  if (good) {
    const result = await output;
    expect(result.sha).toBe(hash);
    expect(new Uint8Array(await result.blob.arrayBuffer())).toEqual(bytes);
  } else await expect(output).rejects.toThrow('SHA-256');
  expect(fetcher).toHaveBeenCalledTimes(1);
});

function timelineSelect(label: string) {
  return container.querySelector<HTMLSelectElement>('[aria-label="' + label + '"]')!;
}

const summary = (id: string, version: number) => ({
  id,
  version,
  project_id: props.project,
  title: '稿件 ' + version,
  clip_count: 1,
  created_at: ver.created_at,
});

it('retains the acknowledged new timeline while the directory page is delayed, missing or later includes it', async () => {
  setup();
  let release!: (page: { items: ReturnType<typeof summary>[]; next_cursor?: string }) => void;
  const waiting = new Promise<{ items: ReturnType<typeof summary>[]; next_cursor?: string }>((done) => {
    release = done;
  });
  vi.mocked(timelineApi.list).mockResolvedValueOnce({ items: [] }).mockReturnValue(waiting);
  const save = vi.spyOn(timelineApi, 'save').mockRejectedValue(new ApiError('回执不明', 0, true));
  const receipt = vi.spyOn(timelineApi, 'receipt').mockResolvedValue(ver);
  vi.spyOn(api, 'library').mockResolvedValue({
    items: [
      {
        id: 'asset',
        project_id: 'p',
        source: 'generated',
        state: 'active',
        kind: 'video',
        content_type: 'video/mp4',
        content_url: '/private',
        name: '来源素材',
        revision: 3,
      } as AssetEntry,
    ],
    next_cursor: null,
    has_more: false,
    limit: 24,
  });
  await render();
  await field('剪辑稿名称', ver.document.title);
  await click('添加项目视频');
  await act(() => container.querySelector<HTMLButtonElement>('[data-video-asset]')!.click());
  await click('保存剪辑版本');
  const key = readOperation(slotFor(props.owner, props.project))?.key;
  await click('只读核对原保存回执');
  expect(receipt).toHaveBeenCalledWith(props.project, key);
  expect(save).toHaveBeenCalledTimes(1);
  expect(timelineSelect('项目剪辑稿').value).toBe(ver.id);
  expect(timelineSelect('剪辑固定版本').value).toBe('1');
  expect(timelineSelect('项目剪辑稿').disabled).toBe(false);
  await field('镜头名称', '收到回执后继续剪辑');
  await act(() => release({ items: [summary('another', 1)], next_cursor: 'next' }));
  expect(timelineSelect('项目剪辑稿').value).toBe(ver.id);
  vi.mocked(timelineApi.list).mockResolvedValue({ items: [summary(ver.id, 2)] });
  await click('更多剪辑稿');
  expect(timelineSelect('项目剪辑稿').value).toBe(ver.id);
  expect([...timelineSelect('项目剪辑稿').options].filter((opt) => opt.value === ver.id)).toHaveLength(1);
  expect(timelineSelect('剪辑固定版本').value).toBe('1');
  expect(container.querySelector<HTMLInputElement>('[aria-label="镜头名称"]')!.value).toBe(
    '收到回执后继续剪辑',
  );
});

it('retains an opened earlier version when both refreshed directories omit the selected entries', async () => {
  setup();
  const first = summary('another', 1);
  vi.mocked(timelineApi.list).mockImplementation(async (_, cursor) =>
    cursor ? { items: [summary(ver.id, 3)] } : { items: [first], next_cursor: 'page2' },
  );
  vi.mocked(timelineApi.versions).mockImplementation(async (_, id, before) =>
    before ? { items: [summary(id, 1)] } : { items: [summary(id, 3)], next_before_version: 3 },
  );
  vi.mocked(timelineApi.version).mockImplementation(async (_, id, n) => ({ ...ver, id, version: n }));
  await render();
  await click('更多剪辑稿');
  await field('项目剪辑稿', ver.id);
  await click('更早版本');
  await field('剪辑固定版本', '1');
  expect(timelineSelect('项目剪辑稿').value).toBe(ver.id);
  expect(timelineSelect('剪辑固定版本').value).toBe('1');
  expect([...timelineSelect('剪辑固定版本').options].map((o) => o.value)).toEqual(['3', '1']);
  await field('剪辑稿名称', '本页保留的历史草稿');
  await click('刷新剪辑目录');
  await field('项目剪辑稿', '');
  await click('保留本页剪辑');
  expect(timelineSelect('项目剪辑稿').value).toBe(ver.id);
  expect(container.querySelector<HTMLInputElement>('[aria-label="剪辑稿名称"]')!.value).toBe(
    '本页保留的历史草稿',
  );
  expect(timelineApi.version).toHaveBeenCalledTimes(2);
});

it('does not create a current selection from an out-of-project original receipt', async () => {
  setup();
  vi.mocked(timelineApi.list).mockResolvedValue({ items: [] });
  vi.spyOn(timelineApi, 'receipt').mockResolvedValue({ ...ver, project_id: 'elsewhere' });
  sessionStorage.setItem(
    slotFor(props.owner, props.project),
    JSON.stringify({ kind: 'save', version: 0, key: 'original-save-key' }),
  );
  await render();
  await click('只读核对原保存回执');
  expect(timelineSelect('项目剪辑稿').value).toBe('');
  expect([...timelineSelect('项目剪辑稿').options]).toHaveLength(1);
  expect(readOperation(slotFor(props.owner, props.project))?.key).toBe('original-save-key');
});
