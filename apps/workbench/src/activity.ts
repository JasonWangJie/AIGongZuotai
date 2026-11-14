import type { AuditEvent } from './types';
import { statusName } from './types';

const actions: Record<string, string> = { 'conversation.created': '创建多轮对话', 'conversation.turn_created': '发送对话新一轮', 'project.created': '创建项目', 'run.accepted': '发起灵感对话', 'task.accepted': '提交媒体创作', 'run.cancelled': '停止对话生成', 'task.cancelled': '取消媒体任务', 'task.reconcile_requested': '重新核对原任务', 'draft.saved': '保存项目草稿', 'reference.uploaded': '上传参考图片', 'asset.updated': '更新素材名称与标签', 'asset.trashed': '素材移入回收站', 'asset.restored': '从回收站恢复素材' };
const statuses = new Set(['QUEUED', 'SUBMITTING', 'RUNNING', 'INGESTING', 'SUCCEEDED', 'COMPLETED', 'FAILED', 'CANCELLED', 'PARTIAL', 'SUBMIT_UNKNOWN', 'RECONCILE_REQUIRED', 'PROVIDER_STATE_UNKNOWN', 'INTERRUPTED']);
export function activityTitle(event: AuditEvent): string { return actions[event.action] || '其他项目活动'; }
export function activityDetails(event: AuditEvent): string {
  const detail = event.details || {}, parts: string[] = [];
  if (detail.kind === 'image') parts.push('图像'); else if (detail.kind === 'video') parts.push('视频');
  if (detail.from_status && statuses.has(detail.from_status)) parts.push(statusName(detail.from_status));
  if (detail.to_status && statuses.has(detail.to_status)) parts.push('→ ' + statusName(detail.to_status));
  if (Number.isSafeInteger(detail.revision) && detail.revision! >= 0) parts.push('版本 v' + detail.revision);
  if (Number.isSafeInteger(detail.node_count) && detail.node_count! >= 0 && detail.node_count! <= 40) parts.push(detail.node_count + ' 个节点');
  return parts.join(' · ');
}
export function mergeActivity(current: AuditEvent[], incoming: AuditEvent[], projectId: string): AuditEvent[] {
  const byId = new Map(current.filter(item => item.project_id === projectId).map(item => [item.id, item]));
  incoming.filter(item => item.project_id === projectId).forEach(item => byId.set(item.id, item));
  return [...byId.values()].sort((a, b) => b.created_at.localeCompare(a.created_at) || b.id.localeCompare(a.id));
}
