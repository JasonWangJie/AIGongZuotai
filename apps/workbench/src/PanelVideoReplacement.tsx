import { useLayoutEffect, useRef, useState } from 'react';
import { api } from './api';
import { defaultLibraryFilters, type AssetEntry } from './libraryState';
import { uiText } from './locale';
import { libraryURL, summaryMatches, timelineApi, timelineProblem, usableVideo, versionMatches } from './videoTimeline';
import type { TimelineSummary, TimelineVersion } from './videoTimeline';
import type { Attempt, PanelScope, ReplaceTicket } from './panelVideos';

type Props = { scope: PanelScope; attempt: Attempt; disabled: boolean; issue: (e: unknown) => void;
  onReplace: (ticket: ReplaceTicket) => void };

export function PanelVideoReplacement({ scope, attempt, disabled, issue, onReplace }: Props) {
  const [assets, setAssets] = useState<AssetEntry[]>([]), [cursor, setCursor] = useState('');
  const [assetID, setAssetID] = useState(''), [read, setRead] = useState(false);
  const [timelines, setTimelines] = useState<TimelineSummary[]>([]), [listCursor, setListCursor] = useState('');
  const [target, setTarget] = useState(''), [versions, setVersions] = useState<TimelineSummary[]>([]);
  const [before, setBefore] = useState(0), [fixed, setFixed] = useState<TimelineVersion | null>(null);
  const [clipID, setClipID] = useState(''), [start, setStart] = useState(''), [end, setEnd] = useState('');
  const [busy, setBusy] = useState(false);
  const seq = useRef(0), lock = useRef(false);
  useLayoutEffect(() => () => { seq.current++; }, []);
  const p = scope.project;
  const asset = assets.find(item => item.id === assetID);
  const clip = fixed?.document.clips.find(item => item.id === clipID);
  async function load(work: (current: () => boolean) => Promise<void>) {
    if (lock.current || disabled) return;
    lock.current = true;
    const token = ++seq.current;
    const current = () => token === seq.current;
    setBusy(true);
    try { await work(current); }
    catch (e) { if (current()) issue(e); }
    finally { if (current()) { lock.current = false; setBusy(false); } }
  }
  function readAssets(more: boolean) {
    if (!more) { setAssets([]); setAssetID(''); setCursor(''); }
    void load(async current => {
      const page = await api.library(p, { ...defaultLibraryFilters(), source: 'generated', kind: 'video' }, more ? cursor : undefined);
      if (!current()) return;
      const allowed = page.items.filter(a => usableVideo(a, p) && a.task_id === attempt.task_id && !!a.artifact_id);
      setAssets(old => [...new Map([...(more ? old : []), ...allowed].map(a => [a.id, a])).values()]);
      setRead(true);
      setCursor(page.next_cursor || '');
    });
  }
  async function list(current: () => boolean, more = false) {
    const page = await timelineApi.list(p, more ? listCursor : undefined);
    if (!current()) return;
    if (!page.items.every(row => summaryMatches(row, p))) throw Error(uiText('剪辑稿清单范围不一致。'));
    setTimelines(old => [...new Map([...(more ? old : []), ...page.items].map(row => [row.id, row])).values()]);
    setListCursor(page.next_cursor || '');
  }
  function chooseAsset(id: string) {
    setAssetID(id); setTarget(''); setFixed(null); setClipID(''); setVersions([]); setTimelines([]);
    if (id) void load(current => list(current));
  }
  async function readVersion(current: () => boolean, id: string, version: number) {
    const result = await timelineApi.version(p, id, version);
    if (!current()) return;
    if (!versionMatches(result, p, id, version)) throw Error(uiText('剪辑版本身份不一致。'));
    setFixed(result); setClipID(''); setStart(''); setEnd('');
  }
  async function history(current: () => boolean, id: string, more = false) {
    const result = await timelineApi.versions(p, id, more ? before : undefined);
    if (!current()) return;
    if (!result.items.every(row => summaryMatches(row, p) && row.id === id)) throw Error(uiText('剪辑版本身份不一致。'));
    setVersions(old => [...new Map([...(more ? old : []), ...result.items].map(v => [v.version, v])).values()]);
    setBefore(result.next_before_version || 0);
  }
  function chooseTimeline(id: string) {
    setTarget(id); setFixed(null); setClipID(''); setVersions([]); setBefore(0);
    const chosen = timelines.find(row => row.id === id);
    if (chosen) void load(async current => {
      await history(current, chosen.id);
      if (current()) await readVersion(current, chosen.id, chosen.version);
    });
  }
  function chooseClip(id: string) {
    const c = fixed?.document.clips.find(row => row.id === id);
    setClipID(id); setStart(c ? String(c.in_ms) : ''); setEnd(c ? String(c.out_ms) : '');
  }
  const from = /^\d+$/.test(start) ? Number(start) : NaN, to = /^\d+$/.test(end) ? Number(end) : NaN;
  const change = asset && clip && fixed ? {
    ...fixed.document, clips: fixed.document.clips.map(c => c.id === clip.id ?
      { ...c, asset_id: asset.id, asset_revision: asset.revision, in_ms: from, out_ms: to } : c),
  } : null;
  const problem = change && timelineProblem(change);
  function replace() {
    if (!fixed || !asset || !clip || !change || problem || disabled || busy) return;
    onReplace({ kind: 'replace', key: crypto.randomUUID(), timeline: fixed.id, version: fixed.version, clip: clip.id,
      task_id: attempt.task_id, asset_id: asset.id, asset_revision: asset.revision, in_ms: from, out_ms: to });
  }
  return <fieldset className="pv-replace" disabled={disabled || busy}>
    <legend>{uiText('交付与剪辑')}</legend>
    <button onClick={() => readAssets(false)}>{uiText('读取该次交付成片')}</button>
    {read && <label>{uiText('交付成片')}<select value={assetID} onChange={e => chooseAsset(e.target.value)}>
      <option value="">{uiText('选择项目内的 MP4')}</option>
      {assets.map(a => <option key={a.id} value={a.id}>{a.name}</option>)}
    </select></label>}
    {read && !assets.length && <small>{uiText('此页没有该次尝试的可用成片。')}</small>}
    {cursor && <button onClick={() => readAssets(true)}>{uiText('继续查找成片')}</button>}
    {asset && <>
      <video controls playsInline preload="metadata" aria-label={uiText('交付成片预览')} src={libraryURL(p, asset.id)} />
      <label>{uiText('目标剪辑稿')}<select value={target} onChange={e => chooseTimeline(e.target.value)}>
        <option value="">{uiText('选择已有剪辑稿')}</option>
        {timelines.map(row => <option key={row.id} value={row.id}>{row.title} · v{row.version}</option>)}
      </select></label>
      {listCursor && <button onClick={() => void load(c => list(c, true))}>{uiText('更多剪辑稿')}</button>}
    </>}
    {asset && target && <>
      <label>{uiText('目标版本')}<select value={fixed?.version || ''} onChange={e => {
        const version = Number(e.target.value);
        setFixed(null); setClipID(''); if (version) void load(c => readVersion(c, target, version));
      }}><option value="">{uiText('选择固定版本')}</option>
        {versions.map(v => <option key={v.version} value={v.version}>v{v.version}</option>)}
      </select></label>
      {before > 0 && <button onClick={() => void load(c => history(c, target, true))}>{uiText('更早剪辑版本')}</button>}
    </>}
    {asset && fixed && <>
      <label>{uiText('目标镜头')}<select value={clipID} onChange={e => chooseClip(e.target.value)}>
        <option value="">{uiText('选择要替换的镜头')}</option>
        {fixed.document.clips.map((c, i) => <option key={c.id} value={c.id}>{i + 1} · {c.title}</option>)}
      </select></label>
      {clip && <>
        <div className="pv-controls">
          <label>{uiText('新入点（毫秒）')}<input inputMode="numeric" value={start} onChange={e => setStart(e.target.value)} /></label>
          <label>{uiText('新出点（毫秒）')}<input inputMode="numeric" value={end} onChange={e => setEnd(e.target.value)} /></label>
        </div>
        <p>{uiText('将《{0}》v{1} 的镜头「{2}」替换为选中的成片，并保存一个新版本。', fixed.document.title, fixed.version, clip.title)}</p>
        {problem && <p role="status">{problem}</p>}
        <button className="cb-primary" disabled={!!problem} onClick={replace}>{uiText('替换此镜头')}</button>
      </>}
    </>}
  </fieldset>;
}
