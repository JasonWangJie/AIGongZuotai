import type { BillingSummary, QuotedRequest } from './billing';
import { billingStateName, formatCredits } from './billing';
import type { ApiError } from './api';
import { RequestDetails } from './RequestDetails';
import { useCountdown } from './useCountdown';
import './billing.css';

export function BillingStatus({ value }: { value?: BillingSummary }) {
  if (!value) return null;
  return <div className={'billing-status billing-' + value.state.toLowerCase()}><strong>{billingStateName[value.state] || '结算状态待核对'}</strong><span>{value.state === 'HELD' || value.state === 'REVIEW' ? `冻结 ${formatCredits(value.reserved_micros)}` : `已结算 ${formatCredits(value.charged_micros)}`} 创作点</span>{value.released_micros !== '0' && <small>已释放 {formatCredits(value.released_micros)}</small>}{value.refunded_micros !== '0' && <small>已冲正 {formatCredits(value.refunded_micros)}</small>}{value.state === 'REVIEW' && <p>执行结果与结算分开核对；当前冻结不会自动重复扣除。</p>}</div>;
}

export function QuoteConfirmation({ accepted, valid, loading, error, disabled, onConfirm, onRefresh, onDismiss }: { accepted: QuotedRequest | null; valid: boolean; loading: boolean; error: ApiError | null; disabled: boolean; onConfirm: (accepted: QuotedRequest) => void; onRefresh: () => void; onDismiss: () => void }) {
  const seconds = useCountdown(error?.retryAt || 0);
  if (!accepted && !loading && !error) return null;
  const quote = accepted?.quote, request = accepted?.request;
  const parameters = request?.resource_type === 'task' ? request.task_input.parameters : null;
  return <section className="quote-confirmation" aria-label="本次创作报价" aria-live="polite"><div className="quote-title"><span className="eyebrow">CREATION / CONFIRM</span><button type="button" className="text-button" disabled={disabled} onClick={onDismiss}>收起报价</button></div>{loading ? <p>正在核对当前输入与模型规格，尚未提交生成…</p> : error ? <><strong>暂未取得可确认的报价</strong><p>{error.message}</p><p>仅请求报价，没有发起生成或冻结额度。</p><RequestDetails error={error} unknown={false}/><button type="button" className="button secondary" disabled={seconds > 0 || disabled} onClick={onRefresh}>{seconds ? `${seconds} 秒后可重新报价` : '重新获取报价'}</button></> : quote && accepted ? <><div className="quote-main"><div><span>本次最多冻结</span><strong>{formatCredits(quote.reserve_micros)} <small>创作点</small></strong></div><span className="quote-provider">{quote.provider_mode === 'demo' ? '本地演示额度 · 非付款余额' : '工作区创作额度'}</span></div><p>{parameters ? [parameters.aspect_ratio, parameters.size, parameters.resolution, parameters.seconds ? `${parameters.seconds} 秒` : '', `${parameters.count} 份`].filter(Boolean).join(' · ') : request?.resource_type === 'conversation_run' ? `${request.conversation_input.operation === 'continue' ? '显式继续' : request.conversation_input.edited_message_id ? '编辑并分支' : '当前分支新一轮'} · 服务端上下文预算` : '单轮对话预算'}</p><p>{quote.basis === 'chat_tokens' ? `输入上限 ${quote.max_input_tokens.toLocaleString()}、输出上限 ${quote.max_output_tokens.toLocaleString()} tokens；按可信实际用量结算。` : `按实际交付产物结算，未交付部分按最终状态释放。`}</p><p>{quote.basis === 'chat_tokens' ? `每百万 tokens：输入 ${formatCredits(quote.input_per_million_micros)}、输出 ${formatCredits(quote.output_per_million_micros)} 创作点。` : `每份交付 ${formatCredits(quote.artifact_micros)} 创作点，共请求 ${quote.requested_units} 份。`}</p><p className="quote-expiry">{valid ? `报价有效至 ${new Date(quote.expires_at).toLocaleTimeString('zh-CN')}；输入或规格改变后需重新确认。` : '报价已到期，请重新获取后确认。'} 取消或执行结束不等于自动释放。</p><div className="quote-actions"><button type="button" className="button secondary" disabled={loading || disabled} onClick={onRefresh}>重新报价</button><button type="button" className="button primary" disabled={!valid || disabled || loading} onClick={() => onConfirm(accepted)}>确认额度并提交</button></div></> : null}</section>;
}
