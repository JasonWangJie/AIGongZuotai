import { useEffect, useRef, useState } from 'react';
import { api, ApiError } from './api';
import { Icon } from './components';
import { RequestDetails } from './RequestDetails';
import { capacityFraction, queueAge, RUNTIME_POLL_MS, validRuntime } from './runtime';
import { RequestScope } from './scope';
import { useCountdown } from './useCountdown';
import type { Model, WorkspaceRuntime } from './types';

type Snapshot = { projectId: string; workspaceId: string; data: WorkspaceRuntime; receivedAt: number };
export function RuntimePanel({ projectId, workspaceId, models, enabled, onAuthExpired }: { projectId: string; workspaceId: string; models: Model[]; enabled: boolean; onAuthExpired: () => void }) {
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null), [error, setError] = useState<ApiError | null>(null);
  const [loading, setLoading] = useState(false), [refresh, setRefresh] = useState(0), [visible, setVisible] = useState(document.visibilityState !== 'hidden');
  const scope = useRef(new RequestScope()), expire = useRef(onAuthExpired), retryAt = useRef(0); expire.current = onAuthExpired;
  const seconds = useCountdown(error?.retryAt || 0);
  const currentSnapshot = snapshot?.projectId === projectId && snapshot.workspaceId === workspaceId ? snapshot : null;
  useEffect(() => { const visibility = () => setVisible(document.visibilityState !== 'hidden'); document.addEventListener('visibilitychange', visibility); return () => document.removeEventListener('visibilitychange', visibility); }, []);
  useEffect(() => {
    const token = scope.current.transition(enabled && visible ? projectId : null);
    if (!enabled || !visible) { setLoading(false); return; }
    let timer: number | undefined, controller: AbortController | undefined;
    const current = () => scope.current.current(token) && document.visibilityState !== 'hidden';
    const load = async () => {
      if (!current()) return;
      if (Date.now() < retryAt.current) { timer = window.setTimeout(() => void load(), retryAt.current - Date.now()); return; }
      controller = new AbortController(); setLoading(true); let delay = RUNTIME_POLL_MS;
      try {
        const data = await api.runtime(projectId, controller.signal);
        if (!current()) return;
        if (!validRuntime(data)) throw new ApiError('容量响应格式不完整，未显示推算数据。');
        setSnapshot({ projectId, workspaceId, data, receivedAt: Date.now() }); setError(null); retryAt.current = 0;
      } catch (cause) {
        if (!current()) return;
        const issue = cause instanceof ApiError ? cause : new ApiError('无法读取当前工作区容量。');
        if (issue.status === 401) { expire.current(); return; }
        setError(issue); retryAt.current = issue.retryAt; delay = Math.max(delay, issue.retryAt - Date.now());
      } finally { if (current()) { setLoading(false); timer = window.setTimeout(() => void load(), delay); } }
    };
    void load();
    return () => { scope.current.transition(null); controller?.abort(); window.clearTimeout(timer); };
  }, [projectId, workspaceId, enabled, visible, refresh]);
  const data = currentSnapshot?.data;
  const rows = data ? [{ name: '对话占用', ...data.runs }, { name: '媒体占用', ...data.tasks }, { name: '事件连接', ...data.streams }] : [];
  return <section className="inspector-section workspace-runtime" aria-label="当前工作区容量"><div className="queue-heading"><span className="section-number">WORKSPACE / CAPACITY</span><button className="icon-button" disabled={!enabled || !visible || loading || seconds > 0} aria-label="刷新工作区容量" onClick={() => setRefresh(value => value + 1)}><Icon name="refresh" size={13}/></button></div><h3>当前工作区容量 <i className={data?.ready && !error ? 'connected' : ''}/></h3><p className="runtime-scope">工作区全部项目 · 占用 / 上限</p>{data ? <><div className="capacity-rows">{rows.map(row => <div className="capacity-row" key={row.name}><div><span>{row.name}</span><strong>{row.active}<small> / {row.limit}</small></strong></div><div className="capacity-track" role="meter" aria-label={row.name} aria-valuemin={0} aria-valuemax={Math.max(row.limit, row.active, 1)} aria-valuenow={row.active} aria-valuetext={`${row.active} 占用，上限 ${row.limit}`}><i style={{ width: `${capacityFraction(row.active, row.limit) * 100}%` }}/></div></div>)}</div><div className="runtime-queues"><span>对话排队 <b>{data.runs.queued}</b></span><span>媒体排队 <b>{data.tasks.queued}</b></span></div><p className="runtime-oldest">最久排队：对话 {queueAge(data.runs.oldest_queued_seconds)} · 媒体 {queueAge(data.tasks.oldest_queued_seconds)}</p><details className="runtime-models"><summary>查看模型占用</summary>{[...data.runs.models, ...data.tasks.models].map(model => <div key={model.kind + model.model_id}><span title={model.model_id}>{models.find(item => item.id === model.model_id)?.name || model.model_id}</span><b>{model.active} / {model.limit}</b><small>排队 {model.queued}</small></div>)}</details><p className="runtime-caveat">占用包含排队、执行及待核实任务；不是正在生成数量。容量快照不影响原请求核对。</p></> : <p className="runtime-empty">{loading ? '正在读取实际容量…' : error ? '容量暂不可用；未使用估算值。' : '展开设置后读取实际容量。'}</p>}{error && <div className="runtime-error" role="status"><p>{error.message}</p>{data && <small>当前计数来自上次成功读取，可能已经变化。</small>}<RequestDetails error={error}/></div>}<div className="runtime-updated"><span>{loading ? '正在刷新' : error ? '读取失败' : currentSnapshot ? '数据已更新' : '等待连接'}</span><time>{currentSnapshot ? new Date(currentSnapshot.receivedAt).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit', second: '2-digit' }) : '—'}</time></div><p className="runtime-poll-note">设置展开且页面可见时，每 15 秒更新。</p></section>;
}
