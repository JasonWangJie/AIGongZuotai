import { uiText } from './locale';
import { ApiError, request } from './api';

export type CanvasNode = {
  id: string;
  title: string;
  note?: string;
  x: number;
  y: number;
  group_id?: string;
  asset_id?: string;
  asset_revision?: number;
  run_id?: string;
};
export type CanvasEdge = { id: string; from: string; to: string };
export type CanvasGroup = { id: string; title: string; collapsed: boolean };
export type CanvasDocument = {
  title: string;
  nodes: CanvasNode[];
  edges: CanvasEdge[];
  groups: CanvasGroup[];
};
export type CanvasSource = {
  available: boolean;
  content_version?: string;
  thumbnail_url?: string;
  status?: string;
  seq?: number;
};
export type CanvasSources = Record<string, CanvasSource>;
export type CanvasVersion = {
  project_id: string;
  revision: number;
  created_at: string;
  document: CanvasDocument;
  sources: CanvasSources;
};
export type CanvasRevision = {
  revision: number;
  title: string;
  node_count: number;
  edge_count: number;
  created_at: string;
};

export const emptyCanvas = (): CanvasDocument => ({ title: uiText('未命名画布'), nodes: [], edges: [], groups: [] });
export const newCanvasID = (prefix: string) => prefix + '_' + crypto.randomUUID().replaceAll('-', '');
const base = (project: string) => '/projects/' + encodeURIComponent(project) + '/canvas';
const versionPath = (p: string, n: number) => base(p) + '/revisions/' + n;
function read<T>(project: string, path: string, signal?: AbortSignal, key?: string) {
  return request<T>(path, {
    signal,
    headers: { 'X-Project-ID': project, ...(key ? { 'Idempotency-Key': key } : {}) },
  });
}
export const freeCanvasApi = {
  current: (p: string, s?: AbortSignal) => read<CanvasVersion>(p, base(p), s),
  version: (p: string, n: number, s?: AbortSignal) => read<CanvasVersion>(p, versionPath(p, n), s),
  sources: (p: string, n: number, s?: AbortSignal) =>
    read<Pick<CanvasVersion, 'project_id' | 'revision' | 'sources'>>(p, versionPath(p, n) + '/sources', s),
  revisions: (p: string, before?: number, s?: AbortSignal) =>
    read<{ items: CanvasRevision[]; next_before_revision?: number }>(
      p,
      base(p) + '/revisions?limit=20' + (before ? '&before_revision=' + before : ''),
      s,
    ),
  receipt: (p: string, key: string, s?: AbortSignal) => read<CanvasVersion>(p, base(p) + '/receipt', s, key),
  save: async (p: string, n: number, doc: CanvasDocument, key: string, s: AbortSignal) => {
    const body = JSON.stringify({ expected_revision: n, document: doc });
    if (new TextEncoder().encode(body).byteLength > 2 * 1024 * 1024)
      throw new ApiError(uiText('画布保存正文超过 2 MiB，请缩短节点备注后再保存。'), 413);
    return request<CanvasVersion>(base(p), {
      method: 'PUT',
      signal: s,
      headers: { 'X-Project-ID': p, 'Idempotency-Key': key },
      body,
    });
  },
};

export function imagePath(p: string, revision: number, node: CanvasNode, source?: CanvasSource) {
  if (!node.asset_id || !source?.available || !/^[a-f0-9]{64}$/.test(source.content_version || '')) return;
  const path =
    '/api/v1' +
    versionPath(p, revision) +
    '/nodes/' +
    encodeURIComponent(node.id) +
    '/thumbnail?project_id=' +
    encodeURIComponent(p);
  if (path === source.thumbnail_url) return path;
}

export function usableSource(
  node: CanvasNode,
  version: CanvasVersion | null,
  sources: CanvasSources,
): CanvasSource | undefined {
  if (!version || (!node.asset_id && !node.run_id)) return;
  const original = version.document.nodes.find((n) => n.id === node.id);
  if (
    original &&
    original.asset_id === node.asset_id &&
    original.asset_revision === node.asset_revision &&
    original.run_id === node.run_id
  ) {
    return Object.hasOwn(sources, node.id) ? sources[node.id] : undefined;
  }
}

export const coord = (v: number) => Math.max(-1_000_000, Math.min(1_000_000, Math.round(v)));
export function validCanvas(doc: CanvasDocument) {
  const validText = (s: string, max: number) => !!s.trim() && Array.from(s).length <= max;
  const validID = (s: string) => /^[A-Za-z0-9_-]{1,48}$/.test(s);
  const nodes = new Set(doc.nodes.map((n) => n.id)),
    groups = new Set(doc.groups.map((g) => g.id));
  if (
    !validText(doc.title, 120) ||
    doc.nodes.length > 2000 ||
    doc.edges.length > 4000 ||
    doc.groups.length > 128 ||
    nodes.size !== doc.nodes.length ||
    groups.size !== doc.groups.length ||
    new Set(doc.edges.map((e) => e.id)).size !== doc.edges.length
  )
    return false;
  if (doc.groups.some((g) => !validID(g.id) || !validText(g.title, 120))) return false;
  if (
    doc.nodes.some(
      (n) =>
        !validID(n.id) ||
        !validText(n.title, 120) ||
        Array.from(n.note || '').length > 600 ||
        n.x !== coord(n.x) ||
        n.y !== coord(n.y) ||
        (!!n.group_id && !groups.has(n.group_id)) ||
        (!!n.asset_id &&
          (!Number.isSafeInteger(n.asset_revision) || (n.asset_revision ?? 0) < 1 || !!n.run_id)) ||
        (!n.asset_id && n.asset_revision !== undefined),
    )
  )
    return false;
  const edges = new Set<string>();
  return (
    doc.nodes.filter((n) => n.asset_id).length <= 16 &&
    doc.nodes.filter((n) => n.run_id).length <= 16 &&
    doc.edges.every((e) => {
      const pair = e.from + ' ' + e.to;
      if (!validID(e.id) || !nodes.has(e.from) || !nodes.has(e.to) || e.from === e.to || edges.has(pair))
        return false;
      edges.add(pair);
      return true;
    })
  );
}
