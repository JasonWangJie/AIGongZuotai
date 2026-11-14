import { useEffect, useRef } from 'react';
/** Merge an invalidation received during a read; do not remount or reset editor state. */
export function useInvalidation(version: number, enabled: boolean, blocked: boolean, read: () => void): void {
  const seen = useRef(version), pending = useRef(false), callback = useRef(read); callback.current = read;
  useEffect(() => { if (seen.current !== version) { seen.current = version; pending.current = true; } }, [version]);
  useEffect(() => { if (!enabled || blocked || !pending.current) return; let started = false; const timer = window.setTimeout(() => { started = true; pending.current = false; callback.current(); }, 250); return () => { window.clearTimeout(timer); if (!started) pending.current = true; }; }, [version, enabled, blocked]);
}
