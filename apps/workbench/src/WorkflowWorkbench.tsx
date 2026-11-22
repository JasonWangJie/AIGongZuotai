import { projectLabel } from './serverDisplay';
import { uiText } from './locale';
import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError } from './api';
import { Icon } from './components';
import { RequestDetails } from './RequestDetails';
import { WorkflowEditor } from './WorkflowEditor';
import { WorkflowAttemptControls } from './WorkflowAttemptControls';
import { WorkflowRun } from './WorkflowRun';
import { useInvalidation } from './useInvalidation';
import { workflowApi } from './workflowApi';
import { loadIntent, templateGraph, workflowModels, workflowScope, workflowStatus } from './workflows';
import type { WorkflowDefinition, WorkflowExecution, WorkflowGraph, WorkflowIntent } from './workflows';
import type { Bootstrap } from './types';
import './workflow.css';

type Props = { b: Bootstrap; user: string; visible: boolean; suspended: boolean; refreshVersion?: number; expired: () => void; onDirty: (v: boolean) => void };
export function WorkflowWorkbench(props: Props) {
  const { b } = props;
  if (b.storage !== 'postgres' || !b.auth_enabled || !props.user) return <section className="wf-unavailable"><Icon name="layers" size={24}/><div><h2>{uiText("工作流需要持久项目和登录会话")}</h2><p>{b.storage !== 'postgres' ? uiText('当前项目使用内存演示。连接 PostgreSQL 并启用账户后，可以保存固定版本、执行节点并跨会话选图。') : uiText('当前项目未启用账户登录。启用登录后可保存并运行工作流。')}</p></div></section>;
  return <PersistentWorkflow {...props}/>;
}
function PersistentWorkflow({ b, user, visible, suspended, refreshVersion = 0, expired, onDirty }: Props) {
  const project = b.project.id, scope = workflowScope(b, user);
  const [tab, setTab] = useState<'design'|'runs'>('design');
  const [definitions, setDefinitions] = useState<WorkflowDefinition[]>([]), [runs, setRuns] = useState<WorkflowExecution[]>([]);
  const [definition, setDefinition] = useState<WorkflowDefinition|null>(null), [graph, setGraph] = useState<WorkflowGraph|null>(null);
  const [run, setRun] = useState<WorkflowExecution|null>(null), [fixed, setFixed] = useState<WorkflowDefinition|null>(null);
  const [detailReady, setDetailReady] = useState(false), [syncing, setSyncing] = useState(false);
  const [brief, setBrief] = useState(''), [error, setError] = useState<ApiError|null>(null), [busy, setBusy] = useState(false);
  const [pending, setPending] = useState<WorkflowIntent|null>(null), [storageError, setStorageError] = useState(false);
  const [replacement, setReplacement] = useState<WorkflowDefinition|'template'|'videoTemplate'|null>(null);
  const listTicket = useRef(0), detailTicket = useRef(0), versionTicket = useRef(0), selected = useRef(''), actionLock = useRef(false), alive = useRef(true);
  const locked = busy || !!pending || suspended || storageError;
  const dirty = !!graph && (!definition || JSON.stringify(graph) !== JSON.stringify(definition.graph));
  useEffect(() => { onDirty(dirty); return () => onDirty(false); }, [dirty, onDirty]);
  const report = useCallback((cause: unknown) => {
    if (!alive.current) return;
    const e = cause instanceof ApiError ? cause : new ApiError(cause instanceof Error ? cause.message : uiText('工作流请求失败'));
    setError(e); if (e.status === 401) expired();
  }, [expired]);
  useEffect(() => {
    alive.current = true;
    try { setPending(loadIntent(scope)); } catch (e) { setStorageError(true); report(e); }
    return () => { alive.current = false; ++listTicket.current; ++detailTicket.current; ++versionTicket.current; };
  }, [scope]);
  async function list() {
    const i = ++listTicket.current;
    try {
      const [defs, history] = await Promise.all([workflowApi.definitions(project), workflowApi.runs(project)]);
      if (i !== listTicket.current || !alive.current) return;
      setDefinitions((defs.items || []).filter(d => d.project_id === project)); setRuns((history.items || []).filter(r => r.project_id === project));
    } catch (e) { if (i === listTicket.current) report(e); }
  }
  useEffect(() => { if (visible) void list(); }, [visible, project]);
  async function refresh(id: string) {
    const i = ++detailTicket.current;
    try {
      const value = await workflowApi.run(project, id);
      if (alive.current && i === detailTicket.current && selected.current === id && value.id === id && value.project_id === project) {
        setRun(old => old?.id === id && old.version > value.version ? old : value); setDetailReady(true);
      }
    } catch (e) { if (i === detailTicket.current) report(e); }
  }
  useInvalidation(refreshVersion, visible, syncing || busy || suspended || !!pending, () => {
    setSyncing(true);
    const id = selected.current;
    void Promise.all([list(), ...(id ? [refresh(id)] : [])]).finally(() => { if (alive.current) setSyncing(false); });
  });
  function openSource(id: string) {
    ++versionTicket.current; setFixed(null); selected.current = id; setDetailReady(false); setTab('runs'); void refresh(id);
  }
  function chooseRun(value: WorkflowExecution) {
    ++versionTicket.current; setFixed(null); setDetailReady(false); selected.current = value.id; setRun(value); setTab('runs'); void refresh(value.id);
  }
  useEffect(() => {
    if (!visible || tab !== 'runs' || !run || !['TEXT_ACTIVE','IMAGE_ACTIVE','VIDEO_ACTIVE'].includes(run.status)) return;
    let timer = 0, stop = false; const id = run.id;
    const poll = async () => { await refresh(id); if (!stop) timer = window.setTimeout(poll, 1800); };
    timer = window.setTimeout(poll, 1400);
    return () => { stop = true; window.clearTimeout(timer); };
  }, [visible, tab, run?.id, run?.status]);
  function edit(d: WorkflowDefinition|'template'|'videoTemplate') {
    setDefinition(typeof d === 'string' ? null : d);
    setGraph(typeof d === 'string' ? templateGraph(b, d === 'videoTemplate') : structuredClone(d.graph)); setBrief(''); setReplacement(null); setTab('design');
  }
  function chooseDefinition(d: WorkflowDefinition|'template'|'videoTemplate') { if (locked) return; if (dirty) setReplacement(d); else edit(d); }
  function useResult(result: WorkflowDefinition|WorkflowExecution, intent: WorkflowIntent) {
    const { action } = intent;
    if (result.project_id !== project) throw new Error(uiText('工作流回执与项目不匹配，请核对原操作。'));
    if (action === 'definition' && 'graph' in result && !('definition_id' in result)) { edit(result); }
    else if ('definition_id' in result) {
      if (action === 'rerun' && (result.id === intent.run || result.source_workflow_id !== intent.run || intent.node && result.from_node !== intent.node)) throw new Error(uiText('重跑回执与原运行或起点不一致，请继续核对原键。'));
      if (['cancel','execute','confirm'].includes(action) && result.id !== intent.run || action === 'cancel' && result.status !== 'CANCELLED') throw new Error(uiText('操作回执与原运行不一致，请继续核对原键。'));
      chooseRun(result);
    }
    else throw new Error(uiText('工作流回执缺少资源信息。'));
    void list();
  }
  function clear(intent: WorkflowIntent) {
    if (loadIntent(scope)?.key === intent.key) sessionStorage.removeItem(scope);
    setPending(null);
  }
  async function perform(spec: Omit<WorkflowIntent,'key'>, send: (key: string) => Promise<WorkflowDefinition|WorkflowExecution>) {
    if (locked || actionLock.current) return;
    actionLock.current = true; setBusy(true); setError(null);
    const intent: WorkflowIntent = { ...spec, key: crypto.randomUUID() };
    let sent = false;
    try {
      const old = loadIntent(scope);
      if (old) { setPending(old); return; }
      sessionStorage.setItem(scope, JSON.stringify(intent)); setPending(intent); sent = true;
      const value = await send(intent.key);
      if (!alive.current) return;
      useResult(value, intent); clear(intent);
    } catch (e) {
      if (!alive.current) return;
      if (!sent) setStorageError(true);
      else if (e instanceof ApiError && !e.uncertain) clear(intent);
      report(e);
    } finally { actionLock.current = false; if (alive.current) setBusy(false); }
  }
  async function receipt() {
    if (!pending || actionLock.current) return;
    actionLock.current = true; setBusy(true); setError(null);
    try {
      const result = await workflowApi.receipt(project, pending);
      if (alive.current) { useResult(result, pending); clear(pending); }
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) report(new Error(uiText('暂未找到原键的已提交回执，可稍后再次核对。')));
      else report(e);
    } finally { actionLock.current = false; if (alive.current) setBusy(false); }
  }
  async function openVersion() {
    if (!run) return;
    const i = ++versionTicket.current;
    try { const v = await workflowApi.version(project, run.definition_id, run.definition_version);
      if (alive.current && i === versionTicket.current && v.project_id === project && v.id === run.definition_id && v.version === run.definition_version) setFixed(v);
    } catch (e) { if (i === versionTicket.current) report(e); }
  }
  return <section className="workflow-workbench"><div className="wf-section-heading"><div><h1>{uiText("工作流画布")}</h1><p>{workflowModels(b,'video').length ? uiText('生成视觉简报与候选图片，选定首帧后可继续制作视频。') : uiText('从创意简报生成候选图片，在项目中选定最终画面。')}</p></div><span className="wf-project">{projectLabel(b.project)}</span></div>
    <div className="wf-view-switch" aria-label={uiText("工作流视图")}><button aria-pressed={tab === 'design'} onClick={() => setTab('design')}>{uiText("流程定义")}</button><button aria-pressed={tab === 'runs'} onClick={() => { setTab('runs'); void list(); }}>{uiText("项目运行")}</button></div>
    {pending && !busy && <div className="wf-notice" role="status"><strong>{uiText("上次操作需要核对回执")}</strong>{pending.action === 'cancel' && <p>{uiText("取消结果待核对。后续节点是否已停止以原回执为准，已受理任务及费用请查看原执行记录。")}</p>}<p>{uiText("原操作键")} {pending.key}</p><button className="button secondary" onClick={receipt}>{uiText("只读核对原回执")}</button></div>}
    {error && <div className="wf-notice" role="alert"><p>{error.message}</p><RequestDetails error={error} receiptOnly={!!pending}/></div>}
    <div className="wf-layout"><aside className="wf-index"><div className="wf-section-heading"><h2>{tab === 'design' ? uiText('项目流程') : uiText('运行记录')}</h2><button className="icon-button" aria-label={uiText("刷新项目流程与运行")} onClick={list}><Icon name="refresh" size={15}/></button></div>
    {tab === 'design' ? <><button className="wf-template" disabled={locked} onClick={() => chooseDefinition('template')}><Icon name="layers"/><span><strong>{uiText("创意简报与选图")}</strong><small>{uiText("使用模板 · v1")}</small></span><Icon name="plus" size={15}/></button>
    <button className="wf-template" disabled={locked || !workflowModels(b,'video').length} onClick={() => chooseDefinition('videoTemplate')}><Icon name="video"/><span><strong>{uiText("创意分镜与首帧视频")}</strong><small>{uiText("四步视频模板 · v2")}</small></span><Icon name="plus" size={15}/></button>
    {!workflowModels(b,'video').length && <p className="wf-note">{uiText("当前目录尚未开放支持图片首帧的视频模型。")}</p>}
    {definitions.map(d => <button className="wf-list-item" key={d.id} aria-pressed={definition?.id === d.id} disabled={locked} onClick={() => chooseDefinition(d)}><strong>{d.graph.name}</strong><small>{uiText("固定版本 v")}{d.version}</small></button>)}</> :
    <>{runs.map(r => <button className="wf-list-item" key={r.id} aria-pressed={r.id === run?.id} disabled={busy} onClick={() => chooseRun(r)}><strong>{r.graph.name}</strong><span>{r.input}</span><small>{workflowStatus[r.id === run?.id ? run.status : r.status] || r.status} · v{r.definition_version}{r.source_workflow_id ? uiText(' · 尝试 ') + r.attempt : ''}</small></button>)}{!runs.length && <p className="wf-note">{uiText("项目尚无运行记录。")}</p>}</>}</aside>
    <div className="wf-detail">{tab === 'design' ? <>{replacement && <div className="wf-notice"><p>{uiText("切换会替换当前未保存的流程编辑。")}</p><button className="button secondary" onClick={() => setReplacement(null)}>{uiText("保留编辑")}</button> <button className="text-button" onClick={() => edit(replacement)}>{uiText("放弃编辑并切换")}</button></div>}
    {graph ? <><div className="wf-section-heading"><h2>{definition ? uiText('编辑流程') : uiText('实例化模板')}</h2><span>{definition ? uiText('基于 v') + definition.version : uiText('尚未保存')}</span></div>
      <WorkflowEditor b={b} graph={graph} update={setGraph} disabled={locked} report={report}/>
      <button className="button primary" disabled={locked || !dirty || !graph.name.trim()} onClick={() => perform({action:'definition'}, key => workflowApi.save(project, key, definition, graph))}>{uiText("保存固定版本")}</button>
      {definition && <section className="wf-next"><h3>{uiText("从 v")}{definition.version} {uiText("启动")}</h3><label>{uiText("本次创作主题")}<textarea value={brief} disabled={locked} onChange={e => setBrief(e.target.value)} rows={2}/></label>{dirty && <p>{uiText("有未保存的节点编辑，保存后可启动新版本。")}</p>}
      <button className="button primary" disabled={locked || dirty || !brief.trim()} onClick={() => perform({action:'start'}, key => workflowApi.start(project, key, definition, brief.trim()))}>{uiText("启动此版本")}</button></section>}</> :
    <div className="wf-empty"><Icon name="layers" size={34}/><h2>{uiText("选择一个流程开始编辑")}</h2><p>{uiText("使用模板建立新流程，或打开项目中已保存的固定版本。")}</p></div>}</> : run && !detailReady ? <div className="wf-empty"><p>{uiText("正在读取当前项目的运行详情。")}</p><button className="button secondary" onClick={() => void refresh(selected.current)}>{uiText("重新读取详情")}</button></div> : run ?
    <><WorkflowRun key={run.id + ':' + run.version + ':' + run.status} b={b} run={run} expired={expired} locked={locked} perform={perform} report={report} refresh={() => void refresh(run.id)} openVersion={openVersion}/>
    <WorkflowAttemptControls key={run.id} run={run} locked={locked} perform={perform} expired={expired} openSource={openSource}/>
    {fixed && <section className="wf-fixed"><h3>{fixed.graph.name} {uiText("· 固定版本 v")}{fixed.version}</h3>{fixed.graph.nodes.map(n => <div key={n.id}><strong>{n.title}</strong><span>{n.model_id}</span><p>{n.prompt}</p></div>)}</section>}</> :
    <div className="wf-empty"><Icon name="folder" size={34}/><h2>{uiText("从项目记录打开运行")}</h2><p>{uiText("已保存的人工选图步骤可在其他授权会话继续。")}</p></div>}</div></div></section>;
}

