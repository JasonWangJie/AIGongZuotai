import type { RuntimeUsage, WorkspaceRuntime } from './types';

export const RUNTIME_POLL_MS = 15_000;
export function validRuntime(value: unknown): value is WorkspaceRuntime {
  if (!value || typeof value !== 'object') return false;
  const item = value as WorkspaceRuntime;
  const count = (n: unknown) => typeof n === 'number' && Number.isSafeInteger(n) && n >= 0;
  const usage = (data: RuntimeUsage) => !!data && count(data.active) && count(data.limit) && count(data.queued) && typeof data.oldest_queued_seconds === 'number' && Number.isFinite(data.oldest_queued_seconds) && data.oldest_queued_seconds >= 0;
  const group = (data: WorkspaceRuntime['runs']) => usage(data) && Array.isArray(data.models) && data.models.every(model => usage(model) && typeof model.model_id === 'string' && ['chat', 'image', 'video'].includes(model.kind));
  return item.scope === 'workspace' && typeof item.ready === 'boolean' && typeof item.state === 'string' && group(item.runs) && group(item.tasks) && !!item.streams && count(item.streams.active) && count(item.streams.limit);
}
export function capacityFraction(active: number, limit: number): number { return limit > 0 ? Math.max(0, Math.min(1, active / limit)) : 0; }
export function queueAge(seconds: number): string { const value = Math.max(0, Math.floor(seconds)); return value < 60 ? `${value} 秒` : value < 3600 ? `${Math.floor(value / 60)} 分 ${value % 60} 秒` : `${Math.floor(value / 3600)} 小时 ${Math.floor(value % 3600 / 60)} 分`; }
