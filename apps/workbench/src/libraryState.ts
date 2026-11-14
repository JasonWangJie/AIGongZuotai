import type { ApiError } from './api';
export type AssetEntry = { id: string; project_id: string; source: 'reference' | 'generated'; reference_id?: string; task_id?: string; artifact_id?: string; provider_mode?: 'demo' | 'live'; kind: 'image' | 'video' | 'poster'; name: string; tags: string[]; state: 'active' | 'trashed'; revision: number; created_at: string; updated_at: string; deleted_at: string | null; content_url: string | null; thumbnail_url: string | null; content_type?: string; byte_size?: number };
export type LibraryFilters = { state: 'active' | 'trashed'; source: '' | 'reference' | 'generated'; kind: '' | 'image' | 'video' | 'poster'; q: string; tag: string };
export type LibraryPageData = { items: AssetEntry[]; next_cursor: string | null; has_more: boolean; limit: number };
export type AssetRevision = Pick<AssetEntry, 'revision' | 'name' | 'tags' | 'state' | 'updated_at'>;
export type AssetRevisionPage = { items: AssetRevision[]; has_more: boolean; next_before_revision: number | null };
export type AssetOperation = { kind: 'metadata' | 'trash' | 'restore'; key: string; body: { expected_revision: number; name?: string; tags?: string[] } };
export type AssetEditor = { server: AssetEntry; name: string; tagsText: string; phase: 'ready' | 'saving' | 'uncertain' | 'conflict' | 'error'; pending: AssetOperation | null; conflict: AssetEntry | null; error: ApiError | null; message: string; force: boolean; conflictKind: AssetOperation['kind'] };
export const defaultLibraryFilters = (): LibraryFilters => ({ state: 'active', source: '', kind: '', q: '', tag: '' });
export function parseAssetTags(text: string): string[] { return [...new Set(text.split(/[,，\n]/).map(tag => tag.trim().toLowerCase()).filter(Boolean))]; }
export function metadataError(name: string, tags: string[]): string { if (!name.trim() || Array.from(name.trim()).length > 120 || /[\u0000-\u001f\u007f]/u.test(name)) return '名称需为 1–120 个字符，不能包含控制字符。'; if (tags.length > 10 || tags.some(tag => Array.from(tag).length > 32 || /[\u0000-\u001f\u007f]/u.test(tag))) return '最多 10 个标签，每个不超过 32 个字符。'; return ''; }
export function initialAssetEditor(server: AssetEntry): AssetEditor { return { server, name: server.name, tagsText: server.tags.join(', '), phase: 'ready', pending: null, conflict: null, error: null, message: '', force: false, conflictKind: 'metadata' }; }
export function assetDirty(state: AssetEditor): boolean { return !!state.pending || state.force || state.name.trim() !== state.server.name || JSON.stringify(parseAssetTags(state.tagsText)) !== JSON.stringify(state.server.tags); }
export type AssetAction = { type: 'edit'; name: string; tagsText: string } | { type: 'saving'; operation: AssetOperation } | { type: 'saved'; key: string; asset: AssetEntry } | { type: 'observed'; asset: AssetEntry } | { type: 'reset' } | { type: 'failed'; key: string; error: ApiError; unknown: boolean } | { type: 'conflict_snapshot'; asset: AssetEntry } | { type: 'use_server' } | { type: 'keep_mine' } | { type: 'restore_metadata'; revision: AssetRevision };
export function assetReducer(state: AssetEditor, action: AssetAction): AssetEditor {
  if ('key' in action && state.pending?.key !== action.key) return state;
  if ('asset' in action && (action.asset.project_id !== state.server.project_id || action.asset.id !== state.server.id)) return state;
  switch (action.type) {
    case 'edit': return state.server.state === 'trashed' || ['saving', 'uncertain'].includes(state.phase) ? state : { ...state, name: action.name, tagsText: action.tagsText, error: null, message: '' };
    case 'saving': return { ...state, phase: 'saving', pending: { ...action.operation, body: { ...action.operation.body, ...(action.operation.body.tags ? { tags: [...action.operation.body.tags] } : {}) } }, error: null, message: '' };
    case 'saved': return action.asset.revision < state.server.revision ? { ...state, phase: 'ready', pending: null, message: '原请求已确认，请重新打开详情核对当前版本。' } : { ...initialAssetEditor(action.asset), message: '变更已保存。' };
    case 'observed': return assetDirty(state) || state.phase === 'conflict' || action.asset.revision < state.server.revision ? state : initialAssetEditor(action.asset);
    case 'reset': return state.pending ? state : initialAssetEditor(state.server);
    case 'failed': return { ...state, phase: action.unknown ? 'uncertain' : ['ASSET_REVISION_CONFLICT', 'ASSET_STATE_CONFLICT'].includes(action.error.code || '') ? 'conflict' : 'error', pending: action.unknown ? state.pending : null, conflict: null, conflictKind: state.pending?.kind || 'metadata', error: action.error, message: action.error.message };
    case 'conflict_snapshot': return state.phase !== 'conflict' || action.asset.revision < (state.conflict?.revision || 0) ? state : { ...state, conflict: action.asset };
    case 'use_server': return state.conflict ? initialAssetEditor(state.conflict) : state;
    case 'keep_mine': return state.conflict && state.conflict.state === 'active' ? { ...state, server: state.conflict, phase: 'ready', conflict: null, force: true, error: null, message: '已保留你的名称和标签，请核对后另存为新版本。' } : state;
    case 'restore_metadata': return state.server.state === 'trashed' || ['saving', 'uncertain', 'conflict'].includes(state.phase) ? state : { ...state, name: action.revision.name, tagsText: action.revision.tags.join(', '), force: true, message: '历史名称和标签已载入，保存后成为新版本；回收状态保持当前值。' };
  }
}
export function libraryScope(owner: string, project: string, asset: string): string { return JSON.stringify([owner, project, asset]); }
export class LibraryRecovery {
  private entries = new Map<string, AssetEditor>();
  remember(owner: string, project: string, state: AssetEditor): void { const key = libraryScope(owner, project, state.server.id); if (assetDirty(state)) this.entries.set(key, state); else this.entries.delete(key); }
  restore(owner: string, project: string, asset: AssetEntry): AssetEditor { const saved = this.entries.get(libraryScope(owner, project, asset.id)); return saved ? saved.pending ? { ...saved, phase: 'uncertain', message: '已恢复本页待确认操作，请使用原请求重试核对。' } : saved : initialAssetEditor(asset); }
  list(owner: string, project: string): AssetEditor[] { return [...this.entries.entries()].filter(([key]) => { const scope = JSON.parse(key); return scope[0] === owner && scope[1] === project; }).map(([, value]) => value); }
  hasUnsaved(): boolean { return this.entries.size > 0; }
}
export function mergeLibrary(current: AssetEntry[], incoming: AssetEntry[], project: string): AssetEntry[] { const byId = new Map(current.filter(asset => asset.project_id === project).map(asset => [asset.id, asset])); for (const asset of incoming) if (asset.project_id === project && (!byId.has(asset.id) || asset.revision >= byId.get(asset.id)!.revision)) byId.set(asset.id, asset); return [...byId.values()].sort(compareLibraryOrder); }
export function compareLibraryOrder(a: AssetEntry, b: AssetEntry): number { const time = Date.parse(b.created_at) - Date.parse(a.created_at); if (Number.isFinite(time) && time !== 0) return time; const nanos = (value: string) => Number((value.match(/\.(\d{1,9})(?:Z|[+-]\d{2}:\d{2})$/)?.[1] || '').padEnd(9, '0')), fine = nanos(b.created_at) - nanos(a.created_at); return fine || (a.id === b.id ? 0 : a.id < b.id ? 1 : -1); }
export function updateLoadedLibrary(current: AssetEntry[], asset: AssetEntry, project: string, filters: LibraryFilters, loadedIds: ReadonlySet<string>): AssetEntry[] { const known = current.find(item => item.id === asset.id); if (asset.project_id !== project || !loadedIds.has(asset.id) || known && known.revision > asset.revision) return current; return libraryMatches(asset, filters) ? mergeLibrary(current, [asset], project) : current.filter(item => item.id !== asset.id); }
export function libraryMatches(asset: AssetEntry, filters: LibraryFilters): boolean { const q = filters.q.trim().toLowerCase(), tag = filters.tag.trim().toLowerCase(); return asset.state === filters.state && (!filters.source || asset.source === filters.source) && (!filters.kind || asset.kind === filters.kind) && (!tag || asset.tags.includes(tag)) && (!q || asset.name.toLowerCase().includes(q) || asset.tags.some(value => value.toLowerCase().includes(q))); }
export function libraryQuery(filters: LibraryFilters, cursor?: string): string { const query = new URLSearchParams({ state: filters.state, limit: '24' }); for (const key of ['source', 'kind', 'q', 'tag'] as const) if (filters[key].trim()) query.set(key, filters[key].trim()); if (cursor) query.set('cursor', cursor); return query.toString(); }
