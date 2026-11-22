import { uiText } from './locale';
import { request } from './api';
import type { AppealReason } from './generationControls';
export type AccountTarget = { target_user_id: string; version: number };
export type AccountAccess = {
  target_user_id: string;
  state: 'OPEN' | 'SUSPENDED';
  version: number;
  restriction?: { id: string; reason_code: string; public_notice: string; created_at: string };
  can_appeal: boolean;
};
export type AccountAppeal = {
  id: string;
  target_user_id: string;
  restriction_id: string;
  state: 'OPEN' | 'RESOLVED';
  outcome?: 'UPHELD' | 'LIFTED';
  version: number;
  reason_code: AppealReason;
  statement?: string;
  public_reply?: string;
  created_at: string;
  updated_at: string;
};
export const accountActions = {
  get suspend() { return uiText('限制目标账户'); },
  get uphold() { return uiText('维持本轮限制并答复'); },
  get lift() { return uiText('解除本轮账户限制'); },
};
export type GovernanceAction = keyof typeof accountActions;
export type AccountPlanInput = {
  action: GovernanceAction;
  expected_version: number;
  reason_code: string;
  evidence_ref: string;
  public_reply: string;
  restriction_id?: string;
  appeal_id?: string;
  expected_appeal_version?: number;
};
export type AccountEffect = {
  from_state: AccountAccess['state'];
  to_state: AccountAccess['state'];
  version: number;
  restriction_id: string;
  affected_appeals: number;
};
export type AccountPlan = {
  id: string;
  target_user_id: string;
  action: GovernanceAction;
  expected_version: number;
  restriction_id?: string;
  appeal_id?: string;
  plan_digest: string;
  expires_at: string;
  effect: AccountEffect;
};
export type AccountReceipt = {
  id: string;
  target_user_id: string;
  plan_id: string;
  action: GovernanceAction;
  effect: AccountEffect;
  executed_at: string;
};
export const governanceReasons: Record<string, string> = {
  get manual_review() { return uiText('需要人工核实'); },
  get policy_concern() { return uiText('使用规则需核实'); },
  get abuse_concern() { return uiText('异常使用需核实'); },
  get review_complete() { return uiText('复核完成'); },
  get restriction_error() { return uiText('原限制有误'); },
};
type Page = { items: AccountAppeal[]; next_cursor?: string | null };
const root = '/operations/account-targets',
  personal = '/account/appeals',
  enc = encodeURIComponent;
const targetURL = (id: string) => root + '/' + enc(id);
const pageURL = (after?: string | null) => '?limit=20' + (after ? '&cursor=' + enc(after) : '');
const withKey = (key: string) => ({ 'Idempotency-Key': key });
const post = (body: unknown, key?: string): RequestInit => ({
  method: 'POST',
  headers: key ? withKey(key) : {},
  body: JSON.stringify(body),
});
export const governanceApi = {
  targets: () => request<{ items: AccountTarget[] }>(root),
  access: (target?: string) => request<AccountAccess>(target ? targetURL(target) : '/account/access'),
  history: (target?: string, after?: string | null) =>
    request<Page>((target ? targetURL(target) + '/appeals' : personal) + pageURL(after)),
  detail: (id: string, target?: string) =>
    request<AccountAppeal>((target ? targetURL(target) + '/appeals' : personal) + '/' + enc(id)),
  apply: (round: string, reason: AppealReason, statement: string, key: string) =>
    request<AccountAppeal>(personal, post({ restriction_id: round, reason_code: reason, statement }, key)),
  receipt: (key: string) => request<AccountAppeal>(personal + '/receipt', { headers: withKey(key) }),
  plan: (target: string, input: AccountPlanInput) =>
    request<AccountPlan>(targetURL(target) + '/plans', post(input)),
  execute: (target: string, plan: AccountPlan, password: string) =>
    request<AccountReceipt>(
      targetURL(target) + '/execute',
      post({ plan_id: plan.id, plan_digest: plan.plan_digest, password }),
    ),
  planReceipt: (target: string, id: string) =>
    request<AccountReceipt>(targetURL(target) + '/plans/' + enc(id) + '/receipt'),
};
export function matchesAccess(a: AccountAccess, target: string) {
  return (
    a?.target_user_id === target &&
    Number.isSafeInteger(a.version) &&
    a.version >= 0 &&
    (a.state === 'OPEN' || (a.state === 'SUSPENDED' && !!a.restriction?.id))
  );
}
export function matchesAccountAppeal(a: AccountAppeal, target: string, id?: string) {
  return (
    a?.target_user_id === target &&
    !!a.id &&
    !!a.restriction_id &&
    (!id || a.id === id) &&
    Number.isSafeInteger(a.version) &&
    a.version > 0 &&
    (a.state === 'OPEN' || (a.state === 'RESOLVED' && ['UPHELD', 'LIFTED'].includes(a.outcome || '')))
  );
}
