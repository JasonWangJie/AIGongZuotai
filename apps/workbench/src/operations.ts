import { ApiError } from './api';
import type { BillingSummary } from './billing';

export type OperationPermission = 'read' | 'investigate' | 'finance' | 'case_open' | 'generation_restrict' | 'appeal_resolve';
export type OperationAction = 'reconcile' | 'release' | 'charge' | 'no_action';
export type OperationReason = 'provider_unknown' | 'delivery_issue' | 'usage_missing' | 'billing_review' | 'manual_review';
export type OperationAccess = { enabled: boolean; access: { workspace_id: string; role: string; permissions: string[]; version: number }[]; reason?: string };
export type OperationScope = { id: string; roles: string[]; permissions: OperationPermission[] };
export type OperationException = { id: string; resource_type: 'run' | 'task'; workspace_id: string; project_id: string; model_id: string; provider_mode: 'demo' | 'live'; status: string; resource_version: number; billing?: BillingSummary; can_reconcile: boolean; updated_at: string; open_case_id?: string };
export type OperationReceipt = { id: string; plan_id: string; action: OperationAction; case_version: number; resource_version: number; billing_version: number; executed_at: string };
export type OperationCase = { id: string; workspace_id: string; project_id: string; resource_type: 'run' | 'task'; resource_id: string; resource_version: number; state: 'OPEN' | 'RESOLVED'; version: number; reason: OperationReason; evidence_ref: string; created_at: string; updated_at: string; receipt: OperationReceipt | null; resource?: OperationException };
export type OperationPage<T> = { items: T[]; next_cursor: string | null };
export type OperationUsage = { status: 'reported' | 'demo'; source: 'gateway' | 'demo'; complete: true; prompt_tokens: number; completion_tokens: number; total_tokens: number };
export type OperationCaseInput = { workspace_id: string; resource_type: 'run' | 'task'; resource_id: string; resource_version: number; reason: OperationReason; evidence_ref: string };
export type OperationPlanInput = { workspace_id: string; expected_version: number; action: OperationAction; reason: OperationReason; evidence_ref: string; usage?: OperationUsage; delivered_units?: number };
export type OperationPlan = { id: string; case_id: string; case_version: number; resource_version: number; billing_version: number; action: OperationAction; plan_digest: string; expires_at: string; effect: { charged_micros: string; released_micros: string; unit: 'CREDIT'; scale: 6 } };
export type OperationExecuteInput = { workspace_id: string; expected_version: number; plan_id: string; plan_digest: string; password: string };
export type OperationEvent = { id: string; action: string; resource_type: string; resource_id: string; operation_id: string; reason_code: string; evidence_ref: string; version: number; created_at: string };
export function operationEventName(action: string) { return ({ 'case.created': '建立核实工单', 'case.previewed': '生成处置预演', 'case.executed': '提交处置结果' } as Record<string, string>)[action] || '运营核实记录'; }
export const operationReasons: Record<OperationReason, string> = { provider_unknown: '上游状态不明', delivery_issue: '交付结果异常', usage_missing: '计量缺失或矛盾', billing_review: '冻结额度核实', manual_review: '人工复核' };
export const operationActions: Record<OperationAction, string> = { reconcile: '核对原任务', release: '释放冻结额度', charge: '按证据结算', no_action: '仅关闭工单' };
export const operationRoles: Record<string, string> = { observer: '只读观察', operator: '任务核对', finance: '财务核实', moderator: '生成权限复核' };
const permissions: OperationPermission[] = ['read', 'investigate', 'finance', 'case_open', 'generation_restrict', 'appeal_resolve'];
const safeID = (value: unknown): value is string => typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/.test(value);

export function operationScopes(value: OperationAccess | null): OperationScope[] {
  if (!value?.enabled || !Array.isArray(value.access) || value.access.length > 128) return [];
  const result = new Map<string, OperationScope>();
  for (const item of value.access) {
    if (!safeID(item?.workspace_id) || !Object.hasOwn(operationRoles, item.role) || !Array.isArray(item.permissions) || !Number.isSafeInteger(item.version) || item.version < 1) continue;
    const old = result.get(item.workspace_id) || { id: item.workspace_id, roles: [], permissions: [] };
    if (!old.roles.includes(item.role)) old.roles.push(item.role);
    for (const p of permissions) if (item.permissions.includes(p) && !old.permissions.includes(p)) old.permissions.push(p);
    result.set(item.workspace_id, old);
  }
  return [...result.values()].filter(scope => scope.permissions.includes('read'));
}
export function allowedOperation(scope: OperationScope | undefined, action: OperationAction): boolean { return !!scope?.permissions.includes(action === 'charge' || action === 'release' ? 'finance' : 'investigate'); }
export function operationEvidenceError(reason: string, evidence: string): string {
  if (!Object.hasOwn(operationReasons, reason)) return '请选择本次核实原因。';
  if (!/^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$/.test(evidence) || evidence.includes('://')) return '证据编号使用 1—128 位安全字符，例如 ticket:001；不要填写网址、正文或凭据。';
  return '';
}
export function operationUsage(mode: string, input: string, output: string, total: string): OperationUsage | null {
  if (mode !== 'live' && mode !== 'demo') return null;
  const values = [input, output, total].map(value => /^(0|[1-9][0-9]{0,9})$/.test(value) ? Number(value) : NaN);
  if (values.some(value => !Number.isSafeInteger(value) || value < 0 || value > 1_000_000_000) || values[0] + values[1] !== values[2]) return null;
  return { status: mode === 'live' ? 'reported' : 'demo', source: mode === 'live' ? 'gateway' : 'demo', complete: true, prompt_tokens: values[0], completion_tokens: values[1], total_tokens: values[2] };
}
export function operationQuery(workspace: string, filter: { kind?: string; state?: string; resource_id?: string }, cursor?: string | null) {
  const q = new URLSearchParams({ workspace_id: workspace, limit: '20' });
  for (const [key, value] of Object.entries(filter)) if (value) q.set(key, value);
  if (cursor) q.set('cursor', cursor);
  return q.toString();
}
export function operationCaseMatches(value: OperationCase, scope: string, id?: string) { return safeID(value?.id) && value.workspace_id === scope && (!id || value.id === id) && Number.isSafeInteger(value.version) && value.version > 0 && ['OPEN', 'RESOLVED'].includes(value.state); }
export function operationPlanMatches(plan: OperationPlan, value: OperationCase, action: OperationAction, now = Date.now()) {
  return safeID(plan?.id) && plan.case_id === value.id && plan.case_version === value.version && plan.action === action && /^[a-f0-9]{64}$/.test(plan.plan_digest) && Date.parse(plan.expires_at) > now && Date.parse(plan.expires_at) <= now + 6 * 60_000 && plan.effect?.unit === 'CREDIT' && plan.effect.scale === 6 && [plan.effect.charged_micros, plan.effect.released_micros].every(amount => /^(0|[1-9][0-9]{0,15})$/.test(amount) && BigInt(amount) <= 9_000_000_000_000_000n);
}
export function mergeOperationPage<T extends { id: string }>(old: T[], incoming: T[], maximum = 200): T[] { const values = new Map(old.map(item => [item.id, item])); for (const item of incoming) values.set(item.id, item); return [...values.values()].slice(0, maximum); }

// Only safe metadata survives navigation/authentication loss in this App instance.
// No password, usage evidence, token or executable financial mutation is kept here.
export type OperationUnknown = { kind: 'case_create'; resourceType: 'run' | 'task'; resourceID: string; body: OperationCaseInput; key: string } | { kind: 'execute'; caseID: string; planID: string } | { kind: 'plan'; caseID: string };
export class OperationsRecovery {
  #pending = new Map<string, { value: OperationUnknown; attempt: symbol }>();
  key(user: string, workspace: string) { return JSON.stringify([user, workspace]); }
  get(user: string, workspace: string) { return this.#pending.get(this.key(user, workspace))?.value || null; }
  attempt(user: string, workspace: string) { return this.#pending.get(this.key(user, workspace))?.attempt; }
  remember(user: string, workspace: string, value: OperationUnknown) {
    const key = this.key(user, workspace); if (!this.#pending.has(key) && this.#pending.size >= 64) throw new Error('运营待核对记录已达本页上限，请先核对已有工单。');
    const attempt = Symbol('operation-attempt');
    if (value.kind === 'case_create') { const b = value.body; if (b.workspace_id !== workspace) throw new Error('开单范围不匹配。'); this.#pending.set(key, { attempt, value: { kind: 'case_create', resourceType: value.resourceType, resourceID: value.resourceID, key: value.key, body: { workspace_id: b.workspace_id, resource_type: b.resource_type, resource_id: b.resource_id, resource_version: b.resource_version, reason: b.reason, evidence_ref: b.evidence_ref } } }); }
    else if (value.kind === 'execute') this.#pending.set(key, { attempt, value: { kind: 'execute', caseID: value.caseID, planID: value.planID } });
    else this.#pending.set(key, { attempt, value: { kind: 'plan', caseID: value.caseID } });
    return attempt;
  }
  clearIfMatches(user: string, workspace: string, attempt: symbol | undefined) { const key = this.key(user, workspace); if (!attempt || this.#pending.get(key)?.attempt !== attempt) return false; this.#pending.delete(key); return true; }
  clear(user: string, workspace: string) { this.#pending.delete(this.key(user, workspace)); }
  has() { return this.#pending.size > 0; }
  hasFor(user: string) { return [...this.#pending.keys()].some(key => (JSON.parse(key) as string[])[0] === user); }
}
export class OperationPassword {
  #value = '';
  set(value: string) { this.#value = value; }
  clear() { this.#value = ''; }
  take() { const value = this.#value; this.clear(); return value; }
}
export function safeOperationError(cause: unknown): ApiError {
  const error = cause instanceof ApiError ? cause : new ApiError('运营请求未确认。', 0, true);
  const messages: Record<string, string> = { OPERATIONS_PASSWORD_INVALID: '当前密码不正确，请重新输入。', INVALID_OPERATIONS_PASSWORD: '当前密码需要 10—72 字节有效文本。', OPERATIONS_REAUTH_REQUIRED: '账户凭据已变化，请重新核对身份后预演。', OPERATIONS_NOT_FOUND: '当前工作区、工单不存在或已不在授权范围。', OPERATIONS_CASE_EXISTS: '该资源已有待处置工单，请刷新队列后打开原单。', OPERATIONS_CASE_CHANGED: '工单已有新版本，请刷新后重新预演。', OPERATIONS_CASE_RESOLVED: '工单已结束，请刷新查看已提交收据。', OPERATIONS_PLAN_EXPIRED: '预演已过期，请核对最新工单后重新预演。', OPERATIONS_PLAN_CHANGED: '资源、凭据、授权或预演内容已变化，请重新读取后预演。', OPERATIONS_PLAN_CONSUMED: '该预演已经执行，请刷新原工单核对收据。', OPERATIONS_PLAN_LIMIT: '有效预演已达上限，请等待旧计划过期后再试。', OPERATIONS_CASE_LIMIT: '工单容量已达上限，请联系运营管理员。', OPERATIONS_RATE_LIMIT: '密码核验过于频繁，请等待冷却后再试。', OPERATIONS_REQUEST_CANCELLED: '本次确认请求已取消，请核对工单后重新预演。', AUTH_REQUIRED: '登录状态已失效，请重新登录后核对工单。', INVALID_CREDENTIALS: '当前密码不正确，请重新输入。', UNAUTHORIZED: '登录状态已失效，请重新登录后核对工单。', OPS_FORBIDDEN: '当前账户没有此工作区的运营权限。', OPERATION_FORBIDDEN: '当前角色没有此处置权限。', OPS_DISABLED: '此环境未开启运营核实台。', OPERATIONS_DISABLED: '此环境未开启运营核实台。', CASE_VERSION_CONFLICT: '工单已有新版本，请刷新后重新预演。', PLAN_EXPIRED: '预演已过期，请核对最新工单后重新预演。', PLAN_STALE: '资源状态已变化，请刷新后重新预演。', PLAN_DIGEST_MISMATCH: '预演摘要不匹配，不能执行本次操作。', PLAN_ALREADY_EXECUTED: '该预演已经执行，请刷新工单查看回执。', IDEMPOTENCY_CONFLICT: '原操作编号已用于其他请求，请核对工单记录。', LEDGER_EXECUTION_ACTIVE: '任务仍在执行或自动查询，不能进行财务核销。', LEDGER_REVIEW_REQUIRED: '该冻结单已不处于待核实状态，请刷新工单。' };
  const known = error.code && Object.hasOwn(messages, error.code) ? error.code : undefined;
  const message = known ? messages[known] : error.uncertain ? '响应未能确认。请只刷新工单核对，不要再次提交处置。' : error.status === 403 ? '当前角色不能执行此操作，请核对工作区权限。' : error.status === 401 ? '登录状态需要核对，请重新登录后查看工单。' : error.status === 429 ? '操作过于频繁或已达容量限制，请稍后核对。' : error.status === 409 ? '工单、预演或资源版本已变化，请刷新核对。' : error.status === 422 ? '本次证据或操作不符合服务端规则，请核对后重新预演。' : '运营请求未完成，请刷新查看当前状态。';
  return new ApiError(message, error.status, error.uncertain, known, error.requestId, error.retryAt);
}
