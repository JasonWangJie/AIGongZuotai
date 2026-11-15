import { moderationState } from './moderation';
import type { ModerationCapability, ModerationSummary } from './moderation';
import { mediaReviewEnabled, taskMediaAllowed } from './mediaModeration';
import './moderation.css';

export function MediaModerationStatus({ value, status, subject = 'task', onRefresh, compact = false, canQueryOriginal = false }: { value?: ModerationSummary | null; status?: string; subject?: 'task' | 'reference' | 'asset'; onRefresh?: () => void; compact?: boolean; canQueryOriginal?: boolean }) {
  const state = moderationState(value); if (!state) return null;
  const approved = state === 'ALLOWED' && (subject !== 'task' || taskMediaAllowed({ status: status || '', moderation: value || undefined }));
  const pending = (state === 'PENDING' || state === 'ALLOWED' && !approved) && !['FAILED', 'CANCELLED', 'INTERRUPTED', 'RECONCILE_REQUIRED'].includes(status || '');
  const originalQuery = subject === 'task' && canQueryOriginal && state === 'PENDING' && status === 'RECONCILE_REQUIRED';
  const title = originalQuery ? '原任务查询待恢复' : approved ? '媒体检查已通过' : pending ? subject === 'task' && !['MODERATING', 'INGESTING', 'SAVING', 'SUCCEEDED', 'PARTIAL'].includes(status || '') ? '生成与检查中' : '内容检查中' : state === 'BLOCKED' ? '内容已限制' : '媒体检查待核实';
  const detail = originalQuery ? '尚未取得待检查候选，可继续查询原生成任务；不会重新生成，也不会重审已隔离结果。' : approved ? '此内容通过了登记时的媒体检查，可按当前权限使用。' : pending ? '整组结果通过检查并确认交付后才开放，无需再次提交。' : subject === 'task' ? '本组结果尚未开放。只能刷新原任务状态；不会重新生成或自动重新审核，费用请以冻结状态为准。' : '此素材暂不可预览、下载或用于新的生成。原有输入与未保存编辑仍保留。';
  return <div className={'moderation-status media-moderation-status moderation-' + (approved ? 'allowed' : pending ? 'pending' : state === 'BLOCKED' ? 'blocked' : 'review') + (compact ? ' is-compact' : '')} role="status"><span className="moderation-symbol" aria-hidden="true">{approved ? '✓' : pending ? '◌' : state === 'BLOCKED' ? '⊘' : '◇'}</span><div><strong>{title}</strong>{!compact && <p>{detail}</p>}</div>{onRefresh && !approved && <button type="button" className="text-button" onClick={onRefresh}>刷新状态</button>}</div>;
}
export function MediaModerationNotice({ value, reference = false }: { value?: ModerationCapability; reference?: boolean }) {
  if (!mediaReviewEnabled(value)) return null;
  return <aside className="moderation-delivery" aria-label={reference ? '参考上传检查说明' : '媒体交付检查说明'}><span className="moderation-delivery-mark" aria-hidden="true">◇</span><div><div className="moderation-delivery-heading"><strong>{reference ? '检查通过，才会登记参考图' : '整组检查通过，才会开放结果'}</strong><small>{value?.media_mode === 'fixtures' ? 'DEMO · 本地样本校验' : 'HTTP · 媒体审核适配'}</small></div><p>{reference ? '100%只表示传输结束。服务端完成格式与内容检查后才保存到素材库；未受理时保留本页原文件，可移出并更换图片。' : '描述和有序参考先检查，生成结果整组检查后再交付。等待或隔离期间没有预览和下载，不会自动新建生成。'}{value?.media_mode === 'fixtures' && ' 固定PNG/1秒视频样本，不按所选规格生成。本地样本仅按已登记文件校验，不代表图像或视频的语义审核能力。'}</p></div></aside>;
}
