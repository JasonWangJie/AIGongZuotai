import type { CloudDraft, DraftNode, DraftWriteInput } from './types';

export type DraftOperation = { key: string; body: DraftWriteInput };
export type DraftEditorState = { projectId: string; server: CloudDraft | null; nodes: DraftNode[]; phase: 'loading' | 'ready' | 'saving' | 'uncertain' | 'conflict' | 'error'; pending: DraftOperation | null; conflict: CloudDraft | null; forceSave: boolean; error: string };
export type DraftAction =
  | { type: 'loaded'; draft: CloudDraft }
  | { type: 'observed'; draft: CloudDraft }
  | { type: 'edit'; nodes: DraftNode[] }
  | { type: 'restore'; nodes: DraftNode[] }
  | { type: 'saving'; operation: DraftOperation }
  | { type: 'saved'; key: string; draft: CloudDraft }
  | { type: 'uncertain'; key: string; error: string }
  | { type: 'conflict'; key: string; error: string }
  | { type: 'conflict_snapshot'; draft: CloudDraft }
  | { type: 'rejected'; key?: string; error: string }
  | { type: 'use_server' }
  | { type: 'keep_mine' };

export function initialDraftState(projectId: string): DraftEditorState { return { projectId, server: null, nodes: [], phase: 'loading', pending: null, conflict: null, forceSave: false, error: '' }; }
export function copyNodes(nodes: DraftNode[]): DraftNode[] { return nodes.map(({ id, title, note }) => ({ id, title, note })); }
export function sameNodes(left: DraftNode[], right: DraftNode[]): boolean { return JSON.stringify(copyNodes(left)) === JSON.stringify(copyNodes(right)); }
export function isDraftDirty(state: DraftEditorState): boolean { return !!state.pending || state.forceSave || !!state.server && !sameNodes(state.nodes, state.server.nodes); }
export function draftSyncLabel(state: DraftEditorState): string {
  if (state.phase === 'loading') return '读取中';
  if (!state.server) return state.phase === 'error' ? '读取失败' : '未连接';
  if (state.phase === 'saving') return '正在保存';
  if (state.phase === 'uncertain') return '受理待确认';
  if (state.phase === 'conflict') return '版本冲突';
  if (isDraftDirty(state)) return '未保存编辑';
  return state.phase === 'error' ? '需要检查' : '已同步';
}
export function draftNodesValid(value: unknown): value is DraftNode[] {
  if (!Array.isArray(value) || value.length > 40) return false;
  const ids = new Set<string>();
  return value.every(node => {
    if (!node || typeof node.id !== 'string' || !/^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,79}$/.test(node.id) || ids.has(node.id) || typeof node.title !== 'string' || node.title.includes('\0') || Array.from(node.title).length > 80 || typeof node.note !== 'string' || node.note.includes('\0') || Array.from(node.note).length > 5000) return false;
    ids.add(node.id); return true;
  });
}
export function draftReducer(state: DraftEditorState, action: DraftAction): DraftEditorState {
  if ('draft' in action && action.draft.project_id !== state.projectId) return state;
  if ('key' in action && action.key && state.pending?.key !== action.key) return state;
  switch (action.type) {
    case 'loaded': return state.phase !== 'loading' && state.server ? state : { ...state, server: action.draft, nodes: copyNodes(action.draft.nodes), phase: 'ready', error: '' };
    case 'observed': return isDraftDirty(state) || ['saving', 'uncertain', 'conflict'].includes(state.phase) || action.draft.revision < (state.server?.revision || 0) ? state : { ...state, server: action.draft, nodes: copyNodes(action.draft.nodes), phase: 'ready', error: '' };
    case 'edit': return !state.server || ['loading', 'saving', 'uncertain'].includes(state.phase) ? state : { ...state, nodes: copyNodes(action.nodes), phase: state.phase === 'conflict' ? 'conflict' : 'ready', error: state.phase === 'conflict' ? state.error : '' };
    case 'restore': return !state.server || ['loading', 'saving', 'uncertain', 'conflict'].includes(state.phase) ? state : { ...state, nodes: copyNodes(action.nodes), phase: 'ready', forceSave: true, error: '' };
    case 'saving': return { ...state, phase: 'saving', pending: action.operation, error: '' };
    case 'saved': return { ...state, server: action.draft, nodes: copyNodes(action.draft.nodes), phase: 'ready', pending: null, conflict: null, forceSave: false, error: '' };
    case 'uncertain': return { ...state, phase: 'uncertain', error: action.error };
    case 'conflict': return { ...state, phase: 'conflict', pending: null, conflict: null, error: action.error };
    case 'conflict_snapshot': return state.phase === 'conflict' && action.draft.revision >= (state.conflict?.revision || 0) ? { ...state, conflict: action.draft } : state;
    case 'rejected': return { ...state, phase: 'error', pending: null, error: action.error };
    case 'use_server': return !state.conflict ? state : { ...state, server: state.conflict, nodes: copyNodes(state.conflict.nodes), phase: 'ready', pending: null, conflict: null, forceSave: false, error: '' };
    case 'keep_mine': return !state.conflict ? state : { ...state, server: state.conflict, phase: 'ready', pending: null, conflict: null, forceSave: true, error: '' };
  }
}
