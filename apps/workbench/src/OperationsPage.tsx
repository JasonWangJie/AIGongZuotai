import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { api, ApiError } from './api';
import { Icon } from './components';
import { formatCredits, billingStateName } from './billing';
import { useCountdown } from './useCountdown';
import { statusName } from './types';
import { allowedOperation, mergeOperationPage, OperationPassword, operationActions, operationCaseMatches, operationEventName, operationEvidenceError, operationPlanMatches, operationQuery, operationReasons, operationRoles, operationScopes, operationUsage, safeOperationError } from './operations';
import type { OperationAccess, OperationAction, OperationCase, OperationCaseInput, OperationEvent, OperationException, OperationPlan, OperationPlanInput, OperationReason, OperationsRecovery, OperationScope, OperationUnknown } from './operations';
import './operations.css';
import { OperationsGenerationPanel } from './OperationsGenerationPanel';
import { GenerationRecovery } from './generationControls';

function stamp(value: string) { const date = new Date(value); return Number.isNaN(date.valueOf()) ? '时间待核对' : date.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }); }
function resourceName(value: 'run' | 'task') { return value === 'run' ? 'LLM 运行' : '媒体任务'; }
function OpsError({ error, unknown }: { error: ApiError | null; unknown?: boolean }) { if (!error) return null; return <div className={'ops-error ' + (unknown ? 'ops-unknown' : '')} role="alert"><p>{error.message}</p>{error.requestId && <code>请求编号 · {error.requestId}</code>}</div>; }

export function OperationsPage({ owner, access, recovery, onExpired, onBusy, onRefreshAccess, generationRecovery, onGenerationChanged }: { owner: string; access: OperationAccess; recovery: OperationsRecovery; onExpired: () => void; onBusy: (value: boolean) => void; onRefreshAccess: () => void; generationRecovery?: GenerationRecovery; onGenerationChanged?: () => void }) {
  const localGenerationRecovery = useRef(new GenerationRecovery()), generationStore = generationRecovery || localGenerationRecovery.current;
  const scopes = operationScopes(access), [preferred, setPreferred] = useState(''), [busy, setBusy] = useState(false);
  const [nextScope, setNextScope] = useState(''), scopeDialog = useRef<HTMLDialogElement>(null);
  const scope = scopes.find(value => value.id === preferred) || scopes[0];
  useEffect(() => { if (nextScope) scopeDialog.current?.showModal(); else scopeDialog.current?.close(); }, [nextScope]);
  function busyChanged(value: boolean) { setBusy(value); onBusy(value); }
  return <section className="operations-page" aria-label="运营异常核实台"><header className="ops-heading"><div><span className="eyebrow">XINGTU / OPERATIONS DESK</span><h1>让每一次处置，<br/>都有据可循。</h1><p>从异常到证据，从预演到确认。在授权工作区核对任务、冻结额度与新生成权限，所有处置留下记录。</p></div><div className="ops-stamp">CONTROL ROOM<small>SCOPED · TRACEABLE</small></div></header>
    {!scope ? <div className="ops-scope-empty"><Icon name="sliders" size={28}/><h2>当前没有可用运营权限</h2><p>此入口需要服务端授予的工作区角色。普通创作账户不会自动获得权限。</p><button className="button secondary" onClick={onRefreshAccess}>重新核对权限</button></div> : <><div className="ops-scope"><label><span>AUTHORIZED WORKSPACE / 授权范围</span><select aria-label="运营工作区" value={scope.id} disabled={busy} onChange={event => { if (recovery.get(owner, scope.id) || generationStore.ops(owner, scope.id) || generationStore.opsDraft(owner, scope.id)) setNextScope(event.target.value); else setPreferred(event.target.value); }}>{scopes.map(value => <option key={value.id} value={value.id}>{value.id}</option>)}</select></label><div className="ops-roles" aria-label="当前运营角色">{scope.roles.map(role => <span className="ops-role" key={role}>{operationRoles[role]}</span>)}</div><p className="ops-scope-note">这里的范围独立于创作项目。权限以每次服务端核验为准。</p><button className="button secondary" disabled={busy} onClick={onRefreshAccess}><Icon name="refresh" size={13}/>核对权限</button></div><OperationsPanels key={owner + ':' + scope.id} owner={owner} scope={scope} recovery={recovery} generationRecovery={generationStore} onExpired={onExpired} onBusy={busyChanged} onChanged={onGenerationChanged}/></>}
    <dialog ref={scopeDialog} className="draft-leave-dialog" aria-labelledby="ops-scope-leave-title" onCancel={event => { event.preventDefault(); setNextScope(''); }}><span className="eyebrow">SCOPE / AWAITING CONFIRMATION</span><h2 id="ops-scope-leave-title">原工作区还有待核对处置</h2><p>切换不会撤回已发送操作。密码会清空，核对编号仅保留在当前页面内存；返回原工作区后只刷新工单核对。</p><div><button className="button primary" autoFocus onClick={() => setNextScope('')}>留在原工作区</button><button className="button secondary" onClick={() => { setPreferred(nextScope); setNextScope(''); }}>切换，保留核对编号</button></div></dialog>
  </section>;
}

function OperationsPanels({ owner, scope, recovery, generationRecovery, onExpired, onBusy, onChanged }: { owner: string; scope: OperationScope; recovery: OperationsRecovery; generationRecovery: GenerationRecovery; onExpired: () => void; onBusy: (value: boolean) => void; onChanged?: () => void }) {
  const generation = scope.permissions.includes('generation_restrict') || scope.permissions.includes('appeal_resolve'), financial = scope.roles.some(role => role !== 'moderator');
  const [tab, setTab] = useState<'resources' | 'generation'>(financial ? 'resources' : 'generation'), [busy, setBusy] = useState(false);
  const showGeneration = generation && (!financial || tab === 'generation');
  function select(next: 'resources' | 'generation') { if (busy || next === tab) return; if ((recovery.get(owner, scope.id) || generationRecovery.ops(owner, scope.id) || generationRecovery.opsDraft(owner, scope.id)) && !window.confirm('当前还有待核对操作或未提交说明。切换后在本页内存保留，密码清空；继续切换？')) return; setTab(next); }
  const busyChanged = (value: boolean) => { setBusy(value); onBusy(value); };
  return <>{financial && generation && <nav className="generation-panel-tabs" aria-label="运营业务区域"><button className={showGeneration ? '' : 'active'} disabled={busy} onClick={() => select('resources')}>任务与冻结核实</button><button className={showGeneration ? 'active' : ''} disabled={busy} onClick={() => select('generation')}>新生成限制与复核</button></nav>}{showGeneration ? <OperationsGenerationPanel owner={owner} scope={scope} recovery={generationRecovery} onExpired={onExpired} onBusy={busyChanged} onChanged={onChanged}/> : financial ? <OperationsWorkspace owner={owner} scope={scope} recovery={recovery} onExpired={onExpired} onBusy={busyChanged}/> : <p className="generation-note">当前没有明确授予的处置权限。请重新核对授权。</p>}</>;
}

function OperationsWorkspace({ owner, scope, recovery, onExpired, onBusy }: { owner: string; scope: OperationScope; recovery: OperationsRecovery; onExpired: () => void; onBusy: (value: boolean) => void }) {
  const [tab, setTab] = useState<'exceptions' | 'cases'>('exceptions'), [kind, setKind] = useState('all'), [caseState, setCaseState] = useState('all');
  const [exceptions, setExceptions] = useState<OperationException[]>([]), [cases, setCases] = useState<OperationCase[]>([]), [cursor, setCursor] = useState<string | null>(null);
  const [selected, setSelected] = useState<OperationException | null>(null), [docket, setDocket] = useState<OperationCase | null>(null), [plan, setPlan] = useState<OperationPlan | null>(null);
  const [events, setEvents] = useState<OperationEvent[]>([]), [eventsCursor, setEventsCursor] = useState<string | null>(null), [eventsLoading, setEventsLoading] = useState(false);
  const [reason, setReason] = useState<OperationReason>('manual_review'), [evidence, setEvidence] = useState(''), [action, setAction] = useState<OperationAction>('no_action');
  const [inputTokens, setInputTokens] = useState(''), [outputTokens, setOutputTokens] = useState(''), [totalTokens, setTotalTokens] = useState(''), [units, setUnits] = useState('');
  const [evidenceConfirmed, setEvidenceConfirmed] = useState(false), [executeConfirmed, setExecuteConfirmed] = useState(false), [passwordPresent, setPasswordPresent] = useState(false);
  const [unknown, setUnknown] = useState<OperationUnknown | null>(() => recovery.get(owner, scope.id)), [busy, setBusy] = useState(false), [loading, setLoading] = useState(false), [detailLoading, setDetailLoading] = useState(false);
  const [error, setError] = useState<ApiError | null>(null), [localError, setLocalError] = useState(''), [message, setMessage] = useState('');
  const alive = useRef(true), listGeneration = useRef(0), detailGeneration = useRef(0), listAbort = useRef<AbortController | null>(null), detailAbort = useRef<AbortController | null>(null), mutationLock = useRef(false), readLock = useRef(false);
  const password = useRef(new OperationPassword()), passwordInput = useRef<HTMLInputElement>(null), detailRef = useRef<HTMLElement>(null), callbacks = useRef({ onExpired, onBusy }); callbacks.current = { onExpired, onBusy };
  const seconds = useCountdown(error?.retryAt || 0), planSeconds = useCountdown(plan ? Date.parse(plan.expires_at) : 0), resource = docket?.resource || selected;
  const rows = tab === 'exceptions' ? exceptions : cases, canOpen = scope.permissions.includes('case_open'), financial = action === 'charge' || action === 'release';
  const actions = (Object.keys(operationActions) as OperationAction[]).filter(value => allowedOperation(scope, value) && (value === 'no_action' || value === 'reconcile' ? value === 'no_action' || resource?.resource_type === 'task' && resource.can_reconcile : resource?.billing?.state === 'REVIEW'));
  function clearPassword() { password.current.clear(); if (passwordInput.current) passwordInput.current.value = ''; setPasswordPresent(false); setExecuteConfirmed(false); }
  function invalidatePlan() { setPlan(null); clearPassword(); setLocalError(''); setMessage(''); }
  function clearUnknown(attempt: symbol | undefined) { const cleared = recovery.clearIfMatches(owner, scope.id, attempt); setUnknown(recovery.get(owner, scope.id)); return cleared; }
  function remember(value: OperationUnknown) { const attempt = recovery.remember(owner, scope.id, value); setUnknown(value); return attempt; }
  function fail(cause: unknown) { const issue = safeOperationError(cause); setError(issue); if (issue.status === 401 && issue.code !== 'INVALID_CREDENTIALS') callbacks.current.onExpired(); return issue; }
  useEffect(() => { alive.current = true; return () => { alive.current = false; listGeneration.current++; detailGeneration.current++; listAbort.current?.abort(); detailAbort.current?.abort(); password.current.clear(); callbacks.current.onBusy(false); }; }, []);
  useEffect(() => { const warn = (event: BeforeUnloadEvent) => { if (mutationLock.current || recovery.get(owner, scope.id)) { event.preventDefault(); event.returnValue = ''; } }; window.addEventListener('beforeunload', warn); return () => window.removeEventListener('beforeunload', warn); }, [owner, scope.id, recovery]);
  useEffect(() => { if (!actions.includes(action)) { setAction(actions[0] || 'no_action'); invalidatePlan(); } }, [scope.permissions.join(','), resource?.id, resource?.billing?.state, resource?.can_reconcile]);

  async function load(more = false) {
    if (mutationLock.current || readLock.current || seconds > 0 || more && (!cursor || rows.length >= 200)) return;
    readLock.current = true; const ticket = ++listGeneration.current; listAbort.current?.abort(); const abort = new AbortController(); listAbort.current = abort; setLoading(true); setError(null);
    try {
      const query = operationQuery(scope.id, tab === 'exceptions' ? { kind } : { state: caseState }, more ? cursor : null);
      if (tab === 'exceptions') {
        const page = await api.operationExceptions(query, abort.signal);
        if (!alive.current || ticket !== listGeneration.current) return;
        if (!Array.isArray(page.items) || page.items.length > 50 || page.items.some(value => value.workspace_id !== scope.id)) throw new ApiError('队列响应范围不匹配。', 200);
        setExceptions(old => more ? mergeOperationPage(old, page.items) : page.items); setCursor(page.next_cursor);
      } else {
        const page = await api.operationCases(query, abort.signal);
        if (!alive.current || ticket !== listGeneration.current) return;
        if (!Array.isArray(page.items) || page.items.length > 50 || page.items.some(value => !operationCaseMatches(value, scope.id))) throw new ApiError('工单响应范围不匹配。', 200);
        setCases(old => more ? mergeOperationPage(old, page.items) : page.items); setCursor(page.next_cursor);
      }
    } catch (cause) { if (alive.current && ticket === listGeneration.current) fail(cause); }
    finally { if (alive.current && ticket === listGeneration.current) { readLock.current = false; setLoading(false); } }
  }
  useEffect(() => { listGeneration.current++; listAbort.current?.abort(); readLock.current = false; setCursor(null); setExceptions([]); setCases([]); void load(); }, [tab, kind, caseState]);

  async function inspect(id: string, checking = false) {
    if (mutationLock.current || seconds > 0) return;
    const pending = recovery.get(owner, scope.id), pendingAttempt = recovery.attempt(owner, scope.id);
    const ticket = ++detailGeneration.current; detailAbort.current?.abort(); const abort = new AbortController(); detailAbort.current = abort; setDetailLoading(true); setError(null); invalidatePlan();
    try {
      const [value, timeline] = await Promise.all([api.operationCase(scope.id, id, abort.signal), api.operationEvents(scope.id, id, null, abort.signal)]);
      if (!alive.current || ticket !== detailGeneration.current) return;
      if (!operationCaseMatches(value, scope.id, id) || value.resource && value.resource.workspace_id !== scope.id) throw new ApiError('工单响应范围不匹配。', 200);
      if (!Array.isArray(timeline.items) || timeline.items.length > 50) throw new ApiError('审计响应无法确认。', 200);
      setEvents(timeline.items); setEventsCursor(timeline.next_cursor);
      setDocket(value); setSelected(value.resource || null); setReason(value.reason); setEvidence(value.evidence_ref); setEvidenceConfirmed(false); setInputTokens(''); setOutputTokens(''); setTotalTokens(''); setUnits('');
      if (pending?.kind === 'execute' && value.receipt?.plan_id === pending.planID && clearUnknown(pendingAttempt)) { setMessage('已从服务端收据确认处置结果。未再次提交操作。'); }
      else if (pending?.kind === 'plan' && pending.caseID === value.id && clearUnknown(pendingAttempt)) { setMessage('工单状态已核对。上次预演没有自动执行；如需继续，请手动重新预演。'); }
      else if (pending?.kind === 'case_create' && pending.resourceID === value.resource_id && pending.resourceType === value.resource_type && clearUnknown(pendingAttempt)) { setMessage('已选择此资源的关联工单。没有再次开单，请核对其证据与状态。'); }
      else if (checking && pending) setMessage('当前仍未找到匹配的执行收据。保持待确认，请稍后再次核对。');
      detailRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    } catch (cause) { if (alive.current && ticket === detailGeneration.current) fail(cause); }
    finally { if (alive.current && ticket === detailGeneration.current) setDetailLoading(false); }
  }
  function selectException(value: OperationException) { if (busy || unknown) return; if (value.open_case_id) { void inspect(value.open_case_id); return; } detailGeneration.current++; detailAbort.current?.abort(); setDetailLoading(false); setSelected(value); setDocket(null); setEvents([]); setEventsCursor(null); setReason('manual_review'); setEvidence(''); setError(null); setEvidenceConfirmed(false); invalidatePlan(); detailRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' }); }
  async function moreEvents() { if (!docket || !eventsCursor || eventsLoading || busy || events.length >= 200 || seconds > 0) return; const ticket = detailGeneration.current, id = docket.id; setEventsLoading(true); try { const page = await api.operationEvents(scope.id, id, eventsCursor); if (!alive.current || ticket !== detailGeneration.current) return; if (!Array.isArray(page.items) || page.items.length > 50) throw new ApiError('审计响应无法确认。', 200); setEvents(old => mergeOperationPage(old, page.items)); setEventsCursor(page.next_cursor); } catch (cause) { if (alive.current && ticket === detailGeneration.current) fail(cause); } finally { if (alive.current) setEventsLoading(false); } }
  async function mutate(kind: 'open' | 'plan' | 'execute', event?: FormEvent) {
    event?.preventDefault(); if (mutationLock.current || unknown || seconds > 0) return;
    const problem = operationEvidenceError(reason, evidence); if (problem) { setLocalError(problem); return; }
    let caseBody: OperationCaseInput | undefined, planBody: OperationPlanInput | undefined, secret = '', attempt: symbol | undefined;
    if (kind === 'open') {
      if (!canOpen || !selected || selected.workspace_id !== scope.id) return;
      caseBody = { workspace_id: scope.id, resource_type: selected.resource_type, resource_id: selected.id, resource_version: selected.resource_version, reason, evidence_ref: evidence };
    } else {
      if (!docket || docket.state !== 'OPEN' || !actions.includes(action) || !allowedOperation(scope, action)) return;
      if (financial && !evidenceConfirmed) { setLocalError('请先确认已核实外部证据，不能根据页面文字或取消状态推测收费。'); return; }
      if (kind === 'plan') {
        setPlan(null); clearPassword();
        planBody = { workspace_id: scope.id, expected_version: docket.version, action, reason, evidence_ref: evidence };
        if (action === 'charge') {
          if (resource?.resource_type === 'run') { const usage = operationUsage(resource.provider_mode, inputTokens, outputTokens, totalTokens); if (!usage) { setLocalError('请输入证据中的三个整数计数；输入＋输出必须等于总数，且每项不超过 10 亿。'); return; } planBody.usage = usage; }
          else if (resource?.resource_type === 'task' && /^[1-9][0-9]{0,2}$/.test(units)) planBody.delivered_units = Number(units);
          else { setLocalError('请输入已核实交付的产物数量。'); return; }
        }
      } else {
        if (!plan || !operationPlanMatches(plan, docket, action) || !executeConfirmed) { setLocalError('请重新预演，并明确确认本次处置。'); clearPassword(); return; }
        secret = password.current.take(); clearPassword();
        if (new TextEncoder().encode(secret).length < 10 || new TextEncoder().encode(secret).length > 72 || secret.includes('\0')) { secret = ''; setLocalError('请输入有效的当前密码，需要 10—72 字节。'); return; }
      }
    }
    mutationLock.current = true; setBusy(true); callbacks.current.onBusy(true); setError(null); setMessage(''); setLocalError('');
    try {
      if (kind === 'open' && caseBody) {
        const key = crypto.randomUUID(); attempt = remember({ kind: 'case_create', resourceType: caseBody.resource_type, resourceID: caseBody.resource_id, body: caseBody, key });
        const value = await api.openOperationCase(caseBody, key);
        if (!operationCaseMatches(value, scope.id) || value.resource_id !== caseBody.resource_id || value.resource_type !== caseBody.resource_type) throw new ApiError('开单结果无法确认。', 200, true);
        const cleared = recovery.clearIfMatches(owner, scope.id, attempt); if (!alive.current || !cleared) return; setUnknown(null); setDocket(value); setSelected(value.resource || selected); setMessage('工单已建立。请先预演，不会自动执行任何处置。');
      } else if (kind === 'plan' && planBody && docket) {
        attempt = remember({ kind: 'plan', caseID: docket.id }); const value = await api.planOperation(docket.id, planBody);
        if (!operationPlanMatches(value, docket, action)) throw new ApiError('预演结果无法确认。', 200, true);
        const cleared = recovery.clearIfMatches(owner, scope.id, attempt); if (!alive.current || !cleared) return; setUnknown(null); setPlan(value); clearPassword(); setMessage('预演已生成，尚未执行。核对影响后再输入当前密码。');
      } else if (kind === 'execute' && plan && docket) {
        attempt = remember({ kind: 'execute', caseID: docket.id, planID: plan.id });
        const request = { workspace_id: scope.id, expected_version: docket.version, plan_id: plan.id, plan_digest: plan.plan_digest, password: secret }; secret = '';
        // api serializes synchronously into the single network request. Clear
        // the temporary object immediately; never keep a retryable password.
        const response = api.executeOperation(docket.id, request); request.password = '';
        const value = await response;
        if (!operationCaseMatches(value, scope.id, docket.id) || value.receipt?.plan_id !== plan.id) throw new ApiError('处置结果无法确认。', 200, true);
        const cleared = recovery.clearIfMatches(owner, scope.id, attempt); if (!alive.current || !cleared) return; setUnknown(null); setDocket(value); setSelected(value.resource || resource || null); setPlan(null); setMessage('处置已提交，收据已确认。');
      }
    } catch (cause) {
      const issue = safeOperationError(cause);
      // A definite refusal clears only this attempted mutation. Unknown writes
      // remain in the App-scoped metadata recovery store, without the password.
      if (!issue.uncertain) { recovery.clearIfMatches(owner, scope.id, attempt); if (alive.current) setUnknown(recovery.get(owner, scope.id)); }
      if (alive.current) { if (kind === 'execute') setPlan(null); fail(issue); }
    } finally { secret = ''; password.current.clear(); mutationLock.current = false; if (alive.current) { setBusy(false); setPasswordPresent(false); callbacks.current.onBusy(false); } }
  }
  async function checkUnknown() { if (!unknown) return; if (unknown.kind === 'case_create') { if (tab !== 'cases' || caseState !== 'all') { setCaseState('all'); setTab('cases'); } else await load(); setMessage('请在工单列表中按资源编号找到关联单并打开；不会重新提交开单。'); } else await inspect(unknown.caseID, true); }
  const controlDisabled = busy || !!unknown || detailLoading;
  return <><OpsError error={error} unknown={!!unknown}/>{unknown && !busy && <div className="ops-policy ops-unknown" role="status"><strong>处置结果待核对 · 禁止重复提交</strong><p>{unknown.kind === 'case_create' ? `原开单资源：${unknown.resourceID}。原输入和操作编号仅保留在本页内存。` : `原工单：${unknown.caseID}。密码已清除，仅保留核对所需编号。`}离开后重新登录同一账户，可在本页继续核对；关闭或刷新页面会清除此内存记录。</p><button className="button secondary" disabled={busy || loading || detailLoading || seconds > 0} onClick={() => void checkUnknown()}><Icon name="refresh" size={13}/>只刷新并核对工单</button></div>}
    <div className="ops-workbench"><section className="ops-docket" aria-label="运营队列"><div className="ops-panel-heading"><h2><Icon name="folder" size={15}/>工作队列</h2><small>READ / TRIAGE</small></div><div className="ops-tabs">{(['exceptions', 'cases'] as const).map(value => <button key={value} aria-pressed={tab === value} disabled={busy} onClick={() => setTab(value)}>{value === 'exceptions' ? '异常资源' : '核实工单'}</button>)}</div><div className="ops-controls"><label>{tab === 'exceptions' ? '资源' : '状态'}<select aria-label={tab === 'exceptions' ? '异常资源类型' : '工单状态'} disabled={busy} value={tab === 'exceptions' ? kind : caseState} onChange={event => tab === 'exceptions' ? setKind(event.target.value) : setCaseState(event.target.value)}>{(tab === 'exceptions' ? [['all', '全部类型'], ['run', 'LLM 运行'], ['task', '媒体任务']] : [['all', '全部状态'], ['OPEN', '待处置'], ['RESOLVED', '已处置']]).map(([id, name]) => <option key={id} value={id}>{name}</option>)}</select></label><button className="text-button" disabled={busy || loading || seconds > 0} onClick={() => void load()}><Icon name="refresh" size={12}/>{loading ? '读取中' : '刷新'}</button></div>
      <div className="ops-list">{tab === 'exceptions' ? exceptions.map(value => <button className="ops-row" key={value.id} aria-pressed={selected?.id === value.id && !docket} disabled={busy || !!unknown} onClick={() => selectException(value)}><span className="ops-row-top"><span>{resourceName(value.resource_type)} · {value.provider_mode.toUpperCase()}</span><span className="ops-state">{statusName(value.status)}</span></span><strong>{value.id}</strong><span className="ops-row-meta"><span>{value.model_id}</span><time>{stamp(value.updated_at)}</time></span></button>) : cases.map(value => <button className="ops-row" key={value.id} aria-pressed={docket?.id === value.id} disabled={busy || !!unknown && unknown.kind !== 'case_create' && unknown.caseID !== value.id} onClick={() => void inspect(value.id)}><span className="ops-row-top"><span>{resourceName(value.resource_type)}</span><span className={'ops-state ' + (value.state === 'RESOLVED' ? 'resolved' : '')}>{value.state === 'RESOLVED' ? '已处置' : '待处置'}</span></span><strong>{value.id}</strong><span className="ops-row-meta"><span>{operationReasons[value.reason] || '人工复核'} · v{value.version}</span><time>{stamp(value.updated_at)}</time></span><span className="ops-row-meta"><span>资源 {value.resource_id}</span></span></button>)}</div>
      {!rows.length && <div className="ops-empty"><Icon name="check" size={26}/><h3>{loading ? '正在读取授权范围' : tab === 'exceptions' ? '当前筛选没有异常' : '当前筛选没有工单'}</h3><p>{loading ? '仅获取安全元信息。' : '此处不包含提示词、输出正文或供应商凭据。'}</p></div>}<div className="ops-page-footer"><span>已加载 {rows.length} 条 · 最多 200 条</span>{cursor && rows.length < 200 ? <button className="button secondary" disabled={busy || loading || seconds > 0} onClick={() => void load(true)}>加载更多</button> : <small>{rows.length >= 200 ? '请缩小筛选后刷新' : '当前页末尾'}</small>}</div></section>
      <section className="ops-case" ref={detailRef} aria-label="工单处置详情"><div className="ops-panel-heading"><h2><Icon name="sliders" size={15}/>{docket ? '工单核实' : '资源核实'}</h2><small>{detailLoading ? '读取中…' : docket ? `CASE / v${docket.version}` : 'EVIDENCE FIRST'}</small></div>
        {!resource && !docket ? <div className="ops-empty"><Icon name="layers" size={30}/><h2>先选择一条记录</h2><p>检查状态与证据，建立工单。<br/>所有业务变动均需预演与二次确认。</p></div> : <div className="ops-case-body">
          <dl className="ops-resource"><div className="wide"><dt>{docket ? '工单编号' : '资源编号'}</dt><dd>{docket?.id || resource?.id}</dd></div>{docket && <div className="wide"><dt>关联资源</dt><dd>{docket.resource_id}</dd></div>}<div><dt>项目范围</dt><dd>{docket?.project_id || resource?.project_id}</dd></div><div><dt>供应模式 / 当前状态</dt><dd>{resource ? `${resource.provider_mode.toUpperCase()} / ${statusName(resource.status)}` : '待刷新资源状态'}</dd></div><div><dt>资源版本</dt><dd>{resource?.resource_version ?? docket?.resource_version}</dd></div><div><dt>冻结状态</dt><dd>{resource?.billing ? billingStateName[resource.billing.state] : '无关联权益冻结'}</dd></div>{resource?.billing && <><div><dt>原冻结额度</dt><dd>{formatCredits(resource.billing.reserved_micros)} 创作点</dd></div><div><dt>已消费 / 已释放</dt><dd>{formatCredits(resource.billing.charged_micros)} / {formatCredits(resource.billing.released_micros)}</dd></div></>}</dl>
          {message && <p className="ops-policy" role="status">{message}</p>}{docket?.receipt && <div className="ops-receipt"><strong><Icon name="check" size={16}/>服务端处置收据</strong>{operationActions[docket.receipt.action] || '已处置'}<dl><dt>收据</dt><dd>{docket.receipt.id}</dd><dt>计划</dt><dd>{docket.receipt.plan_id}</dd><dt>执行时间</dt><dd>{stamp(docket.receipt.executed_at)}</dd><dt>版本</dt><dd>工单 {docket.receipt.case_version} / 资源 {docket.receipt.resource_version} / 账务 {docket.receipt.billing_version}</dd></dl></div>}
          {docket && <div className="ops-secondary"><button className="button secondary" disabled={busy || detailLoading || seconds > 0} onClick={() => void inspect(docket.id, !!unknown)}><Icon name="refresh" size={13}/>核对工单状态</button><span className="ops-note">当前状态：{docket.state === 'OPEN' ? '待处置' : '已处置'}</span></div>}
          {!docket && !canOpen ? <div className="ops-policy">当前角色仅可查看安全元信息，不能建立或处置工单。</div> : docket?.state === 'RESOLVED' ? <p className="ops-note">本工单已结束。收据是已提交结果，不会再次执行同一计划。</p> : docket && !actions.length ? <div className="ops-policy">当前角色或资源状态不允许任何处置；可刷新核对最新状态。</div> : <form className="ops-fields" onSubmit={event => void mutate(docket ? 'plan' : 'open', event)}><h3 className="ops-section-title"><span>01</span>{docket ? '选择处置与证据' : '建立核实工单'}</h3>
            {docket && <label>处置动作<select aria-label="处置动作" disabled={controlDisabled} value={action} onChange={event => { setAction(event.target.value as OperationAction); setEvidenceConfirmed(false); invalidatePlan(); }}>{actions.map(value => <option key={value} value={value}>{operationActions[value]}</option>)}</select></label>}
            <label>核实原因<select aria-label="核实原因" disabled={controlDisabled} value={reason} onChange={event => { setReason(event.target.value as OperationReason); invalidatePlan(); }}>{Object.entries(operationReasons).map(([id, name]) => <option key={id} value={id}>{name}</option>)}</select></label><label>外部证据编号<input aria-label="外部证据编号" value={evidence} disabled={controlDisabled} maxLength={128} autoComplete="off" spellCheck={false} placeholder="ticket:001" onChange={event => { setEvidence(event.target.value); invalidatePlan(); }}/><small>仅填写可追溯的短编号，不填写网址、提示词、邮件、密钥或证据正文。</small></label>
            {docket && <div className="ops-policy">{action === 'reconcile' ? '仅恢复原任务的查询，不创建新生成、不替换上游任务。' : action === 'no_action' ? '只关闭本工单。任务状态、供应商执行和冻结额度均不改变。' : action === 'release' ? '将按冻结规则释放剩余额度。取消或暂时没有作品，不代表供应商没有执行。' : '按原冻结价格及已核实计量结算，不接受任意收费金额。'}</div>}
            {docket && action === 'charge' && resource?.resource_type === 'run' && <div><p className="ops-usage-note">{resource.provider_mode === 'demo' ? 'DEMO · 仅填写已核实的演示 synthetic 计量。' : 'LIVE · 必须来自完整、可靠的上游用量证据，不能按文字长度估算。'}</p><div className="ops-usage">{([['输入 tokens', inputTokens, setInputTokens], ['输出 tokens', outputTokens, setOutputTokens], ['总 tokens', totalTokens, setTotalTokens]] as const).map(([label, value, set]) => <label key={label}>{label}<input aria-label={label} inputMode="numeric" maxLength={10} disabled={controlDisabled} value={value} onChange={event => { set(event.target.value); invalidatePlan(); }}/></label>)}</div></div>}
            {docket && action === 'charge' && resource?.resource_type === 'task' && <label>已核实交付数量<input aria-label="已核实交付数量" inputMode="numeric" maxLength={3} value={units} disabled={controlDisabled} onChange={event => { setUnits(event.target.value); invalidatePlan(); }}/><small>按真实交付产物数，不使用请求数量或进度推断。</small></label>}
            {docket && financial && <label className="ops-check"><input type="checkbox" checked={evidenceConfirmed} disabled={controlDisabled} onChange={event => { setEvidenceConfirmed(event.target.checked); invalidatePlan(); }}/>我已核实外部证据，确认其属于当前资源；人工核销为本次冻结的最终决定。</label>}
            {localError && <p className="ops-error" role="alert">{localError}</p>}<button type="submit" className="ops-submit" disabled={controlDisabled || seconds > 0 || !evidence || !!docket && financial && !evidenceConfirmed}>{busy ? '正在处理…' : seconds > 0 ? `${seconds} 秒后再试` : docket ? '生成预演 · 尚不执行' : '建立工单 · 尚不处置'}</button>
          </form>}
          {plan && docket && !unknown && <section aria-label="处置预演与确认"><h3 className="ops-section-title"><span>02</span>核对本次变化</h3><div className="ops-plan"><div className="ops-plan-title"><strong>{operationActions[plan.action]}</strong><small>{planSeconds > 0 ? `${planSeconds} 秒内有效` : '预演已过期'}</small></div><table className="ops-difference"><thead><tr><th>本次变动</th><th>执行前</th><th>执行后</th></tr></thead><tbody><tr><th>新增消费</th><td>0</td><td>+ {formatCredits(plan.effect.charged_micros)} 创作点</td></tr><tr><th>释放冻结</th><td>0</td><td>+ {formatCredits(plan.effect.released_micros)} 创作点</td></tr></tbody></table><div className="ops-plan-foot">以上是本次增量，不是钱包余额。<br/><strong>计划摘要</strong> {plan.plan_digest}<br/>绑定工单 v{plan.case_version} / 资源 v{plan.resource_version} / 冻结 v{plan.billing_version}</div></div><form className="ops-fields" style={{ marginTop: 18 }} onSubmit={event => void mutate('execute', event)}><h3 className="ops-section-title"><span>03</span>密码确认并提交</h3><label className="ops-check"><input type="checkbox" checked={executeConfirmed} disabled={busy || planSeconds <= 0} onChange={event => setExecuteConfirmed(event.target.checked)}/>我已核对工作区、资源、证据与以上变化，明确执行“{operationActions[plan.action]}”。</label><label>当前账户密码<input ref={passwordInput} aria-label="当前账户密码" type="password" autoComplete="current-password" maxLength={72} disabled={busy || planSeconds <= 0} onChange={event => { password.current.set(event.target.value); setPasswordPresent(!!event.target.value); }}/><small>密码仅用于本次确认，提交或离开即清空，不进入恢复记录。</small></label><button className="ops-submit" type="submit" disabled={busy || planSeconds <= 0 || !executeConfirmed || !passwordPresent || seconds > 0}>{busy ? '提交中…' : '确认执行本次处置'}</button><button className="text-button" type="button" disabled={busy} onClick={invalidatePlan}>取消本次预演</button></form></section>}
          {docket && <section aria-label="运营审计时间线"><h3 className="ops-section-title"><span>LOG</span>处置审计</h3><div className="ops-audit">{events.map(value => <div className="ops-audit-item" key={value.id}><strong>{operationEventName(value.action)}</strong><small>{stamp(value.created_at)} · v{value.version} · {operationReasons[value.reason_code as OperationReason] || '核实记录'}</small><small>证据 {value.evidence_ref || '—'} / 操作 {value.operation_id || '—'}</small></div>)}</div>{!events.length && <p className="ops-note">刷新工单可读取已提交审计；空列表不代表操作未执行。</p>}{eventsCursor && events.length < 200 && <button className="text-button" disabled={busy || eventsLoading || seconds > 0} onClick={() => void moreEvents()}>{eventsLoading ? '读取中…' : '加载更早审计'}</button>}<p className="ops-note">已加载 {events.length} 条；仅显示封闭元信息，不含证据正文或身份信息。</p></section>}
        </div>}
      </section></div><p className="ops-note">本区域只处理任务及冻结；工作区新生成复核请切换专用区域。内容重审、账户封禁、角色在线管理和支付退款仍未开放。</p></>;
}
