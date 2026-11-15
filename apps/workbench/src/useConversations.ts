import { billingEnabled, billingScope, quoteMatches } from './billing';
import type { QuoteRequest, QuotedRequest } from './billing';
import { useEffect, useRef, useState } from 'react';
import { api, ApiError } from './api';
import { useInvalidation } from './useInvalidation';
import { keepUnknown } from './requestFeedback';
import { terminal } from './types';
import type { Bootstrap, Model, Run } from './types';
import { buildTurn, chatReadCurrent, clearAcceptedDraft, ConversationRecovery, editDraft, emptyChatDraft, mergeBranches, mergeConversations, mergeMessages, projectBranch, projectRun, replyDraft, validTurnTarget } from './conversationState';
import { moderationAllowsText } from './moderation';
import type { Branch, ChatDraft, ChatOperation, Conversation, MessageNode } from './conversationState';
import { modelCapability, promptBytes } from './modelCatalog';

type Options = { bootstrap: Bootstrap; recovery: ConversationRecovery; runs: Run[]; refreshVersion: number; visible: boolean; generationAllowed?: boolean; onRun: (run: Run) => void; onWatch: (id: string | null) => void; onAuthExpired: () => void };
export function useConversations(options: Options) {
  const { bootstrap, recovery, runs, refreshVersion, visible } = options, owner = bootstrap.user?.id || 'demo', project = bootstrap.project.id;
  const callbacks = useRef(options); callbacks.current = options;
  const [items, setItems] = useState<Conversation[]>([]), [cursor, setCursor] = useState<string>();
  const [conversation, setConversation] = useState<Conversation | null>(null), [messages, setMessages] = useState<MessageNode[]>([]);
  const [leaf, setLeaf] = useState<string | undefined>(), [before, setBefore] = useState<string>();
  const [branches, setBranches] = useState<Branch[]>([]), [branchCursor, setBranchCursor] = useState<string>();
  const [draft, setDraftState] = useState<ChatDraft>(emptyChatDraft), [pending, setPending] = useState<ChatOperation | null>(() => recovery.getPending(owner, project) || null);
  const [loading, setLoading] = useState(false), [listing, setListing] = useState(false), [busy, setBusy] = useState(false), [issue, setIssue] = useState<ApiError | null>(null), [notice, setNotice] = useState('');
  const alive = useRef(true), generation = useRef(0), selectionGeneration = useRef(0), selectedId = useRef<string | undefined>(undefined), selectedLeaf = useRef<string | undefined>(undefined), draftRef = useRef(draft), currentConversation = useRef(conversation);
  const reading = useRef(false), listingLock = useRef(false), mutationLock = useRef(false), readAbort = useRef(new AbortController()), listAbort = useRef(new AbortController());
  draftRef.current = draft; currentConversation.current = conversation;
  const projected = messages.map(message => projectRun(message, runs));
  const currentMessages = useRef(projected); currentMessages.current = projected;
  const activeId = conversation?.active_run_id, activeSnapshot = runs.find(run => run.id === activeId);
  const activeRun = activeId && (!activeSnapshot || !terminal(activeSnapshot.status)) ? activeId : null;
  const hasDraft = !!draft.text || !!draft.edited || draft.mode === 'continue';
  function current(ticket: number, selection?: number) { return chatReadCurrent(alive.current, generation.current, ticket, selectionGeneration.current, selection); }
  function fail(error: unknown, ticket: number, selection?: number) { if (!current(ticket, selection)) return; const value = error instanceof ApiError ? error : new ApiError(error instanceof Error ? error.message : '对话读取失败'); setIssue(value); if (value.status === 401) callbacks.current.onAuthExpired(); }
  function setDraft(value: ChatDraft) { draftRef.current = value; setDraftState(value); if (selectedId.current) recovery.saveDraft(owner, project, selectedId.current, value); }
  async function list(next?: string) {
    if (listingLock.current) return; listingLock.current = true; setListing(true); const ticket = generation.current;
    try { const page = await api.conversations(project, next, listAbort.current.signal); if (!current(ticket)) return; setItems(old => mergeConversations(old, page.items, project)); setCursor(page.next_cursor || undefined); }
    catch (error) { fail(error, ticket); }
    finally { if (current(ticket)) { listingLock.current = false; setListing(false); } }
  }
  async function readSelected(refresh = false) {
    const id = selectedId.current; if (!id || reading.current) return; const ticket = generation.current, selection = selectionGeneration.current, wantedLeaf = selectedLeaf.current;
    reading.current = true; setLoading(true);
    try {
      const [value, page, branchPage] = await Promise.all([api.conversation(project, id, readAbort.current.signal), api.conversationMessages(project, id, wantedLeaf, undefined, readAbort.current.signal), api.conversationBranches(project, id, undefined, readAbort.current.signal)]);
      if (!current(ticket, selection)) return;
      if (value.project_id !== project || value.id !== id || page.items.some(item => item.conversation_id !== id)) throw new ApiError('会话身份不匹配，已停止加载。');
      setConversation(old => old?.id === value.id && old.version > value.version ? old : value); setItems(old => mergeConversations(old, [value], project));
      const nextLeaf = page.leaf_message_id || undefined; selectedLeaf.current = nextLeaf; setLeaf(nextLeaf); recovery.select(owner, project, id, nextLeaf);
      setMessages(old => mergeMessages(refresh && wantedLeaf === nextLeaf ? old : [], page.items, id));
      if (!refresh || wantedLeaf !== nextLeaf || !before) setBefore(page.has_more ? page.next_cursor : undefined);
      setBranches(old => mergeBranches(old, branchPage.items)); setBranchCursor(branchPage.next_cursor || undefined);
      const stored = recovery.draft(owner, project, id);
      if (stored) { draftRef.current = stored; setDraftState(stored); }
      else { const next = replyDraft(page.items.at(-1), Math.max(value.version, page.conversation_version)); draftRef.current = next; setDraftState(next); }
      setIssue(old => old?.uncertain || recovery.getPending(owner, project) ? old : null);
    } catch (error) { fail(error, ticket, selection); }
    finally { if (current(ticket, selection)) { reading.current = false; setLoading(false); } }
  }
  async function select(id: string, selected?: string) {
    selectionGeneration.current++; readAbort.current.abort(); readAbort.current = new AbortController(); reading.current = false;
    selectedId.current = id; selectedLeaf.current = selected; setLeaf(selected); setConversation(null); setMessages([]); setBranches([]); setBefore(undefined); setBranchCursor(undefined); setIssue(null); setNotice('');
    const stored = recovery.draft(owner, project, id) || emptyChatDraft(); draftRef.current = stored; setDraftState(stored); callbacks.current.onWatch(null);
    recovery.select(owner, project, id, selected); await readSelected();
  }
  async function loadMoreMessages() {
    const id = selectedId.current; if (!id || !before || reading.current) return; const ticket = generation.current, selection = selectionGeneration.current; reading.current = true; setLoading(true);
    try { const page = await api.conversationMessages(project, id, selectedLeaf.current, before, readAbort.current.signal); if (!current(ticket, selection)) return; if (page.leaf_message_id !== selectedLeaf.current) throw new ApiError('分支游标已变化，请刷新当前分支。'); setMessages(old => mergeMessages(old, page.items, id, true)); setBefore(page.has_more ? page.next_cursor : undefined); }
    catch (error) { fail(error, ticket, selection); }
    finally { if (current(ticket, selection)) { reading.current = false; setLoading(false); } }
  }
  async function loadMoreBranches() {
    const id = selectedId.current; if (!id || !branchCursor || reading.current) return; const ticket = generation.current, selection = selectionGeneration.current; reading.current = true; setLoading(true);
    try { const page = await api.conversationBranches(project, id, branchCursor, readAbort.current.signal); if (!current(ticket, selection)) return; setBranches(old => mergeBranches(old, page.items, true)); setBranchCursor(page.next_cursor || undefined); }
    catch (error) { fail(error, ticket, selection); }
    finally { if (current(ticket, selection)) { reading.current = false; setLoading(false); } }
  }
  async function submitOperation(operation: ChatOperation) {
    if (mutationLock.current || operation.owner !== owner || operation.project !== project || Date.now() < (issue?.retryAt || 0)) return false;
    const ticket = generation.current, selection = selectionGeneration.current, priorUnknown = recovery.getPending(owner, project)?.key === operation.key;
    recovery.remember(operation); setPending(operation); mutationLock.current = true; setBusy(true); setIssue(null);
    try {
      const value = operation.kind === 'conversation' ? await api.createConversation(operation.body, operation.key) : await api.createConversationRun(project, operation.conversationId, operation.body, operation.key);
      if (value.project_id !== project || operation.kind === 'turn' && (value as Run).conversation_id !== operation.conversationId) throw new ApiError('返回的会话身份不匹配，请核对原请求。', 0, true);
      recovery.acknowledge(operation);
      recovery.acknowledgeDraft(operation);
      if (!current(ticket)) return false;
      const acceptedDraft = clearAcceptedDraft(draftRef.current, operation, currentConversation.current?.id, currentMessages.current.at(-1), currentConversation.current?.version || 0);
      if (acceptedDraft !== draftRef.current) setDraft(acceptedDraft);
      setPending(null); setIssue(null);
      if (operation.kind === 'conversation') { setItems(old => mergeConversations(old, [value as Conversation], project)); if (current(ticket, selection)) await select(value.id); }
      else { const run = value as Run; callbacks.current.onRun(run); if (current(ticket, selection) && selectedId.current === operation.conversationId) { await select(operation.conversationId, run.assistant_message_id); setNotice(run.moderation ? '本轮已受理，完整生成并通过文本检查后展示。' : '本轮已受理，输出正在流入。'); } }
      void list(); return true;
    } catch (error) {
      const apiError = error instanceof ApiError ? error : new ApiError('无法确认请求结果。', 0, true), unknown = keepUnknown(priorUnknown, apiError);
      recovery.failed(operation, unknown, current(ticket));
      if (current(ticket)) { setPending(unknown ? operation : null); fail(apiError, ticket); if (['CONVERSATION_VERSION_CONFLICT', 'CONVERSATION_BUSY'].includes(apiError.code || '')) setNotice('会话已发生变化。你的输入与分支目标已保留；刷新后确认版本再发送。'); }
      return false;
    } finally { if (current(ticket)) { mutationLock.current = false; setBusy(false); } }
  }
  function create(title: string) { if (pending || busy) return; void submitOperation({ kind: 'conversation', owner, project, key: crypto.randomUUID(), body: { project_id: project, ...(title.trim() ? { title: title.trim() } : {}) } }); }
  function send(model?: Model, quoted?: QuotedRequest) {
    if (callbacks.current.generationAllowed === false) { setNotice('工作区生成权限尚未开放；输入仍保留，原请求可继续核对。'); return; }
    const value = currentConversation.current, next = draftRef.current, cap = modelCapability(bootstrap, model);
    if (!value || !model || !cap.enabled || pending || busy || activeRun || ['CONVERSATION_VERSION_CONFLICT', 'CONVERSATION_BUSY'].includes(issue?.code || '') || next.baseVersion !== value.version || !validTurnTarget(next, projected) || next.mode === 'reply' && (!next.text.trim() || promptBytes(next.text.trim()) > cap.max_prompt_bytes)) return;
    const body = buildTurn(next, model), request: QuoteRequest = { resource_type: 'conversation_run', conversation_id: value.id, conversation_input: body };
    if (billingEnabled(bootstrap) && !quoteMatches(quoted, billingScope(bootstrap), request)) { setNotice('输入、分支或模型已变化，请重新获取并确认本轮报价。'); return; }
    return submitOperation({ kind: 'turn', owner, project, key: crypto.randomUUID(), conversationId: value.id, body: { ...body, ...(billingEnabled(bootstrap) ? { quote_id: quoted!.quote.id } : {}) } });
  }
  function edit(message: MessageNode) { if (pending || busy || !conversation || message.role !== 'user' || !moderationAllowsText(message.moderation, message.status)) return; setDraft(editDraft(message, conversation.version)); setNotice('编辑后将创建兄弟分支，原消息与回复会保留。'); }
  function continueFrom(message: MessageNode) { if (pending || busy || !conversation || message.role !== 'assistant' || !terminal(message.status) || !moderationAllowsText(message.moderation, message.status) || !message.content.trim()) return; setDraft({ text: '', parent: message.id, mode: 'continue', baseVersion: conversation.version }); setNotice('继续是一次新的模型请求。确认所选回复后，点击“确认继续生成”。'); }
  function useCurrentVersion() { if (conversation) setDraft({ ...draftRef.current, baseVersion: conversation.version }); setIssue(null); }
  function resetDraft() { if (!conversation || pending || busy) return; setDraft(replyDraft(projected.at(-1), conversation.version)); setNotice(''); }
  function refresh() { if (Date.now() < (issue?.retryAt || 0)) return; void list(); void readSelected(true); }
  async function showActive() { if (!activeRun || !conversation) return; const ticket = generation.current, selection = selectionGeneration.current; try { const run = await api.snapshot(activeRun, project); if (current(ticket, selection) && run.conversation_id === conversation.id) { callbacks.current.onRun(run); await select(conversation.id, run.assistant_message_id); } } catch (error) { fail(error, ticket, selection); } }
  useEffect(() => {
    alive.current = true; generation.current++; reading.current = false; listingLock.current = false; mutationLock.current = false; readAbort.current = new AbortController(); listAbort.current = new AbortController();
    const saved = recovery.selected(owner, project); void list(); if (saved) void select(saved.id, saved.leaf);
    return () => { alive.current = false; generation.current++; selectionGeneration.current++; readAbort.current.abort(); listAbort.current.abort(); callbacks.current.onWatch(null); };
  }, [owner, project]);
  useInvalidation(refreshVersion, visible, loading || listing || busy || Date.now() < (issue?.retryAt || 0), refresh);
  const watchId = projected.at(-1)?.run_id || null;
  useEffect(() => { if (visible) callbacks.current.onWatch(watchId); else callbacks.current.onWatch(null); }, [watchId, visible]);
  return { items, cursor, conversation, messages: projected, leaf, before, branches: branches.map(branch => projectBranch(branch, runs)), branchCursor, draft, hasDraft, pending, loading, listing, busy, issue, notice, activeRun, create, select, list, send, edit, continueFrom, resetDraft, useCurrentVersion, refresh, showActive, loadMoreMessages, loadMoreBranches, setText: (text: string) => setDraft({ ...draftRef.current, text }), retry: () => pending && submitOperation(pending) };
}
