// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import App from './App';
import { buildModelTaskInput, resolveSelection, validCatalog } from './modelCatalog';
import type { AssetEntry } from './libraryState';
import type { Bootstrap, Model, TaskInput } from './types';
import { eligibleSource } from './sourceVideo';

vi.mock('./useGenerationAccess', () => ({ useGenerationAccess: () => ({ allowed: true, refresh: vi.fn() }) }));
const preset = (id: string, seconds: number) => ({ id, name: id, aspect_ratio: '16:9', size: '1280x720', resolution: '720p', seconds });
const model: Model = { id: 'video', name: 'Fixture video', version: 'v1', description: 'Fixture', enabled: true, kind: 'video',
  capabilities: { cancel_supported: false, max_count: 1, max_prompt_bytes: 4096, parameter_presets: [preset('edit4', 4), preset('extend6', 6)],
    source_video: { data_uri_edit_presets: ['edit4'], data_uri_extend_presets: ['extend6'], extension_directions: ['forward', 'backward'] } } };
const project = { id: 'p', name: 'Project' }, user = { id: 'u', name: 'User', email: 'fixture@example.invalid' };
const boot: Bootstrap = { project, workspace: { id: 'w', name: 'Workspace' }, projects: [project], mode: 'live', auth_enabled: true,
  user, storage: 'postgres', models: [model], billing: { enabled: true, mode: 'credits', scale: 6, unit: 'CREDIT' } };
const asset: AssetEntry = { id: 'lib_' + 'a'.repeat(64), project_id: 'p', source: 'generated', kind: 'video', task_id: 'oldTask', artifact_id: 'oldFile',
  content_type: 'video/mp4', revision: 13, state: 'active', name: 'Original', content_url: '/private', tags: [], created_at: '', updated_at: '', deleted_at: null, thumbnail_url: null };
let holder: HTMLDivElement, root: ReturnType<typeof createRoot>;
let projectEvents: EventTarget;
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  vi.stubGlobal('EventSource', class extends EventTarget {
    constructor(url: string) { super(); if (url.includes('/workspaces/')) projectEvents = this; }
    close() {}
  });
  Object.defineProperty(HTMLElement.prototype, 'scrollTo', { configurable: true, value: vi.fn() });
  Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value: vi.fn() });
  holder = document.createElement('div'); document.body.append(holder); root = createRoot(holder);
});
afterEach(async () => { await act(() => root.unmount()); holder.remove(); localStorage.clear(); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });
function button(label: string) {
  const result = [...holder.querySelectorAll('button')].find(b => b.textContent?.includes(label));
  if (!result) throw Error('Missing button: ' + label);
  return result;
}
async function choose(select: HTMLSelectElement, value: string) {
  await act(() => { select.value = value; select.dispatchEvent(new Event('change', { bubbles: true })); });
}
it('limits each operation to its declared model presets and rejects edit direction or mismatched assets', () => {
  expect(resolveSelection(boot, 'video', undefined, 'video_edit').presets.map(p => p.id)).toEqual(['edit4']);
  expect(resolveSelection(boot, 'video', undefined, 'video_extend').presets.map(p => p.id)).toEqual(['extend6']);
  expect(validCatalog({ version: 'v1', models: [model] })).toBe(true);
  const bad = structuredClone(model); bad.capabilities!.source_video!.data_uri_edit_presets = ['unregistered'];
  expect(validCatalog({ version: 'v1', models: [bad] })).toBe(false);
  bad.capabilities!.source_video = { data_uri_extend_presets: ['extend6'], extension_directions: ['bad.direction'] };
  expect(validCatalog({ version: 'v1', models: [bad] })).toBe(false);
  const disabled = { ...model, enabled: false }, plain = { ...model, capabilities: { ...model.capabilities!, source_video: undefined } };
  for (const fallback of [plain, disabled, bad]) expect(resolveSelection({ ...boot, models: [fallback] }, 'video', undefined, 'video_edit').model).toBeUndefined();
  const ref = { asset_id: asset.id, asset_revision: 13 };
  const build = (op: 'video_edit' | 'video_extend', id: number, direction?: string) =>
    buildModelTaskInput('p', model, 'light', model.capabilities!.parameter_presets[id], 1, op, [], { ref, direction });
  expect(build('video_edit', 0).inputs).toEqual({ prompt: 'light', source_video: ref });
  expect(() => build('video_edit', 0, 'forward')).toThrow();
  expect(() => build('video_extend', 0)).toThrow();
  expect(() => build('video_extend', 1, 'sideways')).toThrow();
  for (const difference of [{ project_id: 'other' }, { state: 'trashed' }, { content_type: 'image/png' },
    { kind: 'poster' }, { artifact_id: '' }, { task_id: '' }, { revision: 0 }, { content_url: null }, { moderation: { state: 'REVIEW' } }]) {
    expect(eligibleSource({ ...asset, ...difference } as AssetEntry, 'p')).toBe(false);
  }
});
it.each(['video_edit', 'video_extend'] as const)('quotes the exact %s request from the video studio and only GETs after an unknown POST', async operation => {
  vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'clearTimeout'] });
  const quotes: TaskInput[] = [], writes: { key: string; body: TaskInput }[] = [], receipts: { url: string; key: string; project: string }[] = [];
  let revision = 13;
  let finishDetail: ((response: Response) => void) | null = null;
  let changes = 0;
  async function libraryChanged() {
    await act(async () => {
      const seq = ++changes;
      projectEvents.dispatchEvent(new MessageEvent('resource.changed', { data: JSON.stringify({ event_id: 'notice-' + seq, seq,
        kind: 'audit.created', resource_type: 'asset', resource_id: 'another-asset', resource_version: seq, action: 'asset.updated' }) }));
    });
    // Allow the project feed to deliver its throttled library refresh.
    await act(async () => { await vi.advanceTimersByTimeAsync(1500); });
    expect(finishDetail).toBeTypeOf('function');
  }
  async function reply(data: unknown, status = 200) {
    const callback = finishDetail!;
    finishDetail = null;
    await act(() => { callback(Response.json(data, { status })); });
  }
  vi.stubGlobal('fetch', async (url: string, init: RequestInit) => {
    const path = new URL(url, 'http://localhost').pathname, method = init.method || 'GET', headers = new Headers(init.headers);
    if (path === '/api/v1/auth/session') return Response.json({ auth_enabled: true, user, project, projects: [project], workspace: boot.workspace });
    if (path === '/api/v1/bootstrap') return Response.json(boot);
    if (path === '/api/v1/models') return Response.json({ version: 'v1', models: [model] });
    if (path === '/api/v1/library/' + asset.id) return new Promise<Response>(done => { finishDetail = done; });
    if (path === '/api/v1/library') {
      const more = new URL(url, 'http://localhost').searchParams.has('cursor');
      return Response.json({ items: more ? [{ ...asset, revision }] : [{ ...asset, state: 'trashed' }], next_cursor: more ? null : 'next' });
    }
    if (path === '/api/v1/billing/quotes') {
      quotes.push(JSON.parse(String(init.body)).task_input);
      return Response.json({ id: 'fixed-quote', workspace_id: 'w', project_id: 'p', resource_type: 'task', model_id: 'video', model_version: 'v1',
        price_version: 'p1', provider_mode: 'live', unit: 'CREDIT', scale: 6, reserve_micros: '1', artifact_micros: '1', requested_units: 1,
        basis: 'delivered_artifact', expires_at: new Date(Date.now() + 60000).toISOString() });
    }
    if (path === '/api/v1/tasks' && method === 'POST') {
      writes.push({ key: headers.get('Idempotency-Key')!, body: JSON.parse(String(init.body)) });
      throw new TypeError('connection lost');
    }
    if (path === '/api/v1/tasks/receipt') {
      expect(method).toBe('GET'); receipts.push({ url, project: headers.get('X-Project-ID')!, key: headers.get('Idempotency-Key')! });
      if (receipts.length === 1) return Response.json({ error: { code: 'TASK_RECEIPT_NOT_FOUND' } }, { status: 404 });
      const b = writes[0].body;
      return Response.json({ id: 't', workspace_id: 'w', project_id: 'p', kind: 'video', model_id: b.model_id, model_version: b.model_version,
        operation: b.operation, source_video: { ...b.inputs.source_video, asset_revision: receipts.length === 2 ? 777 : revision },
        extension_direction: b.inputs.extension_direction, parameters: b.parameters, version: 1, progress: 0, status: 'QUEUED', artifacts: [],
        prompt: receipts.length === 3 ? 'different prompt' : b.inputs.prompt,
        billing: { quote_id: receipts.length === 4 ? 'other-quote' : b.quote_id, reservation_id: 'held', price_version: 'p1',
          state: 'HELD', reserved_micros: '1', charged_micros: '0', refunded_micros: '0', released_micros: '0', unit: 'CREDIT', scale: 6, version: 1 },
        created_at: '2026-11-23T00:00:00Z', updated_at: '2026-11-23T00:00:00Z' });
    }
    if (path === '/api/v1/tasks' || path === '/api/v1/runs') return Response.json({ items: [] });
    return Response.json({ error: { code: 'UNAVAILABLE' } }, { status: 404 });
  });
  await act(async () => root.render(<App/>));
  await act(() => button('视频工作室').click());
  await act(() => button(operation === 'video_edit' ? '编辑原视频' : '延长原视频').click());
  const source = () => holder.querySelector<HTMLSelectElement>('.source-video-fields select')!;
  await act(() => button('读取更多素材').click());
  expect([...source().options].map(o => o.text)).toEqual(['选择项目内的 MP4', 'Original · 修订 13']);
  await choose(source(), source().options[1].value);
  const directions = holder.querySelectorAll<HTMLSelectElement>('.source-video-fields select');
  if (operation === 'video_extend') await choose(directions[1], 'backward');
  else expect(directions).toHaveLength(1);
  const area = holder.querySelector<HTMLTextAreaElement>('#creation-prompt')!;
  await act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')!.set!.call(area, 'extend this scene');
    area.dispatchEvent(new Event('input', { bubbles: true }));
  });
  const quote = () => button('查看本次报价');
  expect(quote().disabled).toBe(false);
  await act(() => quote().click());
  expect(quotes).toHaveLength(1); expect(writes).toHaveLength(0);
  expect(holder.querySelector('.quote-confirmation')?.textContent).toContain(asset.id + ' · 修订 13');
  await libraryChanged();
  const confirmation = () => holder.querySelector<HTMLButtonElement>('.quote-confirmation .button.primary')!;
  expect(confirmation().disabled).toBe(true);
  await act(() => confirmation().click());
  expect(writes).toHaveLength(0);
  await reply({ error: { code: 'UNAVAILABLE' } }, 503);
  expect(confirmation().disabled).toBe(true);
  expect(source().value).toBe(JSON.stringify([asset.id, revision]));
  await act(() => button('重新核对').click());
  await reply({ ...asset, revision });
  expect(confirmation().disabled).toBe(false);
  expect(quotes).toHaveLength(1);
  if (operation === 'video_extend') expect(holder.querySelectorAll<HTMLSelectElement>('.source-video-fields select')[1].value).toBe('backward');
  revision++;
  await libraryChanged();
  await reply({ ...asset, revision });
  expect(holder.querySelector('.quote-confirmation')).toBeNull(); expect(quote().disabled).toBe(true);
  await choose(source(), source().options[1].value);
  if (finishDetail) await reply({ ...asset, revision });
  await act(() => quote().click());
  expect(quotes[1].operation).toBe(operation);
  expect(quotes[1].parameters).toEqual({ aspect_ratio: '16:9', count: 1, size: '1280x720', resolution: '720p', seconds: operation === 'video_edit' ? 4 : 6 });
  expect(quotes[1].inputs).toEqual({ prompt: 'extend this scene', source_video: { asset_id: asset.id, asset_revision: 14 },
    ...(operation === 'video_extend' ? { extension_direction: 'backward' } : {}) });
  await act(() => holder.querySelector<HTMLButtonElement>('.quote-confirmation .button.primary')!.click());
  expect(writes.map(w => w.body)).toEqual([{ ...quotes[1], quote_id: 'fixed-quote' }]);
  expect(writes[0].key).toBeTruthy(); expect(quote().disabled).toBe(true); expect(receipts).toHaveLength(0);
  await act(() => button('查询原任务收据').click());
  expect(holder.textContent).toContain('尚未查到原任务收据'); expect(quote().disabled).toBe(true);
  for (const field of ['revision', 'prompt', 'quote']) {
    await act(() => button('查询原任务收据').click());
    expect(holder.textContent, field).toContain('原视频任务回执与请求不符');
    expect(quote().disabled, field).toBe(true);
  }
  await act(() => button('查询原任务收据').click());
  expect(holder.textContent).not.toContain('查询原任务收据');
  expect(writes).toHaveLength(1); expect(quotes).toHaveLength(2);
  expect(receipts).toEqual(Array(5).fill({ url: '/api/v1/tasks/receipt', project: 'p', key: writes[0].key }));
  await libraryChanged();
  if (operation === 'video_edit') await reply({ error: { code: 'ASSET_NOT_FOUND' } }, 404);
  else await reply({ ...asset, revision, state: 'trashed' });
  expect(source().value).toBe('');
  expect(quote().disabled).toBe(true);
  expect(writes).toHaveLength(1);
});
