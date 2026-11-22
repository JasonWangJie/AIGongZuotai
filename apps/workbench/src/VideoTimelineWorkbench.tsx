import { uiText } from './locale';
import { useEffect, useRef, useState } from 'react';
import { api, ApiError } from './api';
import { defaultLibraryFilters } from './libraryState';
import type { AssetEntry } from './libraryState';
import { TimelinePlayer } from './TimelinePlayer';
import {
  changes,
  emptyTimeline,
  exportVideo,
  lengthMs,
  libraryURL,
  readOperation,
  slotFor,
  sourceURL,
  summaryMatches,
  timelineApi,
  timelineProblem,
  usableVideo,
  versionMatches,
} from './videoTimeline';
import type {
  Clip,
  ClipCapability,
  Operation,
  Timeline,
  TimelineSummary,
  TimelineVersion,
} from './videoTimeline';
import './video-timeline.css';
type Props = {
  owner: string;
  project: string;
  enabled: boolean;
  visible: boolean;
  onDirty: (v: boolean) => void;
  onExpired: () => void;
};
const seconds = (ms: number) => (Number.isFinite(ms) ? (ms / 1000).toFixed(3) + uiText(' 秒') : '—');
function download(blob: Blob, title: string, version: number) {
  const url = URL.createObjectURL(blob),
    a = document.createElement('a');
  a.href = url;
  a.download =
    (title.replace(/[<>:"/\\|?*\u0000-\u001f]/g, '_').slice(0, 80) || uiText('剪辑稿')) + '-v' + version + '.mp4';
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 30000);
}
export function VideoTimelineWorkbench({ project, owner, enabled, visible, onDirty, onExpired }: Props) {
  const [doc, setDoc] = useState<Timeline>(emptyTimeline),
    [base, setBase] = useState<TimelineVersion | null>(null),
    [idx, setIdx] = useState(0),
    [dirty, setDirty] = useState(false),
    [auto, setAuto] = useState(false);
  const [caps, setCaps] = useState<ClipCapability | null>(null),
    [rows, setRows] = useState<TimelineSummary[]>([]),
    [cursor, setCursor] = useState(''),
    [history, setHistory] = useState<TimelineSummary[]>([]),
    [before, setBefore] = useState(0);
  const [assets, setAssets] = useState<AssetEntry[]>([]),
    [assetCursor, setAssetCursor] = useState(''),
    [picking, setPicking] = useState(false),
    [durations, setDurations] = useState<Record<string, number>>({});
  const [pending, setPending] = useState<Operation | null>(null),
    [blocked, setBlocked] = useState(false),
    [busy, setBusy] = useState(''),
    [error, setError] = useState(''),
    [status, setStatus] = useState(''),
    [conflict, setConflict] = useState(false);
  const [swap, setSwap] = useState<{ id: string; version: number } | null>(null),
    [comparison, setComparison] = useState<TimelineVersion | null>(null),
    [restoring, setRestoring] = useState(false);
  const alive = useRef(true),
    lock = useRef(false),
    cancel = useRef<AbortController | null>(null),
    tickets = useRef({ list: 0, versions: 0, assets: 0 }),
    slot = slotFor(owner, project);
  const clip = doc.clips[idx],
    latestID = useRef('');
  latestID.current = base?.id || '';
  const latest = Math.max(
    0,
    ...history.map((s) => s.version),
    rows.find((r) => r.id === base?.id)?.version || 0,
    base?.version || 0,
  );
  // The loaded version is authoritative even when the latest directory page
  // omits this timeline or still advertises an older revision.
  const currentEntry =
    base?.project_id === project ? { id: base.id, title: base.document.title, version: base.version } : null;
  const catalog =
    currentEntry && !rows.some((row) => row.id === currentEntry.id && row.version >= currentEntry.version)
      ? [currentEntry, ...rows.filter((row) => row.id !== currentEntry.id)]
      : rows;
  const locked = !!busy || !!pending || blocked,
    guarded = dirty || !!pending;
  useEffect(() => {
    alive.current = true;
    try {
      setPending(readOperation(slot));
    } catch {
      setBlocked(true);
      setError(uiText('无法核对原操作编号。'));
    }
    return () => {
      alive.current = false;
      cancel.current?.abort();
    };
  }, [slot]);
  useEffect(() => {
    onDirty(guarded);
    return () => onDirty(false);
  }, [guarded, onDirty]);
  useEffect(() => {
    if (!guarded) return;
    const guard = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', guard);
    return () => window.removeEventListener('beforeunload', guard);
  }, [guarded]);
  useEffect(() => {
    if (!visible) setAuto(false);
  }, [visible]);
  function issue(e: unknown) {
    if (!alive.current) return;
    if (e instanceof ApiError && e.status === 401) onExpired();
    setError(e instanceof Error ? e.message : uiText('请求未完成。'));
  }
  async function work(action: string, fn: () => Promise<void>) {
    if (lock.current) return;
    lock.current = true;
    setBusy(action);
    setError('');
    setStatus('');
    try {
      await fn();
    } catch (e) {
      issue(e);
    } finally {
      lock.current = false;
      if (alive.current) setBusy('');
    }
  }
  async function list(more = false) {
    const t = ++tickets.current.list;
    try {
      const r = await timelineApi.list(project, more ? cursor : undefined);
      if (!alive.current || t !== tickets.current.list) return;
      if (!Array.isArray(r.items) || !r.items.every((x) => summaryMatches(x, project)))
        throw Error(uiText('项目剪辑稿列表范围不符。'));
      setRows((old) => [...new Map([...(more ? old : []), ...r.items].map((v) => [v.id, v])).values()]);
      setCursor(r.next_cursor || '');
    } catch (e) {
      if (t === tickets.current.list) issue(e);
    }
  }
  async function versions(id: string, more = false) {
    const t = ++tickets.current.versions;
    try {
      const r = await timelineApi.versions(project, id, more ? before : undefined);
      if (!alive.current || latestID.current !== id || t !== tickets.current.versions) return;
      if (!Array.isArray(r.items) || !r.items.every((v) => summaryMatches(v, project) && v.id === id))
        throw Error(uiText('版本列表与所选剪辑稿不符。'));
      setHistory((old) => [
        ...new Map([...(more ? old : []), ...r.items].map((v) => [v.version, v])).values(),
      ]);
      setBefore(r.next_before_version || 0);
    } catch (e) {
      if (t === tickets.current.versions) issue(e);
    }
  }
  useEffect(() => {
    if (!visible || !enabled) return;
    let active = true;
    void timelineApi
      .caps(project)
      .then((c) => {
        if (
          !c ||
          c.max_clips !== 8 ||
          c.max_duration_ms !== 30000 ||
          typeof c.editable !== 'boolean' ||
          typeof c.export_enabled !== 'boolean'
        )
          throw Error(uiText('剪辑能力响应无法核对。'));
        if (active) setCaps(c);
      })
      .catch((e) => {
        if (active) {
          setCaps(null);
          issue(e);
        }
      });
    void list();
    if (base) void versions(base.id);
    return () => {
      active = false;
    };
  }, [visible, enabled, project, base?.id, base?.version]);
  async function gallery(more = false) {
    const t = ++tickets.current.assets;
    try {
      const r = await api.library(
        project,
        { ...defaultLibraryFilters(), source: 'generated', kind: 'video' },
        more ? assetCursor : undefined,
      );
      if (!alive.current || t !== tickets.current.assets) return;
      const valid = r.items.filter((a) => usableVideo(a, project));
      setAssets((old) => [...new Map([...(more ? old : []), ...valid].map((v) => [v.id, v])).values()]);
      setAssetCursor(r.next_cursor || '');
    } catch (e) {
      if (t === tickets.current.assets) issue(e);
    }
  }
  function edit(value: Timeline) {
    setDoc(value);
    setDirty(true);
    setAuto(false);
    setComparison(null);
    setRestoring(false);
    setStatus('');
  }
  function patch(value: Partial<Clip>) {
    edit({ ...doc, clips: doc.clips.map((c, i) => (i === idx ? { ...c, ...value } : c)) });
  }
  function add(a: AssetEntry) {
    if (doc.clips.length >= 8 || !usableVideo(a, project)) return;
    const c: Clip = {
      id: crypto.randomUUID(),
      title: a.name.slice(0, 110) || uiText('新镜头'),
      asset_id: a.id,
      asset_revision: a.revision,
      in_ms: 0,
      out_ms: 1000,
      asset_available: true,
    };
    edit({ ...doc, clips: [...doc.clips, c] });
    setIdx(doc.clips.length);
    setPicking(false);
  }
  function move(d: number) {
    const n = idx + d;
    if (n < 0 || n >= doc.clips.length) return;
    const clips = [...doc.clips];
    [clips[idx], clips[n]] = [clips[n], clips[idx]];
    edit({ ...doc, clips });
    setIdx(n);
  }
  function install(v: TimelineVersion) {
    if (latestID.current !== v.id) {
      setHistory([]);
      setBefore(0);
    }
    setBase(v);
    setDoc(v.document);
    setDirty(false);
    setIdx(0);
    setAuto(false);
    setComparison(null);
    setRestoring(false);
    setConflict(false);
    setPicking(false);
  }
  async function fixed(id: string, n: number) {
    const v = await timelineApi.version(project, id, n);
    if (!versionMatches(v, project, id, n)) throw Error(uiText('返回的版本与原项目剪辑稿不符。'));
    return v;
  }
  function open(id: string, n: number) {
    setSwap(null);
    if (!id) {
      setBase(null);
      setDoc(emptyTimeline());
      setDirty(false);
      setIdx(0);
      setAuto(false);
      setHistory([]);
      setBefore(0);
      setComparison(null);
      setConflict(false);
      setPicking(false);
      setError('');
      setStatus('');
      return;
    }
    void work(uiText('读取'), async () => {
      const v = await fixed(id, n);
      if (alive.current) install(v);
    });
  }
  function choose(id: string, n: number) {
    if (locked) return;
    if (dirty) setSwap({ id, version: n });
    else open(id, n);
  }
  function remember(i: Operation) {
    sessionStorage.setItem(slot, JSON.stringify(i));
    setPending(i);
  }
  function clear(i: Operation) {
    const current = readOperation(slot);
    if (current && JSON.stringify(current) === JSON.stringify(i)) sessionStorage.removeItem(slot);
    if (alive.current) setPending(readOperation(slot));
  }
  function accept(v: TimelineVersion, i: Operation) {
    if (!versionMatches(v, project, i.id, i.version + 1)) throw Error(uiText('保存回执与原剪辑稿版本不符。'));
    clear(i);
    if (alive.current) {
      install(v);
      setStatus(uiText('已保存固定版本 v') + v.version);
    }
  }
  function save(timelineId: string | null = base?.id || null, n = base?.version || 0) {
    if (locked || !caps?.editable) return;
    const problem =
      timelineProblem(doc) ||
      (doc.clips.find(
        (c) =>
          durations[c.asset_id + ':' + c.asset_revision] &&
          c.out_ms > durations[c.asset_id + ':' + c.asset_revision],
      ) &&
        uiText('有镜头出点超过已读取的原片时长，请核对裁点。'));
    if (problem) {
      setError(problem);
      return;
    }
    const id = timelineId || undefined;
    const i: Operation = { kind: 'save', id, version: n, key: crypto.randomUUID() };
    void work(uiText('保存'), async () => {
      remember(i);
      try {
        accept(await timelineApi.save(project, id, n, doc, i.key!), i);
      } catch (e) {
        if (e instanceof ApiError && !e.uncertain) {
          clear(i);
          if (e.code === 'VIDEO_TIMELINE_VERSION_CONFLICT' && id && alive.current) {
            setConflict(true);
            void list();
            void versions(id);
          }
        }
        throw e;
      }
    });
  }
  function recover() {
    const i = pending;
    if (!i || busy) return;
    void work(uiText('核对'), async () => {
      if (i.kind === 'save') {
        accept(await timelineApi.receipt(project, i.key!), i);
        return;
      }
      const v = await fixed(i.id!, i.version);
      clear(i);
      if (!alive.current) return;
      if (!dirty) install(v);
      setStatus(uiText('已只读核对原版本 v') + v.version + uiText('。需要成片时可再次明确导出。'));
    });
  }
  function exportFixed() {
    if (!base || locked || !caps?.export_enabled) return;
    const original = base,
      i: Operation = { kind: 'export', id: base.id, version: base.version, key: crypto.randomUUID() };
    void work('export-video', async () => {
      const c = new AbortController();
      cancel.current = c;
      remember(i);
      try {
        const out = await exportVideo(original, AbortSignal.any([c.signal, AbortSignal.timeout(180000)]));
        if (!alive.current || c.signal.aborted) return;
        download(out.blob, original.document.title, original.version);
        clear(i);
        setStatus(
          uiText('已下载 v') +
            original.version +
            uiText(' · 实测 ') +
            out.duration +
            uiText(' 毫秒 · SHA-256 ') +
            out.sha.slice(0, 12),
        );
      } catch (e) {
        if (e instanceof ApiError && !e.uncertain) clear(i);
        throw c.signal.aborted ? Error(uiText('已取消本次下载。可只读核对原版本。')) : e;
      } finally {
        if (cancel.current === c) cancel.current = null;
      }
    });
  }
  function compare() {
    if (!base || latest <= base.version) return;
    const id = base.id;
    void work(uiText('比较'), async () => {
      const v = await fixed(id, latest);
      if (alive.current) {
        setComparison(v);
        setRestoring(false);
      }
    });
  }
  const source = base?.document.clips.find(
      (c) => c.id === clip?.id && c.asset_id === clip?.asset_id && c.asset_revision === clip?.asset_revision,
    ),
    src = !clip
      ? ''
      : source
        ? source.asset_available === true
          ? sourceURL(base!, source)
          : ''
        : clip.asset_available === true
          ? libraryURL(project, clip.asset_id)
          : '';
  if (!enabled)
    return (
      <section className="vt-work">
        <h1>{uiText("视频剪辑")}</h1>
        <p>{uiText("项目剪辑稿和私有视频需要 PostgreSQL 与账户登录。")}</p>
      </section>
    );
  return (
    <section className="vt-work">
      <header className="vt-head">
        <div>
          <h1>{uiText("视频剪辑")}</h1>
          <p>{uiText("选用项目内已获准的视频，调整裁点、镜头顺序，再导出固定版本。")}</p>
        </div>
        <button
          className="button secondary"
          disabled={locked || !caps?.editable}
          onClick={() => choose('', 0)}
        >{uiText("新建剪辑稿")}</button>
      </header>
      <div className="vt-catalog">
        <label>{uiText("项目剪辑稿")}<select
            aria-label={uiText("项目剪辑稿")}
            value={base?.id || ''}
            disabled={locked}
            onChange={(e) => {
              const id = e.target.value;
              if (!id) choose('', 0);
              else {
                const entry = catalog.find((r) => r.id === id);
                if (entry) choose(entry.id, entry.version);
              }
            }}
          >
            <option value="">{uiText("新剪辑稿")}</option>
            {catalog.map((r) => (
              <option key={r.id} value={r.id}>
                {r.title} · v{r.version}
              </option>
            ))}
          </select>
        </label>
        {cursor && <button onClick={() => void list(true)}>{uiText("更多剪辑稿")}</button>}
        {base && (
          <label>{uiText("固定版本")}<select
              aria-label={uiText("剪辑固定版本")}
              value={base.version}
              disabled={locked}
              onChange={(e) => choose(base.id, Number(e.target.value))}
            >
              {[...new Set([base.version, ...history.map((v) => v.version)])]
                .sort((a, b) => b - a)
                .map((n) => (
                  <option key={n} value={n}>
                    v{n}
                  </option>
                ))}
            </select>
          </label>
        )}
        {base && before > 0 && <button onClick={() => void versions(base.id, true)}>{uiText("更早版本")}</button>}
        <button
          onClick={() => {
            void list();
            if (base) void versions(base.id);
          }}
        >{uiText("刷新剪辑目录")}</button>
      </div>
      {error && (
        <p className="vt-notice" role="alert">
          {error}
        </p>
      )}
      {status && (
        <p className="vt-status" role="status">
          {status}
        </p>
      )}
      {pending && !busy && (
        <div className="vt-notice">
          {pending.kind === 'save' ? uiText('保存回执待核对。') : uiText('上次成片下载尚未确认。')}
          <button onClick={recover}>
            {pending.kind === 'save' ? uiText('只读核对原保存回执') : uiText('只读核对原导出版本')}
          </button>
        </div>
      )}
      {swap && (
        <div className="vt-notice">{uiText("切换将放弃本页未保存的剪辑。")}<button onClick={() => setSwap(null)}>{uiText("保留本页剪辑")}</button>
          <button onClick={() => open(swap.id, swap.version)}>{uiText("放弃剪辑并切换")}</button>
        </div>
      )}
      {conflict && (
        <div className="vt-notice">{uiText("服务器版本已变化，本页输入仍保留。")}<button disabled={locked} onClick={() => save(null, 0)}>{uiText("将剪辑另存新稿")}</button>
          {base && latest > base.version && (
            <button disabled={locked} onClick={() => choose(base.id, latest)}>{uiText("读取最新版")}</button>
          )}
        </div>
      )}
      <label className="vt-name">{uiText("剪辑稿名称")}<input
          aria-label={uiText("剪辑稿名称")}
          value={doc.title}
          disabled={locked || !caps?.editable}
          onChange={(e) => edit({ ...doc, title: e.target.value })}
          placeholder={uiText("为这段成片命名")}
        />
      </label>
      <div className="vt-grid">
        <aside className="vt-reel">
          <div className="vt-row">
            <h2>{uiText("镜头")}</h2>
            <small>{doc.clips.length} / 8</small>
          </div>
          <ol>
            {doc.clips.map((c, i) => (
              <li key={c.id}>
                <button
                  aria-pressed={idx === i}
                  onClick={() => {
                    setAuto(false);
                    setIdx(i);
                  }}
                >
                  <b>{String(i + 1).padStart(2, '0')}</b>
                  <span>
                    {c.title || uiText('未命名镜头')}
                    <small>{seconds(c.out_ms - c.in_ms)}</small>
                  </span>
                </button>
              </li>
            ))}
          </ol>
          <button
            disabled={locked || !caps?.editable || doc.clips.length >= 8}
            onClick={() => {
              setPicking((v) => !v);
              if (!picking) void gallery();
            }}
          >{uiText("添加项目视频")}</button>
        </aside>
        <div>
          {picking && (
            <section className="vt-picks">
              <h2>{uiText("项目视频素材")}</h2>
              {assets.map((a) => (
                <button key={a.id} data-video-asset={a.id} disabled={locked} onClick={() => add(a)}>
                  {a.name} <small>r{a.revision}</small>
                </button>
              ))}
              {!assets.length && <p>{uiText("暂无可选的已交付 MP4。")}</p>}
              {assetCursor && <button onClick={() => void gallery(true)}>{uiText("更多素材")}</button>}
            </section>
          )}
          {clip && visible ? (
            <TimelinePlayer
              key={clip.id + src}
              clip={clip}
              src={src}
              auto={auto}
              advance={() => {
                if (idx < doc.clips.length - 1) setIdx(idx + 1);
                else setAuto(false);
              }}
              loaded={(ms) => {
                const k = clip.asset_id + ':' + clip.asset_revision;
                setDurations((d) => (d[k] === ms ? d : { ...d, [k]: ms }));
              }}
            />
          ) : (
            <div className="vt-empty">
              <h2>{clip ? uiText('预览已暂停') : uiText('添加第一个镜头')}</h2>
              <p>{uiText("从当前项目选择已经审核可用的视频原片。")}</p>
            </div>
          )}
        </div>
      </div>
      {clip && (
        <fieldset className="vt-editor" disabled={locked || !caps?.editable}>
          <div className="vt-row">
            <h2>{uiText("编辑第 {0} 镜", idx + 1)}</h2>
            <div>
              <button disabled={!idx} onClick={() => move(-1)}>{uiText("上移镜头")}</button>
              <button disabled={idx >= doc.clips.length - 1} onClick={() => move(1)}>{uiText("下移镜头")}</button>
              <button
                onClick={() => {
                  edit({ ...doc, clips: doc.clips.filter((_, i) => i !== idx) });
                  setIdx(Math.max(0, Math.min(idx, doc.clips.length - 2)));
                }}
              >{uiText("移除此镜头")}</button>
            </div>
          </div>
          <div className="vt-form">
            <label>{uiText("镜头名称")}<input
                aria-label={uiText("镜头名称")}
                value={clip.title}
                onChange={(e) => patch({ title: e.target.value })}
              />
            </label>
            {(['in_ms', 'out_ms'] as const).map((k, i) => (
              <label key={k}>
                {i ? uiText('出点') : uiText('入点')}{uiText("（毫秒）")}<input
                  type="number"
                  aria-label={i ? uiText('出点毫秒') : uiText('入点毫秒')}
                  step="1"
                  min="0"
                  max="300000"
                  value={Number.isFinite(clip[k]) ? clip[k] : ''}
                  onChange={(e) => patch({ [k]: e.target.value === '' ? NaN : Number(e.target.value) })}
                />
              </label>
            ))}
          </div>
          <small>{uiText("原素材")} {clip.asset_id} · r{clip.asset_revision}
          </small>
        </fieldset>
      )}
      <section className="vt-sequence">
        <div className="vt-row">
          <h2>{uiText("成片顺序")}</h2>
          <span>{seconds(lengthMs(doc))} {uiText("/ 30 秒")}</span>
        </div>
        <div className="vt-track">
          {doc.clips.map((c, i) => (
            <button
              key={c.id}
              aria-label={uiText('定位第 ') + (i + 1) + uiText(' 镜')}
              aria-pressed={idx === i}
              style={{ flexGrow: Math.max(1, c.out_ms - c.in_ms || 1) }}
              onClick={() => {
                setAuto(false);
                setIdx(i);
              }}
            >
              {i + 1}
              <small>{c.title}</small>
            </button>
          ))}
        </div>
        {doc.clips.length > 0 && (
          <button
            disabled={locked || !!timelineProblem(doc)}
            onClick={() => {
              if (auto) setAuto(false);
              else {
                setIdx(0);
                setAuto(true);
              }
            }}
          >
            {auto ? uiText('停止顺序预览') : uiText('顺序预览')}
          </button>
        )}
      </section>
      <footer className="vt-actions">
        <div>
          <strong>{dirty ? uiText('本页剪辑未保存') : base ? uiText('固定版本 v') + base.version : uiText('新剪辑稿')}</strong>
          {base && dirty && <small>{uiText("导出使用已保存的 v")}{base.version}。</small>}
          {caps && !caps.export_enabled && <small>{uiText("本机合成工作线程未启用，可继续保存剪辑稿。")}</small>}
        </div>
        <button
          className="button primary"
          disabled={locked || !caps?.editable || !dirty}
          onClick={() => save()}
        >{uiText("保存剪辑版本")}</button>
        {base && latest > base.version && (
          <button disabled={locked} onClick={compare}>{uiText("与最新版比较")}</button>
        )}
        {base && (
          <button disabled={locked || !caps?.export_enabled} onClick={exportFixed}>{uiText("导出 MP4")}</button>
        )}
        {busy === 'export-video' && <button onClick={() => cancel.current?.abort()}>{uiText("取消本次导出")}</button>}
      </footer>
      {comparison && base && (
        <section className="vt-compare">
          <h2>
            v{base.version} {uiText("与 v")}{comparison.version}
          </h2>
          <ul>
            {changes(base.document, comparison.document).map((s, i) => (
              <li key={i}>{s}</li>
            ))}
          </ul>
          {!changes(base.document, comparison.document).length && <p>{uiText("镜头内容和顺序一致。")}</p>}
          {!dirty && !restoring && (
            <button disabled={locked} onClick={() => setRestoring(true)}>{uiText("将此版恢复为新版本")}</button>
          )}
          {restoring && (
            <div className="vt-notice">{uiText("将以 v")}{base.version} {uiText("的镜头另存 v")}{comparison.version + 1}{uiText("，旧版本保留。")}<button onClick={() => setRestoring(false)}>{uiText("取消恢复")}</button>
              <button disabled={locked} onClick={() => save(base.id, comparison.version)}>{uiText("确认保存恢复版本")}</button>
            </div>
          )}
        </section>
      )}
    </section>
  );
}
