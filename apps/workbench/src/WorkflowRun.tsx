import { uiText } from './locale';
import { useEffect, useState } from 'react';
import type { Bootstrap } from './types';
import { QuoteConfirmation } from './BillingComponents';
import { billingEnabled } from './billing';
import type { QuoteRequest } from './billing';
import { useBillingQuote } from './useBillingQuote';
import { workflowApi } from './workflowApi';
import { sameWorkflowCandidate, sameWorkflowContent, workflowStatus } from './workflows';
import type { WorkflowCandidate, WorkflowExecution, WorkflowIntent } from './workflows';
import { ContentAppealLink } from './ContentAppealLink';
import { Icon } from './components';
import { statusName } from './types';
import { mediaContentAllowed } from './mediaModeration';
import { moderationAllowsText } from './moderation';

export type RunAction = (intent: Omit<WorkflowIntent, 'key'>, send: (key: string) => Promise<WorkflowExecution>) => Promise<void>;
export function WorkflowRun({ b, run, locked, expired, perform, refresh, openVersion, report }: { b: Bootstrap; run: WorkflowExecution; locked: boolean; expired: () => void; perform: RunAction; refresh: () => void; openVersion: () => void; report: (error: unknown) => void }) {
  const nodeKind = run.status === 'TEXT_READY' ? 'text' : run.status === 'IMAGE_READY' ? 'image' : run.status === 'WAITING_APPROVAL' ? 'approval' : run.status === 'VIDEO_READY' ? 'video' : null;
  const node = run.graph.nodes.find(n => n.kind === nodeKind);
  const [input, setInput] = useState<QuoteRequest | null>(null), [loadError, setLoadError] = useState(false), [attempt, setAttempt] = useState(0);
  const [candidate, setCandidate] = useState<WorkflowCandidate | null>(null);
  const quote = useBillingQuote(b, input, expired);
  const [showQuote, setShowQuote] = useState(false);
  const project = b.project.id;
  const selectedVisible = !!run.selected?.content_version && run.candidates?.some(c => sameWorkflowContent(c, run.selected));
  useEffect(() => {
    if (!node || node.kind === 'approval' || node.kind === 'video' && !selectedVisible) return;
    const c = new AbortController(); setLoadError(false); setInput(null);
    workflowApi.input(project, run.id, node.id, c.signal).then(value => {
      const child = value.resource_type === 'run' ? value.run_input : value.resource_type === 'task' ? value.task_input : null;
      if (!child || child.project_id !== project || value.resource_type !== (node.kind === 'text' ? 'run' : 'task')) throw new Error(uiText('节点输入与项目不匹配'));
      if (child.workflow_attempt_id !== (run.source_workflow_id ? run.id : undefined)) throw new Error(uiText('节点输入与本次尝试不匹配，请重新读取。'));
      if (node.kind === 'video' && (value.resource_type !== 'task' || value.task_input.kind !== 'video' || value.task_input.operation !== 'first_frame' ||
          value.task_input.inputs.workflow_id !== run.id || !run.selected || !run.selected.content_version ||
          value.task_input.model_id !== node.model_id || value.task_input.model_version !== node.model_version ||
          !sameWorkflowCandidate(value.task_input.inputs.first_frame, run.selected))) {
        throw new Error(uiText('视频节点的冻结输入与原选图不一致，请刷新运行。'));
      }
      if (!c.signal.aborted) setInput(value);
    }).catch(error => { if (!c.signal.aborted) { setLoadError(true); report(error); } });
    return () => c.abort();
  }, [project, run.id, run.version, node?.id, attempt, selectedVisible]);
  function execute(quoteId?: string) {
    if (!node || !input || locked || (billingEnabled(b) && (!quote.valid || !quoteId || quoteId !== quote.accepted?.quote.id))) return;
    void perform({action: 'execute', run: run.id, node: node.id}, key => workflowApi.execute(project, key, run.id, node.id, run.version, quoteId));
  }
  function confirm() {
    if (!node || !candidate || locked || !run.candidates?.some(c => sameWorkflowCandidate(c, candidate))) return;
    void perform({action: 'confirm', run: run.id, node: node.id}, key => workflowApi.confirm(project, key, run.id, node.id, run.version, candidate.asset_id, candidate.revision));
  }
  const content = (c: WorkflowCandidate, kind: string) => '/api/v1/library/' + encodeURIComponent(c.asset_id) + '/' + kind + '?project_id=' + encodeURIComponent(project);
  return <section className="wf-run"><div className="wf-section-heading"><div><small>{uiText("运行详情")}</small><h2>{run.graph.name}</h2></div><button className="button secondary" onClick={refresh}><Icon name="refresh" size={14}/>{uiText("刷新进度")}</button></div>
    <div className="wf-run-meta"><span className="wf-status">{workflowStatus[run.status] || run.status}</span><button className="text-button" onClick={openVersion}>{uiText("查看固定版本 v")}{run.definition_version}</button></div>
    <p className="wf-brief">{run.input}</p>
    {run.text_run_id && <details><summary>{uiText("文本 Run ·")} {run.text_run ? statusName(run.text_run.status) : run.text_run_id}</summary><code>{run.text_run_id}</code>{run.text_run && <ContentAppealLink type="run" id={run.text_run.id} project={run.text_run.project_id} status={run.text_run.status} moderation={run.text_run.moderation}/>}{run.text_run && moderationAllowsText(run.text_run.moderation, run.text_run.status) && <p className="wf-brief">{run.text_run.output}</p>}</details>}
    {run.image_task_id && <details><summary>{uiText("图像 Task ·")} {run.image_task ? statusName(run.image_task.status) : run.image_task_id}</summary><code>{run.image_task_id}</code>{run.image_task && <ContentAppealLink type="task" id={run.image_task.id} project={run.image_task.project_id} status={run.image_task.status} moderation={run.image_task.moderation}/>}{run.image_task && mediaContentAllowed(run.image_task.moderation) && <p>{run.image_task.artifacts?.length || 0} {uiText("个已登记产物")}</p>}</details>}
    {run.video_task_id && <details><summary>{uiText("视频 Task ·")} {run.video_task ? statusName(run.video_task.status) : run.video_task_id}</summary><code>{run.video_task_id}</code>{run.video_task && <ContentAppealLink type="task" id={run.video_task.id} project={run.video_task.project_id} status={run.video_task.status} moderation={run.video_task.moderation}/>}{run.video_task?.parameters?.seconds && <p>{run.video_task.parameters.seconds} {uiText("秒 ·")} {run.video_task.parameters.aspect_ratio}</p>}</details>}
    {node && node.kind !== 'approval' && (node.kind !== 'video' || selectedVisible) && <section className="wf-next"><h3>{node.title}</h3>
      {input?.resource_type === 'task' && input.task_input.kind === 'video' && <p>{uiText("使用已选图片作为首帧 ·")} {input.task_input.parameters.seconds} {uiText("秒 ·")} {input.task_input.parameters.aspect_ratio}</p>}
      {input ? <><details><summary>{uiText("核对本步生成输入")}</summary><p className="wf-brief">{input.resource_type === 'run' ? input.run_input.input : input.resource_type === 'task' ? input.task_input.inputs.prompt : ''}</p></details>
      {billingEnabled(b) ? showQuote ? <QuoteConfirmation accepted={quote.accepted} valid={quote.valid} error={quote.error} loading={quote.loading} disabled={locked} onConfirm={q => execute(q.quote.id)} onRefresh={quote.fetchQuote} onDismiss={() => { quote.clear(); setShowQuote(false); }}/> : <button className="button primary" disabled={locked} onClick={() => { setShowQuote(true); void quote.fetchQuote(); }}>{uiText("获取本步报价")}</button> :
      <button className="button primary" disabled={locked} onClick={() => execute()}>{uiText("确认生成")}{node.kind === 'text' ? uiText('文本') : node.kind === 'video' ? uiText('视频') : uiText('图片')}</button>}</> : loadError ? <button className="button secondary" onClick={() => setAttempt(n => n + 1)}>{uiText("重读节点输入")}</button> : <p role="status">{uiText("正在读取节点输入…")}</p>}</section>}
    {(run.status === 'TEXT_ACTIVE' || run.status === 'IMAGE_ACTIVE' || run.status === 'VIDEO_ACTIVE') && <p className="wf-note" role="status">{uiText("正在读取已受理步骤的进度，可离开后从项目运行记录继续。")}</p>}
    {run.status === 'WAITING_APPROVAL' && <fieldset className="wf-choice" disabled={locked}><legend>{uiText("选择本次运行的图片")}</legend><div>{(run.candidates || []).map((c, i) => <label key={c.asset_id}><input type="radio" name="workflow-candidate" checked={sameWorkflowCandidate(candidate, c)} onChange={() => setCandidate(c)}/><img src={content(c, 'thumbnail')} alt={uiText('候选图 ') + (i+1)}/><span>{uiText("候选图")} {i+1} · v{c.revision}</span></label>)}</div><button className="button primary" disabled={!run.candidates?.some(c => sameWorkflowCandidate(c, candidate))} onClick={confirm}>{uiText("确认所选图片")}</button></fieldset>}
    {run.selected && <div className="wf-selected">{selectedVisible && <img src={content(run.selected, 'thumbnail')} alt={uiText("已选定的图片")}/>}<div><strong>{uiText("已选定图片")}</strong><p>{uiText("选图时资产修订 v")}{run.selected.revision}</p>{selectedVisible ? <a href={content(run.selected, 'content')} target="_blank" rel="noreferrer">{uiText("查看原图 ↗")}</a> : <p>{uiText("原选图内容目前不可读取。")}</p>}</div></div>}
    {run.status === 'COMPLETED' && run.video_task && run.video_task.id === run.video_task_id && run.video_task.project_id === project && ['SUCCEEDED','PARTIAL'].includes(run.video_task.status) && mediaContentAllowed(run.video_task.moderation) && run.video_artifacts?.filter(a => a.task_id === run.video_task_id).map(a =>
      <section className="wf-film" key={a.asset_id}><h3>{uiText("视频成品")}</h3><video controls preload="metadata" playsInline src={content(a,'content')} aria-label={uiText("已交付的视频")}/>
      <p>{uiText("项目资产 v")}{a.revision} · <a target="_blank" rel="noreferrer" href={content(a,'content')}>{uiText("打开私有视频 ↗")}</a></p></section>)}
    {run.error_code && <p role="alert">{uiText("步骤停止 ·")} {run.error_code}</p>}
  </section>;
}

