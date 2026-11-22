import { uiText } from './locale';
import { useEffect, useRef, useState } from 'react';
import type { AuthSession } from './types';
import { ApiError } from './api';
import { Icon } from './components';
import { AccountSecurityPage } from './AccountSecurityPage';
import { AppealError, ReceiptNotice, useAppealIO } from './ContentAppealPage';
import { recoveryKey, safeID, textBytes } from './contentAppeals';
import type { PendingAppeal } from './contentAppeals';
import { appealReasons, publicTextError } from './generationControls';
import type { AppealReason } from './generationControls';
import { operationEvidenceError } from './operations';
import { useCountdown } from './useCountdown';
import {
  accountActions,
  governanceApi,
  governanceReasons,
  matchesAccess,
  matchesAccountAppeal,
} from './accountGovernance';
import type {
  AccountAccess,
  AccountAppeal,
  AccountPlan,
  AccountPlanInput,
  AccountReceipt,
  AccountTarget,
  GovernanceAction,
} from './accountGovernance';
import './content-appeals.css';
import './account-governance.css';

type IO = ReturnType<typeof useAppealIO>;
const stateName = (s: string) => (s === 'SUSPENDED' ? uiText('账户已受限') : uiText('账户正常'));
const resultName = (a: AccountAppeal) =>
  a.state === 'OPEN' ? uiText('待审议') : a.outcome === 'LIFTED' ? uiText('本轮限制已解除') : uiText('维持本轮限制');
function useAccountRecords(id: string, operator: boolean, io: IO) {
  const target = operator ? id : undefined,
    ticket = useRef(0),
    detailTicket = useRef(0);
  const [access, setAccess] = useState<AccountAccess | null>(null),
    [items, setItems] = useState<AccountAppeal[]>([]),
    [cursor, setCursor] = useState<string | null>(null),
    [detail, setDetail] = useState<AccountAppeal | null>(null);
  async function load(more = false) {
    const t = ++ticket.current;
    try {
      const [value, page] = await Promise.all([
        governanceApi.access(target),
        governanceApi.history(target, more ? cursor : null),
      ]);
      if (t !== ticket.current || !io.alive.current) return;
      if (
        !matchesAccess(value, id) ||
        !Array.isArray(page.items) ||
        page.items.length > 50 ||
        !page.items.every((a) => matchesAccountAppeal(a, id))
      )
        throw Error(uiText('账户或历史申请与当前范围不匹配。'));
      setAccess(value);
      setItems((old) =>
        [...new Map([...(more ? old : []), ...page.items].map((a) => [a.id, a])).values()].slice(0, 200),
      );
      setCursor(page.next_cursor || null);
    } catch (e) {
      if (io.alive.current && t === ticket.current) {
        setAccess(null);
        setItems([]);
        setCursor(null);
        ++detailTicket.current;
        setDetail(null);
        io.report(e);
      }
    }
  }
  async function read(aid: string) {
    const t = ++detailTicket.current;
    setDetail(null);
    try {
      const a = await governanceApi.detail(aid, target);
      if (!io.alive.current || t !== detailTicket.current) return;
      if (!matchesAccountAppeal(a, id, aid)) throw Error(uiText('详情与当前账户或申请不匹配。'));
      setDetail(a);
    } catch (e) {
      if (t === detailTicket.current) io.report(e);
    }
  }
  useEffect(() => {
    void load();
    return () => {
      ++ticket.current;
      ++detailTicket.current;
    };
  }, [id, operator]);
  return { access, items, cursor: items.length < 200 ? cursor : null, detail, load, read };
}
type Records = ReturnType<typeof useAccountRecords>;
function StatusCard({ a }: { a: AccountAccess }) {
  return (
    <div className={'ag-status ' + (a.state === 'SUSPENDED' ? 'ag-held' : '')}>
      <div className="appeal-heading">
        <h2>{stateName(a.state)}</h2>
        <small>{uiText("状态版本")} {a.version}</small>
      </div>
      {a.state === 'SUSPENDED' && a.restriction ? (
        <>
          <p>{a.restriction.public_notice}</p>
          <p>{uiText("原因 ·")} {governanceReasons[a.restriction.reason_code] || a.restriction.reason_code}</p>
          <small>{uiText("当前限制轮次")} <code>{a.restriction.id}</code>
          </small>
        </>
      ) : (
        <p>{uiText("当前账户可按原有权限进入工作台。")}</p>
      )}
    </div>
  );
}
function History({ q, onSelect }: { q: Records; onSelect?: (id: string) => void }) {
  return (
    <section className="ag-history">
      <div className="appeal-heading">
        <h2>{uiText("账户申诉记录")}</h2>
      </div>
      <div className="appeal-columns">
        <aside>
          <div className="appeal-rows">
            {q.items.map((a) => (
              <button
                key={a.id}
                aria-pressed={q.detail?.id === a.id}
                onClick={() => (onSelect ? onSelect(a.id) : void q.read(a.id))}
              >
                <span>
                  <strong>{resultName(a)}</strong>
                  <small>{a.restriction_id}</small>
                </span>
              </button>
            ))}
          </div>
          {!q.items.length && <p>{uiText("暂无账户申诉。")}</p>}
          {q.cursor && (
            <button className="text-button" onClick={() => void q.load(true)}>{uiText("读取更早申请")}</button>
          )}
        </aside>
        {q.detail && (
          <article className="appeal-detail">
            <h3>{resultName(q.detail)}</h3>
            <p>{uiText("原限制轮次")} <code>{q.detail.restriction_id}</code>
            </p>
            <p>{uiText("申请原因 ·")} {appealReasons[q.detail.reason_code]}</p>
            {q.detail.statement && <p className="appeal-prose">{q.detail.statement}</p>}
            {q.detail.public_reply && (
              <>
                <h3>{uiText("公开答复")}</h3>
                <p className="appeal-prose">{q.detail.public_reply}</p>
              </>
            )}
          </article>
        )}
      </div>
    </section>
  );
}
type IdentityProps = {
  session: AuthSession;
  onExpired: (s?: string) => void;
  refresh: () => Promise<void>;
  logout: () => void;
  hasUnsaved?: boolean;
};
export function AccountAccessPage({
  session,
  onExpired,
  refresh,
  logout,
  hasUnsaved = false,
}: IdentityProps) {
  const id = session.user!.id,
    io = useAppealIO(recoveryKey('account-owner', id, id), onExpired),
    q = useAccountRecords(id, false, io);
  const [tab, setTab] = useState<'status' | 'security'>('status'),
    [securityBusy, setSecurityBusy] = useState(false),
    [reason, setReason] = useState<AppealReason>('request_review'),
    [statement, setStatement] = useState('');
  const restriction = q.access?.restriction;
  async function checked(i: PendingAppeal, a: AccountAppeal) {
    if (!matchesAccountAppeal(a, id) || a.restriction_id !== i.id)
      throw Error(uiText('回执与本人原限制轮次不匹配。'));
    if (io.alive.current) {
      setStatement('');
      await q.load();
      await q.read(a.id);
    }
  }
  function apply() {
    if (!restriction || !q.access?.can_appeal || io.locked || publicTextError(statement, false)) return;
    const i: PendingAppeal = { action: 'create', id: restriction.id, key: crypto.randomUUID() };
    void io.act(i, async () => checked(i, await governanceApi.apply(i.id, reason, statement.trim(), i.key)));
  }
  function receipt() {
    const i = io.pending;
    if (i?.action === 'create')
      void io.act(i, async () => checked(i, await governanceApi.receipt(i.key)), true);
  }
  const locked = io.busy || securityBusy;
  return (
    <main className={'appeal-page ag-page ' + (session.account_restricted ? 'ag-restricted' : '')}>
      <header className="appeal-hero">
        <Icon name="sliders" size={28} />
        <div>
          <h1>{uiText("账户状态与申诉")}</h1>
          <p>
            {session.account_restricted
              ? uiText('当前会话可查看本人限制、申请复核并管理账户安全。')
              : uiText('查看本人的账户状态、限制轮次和复核答复。')}
          </p>
        </div>
      </header>
      {session.account_restricted && (
        <nav className="ag-nav" aria-label={uiText("受限账户导航")}>
          <button
            className="button secondary"
            disabled={locked}
            aria-pressed={tab === 'status'}
            onClick={() => setTab('status')}
          >{uiText("我的账户状态")}</button>
          <button
            className="button secondary"
            disabled={locked}
            aria-pressed={tab === 'security'}
            onClick={() => setTab('security')}
          >{uiText("账户安全")}</button>
          <button className="text-button" disabled={locked} onClick={logout}>{uiText("退出登录")}</button>
        </nav>
      )}
      {tab === 'security' && session.account_restricted ? (
        <AccountSecurityPage
          session={session}
          visible
          hasUnsaved={hasUnsaved || !!statement}
          onBusy={setSecurityBusy}
          onExpired={onExpired}
          onRefreshIdentity={refresh}
        />
      ) : (
        <>
          <div className="appeal-heading">
            <h2>{uiText("当前账户")}</h2>
            <button
              className="text-button"
              disabled={locked}
              onClick={() => {
                void q.load();
                void refresh();
              }}
            >{uiText("刷新账户状态")}</button>
          </div>
          <ReceiptNotice pending={io.pending} onRead={receipt} busy={io.busy} />
          <AppealError error={io.error} pending={!!io.pending} />
          {q.access && <StatusCard a={q.access} />}
          {q.access?.state === 'SUSPENDED' && q.access.can_appeal && restriction && (
            <form
              className="appeal-compose"
              onSubmit={(e) => {
                e.preventDefault();
                apply();
              }}
            >
              <h2>{uiText("申请复核本轮限制")}</h2>
              <fieldset disabled={io.locked}>
                <label>{uiText("申请原因")}<select value={reason} onChange={(e) => setReason(e.target.value as AppealReason)}>
                    {Object.entries(appealReasons).map(([k, v]) => (
                      <option key={k} value={k}>
                        {v}
                      </option>
                    ))}
                  </select>
                </label>
                <label>{uiText("补充说明")}<textarea
                    maxLength={1024}
                    value={statement}
                    rows={3}
                    onChange={(e) => setStatement(e.target.value)}
                    placeholder={uiText("简述需要核对的事实。请勿填写密码或令牌。")}
                  />
                </label>
                <small>{textBytes(statement)} {uiText("/ 1024 UTF-8 字节 · 仅本人和获授权的账户治理员可读")}</small>
                <button className="button primary" disabled={!!publicTextError(statement, false)}>{uiText("提交账户申诉")}</button>
              </fieldset>
            </form>
          )}
          <History q={q} />
        </>
      )}
    </main>
  );
}

export function AccountGovernancePage({
  owner,
  targets,
  onExpired,
  refreshTargets,
}: {
  owner: string;
  targets: AccountTarget[];
  onExpired: () => void;
  refreshTargets: () => void;
}) {
  const allowed = targets.filter((v) => safeID(v.target_user_id) && v.target_user_id !== owner),
    [preferred, setPreferred] = useState(''),
    [busy, setBusy] = useState(false);
  const target =
    allowed.find((v) => v.target_user_id === preferred)?.target_user_id || allowed[0]?.target_user_id;
  return (
    <section className="appeal-page ag-page">
      <header className="appeal-hero">
        <Icon name="sliders" size={24} />
        <div>
          <h1>{uiText("账户治理")}</h1>
          <p>{uiText("按专门授权处理指定账户的使用限制与申诉。所有处置均绑定当前账户和限制轮次。")}</p>
        </div>
      </header>
      <div className="ag-target">
        <label>{uiText("已授权目标账户")}<select
            disabled={busy || !target}
            value={target || ''}
            onChange={(e) => setPreferred(e.target.value)}
          >
            {allowed.map((v) => (
              <option value={v.target_user_id} key={v.target_user_id}>
                {v.target_user_id}
              </option>
            ))}
          </select>
        </label>
        <button className="text-button" disabled={busy} onClick={refreshTargets}>{uiText("重新核对目标授权")}</button>
      </div>
      {target ? (
        <TargetPanel
          key={owner + ':' + target}
          owner={owner}
          target={target}
          onExpired={onExpired}
          onBusy={setBusy}
        />
      ) : (
        <p>{uiText("当前没有获授权的目标账户。")}</p>
      )}
    </section>
  );
}
function TargetPanel({
  owner,
  target,
  onExpired,
  onBusy,
}: {
  owner: string;
  target: string;
  onExpired: () => void;
  onBusy: (v: boolean) => void;
}) {
  const io = useAppealIO(recoveryKey('account-operator', owner, target), onExpired),
    q = useAccountRecords(target, true, io);
  const [chosen, setChosen] = useState<GovernanceAction | ' '>(' '),
    [reason, setReason] = useState('manual_review'),
    [evidence, setEvidence] = useState(''),
    [reply, setReply] = useState('');
  const [plan, setPlan] = useState<AccountPlan | null>(null),
    [receipt, setReceipt] = useState<AccountReceipt | null>(null),
    [planning, setPlanning] = useState(false),
    [checked, setChecked] = useState(false),
    [hasPassword, setHasPassword] = useState(false);
  const [previewError, setPreviewError] = useState(''),
    pw = useRef<HTMLInputElement>(null),
    active = useRef(0);
  const remaining = useCountdown(plan ? Date.parse(plan.expires_at) : 0, 900),
    acc = q.access,
    round = acc?.restriction?.id;
  const currentAppeal =
    !!round && q.detail?.restriction_id === round && q.detail.state === 'OPEN' ? q.detail : null;
  const available: GovernanceAction[] =
    acc?.state === 'OPEN'
      ? ['suspend']
      : acc?.state === 'SUSPENDED'
        ? currentAppeal
          ? ['uphold', 'lift']
          : ['lift']
        : [];
  const action = available.includes(chosen as GovernanceAction) ? (chosen as GovernanceAction) : available[0];
  const reasons =
    action === 'lift'
      ? ['review_complete', 'restriction_error']
      : ['manual_review', 'policy_concern', 'abuse_concern'];
  const currentReason = reasons.includes(reason) ? reason : reasons[0];
  const valid =
    !!action && !publicTextError(reply, true) && !operationEvidenceError('manual_review', evidence.trim());
  useEffect(() => {
    onBusy(planning || io.busy);
    return () => onBusy(false);
  }, [planning, io.busy, onBusy]);
  function invalidate() {
    ++active.current;
    setPlan(null);
    setChecked(false);
    setHasPassword(false);
    setPreviewError('');
    if (pw.current) pw.current.value = '';
  }
  function select(id: string) {
    if (io.locked || planning) return;
    invalidate();
    setChosen('uphold');
    setReply('');
    setEvidence('');
    void q.read(id);
  }
  async function preview() {
    if (!acc || !action || !valid || io.locked || planning) return;
    invalidate();
    setPlanning(true);
    const t = active.current;
    const input: AccountPlanInput = {
      action,
      expected_version: acc.version,
      reason_code: currentReason,
      evidence_ref: evidence.trim(),
      public_reply: reply.trim(),
    };
    if (action !== 'suspend') input.restriction_id = round;
    if (action === 'uphold' && currentAppeal) {
      input.appeal_id = currentAppeal.id;
      input.expected_appeal_version = currentAppeal.version;
    }
    try {
      const p = await governanceApi.plan(target, input);
      if (!io.alive.current || active.current !== t) return;
      if (
        p.target_user_id !== target ||
        !safeID(p.id) ||
        p.action !== action ||
        p.expected_version !== input.expected_version ||
        p.effect?.from_state !== acc.state ||
        p.effect?.to_state !== (action === 'lift' ? 'OPEN' : 'SUSPENDED') ||
        (!p.effect.restriction_id && action !== 'suspend') ||
        (round && p.effect.restriction_id !== round) ||
        !safeID(p.effect.restriction_id) ||
        p.appeal_id !== input.appeal_id ||
        !p.plan_digest ||
        !(Date.parse(p.expires_at) > Date.now())
      )
        throw Error(uiText('预演与当前目标、轮次或申请不匹配。'));
      setPlan(p);
    } catch (e) {
      if (e instanceof ApiError) io.report(e);
      else setPreviewError(e instanceof Error ? e.message : uiText('预演无法核对。'));
    } finally {
      if (io.alive.current) setPlanning(false);
    }
  }
  async function accept(i: PendingAppeal, result: AccountReceipt) {
    if (
      result.target_user_id !== target ||
      result.plan_id !== i.key ||
      result.action !== i.type ||
      result.effect?.restriction_id !== i.id ||
      !safeID(result.id)
    )
      throw Error(uiText('回执与原计划、目标或轮次不匹配。'));
    if (io.alive.current) {
      invalidate();
      setReceipt(result);
      setReply('');
      setEvidence('');
      await q.load();
      if (q.detail) await q.read(q.detail.id);
    }
  }
  function execute() {
    if (
      !plan ||
      !acc ||
      io.locked ||
      !checked ||
      !pw.current?.value ||
      !remaining ||
      acc.version !== plan.expected_version ||
      plan.target_user_id !== target
    )
      return;
    const password = pw.current.value;
    pw.current.value = '';
    setHasPassword(false);
    const i: PendingAppeal = {
      action: 'execute',
      type: plan.action,
      key: plan.id,
      id: plan.effect.restriction_id,
    };
    void io.act(i, async () => accept(i, await governanceApi.execute(target, plan, password)));
  }
  function readReceipt() {
    const i = io.pending;
    if (i?.action === 'execute')
      void io.act(i, async () => accept(i, await governanceApi.planReceipt(target, i.key)), true);
  }
  return (
    <>
      <ReceiptNotice pending={io.pending} onRead={readReceipt} busy={io.busy} />
      <AppealError error={io.error} pending={!!io.pending} />
      {acc && <StatusCard a={acc} />}
      <button
        className="text-button"
        disabled={io.locked || planning}
        onClick={() => {
          invalidate();
          void q.load();
          if (q.detail) void q.read(q.detail.id);
        }}
      >{uiText("刷新目标与申请")}</button>
      <History q={q} onSelect={select} />
      {receipt && (
        <p className="appeal-notice">{uiText("已核对账户处置收据")} <code>{receipt.id}</code>
        </p>
      )}
      {acc && action && (
        <section className="appeal-compose">
          <h2>{uiText("准备账户处置")}</h2>
          <fieldset disabled={io.locked || planning}>
            <label>{uiText("目标处置")}<select
                value={action}
                onChange={(e) => {
                  invalidate();
                  setChosen(e.target.value as GovernanceAction);
                }}
              >
                {available.map((k) => (
                  <option key={k} value={k}>
                    {accountActions[k]}
                  </option>
                ))}
              </select>
            </label>
            <label>{uiText("原因")}<select
                value={currentReason}
                onChange={(e) => {
                  invalidate();
                  setReason(e.target.value);
                }}
              >
                {reasons.map((k) => (
                  <option key={k} value={k}>
                    {governanceReasons[k]}
                  </option>
                ))}
              </select>
            </label>
            <label>{uiText("内部证据编号")}<input
                aria-label={uiText("账户处置证据编号")}
                maxLength={128}
                value={evidence}
                onChange={(e) => {
                  invalidate();
                  setEvidence(e.target.value);
                }}
              />
            </label>
            <label>{uiText("给目标用户的公开说明")}<textarea
                aria-label={uiText("账户处置公开说明")}
                rows={3}
                maxLength={1024}
                value={reply}
                onChange={(e) => {
                  invalidate();
                  setReply(e.target.value);
                }}
              />
            </label>
            <button className="button secondary" disabled={!valid} onClick={preview}>{uiText("预演账户处置")}</button>
          </fieldset>
          {previewError && <p role="alert">{previewError}</p>}
          {plan && (
            <div className="appeal-preview">
              <h3>{uiText("确认账户处置")}</h3>
              <p>
                {stateName(plan.effect.from_state)} → {stateName(plan.effect.to_state)} {uiText("· 涉及")}{' '}
                {plan.effect.affected_appeals} {uiText("项申请")}</p>
              <small>{uiText("目标")} <code>{target}</code> {uiText("· 轮次")} <code>{plan.effect.restriction_id}</code>
              </small>
              <p className="appeal-prose">{reply}</p>
              <small>{uiText("预演剩余")} {remaining} {uiText("秒")}</small>
              <label className="appeal-check">
                <input
                  type="checkbox"
                  disabled={io.locked}
                  checked={checked}
                  onChange={(e) => setChecked(e.target.checked)}
                />{uiText("已核对指定账户、限制轮次和公开说明")}</label>
              <label>{uiText("当前治理员密码")}<input
                  type="password"
                  autoComplete="current-password"
                  maxLength={72}
                  ref={pw}
                  disabled={io.locked}
                  onChange={(e) => setHasPassword(!!e.target.value)}
                />
              </label>
              <button
                className="button primary"
                disabled={io.locked || !checked || !hasPassword || !remaining}
                onClick={execute}
              >{uiText("确认执行账户处置")}</button>
            </div>
          )}
        </section>
      )}
    </>
  );
}
