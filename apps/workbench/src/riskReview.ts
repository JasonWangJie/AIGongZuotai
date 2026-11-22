import { uiText } from './locale';
import { request } from './api';
import { safeID } from './contentAppeals';
export type RiskReason = 'HELD_LIMIT' | 'RATE_LIMIT';
export const riskNames: Record<RiskReason, string> = {
  get HELD_LIMIT() { return uiText('工作区冻结额度上限'); },
  get RATE_LIMIT() { return uiText('工作区每分钟生成频率上限'); },
};
export const appealReasons = {
  get request_review() { return uiText('申请人工复核'); },
  get limit_error() { return uiText('限制可能有误'); },
  get billing_concern() { return uiText('冻结额度需核实'); },
};
export type RiskEvidence = {
  quote_id: string;
  workspace_id: string;
  project_id: string;
  amount_micros: string;
  expires_at: string;
  reason_codes: RiskReason[];
  policy_version: number;
  allowed_to_appeal: boolean;
  appeal_id?: string;
};
export type RiskAppeal = Omit<RiskEvidence, 'allowed_to_appeal'> & {
  id: string;
  state: 'OPEN' | 'APPROVED' | 'DENIED';
  version: number;
  reason_code: keyof typeof appealReasons;
  statement?: string;
  public_reply?: string;
  exemption_state?: 'AVAILABLE' | 'EXPIRED' | 'CONSUMED' | 'INACTIVE';
};
export type RiskAction = 'approve' | 'deny';
export type RiskPlan = {
  id: string;
  workspace_id: string;
  appeal_id: string;
  action: RiskAction;
  outcome: 'APPROVED' | 'DENIED';
  expected_version: number;
  expires_at: string;
  plan_digest: string;
};
export type RiskReceipt = {
  id: string;
  workspace_id: string;
  appeal_id: string;
  action: RiskAction;
  outcome: 'APPROVED' | 'DENIED';
  version: number;
  plan_id: string;
};
type Page = { items: RiskAppeal[]; next_cursor?: string | null };
const enc = encodeURIComponent,
  user = '/risk/appeals',
  ops = '/operations/risk-appeals';
const post = (body: unknown, headers: Record<string, string> = {}): RequestInit => ({
  method: 'POST',
  body: JSON.stringify(body),
  headers,
});
const scope = (project: string, key?: string) => ({
  'X-Project-ID': project,
  ...(key ? { 'Idempotency-Key': key } : {}),
});
const query = (mode: 'user' | 'ops', id: string) =>
  mode === 'user' ? '?project_id=' + enc(id) : '?workspace_id=' + enc(id);
export const riskApi = {
  eligibility: (p: string, q: string) =>
    request<RiskEvidence>('/risk/eligibility' + query('user', p) + '&quote_id=' + enc(q), {
      headers: scope(p),
    }),
  list: (mode: 'user' | 'ops', id: string, cursor?: string | null) =>
    request<Page>(
      (mode === 'user' ? user : ops) +
        query(mode, id) +
        '&limit=20' +
        (cursor ? '&cursor=' + enc(cursor) : ''),
      { headers: mode === 'user' ? scope(id) : {} },
    ),
  detail: (mode: 'user' | 'ops', scopeID: string, id: string) =>
    request<RiskAppeal>((mode === 'user' ? user : ops) + '/' + enc(id) + query(mode, scopeID), {
      headers: mode === 'user' ? scope(scopeID) : {},
    }),
  apply: (p: string, q: string, reason: keyof typeof appealReasons, statement: string, key: string) =>
    request<RiskAppeal>(
      user,
      post({ project_id: p, quote_id: q, reason_code: reason, statement }, scope(p, key)),
    ),
  receipt: (p: string, key: string) =>
    request<RiskAppeal>(user + '/receipt' + query('user', p), { headers: scope(p, key) }),
  preview: (w: string, id: string, version: number, action: RiskAction, evidence: string, reply: string) =>
    request<RiskPlan>(
      ops + '/' + enc(id) + '/plans',
      post({
        workspace_id: w,
        expected_version: version,
        action,
        evidence_ref: evidence,
        public_reply: reply,
      }),
    ),
  execute: (p: RiskPlan, password: string) =>
    request<RiskReceipt>(
      ops + '/' + enc(p.appeal_id) + '/execute',
      post({ workspace_id: p.workspace_id, plan_id: p.id, plan_digest: p.plan_digest, password }),
    ),
  opsReceipt: (w: string, id: string, plan: string) =>
    request<RiskReceipt>(ops + '/' + enc(id) + '/plans/' + enc(plan) + '/receipt' + query('ops', w)),
};
export function evidenceMatches(v: RiskEvidence | RiskAppeal, w: string, p?: string, q?: string) {
  return (
    !!v &&
    v.workspace_id === w &&
    (!p || v.project_id === p) &&
    (!q || v.quote_id === q) &&
    safeID(v.quote_id) &&
    safeID(v.project_id) &&
    typeof v.amount_micros === 'string' &&
    /^[0-9]{1,30}$/.test(v.amount_micros) &&
    Number.isSafeInteger(v.policy_version) &&
    Array.isArray(v.reason_codes) &&
    v.reason_codes.every((k) => Object.hasOwn(riskNames, k)) &&
    Number.isFinite(Date.parse(v.expires_at))
  );
}
