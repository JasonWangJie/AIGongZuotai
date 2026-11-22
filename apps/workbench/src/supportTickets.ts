import { uiText } from './locale';
import { request } from './api';

export const categories = { get account() { return uiText('账户'); }, get billing() { return uiText('权益与账务'); }, get generation() { return uiText('创作生成'); }, get other() { return uiText('其他'); } };
export const kinds = { get created() { return uiText('用户建单'); }, get followup() { return uiText('用户追问'); }, get reply() { return uiText('客服回复'); }, get closed() { return uiText('用户关闭'); } };
export const operations = { get create() { return uiText('创建'); }, get followup() { return uiText('追问'); }, get reply() { return uiText('回复'); }, get close() { return uiText('关闭'); } };
export type Category = keyof typeof categories;
export type Op = keyof typeof operations;
export type Card = {
  id: string;
  workspace_id: string;
  category: Category;
  subject: string;
  state: 'OPEN' | 'CLOSED';
  version: number;
  created_at: string;
  updated_at: string;
};
export type Ticket = Card & {
  messages: { version: number; kind: keyof typeof kinds; body: string; created_at: string }[];
};
export type Actor = { user: string; workspace: string; staff: boolean };
export type Original = { op: Op; key: string; expected: number; id?: string };
type Payload = { category?: Category; subject?: string; body?: string; password?: string };
export function validText(value: unknown, subject = false): value is string {
  if (
    typeof value !== 'string' ||
    !value.trim() ||
    Array.from(value).length > (subject ? 120 : 2000) ||
    new TextEncoder().encode(value).length > (subject ? 480 : 6000)
  )
    return false;
  return !/\p{Cc}/u.test(subject ? value : value.replace(/[\n\r\t]/g, '')) && !/\p{Cs}/u.test(value);
}
const date = (v: unknown) => typeof v === 'string' && Number.isFinite(Date.parse(v));
const ticketID = (v: unknown) => typeof v === 'string' && /^tkt_[a-f0-9]{32}$/.test(v);
const fields = ['id', 'workspace_id', 'category', 'subject', 'state', 'version', 'created_at', 'updated_at'];
export function cardOK(c: Card, w: string) {
  return (
    !!c &&
    ticketID(c.id) &&
    c.workspace_id === w &&
    Object.hasOwn(categories, c.category) &&
    validText(c.subject, true) &&
    ['OPEN', 'CLOSED'].includes(c.state) &&
    Number.isSafeInteger(c.version) &&
    c.version >= 1 &&
    c.version <= 32 &&
    date(c.created_at) &&
    date(c.updated_at)
  );
}
export function checkedPage(page: { items: Card[]; next_cursor: string | null }, w: string) {
  if (
    !page ||
    !Array.isArray(page.items) ||
    page.items.length > 20 ||
    !page.items.every((c) => cardOK(c, w) && Object.keys(c).every((k) => fields.includes(k))) ||
    new Set(page.items.map((c) => c.id)).size !== page.items.length ||
    (page.next_cursor !== null && typeof page.next_cursor !== 'string')
  )
    throw Error(uiText('工单列表范围或格式不符。'));
  return page;
}
export function checkedTicket(t: Ticket, w: string, id?: string) {
  if (
    !cardOK(t, w) ||
    (id && id !== t.id) ||
    !Array.isArray(t.messages) ||
    t.messages.length !== t.version ||
    !t.messages.every(
      (m, i) =>
        m &&
        m.version === i + 1 &&
        Object.hasOwn(kinds, m.kind) &&
        date(m.created_at) &&
        (m.kind === 'closed'
          ? m.body === '' && i === t.version - 1 && t.state === 'CLOSED'
          : validText(m.body)) &&
        (i === 0 ? m.kind === 'created' : m.kind !== 'created'),
    ) ||
    (t.state === 'CLOSED' && t.messages.at(-1)?.kind !== 'closed')
  )
    throw Error(uiText('工单详情与当前范围或版本不符。'));
  return t;
}
const lastKind: Record<Op, keyof typeof kinds> = {
  create: 'created',
  followup: 'followup',
  reply: 'reply',
  close: 'closed',
};
export function checkedReceipt(t: Ticket, a: Actor, i: Original) {
  checkedTicket(t, a.workspace, i.id);
  if (t.version !== i.expected + 1 || t.messages.at(-1)?.kind !== lastKind[i.op])
    throw Error(uiText('工单原操作收据与预期版本不符。'));
  return t;
}
const enc = encodeURIComponent;
const base = (a: Actor) => (a.staff ? '/operations/support/tickets' : '/support/tickets');
function query(a: Actor, params: Record<string, string> = {}) {
  const p = new URLSearchParams(params);
  if (a.staff) p.set('workspace_id', a.workspace);
  return p.size ? '?' + p.toString() : '';
}
export const supportApi = {
  list: (a: Actor, cursor?: string) =>
    request<{ items: Card[]; next_cursor: string | null }>(base(a) + query(a, cursor ? { cursor } : {})),
  detail: (a: Actor, id: string) => request<Ticket>(base(a) + '/' + enc(id) + query(a)),
  receipt: (a: Actor, i: Original) =>
    request<Ticket>(base(a) + '/receipt' + query(a, { operation: i.op }), {
      headers: { 'Idempotency-Key': i.key },
    }),
  submit: (a: Actor, i: Original, form: Payload) => {
    if (a.staff !== (i.op === 'reply') || (i.op !== 'create' && !ticketID(i.id)))
      throw Error(uiText('工单操作范围不符。'));
    let body: object;
    switch (i.op) {
      case 'create':
        body = { category: form.category, subject: form.subject, body: form.body };
        break;
      case 'close':
        body = { expected_version: i.expected };
        break;
      case 'reply':
        body = {
          workspace_id: a.workspace,
          expected_version: i.expected,
          body: form.body,
          password: form.password,
        };
        break;
      default:
        body = { expected_version: i.expected, body: form.body };
    }
    const path = i.op === 'create' ? base(a) : base(a) + '/' + enc(i.id!) + '/' + i.op;
    return request<Ticket>(path, {
      method: 'POST',
      headers: { 'Idempotency-Key': i.key },
      body: JSON.stringify(body),
    });
  },
};
export const originalSlot = (a: Actor) =>
  'xingtu-support-action:' + JSON.stringify([a.user, a.workspace, a.staff]);
export function readOriginal(a: Actor): Original | null {
  const raw = sessionStorage.getItem(originalSlot(a));
  if (raw === null) return null;
  const i = JSON.parse(raw) as Original;
  if (
    !i ||
    !Object.hasOwn(operations, i.op) ||
    a.staff !== (i.op === 'reply') ||
    !/^[-\w]{12,100}$/.test(i.key) ||
    !Number.isSafeInteger(i.expected) ||
    (i.op === 'create'
      ? i.expected !== 0 || i.id !== undefined
      : !ticketID(i.id) || i.expected < 1 || i.expected > 31)
  )
    throw Error(uiText('本页原工单操作编号无法读取。'));
  return i;
}
export function keepOriginal(a: Actor, i: Original) {
  if (readOriginal(a)) throw Error(uiText('请先核对原工单收据。'));
  sessionStorage.setItem(originalSlot(a), JSON.stringify(i));
}
export function forgetOriginal(a: Actor, i: Original) {
  if (readOriginal(a)?.key === i.key) sessionStorage.removeItem(originalSlot(a));
}
