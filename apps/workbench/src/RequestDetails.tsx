import { useEffect, useState } from 'react';
import type { ApiError } from './api';
import { ACCOUNT_RETRY_SECONDS, MAX_RETRY_SECONDS, requestAdvice } from './requestFeedback';
import { useCountdown } from './useCountdown';
import { Icon } from './components';

export function RequestDetails({ error, unknown = false, sensitive = false }: { error: ApiError | null; unknown?: boolean; sensitive?: boolean }) {
  const seconds = useCountdown(error?.retryAt || 0, sensitive ? ACCOUNT_RETRY_SECONDS : MAX_RETRY_SECONDS), [copied, setCopied] = useState('');
  useEffect(() => setCopied(''), [error?.requestId]);
  if (!error) return null;
  const advice = sensitive ? unknown ? '敏感操作不保留可重发的正文。请核对账户状态，必要时重新登录或申请新链接。' : error.status === 429 ? '请等待冷却结束后再手动操作；不会自动发送敏感请求。' : '' : requestAdvice(error, unknown);
  return <div className="request-details">{advice && <p>{advice}</p>}{error.retryAt > 0 && <span className="request-cooldown" role="status">{sensitive ? seconds ? `冷却剩余 ${seconds} 秒` : '冷却已结束，可核对账户状态' : seconds ? `${seconds} 秒后可手动重试` : '冷却已结束，可手动重试'}</span>}{error.requestId && <div className="request-trace"><span>请求编号 <code>{error.requestId}</code></span><button className="text-button" onClick={async () => { try { await navigator.clipboard.writeText(error.requestId!); setCopied('已复制'); } catch { setCopied('复制失败，请选择编号复制'); } }} aria-label="复制请求编号"><Icon name="copy" size={12}/>{copied || '复制'}</button></div>}</div>;
}
