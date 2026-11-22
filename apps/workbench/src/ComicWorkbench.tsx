import { uiText } from './locale';
import { useEffect, useRef, useState } from 'react';
import { api } from './api';
import { defaultLibraryFilters } from './libraryState';
import type { AssetEntry } from './libraryState';
import { comicImagePath, newCard, newPanel, usableComicImage } from './comics';
import type { Card, Comic, Panel } from './comics';
import { pageStyle } from './presentationExport';
import { useComicEditor } from './useComicEditor';
import { PanelVideoWorkbench } from './PanelVideoWorkbench';
import type { ComicProps } from './useComicEditor';
import './comic.css';
function Cards({
  label,
  rows,
  used,
  change,
}: {
  label: string;
  rows: Card[];
  used: (id: string) => boolean;
  change: (next: Card[]) => void;
}) {
  return (
    <fieldset className="cb-cards">
      <legend>{label}{uiText("卡")}</legend>
      {rows.map((card, i) => (
        <div className="cb-card" key={card.id}>
          <label>
            {label}{uiText("名称")}<input
              aria-label={label + uiText('名称 ') + (i + 1)}
              value={card.name}
              placeholder={label === uiText('角色') ? uiText('角色姓名') : uiText('场景名称')}
              onChange={(e) =>
                change(rows.map((c) => (c.id === card.id ? { ...c, name: e.target.value } : c)))
              }
            />
          </label>
          <label>
            {label}{uiText("设定")}<textarea
              aria-label={label + uiText('设定 ') + (i + 1)}
              value={card.description}
              rows={2}
              onChange={(e) =>
                change(rows.map((c) => (c.id === card.id ? { ...c, description: e.target.value } : c)))
              }
            />
          </label>
          <button
            disabled={rows.length <= 1 || used(card.id)}
            title={used(card.id) ? uiText('先在镜头中解除引用') : ''}
            onClick={() => change(rows.filter((c) => c.id !== card.id))}
          >{uiText("移除")}{label}{uiText("卡")}</button>
        </div>
      ))}
      <button disabled={rows.length >= 16} onClick={() => change([...rows, newCard()])}>{uiText("添加")}{label}{uiText("卡")}</button>
    </fieldset>
  );
}
function ImagePicker({
  project,
  panel,
  select,
  issue,
  locked,
}: {
  project: string;
  panel: Panel;
  select: (patch: Partial<Panel>) => void;
  issue: (e: unknown) => void;
  locked: boolean;
}) {
  const [open, setOpen] = useState(false),
    [items, setItems] = useState<AssetEntry[]>([]),
    [cursor, setCursor] = useState(''),
    [busy, setBusy] = useState(false);
  const seq = useRef(0);
  useEffect(
    () => () => {
      seq.current++;
    },
    [project, panel.id],
  );
  async function load(more = false) {
    const t = ++seq.current;
    setBusy(true);
    try {
      const r = await api.library(
        project,
        { ...defaultLibraryFilters(), kind: 'image' },
        more ? cursor : undefined,
      );
      if (t !== seq.current) return;
      const photos = r.items.filter((a) => usableComicImage(a, project));
      setItems((old) => [...new Map([...(more ? old : []), ...photos].map((a) => [a.id, a])).values()]);
      setCursor(r.next_cursor || '');
    } catch (e) {
      if (t === seq.current) issue(e);
    } finally {
      if (t === seq.current) setBusy(false);
    }
  }
  return (
    <div className="cb-picture">
      <button
        disabled={locked}
        onClick={() => {
          setOpen(!open);
          if (!open) void load();
        }}
      >
        {panel.asset_id ? uiText('更换镜头配图') : uiText('选择项目图片')}
      </button>
      {panel.asset_id && (
        <button
          disabled={locked}
          onClick={() =>
            select({
              asset_id: undefined,
              asset_revision: undefined,
              asset_available: undefined,
              content_version: undefined,
            })
          }
        >{uiText("移除配图")}</button>
      )}
      {open && (
        <div className="cb-library" aria-label={uiText("可授权项目图片")}>
          {items.map((a) => (
            <button
              disabled={locked}
              key={a.id}
              data-comic-asset={a.id}
              onClick={() => {
                select({
                  asset_id: a.id,
                  asset_revision: a.revision,
                  asset_available: true,
                  content_version: undefined,
                });
                setOpen(false);
              }}
            >
              <img
                loading="lazy"
                alt=""
                src={
                  '/api/v1/library/' +
                  encodeURIComponent(a.id) +
                  '/thumbnail?project_id=' +
                  encodeURIComponent(project)
                }
              />
              <span>{a.name}</span>
            </button>
          ))}
          {!items.length && <small>{busy ? uiText('正在读取项目图片…') : uiText('暂无可用的项目 PNG 或 JPEG。')}</small>}
          {cursor && (
            <button disabled={busy} onClick={() => void load(true)}>{uiText("更多图片")}</button>
          )}
        </div>
      )}
    </div>
  );
}
function cardNames(doc: Comic, p: Panel) {
  return p.character_ids
    .map((id) => doc.characters.find((c) => c.id === id)?.name || uiText('未命名角色'))
    .join('、');
}
export function ComicWorkbench(props: ComicProps) {
  const e = useComicEditor(props),
    { doc, base, locked } = e;
  const [index, setIndex] = useState(0);
  const idx = Math.min(index, doc.panels.length - 1),
    panel = doc.panels[idx];
  const [tab, setTab] = useState<'panels' | 'cards'>('panels');
  function edit(value: Partial<Comic>) {
    e.edit({ ...doc, ...value });
  }
  function patch(value: Partial<Panel>) {
    edit({ panels: doc.panels.map((p, i) => (i === idx ? { ...p, ...value } : p)) });
  }
  function move(n: number) {
    const to = idx + n;
    if (to < 0 || to >= doc.panels.length) return;
    const p = [...doc.panels];
    [p[idx], p[to]] = [p[to], p[idx]];
    edit({ panels: p });
    setIndex(to);
  }
  const fixedPanel =
    base?.confirmed &&
    base.document.panels.find(
      (original) =>
        original.id === panel.id &&
        original.asset_id === panel.asset_id &&
        original.asset_revision === panel.asset_revision &&
        original.content_version === panel.content_version,
    );
  const previewURL =
    panel.asset_id && panel.asset_available
      ? fixedPanel
        ? comicImagePath(props.project, base!.version, panel.id)
        : '/api/v1/library/' +
          encodeURIComponent(panel.asset_id) +
          '/thumbnail?project_id=' +
          encodeURIComponent(props.project)
      : '';
  if (!props.enabled)
    return (
      <section className="cb-work">
        <h1>{uiText("漫剧分镜")}</h1>
        <p>{uiText("项目分镜本需要 PostgreSQL 和账户登录。")}</p>
      </section>
    );
  return (
    <section className="cb-work">
      <style>{pageStyle}</style>
      <header className="cb-head">
        <div>
          <h1>{uiText("漫剧分镜")}</h1>
          <p>{uiText("编排故事、角色和场景，确认版本后导出可编辑的分镜稿。")}</p>
        </div>
        <label>{uiText("分镜版本")}<select
            aria-label={uiText("分镜版本")}
            value={base?.version || 0}
            disabled={locked}
            onChange={(ev) => e.select(Number(ev.target.value))}
          >
            {!base && <option value={0}>{uiText("新分镜本")}</option>}
            {[...new Set([...(base ? [base.version] : []), ...e.rows.map((r) => r.version)])]
              .sort((a, b) => b - a)
              .map((n) => (
                <option key={n} value={n}>
                  v{n}
                  {e.rows.find((r) => r.version === n)?.confirmed || (base?.version === n && base.confirmed)
                    ? uiText(' · 已确认')
                    : ''}
                </option>
              ))}
          </select>
        </label>
        <button onClick={e.refresh}>{uiText("刷新版本")}</button>
        {e.before > 0 && <button onClick={e.more}>{uiText("更早版本")}</button>}
      </header>
      {e.error && (
        <p className="cb-alert" role="alert">
          {e.error}
        </p>
      )}
      {e.status && (
        <p className="cb-status" role="status">
          {e.status}
        </p>
      )}
      {e.pending && !e.busy && (
        <div className="cb-alert">
          {e.pending.operation === 'save' ? uiText('保存回执待核对。') : uiText('作者确认回执待核对。')}
          <button disabled={!e.ready} onClick={e.receipt}>
            {e.pending.operation === 'save' ? uiText('只读核对原保存回执') : uiText('只读核对原确认回执')}
          </button>
        </div>
      )}
      {e.choice > 0 && (
        <div className="cb-alert">{uiText("读取 v")}{e.choice} {uiText("会放弃本页修改。")}<button onClick={() => e.setChoice(0)}>{uiText("保留本页输入")}</button>
          <button onClick={() => e.open(e.choice)}>{uiText("放弃修改并读取")}</button>
        </div>
      )}
      {e.conflict && (
        <div className="cb-alert">{uiText("原版本状态已变化，本页输入仍保留。")}<button disabled={locked} onClick={e.reviewLatest}>{uiText("核对服务器最新版")}</button>
        </div>
      )}
      {e.merge && (
        <section className="cb-compare">
          <h2>{uiText("服务器 v")}{e.merge.version} · {e.merge.document.title}
          </h2>
          <p>{e.merge.document.panels.map((p) => p.title).join(' → ')}</p>
          <details>
            <summary>{uiText("阅读服务器上的故事、设定与镜头")}</summary>
            <p>{e.merge.document.story}</p>
            {[...e.merge.document.characters, ...e.merge.document.scenes].map((c, i) => (
              <p key={i}>
                <strong>{c.name}</strong> {c.description}
              </p>
            ))}
            <ol>
              {e.merge.document.panels.map((p) => (
                <li key={p.id}>
                  <strong>{p.title}</strong>
                  <p>{p.action}</p>
                  <p>{p.dialogue}</p>
                </li>
              ))}
            </ol>
          </details>
          <button disabled={locked} onClick={() => e.install(e.merge!)}>{uiText("采用服务器版本")}</button>
          {e.dirty && (
            <button disabled={locked} onClick={e.keepDraft}>{uiText("保留本页并基于此版编辑")}</button>
          )}
        </section>
      )}
      <fieldset className="cb-story" disabled={locked}>
        <label>{uiText("分镜本名称")}<input
            aria-label={uiText("分镜本名称")}
            value={doc.title}
            placeholder={uiText("为故事命名")}
            onChange={(ev) => edit({ title: ev.target.value })}
          />
        </label>
        <label>{uiText("故事梗概")}<textarea
            aria-label={uiText("故事梗概")}
            value={doc.story}
            rows={2}
            placeholder={uiText("这段故事从哪里开始，又如何结束？")}
            onChange={(ev) => edit({ story: ev.target.value })}
          />
        </label>
      </fieldset>
      <nav className="cb-tabs" aria-label={uiText("分镜编辑区域")}>
        <button aria-pressed={tab === 'panels'} onClick={() => setTab('panels')}>{uiText("镜头编排")}</button>
        <button aria-pressed={tab === 'cards'} onClick={() => setTab('cards')}>{uiText("角色与场景")}</button>
      </nav>
      {tab === 'cards' ? (
        <fieldset disabled={locked} className="cb-settings">
          <Cards
            label={uiText("角色")}
            rows={doc.characters}
            used={(id) => doc.panels.some((p) => p.character_ids.includes(id))}
            change={(characters) => edit({ characters })}
          />
          <Cards
            label={uiText("场景")}
            rows={doc.scenes}
            used={(id) => doc.panels.some((p) => p.scene_id === id)}
            change={(scenes) => edit({ scenes })}
          />
        </fieldset>
      ) : (
        <>
          <div className="cb-panels">
            <aside className="cb-rail">
              <div className="cb-line">
                <h2>{uiText("镜头顺序")}</h2>
                <small>{doc.panels.length} / 12</small>
              </div>
              <ol>
                {doc.panels.map((p, i) => (
                  <li key={p.id}>
                    <button
                      aria-label={uiText('打开镜头 ') + (i + 1)}
                      aria-pressed={idx === i}
                      onClick={() => setIndex(i)}
                    >
                      <b>{String(i + 1).padStart(2, '0')}</b>
                      <span>
                        {p.title || uiText('未命名镜头')}
                        <small>{doc.scenes.find((s) => s.id === p.scene_id)?.name || uiText('未命名场景')}</small>
                      </span>
                    </button>
                  </li>
                ))}
              </ol>
              <button
                disabled={locked || doc.panels.length >= 12}
                onClick={() => {
                  edit({ panels: [...doc.panels, newPanel(doc.scenes[0].id)] });
                  setIndex(doc.panels.length);
                }}
              >{uiText("添加镜头")}</button>
            </aside>
            <article className="cb-preview" aria-label={uiText("当前分镜预览")}>
              <div className="cb-panel-art">
                {previewURL ? (
                  <img src={previewURL} alt={uiText("当前镜头项目配图")} />
                ) : (
                  <p>{panel.asset_id ? uiText('原配图不可用') : uiText('此镜头使用文字分镜')}</p>
                )}
              </div>
              <div className="cb-caption">
                <small>{uiText("镜头")} {idx + 1} · {doc.scenes.find((c) => c.id === panel.scene_id)?.name || uiText('未命名场景')}
                </small>
                <h2>{panel.title || uiText('未命名镜头')}</h2>
                {cardNames(doc, panel) && <span>{cardNames(doc, panel)}</span>}
                {panel.action && <p>{panel.action}</p>}
                {panel.dialogue && <blockquote>{panel.dialogue}</blockquote>}
              </div>
            </article>
          </div>
          <fieldset className="cb-editor" disabled={locked}>
            <div className="cb-line">
              <h2>{uiText("编辑第 {0} 镜", idx + 1)}</h2>
              <div>
                <button disabled={idx === 0} onClick={() => move(-1)}>{uiText("上移镜头")}</button>
                <button disabled={idx === doc.panels.length - 1} onClick={() => move(1)}>{uiText("下移镜头")}</button>
                <button
                  disabled={doc.panels.length === 1}
                  onClick={() => {
                    edit({ panels: doc.panels.filter((_, i) => i !== idx) });
                    setIndex(Math.max(0, idx - 1));
                  }}
                >{uiText("移除此镜头")}</button>
              </div>
            </div>
            <div className="cb-fields">
              <label>{uiText("镜头标题")}<input
                  aria-label={uiText("镜头标题")}
                  value={panel.title}
                  onChange={(ev) => patch({ title: ev.target.value })}
                />
              </label>
              <label>{uiText("镜头场景")}<select
                  aria-label={uiText("镜头场景")}
                  value={panel.scene_id}
                  onChange={(ev) => patch({ scene_id: ev.target.value })}
                >
                  {doc.scenes.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.name || uiText('未命名场景')}
                    </option>
                  ))}
                </select>
              </label>
            </div>
            <fieldset className="cb-cast">
              <legend>{uiText("出场角色")}</legend>
              {doc.characters.map((c) => (
                <label key={c.id}>
                  <input
                    type="checkbox"
                    checked={panel.character_ids.includes(c.id)}
                    disabled={!panel.character_ids.includes(c.id) && panel.character_ids.length >= 8}
                    onChange={(ev) =>
                      patch({
                        character_ids: ev.target.checked
                          ? [...panel.character_ids, c.id]
                          : panel.character_ids.filter((id) => id !== c.id),
                      })
                    }
                  />
                  {c.name || uiText('未命名角色')}
                </label>
              ))}
            </fieldset>
            <div className="cb-fields">
              <label>{uiText("镜头动作")}<textarea
                  aria-label={uiText("镜头动作")}
                  rows={3}
                  value={panel.action}
                  onChange={(ev) => patch({ action: ev.target.value })}
                />
              </label>
              <label>{uiText("镜头对白")}<textarea
                  aria-label={uiText("镜头对白")}
                  rows={3}
                  value={panel.dialogue}
                  onChange={(ev) => patch({ dialogue: ev.target.value })}
                />
              </label>
            </div>
            <ImagePicker
              key={panel.id}
              project={props.project}
              panel={panel}
              select={patch}
              locked={locked}
              issue={e.issue}
            />
          </fieldset>
        </>
      )}
      {props.visible && tab === 'panels' && base?.confirmed && fixedPanel && !e.dirty && !locked &&
        props.bootstrap?.project.id === props.project && props.bootstrap.user?.id === props.owner &&
        <PanelVideoWorkbench key={JSON.stringify([props.owner, props.project, base.version, panel.id])}
          scope={{ project: props.project, version: base.version, panel: panel.id }}
          owner={props.owner} title={fixedPanel.title} bootstrap={props.bootstrap} onExpired={props.onExpired}/>}
      <footer className="cb-footer">
        <strong>
          {e.dirty
            ? uiText('本页分镜未保存')
            : base
              ? uiText('固定版本 v') + base.version + (base.confirmed ? uiText(' · 作者已确认') : uiText(' · 待作者确认'))
              : uiText('项目新分镜本')}
        </strong>
        <button className="cb-primary" disabled={locked || !e.dirty} onClick={() => e.mutate('save')}>{uiText("保存分镜版本")}</button>
        {base && !base.confirmed && (
          <button
            disabled={locked || e.dirty || base.version !== e.latest}
            onClick={() => e.setConfirming(true)}
          >{uiText("作者确认此版")}</button>
        )}
        {base?.confirmed && (
          <button disabled={locked || e.dirty} onClick={e.exportDeck}>{uiText("下载分镜 PowerPoint")}</button>
        )}
        {e.busy === 'export-pptx' && <button onClick={e.cancelExport}>{uiText("取消导出")}</button>}
      </footer>
      {e.confirming && base && (
        <div className="cb-confirm">
          <strong>{uiText("确认交付 v")}{base.version}《{base.document.title}》？
          </strong>
          <p>{uiText("将确认此版的")} {base.document.panels.length} {uiText("个镜头。后续编辑会形成新的待确认版本。")}</p>
          <button disabled={locked} onClick={() => e.setConfirming(false)}>{uiText("返回核对")}</button>
          <button disabled={locked} onClick={() => e.mutate('confirm')}>{uiText("确认此分镜版本")}</button>
        </div>
      )}
    </section>
  );
}
