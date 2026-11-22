// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { expect, it, vi } from 'vitest';
import { api, ApiError } from './api';
import type { Conversation, MessageNode, MessagePage } from './conversationState';
import { ConversationRecovery } from './conversationState';
import { useConversations } from './useConversations';
import type { Bootstrap, Run } from './types';

it('discards a hidden-window read, retries only on an explicit action and purges denied scope', async () => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  const host = document.createElement('div');
  const root = createRoot(host);
  const ids = Array.from({length:180}, (_,i) => 'm' + i);
  const leaf = ids.at(-1)!;
  const conversation: Conversation = {id:'c', project_id:'p', title:'', version:91, head_message_id:leaf,
    active_run_id:null, node_count:180, created_at:'', updated_at:''};
  const recovery = new ConversationRecovery(), runs: Run[] = [];
  const bootstrap = {project:{id:'p'}, user:{id:'owner'}} as Bootstrap;
  const noop = () => {};
  function page(n: number): MessagePage {
    const items: MessageNode[] = Array.from({length:20}, (_,i) => {
      const index = n * 20 + i;
      return {id:ids[index],conversation_id:'c',role:'assistant',run_id:'run'+index,
        parent_id:ids[index-1]||null,seq:1,status:'COMPLETED',content:'body-'+index,created_at:'',updated_at:''};
    });
    return {items,leaf_message_id:leaf,has_more:n>0,next_cursor:n?'p'+(n-1):undefined,conversation_version:91};
  }
  vi.spyOn(api, 'conversation').mockResolvedValue(conversation);
  vi.spyOn(api, 'conversations').mockResolvedValue({items:[conversation]});
  vi.spyOn(api, 'conversationBranches').mockResolvedValue({items:[]});
  let mode: 'success' | 'wait' | 'failed' | 'denied' = 'success';
  let pending!: (page: MessagePage) => void, pendingSignal: AbortSignal | undefined;
  const read = vi.spyOn(api,'conversationMessages').mockImplementation(async (_p, _c, fixed, before, signal) => {
    expect(fixed).toBe(leaf);
    if (mode === 'denied') throw new ApiError('scope denied',404);
    if (mode === 'failed') throw new ApiError('temporary',503);
    if (mode === 'wait') { pendingSignal=signal; return new Promise(resolve => {pending=resolve;}); }
    return page(before ? Number(before.slice(1)) : 8);
  });
  let chat!: ReturnType<typeof useConversations>;
  function Probe({visible}: {visible:boolean}) {
    chat=useConversations({bootstrap,recovery,runs,visible,refreshVersion:0,onRun:noop,onWatch:noop,onAuthExpired:noop});
    return null;
  }
  try {
    await act(async()=>root.render(<Probe visible/>));
    await act(async()=>chat.select('c',leaf));
    act(()=>chat.setText('untouched'));
    for (let i=0;i<8;i++) await act(async()=>chat.loadMoreMessages());
    expect(chat.getMessage(ids[165])).toBeUndefined();
    expect(chat.bodyStats?.indexed).toBe(180);
    mode='wait';
    await act(async()=>chat.showWindow([ids[165]]));
    expect(pendingSignal?.aborted).toBe(false);
    await act(async()=>root.render(<Probe visible={false}/>));
    expect(pendingSignal?.aborted).toBe(true);
    await act(async()=>pending(page(8)));
    expect(chat.getMessage(ids[165])).toBeUndefined();
    mode='failed';
    await act(async()=>root.render(<Probe visible/>));
    const afterFailure=read.mock.calls.length;
    expect(chat.bodyError).toBeTruthy();
    act(()=>chat.setText('still untouched'));
    await act(async()=>chat.showWindow([ids[165]]));
    expect(read).toHaveBeenCalledTimes(afterFailure);
    mode='success';
    await act(async()=>chat.retryBodies());
    expect(chat.getMessage(ids[165])?.content).toBe('body-165');
    expect(chat.draft.parent).toBe(leaf);
    expect(chat.draft.text).toBe('still untouched');
    mode='denied';
    await act(async()=>chat.showWindow([ids[145]]));
    expect(chat.conversation).toBeNull();
    expect(chat.messageIds).toHaveLength(0);
    expect(chat.getMessage(ids[165])).toBeUndefined();
    expect(recovery.draft('owner','p','c')?.text).toBe('still untouched');
    expect(recovery.draft('other','p','c')).toBeUndefined();
  } finally {
    act(()=>root.unmount());
    vi.restoreAllMocks(); vi.unstubAllGlobals();
  }
});

