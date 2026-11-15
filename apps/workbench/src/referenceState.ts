import type { ApiError } from './api';
import type { ReferenceAsset, ReferenceLimits } from './types';
import { mediaContentAllowed, mergeReferenceAsset } from './mediaModeration';

export type UploadPhase = 'queued' | 'uploading' | 'checking' | 'ready' | 'uncertain' | 'failed';
export type ReferenceItem = { id: string; name: string; byteSize: number; file?: File; key?: string; phase: UploadPhase; progress: number | null; attempt: number; wasUnknown: boolean; asset?: ReferenceAsset; error?: ApiError };
export type ReferenceState = { items: ReferenceItem[]; selected: string[]; validation: string[]; hasMore: boolean; offset: number };
export function emptyReferences(): ReferenceState { return { items: [], selected: [], validation: [], hasMore: false, offset: 0 }; }
export function referenceScope(ownerId: string, projectId: string): string { return JSON.stringify([ownerId, projectId]); }
export function fileValidation(file: Pick<File, 'name' | 'size' | 'type'>, limits: ReferenceLimits): string | null {
  if (file.size <= 0) return '文件为空，请重新选择。';
  if (file.size > limits.max_file_bytes) return `文件超过 ${(limits.max_file_bytes / 1048576).toFixed(0)} MiB 上限。`;
  const extensionType = /\.png$/i.test(file.name) ? 'image/png' : /\.jpe?g$/i.test(file.name) ? 'image/jpeg' : /\.webp$/i.test(file.name) ? 'image/webp' : '';
  const type = file.type || extensionType;
  return limits.allowed_content_types.includes(type) ? null : '仅支持 PNG、JPEG、WebP 图片。';
}
export function patchReference(state: ReferenceState, id: string, attempt: number, change: Partial<ReferenceItem>): ReferenceState {
  const item = state.items.find(value => value.id === id); if (!item || item.attempt !== attempt) return state;
  return { ...state, items: state.items.map(value => value.id === id ? { ...value, ...change } : value) };
}
export function completeReference(state: ReferenceState, id: string, attempt: number, asset: ReferenceAsset): ReferenceState {
  const original = state.items.find(item => item.id === id); if (!original || original.attempt !== attempt) return state;
  const duplicates = new Set(state.items.filter(item => item.id !== id && item.asset?.id === asset.id).map(item => item.id));
  const known = state.items.filter(item => item.asset?.id === asset.id).reduce<ReferenceAsset | undefined>((value, item) => mergeReferenceAsset(value, item.asset!), undefined);
  const safe = mergeReferenceAsset(known, asset);
  const next = patchReference(state, id, attempt, { asset: safe, name: asset.name, phase: 'ready', file: undefined, progress: 100, wasUnknown: false, error: undefined });
  return { ...next, items: next.items.filter(item => !duplicates.has(item.id)), selected: [...new Set(next.selected.map(item => duplicates.has(item) ? id : item))] };
}
export function mergeReferences(state: ReferenceState, assets: ReferenceAsset[], projectId: string): ReferenceState {
  const items = [...state.items];
  for (const asset of assets) { if (asset.project_id !== projectId) continue; const existing = items.findIndex(item => item.asset?.id === asset.id); if (existing >= 0) items[existing] = { ...items[existing], asset: mergeReferenceAsset(items[existing].asset, asset) }; else items.push({ id: 'asset:' + asset.id, name: asset.name, byteSize: asset.byte_size, phase: 'ready', progress: 100, attempt: 0, wasUnknown: false, asset: mergeReferenceAsset(undefined, asset) }); }
  return { ...state, items };
}
export function reconcileReferenceList(state: ReferenceState, assets: ReferenceAsset[], projectId: string, knownReady: Set<string>): ReferenceState {
  const visible = new Set(assets.filter(asset => asset.project_id === projectId).map(asset => asset.id));
  const removed = new Set(state.items.filter(item => item.phase === 'ready' && item.asset && knownReady.has(item.asset.id) && !visible.has(item.asset.id)).map(item => item.id));
  const selected = state.selected.filter(id => !removed.has(id));
  return mergeReferences({ ...state, items: state.items.filter(item => !removed.has(item.id)), selected, validation: selected.length !== state.selected.length ? ['部分参考素材已回收或不可用，已从本次选择移出。已受理任务的原请求保持不变。'] : state.validation }, assets, projectId);
}
export function selectedReferenceIds(state: ReferenceState): string[] | null {
  const items = state.selected.map(id => state.items.find(item => item.id === id));
  if (items.some(item => !item?.asset || item.phase !== 'ready' || !mediaContentAllowed(item.asset.moderation))) return null;
  const ids = items.map(item => item!.asset!.id);
  return new Set(ids).size === ids.length ? ids : null;
}
export function moveReference(state: ReferenceState, id: string, direction: -1 | 1): ReferenceState {
  const index = state.selected.indexOf(id), next = index + direction; if (index < 0 || next < 0 || next >= state.selected.length) return state;
  const selected = [...state.selected]; [selected[index], selected[next]] = [selected[next], selected[index]]; return { ...state, selected };
}
export class ReferenceMemory {
  private entries = new Map<string, ReferenceState>();
  get(scope: string): ReferenceState { return this.entries.get(scope) || emptyReferences(); }
  update(scope: string, change: (state: ReferenceState) => ReferenceState): ReferenceState { const next = change(this.get(scope)); this.entries.set(scope, next); return next; }
  pendingBytes(): number { return [...this.entries.values()].reduce((sum, state) => sum + state.items.reduce((total, item) => total + (item.file?.size || 0), 0), 0); }
  hasPending(): boolean { return [...this.entries.values()].some(state => state.items.some(item => !!item.file && item.phase !== 'ready')); }
}
