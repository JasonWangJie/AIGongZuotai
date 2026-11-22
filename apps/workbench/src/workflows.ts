import { uiText } from './locale';
import type { Bootstrap, MediaParameters, Model, ParameterPreset, Run, Task } from './types';
import { buildModelTaskInput, modelCapability } from './modelCatalog';

export type WorkflowKind = 'text' | 'image' | 'approval' | 'video';
export type WorkflowNode = { id: string; kind: WorkflowKind; title: string; prompt?: string; model_id?: string; model_version?: string; parameters?: MediaParameters };
export type WorkflowEdge = { from: string; from_port: string; to: string; to_port: string };
export type WorkflowGraph = { name: string; template_id: string; template_version: string; nodes: WorkflowNode[]; edges: WorkflowEdge[] };
export type WorkflowDefinition = { id: string; project_id: string; version: number; graph: WorkflowGraph };
export type WorkflowCandidate = { asset_id: string; artifact_id: string; task_id: string; revision: number; content_version?: string };
export function sameWorkflowContent(a?: WorkflowCandidate | null, b?: WorkflowCandidate | null): boolean {
  return !!a && !!b && a.asset_id === b.asset_id && a.artifact_id === b.artifact_id && a.task_id === b.task_id && a.content_version === b.content_version;
}
export function sameWorkflowCandidate(a?: WorkflowCandidate | null, b?: WorkflowCandidate | null): boolean {
  return sameWorkflowContent(a, b) && a?.revision === b?.revision;
}
export type WorkflowExecution = {
  id: string; project_id: string; definition_id: string; definition_version: number; version: number; source_workflow_id?: string; source_version?: number; from_node?: string; attempt?: number;
  input: string; status: string; graph: WorkflowGraph; candidates: WorkflowCandidate[]; selected?: WorkflowCandidate;
  text_run_id?: string; image_task_id?: string; video_task_id?: string; video_task?: Task; video_artifacts?: WorkflowCandidate[]; text_run?: Run; image_task?: Task; error_code?: string;
};
export const workflowKind = { get text() { return uiText('文本生成'); }, get image() { return uiText('图像生成'); }, get approval() { return uiText('人工选图'); }, get video() { return uiText('首帧视频'); } };
export const workflowStatus: Record<string, string> = { get TEXT_READY() { return uiText('等待确认生成文本'); }, get TEXT_ACTIVE() { return uiText('正在生成文本'); }, get IMAGE_READY() { return uiText('等待确认生成图片'); }, get IMAGE_ACTIVE() { return uiText('正在生成图片'); }, get WAITING_APPROVAL() { return uiText('等待人工选图'); }, get VIDEO_READY() { return uiText('等待确认生成视频'); }, get VIDEO_ACTIVE() { return uiText('正在生成视频'); }, get COMPLETED() { return uiText('已完成'); }, get FAILED() { return uiText('运行失败'); }, get CANCELLED() { return uiText('流程已取消'); }, get REVIEW() { return uiText('待核实'); } };
export function workflowModels(b: Bootstrap, kind: WorkflowKind) {
  return b.models.filter(m => m.kind === (kind === 'text' ? 'chat' : kind) && (kind !== 'video' || m.capabilities?.video_first_frame_supported === true) && modelCapability(b, m).enabled && !!m.version);
}
export function workflowParameters(b: Bootstrap, model: Model, preset?: ParameterPreset, count = 1) {
  const cap = modelCapability(b, model);
  const selected = preset || cap.parameter_presets[0];
  return selected ? buildModelTaskInput(b.project.id, model, '', selected, Math.min(count, cap.max_count)).parameters : undefined;
}
export function templateGraph(b: Bootstrap, withVideo = false): WorkflowGraph {
  const text = workflowModels(b, 'text')[0], image = workflowModels(b, 'image')[0];
  const graph: WorkflowGraph = { name: uiText('创意简报与选图'), template_id: 'text-image-approval', template_version: '1', nodes: [
    { id: 'brief', kind: 'text', title: uiText('视觉简报'), prompt: uiText('根据创作主题，写一段可直接用于图像生成的画面描述。'), model_id: text?.id, model_version: text?.version },
    { id: 'frames', kind: 'image', title: uiText('生成候选图'), prompt: uiText('按视觉简报创作画面。'), model_id: image?.id, model_version: image?.version, parameters: image && workflowParameters(b, image, undefined, 2) },
    { id: 'pick', kind: 'approval', title: uiText('选定画面') },
  ], edges: [{ from: 'brief', from_port: 'text', to: 'frames', to_port: 'prompt' }, { from: 'frames', from_port: 'images', to: 'pick', to_port: 'images' }] };
  if (withVideo) {
    graph.name = uiText('创意分镜与首帧视频'); graph.template_id = 'text-image-approval-video'; graph.template_version = '2';
    const video = workflowModels(b, 'video')[0];
    graph.nodes.push({id:'movie',kind:'video',title:uiText('生成首帧视频'),prompt:uiText('基于已选画面与分镜描述，生成一个连续镜头。'),model_id:video?.id,model_version:video?.version,parameters:video && workflowParameters(b,video)});
    graph.edges.push({from:'pick',from_port:'selected_image',to:'movie',to_port:'first_frame'},{from:'brief',from_port:'text',to:'movie',to_port:'prompt'});
  }
  return graph;
}
export function inputEdge(nodes: WorkflowNode[], target: WorkflowNode, sourceId: string, port?: string): WorkflowEdge | undefined {
  const source = nodes.find(n => n.id === sourceId);
  if (source?.kind === 'approval' && target.kind === 'video' && port === 'first_frame') return {from:source.id,from_port:'selected_image',to:target.id,to_port:port};
  if (source?.kind === 'text' && target.kind === 'video' && port === 'prompt') return {from:source.id,from_port:'text',to:target.id,to_port:port};
  if (source?.kind === 'text' && target.kind === 'image') return { from: source.id, from_port: 'text', to: target.id, to_port: 'prompt' };
  if (source?.kind === 'image' && target.kind === 'approval') return { from: source.id, from_port: 'images', to: target.id, to_port: 'images' };
}
export type WorkflowIntent = { key: string; action: 'definition' | 'start' | 'execute' | 'confirm' | 'rerun' | 'cancel'; run?: string; node?: string };
export function workflowScope(b: Bootstrap, user: string) { return 'xingtu-workflow-receipt:' + JSON.stringify([user, b.workspace.id, b.project.id]); }
export function loadIntent(scope: string): WorkflowIntent | null {
  const raw = sessionStorage.getItem(scope);
  if (!raw) return null;
  const item = JSON.parse(raw) as WorkflowIntent;
  if (!item || typeof item.key !== 'string' || !['definition','start','execute','confirm','rerun','cancel'].includes(item.action)) throw new Error(uiText('工作流回执索引损坏，请检查当前浏览器的页面存储。'));
  return item;
}

