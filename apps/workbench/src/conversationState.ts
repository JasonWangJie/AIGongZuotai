import type { Run } from './types';
import { terminal } from './types';
import { mergeModeratedText, moderationAllowsText } from './moderation';
import type { ModerationSummary } from './moderation';

export type Conversation = { id: string; project_id: string; title: string; version: number; head_message_id: string | null; active_run_id: string | null; node_count: number; created_at: string; updated_at: string };
export type MessageNode = { id: string; conversation_id: string; parent_id: string | null; role: 'user' | 'assistant'; content: string; run_id: string | null; status: string; seq: number; created_at: string; updated_at: string; model_id?: string; model_name?: string; error?: { code: string; message: string }; moderation?: ModerationSummary };
export type ContextSummary = { input_tokens_estimate: number; output_tokens_reserved: number; context_window_tokens: number; included_messages: number; omitted_messages: number; estimator: string };
export type ConversationPage = { items: Conversation[]; next_cursor?: string };
export type MessagePage = { items: MessageNode[]; leaf_message_id: string | null; next_cursor?: string; has_more: boolean; conversation_version: number };
export type Branch = { leaf_message_id: string; parent_message_id: string | null; run_id: string | null; status: string; preview: string; created_at: string; moderation?: ModerationSummary };
export type BranchPage = { items: Branch[]; next_cursor?: string };
export type TurnInput = { quote_id?: string; expected_version: number; model_id: string; model_version?: string; operation: 'reply' | 'continue'; parent_message_id: string | null; input: string; edited_message_id?: string };
export type ChatOperation = ({ kind: 'conversation'; body: { project_id: string; title?: string } } | { kind: 'turn'; conversationId: string; body: TurnInput }) & { owner: string; project: string; key: string };
export type ChatDraft = { text: string; parent: string | null; edited?: string; mode: 'reply' | 'continue'; baseVersion: number };
export const emptyChatDraft = (): ChatDraft => ({ text: '', parent: null, mode: 'reply', baseVersion: 0 });
type SelectionStorage = Pick<Storage, 'getItem' | 'setItem'>;
function browserSelectionStorage(): SelectionStorage | undefined { try { return typeof window !== 'undefined' ? window.localStorage : undefined; } catch { return undefined; } }

/** Page-memory only: immutable pending identities survive component/auth unmounts. */
export class ConversationRecovery {
  private pending = new Map<string, ChatOperation>();
  private drafts = new Map<string, ChatDraft>();
  private selections = new Map<string, { id: string; leaf?: string }>();
  constructor(private storage: SelectionStorage | undefined = browserSelectionStorage()) {}
  private slot(owner: string, project: string) { return JSON.stringify([owner, project]); }
  getPending(owner: string, project: string) { return this.pending.get(this.slot(owner, project)); }
  remember(operation: ChatOperation) { this.pending.set(this.slot(operation.owner, operation.project), structuredClone(operation)); }
  acknowledge(operation: ChatOperation) { const key = this.slot(operation.owner, operation.project); if (this.pending.get(key)?.key === operation.key) this.pending.delete(key); }
  failed(operation: ChatOperation, unknown: boolean, currentAttempt: boolean) { if (!currentAttempt) return; if (unknown) this.remember(operation); else this.acknowledge(operation); }
  acknowledgeDraft(operation: ChatOperation) {
    if (operation.kind !== 'turn') return;
    const stored = this.draft(operation.owner, operation.project, operation.conversationId);
    if (stored && draftMatchesOperation(stored, operation)) this.saveDraft(operation.owner, operation.project, operation.conversationId, emptyChatDraft());
  }
  draft(owner: string, project: string, conversation: string): ChatDraft | undefined { const draft = this.drafts.get(JSON.stringify([owner, project, conversation])); return draft && { ...draft }; }
  saveDraft(owner: string, project: string, conversation: string, draft: ChatDraft) { const key = JSON.stringify([owner, project, conversation]); if (draft.text || draft.edited || draft.mode === 'continue') this.drafts.set(key, { ...draft }); else this.drafts.delete(key); }
  select(owner: string, project: string, id: string, leaf?: string) { const slot = this.slot(owner, project), value = { id, leaf }; this.selections.set(slot, value); try { this.storage?.setItem('xingtu-chat-selection:' + slot, JSON.stringify(value)); } catch { /* A display preference is optional. */ } }
  selected(owner: string, project: string) {
    const slot = this.slot(owner, project), value = this.selections.get(slot); if (value) return value;
    try { const raw = this.storage?.getItem('xingtu-chat-selection:' + slot); if (!raw || raw.length > 512) return undefined; const parsed = JSON.parse(raw); const validId = (id: unknown) => typeof id === 'string' && /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$/.test(id); if (validId(parsed?.id) && (parsed.leaf === undefined || validId(parsed.leaf))) return { id: parsed.id as string, leaf: parsed.leaf as string | undefined }; } catch { /* Malformed local data cannot grant access. */ }
    return undefined;
  }
  hasUnsaved() { return this.pending.size > 0 || this.drafts.size > 0; }
}
export function mergeConversations(current: Conversation[], incoming: Conversation[], project: string): Conversation[] {
  const map = new Map(current.filter(item => item.project_id === project).map(item => [item.id, item]));
  for (const item of incoming) if (item.project_id === project && (!map.has(item.id) || map.get(item.id)!.version <= item.version)) map.set(item.id, item);
  return [...map.values()].sort((a, b) => b.created_at.localeCompare(a.created_at) || b.id.localeCompare(a.id));
}
export function mergeMessages(current: MessageNode[], incoming: MessageNode[], conversation: string, prepend = false): MessageNode[] {
  const allowed = incoming.filter(item => item.conversation_id === conversation), map = new Map(current.filter(item => item.conversation_id === conversation).map(item => [item.id, item]));
  for (const item of allowed) {
    const previous = map.get(item.id), accepted = !previous || item.seq > previous.seq || item.seq === previous.seq && !(terminal(previous.status) && !terminal(item.status));
    const base = accepted ? item : previous!;
    const value = mergeModeratedText(previous && { ...previous, text: previous.content }, { ...item, text: item.content }, accepted);
    map.set(item.id, { ...base, ...(value.moderation ? { moderation: value.moderation } : {}), content: moderationAllowsText(value.moderation, base.status) ? value.text : '' });
  }
  const order = prepend ? [...allowed, ...current] : [...current, ...allowed];
  return [...new Set(order.map(item => item.id))].flatMap(id => map.has(id) ? [map.get(id)!] : []);
}
export function projectRun(message: MessageNode, runs: Run[]): MessageNode {
  const run = runs.find(item => item.id === message.run_id && item.conversation_id === message.conversation_id && item.assistant_message_id === message.id);
  if (!run) return moderationAllowsText(message.moderation, message.status) ? message : { ...message, content: '' };
  const accepted = run.seq >= message.seq && !(terminal(message.status) && !terminal(run.status));
  const base = accepted ? { ...message, seq: run.seq, status: run.status, model_id: run.model_id, model_name: run.model_name, error: run.error } : message;
  const value = mergeModeratedText({ ...message, text: message.content }, { ...run, text: run.output }, accepted);
  const content = moderationAllowsText(value.moderation, base.status) ? value.text : '';
  if (base === message && message.content === content && message.moderation === value.moderation) return message;
  return { ...base, ...(value.moderation ? { moderation: value.moderation } : {}), content };
}
export function mergeBranches(current: Branch[], incoming: Branch[], append = false): Branch[] {
  const previous = new Map(current.map(branch => [branch.leaf_message_id, branch]));
  const merged = incoming.map(branch => { const old = previous.get(branch.leaf_message_id), accepted = !old || !(terminal(old.status) && !terminal(branch.status)); const base = accepted ? branch : old!; const value = mergeModeratedText(old && { ...old, text: old.preview }, { ...branch, text: branch.preview }, accepted); return { ...base, ...(value.moderation ? { moderation: value.moderation } : {}), preview: moderationAllowsText(value.moderation, base.status) ? value.text : '' }; });
  return append ? [...current.filter(old => !incoming.some(branch => branch.leaf_message_id === old.leaf_message_id)), ...merged] : merged;
}
export function projectBranch(branch: Branch, runs: Run[]): Branch {
  const run = runs.find(item => item.id === branch.run_id && item.assistant_message_id === branch.leaf_message_id);
  if (!run) return moderationAllowsText(branch.moderation, branch.status) ? branch : { ...branch, preview: '' };
  const value = mergeModeratedText({ ...branch, text: branch.preview }, { ...run, text: '' }, false);
  return { ...branch, ...(value.moderation ? { moderation: value.moderation } : {}), preview: moderationAllowsText(value.moderation, branch.status) ? value.text : '' };
}
export function replyDraft(leaf: MessageNode | undefined, version: number): ChatDraft { return { ...emptyChatDraft(), parent: leaf?.role === 'assistant' ? leaf.id : null, baseVersion: version }; }
export function editDraft(message: MessageNode, version: number): ChatDraft { return { text: message.content, parent: message.parent_id, edited: message.id, mode: 'reply', baseVersion: version }; }
export function buildTurn(draft: ChatDraft, model: { id: string; version?: string }): TurnInput {
  return { expected_version: draft.baseVersion, model_id: model.id, ...(model.version ? { model_version: model.version } : {}), operation: draft.mode, parent_message_id: draft.parent, input: draft.mode === 'continue' ? '' : draft.text.trim(), ...(draft.edited ? { edited_message_id: draft.edited } : {}) };
}
export function draftMatchesOperation(draft: ChatDraft, operation: ChatOperation): boolean { return operation.kind === 'turn' && draft.baseVersion === operation.body.expected_version && draft.parent === operation.body.parent_message_id && draft.edited === operation.body.edited_message_id && draft.mode === operation.body.operation && (draft.mode === 'continue' || draft.text.trim() === operation.body.input); }
export function clearAcceptedDraft(draft: ChatDraft, operation: ChatOperation, selectedConversation: string | undefined, leaf: MessageNode | undefined, version: number): ChatDraft { return operation.kind === 'turn' && operation.conversationId === selectedConversation && draftMatchesOperation(draft, operation) ? replyDraft(leaf, version) : draft; }
export function validTurnTarget(draft: ChatDraft, messages: MessageNode[]): boolean {
  if (draft.edited) { const message = messages.find(item => item.id === draft.edited); return draft.mode === 'reply' && message?.role === 'user' && moderationAllowsText(message.moderation, message.status) && message.parent_id === draft.parent; }
  if (draft.parent === null) return draft.mode === 'reply';
  const parent = messages.find(item => item.id === draft.parent);
  // A new question can retain its tree parent while the server omits that parent's isolated complete turn from model context.
  return !!parent && parent.role === 'assistant' && terminal(parent.status) && (draft.mode === 'reply' || moderationAllowsText(parent.moderation, parent.status) && !!parent.content.trim());
}
export function chatReadCurrent(alive: boolean, generation: number, ticket: number, selectedGeneration: number, selection?: number): boolean { return alive && generation === ticket && (selection === undefined || selectedGeneration === selection); }
export function messageProviderLabel(run?: Run): string { return run?.provider_mode === 'demo' ? '演示输出' : run?.provider_mode === 'live' ? '模型输出' : '来源待核对'; }
