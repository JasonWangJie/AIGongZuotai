import { serverErrorText } from './serverDisplay';
import { uiText } from './locale';
import { readMessagePageText } from './readMessagePageText';
import { maxMessagePageBytes } from './messageBodyCache';
import { captureRiskInput, rememberRisk, riskChangedInput, riskCodes } from './riskOriginal';
import type { BillingQuote, QuoteRequest, LedgerWallet, LedgerEntry, LedgerReservation, LedgerPage } from './billing';
import type { AuditPage, AuthSession, Bootstrap, CloudDraft, DraftRevisionPage, DraftWriteInput, HistoryPage, Project, Run, Task, TaskInput, WorkspaceRuntime, ModelCatalog, ReferenceAsset } from './types';
import type { CatalogEntry } from './modelCatalog';
import type { AssetEntry, AssetOperation, AssetRevisionPage, LibraryFilters, LibraryPageData } from './libraryState';
import { libraryQuery } from './libraryState';
import { ACCOUNT_RETRY_SECONDS, MAX_RETRY_SECONDS, retryDeadline, safeRequestId, definiteExecutionRejection, executionErrorMessage } from './requestFeedback';
import type { Conversation, ConversationPage, MessagePage, BranchPage, TurnInput } from './conversationState';
import type { AccountResult, LoginSession, SecurityEventPage } from './accountSecurity';
import type { OperationAccess, OperationCase, OperationCaseInput, OperationEvent, OperationException, OperationExecuteInput, OperationPage, OperationPlan, OperationPlanInput } from './operations';
import { moderationErrorMessage } from './moderation';
import { definiteModerationUnavailable, mediaModerationErrorMessage } from './mediaModeration';
import { generationErrorMessage } from './generationControls';
import type { GenerationAccess, GenerationAppeal, GenerationPage, AppealInput, GenerationPlanInput, GenerationPlan, GenerationExecuteInput, GenerationReceipt } from './generationControls';

type ErrorDetail = string | { ui: string } | { server?: string };

function errorCodeText(code?: string) {
  return executionErrorMessage(code) || generationErrorMessage(code) || mediaModerationErrorMessage(code) || moderationErrorMessage(code);
}

function formatErrorDetail(detail: ErrorDetail, status: number, code?: string) {
  if (typeof detail === 'string') return detail;
  if ('ui' in detail) return uiText(detail.ui);
  return errorCodeText(code) || serverErrorText(code, detail.server) || uiText('请求失败（{0}）', status);
}

export class ApiError extends Error {
  readonly #detail: ErrorDetail;
  get displayMessage() { return formatErrorDetail(this.#detail, this.status, this.code); }
  status: number;
  uncertain: boolean;
  code?: string;
  requestId?: string;
  retryAt: number;
  constructor(message: ErrorDetail, status = 0, uncertain = false, code?: string, requestId?: string, retryAt = 0) {
    super(formatErrorDetail(message, status, code));
    // A closed error code is sufficient to translate its message on later renders.
    this.#detail = typeof message !== 'string' && 'server' in message && errorCodeText(code) ? {} : message;
    this.status = status;
    this.uncertain = uncertain;
    this.code = code;
    this.requestId = safeRequestId(requestId);
    this.retryAt = retryAt;
  }
}

export async function request<T>(path: string, init?: RequestInit, cache?: { value?: T; response?: (response: Response) => void; maxBytes?: number }): Promise<T> {
  const riskInput=captureRiskInput(path,init);
  if(riskChangedInput(riskInput))throw new ApiError({ ui: '该报价已有受限的原请求，请到额度风控复核查看原记录。' },409,false,'CREDIT_RISK_ORIGINAL_REQUIRED');
  const mutation = ['POST', 'PUT', 'PATCH', 'DELETE'].includes(init?.method || 'GET');
  const controller = new AbortController();
  const abort = () => controller.abort();
  init?.signal?.addEventListener('abort', abort, { once: true });
  if (init?.signal?.aborted) controller.abort();
  const timer = window.setTimeout(() => controller.abort(), 15_000);
  try {
    const response = await fetch('/api/v1' + path, { ...init, credentials: 'same-origin', signal: controller.signal, headers: { 'Content-Type': 'application/json', ...init?.headers } });
    if (response.status === 304) { if (cache?.value !== undefined) { cache.response?.(response); return cache.value; } throw new ApiError({ ui: '模型缓存不可用，请重新刷新目录。' }, 304); }
    const raw = cache?.maxBytes ? await readMessagePageText(response, cache.maxBytes).catch(error => {
      throw new ApiError(error instanceof Error ? error.message : uiText('无法读取消息页。'), response.status);
    }) : await response.text();
    const requestId = safeRequestId(response.headers.get('X-Request-ID'));
    const retryAt = retryDeadline(response.headers.get('Retry-After'), response.status, Date.now(), path.startsWith('/auth/') ? ACCOUNT_RETRY_SECONDS : MAX_RETRY_SECONDS);
    let data: unknown;
    try { data = raw ? JSON.parse(raw) : {}; } catch { throw new ApiError({ ui: '服务返回了无法识别的响应，请检查 Go API 是否运行。' }, response.status, mutation, undefined, requestId, retryAt); }
    if (!response.ok) {
      const body = data as { error?: { message?: string; code?: string; request_id?: string } | string; message?: string };
      const code = typeof body.error === 'object' && typeof body.error?.code === 'string' ? body.error.code : undefined;
      if (response.status === 403 && code === 'ACCOUNT_RESTRICTED') window.dispatchEvent(new Event('studio-account-restricted'));
      if(response.status===429 && riskCodes.includes(code||''))rememberRisk(riskInput);
      const serverMessage = typeof body.error === 'string' ? body.error : body.error?.message || body.message;
      // This closed code is exclusively a confirmed pre-admission rejection. Existing unknown requests still retain their original key in the recovery stores.
      const chatCreation = init?.method === 'POST' && (path === '/runs' || /^\/conversations\/[^/?]+\/runs$/.test(path));
      const preAdmissionRejection = definiteModerationUnavailable(response.status, code) || (chatCreation && definiteExecutionRejection(response.status, code));
      throw new ApiError({ server: typeof serverMessage === 'string' ? serverMessage : undefined }, response.status, response.status >= 500 && mutation && !preAdmissionRejection, code, requestId || (typeof body.error === 'object' ? body.error?.request_id : undefined), retryAt);
    }
    cache?.response?.(response); return data as T;
  } catch (error) {
    if (error instanceof ApiError) throw error;
    throw new ApiError({ ui: error instanceof DOMException && error.name === 'AbortError' ? '连接超时，请检查本地服务后重试。' : '无法连接 Go API，请确认服务已在 127.0.0.1:8080 启动。' }, 0, mutation);
  } finally { window.clearTimeout(timer); init?.signal?.removeEventListener('abort', abort); }
}

export const api = {
  generationAccess: (signal?: AbortSignal) => request<GenerationAccess>('/workspace/generation-access', { signal }),
  generationAppeals: (query: string, signal?: AbortSignal) => request<GenerationPage<GenerationAppeal>>('/appeals?' + query, { signal }),
  generationAppeal: (id: string, signal?: AbortSignal) => request<GenerationAppeal>('/appeals/' + encodeURIComponent(id), { signal }),
  createGenerationAppeal: (restriction: string, body: AppealInput, key: string) => request<GenerationAppeal>('/restrictions/' + encodeURIComponent(restriction) + '/appeals', { method: 'POST', headers: { 'Idempotency-Key': key }, body: JSON.stringify(body) }),
  operationGenerationAccess: (workspace: string, signal?: AbortSignal) => request<GenerationAccess>('/operations/generation-access?workspace_id=' + encodeURIComponent(workspace), { signal }),
  operationGenerationAppeals: (query: string, signal?: AbortSignal) => request<GenerationPage<GenerationAppeal>>('/operations/appeals?' + query, { signal }),
  operationGenerationAppeal: (workspace: string, id: string, signal?: AbortSignal) => request<GenerationAppeal>('/operations/appeals/' + encodeURIComponent(id) + '?workspace_id=' + encodeURIComponent(workspace), { signal }),
  planGeneration: (body: GenerationPlanInput) => request<GenerationPlan>('/operations/generation-plans', { method: 'POST', body: JSON.stringify(body) }),
  executeGeneration: (body: GenerationExecuteInput) => request<GenerationReceipt>('/operations/generation-executions', { method: 'POST', body: JSON.stringify(body) }),
  generationReceipt: (workspace: string, plan: string, signal?: AbortSignal) => request<GenerationReceipt>('/operations/generation-plans/' + encodeURIComponent(plan) + '/receipt?workspace_id=' + encodeURIComponent(workspace), { signal }),
  operationsAccess: (signal?: AbortSignal) => request<OperationAccess>('/operations/access', { signal }),
  operationExceptions: (query: string, signal?: AbortSignal) => request<OperationPage<OperationException>>('/operations/exceptions?' + query, { signal }),
  operationCases: (query: string, signal?: AbortSignal) => request<OperationPage<OperationCase>>('/operations/cases?' + query, { signal }),
  operationCase: (workspace: string, id: string, signal?: AbortSignal) => request<OperationCase>('/operations/cases/' + encodeURIComponent(id) + '?workspace_id=' + encodeURIComponent(workspace), { signal }),
  operationEvents: (workspace: string, id: string, cursor?: string | null, signal?: AbortSignal) => request<OperationPage<OperationEvent>>('/operations/cases/' + encodeURIComponent(id) + '/events?workspace_id=' + encodeURIComponent(workspace) + '&limit=20' + (cursor ? '&cursor=' + encodeURIComponent(cursor) : ''), { signal }),
  openOperationCase: (body: OperationCaseInput, key: string) => request<OperationCase>('/operations/cases', { method: 'POST', headers: { 'Idempotency-Key': key }, body: JSON.stringify(body) }),
  planOperation: (id: string, body: OperationPlanInput) => request<OperationPlan>('/operations/cases/' + encodeURIComponent(id) + '/plans', { method: 'POST', body: JSON.stringify(body) }),
  executeOperation: (id: string, body: OperationExecuteInput) => request<OperationCase>('/operations/cases/' + encodeURIComponent(id) + '/execute', { method: 'POST', body: JSON.stringify(body) }),
  billingQuote: (project: string, body: QuoteRequest, signal?: AbortSignal) => request<BillingQuote>('/billing/quotes', { method: 'POST', body: JSON.stringify(body), headers: projectHeaders(project), signal }),
  billingWallet: (project: string, signal?: AbortSignal) => request<LedgerWallet>('/billing/wallet', { headers: projectHeaders(project), signal }),
  billingEntries: (project: string, query: string, signal?: AbortSignal) => request<LedgerPage<LedgerEntry>>('/billing/entries?limit=20' + query, { headers: projectHeaders(project), signal }),
  billingReservations: (project: string, query: string, signal?: AbortSignal) => request<LedgerPage<LedgerReservation>>('/billing/reservations?limit=20' + query, { headers: projectHeaders(project), signal }),
  requestEmailVerification: () => request<AccountResult>('/auth/email/verification/request', { method: 'POST', body: '{}' }),
  confirmEmailVerification: (token: string) => request<AccountResult>('/auth/email/verification/confirm', { method: 'POST', body: JSON.stringify({ token }) }),
  requestPasswordReset: (email: string) => request<AccountResult>('/auth/password/reset/request', { method: 'POST', body: JSON.stringify({ email }) }),
  confirmPasswordReset: (token: string, new_password: string) => request<AccountResult>('/auth/password/reset/confirm', { method: 'POST', body: JSON.stringify({ token, new_password }) }),
  changePassword: (current_password: string, new_password: string) => request<AccountResult>('/auth/password/change', { method: 'POST', body: JSON.stringify({ current_password, new_password }) }),
  loginSessions: (signal?: AbortSignal) => request<{ items: LoginSession[] }>('/auth/sessions', { signal }),
  revokeSession: (id: string) => request<AccountResult>('/auth/sessions/' + encodeURIComponent(id) + '/revoke', { method: 'POST', body: '{}' }),
  revokeAllSessions: () => request<AccountResult>('/auth/sessions/revoke-all', { method: 'POST', body: '{}' }),
  securityEvents: (cursor?: string, signal?: AbortSignal) => request<SecurityEventPage>('/auth/security/events?limit=20' + (cursor ? '&cursor=' + encodeURIComponent(cursor) : ''), { signal }),
  conversations: (project: string, cursor?: string, signal?: AbortSignal) => request<ConversationPage>('/conversations?limit=20' + (cursor ? '&cursor=' + encodeURIComponent(cursor) : ''), { headers: projectHeaders(project), signal }),
  conversation: (project: string, id: string, signal?: AbortSignal) => request<Conversation>('/conversations/' + encodeURIComponent(id), { headers: projectHeaders(project), signal }),
  conversationMessages: (project: string, id: string, leaf?: string, before?: string, signal?: AbortSignal) => request<MessagePage>('/conversations/' + encodeURIComponent(id) + '/messages?limit=20' + (leaf ? '&leaf_id=' + encodeURIComponent(leaf) : '') + (before ? '&before=' + encodeURIComponent(before) : ''), { headers: projectHeaders(project), signal }, { maxBytes:maxMessagePageBytes }),
  conversationBranches: (project: string, id: string, cursor?: string, signal?: AbortSignal) => request<BranchPage>('/conversations/' + encodeURIComponent(id) + '/branches?limit=20' + (cursor ? '&cursor=' + encodeURIComponent(cursor) : ''), { headers: projectHeaders(project), signal }),
  createConversation: (body: { project_id: string; title?: string }, key: string) => request<Conversation>('/conversations', { method: 'POST', body: JSON.stringify(body), headers: { 'Idempotency-Key': key, ...projectHeaders(body.project_id) } }),
  createConversationRun: (project: string, id: string, body: TurnInput, key: string) => request<Run>('/conversations/' + encodeURIComponent(id) + '/runs', { method: 'POST', body: JSON.stringify(body), headers: { 'Idempotency-Key': key, ...projectHeaders(project) } }),
  session: () => request<AuthSession>('/auth/session'),
  login: (body: { email: string; password: string }) => request<AuthSession>('/auth/login', { method: 'POST', body: JSON.stringify(body) }),
  register: (body: { email: string; password: string; name: string }) => request<AuthSession>('/auth/register', { method: 'POST', body: JSON.stringify(body) }),
  logout: () => request<unknown>('/auth/logout', { method: 'POST' }),
  projects: () => request<{ items: Project[] }>('/projects'),
  references: (projectId: string, offset = 0) => request<HistoryPage<ReferenceAsset>>(`/references?limit=100&offset=${offset}`, { headers: projectHeaders(projectId) }),
  library: (projectId: string, filters: LibraryFilters, cursor?: string, signal?: AbortSignal) => request<LibraryPageData>('/library?' + libraryQuery(filters, cursor), { headers: projectHeaders(projectId), signal }),
  libraryAsset: (projectId: string, id: string, signal?: AbortSignal) => request<AssetEntry>('/library/' + encodeURIComponent(id), { headers: projectHeaders(projectId), signal }),
  libraryWrite: (projectId: string, id: string, operation: AssetOperation) => request<AssetEntry>('/library/' + encodeURIComponent(id) + (operation.kind === 'metadata' ? '' : '/' + operation.kind), { method: operation.kind === 'metadata' ? 'PATCH' : 'POST', body: JSON.stringify(operation.body), headers: { ...projectHeaders(projectId), 'Idempotency-Key': operation.key } }),
  libraryRevisions: (projectId: string, id: string, before?: number, signal?: AbortSignal) => request<AssetRevisionPage>('/library/' + encodeURIComponent(id) + '/revisions?limit=20' + (before ? '&before_revision=' + before : ''), { headers: projectHeaders(projectId), signal }),
  createProject: (name: string) => request<Project>('/projects', { method: 'POST', body: JSON.stringify({ name }) }),
  draft: (projectId: string) => request<CloudDraft>(`/projects/${encodeURIComponent(projectId)}/draft`, { headers: projectHeaders(projectId) }),
  saveDraft: (projectId: string, body: DraftWriteInput, key: string) => request<CloudDraft>(`/projects/${encodeURIComponent(projectId)}/draft`, { method: 'PUT', body: JSON.stringify(body), headers: { ...projectHeaders(projectId), 'Idempotency-Key': key } }),
  draftRevisions: (projectId: string, before?: number) => request<DraftRevisionPage>(`/projects/${encodeURIComponent(projectId)}/draft/revisions?limit=20${before ? '&before_revision=' + before : ''}`, { headers: projectHeaders(projectId) }),
  draftRevision: (projectId: string, revision: number) => request<CloudDraft>(`/projects/${encodeURIComponent(projectId)}/draft/revisions/${revision}`, { headers: projectHeaders(projectId) }),
  audit: (projectId: string, cursor?: string) => request<AuditPage>(`/audit?scope=project&limit=50${cursor ? '&cursor=' + encodeURIComponent(cursor) : ''}`, { headers: projectHeaders(projectId) }),
  bootstrap: (projectId?: string) => request<Bootstrap>('/bootstrap', { headers: projectHeaders(projectId) }),
  runtime: (projectId: string, signal?: AbortSignal) => request<WorkspaceRuntime>('/runtime', { headers: projectHeaders(projectId), signal }),
  modelCatalog: async (projectId: string, entry?: CatalogEntry, signal?: AbortSignal): Promise<{ catalog: ModelCatalog; etag?: string }> => {
    let etag = entry?.etag;
    const catalog = await request<ModelCatalog>('/models', { headers: { ...projectHeaders(projectId), ...(entry?.etag ? { 'If-None-Match': entry.etag } : {}) }, signal }, { value: entry?.catalog, response: response => { if (response.status !== 304) etag = undefined; const value = response.headers.get('ETag'); if (value && value.length <= 200 && !/[\r\n]/.test(value)) etag = value; } });
    return { catalog, etag };
  },
  tasks: (projectId: string, offset = 0, signal?: AbortSignal) => request<HistoryPage<Task>>(`/tasks?limit=50&offset=${offset}`, { headers: projectHeaders(projectId), signal }),
  task: (id: string, projectId: string, signal?: AbortSignal) => request<Task>('/tasks/' + encodeURIComponent(id), { headers: projectHeaders(projectId), signal }),
  taskReceipt: (project: string, key: string) => request<Task>('/tasks/receipt', { headers: { ...projectHeaders(project), 'Idempotency-Key': key } }),
  createTask: (body: TaskInput, key: string) => request<Task>('/tasks', { method: 'POST', body: JSON.stringify(body), headers: { 'Idempotency-Key': key } }),
  cancelTask: (id: string, projectId: string) => request<Task>('/tasks/' + encodeURIComponent(id) + '/cancel', { method: 'POST', headers: projectHeaders(projectId) }),
  reconcileTask: (id: string, projectId: string) => request<Task>('/tasks/' + encodeURIComponent(id) + '/reconcile', { method: 'POST', headers: projectHeaders(projectId) }),
  runs: (projectId: string, offset = 0, signal?: AbortSignal) => request<HistoryPage<Run>>(`/runs?limit=50&offset=${offset}`, { headers: projectHeaders(projectId), signal }),
  snapshot: (id: string, projectId: string, signal?: AbortSignal) => request<Run>('/runs/' + encodeURIComponent(id) + '/snapshot', { headers: projectHeaders(projectId), signal }),
  createRun: (body: { project_id: string; model_id: string; model_version?: string; input: string; quote_id?: string }, key: string) => request<Run>('/runs', { method: 'POST', body: JSON.stringify(body), headers: { 'Idempotency-Key': key } }),
  cancelRun: (id: string, projectId: string) => request<Run>('/runs/' + encodeURIComponent(id) + '/cancel', { method: 'POST', headers: projectHeaders(projectId) }),
};

function projectHeaders(projectId?: string): Record<string, string> { return projectId ? { 'X-Project-ID': projectId } : {}; }

export function safeAssetURL(url: string): string | undefined {
  try {
    const parsed = new URL(url, window.location.origin);
    if (parsed.origin !== window.location.origin || !['http:', 'https:'].includes(parsed.protocol)) return undefined;
    if (!parsed.pathname.startsWith('/demo-assets/') && !parsed.pathname.startsWith('/api/v1/assets/') && !/^\/api\/v1\/(references|library)\/[^/]+\/(content|thumbnail)$/.test(parsed.pathname)) return undefined;
    return parsed.pathname + parsed.search;
  } catch { return undefined; }
}
