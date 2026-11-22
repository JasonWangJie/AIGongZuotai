export type RefreshArea = 'history' | 'activity' | 'library' | 'references' | 'draft' | 'conversations' | 'workflow' | 'canvas';
export type ResourceHint = { type: 'task' | 'run'; id: string; version: number; status: string; force?: boolean };
export type RefreshBatch = { areas: RefreshArea[]; resources: ResourceHint[]; resetId?: number };
export type ResourceChanged = { event_id: string; seq: number; kind: 'task.updated' | 'run.updated' | 'audit.created' | 'workflow.updated'; resource_type: string; resource_id: string; resource_version: number; status?: string; action?: string };
const MAX_PENDING = 256, MAX_SEEN = 512, MAX_BATCH = 8;
const allAreas: RefreshArea[] = ['history', 'activity', 'library', 'references', 'draft', 'conversations', 'workflow','canvas'];
const terminalStates = new Set(['SUCCEEDED', 'COMPLETED', 'FAILED', 'PARTIAL', 'CANCELLED', 'INTERRUPTED']);
function boundedText(value: unknown, max = 128): value is string { return typeof value === 'string' && value.length > 0 && value.length <= max && !/[\u0000-\u001f\u007f]/u.test(value); }
export function parseResourceChanged(raw: string): ResourceChanged | null {
  if (raw.length > 8192) return null;
  try { const value = JSON.parse(raw) as ResourceChanged; if (!value || !boundedText(value.event_id) || !Number.isSafeInteger(value.seq) || value.seq < 1 || !['task.updated', 'run.updated', 'audit.created', 'workflow.updated'].includes(value.kind) || !['task', 'run', 'project', 'draft', 'reference', 'asset', 'conversation', 'workflow','canvas'].includes(value.resource_type) || !boundedText(value.resource_id) || !Number.isSafeInteger(value.resource_version) || value.resource_version < 0 || value.status !== undefined && value.status !== '' && !boundedText(value.status, 64) || value.action !== undefined && value.action !== '' && !boundedText(value.action, 64)) return null; if (value.kind === 'task.updated' && value.resource_type !== 'task' || value.kind === 'run.updated' && value.resource_type !== 'run' || (value.kind === 'workflow.updated') !== (value.resource_type === 'workflow')) return null; if ((value.resource_type === 'canvas') !== (value.kind === 'audit.created' && value.action === 'canvas.saved')) return null; return value; } catch { return null; }
}
export function validStreamReset(raw: string): boolean { if (raw.length > 4096) return false; try { const value = JSON.parse(raw); return !!value && ['initial', 'cursor_expired'].includes(value.reason) && boundedText(value.cursor, 1024); } catch { return false; } }
export function resourceHintCovered(known: ResourceHint, needed: ResourceHint): boolean { return known.type === needed.type && known.id === needed.id && (known.version > needed.version || known.version === needed.version && (known.status === needed.status || terminalStates.has(known.status) && !terminalStates.has(needed.status))); }
/** Read invalidations only. It never stores an SSE cursor, acknowledges delivery, or submits mutations. */
export class DurableFeedSync {
  private areas = new Set<RefreshArea>(); private resources = new Map<string, ResourceHint>();
  private seen = new Set<string>(); private known = new Map<string, ResourceHint>();
  private scan: Iterator<ResourceHint> | null = null; private nextScan: Iterator<ResourceHint> | null = null; private retryResources: ResourceHint[] = [];
  private resetCounter = 0; private pendingResetId = 0; private lastReconciliationId = 0;
  private timer: ReturnType<typeof setTimeout> | undefined; private closed = false; private busy = false; private failures = 0; private paused = false; private hidden = false; private lastStarted = -Infinity; private retryUntil = 0;
  constructor(private read: (batch: RefreshBatch) => Promise<void>, private failed: (error: unknown, paused: boolean) => void, private status: (busy: boolean) => void = () => {}) {}
  reset(): void { if (this.closed) return; if (!this.areas.has('history')) this.pendingResetId = ++this.resetCounter; allAreas.forEach(area => this.areas.add(area)); this.arm(); }
  // The scan walks existing loaded data lazily; it never fills or overflows the event queue.
  // A retry of the same local reset retains its position. A later reset queues at most one follow-up scan.
  beginReconciliation(resetId: number, resources: Iterable<ResourceHint>): void { if (this.closed || resetId <= this.lastReconciliationId) return; this.lastReconciliationId = resetId; const iterator = resources[Symbol.iterator](); if (this.scan) this.nextScan = iterator; else this.scan = iterator; this.arm(); }
  addResource(hint: ResourceHint): void { if (this.closed) return; const key = hint.type + ':' + hint.id, known = this.known.get(key); if (!hint.force && known && resourceHintCovered(known, hint)) return; const old = this.resources.get(key); if (old && old.version > hint.version && !hint.force) return; if (!this.resources.has(key) && this.resources.size >= MAX_PENDING) { this.reset(); return; } this.resources.set(key, { ...hint, force: old?.force || hint.force }); this.arm(); }
  acknowledge(hint: ResourceHint): void { const key = hint.type + ':' + hint.id, old = this.known.get(key); if (!old || !resourceHintCovered(old, hint)) { this.known.delete(key); this.known.set(key, hint); if (this.known.size > MAX_PENDING) this.known.delete(this.known.keys().next().value!); } const pending = this.resources.get(key); if (pending && !pending.force && resourceHintCovered(hint, pending)) this.resources.delete(key); }
  accept(event: ResourceChanged): void {
    if (this.closed || this.seen.has(event.event_id)) return; this.seen.add(event.event_id); if (this.seen.size > MAX_SEEN) this.seen.delete(this.seen.values().next().value!);
    if (event.kind === 'task.updated' || event.kind === 'run.updated') { this.addResource({ type: event.kind === 'task.updated' ? 'task' : 'run', id: event.resource_id, version: event.resource_version, status: event.status || '' }); if (event.kind === 'run.updated' && terminalStates.has(event.status || '')) this.areas.add('conversations'); if (event.kind === 'task.updated' && terminalStates.has(event.status || '')) this.areas.add('library'); }
    else if (event.kind === 'workflow.updated') { this.areas.add('workflow'); }
    else { this.areas.add('activity'); if (event.resource_type === 'conversation') this.areas.add('conversations'); if (['asset.updated', 'asset.trashed', 'asset.restored'].includes(event.action || '')) this.areas.add('workflow'); if (event.resource_type === 'canvas' && event.action === 'canvas.saved') this.areas.add('canvas'); if (event.action === 'draft.saved') this.areas.add('draft'); if (['reference.uploaded', 'asset.updated', 'asset.trashed', 'asset.restored'].includes(event.action || '')) { this.areas.add('library'); this.areas.add('references'); } }
    this.arm();
  }
  resume(): void { if (this.closed) return; this.paused = false; this.failures = 0; this.reset(); }
  setVisible(visible: boolean): void { this.hidden = !visible; if (this.hidden) { clearTimeout(this.timer); this.timer = undefined; } else this.arm(); }
  stop(): void { this.closed = true; clearTimeout(this.timer); this.timer = undefined; this.areas.clear(); this.resources.clear(); this.seen.clear(); this.known.clear(); this.scan = this.nextScan = null; this.retryResources = []; }
  private arm(delay = 250): void { if (this.closed || this.paused || this.hidden || this.busy || this.timer !== undefined || !this.areas.size && !this.resources.size && !this.scan && !this.nextScan && !this.retryResources.length) return; const wait = Math.max(delay, 1500 - (Date.now() - this.lastStarted), this.retryUntil - Date.now()); this.timer = setTimeout(() => { this.timer = undefined; void this.flush(); }, wait); }
  private nextScannedResource(): ResourceHint | undefined { while (this.scan || this.nextScan) { if (!this.scan) { this.scan = this.nextScan; this.nextScan = null; } const next = this.scan!.next(); if (next.done) { this.scan = null; continue; } return { ...next.value, force: true }; } return undefined; }
  private async flush(): Promise<void> {
    if (this.closed || this.busy || this.paused) return;
    const resources = this.retryResources; this.retryResources = [];
    const takeEvents = (limit: number) => { for (const [key, hint] of this.resources) { if (resources.length >= limit) break; resources.push(hint); this.resources.delete(key); } };
    takeEvents(this.scan || this.nextScan ? Math.max(resources.length, 4) : MAX_BATCH);
    while (resources.length < MAX_BATCH) { const hint = this.nextScannedResource(); if (!hint) break; resources.push(hint); }
    takeEvents(MAX_BATCH);
    const batch: RefreshBatch = { areas: [...this.areas], resources, ...(this.areas.has('history') ? { resetId: this.pendingResetId } : {}) }; if (!batch.areas.length && !batch.resources.length) return;
    this.areas.clear(); this.busy = true; this.lastStarted = Date.now(); this.status(true);
    try { await this.read(batch); if (this.closed) return; this.failures = 0; this.retryUntil = 0; }
    catch (error) { if (this.closed) return; const retryAt = (error as { retryAt?: number } | null)?.retryAt; this.retryUntil = Number.isFinite(retryAt) ? Math.min(Date.now() + 300000, retryAt!) : 0; batch.areas.forEach(area => this.areas.add(area)); if (batch.resetId) this.pendingResetId = Math.max(this.pendingResetId, batch.resetId); this.retryResources = batch.resources; this.failures++; this.paused = this.failures >= 3; this.failed(error, this.paused); }
    finally { this.busy = false; if (!this.closed) { this.status(false); this.arm(this.failures ? Math.min(15000, 3000 * 2 ** (this.failures - 1)) : 250); } }
  }
}
export async function boundedReads<T>(values: T[], work: (value: T) => Promise<void>, concurrency = 4): Promise<void> { let index = 0; const errors: unknown[] = []; await Promise.all(Array.from({ length: Math.min(concurrency, values.length) }, async () => { while (index < values.length) { const value = values[index++]; try { await work(value); } catch (error) { errors.push(error); } } })); if (errors.length) throw errors[0]; }
