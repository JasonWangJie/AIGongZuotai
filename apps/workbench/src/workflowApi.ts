import { request } from './api';
import type { QuoteRequest } from './billing';
import type { WorkflowDefinition, WorkflowExecution, WorkflowGraph, WorkflowIntent } from './workflows';

export type RerunPreview = { source_workflow_id: string; source_version: number; from_node: string; affected_nodes: string[]; inherited_nodes: string[] };
export type RerunInput = { expected_version: number; from_node: string; prompt?: string; input?: string };
const part = encodeURIComponent;
function base(p: string, collection: string) { return '/projects/' + part(p) + '/workflow-' + collection; }
function read<T>(p: string, url: string, signal?: AbortSignal, key?: string) {
  return request<T>(url, { signal, headers: { 'X-Project-ID': p, ...(key && { 'Idempotency-Key': key }) } });
}
function write<T>(p: string, url: string, key: string, body: unknown) {
  return request<T>(url, { method: 'POST', headers: { 'X-Project-ID': p, 'Idempotency-Key': key }, body: JSON.stringify(body) });
}
function nodePath(p: string, run: string, node: string) { return base(p, 'runs') + '/' + part(run) + '/nodes/' + part(node); }
export const workflowApi = {
  definitions: (p: string, signal?: AbortSignal) => read<{items: WorkflowDefinition[]}>(p, base(p, 'definitions'), signal),
  version: (p: string, id: string, version: number) => read<WorkflowDefinition>(p, base(p, 'definitions') + '/' + part(id) + '/versions/' + version),
  save: (p: string, key: string, definition: WorkflowDefinition | null, graph: WorkflowGraph) => write<WorkflowDefinition>(p, base(p, 'definitions'), key, { definition_id: definition?.id, expected_version: definition?.version || 0, graph }),
  runs: (p: string, signal?: AbortSignal) => read<{items: WorkflowExecution[]}>(p, base(p, 'runs'), signal),
  run: (p: string, id: string, signal?: AbortSignal) => read<WorkflowExecution>(p, base(p, 'runs') + '/' + part(id), signal),
  start: (p: string, key: string, d: WorkflowDefinition, input: string) => write<WorkflowExecution>(p, base(p, 'runs'), key, {definition_id: d.id, definition_version: d.version, input}),
  input: (p: string, run: string, node: string, signal?: AbortSignal) => read<QuoteRequest>(p, nodePath(p, run, node) + '/input', signal),
  execute: (p: string, key: string, run: string, node: string, version: number, quote?: string) => write<WorkflowExecution>(p, nodePath(p, run, node) + '/execute', key, {expected_version: version, quote_id: quote}),
  confirm: (p: string, key: string, run: string, node: string, version: number, assetId: string, revision: number) => write<WorkflowExecution>(p, nodePath(p, run, node) + '/confirm', key, {expected_version: version, asset_id: assetId, revision}),
  preview: (p: string, run: string, node: string, signal?: AbortSignal) => read<RerunPreview>(p, base(p, 'runs') + '/' + part(run) + '/rerun/preview?from_node=' + part(node), signal),
  rerun: (p: string, key: string, run: string, body: RerunInput) => write<WorkflowExecution>(p, base(p, 'runs') + '/' + part(run) + '/rerun', key, body),
  cancel: (p: string, key: string, run: string, version: number) => write<WorkflowExecution>(p, base(p, 'runs') + '/' + part(run) + '/cancel', key, {expected_version:version}),
  receipt: (p: string, intent: WorkflowIntent) => {
    const collection = intent.action === 'definition' ? 'definitions' : 'runs';
    if ((intent.action === 'rerun' || intent.action === 'cancel') && intent.run)
      return read<WorkflowExecution>(p, base(p, 'runs') + '/' + part(intent.run) + '/' + intent.action + '/receipt', undefined, intent.key);
    const path = intent.node && intent.run ? nodePath(p, intent.run, intent.node) : base(p, collection);
    return read<WorkflowDefinition | WorkflowExecution>(p, path + '/receipt', undefined, intent.key);
  },
};

