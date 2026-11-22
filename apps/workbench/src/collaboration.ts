import { uiText } from './locale';
import { request } from './api';
import { draftNodesValid } from './draftState';
import type { CloudDraft, DraftWriteInput } from './types';
export type Role = 'viewer' | 'editor';
export type MemberState = 'PENDING' | 'ACTIVE' | 'WITHDRAWN' | 'REVOKED' | 'EXPIRED';
export type Member = {
  project_id: string;
  project_name: string;
  target_user_id: string;
  inviter_name: string;
  invitation_id: string;
  role: Role;
  state: MemberState;
  round: number;
  version: number;
  expires_at?: string;
};
export type Operation = 'invite' | 'accept' | 'withdraw' | 'revoke' | 'downgrade';
export type Receipt = Member & { operation: Operation };
export type RelationTicket = {
  operation: Operation;
  project: string;
  target: string;
  expected: number;
  id?: string;
  key: string;
};
export type DraftTicket = { expected: number; key: string };
const enc = encodeURIComponent,
  shared = '/collaboration',
  own = (p: string) => '/projects/' + enc(p) + '/collaboration',
  draft = (p: string) => shared + '/projects/' + enc(p) + '/draft';
const key = (k: string) => ({ 'Idempotency-Key': k });
export const collabApi = {
  projects: () => request<{ items: Member[] }>(shared + '/shared-projects'),
  invitations: () => request<{ items: Member[] }>(shared + '/invitations'),
  members: (p: string, target?: string) =>
    request<{ items: Member[] }>(own(p) + '/members' + (target ? '?target_user_id=' + enc(target) : '')),
  mutate: (t: RelationTicket, role?: Role) => {
    let path: string, body: object;
    if (t.operation === 'invite') {
      path = own(t.project) + '/invitations';
      body = { target_user_id: t.target, role, expected_version: t.expected };
    } else if (t.operation === 'accept') {
      path = shared + '/invitations/' + enc(t.id!) + '/accept';
      body = { expected_version: t.expected };
    } else {
      path = own(t.project) + '/members/' + enc(t.target) + '/' + t.operation;
      body = { invitation_id: t.id, expected_version: t.expected };
    }
    return request<Receipt>(path, { method: 'POST', headers: key(t.key), body: JSON.stringify(body) });
  },
  receipt: (t: RelationTicket) =>
    request<Receipt>(shared + '/receipt?operation=' + t.operation, { headers: key(t.key) }),
  draft: (p: string) => request<CloudDraft>(draft(p)),
  history: (p: string, before?: number) =>
    request<{
      items: { revision: number; node_count: number; updated_at: string }[];
      has_more: boolean;
      next_before_revision?: number;
    }>(draft(p) + '/revisions?limit=20' + (before ? '&before_revision=' + before : '')),
  revision: (p: string, n: number) => request<CloudDraft>(draft(p) + '/revisions/' + n),
  save: (p: string, body: DraftWriteInput, k: string) =>
    request<CloudDraft>(draft(p), { method: 'PUT', headers: key(k), body: JSON.stringify(body) }),
  saved: (p: string, k: string) => request<CloudDraft>(draft(p) + '/receipt', { headers: key(k) }),
};
export function validMember(m: Member) {
  return (
    !!m &&
    ['project_id', 'project_name', 'target_user_id', 'inviter_name', 'invitation_id'].every(
      (k) => typeof m[k as keyof Member] === 'string' && !!m[k as keyof Member],
    ) &&
    ['viewer', 'editor'].includes(m.role) &&
    ['PENDING', 'ACTIVE', 'WITHDRAWN', 'REVOKED', 'EXPIRED'].includes(m.state) &&
    Number.isSafeInteger(m.round) &&
    m.round > 0 &&
    Number.isSafeInteger(m.version) &&
    m.version > 0
  );
}
export function checkReceipt(r: Receipt, t: RelationTicket) {
  if (
    !validMember(r) ||
    r.operation !== t.operation ||
    r.project_id !== t.project ||
    r.target_user_id !== t.target ||
    r.version !== t.expected + 1 ||
    (t.id && t.id !== r.invitation_id)
  )
    throw Error(uiText('回执与原项目、成员或邀请轮次不符。'));
  return r;
}
export function checkDraft(d: CloudDraft, p: string, n?: number) {
  if (
    !d ||
    d.project_id !== p ||
    !Number.isSafeInteger(d.revision) ||
    d.revision < 0 ||
    (n !== undefined && d.revision !== n) ||
    !draftNodesValid(d.nodes)
  )
    throw Error(uiText('草稿与原项目或版本不符。'));
  return d;
}
export const roleLabel = (r: Role | 'owner') => ({ owner: uiText('所有者'), editor: uiText('可编辑'), viewer: uiText('只读') })[r];
export const stateLabel = (s: MemberState) =>
  ({
    PENDING: uiText('待接受'),
    ACTIVE: uiText('已加入'),
    WITHDRAWN: uiText('邀请已撤回'),
    REVOKED: uiText('权限已收回'),
    EXPIRED: uiText('邀请已到期'),
  })[s];
export const opLabel = (op: Operation) =>
  ({ invite: uiText('邀请'), accept: uiText('接受邀请'), withdraw: uiText('撤回邀请'), downgrade: uiText('降为只读'), revoke: uiText('收回权限') })[
    op
  ];
export const relationSlot = (u: string) => 'xingtu-member-operation:' + JSON.stringify(u);
export const draftSlot = (u: string, p: string, round: string) =>
  'xingtu-shared-draft-operation:' + JSON.stringify([u, p, round]);
export function readTicket<T extends DraftTicket | RelationTicket>(slot: string): T | null {
  const raw = sessionStorage.getItem(slot);
  if (!raw) return null;
  const t = JSON.parse(raw);
  if (
    !t ||
    typeof t.key !== 'string' ||
    !/^[-\w]{12,100}$/.test(t.key) ||
    !Number.isSafeInteger(t.expected) ||
    t.expected < 0
  )
    throw Error(uiText('原操作编号无法读取。'));
  if (
    slot.startsWith('xingtu-member-') &&
    (!['invite', 'accept', 'withdraw', 'downgrade', 'revoke'].includes(t.operation) ||
      !t.project ||
      !t.target ||
      (t.operation !== 'invite' && !t.id))
  )
    throw Error(uiText('邀请操作范围不完整。'));
  return t as T;
}
