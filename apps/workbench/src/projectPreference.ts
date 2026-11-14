import type { AuthSession } from './types';

type PreferenceStorage = Pick<Storage, 'getItem' | 'setItem'>;
export function browserProjectStorage(): PreferenceStorage | null { try { return window.localStorage; } catch { return null; } }
function key(session: AuthSession): string | null {
  if (session.auth_enabled && !session.user) return null;
  return 'xingtu-last-project-v1:' + (session.user?.id || 'demo');
}

// A UI preference is accepted only after the authenticated server lists ownership.
// It never authenticates a user, grants access, or stores a session token.
export function readProjectPreference(session: AuthSession, storage: PreferenceStorage | null): string | undefined {
  const storageKey = key(session); if (!storageKey || !storage) return undefined;
  try { const stored = storage.getItem(storageKey); return stored && session.projects?.some(project => project.id === stored) ? stored : undefined; }
  catch { return undefined; }
}

export function rememberProject(session: AuthSession, projectId: string, storage: PreferenceStorage | null): void {
  const storageKey = key(session);
  if (!storageKey || !storage || !session.projects?.some(project => project.id === projectId)) return;
  try { storage.setItem(storageKey, projectId); } catch { /* Storage is optional; server authorization remains authoritative. */ }
}
