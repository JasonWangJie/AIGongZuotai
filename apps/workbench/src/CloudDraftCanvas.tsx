import { uiText, currentLocale } from './locale';
import { useCallback, useEffect, useReducer, useRef, useState } from 'react';
import { api, ApiError } from './api';
import { Icon } from './components';
import { copyNodes, draftNodesValid, draftReducer, draftSyncLabel, isDraftDirty } from './draftState';
import type { DraftOperation } from './draftState';
import type { DraftAction } from './draftState';
import type { DraftRecoveryStore } from './draftRecovery';
import type { CloudDraft, DraftNode, DraftRevision } from './types';
import { RequestDetails } from './RequestDetails';
import { useCountdown } from './useCountdown';
import { keepUnknown } from './requestFeedback';
import { useInvalidation } from './useInvalidation';

type Props = { projectId: string; localDraftKey: string; recovery: DraftRecoveryStore; suspended: boolean; refreshVersion?: number; visible?: boolean; onDirtyChange: (dirty: boolean) => void; onAuthExpired: () => void };
export function CloudDraftCanvas({ projectId, localDraftKey, recovery, suspended, refreshVersion = 0, visible = true, onDirtyChange, onAuthExpired }: Props) {
  const [state, reactDispatch] = useReducer(draftReducer, projectId, id => recovery.restore(localDraftKey, id));
  const stateRef = useRef(state); stateRef.current = state;
  const suspendedRef = useRef(suspended); suspendedRef.current = suspended;
  const dispatch = useCallback((action: DraftAction) => {
    const userIntent = ['edit', 'restore', 'saving', 'use_server', 'keep_mine'].includes(action.type);
    if (suspendedRef.current && userIntent) return;
    const next = draftReducer(stateRef.current, action); stateRef.current = next;
    recovery.remember(localDraftKey, next, userIntent); reactDispatch(action);
  }, [localDraftKey, recovery]);
  const [syncReading, setSyncReading] = useState(false);
  const [revisions, setRevisions] = useState<DraftRevision[]>([]);
  const [nextRevision, setNextRevision] = useState<number | null>(null);
  const [historyOpen, setHistoryOpen] = useState(false);
  const [historyBusy, setHistoryBusy] = useState(false);
  const [preview, setPreview] = useState<CloudDraft | null>(null);
  const [previewBusy, setPreviewBusy] = useState(false);
  const [historyError, setHistoryError] = useState('');
  const [requestIssue, setRequestIssue] = useState<ApiError | null>(null);
  const retrySeconds = useCountdown(requestIssue?.retryAt || 0);
  const [importNodes, setImportNodes] = useState<DraftNode[] | null>(null);
  const [notice, setNotice] = useState(() => isDraftDirty(state) ? uiText('已恢复本页会话内存中的未保存草稿。仅同一账户、同一项目可见；刷新或关闭页面前请保存或导出。') : '');
  const alive = useRef(true), savingLock = useRef(false), historyLock = useRef(false), requestGeneration = useRef(0), previewGeneration = useRef(0);
  const authExpired = useRef(onAuthExpired); authExpired.current = onAuthExpired;
  const dirty = isDraftDirty(state);
  const frozen = suspended || !state.server || ['loading', 'saving', 'uncertain'].includes(state.phase);
  const saveDisabled = frozen || retrySeconds > 0 || state.phase === 'conflict' || !dirty;

  const readServer = useCallback(async () => {
    const generation = ++requestGeneration.current;
    try { const draft = await api.draft(projectId); if (!alive.current || generation !== requestGeneration.current) return; if (draft.project_id !== projectId || !draftNodesValid(draft.nodes)) throw new Error(uiText('服务器草稿结构无效，未载入编辑区。')); dispatch({ type: 'loaded', draft }); }
    catch (error) { if (!alive.current || generation !== requestGeneration.current) return; if (error instanceof ApiError && error.status === 401) { authExpired.current(); return; } setRequestIssue(error instanceof ApiError ? error : null); dispatch({ type: 'rejected', error: error instanceof Error ? error.message : uiText('读取云端草稿失败') }); }
  }, [projectId, dispatch]);

  useEffect(() => {
    alive.current = true; savingLock.current = false; historyLock.current = false; setHistoryBusy(false); setPreviewBusy(false);
    if (stateRef.current.pending && stateRef.current.phase === 'saving') dispatch({ type: 'uncertain', key: stateRef.current.pending.key, error: uiText('界面连接已重建，之前的保存仍需确认。请使用原请求重试。') });
    if (!stateRef.current.server) void readServer();
    return () => { alive.current = false; requestGeneration.current++; previewGeneration.current++; };
  }, [readServer]);
  useEffect(() => { onDirtyChange(dirty); }, [dirty, onDirtyChange]);
  useEffect(() => { const guard = (event: BeforeUnloadEvent) => { if (dirty) { event.preventDefault(); event.returnValue = ''; } }; window.addEventListener('beforeunload', guard); return () => window.removeEventListener('beforeunload', guard); }, [dirty]);

  async function syncSnapshot() {
    if (isDraftDirty(stateRef.current) || savingLock.current) return;
    const generation = requestGeneration.current; setSyncReading(true);
    try { const draft = await api.draft(projectId); if (!alive.current || generation !== requestGeneration.current) return; if (draft.project_id !== projectId || !draftNodesValid(draft.nodes)) throw new ApiError(uiText('云端草稿结构无效。')); dispatch({ type: 'observed', draft }); }
    catch (cause) { if (!alive.current || generation !== requestGeneration.current) return; if (cause instanceof ApiError && cause.status === 401) authExpired.current(); else setNotice(uiText('云端状态暂未同步，当前草稿保持不变。可稍后手动核对或保存。')); }
    finally { if (alive.current && generation === requestGeneration.current) setSyncReading(false); }
  }
  useInvalidation(refreshVersion, visible, syncReading || dirty || frozen || state.phase === 'conflict' || retrySeconds > 0, () => void syncSnapshot());
  async function loadHistory(before?: number) {
    if (historyLock.current) return; historyLock.current = true; setHistoryBusy(true); setHistoryError('');
    const generation = requestGeneration.current;
    try { const page = await api.draftRevisions(projectId, before); if (!alive.current || generation !== requestGeneration.current) return; setRevisions(old => before ? [...old, ...page.items.filter(item => !old.some(saved => saved.revision === item.revision))] : page.items); setNextRevision(page.has_more ? page.next_before_revision : null); }
    catch (error) { if (alive.current && generation === requestGeneration.current) { if (error instanceof ApiError && error.status === 401) authExpired.current(); else setHistoryError(error instanceof Error ? error.message : uiText('版本列表暂不可用')); } }
    finally { if (generation === requestGeneration.current) { historyLock.current = false; if (alive.current) setHistoryBusy(false); } }
  }

  async function readConflict() {
    const generation = requestGeneration.current;
    try { const draft = await api.draft(projectId); if (!alive.current || generation !== requestGeneration.current) return; if (draft.project_id !== projectId || !draftNodesValid(draft.nodes)) throw new Error(uiText('服务器草稿结构无效')); dispatch({ type: 'conflict_snapshot', draft }); }
    catch (error) { if (alive.current && generation === requestGeneration.current) { if (error instanceof ApiError && error.status === 401) authExpired.current(); else setNotice(uiText('当前编辑仍保留，服务器最新版读取失败，可再次读取或导出。')); } }
  }

  async function submit(operation: DraftOperation) {
    if (savingLock.current || suspendedRef.current || Date.now() < (requestIssue?.retryAt || 0)) return;
    const priorUnknown = stateRef.current.phase === 'uncertain' && stateRef.current.pending?.key === operation.key;
    savingLock.current = true; dispatch({ type: 'saving', operation }); setNotice(''); setRequestIssue(null);
    const generation = requestGeneration.current;
    try {
      const draft = await api.saveDraft(projectId, operation.body, operation.key);
      if (!alive.current || generation !== requestGeneration.current) return;
      if (draft.project_id !== projectId || !draftNodesValid(draft.nodes)) { dispatch({ type: 'uncertain', key: operation.key, error: uiText('保存响应无法核对，请使用原请求重试，或导出当前编辑。') }); return; }
      dispatch({ type: 'saved', key: operation.key, draft }); setNotice(uiText("已保存云端版本 v{0}", draft.revision)); setPreview(null);
      if (historyOpen) void loadHistory();
    } catch (error) {
      if (!alive.current || generation !== requestGeneration.current) return;
      if (error instanceof ApiError && error.status === 401) { authExpired.current(); return; }
      setRequestIssue(error instanceof ApiError ? error : null);
      if (error instanceof ApiError && error.code === 'DRAFT_CONFLICT') { dispatch({ type: 'conflict', key: operation.key, error: uiText('另一个窗口已保存更新。你的编辑完整保留，请选择如何处理。') }); await readConflict(); }
      else if (keepUnknown(priorUnknown, error instanceof ApiError ? error : {})) dispatch({ type: 'uncertain', key: operation.key, error: uiText('保存受理状态未知，云端可能已写入。编辑暂时锁定，原请求重试会使用完全相同的内容与幂等键。') });
      else dispatch({ type: 'rejected', key: operation.key, error: error instanceof Error ? error.message : uiText('保存失败，当前编辑未丢失。') });
    } finally { if (generation === requestGeneration.current) savingLock.current = false; }
  }

  function save() {
    if (!state.server || saveDisabled || savingLock.current) return;
    if (!draftNodesValid(state.nodes)) { dispatch({ type: 'rejected', error: uiText('节点内容不符合限制，请检查后再保存。') }); return; }
    void submit({ key: crypto.randomUUID(), body: { expected_revision: state.server.revision, nodes: copyNodes(state.nodes) } });
  }
  function exportLocal() {
    const content = JSON.stringify({ format: 'xingtu-draft-v1', project_id: projectId, base_revision: state.server?.revision || 0, nodes: copyNodes(state.nodes) }, null, 2);
    const url = URL.createObjectURL(new Blob([content], { type: 'application/json;charset=utf-8' })); const link = document.createElement('a'); link.href = url; link.download = `xingtu-draft-${projectId}-local.json`; link.click(); window.setTimeout(() => URL.revokeObjectURL(url), 1000); setNotice(uiText('已导出当前编辑内容；导出文件不代表已保存云端。'));
  }
  function importLocal() {
    try { const raw = window.localStorage.getItem(localDraftKey); if (!raw) { setNotice(uiText('这台设备没有当前用户、当前项目的旧本地草稿。')); return; } const nodes: unknown = JSON.parse(raw); if (!draftNodesValid(nodes)) { setNotice(uiText('本地草稿格式不符合限制，未导入。')); return; } setImportNodes(copyNodes(nodes)); }
    catch { setNotice(uiText('无法读取本地草稿，当前云端编辑未改变。')); }
  }
  async function viewRevision(revision: number) {
    const generation = ++previewGeneration.current; setPreviewBusy(true); setHistoryError('');
    try { const draft = await api.draftRevision(projectId, revision); if (!alive.current || generation !== previewGeneration.current) return; if (draft.project_id !== projectId || !draftNodesValid(draft.nodes)) throw new Error(uiText('版本内容无效')); setPreview(draft); }
    catch (error) { if (alive.current && generation === previewGeneration.current) { if (error instanceof ApiError && error.status === 401) authExpired.current(); else setHistoryError(error instanceof Error ? error.message : uiText('读取旧版失败')); } }
    finally { if (alive.current && generation === previewGeneration.current) setPreviewBusy(false); }
  }

  return <section className="draft-section cloud-draft"><div className="section-title"><div><span className="eyebrow">IDEAS WITH A HISTORY</span><h1>{uiText("让灵感，留下每一步。")}</h1><p>{uiText("云端文字草稿 · 手动保存版本 · 支持节点编辑，不支持拖拽、连线或模型执行。")}</p></div><span className="cloud-state" data-state={state.phase}><i/>{state.server ? `v${state.server.revision}` : '—'} <small>{draftSyncLabel(state)}</small></span></div>
    <div className="draft-toolbar"><div><button className="button primary" disabled={saveDisabled} onClick={save}><Icon name="check" size={15}/>{state.phase === 'saving' ? uiText('正在保存…') : uiText('保存云端版本')}</button><button className="button secondary" disabled={frozen || state.nodes.length >= 40} onClick={() => dispatch({ type: 'edit', nodes: [...state.nodes, { id: crypto.randomUUID(), title: uiText('新的灵感'), note: '' }] })}><Icon name="plus" size={15}/>{uiText("新增节点")}</button></div><div><button className="text-button" disabled={!state.server} onClick={exportLocal}>{uiText("导出本地编辑")}</button><button className="text-button" disabled={frozen || state.phase === 'conflict'} onClick={importLocal}>{uiText("导入旧本地草稿")}</button><button className="text-button" onClick={() => { setHistoryOpen(!historyOpen); if (!historyOpen) void loadHistory(); }} aria-expanded={historyOpen}>{uiText("版本历史")} <span>{historyOpen ? '−' : '+'}</span></button></div></div>
    {suspended && <p className="draft-notice" role="status">{uiText("正在完成项目或账户操作，草稿编辑已暂时锁定。")}</p>}{notice && <p className="draft-notice" role="status">{notice}</p>}
    {state.error && <div className={'draft-error ' + (state.phase === 'conflict' ? 'conflict' : '')} role="alert"><strong>{state.phase === 'conflict' ? uiText('发现版本冲突') : state.phase === 'uncertain' ? uiText('保存结果需要确认') : uiText('草稿暂未保存')}</strong><p>{state.error}</p><RequestDetails error={requestIssue} unknown={state.phase === 'uncertain'}/>{state.phase === 'uncertain' && state.pending && <div className="draft-resolution"><button className="button primary" disabled={suspended || retrySeconds > 0} onClick={() => state.pending && void submit(state.pending)}>{uiText("原请求重试")}</button><button className="button secondary" onClick={exportLocal}>{uiText("保留本地导出")}</button><span>{uiText("不要重新创建保存请求；重试将回放同一次提交。")}</span></div>}{state.phase === 'conflict' && <><p>{state.conflict ? uiText("云端现在是 v{0}，编辑区基于 v{1}。不会自动覆盖任何一方。", state.conflict.revision, state.server?.revision) : uiText('正在获取服务器最新版；编辑内容仍保留。')}</p><div className="draft-resolution"><button className="button secondary" onClick={() => void readConflict()}>{uiText("重新读取服务器版本")}</button><button className="button secondary" onClick={exportLocal}>{uiText("保留本地导出")}</button>{state.conflict && <><button className="text-button danger" disabled={suspended} onClick={() => dispatch({ type: 'use_server' })}>{uiText("放弃本地编辑，加载服务器版")}</button><button className="text-button" disabled={suspended} onClick={() => { dispatch({ type: 'keep_mine' }); setNotice(uiText('已以服务器最新版为基准保留你的内容。再次点击“保存云端版本”会另存新版本，不会自动合并。')); }}>{uiText("保留我的内容，准备另存新版")}</button></>}</div>{state.conflict && <details className="server-draft-preview"><summary>{uiText("查看服务器 v")}{state.conflict.revision} {uiText("内容 ·")} {state.conflict.nodes.length} {uiText("个节点")}</summary>{state.conflict.nodes.map(node => <article key={node.id}><strong>{node.title || uiText('未命名节点')}</strong><p>{node.note}</p></article>)}</details>}</>}{state.phase === 'error' && !state.server && <button className="button secondary" disabled={retrySeconds > 0} onClick={() => void readServer()}>{uiText("重新连接草稿")}</button>}</div>}
    {importNodes && <div className="draft-import"><strong>{uiText("找到")} {importNodes.length} {uiText("个本地节点")}</strong><p>{uiText("只替换当前编辑区，不会自动写入云端；当前未保存编辑将被替换。")}</p><div><button className="button secondary" disabled={frozen} onClick={() => { dispatch({ type: 'edit', nodes: importNodes }); setImportNodes(null); setNotice(uiText('本地草稿已载入编辑区，点击保存后才会创建云端新版本。')); }}>{uiText("载入本地草稿")}</button><button className="text-button" onClick={() => setImportNodes(null)}>{uiText("取消导入")}</button></div></div>}
    {historyOpen && <div className="draft-history"><div className="section-title compact"><h2>{uiText("版本历史")}</h2><button className="text-button" disabled={historyBusy} onClick={() => void loadHistory()}>{uiText("刷新版本")}</button></div>{historyError && <p className="inline-error">{historyError}</p>}<div className="revision-list">{revisions.map(revision => <button key={revision.revision} className={preview?.revision === revision.revision ? 'active' : ''} onClick={() => void viewRevision(revision.revision)}><strong>v{revision.revision}</strong><span>{revision.node_count} {uiText("节点")}</span><time>{new Date(revision.updated_at).toLocaleString(currentLocale(), { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' })}</time></button>)}</div>{!revisions.length && !historyBusy && <p className="subtle">{uiText("尚无已保存版本；首次保存后建立版本历史。")}</p>}{nextRevision && <button className="text-button" disabled={historyBusy} onClick={() => void loadHistory(nextRevision)}>{uiText("加载更早版本")}</button>}{historyBusy && <p className="subtle">{uiText("正在读取版本列表…")}</p>}{previewBusy && <p className="subtle">{uiText("正在读取旧版内容…")}</p>}{preview && <div className="revision-preview"><div><strong>{uiText("只读预览 · v")}{preview.revision}</strong><button className="button secondary" disabled={frozen || state.phase === 'conflict'} onClick={() => { dispatch({ type: 'restore', nodes: preview.nodes }); setNotice(uiText("v{0} 已替换编辑区内容；保存后创建新版本，原历史不会改写。", preview.revision)); }}>{uiText("用此版本替换编辑区，准备另存")}</button></div>{dirty && <p className="inline-error">{uiText("载入旧版会替换当前未保存编辑；可以先导出保留。")}</p>}{preview.nodes.map(node => <article key={node.id}><strong>{node.title || uiText('未命名节点')}</strong><p>{node.note}</p></article>)}</div>}</div>}
    <div className="draft-board">{state.nodes.map((node, index) => <article className="draft-node" key={node.id}><div className="draft-node-top"><span>NOTE / {String(index + 1).padStart(2, '0')}</span><button className="icon-button" disabled={frozen} aria-label={uiText('删除节点 ') + node.title} onClick={() => dispatch({ type: 'edit', nodes: state.nodes.filter(item => item.id !== node.id) })}><Icon name="trash" size={16}/></button></div><input aria-label={uiText('节点 ') + (index + 1) + uiText(' 标题')} disabled={frozen} value={node.title} maxLength={80} onChange={event => dispatch({ type: 'edit', nodes: state.nodes.map(item => item.id === node.id ? { ...item, title: event.target.value } : item) })}/><textarea aria-label={uiText('节点 ') + (index + 1) + uiText(' 内容')} disabled={frozen} placeholder={uiText("写下一个可生长的想法…")} value={node.note} maxLength={5000} onChange={event => dispatch({ type: 'edit', nodes: state.nodes.map(item => item.id === node.id ? { ...item, note: event.target.value } : item) })}/></article>)}{!state.nodes.length && <div className="empty-state"><Icon name="layers" size={30}/><h2>{state.phase === 'loading' ? uiText('正在读取云端草稿') : uiText('从第一个节点开始')}</h2><p>{uiText("新增文字节点，整理你的创作方向。保存后可在这个项目的其他会话继续。")}</p>{state.server && !frozen && <button className="button secondary" onClick={() => dispatch({ type: 'edit', nodes: [{ id: 'brief', title: uiText('创意简报'), note: uiText('写下作品的主题、情绪与想传达的故事。') }, { id: 'visual', title: uiText('视觉方向'), note: uiText('确定光线、材质、色彩与画面规则。') }] })}>{uiText("载入创意提纲（未保存）")}</button>}</div>}</div><p className="subtle">{state.nodes.length} {uiText("/ 40 个节点 · 手动保存到 PostgreSQL · 最多 1,000 个历史版本 · 非可执行工作流")}</p></section>;
}
