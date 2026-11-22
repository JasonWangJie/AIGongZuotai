import { uiText } from './locale';
import { mergeModeration, sameModerationDecision, validModeration } from './moderation';
import type { ModerationCapability, ModerationSummary } from './moderation';
import type { ReferenceAsset, Task } from './types';

export function mediaReviewEnabled(capability?: ModerationCapability): boolean { return capability?.enabled === true && ['fixtures', 'http'].includes(capability.media_mode || ''); }
export function mediaContentAllowed(summary?: ModerationSummary | null): boolean { return summary == null || validModeration(summary) && summary.state === 'ALLOWED'; }
export function taskMediaAllowed(task: Pick<Task, 'status' | 'moderation'>): boolean { return task.moderation == null || mediaContentAllowed(task.moderation) && task.moderation.phase === 'output' && ['SUCCEEDED', 'PARTIAL'].includes(task.status); }
/** The server attests an original handle with no candidate. This resumes query only, never generation or re-review. */
export function canQueryModeratedTask(task: Pick<Task, 'status' | 'moderation' | 'can_reconcile'>): boolean { return task.can_reconcile === true && task.status === 'RECONCILE_REQUIRED' && validModeration(task.moderation) && task.moderation.phase === 'output' && task.moderation.state === 'PENDING'; }

/** Content permissions have their own version and cannot be cleared by an older metadata mutation or an omitted summary. */
export function mergeMediaProjection<T extends { moderation?: ModerationSummary | null }>(previous: T | undefined, incoming: T, acceptIncoming = true): { moderation?: ModerationSummary; source?: T } {
  const moderation = mergeModeration(previous?.moderation, incoming.moderation);
  const newerDecision = !!moderation && sameModerationDecision(moderation, incoming.moderation) && (!previous?.moderation || validModeration(moderation) && validModeration(previous.moderation) && moderation.version > previous.moderation.version);
  const source = (acceptIncoming || newerDecision) && sameModerationDecision(moderation, incoming.moderation) ? incoming : previous && sameModerationDecision(moderation, previous.moderation) ? previous : undefined;
  return { ...(moderation ? { moderation } : {}), ...(source ? { source } : {}) };
}
export function mergeReferenceAsset(previous: ReferenceAsset | undefined, incoming: ReferenceAsset): ReferenceAsset {
  if (previous && (previous.id !== incoming.id || previous.project_id !== incoming.project_id)) return previous;
  const projection = mergeMediaProjection(previous, incoming), allowed = mediaContentAllowed(projection.moderation);
  return { ...incoming, ...(projection.moderation ? { moderation: projection.moderation } : {}), url: allowed ? projection.source?.url || '' : '', thumbnail_url: allowed ? projection.source?.thumbnail_url || '' : '' };
}
export function mediaModerationErrorMessage(code?: string): string | undefined {
  if (!code?.startsWith('MEDIA_MODERATION_')) return undefined;
  return ({ MEDIA_MODERATION_INPUT_BLOCKED: uiText('图片、视频描述或参考内容未通过检查，本次新请求未受理。请调整描述或更换参考图后手动提交。'), MEDIA_MODERATION_INPUT_REVIEW: uiText('媒体内容需要进一步核实，本次新请求未受理。请保留输入并联系维护者，或更换内容。'), MEDIA_MODERATION_INPUT_INVALID: uiText('媒体内容不符合当前检查的格式或大小要求，本次新请求未受理。'), MEDIA_MODERATION_INVALID: uiText('媒体内容不符合当前检查的格式或大小要求，本次新请求未受理。'), MEDIA_MODERATION_BUSY: uiText('媒体检查繁忙，本次新请求未受理。请等待冷却后手动操作。'), MEDIA_MODERATION_UNAVAILABLE: uiText('媒体检查暂不可用，本次新请求未受理。输入或原文件仍保留，不会自动重新提交。'), MEDIA_MODERATION_CONTEXT_CHANGED: uiText('参考素材或生成条件已变化，本次新请求未受理。请刷新并核对原有选择。') } as Record<string, string>)[code] || uiText('媒体内容检查需要处理。请刷新原记录核对，必要时提供请求编号；不会自动重新生成。');
}
export function mediaUploadRetryAllowed(errorCode: string | undefined, wasUnknown: boolean): boolean { return wasUnknown || !['MEDIA_MODERATION_INPUT_BLOCKED', 'MEDIA_MODERATION_INPUT_REVIEW', 'MEDIA_MODERATION_INPUT_INVALID', 'MEDIA_MODERATION_INVALID'].includes(errorCode || ''); }
export function definiteModerationUnavailable(status: number, code?: string): boolean { return status === 503 && ['MODERATION_UNAVAILABLE', 'MEDIA_MODERATION_UNAVAILABLE'].includes(code || ''); }
