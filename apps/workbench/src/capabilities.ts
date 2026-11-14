import type { Bootstrap, Kind, KindCapability, Task } from './types';

export const reconciliationStates = ['RECONCILE_REQUIRED', 'PROVIDER_STATE_UNKNOWN', 'SUBMIT_UNKNOWN'];
export function canReconcile(task: Pick<Task, 'status' | 'can_reconcile'>): boolean { return task.can_reconcile === true && reconciliationStates.includes(task.status); }
export function isDemoResource(resource: { provider_mode?: 'demo' | 'live' }, fallbackMode: 'demo' | 'live'): boolean { return (resource.provider_mode || fallbackMode) === 'demo'; }

export function capability(bootstrap: Bootstrap | null, kind: Kind): KindCapability {
  if (!bootstrap) return { enabled: false, cancel_supported: false, max_count: 1 };
  const declared = bootstrap.capabilities?.[kind];
  if (declared) return { ...declared, cancel_supported: declared.cancel_supported === true, max_count: kind === 'video' ? 1 : Math.min(4, Math.max(1, declared.max_count || (kind === 'image' ? 4 : 1))) };
  // Compatibility with the first local-demo bootstrap contract only.
  const enabled = bootstrap.mode === 'demo' || kind === 'chat';
  return { enabled, cancel_supported: enabled, max_count: kind === 'image' ? 4 : 1, aspect_ratios: ['1:1', '16:9', '9:16'] };
}
