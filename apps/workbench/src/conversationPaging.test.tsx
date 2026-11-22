// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { expect, it, vi } from 'vitest';
import { useConversations } from './useConversations';
import { api } from './api';
import { ConversationRecovery } from './conversationState';
import type { Conversation, MessageNode, MessagePage } from './conversationState';
import type { Bootstrap, Run } from './types';

it('discards a prior branch page after switching and keeps the complete history cursor and unsent draft across refresh', async () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  const host = document.createElement('div');
  document.body.append(host);
  const root = createRoot(host);
  const conversation: Conversation = {id:'c', project_id:'p', title:'', version:4, head_message_id:'b',
    active_run_id:null, node_count:4, created_at:'', updated_at:''};
  const boot = {project:{id:'p'}, user:{id:'owner'}} as Bootstrap;
  const runs: Run[] = [];
  const draftStore = new ConversationRecovery();
  const noop = () => {};
  const node = (id: string): MessageNode => ({id, conversation_id:'c', parent_id:null, role:'assistant',
    content:id, run_id:null, status:'COMPLETED', seq:1, created_at:'', updated_at:''});
  const page = (leaf: string, id: string, cursor?: string): MessagePage => ({
    items:[node(id)], leaf_message_id:leaf, next_cursor:cursor, has_more:!!cursor, conversation_version:4,
  });
  let finishOld!: (result: MessagePage) => void;
  let oldSignal: AbortSignal | undefined;
  vi.spyOn(api, 'conversation').mockResolvedValue(conversation);
  vi.spyOn(api, 'conversations').mockResolvedValue({items:[conversation]});
  vi.spyOn(api, 'conversationBranches').mockResolvedValue({items:[]});
  vi.spyOn(api, 'conversationMessages').mockImplementation(async (_project, _id, leaf, cursor, signal) => {
    if (cursor === 'older-a') {
      oldSignal = signal;
      return new Promise(resolve => { finishOld = resolve; });
    }
    if (cursor === 'older-b') return page('b', 'base');
    return page(leaf!, leaf!, 'older-' + leaf);
  });
  let chat!: ReturnType<typeof useConversations>;
  function Probe() {
    chat = useConversations({bootstrap:boot, recovery:draftStore, runs, refreshVersion:0, visible:true,
      onAuthExpired:noop, onRun:noop, onWatch:noop});
    return null;
  }
  try {
    await act(async () => root.render(<Probe/>));
    await act(async () => chat.select('c', 'a'));
    act(() => chat.setText('unsent'));
    let stale!: Promise<void>;
    act(() => { stale = chat.loadMoreMessages(); });
    await act(async () => chat.select('c', 'b'));
    expect(oldSignal?.aborted).toBe(true);
    await act(async () => { finishOld(page('a', 'old-private')); await stale; });
    expect(chat.messageIds).toEqual(['b']);
    expect(chat.before).toBe('older-b');
    expect(chat.draft.text).toBe('unsent');
    expect(chat.draft.parent).toBe('a');
    await act(async () => chat.loadMoreMessages());
    expect(chat.before).toBeUndefined();
    await act(async () => chat.refresh());
    expect(chat.messageIds).toEqual(['base','b']);
    expect(chat.before).toBeUndefined();
    expect(chat.draft.parent).toBe('a');
  } finally {
    act(() => root.unmount());
    host.remove();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  }
});

