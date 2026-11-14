import { describe, expect, it } from 'vitest';
import { RequestScope } from './scope';
import { mergeTask } from './state';
import type { Task } from './types';

describe('project request isolation', () => {
  it('rejects an old bootstrap after a newer project is selected', () => { const scope = new RequestScope(); const old = scope.transition('a'); const next = scope.transition('b'); expect(scope.bind(old, 'a')).toBeNull(); expect(scope.bind(next, 'b')?.projectId).toBe('b'); });
  it('rejects old responses even after switching A → B → A', () => { const scope = new RequestScope(); const old = scope.transition('a'); scope.transition('b'); const next = scope.transition('a'); expect(scope.current(old)).toBe(false); expect(scope.current(next)).toBe(true); });
  it('rejects resources from the wrong project even on the current connection', () => { const scope = new RequestScope(); const token = scope.transition('a'); expect(scope.accepts(token, { project_id: 'b' })).toBe(false); expect(scope.accepts(token, { project_id: 'a' })).toBe(true); });
  it('invalidates every outstanding token on logout', () => { const scope = new RequestScope(); const token = scope.transition('a'); scope.transition(null); expect(scope.current(token)).toBe(false); expect(scope.accepts(scope.capture(), { project_id: 'a' })).toBe(false); });
  it('ignores a delayed previous-project list after a new-project SSE event', async () => {
    const scope = new RequestScope();
    const a = scope.transition('a');
    let resolve!: (task: Task) => void;
    const delayed = new Promise<Task>(done => { resolve = done; });
    let state: Task[] = [];
    const pending = delayed.then(task => { if (scope.accepts(a, task)) state = mergeTask(state, task); });
    const b = scope.transition('b');
    const task: Task = { id: 'new-b', project_id: 'b', model_id: 'm', prompt: 'B', kind: 'image', status: 'RUNNING', progress: 25, version: 2, created_at: '2026-11-13', updated_at: '2026-11-13', artifacts: [] };
    if (scope.accepts(b, task)) state = mergeTask(state, task);
    resolve({ ...task, id: 'old-a', project_id: 'a' }); await pending;
    expect(state.map(item => item.id)).toEqual(['new-b']);
  });
  it('deduplicates overlapping offset pages without replacing newer SSE state', () => {
    const task: Task = { id: 'overlap', project_id: 'a', model_id: 'm', prompt: 'A', kind: 'image', status: 'RUNNING', progress: 75, version: 5, created_at: '2026-11-13', updated_at: '2026-11-13', artifacts: [] };
    const older = { ...task, id: 'older', created_at: '2026-11-12' };
    const state = [{ ...task, version: 3, progress: 10 }, older].reduce(mergeTask, [task]);
    expect(state.map(item => item.id)).toEqual(['overlap', 'older']); expect(state[0].version).toBe(5);
  });
});
