import { uiText } from './locale';
import { useEffect, useState } from 'react';
import type { ApiError } from './api';
import { ACCOUNT_RETRY_SECONDS, MAX_RETRY_SECONDS, requestAdvice } from './requestFeedback';
import { useCountdown } from './useCountdown';
import { Icon } from './components';

export function RequestDetails({ error, unknown = false, sensitive = false, receiptOnly = false }: { error: ApiError | null; unknown?: boolean; sensitive?: boolean; receiptOnly?: boolean }) {
  const seconds = useCountdown(error?.retryAt || 0, sensitive ? ACCOUNT_RETRY_SECONDS : MAX_RETRY_SECONDS), [copied, setCopied] = useState('');
  useEffect(() => setCopied(''), [error?.requestId]);
  if (!error) return null;
  const advice = receiptOnly ? uiText('请保留原操作键，只读核对原回执。') : sensitive ? unknown ? uiText('敏感操作不保留可重发的正文。请核对账户状态，必要时重新登录或申请新链接。') : error.status === 429 ? uiText('请等待冷却结束后再手动操作；不会自动发送敏感请求。') : '' : requestAdvice(error, unknown);
  return <div className="request-details">{advice && <p>{advice}</p>}{error.retryAt > 0 && <span className="request-cooldown" role="status">{receiptOnly ? seconds ? uiText("{0} 秒后可再次核对回执", seconds) : uiText('可再次核对原回执') : sensitive ? seconds ? uiText("冷却剩余 {0} 秒", seconds) : uiText('冷却已结束，可核对账户状态') : seconds ? uiText("{0} 秒后可手动重试", seconds) : uiText('冷却已结束，可手动重试')}</span>}{error.requestId && <div className="request-trace"><span>{uiText("请求编号")} <code>{error.requestId}</code></span><button className="text-button" onClick={async () => { try { await navigator.clipboard.writeText(error.requestId!); setCopied('已复制'); } catch { setCopied('复制失败，请选择编号复制'); } }} aria-label={uiText("复制请求编号")}><Icon name="copy" size={12}/>{uiText(copied || '复制')}</button></div>}</div>;
}
