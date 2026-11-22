import { uiText } from './locale';
import { ApiError, request } from './api';
import type { AssetEntry } from './libraryState';
import { mediaContentAllowed } from './mediaModeration';
export type Clip = {
  id: string;
  title: string;
  asset_id: string;
  asset_revision: number;
  in_ms: number;
  out_ms: number;
  content_version?: string;
  asset_available?: boolean;
};
export type Timeline = { title: string; clips: Clip[] };
export type TimelineVersion = {
  id: string;
  project_id: string;
  version: number;
  created_at: string;
  document: Timeline;
};
export type TimelineSummary = {
  id: string;
  project_id: string;
  version: number;
  title: string;
  clip_count: number;
  created_at: string;
};
export type ClipCapability = {
  editable: boolean;
  export_enabled: boolean;
  max_clips: number;
  max_duration_ms: number;
};
export type Operation = { kind: 'save' | 'export'; id?: string; version: number; key?: string };
const encoded = encodeURIComponent,
  headers = (p: string, k?: string) => ({ 'X-Project-ID': p, ...(k ? { 'Idempotency-Key': k } : {}) });
const base = (p: string) => '/projects/' + encoded(p) + '/video-timelines';
const fixed = (p: string, id: string, n: number) => base(p) + '/' + encoded(id) + '/versions/' + n;
export const timelineApi = {
  caps: (p: string) => request<ClipCapability>(base(p) + '/capabilities', { headers: headers(p) }),
  list: (p: string, cursor?: string) =>
    request<{ items: TimelineSummary[]; next_cursor?: string }>(
      base(p) + '?limit=24' + (cursor ? '&cursor=' + encoded(cursor) : ''),
      { headers: headers(p) },
    ),
  versions: (p: string, id: string, before?: number) =>
    request<{ items: TimelineSummary[]; next_before_version?: number }>(
      base(p) + '/' + encoded(id) + '/versions?limit=20' + (before ? '&before_version=' + before : ''),
      { headers: headers(p) },
    ),
  version: (p: string, id: string, n: number) =>
    request<TimelineVersion>(fixed(p, id, n), { headers: headers(p) }),
  save: (p: string, id: string | undefined, version: number, doc: Timeline, k: string) =>
    request<TimelineVersion>(base(p), {
      method: 'POST',
      headers: headers(p, k),
      body: JSON.stringify({ timeline_id: id, expected_version: version, document: writableTimeline(doc) }),
    }),
  receipt: (p: string, k: string) =>
    request<TimelineVersion>(base(p) + '/receipt', { headers: headers(p, k) }),
};
export const emptyTimeline = (): Timeline => ({ title: '', clips: [] });
export const lengthMs = (doc: Timeline) => doc.clips.reduce((n, c) => n + c.out_ms - c.in_ms, 0);
export const sourceURL = (v: TimelineVersion, c: Clip) =>
  '/api/v1' + fixed(v.project_id, v.id, v.version) + '/clips/' + encoded(c.id) + '/source';
export const libraryURL = (p: string, a: string) =>
  '/api/v1/library/' + encoded(a) + '/content?project_id=' + encoded(p);
export function usableVideo(a: AssetEntry, p: string) {
  return (
    !!a &&
    a.project_id === p &&
    a.source === 'generated' &&
    a.state === 'active' &&
    a.kind === 'video' &&
    a.content_type === 'video/mp4' &&
    typeof a.content_url === 'string' &&
    !!a.content_url &&
    mediaContentAllowed(a.moderation) &&
    Number.isSafeInteger(a.revision) &&
    a.revision > 0
  );
}
export function writableTimeline(d: Timeline): Timeline {
  return {
    title: d.title,
    clips: d.clips.map(({ id, title, asset_id, asset_revision, in_ms, out_ms }) => ({
      id,
      title,
      asset_id,
      asset_revision,
      in_ms,
      out_ms,
    })),
  };
}
export function timelineProblem(d: Timeline) {
  const validTitle = (v: unknown) =>
    typeof v === 'string' &&
    !!v.trim() &&
    Array.from(v).length <= 120 &&
    !/[\p{Cc}\p{Cf}\p{Cs}\uFFFE\uFFFF]/u.test(v);
  if (!d || !validTitle(d.title)) return uiText('剪辑稿名称需为 1–120 个字符。');
  if (!Array.isArray(d.clips) || !d.clips.length || d.clips.length > 8) return uiText('每份剪辑稿需要 1–8 个镜头。');
  const ids = new Set<string>();
  let cumulativeMs = 0;
  for (const [idx, c] of d.clips.entries()) {
    const name = uiText('第 ') + (idx + 1) + uiText(' 镜');
    if (
      !c ||
      typeof c.id !== 'string' ||
      !/^[-\w]{1,48}$/.test(c.id) ||
      ids.has(c.id) ||
      !validTitle(c.title)
    )
      return name + uiText('的镜头编号或名称无效。');
    ids.add(c.id);
    if (!c.asset_id || !Number.isSafeInteger(c.asset_revision) || c.asset_revision < 1)
      return name + uiText('需要项目内可用的视频。');
    if (
      !Number.isSafeInteger(c.in_ms) ||
      !Number.isSafeInteger(c.out_ms) ||
      c.in_ms < 0 ||
      c.out_ms <= c.in_ms ||
      c.out_ms > 300000
    )
      return name + uiText('入点和出点需为整数毫秒，满足 0 ≤ 入点 < 出点 ≤ 300000。');
    const previousFrames = Math.round((cumulativeMs * 24) / 1000);
    cumulativeMs += c.out_ms - c.in_ms;
    if (Math.round((cumulativeMs * 24) / 1000) <= previousFrames)
      return name + uiText('按 24 帧/秒换算后不足一帧，请扩大裁点间隔。');
  }
  return lengthMs(d) > 30000 ? uiText('成片总时长最多 30000 毫秒。') : '';
}
export const versionMatches = (v: TimelineVersion, p: string, id?: string, n?: number) =>
  !!v &&
  v.project_id === p &&
  typeof v.id === 'string' &&
  !!v.id &&
  (!id || id === v.id) &&
  Number.isSafeInteger(v.version) &&
  v.version >= 1 &&
  (!n || n === v.version) &&
  !timelineProblem(v.document);
export const summaryMatches = (v: TimelineSummary, p: string) =>
  !!v &&
  v.project_id === p &&
  typeof v.id === 'string' &&
  !!v.id &&
  Number.isSafeInteger(v.version) &&
  v.version > 0 &&
  typeof v.title === 'string' &&
  Number.isSafeInteger(v.clip_count) &&
  v.clip_count >= 1 &&
  v.clip_count <= 8;
export function changes(old: Timeline, next: Timeline) {
  const names: string[] = [];
  if (old.title !== next.title) names.push(uiText('剪辑稿名称'));
  for (const [idx, c] of next.clips.entries()) {
    const i = old.clips.findIndex((s) => s.id === c.id),
      prior = old.clips[i];
    if (!prior) {
      names.push(uiText('新增「') + c.title + '」');
      continue;
    }
    const diff = [];
    if (i !== idx) diff.push(uiText('顺序'));
    if (c.title !== prior.title) diff.push(uiText('名称'));
    if (c.in_ms !== prior.in_ms || c.out_ms !== prior.out_ms) diff.push(uiText('裁点'));
    if (c.asset_id !== prior.asset_id || c.asset_revision !== prior.asset_revision) diff.push(uiText('来源'));
    if (diff.length) names.push('「' + c.title + '」' + diff.join('、') + uiText('变化'));
  }
  for (const c of old.clips)
    if (!next.clips.some((s) => s.id === c.id)) names.push(uiText('移除「') + c.title + '」');
  return names;
}
export const slotFor = (owner: string, project: string) =>
  'xingtu-video-timeline:' + JSON.stringify([owner, project]);
export function readOperation(slot: string): Operation | null {
  const raw = sessionStorage.getItem(slot);
  if (!raw) return null;
  const i: Operation = JSON.parse(raw);
  if (
    !i ||
    !['save', 'export'].includes(i.kind) ||
    !Number.isSafeInteger(i.version) ||
    i.version < 0 ||
    (i.version > 0 && !i.id) ||
    (i.kind === 'export' && i.version < 1) ||
    (i.kind === 'save' && !/^[-\w]{12,100}$/.test(i.key || ''))
  )
    throw Error(uiText('原操作编号无法核对。'));
  return i;
}
const maxOutput = 64 * 1024 * 1024;
export async function exportVideo(v: TimelineVersion, signal: AbortSignal) {
  const res = await fetch('/api/v1' + fixed(v.project_id, v.id, v.version) + '/export', {
    method: 'POST',
    credentials: 'same-origin',
    cache: 'no-store',
    redirect: 'error',
    headers: headers(v.project_id),
    signal,
  });
  if (!res.ok) {
    const code = res.status;
    await res.body?.cancel();
    throw new ApiError(
      code === 403 || code === 404
        ? uiText('当前身份无法读取原稿或素材。')
        : code === 409
          ? uiText('原素材或裁点不可用于导出，请核对固定版本。')
          : code === 429
            ? uiText('合成工作线程繁忙，请稍后手动核对。')
            : uiText('成片计算未完成，请核对原版本。'),
      code,
      code >= 500,
    );
  }
  const sha = res.headers.get('X-Content-SHA256') || '',
    duration = Number(res.headers.get('X-Video-Duration-Ms')),
    declared = Number(res.headers.get('Content-Length') || 0);
  if (
    !res.body ||
    res.headers.get('Content-Type')?.split(';')[0] !== 'video/mp4' ||
    !/^attachment(?:;|$)/i.test(res.headers.get('Content-Disposition') || '') ||
    !/^[a-f0-9]{64}$/.test(sha) ||
    !Number.isSafeInteger(duration) ||
    duration <= 0 ||
    Math.abs(duration - lengthMs(v.document)) > Math.ceil((v.document.clips.length * 1000) / 24 + 16) ||
    !Number.isSafeInteger(declared) ||
    declared < 0 ||
    declared > maxOutput
  ) {
    await res.body?.cancel();
    throw Error(uiText('成片回执的格式、大小或实测时长不符，请核对原版本。'));
  }
  const reader = res.body.getReader(),
    chunks: ArrayBuffer[] = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > maxOutput) {
        await reader.cancel();
        throw Error(uiText('成片超过 64 MiB。'));
      }
      chunks.push(new Uint8Array(value).buffer);
    }
  } finally {
    reader.releaseLock();
  }
  if (!size || (declared > 0 && declared !== size)) throw Error(uiText('收到的成片字节不完整。'));
  const blob = new Blob(chunks, { type: 'video/mp4' }),
    bytes = await blob.arrayBuffer(),
    ftyp = new Uint8Array(bytes, 0, Math.min(12, bytes.byteLength));
  if (ftyp.length < 12 || String.fromCharCode(...ftyp.slice(4, 8)) !== 'ftyp')
    throw Error(uiText('收到的文件缺少 MP4 容器头。'));
  const actual = Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes)), (x) =>
    x.toString(16).padStart(2, '0'),
  ).join('');
  signal.throwIfAborted();
  if (actual !== sha) throw Error(uiText('成片 SHA-256 与服务端回执不符。'));
  return { blob, duration, sha };
}
