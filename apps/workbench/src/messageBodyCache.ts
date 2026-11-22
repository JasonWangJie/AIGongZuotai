import { uiText } from './locale';
import { mergeMessages } from './conversationState';
import type { MessageNode, MessagePage } from './conversationState';
import { mergeModeration, moderationAllowsText, validModeration } from './moderation';
import type { ModerationSummary } from './moderation';
import { terminal } from './types';

export const maxMessagePageBytes = 1 << 20;
export const maxCachedMessageBytes = 8 << 20;
export const maxCachedMessages = 120;
const encoder = new TextEncoder();
const invalid = () => new Error(uiText('消息分页与当前分支不匹配，请重新打开会话。'));

type Entry = { cursor: string; seq: number; status: string; role: string; parent: string | null; runId: string | null; moderation?: ModerationSummary };
type Body = { node: MessageNode; bytes: number };

function decisionFloor(value?: ModerationSummary): ModerationSummary | undefined {
  if (!value) return undefined;
  if (!validModeration(value)) return {policy_id:'',policy_version:'',phase:'output',state:'REVIEW',version:0};
  // Keep only fixed decision fields after eviction. A detail, error or body
  // attached to a malformed input can never become part of the long index.
  return {policy_id:value.policy_id, policy_version:value.policy_version, phase:value.phase,
    state:value.state, version:value.version};
}

export class MessageBodyCache {
  ids: string[] = [];
  private entries = new Map<string, Entry>();
  private bodies = new Map<string, Body>();
  private pinned = new Set<string>();
  private bytes = 0;
  constructor(readonly conversation: string, readonly leaf: string | null) {}

  stats() { return { indexed: this.ids.length, resident: this.bodies.size, bytes: this.bytes }; }
  get(id: string | null | undefined) { return id ? this.bodies.get(id)?.node : undefined; }
  has(id: string) { return this.entries.has(id); }
  runId(id: string | undefined) { return id ? this.entries.get(id)?.runId : undefined; }
  cursor(id: string) { return this.entries.get(id)?.cursor; }
  hasBody(id: string) { return this.bodies.has(id); }
  pin(ids: readonly string[]) {
    const selected = new Set(ids.filter(id => this.entries.has(id)));
    if (selected.size > 64) throw new Error(uiText('当前消息窗口超过读取预算。'));
    this.pinned = selected;
    // Move current rows to the recent end of the bounded insertion-order map.
    for (const id of selected) {
      const body = this.bodies.get(id);
      if (body) { this.bodies.delete(id); this.bodies.set(id, body); }
    }
  }

  private trim() {
    for (const [id, body] of this.bodies) {
      if (this.bodies.size <= maxCachedMessages && this.bytes <= maxCachedMessageBytes) return;
      if (this.pinned.has(id)) continue;
      this.bytes -= body.bytes;
      this.bodies.delete(id);
    }
    if (this.bytes > maxCachedMessageBytes || this.bodies.size > maxCachedMessages) throw new Error(uiText('当前消息窗口超过读取预算。'));
  }

  ingest(page: MessagePage, cursor: string, mode: 'head' | 'earlier' | 'restore') {
    if (page.leaf_message_id !== this.leaf || page.items.length > 40 ||
        (mode === 'head' && (page.items.at(-1)?.id || null) !== this.leaf) ||
        (page.has_more && (!page.next_cursor || page.next_cursor === cursor || page.next_cursor.length > 1024))) throw invalid();
    const lengths = new Map<string, number>();
    const additions: string[] = [];
    for (const n of page.items) {
      const existing = this.entries.get(n.id);
      if (n.conversation_id !== this.conversation || !n.id || n.id.length > 80 ||
          !['user', 'assistant'].includes(n.role) || typeof n.status !== 'string' || n.status.length > 32 ||
          (n.run_id !== null && (typeof n.run_id !== 'string' || n.run_id.length > 80)) ||
          (n.parent_id !== null && (typeof n.parent_id !== 'string' || n.parent_id.length > 80)) ||
          typeof n.content !== 'string' || !Number.isSafeInteger(n.seq) || n.seq < 0 || lengths.has(n.id) ||
          (existing && (existing.role !== n.role || existing.parent !== n.parent_id || existing.runId !== n.run_id))) throw invalid();
      const bytes = encoder.encode(n.content).length;
      if (bytes > (128 << 10)) throw new Error(uiText('消息正文超过单条读取预算。'));
      lengths.set(n.id, bytes);
      if (!this.entries.has(n.id) && mode !== 'restore') additions.push(n.id);
    }
    // Format 036 also serves previously created long branches with new writes
    // disabled. Reading uses the format's supported depth in both modes.
    if (this.ids.length + additions.length > 16384) throw new Error(uiText('当前分支超过消息索引上限。'));
    if (additions.length) {
      // Both the initial page and any newly exposed ancestors of a fixed head
      // appear in ascending order before the already indexed suffix.
      this.ids = [...additions, ...this.ids];
    }
    for (const incoming of page.items) {
      const id = incoming.id;
      const entry = this.entries.get(id);
      if (!entry && mode === 'restore') continue;
      const previous = this.bodies.get(id);
      if (entry && (incoming.seq < entry.seq || (terminal(entry.status) && !terminal(incoming.status)))) {
        this.recordDecision(id, entry.runId, incoming.seq, incoming.status, incoming.moderation);
        continue;
      }
      const floor = entry && { ...incoming, seq:entry.seq, status:entry.status, moderation:entry.moderation, content:'' };
      const old = previous?.node || floor;
      const [node] = mergeMessages(old ? [old] : [], [incoming], this.conversation);
      this.entries.set(id, {cursor:entry?.cursor ?? cursor, seq:node.seq, status:node.status, moderation:decisionFloor(node.moderation),
        role:node.role, parent:node.parent_id, runId:node.run_id});
      const bytes = node.content === incoming.content ? lengths.get(id)! : encoder.encode(node.content).length;
      // The platform's largest released Run is 128 KiB; a user message is
      // bounded by the catalog's smaller prompt limit. 64 pins fit in 8 MiB.
      this.bytes += bytes - (previous?.bytes || 0);
      this.bodies.delete(id);
      this.bodies.set(id, {node, bytes});
      this.trim();
    }
  }

  // A newer Run decision can reach the workspace stream before the next
  // history page. Keep its version/decision after the body and Run leave RAM.
  recordDecision(id: string, runId: string | null, seq: number, status: string, moderation?: ModerationSummary) {
    const entry = this.entries.get(id);
    if (!entry || entry.runId !== runId) return;
    const merged = mergeModeration(entry.moderation, moderation);
    if (seq >= entry.seq && !(terminal(entry.status) && !terminal(status))) {
      entry.seq = seq;
      entry.status = status;
    }
    entry.moderation = decisionFloor(merged);
    const body = this.bodies.get(id);
    if (body && !moderationAllowsText(merged, entry.status)) {
      this.bytes -= body.bytes;
      this.bodies.set(id, {node:{...body.node, seq:entry.seq, status:entry.status, moderation:merged, content:''}, bytes:0});
    }
  }

  async restore(ids: readonly string[], read: (cursor: string) => Promise<MessagePage>, current: () => boolean) {
    const wanted = ids.filter(id => this.entries.has(id) && !this.bodies.has(id));
    const nextCursor = new Map<string, string | undefined>();
    for (const id of wanted) {
      let cursor = this.cursor(id);
      // The server may return fewer rows after the byte budget changes. Follow
      // only returned cursors, scoped to the same leaf, with a per-demand cap.
      const visited = new Set<string>();
      for (let depth = 0; !this.hasBody(id) && cursor !== undefined && !visited.has(cursor) && depth < 40; depth++) {
        if (!current()) return;
        visited.add(cursor);
        if (nextCursor.has(cursor)) { cursor = nextCursor.get(cursor); continue; }
        if (nextCursor.size >= 64) throw invalid();
        const page = await read(cursor);
        if (!current()) return;
        this.ingest(page, cursor, 'restore');
        if (!this.hasBody(id) && page.items.some(item => item.id === id)) {
          throw new Error(uiText('服务端消息页尚未包含已观察的回复版本，请重新核对。'));
        }
        const next = page.has_more ? page.next_cursor : undefined;
        nextCursor.set(cursor, next);
        cursor = next;
      }
    }
    if (current() && wanted.some(id => !this.hasBody(id))) throw invalid();
  }
}

