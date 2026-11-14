import type { Run, RunEvent, Task } from './types';
import { terminal } from './types';
import { mergeModeratedText, moderationAllowsText } from './moderation';

export function mergeTask(current: Task[], incoming: Task): Task[] {
  const existing = current.find(t => t.id === incoming.id);
  if (existing && incoming.version <= existing.version) return current;
  // Defensive guard: terminal task state cannot regress even if an upstream bug increments version.
  if (existing && terminal(existing.status) && !terminal(incoming.status)) return current;
  return [incoming, ...current.filter(t => t.id !== incoming.id)].sort((a, b) => b.created_at.localeCompare(a.created_at));
}

export function mergeRun(current: Run[], incoming: Run): Run[] {
  const existing = current.find(r => r.id === incoming.id);
  if (existing && existing.project_id !== incoming.project_id) return current;
  const next = mergeRunSnapshot(existing, incoming);
  if (next === existing) return current;
  return [next, ...current.filter(r => r.id !== incoming.id)].sort((a, b) => b.created_at.localeCompare(a.created_at));
}

export function mergeRunSnapshot(existing: Run | undefined, incoming: Run): Run {
  if (existing && (existing.id !== incoming.id || existing.project_id !== incoming.project_id)) return existing;
  const accepted = !existing || incoming.seq >= existing.seq && !(terminal(existing.status) && !terminal(incoming.status));
  const base = accepted ? incoming : existing!;
  const content = mergeModeratedText(existing && { ...existing, text: existing.output }, { ...incoming, text: incoming.output }, accepted);
  const output = moderationAllowsText(content.moderation, base.status) ? content.text : '';
  const billing = existing?.billing && (!base.billing || existing.billing.version > base.billing.version) ? existing.billing : base.billing;
  if (base.output === output && base.moderation === content.moderation && base.billing === billing) return base;
  return { ...base, output, ...(content.moderation ? { moderation: content.moderation } : {}), ...(billing ? { billing } : {}) };
}

export type RunReduction = { run: Run; gap: boolean };
export function applyRunEvent(run: Run, event: RunEvent): RunReduction {
  if (event.event === 'run.snapshot') {
    const snapshot = event.data as Run;
    if (snapshot.id !== run.id || snapshot.project_id !== run.project_id) return { run, gap: false };
    return { run: mergeRunSnapshot(run, snapshot), gap: false };
  }
  const data = event.data as { run_id?: string; seq?: number; delta?: string; status?: string };
  if (data.run_id !== run.id || typeof data.seq !== 'number' || data.seq <= run.seq || terminal(run.status)) return { run, gap: false };
  if (data.seq !== run.seq + 1) return { run, gap: true };
  // Reviewed runs publish one final snapshot. Never append an unreviewed delta, even if a faulty peer sends one.
  if (event.event === 'message.delta' && run.moderation != null) return { run: mergeRunSnapshot(undefined, run), gap: true };
  if (event.event === 'message.delta' && typeof data.delta === 'string') return { run: { ...run, output: run.output + data.delta, seq: data.seq, status: 'RUNNING' }, gap: false };
  const finalStatus = { 'run.completed': 'COMPLETED', 'run.cancelled': 'CANCELLED', 'run.failed': 'FAILED', 'run.interrupted': 'INTERRUPTED' }[event.event];
  if (finalStatus) return { run: mergeRunSnapshot(undefined, { ...run, status: data.status || finalStatus, seq: data.seq }), gap: false };
  return { run, gap: false };
}
