import { uiText } from './locale';
import { useEffect, useState } from 'react';
import { api, ApiError } from './api';
import { defaultLibraryFilters } from './libraryState';
import type { AssetEntry } from './libraryState';
import { coord, newCanvasID } from './freeCanvas';
import type { CanvasDocument, CanvasNode } from './freeCanvas';
import { statusName } from './types';
import type { Run } from './types';

type Props = {
  doc: CanvasDocument;
  selected: string;
  project: string;
  runs: Run[];
  locked: boolean;
  edit: (doc: CanvasDocument) => void;
  locate: (id: string) => void;
  onExpired: () => void;
  sourceVersion: number;
};
function matches(node: CanvasNode, search: string) {
  return (node.title + ' ' + node.id).toLowerCase().includes(search.trim().toLowerCase());
}
function NodePicker({
  nodes,
  select,
  label,
}: {
  nodes: CanvasNode[];
  select: (n: CanvasNode) => void;
  label: string;
}) {
  const [search, setSearch] = useState(''),
    [page, setPage] = useState(0);
  const found = nodes.filter((n) => matches(n, search)),
    start = Math.min(page, Math.max(0, Math.ceil(found.length / 12) - 1)) * 12;
  return (
    <div className="free-picker">
      <label>
        {label}
        <input
          type="search"
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            setPage(0);
          }}
          placeholder={uiText("按名称或节点 ID 查找")}
        />
      </label>
      <ul>
        {found.slice(start, start + 12).map((n) => (
          <li key={n.id}>
            <button type="button" onClick={() => select(n)}>
              {n.title}
              <small>{n.id}</small>
            </button>
          </li>
        ))}
      </ul>
      {found.length > 12 && (
        <div>
          <button disabled={start === 0} onClick={() => setPage((p) => p - 1)}>{uiText("上一页")}</button>
          <span>
            {1 + start / 12} / {Math.ceil(found.length / 12)}
          </span>
          <button disabled={start + 12 >= found.length} onClick={() => setPage((p) => p + 1)}>{uiText("下一页")}</button>
        </div>
      )}
    </div>
  );
}

function SourcePicker({
  project,
  runs,
  node,
  update,
  onExpired,
  sourceVersion,
}: {
  project: string;
  runs: Run[];
  node: CanvasNode;
  update: (patch: Partial<CanvasNode>) => void;
  onExpired: () => void;
  sourceVersion: number;
}) {
  const [mode, setMode] = useState(''),
    [assets, setAssets] = useState<AssetEntry[]>([]);
  const [cursor, setCursor] = useState<string>(),
    [next, setNext] = useState<string | null>(null);
  const [error, setError] = useState(''),
    [loading, setLoading] = useState(false);
  useEffect(() => {
    setAssets([]);
    setNext(null);
    setError('');
    if (mode !== 'image') return;
    const c = new AbortController();
    setLoading(true);
    api
      .library(project, { ...defaultLibraryFilters(), kind: 'image' }, cursor, c.signal)
      .then((page) => {
        if (c.signal.aborted) return;
        setAssets(
          page.items.filter(
            (a) =>
              a.project_id === project &&
              ['generated', 'reference'].includes(a.source) &&
              a.kind === 'image' &&
              a.state === 'active' &&
              a.moderation?.state === 'ALLOWED' &&
              ['image/png', 'image/jpeg'].includes(a.content_type || '') &&
              !!a.content_url &&
              !!a.thumbnail_url &&
              (a.byte_size || 0) > 0 &&
              (a.byte_size || 0) <= 8 * 1024 * 1024,
          ),
        );
        setNext(page.next_cursor);
      })
      .catch((e) => {
        if (!c.signal.aborted) {
          if ((e as ApiError).status === 401) onExpired();
          setError(uiText('无法读取本项目图片。'));
        }
      })
      .finally(() => {
        if (!c.signal.aborted) setLoading(false);
      });
    return () => c.abort();
  }, [project, mode, cursor, onExpired, sourceVersion]);
  return (
    <div className="free-bind">
      <p>
        {node.asset_id
          ? uiText('已绑定图片 · 修订 ') + node.asset_revision
          : node.run_id
            ? uiText('已绑定对话 ') + node.run_id
            : uiText('此节点尚未绑定来源')}
      </p>
      <div className="free-actions">
        <button
          onClick={() => {
            setMode(mode === 'image' ? '' : 'image');
            setCursor(undefined);
          }}
        >{uiText("选择项目图片")}</button>
        <button onClick={() => setMode(mode === 'run' ? '' : 'run')}>{uiText("选择已有对话")}</button>
        {(node.asset_id || node.run_id) && (
          <button
            onClick={() => {
              update({ asset_id: undefined, asset_revision: undefined, run_id: undefined });
              setMode('');
            }}
          >{uiText("解除绑定")}</button>
        )}
      </div>
      {mode === 'image' && (
        <div>
          {loading ? (
            <p>{uiText("读取图片中…")}</p>
          ) : (
            <ul>
              {assets.map((a) => (
                <li key={a.id}>
                  <button
                    onClick={() => {
                      update({ asset_id: a.id, asset_revision: a.revision, run_id: undefined });
                      setMode('');
                    }}
                  >
                    {a.name} · v{a.revision}
                  </button>
                </li>
              ))}
            </ul>
          )}
          {!loading && !assets.length && <p>{uiText("本页没有可绑定的已审核 PNG 或 JPEG。")}</p>}
          {next && <button onClick={() => setCursor(next)}>{uiText("下一页图片")}</button>}
          {error && <p role="alert">{error}</p>}
        </div>
      )}
      {mode === 'run' && (
        <div>
          <label>{uiText("本项目对话")}<select
              value=""
              onChange={(e) => {
                if (e.target.value) {
                  update({ run_id: e.target.value, asset_id: undefined, asset_revision: undefined });
                  setMode('');
                }
              }}
            >
              <option value="">{uiText("从当前已加载的对话中选择")}</option>
              {runs
                .filter((r) => r.project_id === project)
                .map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.model_name || r.model_id} · {statusName(r.status)} · {r.id}
                  </option>
                ))}
            </select>
          </label>
        </div>
      )}
    </div>
  );
}

export function CanvasInspector({
  doc,
  selected,
  project,
  runs,
  locked,
  edit,
  locate,
  onExpired,
  sourceVersion,
}: Props) {
  const node = doc.nodes.find((n) => n.id === selected);
  const [groupID, setGroupID] = useState(''),
    [groupName, setGroupName] = useState('');
  const [connect, setConnect] = useState(false),
    [edgePage, setEdgePage] = useState(0);
  const id = selected.startsWith('!') ? selected.slice(1) : groupID;
  const group = doc.groups.find((g) => g.id === id);
  const edges = node ? doc.edges.filter((e) => e.from === node.id || e.to === node.id) : [];
  const connected = new Set(edges.filter((e) => e.from === node?.id).map((e) => e.to));
  const edgeStart = Math.min(edgePage, Math.max(0, Math.ceil(edges.length / 10) - 1)) * 10;
  function update(patch: Partial<CanvasNode>) {
    if (node) edit({ ...doc, nodes: doc.nodes.map((n) => (n.id === node.id ? { ...n, ...patch } : n)) });
  }
  function toggleGroup() {
    if (group)
      edit({
        ...doc,
        groups: doc.groups.map((g) => (g.id === group.id ? { ...g, collapsed: !g.collapsed } : g)),
      });
  }
  return (
    <aside className="free-inspector" aria-label={uiText("自由画布检查器")}>
      <details className="free-section">
        <summary>{uiText("查找节点 ·")} {doc.nodes.length}</summary>
        <NodePicker nodes={doc.nodes} select={(n) => locate(n.id)} label={uiText("查找并定位节点")} />
      </details>
      <fieldset disabled={locked} className="free-section">
        <legend>{uiText("节点属性")}</legend>
        {node ? (
          <>
            <label htmlFor="free-node-title">{uiText("节点名称")}</label>
            <input
              id="free-node-title"
              maxLength={120}
              value={node.title}
              onChange={(e) => update({ title: e.target.value })}
            />
            <label htmlFor="free-node-note">{uiText("备注")}</label>
            <textarea
              id="free-node-note"
              maxLength={600}
              rows={3}
              value={node.note || ''}
              onChange={(e) => update({ note: e.target.value })}
            />
            <div className="free-coords">
              {(['x', 'y'] as const).map((axis) => (
                <label key={axis} htmlFor={'free-node-' + axis}>
                  {axis.toUpperCase()}
                  <input
                    id={'free-node-' + axis}
                    type="number"
                    step="1"
                    min="-1000000"
                    max="1000000"
                    value={node[axis]}
                    onChange={(e) => {
                      if (Number.isFinite(e.target.valueAsNumber))
                        update({ [axis]: coord(e.target.valueAsNumber) });
                    }}
                  />
                </label>
              ))}
            </div>
            <label>{uiText("所属分组")}<select
                value={node.group_id || ''}
                onChange={(e) => update({ group_id: e.target.value || undefined })}
              >
                <option value="">{uiText("无分组")}</option>
                {doc.groups.map((g) => (
                  <option key={g.id} value={g.id}>
                    {g.title}
                  </option>
                ))}
              </select>
            </label>
            <details>
              <summary>{uiText("来源绑定")}</summary>
              <SourcePicker key={node.id} {...{ project, runs, node, update, onExpired, sourceVersion }} />
            </details>
            <details>
              <summary>{uiText("节点连线 ·")} {edges.length}</summary>
              <button disabled={doc.edges.length >= 4000} onClick={() => setConnect((v) => !v)}>{uiText("添加出向连线")}</button>
              {connect && (
                <NodePicker
                  nodes={doc.nodes.filter((n) => n.id !== node.id && !connected.has(n.id))}
                  label={uiText("目标节点")}
                  select={(n) => {
                    edit({
                      ...doc,
                      edges: [...doc.edges, { id: newCanvasID('edge'), from: node.id, to: n.id }],
                    });
                    setConnect(false);
                  }}
                />
              )}
              <ul>
                {edges.slice(edgeStart, edgeStart + 10).map((e) => (
                  <li key={e.id}>
                    {e.from === node.id ? '→ ' : '← '}
                    {doc.nodes.find((n) => n.id === (e.from === node.id ? e.to : e.from))?.title}
                    <button
                      aria-label={uiText('移除连线 ') + e.id}
                      onClick={() => edit({ ...doc, edges: doc.edges.filter((old) => old.id !== e.id) })}
                    >{uiText("移除")}</button>
                  </li>
                ))}
              </ul>
              {edges.length > 10 && (
                <div className="free-actions">
                  <button disabled={!edgeStart} onClick={() => setEdgePage((p) => p - 1)}>{uiText("上一页连线")}</button>
                  <button disabled={edgeStart + 10 >= edges.length} onClick={() => setEdgePage((p) => p + 1)}>{uiText("下一页连线")}</button>
                </div>
              )}
            </details>
            <button
              onClick={() =>
                edit({
                  ...doc,
                  nodes: doc.nodes.filter((n) => n.id !== node.id),
                  edges: doc.edges.filter((e) => e.from !== node.id && e.to !== node.id),
                })
              }
            >{uiText("移除节点及其连线")}</button>
          </>
        ) : (
          <p>{uiText("选择画布中的节点，或使用上方查找。")}</p>
        )}
      </fieldset>
      <fieldset disabled={locked} className="free-section">
        <legend>{uiText("分组")}</legend>
        <label htmlFor="free-group-select">{uiText("管理分组")}</label>
        <select
          id="free-group-select"
          value={group?.id || ''}
          onChange={(e) => {
            setGroupID(e.target.value);
            if (selected.startsWith('!') && e.target.value) locate('!' + e.target.value);
          }}
        >
          <option value="">{uiText("选择分组")}</option>
          {doc.groups.map((g) => (
            <option key={g.id} value={g.id}>
              {g.title}
            </option>
          ))}
        </select>
        {group && (
          <>
            <label>{uiText("分组名称")}<input
                value={group.title}
                maxLength={120}
                onChange={(e) =>
                  edit({
                    ...doc,
                    groups: doc.groups.map((g) => (g.id === group.id ? { ...g, title: e.target.value } : g)),
                  })
                }
              />
            </label>
            <button onClick={toggleGroup}>{group.collapsed ? uiText('展开分组') : uiText('折叠分组')}</button>
            <button
              onClick={() => {
                edit({
                  ...doc,
                  groups: doc.groups.filter((g) => g.id !== group.id),
                  nodes: doc.nodes.map((n) => (n.group_id === group.id ? { ...n, group_id: undefined } : n)),
                });
                setGroupID('');
              }}
            >{uiText("解散分组")}</button>
          </>
        )}
        <label>{uiText("新分组名称")}<input value={groupName} maxLength={120} onChange={(e) => setGroupName(e.target.value)} />
        </label>
        <button
          disabled={!groupName.trim() || doc.groups.length >= 128}
          onClick={() => {
            const g = { id: newCanvasID('group'), title: groupName.trim(), collapsed: false };
            edit({ ...doc, groups: [...doc.groups, g] });
            setGroupName('');
            setGroupID(g.id);
          }}
        >{uiText("新建分组")}</button>
      </fieldset>
    </aside>
  );
}
