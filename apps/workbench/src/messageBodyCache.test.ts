import { describe, expect, it, vi } from 'vitest';
import { MessageBodyCache, maxCachedMessageBytes, maxCachedMessages, maxMessagePageBytes } from './messageBodyCache';
import type { MessageNode, MessagePage } from './conversationState';
import { readMessagePageText } from './readMessagePageText';

const id = (n: number) => 'msg-' + n;
const text = (n: number) => '回复 ' + n + '\n' + (n % 7) + ' 段';
function node(n: number, content = text(n)): MessageNode {
  return {id:id(n), conversation_id:'c', role:'assistant', parent_id:n ? id(n - 1) : null,
    run_id:'run-' + n, content, status:'COMPLETED', seq:n + 1, created_at:'', updated_at:''};
}
const page = (leaf: string, items: MessageNode[], next?: string): MessagePage => ({
  items, leaf_message_id:leaf, next_cursor:next, has_more:!!next, conversation_version:10000,
});
const live = () => true;

describe('fixed-leaf message body retention', () => {
  it('indexes 500 real-shaped pages and reverse-reads evicted bodies using only their first-issued cursor', async () => {
    const leaf = id(9999);
    const cache = new MessageBodyCache('c', leaf);
    const fetchPage = (cursor: string) => {
      const pageNumber = cursor === '' ? 499 : Number(cursor.slice(1));
      const rows = Array.from({length:20}, (_, i) => node(pageNumber * 20 + i));
      return page(leaf, rows, pageNumber ? 'p' + (pageNumber - 1) : undefined);
    };
    let cursor = '';
    for (let i = 499; i >= 0; i--) {
      const received = fetchPage(cursor);
      cache.ingest(received, cursor, i === 499 ? 'head' : 'earlier');
      cache.pin([leaf]);
      expect(cache.stats().resident).toBeLessThanOrEqual(maxCachedMessages);
      expect(cache.stats().bytes).toBeLessThanOrEqual(maxCachedMessageBytes);
      cursor = received.next_cursor || '';
    }
    expect(cache.ids).toEqual(Array.from({length:10000}, (_, n) => id(n)));
    expect(cache.get(id(5045))).toBeUndefined();
    const read = vi.fn(async (source: string) => fetchPage(source));
    const window = [id(5039), id(5040), id(5045), leaf];
    cache.pin(window);
    await cache.restore(window, read, live);
    expect(read.mock.calls.map(([s]) => s)).toEqual(['p251','p252']);
    for (const n of [5039,5040,5045,9999]) expect(cache.get(id(n))?.content).toBe(text(n));
    for (let i = 0; i < 50; i++) await cache.restore(window, read, live);
    expect(read).toHaveBeenCalledTimes(2);
  });

  it('limits multibyte bodies by UTF8 bytes even before the row-count ceiling', () => {
    const cache = new MessageBodyCache('c', id(95));
    const bulk = '星'.repeat(40000); // 120 kB per row, eight fit in one wire page.
    for (let p = 11; p >= 0; p--) {
      cache.ingest(page(id(95), Array.from({length:8}, (_, i) => node(p*8+i, bulk))), p === 11 ? '' : 'p'+p, p===11?'head':'earlier');
      cache.pin([id(95)]);
    }
    expect(cache.stats()).toEqual({indexed:96, resident:69, bytes:69*120000});
    expect(cache.get(id(95))?.content).toBe(bulk);
  });

  it('drops a late restore, rejects foreign leaf/identity, and preserves moderation after eviction', async () => {
    const leaf = id(159), cache = new MessageBodyCache('c', leaf);
    for (let p = 7; p >= 0; p--) {
      cache.ingest(page(leaf, Array.from({length:20}, (_, i) => node(p*20+i))), p===7?'':'p'+p, p===7?'head':'earlier');
    }
    const target = 145;
    expect(cache.hasBody(id(target))).toBe(false);
    cache.recordDecision(id(target), 'run-145', 146, 'COMPLETED',
      {policy_id:'policy',policy_version:'v1',phase:'output',state:'BLOCKED',version:2});
    let active = true;
    await cache.restore([id(target)], async () => { active=false; return page(leaf, [node(target)]); }, () => active);
    expect(cache.hasBody(id(target))).toBe(false);
    await expect(cache.restore([id(target)], async () => page('other', [node(target)]), live)).rejects.toThrow();
    await expect(cache.restore([id(target)], async () => page(leaf, [{...node(target),run_id:'foreign'}]), live)).rejects.toThrow();
    cache.pin([id(target)]);
    await cache.restore([id(target)], async () => page(leaf, [node(target)]), live);
    expect(cache.get(id(target))?.content).toBe('');
    expect(cache.get(id(target))?.moderation?.state).toBe('BLOCKED');
  });

  it('handles a shorter replay page with a bounded next-cursor walk and no repeated source request', async () => {
    const leaf = id(139), c = new MessageBodyCache('c', leaf);
    for (let p=6;p>=0;p--) {
      c.ingest(page(leaf, Array.from({length:20},(_,i)=>node(p*20+i))), p===6?'':'p'+p,p===6?'head':'earlier');
    }
    const wanted = [id(139),id(135),id(132)];
    c.pin(wanted);
    const read = vi.fn(async (source: string) => source === ''
      ? page(leaf, [node(139)], 'subpage')
      : page(leaf, [node(132),node(135)], 'unused'));
    await c.restore(wanted,read,live);
    expect(read.mock.calls.map(([s])=>s)).toEqual(['','subpage']);
    expect(c.get(id(132))?.content).toBe(text(132));
  });
});

it('enforces the single-page wire budget on decoded chunks, including a dishonest or missing Content-Length', async () => {
  const encode = new TextEncoder(), bytes = encode.encode('甲乙');
  const split = new ReadableStream({start(c){c.enqueue(bytes.slice(0,1));c.enqueue(bytes.slice(1));c.close();}});
  expect(await readMessagePageText(new Response(split),bytes.length)).toBe('甲乙');
  for (const headers of [{}, {'Content-Length':'1'}] as HeadersInit[]) {
    const cancel = vi.fn();
    const stream = new ReadableStream({start(c){c.enqueue(new Uint8Array(maxMessagePageBytes));c.enqueue(new Uint8Array(1));},cancel});
    await expect(readMessagePageText(new Response(stream,{headers}),maxMessagePageBytes)).rejects.toThrow();
    expect(cancel).toHaveBeenCalledOnce();
  }
});

