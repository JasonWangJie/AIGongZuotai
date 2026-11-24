import { useCallback, useLayoutEffect, useRef, useState } from 'react';
import { api, ApiError } from './api';
import { defaultLibraryFilters, type AssetEntry } from './libraryState';
import { uiText } from './locale';
import { eligibleSource, sourceDirectionName } from './sourceVideo';
import './sourceVideo.css';

const revisionKey = (asset: AssetEntry) => JSON.stringify([asset.id, asset.revision]);
type Listing = { scope: string; items: AssetEntry[]; version: number; cursor?: string; busy: boolean; error?: Error };
const empty = (scope: string): Listing => ({ scope, items: [], version: -1, busy: false });
type Choice = { scope: string; key: string; verified: number };
export function useSourceVideoPicker(scope: string, project: string, refreshVersion: number, expired: () => void) {
  const [list, setList] = useState<Listing>(() => empty(''));
  const [choice, setChoice] = useState<Choice | null>(null);
  const [retry, setRetry] = useState(0);
  const [checkError, setCheckError] = useState<Error | null>(null);
  const latest = useRef(refreshVersion);
  latest.current = refreshVersion;
  const flight = useRef<AbortController | null>(null);
  const onExpired = useRef(expired);
  onExpired.current = expired;
  const load = useCallback(async (cursor?: string) => {
    if (!scope || flight.current) return;
    const controller = new AbortController();
    flight.current = controller;
    const version = latest.current;
    if (!cursor) setChoice(null);
    setList(old => ({ ...(cursor && old.scope === scope ? old : empty(scope)), busy: true, error: undefined }));
    try {
      const page = await api.library(project, { ...defaultLibraryFilters(), source: 'generated', kind: 'video' }, cursor, controller.signal);
      if (controller.signal.aborted) return;
      setList(old => {
        const byId = new Map((cursor && old.scope === scope ? old.items : []).map(a => [a.id, a]));
        for (const asset of page.items) {
          if (eligibleSource(asset, project)) byId.set(asset.id, asset);
          else byId.delete(asset.id);
        }
        return { scope, items: [...byId.values()], version: cursor ? Math.min(old.version, version) : version, cursor: page.next_cursor || undefined, busy: false };
      });
    } catch (cause) {
      if (!controller.signal.aborted) {
        if (cause instanceof ApiError && cause.status === 401) onExpired.current();
        setList(old => ({ ...old, busy: false, error: cause instanceof Error ? cause : new Error(uiText('原视频列表读取失败。')) }));
      }
    } finally {
      if (flight.current === controller) flight.current = null;
    }
  }, [scope, project]);
  useLayoutEffect(() => {
    if (scope) void load();
    else { setList(empty('')); setChoice(null); }
    return () => { flight.current?.abort(); flight.current = null; };
  }, [scope, load]);
  const page = list.scope === scope ? list : empty(scope);
  const asset = choice?.scope === scope ? page.items.find(a => revisionKey(a) === choice.key) : undefined;
  const assetID = asset?.id, assetRevision = asset?.revision;
  const checking = !!asset && choice?.verified !== refreshVersion;
  const observed = useRef({scope, refreshVersion});
  useLayoutEffect(() => {
    const previous = observed.current;
    observed.current = {scope, refreshVersion};
    setCheckError(null);
    if (!scope) return;
    if (!assetID || !choice) {
      if (previous.scope === scope && previous.refreshVersion !== refreshVersion) {
        flight.current?.abort(); flight.current = null; void load();
      }
      return;
    }
    if (!checking) return;
    const controller = new AbortController();
    function invalidate(replacement?: AssetEntry) {
      setList(old => old.scope !== scope ? old : { ...old, items: old.items.flatMap(a => a.id !== assetID ? [a] : replacement ? [replacement] : []) });
      setChoice(null);
    }
    void api.libraryAsset(project, assetID, controller.signal).then(current => {
      if (controller.signal.aborted) return;
      if (current.id !== assetID || !eligibleSource(current, project)) { invalidate(); return; }
      if (current.revision !== assetRevision) { invalidate(current); return; }
      setChoice({ ...choice, verified: refreshVersion });
    }).catch(cause => {
      if (controller.signal.aborted) return;
      if (cause instanceof ApiError && cause.status === 401) { onExpired.current(); return; }
      if (cause instanceof ApiError && (cause.status === 403 || cause.status === 404 || cause.status === 410)) { invalidate(); return; }
      setCheckError(cause instanceof Error ? cause : new Error(uiText('原视频列表读取失败。')));
    });
    return () => controller.abort();
  }, [scope, project, refreshVersion, load, choice, checking, assetID, assetRevision, retry]);
  return { page, asset, checking, checkError, recheck: () => setRetry(n => n + 1),
    select: (key: string) => setChoice(key ? { scope, key, verified: page.version } : null), load };
}
type Props = { picker: ReturnType<typeof useSourceVideoPicker>; disabled: boolean; directions: string[]; direction: string; onDirection: (direction: string) => void };
export function SourceVideoPicker({ picker, disabled, directions, direction, onDirection }: Props) {
  const { page, asset } = picker;
  return <fieldset className="source-video-fields" disabled={disabled}>
    <legend>{uiText('原视频素材')}</legend>
    <label>{uiText('项目原视频')}<select value={asset ? revisionKey(asset) : ''} disabled={!page.items.length} onChange={e => picker.select(e.target.value)}>
      <option value="">{uiText('选择项目内的 MP4')}</option>
      {page.items.map(a => <option key={revisionKey(a)} value={revisionKey(a)}>{uiText('{0} · 修订 {1}', a.name, a.revision)}</option>)}
    </select></label>
    {directions.length > 0 && <label>{uiText('延长方向')}<select value={direction} onChange={e => onDirection(e.target.value)}>
      <option value="">{uiText('模型默认方向')}</option>
      {directions.map(value => <option key={value} value={value}>{sourceDirectionName(value)}</option>)}
    </select></label>}
    <div className="source-video-actions"><button className="text-button" type="button" disabled={page.busy} onClick={() => void picker.load()}>{uiText('刷新素材')}</button>
      {picker.checkError && <button type="button" className="text-button" onClick={picker.recheck}>{uiText('重新核对')}</button>}
      {page.cursor && <button className="text-button" type="button" disabled={page.busy} onClick={() => void picker.load(page.cursor)}>{uiText('读取更多素材')}</button>}
      <span role="status">{picker.checkError ? picker.checkError.message : page.busy || picker.checking ? uiText('正在读取素材…') : page.error ? page.error.message : !page.items.length ? uiText('此页没有可用的项目 MP4。') : ''}</span>
    </div>
  </fieldset>;
}
