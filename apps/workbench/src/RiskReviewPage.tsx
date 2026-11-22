import { uiText, currentLocale } from './locale';
import { useEffect, useRef, useState } from 'react';
import { ApiError, request } from './api';
import { formatCredits } from './billing';
import { useCountdown } from './useCountdown';
import { AppealError, ReceiptNotice, useAppealIO } from './ContentAppealPage';
import { recoveryKey, safeID, textBytes } from './contentAppeals';
import type { PendingAppeal } from './contentAppeals';
import { operationEvidenceError } from './operations';
import { appealReasons, evidenceMatches, riskApi, riskNames } from './riskReview';
import type { RiskAppeal, RiskEvidence, RiskAction, RiskPlan, RiskReceipt } from './riskReview';
import { claimRisk, riskOriginal, riskResult } from './riskOriginal';
import './content-appeals.css';

type Scope = { owner: string; workspace: string; project: string };
const statuses = { get OPEN() { return uiText('待复核'); }, get APPROVED() { return uiText('已批准'); }, get DENIED() { return uiText('已拒绝'); } };
type IO = ReturnType<typeof useAppealIO>;
function useRecords(w: string, p: string | null, io: IO) {
  const mode = p ? 'user' : 'ops',
    id = p || w;
  const [rows, setRows] = useState<RiskAppeal[]>([]),
    [cursor, setCursor] = useState<string | null>(null),
    [detail, setDetail] = useState<RiskAppeal | null>(null);
  const tickets = useRef({ list: 0, detail: 0 });
  async function list(more = false) {
    const t = ++tickets.current.list;
    try {
      const page = await riskApi.list(mode, id, more ? cursor : null);
      if (!io.alive.current || t !== tickets.current.list) return;
      if (
        !Array.isArray(page.items) ||
        page.items.length > 50 ||
        !page.items.every(
          (v) => evidenceMatches(v, w, p || undefined) && safeID(v.id) && Object.hasOwn(statuses, v.state),
        )
      )
        throw Error(uiText('风控复核队列与当前范围不符。'));
      setRows((old) =>
        [...new Map([...(more ? old : []), ...page.items].map((v) => [v.id, v])).values()].slice(0, 200),
      );
      setCursor(page.next_cursor || null);
    } catch (e) {
      if (io.alive.current && t === tickets.current.list) {
        setRows([]);
        setDetail(null);
        io.report(e);
      }
    }
  }
  async function read(aid: string) {
    const t = ++tickets.current.detail;
    setDetail(null);
    try {
      const value = await riskApi.detail(mode, id, aid);
      if (!io.alive.current || t !== tickets.current.detail) return;
      if (
        !evidenceMatches(value, w, p || undefined) ||
        value.id !== aid ||
        !Object.hasOwn(statuses, value.state)
      )
        throw Error(uiText('风控申请与当前范围不匹配。'));
      setDetail(value);
    } catch (e) {
      if (t === tickets.current.detail) io.report(e);
    }
  }
  useEffect(() => {
    void list();
    return () => {
      ++tickets.current.detail;
      ++tickets.current.list;
    };
  }, [w, p]);
  return { rows, detail, cursor: rows.length < 200 ? cursor : null, list, read };
}
type Records = ReturnType<typeof useRecords>;
function Evidence({ value }: { value: RiskEvidence | RiskAppeal }) {
  return (
    <dl>
      <dt>{uiText("原报价")}</dt>
      <dd>
        <code>{value.quote_id}</code>
      </dd>
      <dt>{uiText("工作区")}</dt>
      <dd>
        <code>{value.workspace_id}</code>
      </dd>
      <dt>{uiText("项目")}</dt>
      <dd>
        <code>{value.project_id}</code>
      </dd>
      <dt>{uiText("拟冻结")}</dt>
      <dd>{formatCredits(value.amount_micros)} {uiText("创作点")}</dd>
      <dt>{uiText("规则")}</dt>
      <dd>
        {value.reason_codes.map((k) => riskNames[k]).join('、') || uiText('当前未达到限制')} · v{value.policy_version}
      </dd>
      <dt>{uiText("报价到期")}</dt>
      <dd>{new Date(value.expires_at).toLocaleString(currentLocale())}</dd>
    </dl>
  );
}
function List({ q, select }: { q: Records; select: (id: string) => void }) {
  return (
    <aside>
      <div className="appeal-heading">
        <h2>{uiText("复核记录")}</h2>
        <button
          className="text-button"
          onClick={() => {
            void q.list();
            if (q.detail) select(q.detail.id);
          }}
        >{uiText("刷新复核")}</button>
      </div>
      <div className="appeal-rows">
        {q.rows.map((v) => (
          <button key={v.id} aria-pressed={q.detail?.id === v.id} onClick={() => select(v.id)}>
            <span>
              <strong>
                {statuses[v.state]} · {formatCredits(v.amount_micros)} {uiText("创作点")}</strong>
              <small>{v.quote_id}</small>
            </span>
          </button>
        ))}
      </div>
      {!q.rows.length && <p>{uiText("暂无可见风控申请。")}</p>}
      {q.cursor && (
        <button className="text-button" onClick={() => void q.list(true)}>{uiText("读取更早申请")}</button>
      )}
    </aside>
  );
}
function Detail({ value }: { value: RiskAppeal }) {
  return (
    <section className="appeal-detail">
      <h2>{uiText("风控复核详情")}</h2>
      <p>
        {statuses[value.state]} · <code>{value.id}</code>
      </p>
      <h3>{uiText("原报价与规则")}</h3>
      <Evidence value={value} />
      <h3>{uiText("本人申请")}</h3>
      <p>{appealReasons[value.reason_code]}</p>
      {value.statement && <p className="appeal-prose">{value.statement}</p>}
      {value.public_reply && (
        <>
          <h3>{uiText("公开答复")}</h3>
          <p className="appeal-prose">{value.public_reply}</p>
        </>
      )}
    </section>
  );
}
function OriginalRetry({ s, a, io, refresh }: { s: Scope; a: RiskAppeal; io: IO; refresh: () => void }) {
  const i = riskOriginal(s, a.quote_id),
    seconds = useCountdown(Date.parse(a.expires_at), 900),
    [busy, setBusy] = useState(false),
    [message, setMessage] = useState('');
  const allow =
    a.state === 'APPROVED' && a.exemption_state === 'AVAILABLE' && seconds > 0 && i?.phase === 'blocked';
  async function send() {
    if (busy || !allow || Date.now() >= Date.parse(a.expires_at)) return;
    const original = claimRisk(s, a.quote_id);
    if (!original) return;
    setBusy(true);
    try {
      const result = await request<{ id: string; project_id: string }>(original.path, {
        method: 'POST',
        headers: {
          'Idempotency-Key': original.key,
          ...(original.projectHeader ? { 'X-Project-ID': original.projectHeader } : {}),
        },
        body: original.body,
      });
      if (!safeID(result.id) || result.project_id !== s.project)
        throw new ApiError(uiText('原请求回执无法核对。'), 200, true);
      riskResult(original, 'accepted', result.id);
      if (io.alive.current) {
        setMessage(uiText('原请求已受理'));
        refresh();
      }
    } catch (e) {
      const unknown = !(e instanceof ApiError) || e.uncertain;
      riskResult(original, unknown ? 'unknown' : 'blocked');
      if (io.alive.current) io.report(e);
    } finally {
      if (io.alive.current) setBusy(false);
    }
  }
  if (a.state !== 'APPROVED') return null;
  return (
    <div className="appeal-preview">
      <h3>{uiText("原报价的单次许可")}</h3>
      {i?.phase === 'accepted' ? (
        <p>{uiText("原请求已受理 ·")} <code>{i.result}</code>
        </p>
      ) : a.exemption_state === 'INACTIVE' ? (
        <p>{uiText("原许可已失效，请核对最新规则并重新报价。")}</p>
      ) : a.exemption_state === 'CONSUMED' ? (
        <p>{uiText("许可已消费，请在原项目历史核对受理结果。")}</p>
      ) : i?.phase === 'unknown' ? (
        <p>{uiText("生成回执待核对。请读取项目历史和原报价的许可状态。")}</p>
      ) : !seconds || a.exemption_state === 'EXPIRED' ? (
        <p>{uiText("原报价已到期。请返回创作入口重新获取报价并确认。")}</p>
      ) : !i ? (
        <p>{uiText("此页面没有原提交正文。可回到保留原请求的页面核对；如已丢失，请重新报价。")}</p>
      ) : (
        <>
          <p>{uiText("仅原请求可使用此许可，剩余")} {seconds} {uiText("秒。提交后按原报价核验并冻结。")}</p>
          <button className="button primary" disabled={!allow || busy || io.locked} onClick={send}>{uiText("按原报价确认提交原请求")}</button>
        </>
      )}
      {message && i?.phase !== 'accepted' && <p>{message}</p>}
    </div>
  );
}
export function RiskReviewPage({
  owner,
  workspace,
  project,
  quote,
  onExpired,
}: {
  owner: string;
  workspace: string;
  project: string;
  quote: string;
  onExpired: () => void;
}) {
  const scope = { owner, workspace, project },
    io = useAppealIO(recoveryKey('risk-user', owner, workspace, project), onExpired),
    q = useRecords(workspace, project, io);
  const [proof, setProof] = useState<RiskEvidence | null>(null),
    [reason, setReason] = useState<keyof typeof appealReasons>('request_review'),
    [statement, setStatement] = useState(''),
    [info, setInfo] = useState('');
  const ticket = useRef(0),
    remaining = useCountdown(proof ? Date.parse(proof.expires_at) : 0, 900);
  async function evidence() {
    const t = ++ticket.current;
    setProof(null);
    setInfo('');
    if (!quote) return;
    try {
      const v = await riskApi.eligibility(project, quote);
      if (!io.alive.current || t !== ticket.current) return;
      if (!evidenceMatches(v, workspace, project, quote)) throw Error(uiText('服务端返回的风控范围与原报价不符。'));
      setProof(v);
      if (v.appeal_id) void q.read(v.appeal_id);
    } catch (e) {
      if (!io.alive.current || t !== ticket.current) return;
      if (e instanceof ApiError && ['QUOTE_EXPIRED', 'QUOTE_USED'].includes(e.code || ''))
        setInfo(uiText('原报价已到期或已使用。可从复核历史核对，新的生成请重新报价。'));
      else io.report(e);
    }
  }
  useEffect(() => {
    void evidence();
    return () => {
      ++ticket.current;
    };
  }, [quote, workspace, project]);
  async function handle(i: PendingAppeal, v: RiskAppeal) {
    if (!evidenceMatches(v, workspace, project, i.id) || !safeID(v.id))
      throw Error(uiText('申请回执与原报价不匹配。'));
    if (io.alive.current) {
      setStatement('');
      setProof(null);
      await q.read(v.id);
      await q.list();
    }
  }
  function apply() {
    if (
      !proof?.allowed_to_appeal ||
      !proof.reason_codes.length ||
      io.locked ||
      !remaining ||
      textBytes(statement) > 1024
    )
      return;
    const i: PendingAppeal = { action: 'create', id: proof.quote_id, key: crypto.randomUUID() };
    void io.act(i, async () =>
      handle(i, await riskApi.apply(project, i.id, reason, statement.trim(), i.key)),
    );
  }
  function receipt() {
    const i = io.pending;
    if (i?.action === 'create')
      void io.act(i, async () => handle(i, await riskApi.receipt(project, i.key)), true);
  }
  return (
    <section className="appeal-page risk-page">
      <header className="appeal-hero">
        <div>
          <h1>{uiText("额度风控复核")}</h1>
          <p>{uiText("核对原报价命中的工作区冻结额度或分钟频率限制，并查看人工复核结果。")}</p>
        </div>
      </header>
      <ReceiptNotice pending={io.pending} busy={io.busy} onRead={receipt} />
      <AppealError pending={!!io.pending} error={io.error} />
      {info && <p className="appeal-notice">{info}</p>}
      {quote && !proof && !info && !q.detail && (
        <button className="button secondary" onClick={evidence}>{uiText("核对原报价限制")}</button>
      )}
      {proof && (
        <section className="appeal-compose">
          <h2>{uiText("本次原报价")}</h2>
          <Evidence value={proof} />
          {proof.allowed_to_appeal && proof.reason_codes.length > 0 && remaining > 0 ? (
            <fieldset disabled={io.locked}>
              <label>{uiText("申请原因")}<select
                  value={reason}
                  onChange={(e) => setReason(e.target.value as keyof typeof appealReasons)}
                >
                  {Object.entries(appealReasons).map(([k, v]) => (
                    <option key={k} value={k}>
                      {v}
                    </option>
                  ))}
                </select>
              </label>
              <label>{uiText("复核说明")}<textarea
                  rows={2}
                  maxLength={1024}
                  value={statement}
                  onChange={(e) => setStatement(e.target.value)}
                  placeholder={uiText("说明需要复核的额度或使用情形，请勿填写提示词或凭据。")}
                />
              </label>
              <button className="button primary" disabled={textBytes(statement) > 1024} onClick={apply}>{uiText("提交额度风控复核")}</button>
            </fieldset>
          ) : (
            <p>{remaining ? uiText('当前报价请查阅复核记录。') : uiText('原报价已到期，请重新报价。')}</p>
          )}
        </section>
      )}
      <div className="appeal-columns">
        <List q={q} select={q.read} />
        <div>
          {q.detail && (
            <>
              <Detail value={q.detail} />
              <OriginalRetry
                key={q.detail.id}
                s={scope}
                a={q.detail}
                io={io}
                refresh={() => {
                  if (q.detail) void q.read(q.detail.id);
                }}
              />
            </>
          )}
        </div>
      </div>
    </section>
  );
}

export function RiskReviewOperations({
  owner,
  workspace,
  onExpired,
  onBusy,
}: {
  owner: string;
  workspace: string;
  onExpired: () => void;
  onBusy: (v: boolean) => void;
}) {
  const io = useAppealIO(recoveryKey('risk-ops', owner, workspace), onExpired),
    q = useRecords(workspace, null, io);
  const [action, setAction] = useState<RiskAction>('deny'),
    [reply, setReply] = useState(''),
    [evidence, setEvidence] = useState(''),
    [plan, setPlan] = useState<RiskPlan | null>(null),
    [confirmed, setConfirmed] = useState(false),
    [hasPassword, setHasPassword] = useState(false),
    [planning, setPlanning] = useState(false),
    [receipt, setReceipt] = useState('');
  const pw = useRef<HTMLInputElement>(null),
    ticket = useRef(0),
    cb = useRef(onBusy);
  cb.current = onBusy;
  const seconds = useCountdown(plan ? Date.parse(plan.expires_at) : 0, 900);
  useEffect(() => {
    cb.current(planning || io.busy);
    return () => cb.current(false);
  }, [planning, io.busy]);
  function clear() {
    ++ticket.current;
    setPlan(null);
    setConfirmed(false);
    setHasPassword(false);
    if (pw.current) pw.current.value = '';
  }
  function select(id: string) {
    if (planning || io.locked) return;
    clear();
    setReply('');
    setEvidence('');
    setReceipt('');
    void q.read(id);
  }
  const valid =
    !!reply.trim() && textBytes(reply) <= 1024 && !operationEvidenceError('manual_review', evidence.trim());
  async function preview() {
    const a = q.detail;
    if (!a || a.state !== 'OPEN' || io.locked || planning || !valid) return;
    clear();
    setPlanning(true);
    const t = ticket.current;
    try {
      const p = await riskApi.preview(workspace, a.id, a.version, action, evidence.trim(), reply.trim());
      if (!io.alive.current || t !== ticket.current) return;
      if (
        p.workspace_id !== workspace ||
        p.appeal_id !== a.id ||
        p.action !== action ||
        p.expected_version !== a.version ||
        p.outcome !== (action === 'approve' ? 'APPROVED' : 'DENIED') ||
        !safeID(p.id) ||
        !p.plan_digest ||
        !(Date.parse(p.expires_at) > Date.now())
      )
        throw Error(uiText('预演与当前工作区、申请或版本不匹配。'));
      setPlan(p);
    } catch (e) {
      io.report(e);
    } finally {
      if (io.alive.current) setPlanning(false);
    }
  }
  async function handle(i: PendingAppeal, r: RiskReceipt) {
    if (
      r.workspace_id !== workspace ||
      r.appeal_id !== i.id ||
      r.plan_id !== i.key ||
      r.action !== i.type ||
      r.version !== (i.version || 0) + 1 ||
      !safeID(r.id) ||
      r.outcome !== (r.action === 'approve' ? 'APPROVED' : 'DENIED')
    )
      throw Error(uiText('回执与原风控复核计划不匹配。'));
    if (io.alive.current) {
      clear();
      setReceipt(r.id);
      await q.read(i.id);
      await q.list();
    }
  }
  function execute() {
    if (
      !plan ||
      !q.detail ||
      !confirmed ||
      !pw.current?.value ||
      io.locked ||
      !seconds ||
      q.detail.id !== plan.appeal_id ||
      q.detail.version !== plan.expected_version
    )
      return;
    const password = pw.current.value;
    pw.current.value = '';
    setHasPassword(false);
    const i: PendingAppeal = {
      action: 'execute',
      key: plan.id,
      id: plan.appeal_id,
      type: plan.action,
      version: plan.expected_version,
    };
    void io.act(i, async () => handle(i, await riskApi.execute(plan, password)));
  }
  function readReceipt() {
    const i = io.pending;
    if (i?.action === 'execute')
      void io.act(i, async () => handle(i, await riskApi.opsReceipt(workspace, i.id, i.key)), true);
  }
  return (
    <section className="appeal-page risk-page">
      <header className="appeal-hero">
        <div>
          <h2>{uiText("额度风控审议")}</h2>
          <p>{uiText("按工作区授权复核实际受限报价。批准后，由请求人决定是否在原报价到期前提交。")}</p>
        </div>
      </header>
      <ReceiptNotice pending={io.pending} busy={io.busy} onRead={readReceipt} />
      <AppealError error={io.error} pending={!!io.pending} />
      <div className="appeal-columns">
        <List q={q} select={select} />
        <div>
          {q.detail && <Detail value={q.detail} />}
          {receipt && (
            <p className="appeal-notice">{uiText("已核对风控处置收据")} <code>{receipt}</code>
            </p>
          )}
          {q.detail?.state === 'OPEN' && (
            <section className="appeal-compose">
              <h3>{uiText("准备风控处置")}</h3>
              <fieldset disabled={io.locked || planning}>
                <label>{uiText("复核结论")}<select
                    value={action}
                    onChange={(e) => {
                      clear();
                      setAction(e.target.value as RiskAction);
                    }}
                  >
                    <option value="deny">{uiText("拒绝原报价豁免")}</option>
                    <option value="approve">{uiText("批准原报价单次许可")}</option>
                  </select>
                </label>
                <label>{uiText("内部证据编号")}<input
                    maxLength={128}
                    value={evidence}
                    onChange={(e) => {
                      clear();
                      setEvidence(e.target.value);
                    }}
                  />
                </label>
                <label>{uiText("给申请人的公开说明")}<textarea
                    rows={3}
                    maxLength={1024}
                    value={reply}
                    onChange={(e) => {
                      clear();
                      setReply(e.target.value);
                    }}
                  />
                </label>
                <button className="button secondary" disabled={!valid} onClick={preview}>{uiText("预演额度风控处置")}</button>
              </fieldset>
              {plan && (
                <div className="appeal-preview">
                  <h3>{uiText("确认本次处置")}</h3>
                  <p>
                    {plan.outcome === 'APPROVED'
                      ? uiText('原报价取得一次许可，仍需本人手动提交。')
                      : uiText('拒绝原报价豁免。')}
                  </p>
                  <p>{reply}</p>
                  <small>{uiText("剩余")} {seconds} {uiText("秒")}</small>
                  <label className="appeal-check">
                    <input
                      type="checkbox"
                      checked={confirmed}
                      disabled={io.locked}
                      onChange={(e) => setConfirmed(e.target.checked)}
                    />{uiText("已核对工作区、原报价与公开结论")}</label>
                  <label>{uiText("当前风控复核员密码")}<input
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
                    disabled={!confirmed || !hasPassword || !seconds || io.locked}
                    onClick={execute}
                  >{uiText("确认执行风控处置")}</button>
                </div>
              )}
            </section>
          )}
        </div>
      </div>
    </section>
  );
}
