import { useCallback, useEffect, useRef, useState } from 'react';
import { api, ApiError } from './api';
import { keepUnknown } from './requestFeedback';
import { useInvalidation } from './useInvalidation';
import { useCountdown } from './useCountdown';
import { completeReference, emptyReferences, fileValidation, mergeReferences, moveReference, patchReference, reconcileReferenceList, ReferenceMemory, referenceScope, selectedReferenceIds } from './referenceState';
import type { ReferenceItem, ReferenceState } from './referenceState';
import { uploadReference } from './referenceUpload';
import type { ReferenceLimits } from './types';
import { mediaContentAllowed, mediaUploadRetryAllowed } from './mediaModeration';

const MAX_BUFFERED_BYTES = 64 * 1048576;
export function useReferences({ ownerId, projectId, limits, maxSelected, refreshVersion = 0, visible = true, onAuthExpired }: { ownerId: string; projectId?: string; limits?: ReferenceLimits; maxSelected: number; refreshVersion?: number; visible?: boolean; onAuthExpired: () => void }) {
  const key = projectId ? referenceScope(ownerId, projectId) : '';
  const memory = useRef(new ReferenceMemory()), currentKey = useRef(key), alive = useRef(true), generation = useRef(0); currentKey.current = key;
  const transfers = useRef(new Map<string, { scope: string; abort: () => void }>());
  const readCooldown = useRef({ scope: '', until: 0 });
  const [version, bump] = useState(0), [listing, setListing] = useState(false), [listError, setListError] = useState<ApiError | null>(null);
  const expire = useRef(onAuthExpired); expire.current = onAuthExpired;
  const update = useCallback((scope: string, change: (state: ReferenceState) => ReferenceState) => { memory.current.update(scope, change); if (alive.current && currentKey.current === scope) bump(value => value + 1); }, []);
  const state = key ? memory.current.get(key) : emptyReferences();

  const load = useCallback(async (offset = 0) => {
    if (!key || !projectId || !limits || readCooldown.current.scope === key && Date.now() < readCooldown.current.until) return;
    const ticket = ++generation.current, knownReady = new Set(memory.current.get(key).items.filter(item => item.phase === 'ready' && item.asset).map(item => item.asset!.id)); setListing(true); setListError(null);
    try { const page = await api.references(projectId, offset); if (!alive.current || currentKey.current !== key || ticket !== generation.current) return; update(key, old => ({ ...(offset === 0 && page.has_more === false ? reconcileReferenceList(old, page.items, projectId, knownReady) : mergeReferences(old, page.items, projectId)), hasMore: page.has_more === true, offset: offset + page.items.length })); }
    catch (cause) { if (alive.current && currentKey.current === key && ticket === generation.current) { if (cause instanceof ApiError && cause.status === 401) expire.current(); else { const issue = cause instanceof ApiError ? cause : new ApiError('参考素材读取失败。'); readCooldown.current = { scope: key, until: issue.retryAt }; setListError(issue); } } }
    finally { if (alive.current && currentKey.current === key && ticket === generation.current) setListing(false); }
  }, [key, projectId, !!limits, update]);
  useEffect(() => { alive.current = true; return () => { alive.current = false; generation.current++; transfers.current.forEach(item => item.abort()); }; }, []);
  useEffect(() => {
    generation.current++; setListing(false); setListError(null);
    for (const transfer of transfers.current.values()) if (transfer.scope !== key) transfer.abort();
    if (key && limits) void load();
    return () => { generation.current++; for (const transfer of transfers.current.values()) if (transfer.scope === key) transfer.abort(); };
  }, [key, load, !!limits]);
  useEffect(() => {
    if (!key || !projectId || !limits) return;
    let available = 2 - [...transfers.current.values()].filter(item => item.scope === key).length;
    for (const item of memory.current.get(key).items) {
      if (available <= 0) break;
      if (item.phase !== 'queued' || !item.file || !item.key || transfers.current.has(item.id)) continue;
      available--; const attempt = item.attempt + 1, scope = key, project = projectId;
      update(scope, old => patchReference(old, item.id, item.attempt, { phase: 'uploading', progress: 0, attempt, error: undefined }));
      const transfer = uploadReference(project, item.file, item.key, progress => update(scope, old => patchReference(old, item.id, attempt, { progress, phase: progress === 100 ? 'checking' : 'uploading' })));
      transfers.current.set(item.id, { scope, abort: transfer.abort });
      void transfer.promise.then(asset => update(scope, old => completeReference(old, item.id, attempt, asset))).catch(cause => {
        const issue = cause instanceof ApiError ? cause : new ApiError('上传结果待确认。', 0, true), unknown = keepUnknown(item.wasUnknown, issue);
        update(scope, old => patchReference(old, item.id, attempt, { phase: unknown ? 'uncertain' : 'failed', wasUnknown: unknown, error: issue }));
        if (issue.status === 401 && alive.current && currentKey.current === scope) expire.current();
      }).finally(() => { transfers.current.delete(item.id); if (alive.current && currentKey.current === scope) bump(value => value + 1); });
    }
  }, [key, projectId, !!limits, version, update]);
  useEffect(() => { const guard = (event: BeforeUnloadEvent) => { if (memory.current.hasPending()) { event.preventDefault(); event.returnValue = ''; } }; window.addEventListener('beforeunload', guard); return () => window.removeEventListener('beforeunload', guard); }, []);

  const readSeconds = useCountdown(listError?.retryAt || 0);
  useInvalidation(refreshVersion, visible && !!key, listing || readSeconds > 0, () => void load());
  function addFiles(files: File[]) {
    if (!key || !limits || !projectId) return;
    update(key, old => {
      const items = [...old.items], selected = [...old.selected], validation: string[] = []; let bytes = memory.current.pendingBytes();
      for (const file of files) {
        const invalid = fileValidation(file, limits);
        if (invalid) { validation.push(`${file.name}：${invalid}`); continue; }
        if (selected.length >= Math.min(limits.max_reference_images, maxSelected)) { validation.push(`${file.name}：本次参考图已达到当前模型上限，未上传此文件。`); continue; }
        if (bytes + file.size > MAX_BUFFERED_BYTES) { validation.push(`${file.name}：本页待处理文件较多，请先完成或处理原项目的上传。`); continue; }
        const id = crypto.randomUUID(); bytes += file.size;
        items.push({ id, name: file.name, byteSize: file.size, file, key: crypto.randomUUID(), phase: 'queued', progress: null, attempt: 0, wasUnknown: false }); selected.push(id);
      }
      return { ...old, items, selected, validation };
    });
  }
  function select(id: string) { if (!key) return; update(key, old => { const already = old.selected.includes(id), item = old.items.find(value => value.id === id); if (!already && item?.asset && !mediaContentAllowed(item.asset.moderation)) return { ...old, validation: ['此参考素材暂不可用于新生成，请移出或更换。原待确认请求保持不变。'] }; if (!already && old.selected.length >= maxSelected) return { ...old, validation: ['本次参考图已达当前模型上限，请先移出一张再选择。'] }; return { ...old, selected: already ? old.selected.filter(value => value !== id) : [...old.selected, id], validation: [] }; }); }
  function retry(id: string) { if (!key) return; update(key, old => { const item = old.items.find(value => value.id === id); if (!item?.file || !mediaUploadRetryAllowed(item.error?.code, item.wasUnknown) || !['uncertain', 'failed'].includes(item.phase) || Date.now() < (item.error?.retryAt || 0)) return old; return patchReference(old, id, item.attempt, { phase: 'queued', error: undefined, progress: null }); }); }
  function stop(id: string) { transfers.current.get(id)?.abort(); }
  function remove(id: string) { if (!key) return; update(key, old => { const item = old.items.find(value => value.id === id); return { ...old, selected: old.selected.filter(value => value !== id), items: item?.phase === 'queued' && item.attempt === 0 ? old.items.filter(value => value.id !== id) : old.items }; }); }
  function forget(id: string) { if (!key) return; update(key, old => { const item = old.items.find(value => value.id === id); if (!item || !['failed', 'uncertain'].includes(item.phase)) return old; return { ...old, items: old.items.filter(value => value.id !== id), selected: old.selected.filter(value => value !== id) }; }); }
  const selected = state.selected.map(id => state.items.find(item => item.id === id)).filter((item): item is ReferenceItem => !!item);
  return { state, selected, referenceIds: selectedReferenceIds(state), listing, listError, addFiles, select, retry, stop, remove, forget, move: (id: string, direction: -1 | 1) => key && update(key, old => moveReference(old, id, direction)), refresh: () => void load(), loadMore: () => void load(state.offset), currentPending: state.items.some(item => !!item.file && item.phase !== 'ready') };
}
