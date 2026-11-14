import { describe, expect, it } from 'vitest';
import { applyRunEvent, mergeRun, mergeTask } from './state';
import type { Run, Task } from './types';

const run: Run = { id: 'r1', project_id: 'p1', model_id: 'm1', prompt: '你好', output: '你', status: 'RUNNING', seq: 1, created_at: '2026-11-13', updated_at: '2026-11-13' };
const task: Task = { id: 't1', project_id: 'p1', model_id: 'm2', kind: 'image', prompt: 'image', status: 'RUNNING', progress: 20, version: 2, artifacts: [], created_at: '2026-11-13', updated_at: '2026-11-13' };

describe('resource event reconciliation', () => {
  it('deduplicates/rejects stale task versions', () => { const current = [task]; expect(mergeTask(current, { ...task, version: 1, progress: 0 })).toBe(current); expect(mergeTask(current, { ...task })).toBe(current); });
  it('applies a newer task snapshot without regressing terminal state', () => { expect(mergeTask([task], { ...task, version: 3, progress: 100, status: 'COMPLETED' })[0].progress).toBe(100); const done = { ...task, status: 'COMPLETED', version: 3 }; expect(mergeTask([done], { ...task, version: 4 })[0].status).toBe('COMPLETED'); });
  it('applies unicode delta exactly once', () => { const event = { event: 'message.delta', data: { run_id: 'r1', seq: 2, delta: '好🌿' } }; const next = applyRunEvent(run, event).run; expect(next.output).toBe('你好🌿'); expect(applyRunEvent(next, event).run).toBe(next); });
  it('detects event gaps without appending potentially incomplete output', () => { const result = applyRunEvent(run, { event: 'message.delta', data: { run_id: 'r1', seq: 4, delta: 'missing' } }); expect(result.gap).toBe(true); expect(result.run).toBe(run); });
  it('repairs a gap with an authoritative snapshot', () => { const snapshot = { ...run, seq: 8, output: '完整文本' }; expect(applyRunEvent(run, { event: 'run.snapshot', data: snapshot }).run).toEqual(snapshot); });
  it('ignores another run and stale snapshots', () => { expect(applyRunEvent(run, { event: 'message.delta', data: { run_id: 'other', seq: 2, delta: 'bad' } }).run).toBe(run); expect(applyRunEvent(run, { event: 'run.snapshot', data: { ...run, seq: 0 } }).run).toBe(run); });
  it('retains final content after terminal events and stale refreshes', () => { const done = applyRunEvent(run, { event: 'run.completed', data: { run_id: 'r1', seq: 2, status: 'COMPLETED' } }).run; expect(done.output).toBe('你'); expect(applyRunEvent(done, { event: 'message.delta', data: { run_id: 'r1', seq: 3, delta: 'late' } }).run).toBe(done); expect(mergeRun([done], run)[0]).toBe(done); });
});
