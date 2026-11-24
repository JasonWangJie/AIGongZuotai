import type { TaskInput } from './types';

export type PendingOperation = ({ kind: 'task'; body: TaskInput } | { kind: 'run'; body: { project_id: string; model_id: string; model_version?: string; input: string; quote_id?: string } }) & { key: string; ownerId: string; workspaceId?: string };
export class PendingRequests {
  private entries = new Map<string, PendingOperation>();
  private slot(ownerId: string, projectId: string): string { return JSON.stringify([ownerId, projectId]); }
  get(ownerId: string, projectId: string): PendingOperation | undefined { return this.entries.get(this.slot(ownerId, projectId)); }
  remember(operation: PendingOperation): void { this.entries.set(this.slot(operation.ownerId, operation.body.project_id), operation); }
  acknowledge(operation: PendingOperation): void { const key = this.slot(operation.ownerId, operation.body.project_id); if (this.entries.get(key)?.key === operation.key) this.entries.delete(key); }
  failed(operation: PendingOperation, unknown: boolean, current: boolean): void { if (!current) return; if (unknown) this.remember(operation); else this.acknowledge(operation); }
}
type FlightToken = { key: string; revision: number };
export class SubmissionFlights {
  private entries = new Map<string, FlightToken>();
  has(project: string): boolean { return this.entries.has(project); }
  claim(project: string, key: string, revision: number): FlightToken { const token = { key, revision }; this.entries.set(project, token); return token; }
  release(project: string, token: FlightToken): void { if (this.entries.get(project) === token) this.entries.delete(project); }
  clear(): void { this.entries.clear(); }
}
