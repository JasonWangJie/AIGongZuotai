import { describe, expect, it } from 'vitest';
import { capacityFraction, queueAge, validRuntime } from './runtime';
import { RequestScope } from './scope';

const runtime = { scope: 'workspace', ready: true, state: 'ready', runs: { active: 3, limit: 4, queued: 1, oldest_queued_seconds: 12.8, models: [{ model_id: 'studio-chat', kind: 'chat', active: 3, limit: 4, queued: 1, oldest_queued_seconds: 12.8 }] }, tasks: { active: 0, limit: 8, queued: 0, oldest_queued_seconds: 0, models: [] }, streams: { active: 2, limit: 20 } };
describe('workspace runtime snapshots', () => {
  it('accepts measured zero and nonzero capacity without substituting a local task count', () => { expect(validRuntime(runtime)).toBe(true); expect(runtime.tasks.active).toBe(0); expect(capacityFraction(runtime.runs.active, runtime.runs.limit)).toBe(.75); });
  it('rejects global, missing, negative and nonnumeric measurements instead of showing fabricated zero', () => { expect(validRuntime({ ...runtime, scope: 'global' })).toBe(false); expect(validRuntime({ ...runtime, streams: { active: -1, limit: 3 } })).toBe(false); expect(validRuntime({ ...runtime, runs: { ...runtime.runs, limit: '4' } })).toBe(false); expect(validRuntime({ ...runtime, tasks: undefined })).toBe(false); });
  it('shows actual occupancy even when a lowered limit is exceeded and bounds only the bar', () => { expect(capacityFraction(7, 4)).toBe(1); expect(capacityFraction(0, 0)).toBe(0); expect(queueAge(125.9)).toBe('2 分 5 秒'); expect(queueAge(3600)).toBe('1 小时 0 分'); });
  it('ignores late snapshots after closing/hidden pause and after A to B to A navigation', () => { const scope = new RequestScope(); const first = scope.transition('project-a'); scope.transition(null); expect(scope.current(first)).toBe(false); const reopened = scope.transition('project-a'); expect(scope.current(reopened)).toBe(true); scope.transition('project-b'); scope.transition('project-a'); expect(scope.current(reopened)).toBe(false); });
});
