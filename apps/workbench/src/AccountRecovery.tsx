import { LanguagePicker } from './LanguagePicker';
import { uiText } from './locale';
import { useEffect, useRef, useState } from 'react';
import { api, ApiError } from './api';
import type { AccountLink, AccountSecurity } from './accountSecurity';
import { confirmedAccountResult, newPasswordError, safeAccountError } from './accountSecurity';
import { Icon } from './components';
import { RequestDetails } from './RequestDetails';
import { useCountdown } from './useCountdown';
import { ACCOUNT_RETRY_SECONDS } from './requestFeedback';
import './security.css';

export function AccountRecovery({ link, capabilities, onClose, onVerified, onPasswordReset }: { link: AccountLink | null; capabilities?: AccountSecurity; onClose: () => void; onVerified: () => void; onPasswordReset: () => void }) {
  const mode = link?.kind || 'forgot', [email, setEmail] = useState(''), [password, setPassword] = useState(''), [confirmation, setConfirmation] = useState('');
  const [busy, setBusy] = useState(false), [issue, setIssue] = useState<ApiError | null>(null), [message, setMessage] = useState(''), [localError, setLocalError] = useState(''), [consumed, setConsumed] = useState(false);
  const mounted = useRef(true), generation = useRef(0), lock = useRef(false), seconds = useCountdown(issue?.retryAt || 0, ACCOUNT_RETRY_SECONDS);
  useEffect(() => { mounted.current = true; generation.current++; return () => { mounted.current = false; generation.current++; }; }, []);
  const enabled = mode !== 'forgot' || !!capabilities?.password_reset_enabled && !!capabilities.mail_configured;
  function leave() { if (busy) return; if (link && link.kind !== 'invalid') link.secret.clear(); setPassword(''); setConfirmation(''); onClose(); }
  async function submit(event: React.FormEvent) {
    event.preventDefault(); if (lock.current || consumed || seconds > 0 || !enabled || mode === 'invalid') return;
    const invalid = mode === 'reset' ? newPasswordError(password, confirmation) : mode === 'forgot' && (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.trim()) || email.trim().length > 254) ? '请输入有效邮箱地址。' : '';
    setLocalError(invalid); if (invalid) return;
    const token = link && link.kind !== 'invalid' ? link.secret.take() : '', nextPassword = password;
    if (mode !== 'forgot' && !token) { setConsumed(true); setLocalError('此页面已提交过链接，请重新申请或打开邮件中的链接。'); return; }
    lock.current = true; setBusy(true); setIssue(null); setMessage(''); setPassword(''); setConfirmation(''); if (mode !== 'forgot') setConsumed(true);
    const ticket = generation.current;
    try {
      const result = mode === 'forgot' ? await api.requestPasswordReset(email.trim()) : mode === 'verify' ? await api.confirmEmailVerification(token) : await api.confirmPasswordReset(token, nextPassword);
      if (!confirmedAccountResult(result, mode === 'reset' ? 'required' : 'none')) throw new ApiError(uiText('账户响应无法确认。'), 200, true);
      if (!mounted.current || ticket !== generation.current) return;
      if (mode === 'forgot') { setMessage('如果该地址符合找回条件，系统已安排发送。请检查收件箱与垃圾邮件；此回复不代表邮件已经送达。'); setConsumed(true); }
      else if (mode === 'verify') { setMessage('邮箱验证已完成。此操作没有切换或建立登录会话。'); onVerified(); }
      else { setMessage('密码已重置，全部登录会话已失效。请使用新密码重新登录。'); onPasswordReset(); }
    } catch (cause) { if (mounted.current && ticket === generation.current) { setIssue(safeAccountError(cause)); if (mode === 'forgot' && (cause instanceof ApiError ? cause.uncertain : true)) setConsumed(true); } }
    finally { if (mounted.current && ticket === generation.current) { lock.current = false; setBusy(false); } }
  }
  const heading = { forgot: uiText('找回创作的入口。'), verify: uiText('让邮箱，成为可信入口。'), reset: uiText('为账户，换一把新钥匙。'), invalid: uiText('这封邀请，需要重新打开。') }[mode];
  return <main className="account-recovery-page"><header className="auth-top"><button className="auth-brand" onClick={leave} disabled={busy}><span>✧</span>{uiText("星图")} <b>AI Studio</b></button><LanguagePicker/></header><section className="recovery-layout"><div className="recovery-illustration" aria-hidden="true"><svg viewBox="0 0 320 320" fill="none"><circle cx="160" cy="160" r="117" stroke="currentColor" opacity=".12"/><circle cx="160" cy="160" r="89" stroke="currentColor" strokeDasharray="3 9" opacity=".3"/><path d="M160 63 234 96v77c0 44-38 73-74 91-36-18-74-47-74-91V96Z" stroke="currentColor" opacity=".65"/><path d="m133 159 19 20 38-43" stroke="currentColor" strokeWidth="3"/><path d="M29 160h47m168 0h47M160 28v27m0 217v20" stroke="currentColor" opacity=".35"/><circle cx="160" cy="63" r="4" fill="currentColor"/><circle cx="234" cy="173" r="3" fill="currentColor"/></svg><span>YOUR SPACE. YOUR ACCESS.</span></div><div className="recovery-panel"><span className="eyebrow">A SECURE WAY BACK</span><h1>{heading}</h1><p className="recovery-intro">{mode === 'forgot' ? uiText('填写注册邮箱。我们会按统一流程处理找回申请，不透露账户是否存在。') : mode === 'verify' ? uiText('链接已从地址栏移除。点击确认后验证链接对应的邮箱，当前账户不会被切换。') : mode === 'reset' ? uiText('链接已从地址栏移除。设置新密码后，包括当前浏览器在内的全部登录会话都将失效。') : uiText('链接格式不正确或已无法安全读取。请从完整的验证或找回邮件重新打开；不要复制令牌到聊天或其他网站。')}</p>
      {!enabled && <div className="security-unavailable"><strong>{uiText("邮件找回暂未开放")}</strong><p>{capabilities ? uiText('当前服务尚未配置可用邮件渠道，暂时不能安排找回邮件。') : uiText('账户安全能力尚未读取，请返回登录页重新连接服务。')}{uiText("该页面没有发送任何邮件。")}</p></div>}
      {message && <div className="security-success" role="status"><span>✓</span><p>{uiText(message)}</p></div>}
      {mode !== 'invalid' && !message && <form className="auth-form" onSubmit={event => void submit(event)}>{mode === 'forgot' && <div><label htmlFor="recovery-email">{uiText("注册邮箱")}</label><input id="recovery-email" type="email" autoComplete="email" maxLength={254} value={email} onChange={event => setEmail(event.target.value)} disabled={busy || !enabled || consumed} required placeholder="you@example.com"/></div>}{mode === 'reset' && <><div><label htmlFor="recovery-password">{uiText("新密码")}</label><input id="recovery-password" type="password" maxLength={72} autoComplete="new-password" value={password} onChange={event => setPassword(event.target.value)} disabled={busy || consumed} required/><p className="auth-help">{uiText("10—72 UTF-8字节，中文通常占3字节。请使用独立密码。")}</p></div><div><label htmlFor="recovery-confirmation">{uiText("再次输入新密码")}</label><input id="recovery-confirmation" type="password" maxLength={72} autoComplete="new-password" value={confirmation} onChange={event => setConfirmation(event.target.value)} disabled={busy || consumed} required/></div></>}{localError && <p className="auth-error" role="alert">{uiText(localError)}</p>}{issue && <div className="auth-error" role="alert"><p>{issue.displayMessage}</p><RequestDetails error={issue} unknown={issue.uncertain} sensitive/>{consumed && <p>{uiText("本页不会重试敏感提交。可返回登录核对，或重新申请链接。")}</p>}</div>}<button type="submit" className="button primary auth-submit" disabled={busy || consumed || seconds > 0 || !enabled}>{busy ? uiText('正在处理…') : mode === 'verify' ? uiText('确认验证邮箱') : mode === 'reset' ? uiText('确认重置并退出全部会话') : uiText('申请找回邮件')}<Icon name="arrow" size={16}/></button></form>}
      <button className="text-button recovery-return" onClick={leave} disabled={busy}><span>←</span>{message && mode === 'reset' ? uiText('返回并使用新密码登录') : uiText('返回工作台或登录页')}</button><div className="security-footnote">{uiText("密码和链接令牌不进入浏览器存储或草稿恢复仓。网络不明时不会自动重新提交。")}</div>
    </div></section><footer className="auth-footer">{uiText("邮件链接只在当前平台验证，请勿向他人转发。")}</footer></main>;
}
