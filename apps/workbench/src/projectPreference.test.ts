import { describe, expect, it } from 'vitest';
import { readProjectPreference, rememberProject } from './projectPreference';
import type { AuthSession } from './types';

function storage() { const values = new Map<string, string>(); return { getItem: (key: string) => values.get(key) || null, setItem: (key: string, value: string) => { values.set(key, value); } }; }
const session: AuthSession = { auth_enabled: true, user: { id: 'user-a', email: 'a@example.com', name: 'A' }, projects: [{ id: 'p1', name: 'One' }, { id: 'p2', name: 'Two' }] };
describe('last selected project preference', () => {
  it('restores a selected project only when it is in the server-owned project list', () => { const store = storage(); rememberProject(session, 'p2', store); expect(readProjectPreference(session, store)).toBe('p2'); expect(readProjectPreference({ ...session, projects: [{ id: 'p1', name: 'One' }] }, store)).toBeUndefined(); });
  it('isolates users and demo without storing authentication data', () => { const store = storage(); rememberProject(session, 'p2', store); expect(readProjectPreference({ ...session, user: { ...session.user!, id: 'user-b' } }, store)).toBeUndefined(); expect(readProjectPreference({ ...session, auth_enabled: false, user: null }, store)).toBeUndefined(); const demo = { ...session, auth_enabled: false, user: null }; rememberProject(demo, 'p1', store); expect(readProjectPreference(demo, store)).toBe('p1'); expect(readProjectPreference(session, store)).toBe('p2'); });
  it('ignores tampering and anonymous auth sessions', () => { const store = storage(); store.setItem('xingtu-last-project-v1:user-a', 'foreign-project'); expect(readProjectPreference(session, store)).toBeUndefined(); rememberProject(session, 'foreign-project', store); expect(readProjectPreference(session, store)).toBeUndefined(); expect(readProjectPreference({ ...session, user: null }, store)).toBeUndefined(); });
  it('falls back to the server default if browser storage is unavailable', () => { const store = { getItem: () => { throw new Error('blocked'); }, setItem: () => { throw new Error('blocked'); } }; expect(readProjectPreference(session, store)).toBeUndefined(); expect(() => rememberProject(session, 'p1', store)).not.toThrow(); });
});
