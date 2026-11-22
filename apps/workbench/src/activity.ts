import { uiText } from './locale';
import type { AuditEvent } from './types';
import { statusName } from './types';

const actions: Record<string, string> = { get 'conversation.created'() { return uiText('创建多轮对话'); }, get 'conversation.turn_created'() { return uiText('发送对话新一轮'); }, get 'project.created'() { return uiText('创建项目'); }, get 'run.accepted'() { return uiText('发起灵感对话'); }, get 'task.accepted'() { return uiText('提交媒体创作'); }, get 'run.cancelled'() { return uiText('停止对话生成'); }, get 'task.cancelled'() { return uiText('取消媒体任务'); }, get 'task.submit_unknown'() { return uiText('原媒体提交受理不明'); }, get 'task.reconcile_requested'() { return uiText('重新核对原任务'); }, get 'draft.saved'() { return uiText('保存项目草稿'); }, get 'canvas.saved'() { return uiText('保存自由画布'); }, get 'reference.uploaded'() { return uiText('上传参考图片'); }, get 'asset.updated'() { return uiText('更新素材名称与标签'); }, get 'asset.trashed'() { return uiText('素材移入回收站'); }, get 'asset.restored'() { return uiText('从回收站恢复素材'); } };
const statuses = new Set(['QUEUED', 'SUBMITTING', 'RUNNING', 'INGESTING', 'SUCCEEDED', 'COMPLETED', 'FAILED', 'CANCELLED', 'PARTIAL', 'SUBMIT_UNKNOWN', 'RECONCILE_REQUIRED', 'PROVIDER_STATE_UNKNOWN', 'INTERRUPTED']);
export function activityTitle(event: AuditEvent): string { return actions[event.action] || uiText('其他项目活动'); }
export function activityDetails(event: AuditEvent): string {
  const detail = event.details || {}, parts: string[] = [];
  if (detail.kind === 'image') parts.push(uiText('图像')); else if (detail.kind === 'video') parts.push(uiText('视频'));
  if (detail.from_status && statuses.has(detail.from_status)) parts.push(statusName(detail.from_status));
  if (detail.to_status && statuses.has(detail.to_status)) parts.push('→ ' + statusName(detail.to_status));
  if (Number.isSafeInteger(detail.revision) && detail.revision! >= 0) parts.push(uiText('版本 v') + detail.revision);
  if (Number.isSafeInteger(detail.node_count) && detail.node_count! >= 0 && detail.node_count! <= 40) parts.push(detail.node_count + uiText(' 个节点'));
  return parts.join(' · ');
}
export function mergeActivity(current: AuditEvent[], incoming: AuditEvent[], projectId: string): AuditEvent[] {
  const byId = new Map(current.filter(item => item.project_id === projectId).map(item => [item.id, item]));
  incoming.filter(item => item.project_id === projectId).forEach(item => byId.set(item.id, item));
  return [...byId.values()].sort((a, b) => b.created_at.localeCompare(a.created_at) || b.id.localeCompare(a.id));
}
