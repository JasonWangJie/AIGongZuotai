import { uiText } from './locale';
import { ApiError } from './api';

export type AccountSecurity = { enabled: boolean; mail_configured: boolean; verification_enabled: boolean; password_reset_enabled: boolean; reason?: string };
export class OneTimeAccountSecret { #value: string; constructor(value: string) { this.#value = value; } take(): string { const value = this.#value; this.#value = ''; return value; } clear(): void { this.#value = ''; } }
export type AccountLink = { kind: 'verify' | 'reset'; secret: OneTimeAccountSecret } | { kind: 'invalid' };
export type LoginSession = { id: string; current: boolean; created_at: string; expires_at: string };
export type SecurityEvent = { id: string; action: string; created_at: string };
export type SecurityEventPage = { items: SecurityEvent[]; next_cursor: string | null };
export type AccountResult = { ok: boolean; reauthenticate?: boolean; message?: string };
export function confirmedAccountResult(value: AccountResult, reauthentication: 'required' | 'boolean' | 'none' = 'none'): boolean { return !!value && value.ok === true && (reauthentication === 'required' ? value.reauthenticate === true : reauthentication === 'boolean' ? typeof value.reauthenticate === 'boolean' : true); }
/** Called exactly once before React/StrictMode mounts. Never saves the URL or token to storage. */
export function takeAccountLink(location: Pick<Location, 'hash' | 'pathname' | 'search'>, history: Pick<History, 'replaceState' | 'state'>): AccountLink | null {
  const raw = location.hash;
  if (!raw) return null;
  const params = new URLSearchParams(raw.slice(1));
  if (!params.has('auth') && !params.has('token')) return null;
  try { history.replaceState(history.state, '', location.pathname + location.search); } catch { return { kind: 'invalid' }; }
  const kind = params.get('auth'), token = params.get('token');
  if (raw.length > 512 || params.getAll('auth').length !== 1 || params.getAll('token').length !== 1 || params.size !== 2 || !['verify', 'reset'].includes(kind || '') || !token || !/^[A-Za-z0-9_-]{43}$/.test(token)) return { kind: 'invalid' };
  return { kind: kind as 'verify' | 'reset', secret: new OneTimeAccountSecret(token) };
}
export function newPasswordError(password: string, confirmation: string): string {
  const size = new TextEncoder().encode(password).byteLength;
  if (password.includes('\0') || new TextDecoder().decode(new TextEncoder().encode(password)) !== password || size < 10 || size > 72) return '新密码需为 10—72 UTF-8 字节，不能包含空字符；中文通常占 3 字节。';
  if (password !== confirmation) return '两次输入的新密码不一致。';
  return '';
}
export function accountReadCurrent(alive: boolean, generation: number, ticket: number, owner: string, originalOwner: string): boolean { return alive && generation === ticket && owner === originalOwner; }
export function account401Action(issue: { status: number; code?: string }): 'none' | 'inspect' | 'expire' { return issue.status !== 401 ? 'none' : issue.code === 'INVALID_CREDENTIALS' ? 'inspect' : 'expire'; }
export function safeAccountError(cause: unknown): ApiError {
  const issue = cause instanceof ApiError ? cause : new ApiError(uiText('账户请求未完成。'), 0, true);
  const message = issue.uncertain ? '提交结果尚未确认。敏感内容已清除，请先核对账户状态；不要自动重复提交。' : ({ ACCOUNT_TOKEN_INVALID: '此链接无效、已过期或已使用，请重新申请链接。', MAIL_UNAVAILABLE: '邮件服务尚未配置或暂时不可用，当前无法安排发送。', INVALID_CREDENTIALS: '当前密码不正确，请核对后重新输入。', INVALID_PASSWORD: '新密码不符合要求，请重新输入。', SESSION_NOT_FOUND: '该登录会话已不存在，请刷新列表。', ACCOUNT_SECURITY_UNAVAILABLE: '当前服务未启用账户安全功能。' }[issue.code || ''] || (issue.status === 401 ? '登录状态已失效，请重新登录。' : issue.status === 429 ? '操作过于频繁，请等待冷却结束后手动操作。' : issue.status === 404 ? '该登录会话已不存在，请刷新列表。' : '账户操作未完成。请核对输入或稍后手动操作。'));
  const knownCode = ['ACCOUNT_TOKEN_INVALID', 'MAIL_UNAVAILABLE', 'INVALID_CREDENTIALS', 'INVALID_PASSWORD', 'SESSION_NOT_FOUND', 'ACCOUNT_SECURITY_UNAVAILABLE'].includes(issue.code || '') ? issue.code : undefined;
  return new ApiError({ ui: message }, issue.status, issue.uncertain, knownCode, issue.requestId, issue.retryAt);
}
export function securityActionName(action: string): string { return ({ 'account.registered': uiText('创建账户'), 'account.login': uiText('登录账户'), 'account.logout': uiText('退出登录'), 'email.verification_requested': uiText('申请验证邮件'), 'email.verified': uiText('完成邮箱验证'), 'password.reset_requested': uiText('申请密码重置'), 'password.reset': uiText('重置密码'), 'password.changed': uiText('修改密码'), 'session.revoked': uiText('撤销登录会话'), 'sessions.revoked_all': uiText('撤销全部登录会话') }[action] || uiText('账户安全事件')); }
