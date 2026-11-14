import { useEffect, useRef, useState } from 'react';
import { api, ApiError } from './api';
import type { AuthSession } from './types';
import type { AccountResult, LoginSession, SecurityEvent } from './accountSecurity';
import { account401Action, accountReadCurrent, confirmedAccountResult, newPasswordError, safeAccountError, securityActionName } from './accountSecurity';
import { Icon } from './components';
import { RequestDetails } from './RequestDetails';
import { useCountdown } from './useCountdown';
import { ACCOUNT_RETRY_SECONDS } from './requestFeedback';
import './security.css';

type Confirmation = { kind: 'password' } | { kind: 'all' } | { kind: 'session'; session: LoginSession };
export function AccountSecurityPage({ session, visible, hasUnsaved, onBusy, onExpired, onRefreshIdentity }: { session: AuthSession; visible: boolean; hasUnsaved: boolean; onBusy: (busy: boolean) => void; onExpired: (message?: string) => void; onRefreshIdentity: () => Promise<void> }) {
  const owner = session.user?.id || '', caps = session.account_security;
  const [sessions, setSessions] = useState<LoginSession[]>([]), [events, setEvents] = useState<SecurityEvent[]>([]), [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(false), [busy, setBusy] = useState(false), [issue, setIssue] = useState<ApiError | null>(null), [message, setMessage] = useState(''), [localError, setLocalError] = useState('');
  const [oldPassword, setOldPassword] = useState(''), [password, setPassword] = useState(''), [confirmation, setConfirmation] = useState(''), [confirm, setConfirm] = useState<Confirmation | null>(null);
  const [mailRequested, setMailRequested] = useState(false), [unknownAction, setUnknownAction] = useState(false);
  const alive = useRef(true), generation = useRef(0), ownerRef = useRef(owner), requestLock = useRef(false), readLock = useRef(false), abort = useRef(new AbortController()), dialog = useRef<HTMLDialogElement>(null);
  const callbacks = useRef({ onBusy, onExpired, onRefreshIdentity }); callbacks.current = { onBusy, onExpired, onRefreshIdentity }; ownerRef.current = owner;
  const seconds = useCountdown(issue?.retryAt || 0, ACCOUNT_RETRY_SECONDS), enabled = !!session.auth_enabled && !!session.user && !!caps?.enabled;
  function current(ticket: number, requestedOwner = owner) { return accountReadCurrent(alive.current, generation.current, ticket, ownerRef.current, requestedOwner); }
  function clearPasswords() { setOldPassword(''); setPassword(''); setConfirmation(''); }
  function fail(cause: unknown, ticket: number) { if (!current(ticket)) return; const error = safeAccountError(cause); setIssue(error); const action = account401Action(error); if (action === 'expire') callbacks.current.onExpired(); else if (action === 'inspect') void callbacks.current.onRefreshIdentity(); }
  async function refresh(next?: string) {
    if (!enabled || readLock.current || Date.now() < (issue?.retryAt || 0)) return; readLock.current = true; setLoading(true); const ticket = generation.current;
    try {
      const [sessionPage, eventPage] = await Promise.all([next ? Promise.resolve(null) : api.loginSessions(abort.current.signal), api.securityEvents(next, abort.current.signal)]);
      if (!current(ticket)) return;
      if (sessionPage) setSessions(sessionPage.items.slice(0, 10));
      setEvents(old => next ? [...old, ...eventPage.items.filter(item => !old.some(previous => previous.id === item.id))] : eventPage.items); setCursor(eventPage.next_cursor);
      setIssue(old => old?.uncertain ? old : null); if (!next) await callbacks.current.onRefreshIdentity();
    } catch (cause) { fail(cause, ticket); }
    finally { if (current(ticket)) { readLock.current = false; setLoading(false); } }
  }
  async function mutate(action: 'verify' | Confirmation) {
    if (!enabled || requestLock.current || seconds > 0 || unknownAction) return;
    if (action === 'verify' && (!caps?.mail_configured || !caps.verification_enabled || session.user?.email_verified || mailRequested)) return;
    const currentPassword = oldPassword, newPassword = password;
    if (action !== 'verify' && action.kind === 'password') { const error = !currentPassword || currentPassword.includes('\0') ? '请输入有效的当前密码。' : newPasswordError(newPassword, confirmation); setLocalError(error); if (error) { setConfirm(null); return; } }
    requestLock.current = true; setBusy(true); callbacks.current.onBusy(true); setIssue(null); setMessage(''); setConfirm(null); clearPasswords();
    const ticket = generation.current;
    try {
      let result: AccountResult;
      if (action === 'verify') result = await api.requestEmailVerification();
      else if (action.kind === 'password') result = await api.changePassword(currentPassword, newPassword);
      else if (action.kind === 'all') result = await api.revokeAllSessions();
      else result = await api.revokeSession(action.session.id);
      if (!confirmedAccountResult(result, action === 'verify' ? 'none' : action.kind === 'session' ? 'boolean' : 'required')) throw new ApiError('账户响应无法确认。', 200, true);
      if (!current(ticket)) return;
      if (result.reauthenticate) { callbacks.current.onExpired(action !== 'verify' && action.kind === 'password' ? '密码已修改，全部会话已退出。请使用新密码登录。' : '所选登录会话已撤销，请重新登录。'); return; }
      if (action === 'verify') { setMailRequested(true); setMessage('验证邮件申请已受理；符合条件时系统会安排发送。此状态不代表邮件已送达，请检查收件箱与垃圾邮件。'); }
      else setMessage('所选登录会话已撤销。已打开的实时连接会在服务端下一次身份检查时关闭。');
      void refresh();
    } catch (cause) { if (current(ticket)) { const error = safeAccountError(cause); if (error.uncertain) setUnknownAction(true); fail(error, ticket); } }
    finally { if (current(ticket)) { requestLock.current = false; setBusy(false); callbacks.current.onBusy(false); } }
  }
  function requestPasswordChange(event: React.FormEvent) { event.preventDefault(); const error = !oldPassword || oldPassword.includes('\0') ? '请输入有效的当前密码。' : newPasswordError(password, confirmation); setLocalError(error); if (!error && !busy && !unknownAction) setConfirm({ kind: 'password' }); }
  function cancelConfirmation() { setConfirm(null); clearPasswords(); }
  useEffect(() => { alive.current = true; generation.current++; requestLock.current = false; readLock.current = false; abort.current = new AbortController(); if (visible) void refresh(); return () => { alive.current = false; generation.current++; abort.current.abort(); callbacks.current.onBusy(false); }; }, [owner, enabled]);
  useEffect(() => { if (!visible) { clearPasswords(); setConfirm(null); } else void refresh(); }, [visible]);
  useEffect(() => { if (confirm) dialog.current?.showModal(); else dialog.current?.close(); }, [confirm]);
  const invalidatesCurrent = confirm?.kind === 'password' || confirm?.kind === 'all' || confirm?.kind === 'session' && confirm.session.current;
  return <section className="security-page" hidden={!visible}><header className="security-heading"><div><span className="eyebrow">YOUR CREATIVE SPACE, PROTECTED</span><h1>安心创作，<em>从账户开始。</em></h1><p>管理邮箱所有权、登录会话与密码。安全事件独立于项目活动。</p></div><span className="security-heading-symbol">◇</span></header>
    {!enabled ? <div className="security-unavailable"><strong>当前工作区未启用账户安全</strong><p>此服务处于本地无鉴权模式，或尚未开放账户安全接口。不会发送验证／找回邮件，也无法管理登录会话。</p></div> : <>
      {hasUnsaved && <div className="security-draft-note"><Icon name="layers" size={16}/><p>本页仍有未保存内容或待确认请求。修改密码、撤销当前／全部会话前，请优先保存或导出。现有恢复仓仅在本页面内按账户隔离，关闭页面可能丢失；未提交的创作描述请复制留存。</p></div>}
      {issue && <div className="security-issue" role="alert"><strong>{unknownAction ? '敏感操作结果待核对' : '账户操作需要处理'}</strong><p>{issue.message}</p><RequestDetails error={issue} unknown={issue.uncertain} sensitive/>{unknownAction && <p>密码已清空，不保留可重发的敏感请求。请核对登录状态与会话；必要时重新登录或申请新的找回链接。</p>}<button className="text-button" disabled={loading || seconds > 0} onClick={() => void refresh()}>核对账户状态</button></div>}
      {message && <div className="security-success" role="status"><span>✓</span><p>{message}</p></div>}
      <div className="security-grid"><section className="security-card email-card"><div className="security-card-top"><span className="section-number">01 / EMAIL</span><span className={'verification-badge ' + (session.user?.email_verified ? 'verified' : '')}>{session.user?.email_verified ? '已验证' : '待验证'}</span></div><h2>一个可靠的联络点。</h2><p className="security-email">{session.user?.email}</p><p>邮箱验证确认地址所有权，不会扩大工作区权限。</p>{session.user?.email_verified_at && <small>验证时间 {new Date(session.user.email_verified_at).toLocaleString('zh-CN')}</small>}{!caps?.mail_configured && <div className="security-mail-disabled"><strong>邮件渠道未配置</strong><span>当前无法安排发送验证邮件，页面不会伪装为已发送。</span></div>}<button className="button secondary" disabled={busy || unknownAction || seconds > 0 || mailRequested || !caps?.verification_enabled || !caps.mail_configured || !!session.user?.email_verified} onClick={() => void mutate('verify')}><Icon name="arrow" size={14}/>{session.user?.email_verified ? '邮箱已验证' : mailRequested ? '本页已申请，请检查邮件' : '申请验证邮件'}</button></section>
      <section className="security-card password-card"><div className="security-card-top"><span className="section-number">02 / PASSWORD</span><span className="security-impact">修改后全部会话退出</span></div><h2>更新你的账户密码。</h2><form className="auth-form" onSubmit={requestPasswordChange}><div><label htmlFor="security-current-password">当前密码</label><input id="security-current-password" type="password" maxLength={72} autoComplete="current-password" value={oldPassword} onChange={event => setOldPassword(event.target.value)} disabled={busy || unknownAction} required/></div><div className="security-password-pair"><div><label htmlFor="security-new-password">新密码</label><input id="security-new-password" type="password" maxLength={72} autoComplete="new-password" value={password} onChange={event => setPassword(event.target.value)} disabled={busy || unknownAction} required/></div><div><label htmlFor="security-confirm-password">确认新密码</label><input id="security-confirm-password" type="password" maxLength={72} autoComplete="new-password" value={confirmation} onChange={event => setConfirmation(event.target.value)} disabled={busy || unknownAction} required/></div></div><p className="auth-help">10—72 UTF-8字节。提交后会撤销全部会话和此前签发的验证／找回链接。</p>{localError && <p className="auth-error" role="alert">{localError}</p>}<button type="submit" className="button secondary" disabled={busy || unknownAction || seconds > 0}>确认修改方式 <Icon name="arrow" size={14}/></button></form></section></div>
      <section className="security-card sessions-card"><div className="security-section-heading"><div><span className="section-number">03 / SESSIONS</span><h2>谁还连接着你的空间。</h2><p>最多显示10个尚未到期的登录会话，不推测设备或地理位置。</p></div><button className="text-button" disabled={loading || busy || seconds > 0} onClick={() => void refresh()}><Icon name="refresh" size={13}/>{loading ? '读取中…' : '刷新'}</button></div><div className="security-session-list">{sessions.map((item, index) => <article className="security-session-row" key={item.id}><span className={'session-marker ' + (item.current ? 'current' : '')}><Icon name="folder" size={17}/></span><div><strong>{item.current ? '当前浏览器会话' : `其他登录会话 ${index + 1}`}</strong><p>登录 {new Date(item.created_at).toLocaleString('zh-CN')}<span>到期 {new Date(item.expires_at).toLocaleString('zh-CN')}</span></p></div><button className="button secondary" disabled={busy || unknownAction || seconds > 0} onClick={() => setConfirm({ kind: 'session', session: item })}>{item.current ? '退出当前会话' : '撤销会话'}</button></article>)}</div>{!sessions.length && !loading && <p className="security-empty">暂未读到登录会话，可刷新核对。</p>}<div className="security-session-footer"><p>撤销后新请求立即失效；已有流连接在下一次身份检查时关闭。</p><button className="text-button danger" disabled={busy || unknownAction || seconds > 0 || !sessions.length} onClick={() => setConfirm({ kind: 'all' })}>撤销全部会话并退出</button></div></section>
      <section className="security-card security-events"><div className="security-section-heading"><div><span className="section-number">04 / RECENT SECURITY EVENTS</span><h2>最近的账户安全记录。</h2><p>这里只展示操作与时间，不显示邮箱、IP、浏览器标识或密钥。</p></div></div><div className="security-event-list">{events.map(event => <div key={event.id}><span>{securityActionName(event.action)}</span><time dateTime={event.created_at}>{new Date(event.created_at).toLocaleString('zh-CN')}</time></div>)}</div>{cursor && <button className="text-button" disabled={loading || seconds > 0} onClick={() => void refresh(cursor)}>加载更早安全记录</button>}{!events.length && !loading && <p className="security-empty">暂无可展示的安全记录。</p>}</section>
    </>}
    <dialog ref={dialog} className="security-confirm-dialog" aria-labelledby="security-confirm-title" onCancel={event => { event.preventDefault(); cancelConfirmation(); }}><span className="eyebrow">CONFIRM ACCOUNT CHANGE</span><h2 id="security-confirm-title">{confirm?.kind === 'password' ? '修改密码并退出所有会话？' : confirm?.kind === 'all' ? '撤销全部登录会话？' : invalidatesCurrent ? '退出当前浏览器会话？' : '撤销这个登录会话？'}</h2><p>{invalidatesCurrent ? '当前页面将返回登录入口。再次进入时，请使用当前有效的账户凭据登录。' : '该会话将无法继续发起请求，你当前的登录保持有效。'}</p>{confirm?.kind === 'password' && <p>新密码生效后，旧密码与此前的验证／找回链接均失效。</p>}{hasUnsaved && <div className="security-confirm-warning">检测到未保存或待确认内容。建议先取消操作并保存／导出；页面内恢复不等于跨关闭备份。</div>}<div className="security-confirm-actions"><button className="button secondary" autoFocus onClick={cancelConfirmation}>先取消</button><button className="button primary" onClick={() => confirm && void mutate(confirm)} disabled={busy}>{confirm?.kind === 'password' ? '确认修改并退出' : invalidatesCurrent ? '确认撤销并退出' : '确认撤销'}</button></div></dialog>
  </section>;
}
