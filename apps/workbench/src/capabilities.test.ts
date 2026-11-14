import { describe, expect, it } from 'vitest';
import { capability, canReconcile, isDemoResource } from './capabilities';
import { terminal } from './types';
import type { Bootstrap } from './types';

const bootstrap: Bootstrap = { workspace: { id: 'w', name: 'W' }, project: { id: 'p', name: 'P' }, models: [], mode: 'live', storage: 'postgres' };
describe('server capability and recovery states', () => {
  it('opens real media explicitly declared by the server without enabling cancellation', () => { const data = { ...bootstrap, capabilities: { image: { enabled: true, cancel_supported: false, max_count: 4 }, video: { enabled: true, cancel_supported: false } } }; expect(capability(data, 'image').enabled).toBe(true); expect(capability(data, 'video').enabled).toBe(true); expect(capability(data, 'image').cancel_supported).toBe(false); expect(capability(data, 'video').max_count).toBe(1); });
  it('honors disabled capabilities even in demo and defaults undeclared cancellation to off', () => { expect(capability({ ...bootstrap, mode: 'demo', capabilities: { image: { enabled: false } } }, 'image').enabled).toBe(false); expect(capability({ ...bootstrap, capabilities: { image: { enabled: true } } }, 'image').cancel_supported).toBe(false); });
  it('retains conservative compatibility for old bootstrap payloads', () => { expect(capability(bootstrap, 'image').enabled).toBe(false); expect(capability(bootstrap, 'chat').enabled).toBe(true); expect(capability({ ...bootstrap, mode: 'demo' }, 'video').enabled).toBe(true); });
  it('treats interrupted/partial results as terminal but keeps unknown/reconciliation states unresolved', () => { expect(terminal('INTERRUPTED')).toBe(true); expect(terminal('PARTIAL')).toBe(true); expect(terminal('SUBMIT_UNKNOWN')).toBe(false); expect(terminal('RECONCILE_REQUIRED')).toBe(false); });
  it('offers rechecking only with explicit server permission and a recoverable status', () => { expect(canReconcile({ status: 'SUBMIT_UNKNOWN' })).toBe(false); expect(canReconcile({ status: 'RECONCILE_REQUIRED', can_reconcile: false })).toBe(false); expect(canReconcile({ status: 'RECONCILE_REQUIRED', can_reconcile: true })).toBe(true); expect(canReconcile({ status: 'PROVIDER_STATE_UNKNOWN', can_reconcile: true })).toBe(true); expect(canReconcile({ status: 'COMPLETED', can_reconcile: true })).toBe(false); });
  it('never relabels a persisted demo task/run as live after changing the current provider', () => { expect(isDemoResource({ provider_mode: 'demo' }, 'live')).toBe(true); expect(isDemoResource({ provider_mode: 'live' }, 'demo')).toBe(false); });
  it('uses the current provider only for legacy records without provider_mode', () => { expect(isDemoResource({}, 'demo')).toBe(true); expect(isDemoResource({}, 'live')).toBe(false); });
});
