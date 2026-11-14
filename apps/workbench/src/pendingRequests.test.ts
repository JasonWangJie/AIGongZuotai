import { describe, expect, it } from 'vitest';
import { PendingRequests, SubmissionFlights } from './pendingRequests';
import type { PendingOperation } from './pendingRequests';
const operation: PendingOperation = { kind: 'run', ownerId: 'owner', key: 'original-key', body: { project_id: 'p1', model_id: 'm1', input: '原始输入' } };
describe('generation attempts across authentication invalidation', () => {
  it('does not let a late 401 or unknown result erase or overwrite a reauthenticated pending retry', () => { const pending = new PendingRequests(); pending.remember(operation); pending.failed(operation, false, false); expect(pending.get('owner', 'p1')).toEqual(operation); pending.failed(operation, true, true); const newer = { ...operation, key: 'later-key' }; pending.remember(newer); pending.failed(operation, true, false); expect(pending.get('owner', 'p1')?.key).toBe('later-key'); pending.failed(newer, false, true); expect(pending.get('owner', 'p1')).toBeUndefined(); });
  it('does not release a new in-flight retry lock when the old same-key attempt finishes', () => { const flights = new SubmissionFlights(), old = flights.claim('p1', 'same-key', 1); flights.clear(); const retry = flights.claim('p1', 'same-key', 3); flights.release('p1', old); expect(flights.has('p1')).toBe(true); flights.release('p1', retry); expect(flights.has('p1')).toBe(false); });
});
