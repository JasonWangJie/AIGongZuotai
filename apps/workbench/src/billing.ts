import { uiText } from './locale';
import type { Bootstrap, RunInput, TaskInput } from './types';
import type { TurnInput } from './conversationState';

export type BillingCapability = { enabled: boolean; mode: 'disabled' | 'credits'; unit: 'CREDIT'; scale: 6; pricing_version?: string; reason?: string };
export type BillingState = 'HELD' | 'REVIEW' | 'SETTLED' | 'RELEASED' | 'REFUNDED';
export type BillingSummary = { quote_id: string; reservation_id: string; price_version: string; state: BillingState; reserved_micros: string; charged_micros: string; released_micros: string; refunded_micros: string; unit: 'CREDIT'; scale: 6; version: number; review_reason?: string };
export type LedgerWallet = { workspace_id: string; unit: 'CREDIT'; scale: 6; available_micros: string; reserved_micros: string; review_micros: string; charged_micros: string; refunded_micros: string; version: number; updated_at: string | null };
export type EntryKind = 'CREDIT' | 'RESERVE' | 'CHARGE' | 'RELEASE' | 'REFUND' | 'REFUND_RELEASE';
export type LedgerEntry = { id: string; kind: EntryKind; project_id?: string; resource_type?: string; resource_id?: string; reservation_id?: string; quote_id?: string; reversal_of?: string; amount_micros: string; available_delta_micros: string; reserved_delta_micros: string; unit: 'CREDIT'; scale: 6; created_at: string };
export type LedgerReservation = BillingSummary & { project_id: string; resource_type: string; resource_id: string; model_id: string; created_at: string; updated_at: string };
export type LedgerPage<T> = { items: T[]; next_cursor: string | null };
export type QuoteRequest = { resource_type: 'run'; run_input: RunInput } | { resource_type: 'task'; task_input: TaskInput } | { resource_type: 'conversation_run'; conversation_id: string; conversation_input: TurnInput };
export type BillingQuote = { id: string; workspace_id: string; project_id: string; resource_type: 'run' | 'task'; conversation_id?: string; model_id: string; model_version: string; price_version: string; provider_mode: 'demo' | 'live'; unit: 'CREDIT'; scale: 6; reserve_micros: string; expires_at: string; created_at: string; basis: 'chat_tokens' | 'delivered_artifact'; policy_version: string; input_per_million_micros: string; output_per_million_micros: string; artifact_micros: string; max_input_tokens: number; max_output_tokens: number; requested_units: number };
export type QuotedRequest = { scope: string; request: QuoteRequest; quote: BillingQuote };
export const billingStateName: Record<BillingState, string> = { get HELD() { return uiText('已冻结'); }, get REVIEW() { return uiText('待核实'); }, get SETTLED() { return uiText('已结算'); }, get RELEASED() { return uiText('已释放'); }, get REFUNDED() { return uiText('已冲正'); } };
export const entryKindName: Record<EntryKind, string> = { get CREDIT() { return uiText('额度入账'); }, get RESERVE() { return uiText('请求冻结'); }, get CHARGE() { return uiText('交付结算'); }, get RELEASE() { return uiText('释放冻结'); }, get REFUND() { return uiText('结算冲正'); }, get REFUND_RELEASE() { return uiText('冲正并释放'); } };
export const billingEnabled = (bootstrap?: Bootstrap | null) => bootstrap?.billing?.enabled === true && bootstrap.billing.mode === 'credits';
export const billingScope = (bootstrap?: Bootstrap | null) => bootstrap ? JSON.stringify([bootstrap.user?.id || 'demo', bootstrap.workspace.id, bootstrap.project.id]) : '';
/** Never coerce ledger micros to Number: even valid int64 values exceed IEEE-754 precision. */
export function formatCredits(value: string, signed = false): string {
  if (!/^-?\d{1,30}$/.test(value)) return '—';
  const amount = BigInt(value), negative = amount < 0n, absolute = negative ? -amount : amount;
  const whole = (absolute / 1_000_000n).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  const fraction = (absolute % 1_000_000n).toString().padStart(6, '0').replace(/0+$/, '');
  return (negative ? '−' : signed && amount > 0n ? '+' : '') + whole + (fraction ? '.' + fraction : '');
}
export function quoteSignature(scope: string, request: QuoteRequest | null): string { return JSON.stringify([scope, request]); }
export function quoteMatches(accepted: QuotedRequest | null | undefined, scope: string, request: QuoteRequest, now = Date.now()): accepted is QuotedRequest {
  return !!accepted && accepted.scope === scope && quoteSignature(scope, accepted.request) === quoteSignature(scope, request) && Date.parse(accepted.quote.expires_at) > now;
}
export function quoteResponseMatches(quote: BillingQuote, request: QuoteRequest, bootstrap: Bootstrap): boolean {
  const input = request.resource_type === 'run' ? request.run_input : request.resource_type === 'task' ? request.task_input : request.conversation_input;
  return !!quote.id && quote.workspace_id === bootstrap.workspace.id && quote.project_id === bootstrap.project.id && quote.model_id === input.model_id && (!input.model_version || quote.model_version === input.model_version) && quote.resource_type === (request.resource_type === 'task' ? 'task' : 'run') && (request.resource_type !== 'conversation_run' || quote.conversation_id === request.conversation_id) && quote.provider_mode === bootstrap.mode && quote.unit === 'CREDIT' && quote.scale === 6 && /^\d{1,30}$/.test(quote.reserve_micros) && Number.isFinite(Date.parse(quote.expires_at));
}
export function ledgerQuery(project: string, filterName: 'kind' | 'state', filter: string, cursor?: string | null): string {
  const params = new URLSearchParams(); if (project) params.set('project_id', project); if (filter) params.set(filterName, filter); if (cursor) params.set('cursor', cursor); const value = params.toString(); return value ? '&' + value : '';
}
