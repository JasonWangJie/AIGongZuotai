import { describe, expect, it } from 'vitest';
import { activityDetails, activityTitle, mergeActivity } from './activity';
import type { AuditEvent } from './types';

const event: AuditEvent = { id: 'e2', project_id: 'p1', workspace_id: 'w1', actor_id: 'u1', actor_type: 'user', action: 'draft.saved', resource_type: 'draft', resource_id: 'p1', created_at: '2026-11-13T00:00:02Z', details: { revision: 3, node_count: 2 } };
describe('read-only project activity presentation', () => {
  it('renders the allowlisted draft action and bounded summary', () => { expect(activityTitle(event)).toBe('保存项目草稿'); expect(activityDetails(event)).toBe('版本 v3 · 2 个节点'); });
  it('never renders unrecognized detail keys or arbitrary action strings', () => { const injected = { ...event, action: 'secret prompt body', details: { prompt: 'private prompt', key: 'private key', from_status: 'private output', node_count: 999 } } as unknown as AuditEvent; expect(activityTitle(injected)).toBe('其他项目活动'); expect(activityDetails(injected)).toBe(''); });
  it('deduplicates overlapping pages and rejects another project', () => { const merged = mergeActivity([event], [{ ...event }, { ...event, id: 'e1', created_at: '2026-11-13T00:00:01Z' }, { ...event, id: 'foreign', project_id: 'p2' }], 'p1'); expect(merged.map(item => item.id)).toEqual(['e2', 'e1']); });
});
