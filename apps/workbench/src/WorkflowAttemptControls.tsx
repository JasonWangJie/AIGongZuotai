import { uiText } from './locale';
import { useEffect, useState } from 'react';
import { BillingStatus } from './BillingComponents';
import { ApiError } from './api';
import { RequestDetails } from './RequestDetails';
import { statusName } from './types';
import { workflowApi } from './workflowApi';
import type { RerunInput, RerunPreview } from './workflowApi';
import { affectedWorkflowNodes, originalNodeResult } from './workflowAttempts';
import { workflowKind } from './workflows';
import type { WorkflowExecution } from './workflows';
import type { RunAction } from './WorkflowRun';

const cancelStates = ['TEXT_READY','TEXT_ACTIVE','IMAGE_READY','IMAGE_ACTIVE','WAITING_APPROVAL','VIDEO_READY','VIDEO_ACTIVE','REVIEW'];
const rerunStates = ['COMPLETED','FAILED','CANCELLED'];
function previewMatches(run: WorkflowExecution, id: string, p: RerunPreview) {
  if (p.source_workflow_id !== run.id || p.source_version !== run.version || p.from_node !== id || !Array.isArray(p.affected_nodes) || !Array.isArray(p.inherited_nodes)) return false;
  const affected = affectedWorkflowNodes(run, id).map(n => n.id);
  const inherited = run.graph.nodes.filter(n => !affected.includes(n.id));
  return affected.length > 0 && p.affected_nodes.length === affected.length && affected.every(id => p.affected_nodes.includes(id)) &&
    inherited.length === p.inherited_nodes.length && inherited.every(n => p.inherited_nodes.includes(n.id));
}
type Props = {run: WorkflowExecution; locked: boolean; perform: RunAction; expired: () => void; openSource: (id:string)=>void};
export function WorkflowAttemptControls({run, locked, perform, expired, openSource}: Props) {
  const [opened, setOpened] = useState(false);
  return <section className="wf-attempts">
    {run.source_workflow_id && <div className="wf-run-meta"><strong>{uiText("第")} {run.attempt} {uiText("次尝试")}</strong>{run.source_version && <span>{uiText("基于原运行 v")}{run.source_version}</span>}<button className="text-button" onClick={()=>openSource(run.source_workflow_id!)}>{uiText("查看原运行与成果")}</button></div>}
    <button type="button" className="button secondary" aria-expanded={opened} onClick={()=>setOpened(!opened)}>{opened?uiText('收起运行操作'):uiText('查看重跑与取消')}</button>
    {opened && <AttemptPanel key={run.id} run={run} locked={locked} perform={perform} expired={expired} openSource={openSource}/>}
  </section>;
}
function AttemptPanel({run,locked,perform,expired}: Props) {
  const [id, setID] = useState(''), [preview,setPreview] = useState<RerunPreview|null>(null), [error,setError]=useState<ApiError|null>(null);
  const [reload,setReload]=useState(0), [prompt,setPrompt]=useState(''), [input,setInput]=useState('');
  const [cancelVersion,setCancelVersion]=useState<number|null>(null);
  const node = run.graph.nodes.find(n=>n.id===id), canRerun = rerunStates.includes(run.status), canCancel=cancelStates.includes(run.status);
  useEffect(()=>{
    setPreview(null);setError(null);
    if(!canRerun || !id) return;
    const c=new AbortController();
    workflowApi.preview(run.project_id,run.id,id,c.signal).then(p=>{
      if(c.signal.aborted) return;
      if(!previewMatches(run,id,p)) throw new Error(uiText('预览与当前运行范围不一致，请重新读取运行后核对。'));
      setPreview(p);
    }).catch(e=>{if(!c.signal.aborted){setError(e instanceof ApiError ? e : new ApiError(e instanceof Error?e.message:uiText('无法核对重跑范围')));if(e instanceof ApiError && e.status===401) expired();}});
    return ()=>c.abort();
  },[id,run.id,run.version,run.project_id,canRerun,reload]);
  function choose(value:string) {
    setID(value);setPreview(null);setError(null);
    setPrompt(run.graph.nodes.find(n=>n.id===value)?.prompt || '');setInput(run.input);
  }
  async function rerun() {
    if(!canRerun || !node || !preview || !previewMatches(run,id,preview) || locked) return;
    const body:RerunInput={expected_version:preview.source_version,from_node:id};
    if(node.kind!=='approval' && prompt!==(node.prompt || '')) body.prompt=prompt;
    if(node.kind==='text' && input!==run.input) body.input=input;
    await perform({action:'rerun',run:run.id,node:id}, async key=>{
      const next=await workflowApi.rerun(run.project_id,key,run.id,body);
      if(next.id===run.id || next.source_workflow_id!==run.id || next.from_node!==id) throw new Error(uiText('新尝试的来源与原运行不匹配，请核对原回执。'));
      return next;
    });
  }
  async function cancel() {
    if(locked || !canCancel || cancelVersion!==run.version) return;
    await perform({action:'cancel',run:run.id},async key=>{
      const next=await workflowApi.cancel(run.project_id,key,run.id,run.version);
      if(next.id!==run.id || next.status!=='CANCELLED') throw new Error(uiText('取消回执与原运行不匹配，请只读核对。'));
      return next;
    });
  }
  return <div className="wf-attempt-panel"><h3>{uiText("本次运行的执行记录")}</h3><code>{run.id}</code>
    {run.graph.nodes.filter(n=>n.kind!=='approval').map(n=>{
      const child=n.kind==='text'?run.text_run:n.kind==='image'?run.image_task:run.video_task;
      return <div className="wf-child-evidence" key={n.id}><strong>{n.title}</strong><span>{child ? statusName(child.status) : originalNodeResult(run,n.kind)}</span>
      {child && <><code>{child.id}</code>{child.moderation && <small>{uiText("审核状态")} {child.moderation.state}</small>}<BillingStatus value={child.billing}/></>}</div>;
    })}
    {canRerun && <fieldset disabled={locked}><legend>{uiText("从指定节点创建新尝试")}</legend><label>{uiText("重跑起点")}<select value={id} onChange={e=>choose(e.target.value)}><option value="">{uiText("选择要重新执行的节点")}</option>{run.graph.nodes.map(n=><option key={n.id} value={n.id}>{n.title} · {workflowKind[n.kind]}</option>)}</select></label>
      {id && !preview && !error && <p role="status">{uiText("正在读取原运行的重跑范围…")}</p>}
      {error && <div className="wf-notice" role="alert"><p>{error.message}</p><RequestDetails error={error}/><button className="text-button" onClick={()=>setReload(i=>i+1)}>{uiText("重新核对可用范围")}</button></div>}
      {preview && node && <><div className="wf-range"><h4>{uiText("将重新执行")}</h4>{preview.affected_nodes.map(k=>{const n=run.graph.nodes.find(v=>v.id===k)!;return <div key={k}><strong>{n.title}</strong><small>{originalNodeResult(run,n.kind)}</small></div>;})}
      {!!preview.inherited_nodes.length && <><h4>{uiText("沿用原运行结果")}</h4>{preview.inherited_nodes.map(k=>{const n=run.graph.nodes.find(v=>v.id===k)!;return <div key={k}><strong>{n.title}</strong><small>{originalNodeResult(run,n.kind)}</small></div>;})}</>}</div>
      {node.kind!=='approval' && <label>{uiText("新尝试的起点指令")}<textarea rows={2} value={prompt} onChange={e=>setPrompt(e.target.value)}/></label>}
      {node.kind==='text' && <label>{uiText("新尝试的创作主题")}<textarea rows={2} value={input} onChange={e=>setInput(e.target.value)}/></label>}
      <p>{uiText("原运行、原产物和已执行费用保留。新尝试的生成步骤需分别确认，启用创作点时重新报价。")}</p>
      <button className="button primary" disabled={node.kind==='text' && !input.trim()} onClick={rerun}>{uiText("确认创建新尝试")}</button></>}
    </fieldset>}
    {canCancel && <div className="wf-cancel">{cancelVersion!==run.version ? <button className="button secondary" disabled={locked} onClick={()=>setCancelVersion(run.version)}>{uiText("准备取消此运行")}</button> :
      <><p>{uiText("确定停止后续节点？已经受理的生成、审核和费用按原子任务核对。已结算金额保留，待核实冻结按原账本处理。供应商可能仍在处理已提交的任务。")}</p><button className="button secondary" disabled={locked} onClick={()=>setCancelVersion(null)}>{uiText("保留运行")}</button> <button className="button danger" disabled={locked} onClick={cancel}>{uiText("确认取消此运行")}</button></>}</div>}
    {run.status==='CANCELLED' && <p>{uiText("后续节点已停止，刷新运行可核对原任务、审核和费用的最新状态。")}</p>}
  </div>;
}

