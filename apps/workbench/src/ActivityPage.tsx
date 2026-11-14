import { useEffect, useRef, useState } from 'react';
import { api, ApiError } from './api';
import { activityDetails, activityTitle, mergeActivity } from './activity';
import { Icon } from './components';
import type { AuditEvent } from './types';
import { RequestDetails } from './RequestDetails';
import { useCountdown } from './useCountdown';
import { useInvalidation } from './useInvalidation';

export function ActivityPage({ projectId, userId, refreshVersion = 0, onAuthExpired }: { projectId: string; userId?: string; refreshVersion?: number; onAuthExpired: () => void }) {
  const [items, setItems] = useState<AuditEvent[]>([]), [cursor, setCursor] = useState<string | undefined>(), [storage, setStorage] = useState('');
  const [loading, setLoading] = useState(false), [error, setError] = useState('');
  const [issue, setIssue] = useState<ApiError | null>(null);
  const retrySeconds = useCountdown(issue?.retryAt || 0);
  const alive = useRef(true), generation = useRef(0), lock = useRef(false);
  const expire = useRef(onAuthExpired); expire.current = onAuthExpired;
  async function load(next?: string) {
    if (lock.current || Date.now() < (issue?.retryAt || 0)) return; lock.current = true; const ticket = ++generation.current; setLoading(true); setError(''); setIssue(null);
    try { const page = await api.audit(projectId, next); if (!alive.current || ticket !== generation.current) return; setItems(old => mergeActivity(next ? old : [], page.items, projectId)); setCursor(page.next_cursor || undefined); setStorage(page.storage); }
    catch (cause) { if (alive.current && ticket === generation.current) { if (cause instanceof ApiError && cause.status === 401) expire.current(); else { setError(cause instanceof Error ? cause.message : '活动记录暂不可用'); setIssue(cause instanceof ApiError ? cause : null); } } }
    finally { if (ticket === generation.current) lock.current = false; if (alive.current && ticket === generation.current) setLoading(false); }
  }
  useEffect(() => { alive.current = true; lock.current = false; setItems([]); setCursor(undefined); void load(); return () => { alive.current = false; generation.current++; }; }, [projectId]);
  useInvalidation(refreshVersion, true, loading || retrySeconds > 0, () => void load());
  return <section className="collection-page activity-page"><div className="section-title"><div><span className="eyebrow">A TRACE OF YOUR CREATIVE PROCESS</span><h1>每一步创作，都有迹可循。</h1><p>当前项目的操作记录；不展示提示词、输出正文或密钥，也不是消费账单。</p></div><button className="button secondary" disabled={loading || retrySeconds > 0} onClick={() => void load()}><Icon name="refresh" size={14}/>{loading ? '读取中…' : '刷新活动'}</button></div><div className="activity-meta"><span>已加载 {items.length} 条记录</span><span>{storage === 'postgres' ? 'PostgreSQL · 持久记录' : storage === 'memory' ? '内存模式 · 最多 512 条，重启清空' : '正在连接活动记录'}</span></div>{error && <div className="draft-error" role="alert"><strong>活动记录暂未加载</strong><p>{error}</p><RequestDetails error={issue}/><button className="text-button" disabled={retrySeconds > 0} onClick={() => void load()}>重新读取</button></div>}<div className="activity-list">{items.map(item => <article className="activity-row" key={item.id}><span className="activity-symbol"><Icon name={(item.resource_type === 'run' || item.resource_type === 'conversation') ? 'chat' : item.resource_type === 'draft' ? 'layers' : (item.resource_type === 'task' || item.resource_type === 'reference' || item.resource_type === 'asset') ? 'image' : 'folder'} size={17}/></span><div className="activity-copy"><div><strong>{activityTitle(item)}</strong><span>{item.actor_type === 'local' ? '本地模式' : item.actor_id === userId ? '我' : '工作区成员'}</span></div>{activityDetails(item) && <p>{activityDetails(item)}</p>}<small>{item.resource_type === 'draft' ? '草稿' : (item.resource_type === 'run' || item.resource_type === 'conversation') ? '对话' : item.resource_type === 'task' ? '任务' : item.resource_type === 'reference' ? '参考图' : item.resource_type === 'asset' ? '素材' : '项目'} · {item.resource_id.slice(0, 18)}</small></div><time dateTime={item.created_at}>{new Date(item.created_at).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' })}</time></article>)}</div>{!items.length && !loading && !error && <div className="empty-state"><Icon name="folder" size={30}/><h2>项目的故事，刚刚开始</h2><p>创建项目、提交创作、取消、核对、上传参考图及保存草稿后，可在这里查看操作记录。</p></div>}<div className="history-footer"><span>按时间倒序，只读查看</span>{cursor ? <button className="button secondary" disabled={loading || retrySeconds > 0} onClick={() => void load(cursor)}>加载更早活动 <Icon name="arrow" size={14}/></button> : items.length > 0 && <small>已读取到当前历史末尾</small>}</div></section>;
}
