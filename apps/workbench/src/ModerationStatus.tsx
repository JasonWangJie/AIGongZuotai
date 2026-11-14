import type { ModerationCapability, ModerationSummary } from './moderation';
import { moderationDisplayState, moderationText } from './moderation';
import './moderation.css';

export function ModerationStatus({ value, status, onRefresh }: { value?: ModerationSummary | null; status?: string; onRefresh?: () => void }) {
  const state = moderationDisplayState(value, status);
  if (!state) return null;
  const waiting = state === 'PENDING';
  const title = moderationText(value, status);
  const detail = waiting ? '回复完整生成并通过检查后，才会显示全文。无需重新发送。' : state === 'BLOCKED' ? '本轮回复暂不提供，也不能复制或继续生成。不会自动发起新请求。' : state === 'REVIEW' ? '检查未完成或执行已中断，内容暂不开放。刷新只核对原运行，不重新生成。' : '本轮全文已通过受理时策略的文本检查。';
  return <div className={'moderation-status moderation-' + (waiting ? 'pending' : state.toLowerCase())} role="status"><span className="moderation-symbol" aria-hidden="true">{waiting ? '◌' : state === 'ALLOWED' ? '✓' : state === 'BLOCKED' ? '⊘' : '◇'}</span><div><strong>{title}</strong><p>{detail}</p></div>{onRefresh && state !== 'ALLOWED' && <button type="button" className="text-button" onClick={onRefresh}>刷新状态</button>}</div>;
}
export function ModerationDelivery({ value, media = false }: { value?: ModerationCapability; media?: boolean }) {
  if (!value?.enabled) return null;
  return <aside className={'moderation-delivery' + (media ? ' moderation-media' : '')} aria-label={media ? '媒体审核能力说明' : '文本交付方式'}><span className="moderation-delivery-mark" aria-hidden="true">◇</span><div><div className="moderation-delivery-heading"><strong>{media ? '当前仅开放文本审核链路' : '完整生成，通过后展示'}</strong><small>{value.mode === 'rules' ? 'DEMO · 本地规则' : value.mode === 'http' ? 'HTTP · 审核适配' : '审核配置待核对'}</small></div><p>{media ? '图片与视频的新生成暂未开放；上传和历史素材不代表已经过内容审核。原任务及待确认请求仍可按原记录核对。' : '本轮不会逐字展示未检查的内容。服务端完成全文检查后才开放回复，等待期间不需要再次发送。'}{value.mode === 'rules' && ' 当前为演示规则，不代表真实审核服务的判断效果。'}</p></div></aside>;
}
