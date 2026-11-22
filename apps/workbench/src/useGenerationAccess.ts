import { uiText } from './locale';
import { useEffect, useRef, useState } from 'react';
import { api, ApiError } from './api';
import { generationCanCreate, generationScope, mergeGenerationAccess, safeGenerationError, validGenerationAccess } from './generationControls';
import type { GenerationAccess } from './generationControls';

export const GENERATION_POLL_MS = 15000;
export function useGenerationAccess({ owner, workspace, required, visible, onExpired }: { owner: string; workspace: string; required: boolean; visible: boolean; onExpired: () => void }) {
  const scope = generationScope(owner, workspace), current = useRef(scope); current.current = scope;
  const [snapshot, setSnapshot] = useState<{ scope: string; value: GenerationAccess; at: number } | null>(null), [issue, setIssue] = useState<{ scope: string; value: ApiError } | null>(null), [loading, setLoading] = useState(false), [refreshVersion, refresh] = useState(0);
  const expire = useRef(onExpired), cooldown = useRef({ scope: '', until: 0 }), requestedAt = useRef({ scope: '', at: 0 }); expire.current = onExpired;
  useEffect(() => {
    if (!required || !owner || !workspace || !visible) { setLoading(false); return; }
    let alive = true, timer: number | undefined, abort: AbortController | undefined, busy = false;
    const active = () => alive && current.current === scope && document.visibilityState !== 'hidden';
    const schedule = (delay: number) => { window.clearTimeout(timer); if (active()) timer = window.setTimeout(() => void load(), delay); };
    async function load() {
      if (!active() || busy) return;
      const waitUntil = Math.max(cooldown.current.scope === scope ? cooldown.current.until : 0, requestedAt.current.scope === scope ? requestedAt.current.at + 3000 : 0);
      if (Date.now() < waitUntil) { schedule(waitUntil - Date.now()); return; }
      busy = true; requestedAt.current = { scope, at: Date.now() }; abort = new AbortController(); setLoading(true);
      try { const value = await api.generationAccess(abort.signal); if (!active()) return; if (!validGenerationAccess(value, workspace)) throw new ApiError(uiText('生成权限响应范围或内容无效。')); setSnapshot(old => ({ scope, value: mergeGenerationAccess(old?.scope === scope ? old.value : null, value), at: Date.now() })); setIssue(null); }
      catch (cause) { if (!active() || abort.signal.aborted) return; const error = safeGenerationError(cause); cooldown.current = { scope, until: error.retryAt }; setIssue({ scope, value: error }); if (error.status === 401) expire.current(); }
      finally { busy = false; if (active()) { setLoading(false); schedule(GENERATION_POLL_MS); } }
    }
    const wake = () => { if (active()) schedule(0); else { window.clearTimeout(timer); abort?.abort(); } };
    void load(); window.addEventListener('focus', wake); document.addEventListener('visibilitychange', wake);
    return () => { alive = false; window.clearTimeout(timer); abort?.abort(); window.removeEventListener('focus', wake); document.removeEventListener('visibilitychange', wake); };
  }, [scope, required, visible, refreshVersion, owner, workspace]);
  const data = snapshot?.scope === scope ? snapshot.value : null, error = issue?.scope === scope ? issue.value : null;
  return { data, error, loading, updatedAt: snapshot?.scope === scope ? snapshot.at : 0, allowed: generationCanCreate(required, data, error), refresh: () => refresh(value => value + 1), observe: (value: GenerationAccess) => { if (validGenerationAccess(value, workspace) && current.current === scope) { setSnapshot(old => ({ scope, value: mergeGenerationAccess(old?.scope === scope ? old.value : null, value), at: Date.now() })); setIssue(null); } } };
}
export type GenerationAccessController = ReturnType<typeof useGenerationAccess>;
