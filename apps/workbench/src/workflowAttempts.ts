import { uiText } from './locale';
import type { WorkflowExecution, WorkflowKind, WorkflowNode } from './workflows';

const stageOrder: WorkflowKind[] = ['text', 'image', 'approval', 'video'];
export function affectedWorkflowNodes(run: WorkflowExecution, nodeId: string): WorkflowNode[] {
  if (!run.graph.nodes.some(n => n.id === nodeId)) return [];
  const ids = new Set<string>([nodeId]);
  const queue = [nodeId];
  for (let i = 0; i < queue.length; i++) {
    for (const e of run.graph.edges) {
      if (e.from === queue[i] && !ids.has(e.to)) { ids.add(e.to); queue.push(e.to); }
    }
  }
  return run.graph.nodes.filter(n => ids.has(n.id)).sort((a,b) => stageOrder.indexOf(a.kind) - stageOrder.indexOf(b.kind));
}
export function originalNodeResult(run: WorkflowExecution, kind: WorkflowKind): string {
  if (kind === 'text') return run.text_run_id || uiText('尚无文本 Run');
  if (kind === 'image') return run.image_task_id || uiText('尚无图像 Task');
  if (kind === 'video') return run.video_task_id || uiText('尚无视频 Task');
  return run.selected ? run.selected.asset_id + uiText(' · 选图时修订 v') + run.selected.revision : uiText('尚未选定图片');
}

