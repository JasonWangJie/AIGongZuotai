import { uiText } from './locale';
import { useEffect, useMemo, useRef, useState } from 'react';
import { CanvasInspector } from './CanvasInspector';
import { mergeCanvas } from './canvasMerge';
import { FreeCanvasViewport } from './FreeCanvasViewport';
import type { CanvasSources } from './freeCanvas';
import { coord, newCanvasID, usableSource, validCanvas } from './freeCanvas';
import type { Run } from './types';
import { useFreeCanvas } from './useFreeCanvas';
import type { CanvasProps } from './useFreeCanvas';
import './free-canvas.css';

export function FreeCanvasWorkbench(props: CanvasProps & { runs: Run[] }) {
  const { enabled, visible, project, runs, onExpired, assetVersion } = props;
  const state = useFreeCanvas(props);
  const { doc, version, pending, dirty, ready, head, busy, edit } = state;
  const valid = useMemo(() => validCanvas(doc), [doc]);
  const revision = version?.revision || 0;
  const locked = !!state.mergeHead || busy || !!pending || !ready || !visible || state.denied;
  const [selected, select] = useState('');
  const [focus, setFocus] = useState<{ id: string; ticket: number }>();
  const ticket = useRef(0);
  const [discard, setDiscard] = useState<{ revision?: number }>();
  const [editTitle, setEditTitle] = useState(false);
  useEffect(() => {
    setDiscard(undefined);
    setEditTitle(false);
  }, [state.epoch]);
  const sources = useMemo(() => {
    const result: CanvasSources = Object.create(null);
    for (const n of doc.nodes) {
      if (!n.asset_id && !n.run_id) continue;
      const source = usableSource(n, version, state.sources);
      if (source) result[n.id] = source;
    }
    return result;
  }, [doc.nodes, version, state.sources]);
  const merge = useMemo(
    () =>
      state.mergeHead
        ? mergeCanvas(state.mergeBase, doc, state.mergeHead.document, 'local')
        : null,
    [state.mergeBase, doc, state.mergeHead],
  );
  function locate(id: string) {
    const n = doc.nodes.find((item) => item.id === id);
    if (!locked && n?.group_id && doc.groups.some((g) => g.id === n.group_id && g.collapsed)) {
      edit({ ...doc, groups: doc.groups.map((g) => (g.id === n.group_id ? { ...g, collapsed: false } : g)) });
    }
    select(id);
    setFocus({ id, ticket: ++ticket.current });
  }
  function pickRevision(n?: number) {
    if (dirty) {
      setDiscard({ revision: n });
      return;
    }
    void state.readVersion(n);
  }
  function add() {
    const previous = doc.nodes.find((n) => n.id === selected) || doc.nodes.at(-1);
    const n = {
      id: newCanvasID('node'),
      title: uiText('新节点'),
      note: '',
      x: previous ? coord(previous.x + 210) : 0,
      y: previous?.y || 0,
    };
    edit({ ...doc, nodes: [...doc.nodes, n] });
    select(n.id);
    setFocus({ id: n.id, ticket: ++ticket.current });
  }
  return (
    <section className="free-canvas" aria-label={uiText("项目自由画布")}>
      <header className="free-heading">
        <div>
          <p>{uiText("自由画布")}</p>
          <h2>{doc.title || uiText('未命名画布')}</h2>
        </div>
        {ready && (
          <button onClick={() => setEditTitle((v) => !v)} disabled={locked}>{uiText("编辑画布名称")}</button>
        )}
      </header>
      {!enabled ? (
        <p>{uiText("登录启用了账户与 PostgreSQL 的工作台后，可编辑和保存项目自由画布。")}</p>
      ) : (
        <>
          {editTitle && (
            <label className="free-title-edit">{uiText("画布名称")}<input
                autoFocus
                value={doc.title}
                maxLength={120}
                onChange={(e) => edit({ ...doc, title: e.target.value })}
                disabled={locked}
              />
            </label>
          )}
          <div className="free-command">
            <div>
              <span>{revision ? uiText('修订 v') + revision : uiText('新画布')}</span>
              <span>
                {doc.nodes.length} {uiText("个节点 ·")} {doc.edges.length} {uiText("条连线")}</span>
              {dirty && <b>{uiText("有未保存编辑")}</b>}
            </div>
            <div className="free-actions">
              <button disabled={locked || doc.nodes.length >= 2000} onClick={add}>{uiText("添加节点")}</button>
              <button
                className="free-primary"
                disabled={locked || !dirty || head > revision || !valid}
                onClick={() => void state.save()}
              >{uiText("保存画布")}</button>
              <button
                disabled={busy || state.denied}
                onClick={() => (state.historyOpen ? state.closeHistory() : void state.list(false))}
              >{uiText("修订历史")}</button>
            </div>
          </div>
          {pending && (
            <div className="free-notice">
              <span>{uiText("这次保存需要核对原回执。")}</span>
              <button disabled={busy || state.denied} onClick={() => void state.check()}>{uiText("核对原保存回执")}</button>
            </div>
          )}
          {head > revision && ready && (
            <div className="free-notice">
              <span>{uiText("项目最新修订 v")}{head} {uiText("· 当前查看")}{revision ? ' v' + revision : uiText('新稿')}
              </span>
              <button disabled={locked} onClick={() => pickRevision()}>{uiText("读取最新修订")}</button>
              {dirty && (
                <button disabled={locked} onClick={() => void state.previewMerge()}>{uiText("核对并合并本页编辑")}</button>
              )}
            </div>
          )}
          {merge && state.mergeHead && (
            <div className="free-notice" role="group" aria-label={uiText("合并画布编辑")}>
              <p>{uiText("服务端 v")}{state.mergeHead.revision} {uiText("与本页有")} {merge.conflicts.length}{' '}{uiText("处同时更改。其余独立修改会一起保留。")}</p>
              {merge.conflicts.length > 0 && (
                <details>
                  <summary>{uiText("查看同时修改的项目")}</summary>
                  <p>
                    {merge.conflicts.slice(0, 20).join('、')}
                    {merge.conflicts.length > 20 ? uiText('等 ') + merge.conflicts.length + uiText(' 处') : ''}
                  </p>
                </details>
              )}
              <button onClick={state.cancelMerge}>{uiText("返回本页编辑")}</button>
              <button disabled={state.mergeHead.revision !== head} onClick={() => state.reconcile('local')}>
                {merge.conflicts.length ? uiText('冲突项保留本页') : uiText('采用合并草稿')}
              </button>
              {merge.conflicts.length > 0 && (
                <button
                  disabled={state.mergeHead.revision !== head}
                  onClick={() => state.reconcile('remote')}
                >{uiText("冲突项采用服务器")}</button>
              )}
            </div>
          )}
          {state.historyOpen && (
            <div className="free-history">
              <h3>{uiText("修订历史")}</h3>
              {state.history.length === 0 ? (
                <p>{uiText("暂无可读修订。")}</p>
              ) : (
                state.history.map((r) => (
                  <button
                    key={r.revision}
                    disabled={busy || !!pending || state.denied}
                    onClick={() => pickRevision(r.revision)}
                  >
                    v{r.revision} · {r.title} · {r.node_count} {uiText("个节点")}</button>
                ))
              )}
              {state.before && (
                <button disabled={busy} onClick={() => void state.list(true)}>{uiText("更早修订")}</button>
              )}
            </div>
          )}
          {discard && (
            <div className="free-notice" role="group" aria-label={uiText("替换本页编辑")}>
              <p>{uiText("读取修订会替换当前未保存的画布内容。")}</p>
              <button onClick={() => setDiscard(undefined)}>{uiText("保留本页编辑")}</button>
              <button
                onClick={() => {
                  const n = discard.revision;
                  setDiscard(undefined);
                  void state.readVersion(n);
                }}
              >{uiText("放弃编辑并读取")}</button>
            </div>
          )}
          {ready && dirty && !valid && (
            <p role="alert">{uiText("请核对名称、节点及连线引用，确保来源和数量符合画布限制后再保存。")}</p>
          )}
          <div className="free-status" role="status">
            {state.message || (!ready && busy ? uiText('正在读取项目画布…') : '')}
          </div>
          {!ready && !busy && !pending && !state.denied && (
            <button onClick={() => void state.readVersion()}>{uiText("重新读取项目画布")}</button>
          )}
          {ready && visible && (
            <div className="free-layout">
              <div className="free-workspace">
                <FreeCanvasViewport
                  key={state.epoch}
                  doc={doc}
                  project={project}
                  revision={revision}
                  sources={sources}
                  selected={selected}
                  visible={visible}
                  locked={locked}
                  onSelect={select}
                  focusRequest={focus}
                  onMove={(id, p) =>
                    edit({ ...doc, nodes: doc.nodes.map((n) => (n.id === id ? { ...n, ...p } : n)) })
                  }
                />
                <p className="free-instructions">{uiText("拖动节点调整位置，拖动空白处平移。方向键平移，Shift + 方向键移动所选节点。")}</p>
              </div>
              <CanvasInspector
                {...{ doc, selected, project, runs, locked, edit, locate, onExpired }}
                sourceVersion={assetVersion}
              />
            </div>
          )}
        </>
      )}
    </section>
  );
}
