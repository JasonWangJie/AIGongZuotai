import { uiText } from './locale';
import { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { api, ApiError } from './api';
import { defaultLibraryFilters } from './libraryState';
import type { AssetEntry } from './libraryState';
import {
  blank,
  colors,
  deckApi,
  deckProblem,
  freshSlide,
  layouts,
  outlineDocument,
  readTicket,
  sameVersion,
  saveSlot,
  summaryMatches,
  themes,
} from './presentations';
import type { Deck, DeckSummary, DeckVersion, SaveTicket, Slide } from './presentations';
import {
  downloadBlob,
  filename,
  loadDelivery,
  nativePptx,
  offlineHTML,
  pageStyle,
  slideMarkup,
  slideSizing,
} from './presentationExport';
import type { Delivery } from './presentationExport';
import './presentation.css';

type Props = {
  owner: string;
  project: string;
  enabled: boolean;
  visible: boolean;
  onDirty: (v: boolean) => void;
  onExpired: () => void;
};
function Preview({ doc, s, index, project }: { doc: Deck; s: Slide; index: number; project: string }) {
  const c = colors[doc.theme],
    size = slideSizing(s);
  return (
    <div
      className={'pd-sheet ' + (s.layout === 'title' ? 'cover' : s.layout === 'image' ? 'pic' : '')}
      style={
        {
          '--sheet': '#' + c.paper,
          '--ink': '#' + c.ink,
          '--sub': '#' + c.sub,
          '--accent': '#' + c.accent,
          '--heading': size.titleSize / 9.6 + 'cqw',
          '--copy': size.bodySize / 9.6 + 'cqw',
        } as React.CSSProperties
      }
    >
      <i className="pd-accent" />
      <h2 className="pd-title">{s.title || uiText('页面标题')}</h2>
      <p className="pd-body">{s.body}</p>
      {s.layout === 'image' &&
        (s.asset_id && s.asset_available === true ? (
          <img
            src={
              '/api/v1/library/' +
              encodeURIComponent(s.asset_id) +
              '/thumbnail?project_id=' +
              encodeURIComponent(project)
            }
            alt={uiText("当前页项目配图")}
          />
        ) : (
          <span className="pd-placeholder">{s.asset_id ? uiText('图片不可用') : uiText('选择项目图片')}</span>
        ))}
      <div className="pd-mark">
        {index + 1} / {doc.slides.length}
      </div>
    </div>
  );
}
function DeckShow({ delivery, close }: { delivery: Delivery; close: () => void }) {
  const [index, setIndex] = useState(0),
    items = delivery.version.document.slides,
    previous = useRef(document.activeElement);
  const dialog = useRef<HTMLDialogElement>(null);
  const turn = (delta: number) => setIndex((v) => Math.max(0, Math.min(items.length - 1, v + delta)));
  useEffect(() => {
    dialog.current?.showModal?.();
    return () => {
      if (previous.current instanceof HTMLElement) previous.current.focus();
    };
  }, []);
  return createPortal(
    <>
      <style>{'@page { size: 13.33333in 7.5in; margin: 0; }'}</style>
      <dialog
        className="pd-show"
        ref={dialog}
        onCancel={(e) => {
          e.preventDefault();
          close();
        }}
        onKeyDown={(e) => {
          if (e.key === 'ArrowRight') {
            e.preventDefault();
            turn(1);
          }
          if (e.key === 'ArrowLeft') {
            e.preventDefault();
            turn(-1);
          }
        }}
        aria-label={uiText("文稿放映")}
      >
        <div className="pd-stage">
          <div
            className="pd-current"
            dangerouslySetInnerHTML={{
              __html: slideMarkup(delivery.version, items[index], index, delivery.images[items[index].id]),
            }}
          />
        </div>
        <div className="pd-show-bar">
          <strong>
            {delivery.version.document.title} · v{delivery.version.version}
          </strong>
          <button disabled={!index} onClick={() => turn(-1)}>{uiText("上一页")}</button>
          <span>
            {index + 1} / {items.length}
          </span>
          <button disabled={index === items.length - 1} onClick={() => turn(1)}>{uiText("下一页")}</button>
          <button onClick={() => window.print()}>{uiText("打印或另存为 PDF")}</button>
          <button onClick={close}>{uiText("结束放映")}</button>
        </div>
      </dialog>
      <div
        className="pd-print"
        dangerouslySetInnerHTML={{
          __html: items.map((s, i) => slideMarkup(delivery.version, s, i, delivery.images[s.id])).join(''),
        }}
      />
    </>,
    document.body,
  );
}
export function PresentationWorkbench({ owner, project, enabled, visible, onDirty, onExpired }: Props) {
  const [doc, setDoc] = useState<Deck>(blank),
    [base, setBase] = useState<DeckVersion | null>(null),
    [idx, selectPage] = useState(0),
    [dirty, setDirty] = useState(false);
  const [material, setMaterial] = useState(''),
    [sourceOpen, setSourceOpen] = useState(false),
    [catalog, setCatalog] = useState<DeckSummary[]>([]),
    [cursor, setCursor] = useState(''),
    [versions, setVersions] = useState<DeckSummary[]>([]),
    [before, setBefore] = useState(0);
  const [assets, setAssets] = useState<AssetEntry[]>([]),
    [assetCursor, setAssetCursor] = useState(''),
    [picking, setPicking] = useState(false);
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(''),
    [message, setMessage] = useState(''),
    [conflict, setConflict] = useState(false),
    [pending, setPending] = useState<SaveTicket | null>(null),
    [hold, setHold] = useState(false);
  const [swap, setSwap] = useState<{ id: string; version: number } | null>(null),
    [discardOutline, setDiscardOutline] = useState(false),
    [show, setShow] = useState<Delivery | null>(null);
  const mounted = useRef(true),
    lock = useRef(false),
    controller = useRef<AbortController | null>(null),
    loadTickets = useRef({ catalog: 0, versions: 0, assets: 0 }),
    slot = saveSlot(owner, project),
    page = doc.slides[idx];
  const current = useRef('');
  current.current = base?.id || '';
  const guarded = dirty || !!material.trim() || !!pending;
  useEffect(() => {
    mounted.current = true;
    try {
      setPending(readTicket(slot));
    } catch {
      setHold(true);
      setError(uiText('浏览器无法核对原保存回执。'));
    }
    return () => {
      mounted.current = false;
      controller.current?.abort();
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
  function issue(e: unknown) {
    if (!mounted.current) return;
    if (e instanceof ApiError && e.status === 401) onExpired();
    setError(e instanceof Error ? e.message : uiText('请求未能完成。'));
  }
  async function catalogRead(more = false) {
    const t = ++loadTickets.current.catalog;
    try {
      const v = await deckApi.list(project, more ? cursor : undefined);
      if (!mounted.current || t !== loadTickets.current.catalog) return;
      if (!Array.isArray(v.items) || !v.items.every((r) => summaryMatches(r, project)))
        throw Error(uiText('项目文稿列表范围无效。'));
      setCatalog((old) => [...new Map([...(more ? old : []), ...v.items].map((i) => [i.id, i])).values()]);
      setCursor(v.next_cursor || '');
    } catch (e) {
      if (t === loadTickets.current.catalog) issue(e);
    }
  }
  async function history(id: string, more = false) {
    const t = ++loadTickets.current.versions;
    try {
      const v = await deckApi.versions(project, id, more ? before : undefined);
      if (!mounted.current || t !== loadTickets.current.versions || current.current !== id) return;
      if (!Array.isArray(v.items) || !v.items.every((r) => summaryMatches(r, project) && r.id === id))
        throw Error(uiText('版本目录与选中文稿不符。'));
      setVersions((old) => [
        ...new Map([...(more ? old : []), ...v.items].map((i) => [i.version, i])).values(),
      ]);
      setBefore(v.next_before_version || 0);
    } catch (e) {
      if (t === loadTickets.current.versions) issue(e);
    }
  }
  useEffect(() => {
    if (enabled && visible) {
      void catalogRead();
      if (base) void history(base.id);
    }
  }, [enabled, visible, base?.id, base?.version]);
  async function gallery(more = false) {
    const t = ++loadTickets.current.assets;
    try {
      const v = await api.library(
        project,
        { ...defaultLibraryFilters(), kind: 'image' },
        more ? assetCursor : undefined,
      );
      if (!mounted.current || t !== loadTickets.current.assets) return;
      const images = v.items.filter(
        (a) =>
          a.project_id === project &&
          a.kind === 'image' &&
          a.state === 'active' &&
          a.content_url &&
          (!a.content_type || ['image/png', 'image/jpeg'].includes(a.content_type)),
      );
      setAssets((old) => [...new Map([...(more ? old : []), ...images].map((i) => [i.id, i])).values()]);
      setAssetCursor(v.next_cursor || '');
    } catch (e) {
      if (t === loadTickets.current.assets) issue(e);
    }
  }
  function edit(next: Deck) {
    setDoc(next);
    setDirty(true);
    setMessage('');
  }
  function amend(value: Partial<Slide>) {
    edit({ ...doc, slides: doc.slides.map((s, i) => (i === idx ? { ...s, ...value } : s)) });
  }
  function move(delta: number) {
    const target = idx + delta;
    if (target < 0 || target >= doc.slides.length) return;
    const slides = [...doc.slides];
    [slides[idx], slides[target]] = [slides[target], slides[idx]];
    edit({ ...doc, slides });
    selectPage(target);
  }
  function install(v: DeckVersion) {
    setBase(v);
    setDoc(v.document);
    setDirty(false);
    selectPage(0);
    setPicking(false);
    setConflict(false);
    setSourceOpen(false);
    setMaterial('');
  }
  async function withLock(work: () => Promise<void>) {
    if (lock.current) return;
    lock.current = true;
    setBusy(true);
    setError('');
    setMessage('');
    try {
      await work();
    } catch (e) {
      issue(e);
    } finally {
      lock.current = false;
      if (mounted.current) setBusy(false);
    }
  }
  function open(id: string, n: number) {
    setSwap(null);
    if (!id) {
      setBase(null);
      setDoc(blank());
      selectPage(0);
      setMaterial('');
      setVersions([]);
      setDirty(false);
      setConflict(false);
      setPicking(false);
      setError('');
      setMessage('');
      return;
    }
    void withLock(async () => {
      const v = await deckApi.version(project, id, n);
      if (!mounted.current) return;
      if (!sameVersion(v, project, id, n)) throw Error(uiText('读取的版本与所选项目文稿不符。'));
      install(v);
    });
  }
  function choose(id: string, n: number) {
    if (busy || pending || hold) return;
    if (dirty || material.trim()) setSwap({ id, version: n });
    else open(id, n);
  }
  async function accept(v: DeckVersion, t: SaveTicket) {
    if (!sameVersion(v, project, t.id, t.expected + 1))
      throw Error(uiText('保存回执与原文稿版本不符，请核对原收据。'));
    if (readTicket(slot)?.key === t.key) sessionStorage.removeItem(slot);
    if (!mounted.current) return;
    install(v);
    setPending(null);
    setMessage(uiText('已保存固定版本 v') + v.version);
    await catalogRead();
  }
  function save(asNew = false) {
    if (pending || hold || busy) return;
    const validation = deckProblem(doc);
    if (validation) {
      setError(validation);
      return;
    }
    const t = {
      key: crypto.randomUUID(),
      id: asNew ? undefined : base?.id,
      expected: asNew ? 0 : base?.version || 0,
    };
    void withLock(async () => {
      try {
        sessionStorage.setItem(slot, JSON.stringify(t));
      } catch {
        throw Error(uiText('浏览器无法保留保存收据编号。'));
      }
      setPending(t);
      try {
        const v = await deckApi.save(project, t.id, t.expected, doc, t.key);
        await accept(v, t);
      } catch (e) {
        if (e instanceof ApiError && !e.uncertain) {
          if (readTicket(slot)?.key === t.key) sessionStorage.removeItem(slot);
          if (mounted.current) {
            setPending(null);
            if (e.code === 'PRESENTATION_VERSION_CONFLICT' && t.id) {
              setConflict(true);
              void catalogRead();
              void history(t.id);
            }
          }
        }
        throw e;
      }
    });
  }
  function receipt() {
    const t = pending;
    if (!t || hold) return;
    void withLock(async () => accept(await deckApi.receipt(project, t.key), t));
  }
  function derive() {
    try {
      const next = outlineDocument(material, doc.theme, doc.title);
      edit(next);
      selectPage(0);
      setSourceOpen(false);
      setMaterial('');
      setDiscardOutline(false);
      setError('');
    } catch (e) {
      issue(e);
      setDiscardOutline(false);
    }
  }
  function delivery(mode: 'pptx' | 'html' | 'show') {
    if (!base) return;
    const original = base;
    void withLock(async () => {
      const c = new AbortController();
      controller.current = c;
      try {
        const data = await loadDelivery(project, original.id, original.version, c.signal);
        if (!mounted.current || c.signal.aborted) return;
        if (mode === 'show') {
          setShow(data);
          return;
        }
        const blob =
          mode === 'pptx'
            ? await nativePptx(data, c.signal)
            : new Blob([offlineHTML(data)], { type: 'text/html;charset=utf-8' });
        if (!mounted.current || c.signal.aborted) return;
        downloadBlob(blob, filename(original, mode));
        setMessage(uiText('已导出固定版本 v') + original.version);
      } finally {
        if (controller.current === c) controller.current = null;
      }
    });
  }
  const locked = busy || !!pending || hold,
    selected = base ? base.id + ':' + base.version : '',
    thumb = (a: string) =>
      '/api/v1/library/' + encodeURIComponent(a) + '/thumbnail?project_id=' + encodeURIComponent(project);
  if (!enabled)
    return (
      <section className="pd-work">
        <h1>{uiText("演示文稿")}</h1>
        <p>{uiText("项目文稿、私有配图和固定版本导出需要启用 PostgreSQL 与账户登录。")}</p>
      </section>
    );
  return (
    <section className="pd-work">
      <style>{pageStyle}</style>
      <header className="pd-head">
        <div>
          <h1>{uiText("演示文稿")}</h1>
          <p>{uiText("把材料分成页面，用项目配图讲清每一个要点。")}</p>
        </div>
        <button className="button secondary" disabled={locked} onClick={() => choose('', 0)}>{uiText("新建文稿")}</button>
      </header>
      <div className="pd-library">
        <label>{uiText("项目文稿")}<select
            aria-label={uiText("项目文稿")}
            disabled={locked}
            value={base?.id || ''}
            onChange={(e) => {
              const s = catalog.find((v) => v.id === e.target.value);
              if (s) choose(s.id, s.version);
            }}
          >
            <option value="">{uiText("新文稿")}</option>
            {catalog.map((v) => (
              <option key={v.id} value={v.id}>
                {v.title} · v{v.version}
              </option>
            ))}
            {base && !catalog.some((v) => v.id === base.id) && (
              <option value={base.id}>{base.document.title}</option>
            )}
          </select>
        </label>
        {cursor && <button onClick={() => void catalogRead(true)}>{uiText("更多文稿")}</button>}
        {base && (
          <label>{uiText("固定版本")}<select
              aria-label={uiText("固定版本")}
              value={selected}
              disabled={locked}
              onChange={(e) => choose(base.id, Number(e.target.value.split(':').pop()))}
            >
              {[...new Set([base.version, ...versions.map((v) => v.version)])]
                .sort((a, b) => b - a)
                .map((n) => (
                  <option key={n} value={base.id + ':' + n}>
                    v{n}
                  </option>
                ))}
            </select>
          </label>
        )}
        {before > 0 && base && <button onClick={() => void history(base.id, true)}>{uiText("更早版本")}</button>}
        <button
          type="button"
          onClick={() => {
            void catalogRead();
            if (base) void history(base.id);
          }}
        >{uiText("刷新目录")}</button>
      </div>
      {error && (
        <p className="pd-alert" role="alert">
          {error}
        </p>
      )}
      {message && (
        <p className="pd-status" role="status">
          {message}
        </p>
      )}
      {pending && (
        <div className="pd-alert" role="status">{uiText("保存回执待核对。")}<button disabled={busy} onClick={receipt}>{uiText("只读核对原保存回执")}</button>
        </div>
      )}
      {conflict && (
        <div className="pd-alert">{uiText("服务器版本已变化，当前编辑保留。")}<button disabled={locked} onClick={() => save(true)}>{uiText("将当前编辑另存为新文稿")}</button>
        </div>
      )}
      {swap && (
        <div className="pd-alert" role="alert">{uiText("切换将丢弃当前页面的未保存编辑。")}<button onClick={() => setSwap(null)}>{uiText("保留当前编辑")}</button>
          <button onClick={() => open(swap.id, swap.version)}>{uiText("放弃编辑并切换")}</button>
        </div>
      )}
      <div className="pd-controls">
        <label>{uiText("文稿名称")}<input
            aria-label={uiText("文稿名称")}
            value={doc.title}
            disabled={locked}
            onChange={(e) => edit({ ...doc, title: e.target.value })}
            placeholder={uiText("为这份演示命名")}
          />
        </label>
        <label>{uiText("主题")}<select
            aria-label={uiText("演示主题")}
            value={doc.theme}
            disabled={locked}
            onChange={(e) => edit({ ...doc, theme: e.target.value as Deck['theme'] })}
          >
            {Object.entries(themes).map(([v, label]) => (
              <option value={v} key={v}>
                {label}
              </option>
            ))}
          </select>
        </label>
        <button disabled={locked} onClick={() => setSourceOpen((o) => !o)} aria-expanded={sourceOpen}>{uiText("从材料生成大纲")}</button>
      </div>
      {sourceOpen && (
        <div className="pd-source">
          <label>{uiText("材料或大纲")}<textarea
              aria-label={uiText("材料或大纲")}
              rows={5}
              value={material}
              disabled={locked}
              onChange={(e) => setMaterial(e.target.value)}
              placeholder={uiText("每页以空行或 Markdown 标题分隔。段落首行为页标题，其余文字为正文。")}
            />
          </label>
          <small>{uiText("按段落在本机拆页。原材料仅留在当前页面。")}</small>
          <button
            disabled={locked || !material.trim()}
            onClick={() =>
              doc.slides.some((s) => s.title || s.body || s.notes || s.asset_id)
                ? setDiscardOutline(true)
                : derive()
            }
          >{uiText("生成可编辑页面")}</button>
          {discardOutline && (
            <div className="pd-alert">{uiText("将替换当前页面内容。")}<button onClick={() => setDiscardOutline(false)}>{uiText("保留页面")}</button>
              <button onClick={derive}>{uiText("替换为新大纲")}</button>
            </div>
          )}
        </div>
      )}
      <div className="pd-editor">
        <aside className="pd-rail">
          <div>
            <h2>{uiText("页面")}</h2>
            <span>{doc.slides.length} / 30</span>
          </div>
          <ol>
            {doc.slides.map((s, i) => (
              <li key={s.id}>
                <button
                  type="button"
                  aria-pressed={i === idx}
                  onClick={() => {
                    selectPage(i);
                    setPicking(false);
                  }}
                >
                  <span>{String(i + 1).padStart(2, '0')}</span>
                  {s.title || uiText('未命名页')}
                </button>
              </li>
            ))}
          </ol>
          <button
            disabled={locked || doc.slides.length >= 30}
            onClick={() => {
              edit({ ...doc, slides: [...doc.slides, freshSlide()] });
              selectPage(doc.slides.length);
              setPicking(false);
            }}
          >{uiText("添加页面")}</button>
        </aside>
        <div className="pd-board">
          <Preview doc={doc} s={page} index={idx} project={project} />
        </div>
      </div>
      <fieldset disabled={locked} className="pd-fields">
        <div className="pd-fieldhead">
          <h2>{uiText("编辑第")} {idx + 1} {uiText("页")}</h2>
          <div>
            <button disabled={!idx} onClick={() => move(-1)}>{uiText("上移页面")}</button>
            <button disabled={idx === doc.slides.length - 1} onClick={() => move(1)}>{uiText("下移页面")}</button>
            <button
              disabled={doc.slides.length <= 1}
              onClick={() => {
                const slides = doc.slides.filter((_, i) => i !== idx);
                edit({ ...doc, slides });
                selectPage(Math.min(idx, slides.length - 1));
                setPicking(false);
              }}
            >{uiText("删除此页")}</button>
          </div>
        </div>
        <div className="pd-titleline">
          <label>{uiText("页标题")}<input
              aria-label={uiText("页标题")}
              value={page.title}
              onChange={(e) => amend({ title: e.target.value })}
            />
          </label>
          <label>{uiText("页面版式")}<select
              aria-label={uiText("页面版式")}
              value={page.layout}
              onChange={(e) => {
                const layout = e.target.value as Slide['layout'];
                amend({
                  layout,
                  ...(layout === 'image'
                    ? {}
                    : {
                        asset_id: undefined,
                        asset_revision: undefined,
                        content_version: undefined,
                        asset_available: undefined,
                      }),
                });
                setPicking(false);
              }}
            >
              {Object.entries(layouts).map(([v, label]) => (
                <option key={v} value={v}>
                  {label}
                </option>
              ))}
            </select>
          </label>
        </div>
        {!slideSizing(page).fits && (
          <p className="pd-alert">{uiText("第")} {idx + 1} {uiText("页在当前版式中超出可读范围，可继续保存编辑；放映及导出前请拆页或将细节移入备注。")}</p>
        )}
        <div className="pd-copyfields">
          <label>{uiText("正文")}<textarea
              aria-label={uiText("页正文")}
              rows={4}
              value={page.body}
              onChange={(e) => amend({ body: e.target.value })}
            />
          </label>
          <label>{uiText("演讲备注")}<textarea
              aria-label={uiText("演讲备注")}
              rows={4}
              value={page.notes}
              onChange={(e) => amend({ notes: e.target.value })}
            />
          </label>
        </div>
        {page.layout === 'image' && (
          <div className="pd-image-picker">
            <button
              onClick={() => {
                setPicking((v) => !v);
                if (!picking) void gallery();
              }}
            >{uiText("选择项目图片")}</button>
            {page.asset_id && (
              <small>{uiText("已选图片 ·")} {page.asset_id} · r{page.asset_revision}
              </small>
            )}
            {picking && (
              <div className="pd-picks">
                {assets.map((a) => (
                  <button
                    key={a.id}
                    data-asset={a.id}
                    onClick={() => {
                      amend({
                        asset_id: a.id,
                        asset_revision: a.revision,
                        asset_available: true,
                        content_version: undefined,
                      });
                      setPicking(false);
                    }}
                  >
                    <img src={thumb(a.id)} alt="" />
                    <span>{a.name}</span>
                  </button>
                ))}
                {!assets.length && <p>{uiText("当前页没有可用的 PNG / JPEG 项目图片。")}</p>}
                {assetCursor && <button onClick={() => void gallery(true)}>{uiText("更多项目图片")}</button>}
              </div>
            )}
          </div>
        )}
      </fieldset>
      <footer className="pd-actions">
        <span>
          {dirty ? uiText('当前编辑未保存') : base ? uiText('当前固定版本 v') + base.version : uiText('新文稿')}
          {dirty && base ? uiText(' · 导出使用 v') + base.version : ''}
        </span>
        <button className="button primary" disabled={locked || (!!base && !dirty)} onClick={() => save()}>{uiText("保存固定版本")}</button>
        {base && (
          <>
            <button disabled={busy} onClick={() => delivery('show')}>{uiText("放映与打印")}</button>
            <button disabled={busy} onClick={() => delivery('html')}>{uiText("下载离线 HTML")}</button>
            <button disabled={busy} onClick={() => delivery('pptx')}>{uiText("下载 PowerPoint")}</button>
          </>
        )}
      </footer>
      {show && visible && <DeckShow delivery={show} close={() => setShow(null)} />}
    </section>
  );
}
