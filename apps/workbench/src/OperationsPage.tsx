import { uiText, currentLocale } from './locale';
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
import { appealApi } from './contentAppeals';
import type { FinanceLink } from './contentAppeals';
import {RiskReviewOperations} from './RiskReviewPage';
import { ContentAppealModerator, ContentAppealFinance } from './ContentAppealOperations';
import { OperationsGenerationPanel } from './OperationsGenerationPanel';
import { GenerationRecovery } from './generationControls';

function stamp(value: string) { const date = new Date(value); return Number.isNaN(date.valueOf()) ? uiText('时间待核对') : date.toLocaleString(currentLocale(), { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }); }
function resourceName(value: 'run' | 'task') { return value === 'run' ? uiText('LLM 运行') : uiText('媒体任务'); }
function OpsError({ error, unknown }: { error: ApiError | null; unknown?: boolean }) { if (!error) return null; return <div className={'ops-error ' + (unknown ? 'ops-unknown' : '')} role="alert"><p>{error.message}</p>{error.requestId && <code>{uiText("请求编号 ·")} {error.requestId}</code>}</div>; }

export function OperationsPage({ owner, access, recovery, onExpired, onBusy, onRefreshAccess, generationRecovery, onGenerationChanged }: { owner: string; access: OperationAccess; recovery: OperationsRecovery; onExpired: () => void; onBusy: (value: boolean) => void; onRefreshAccess: () => void; generationRecovery?: GenerationRecovery; onGenerationChanged?: () => void }) {
  const localGenerationRecovery = useRef(new GenerationRecovery()), generationStore = generationRecovery || localGenerationRecovery.current;
  const scopes = operationScopes(access), [preferred, setPreferred] = useState(''), [busy, setBusy] = useState(false);
  const [nextScope, setNextScope] = useState(''), scopeDialog = useRef<HTMLDialogElement>(null);
  const scope = scopes.find(value => value.id === preferred) || scopes[0];
  useEffect(() => { if (nextScope) scopeDialog.current?.showModal(); else scopeDialog.current?.close(); }, [nextScope]);
  function busyChanged(value: boolean) { setBusy(value); onBusy(value); }
  return <section className="operations-page" aria-label={uiText("运营异常核实台")}><header className="ops-heading"><div><span className="eyebrow">XINGTU / OPERATIONS DESK</span><h1>{uiText("让每一次处置，")}<br/>{uiText("都有据可循。")}</h1><p>{uiText("从异常到证据，从预演到确认。在授权工作区核对任务、冻结额度与新生成权限，所有处置留下记录。")}</p></div><div className="ops-stamp">CONTROL ROOM<small>SCOPED · TRACEABLE</small></div></header>
    {!scope ? <div className="ops-scope-empty"><Icon name="sliders" size={28}/><h2>{uiText("当前没有可用运营权限")}</h2><p>{uiText("此入口需要服务端授予的工作区角色。普通创作账户不会自动获得权限。")}</p><button className="button secondary" onClick={onRefreshAccess}>{uiText("重新核对权限")}</button></div> : <><div className="ops-scope"><label><span>{uiText("AUTHORIZED WORKSPACE / 授权范围")}</span><select aria-label={uiText("运营工作区")} value={scope.id} disabled={busy} onChange={event => { if (recovery.get(owner, scope.id) || generationStore.ops(owner, scope.id) || generationStore.opsDraft(owner, scope.id)) setNextScope(event.target.value); else setPreferred(event.target.value); }}>{scopes.map(value => <option key={value.id} value={value.id}>{value.id}</option>)}</select></label><div className="ops-roles" aria-label={uiText("当前运营角色")}>{scope.roles.map(role => <span className="ops-role" key={role}>{operationRoles[role]}</span>)}</div><p className="ops-scope-note">{uiText("这里的范围独立于创作项目。权限以每次服务端核验为准。")}</p><button className="button secondary" disabled={busy} onClick={onRefreshAccess}><Icon name="refresh" size={13}/>{uiText("核对权限")}</button></div><OperationsPanels key={owner + ':' + scope.id} owner={owner} scope={scope} recovery={recovery} generationRecovery={generationStore} onExpired={onExpired} onBusy={busyChanged} onChanged={onGenerationChanged}/></>}
    <dialog ref={scopeDialog} className="draft-leave-dialog" aria-labelledby="ops-scope-leave-title" onCancel={event => { event.preventDefault(); setNextScope(''); }}><span className="eyebrow">SCOPE / AWAITING CONFIRMATION</span><h2 id="ops-scope-leave-title">{uiText("原工作区还有待核对处置")}</h2><p>{uiText("切换不会撤回已发送操作。密码会清空，核对编号仅保留在当前页面内存；返回原工作区后只刷新工单核对。")}</p><div><button className="button primary" autoFocus onClick={() => setNextScope('')}>{uiText("留在原工作区")}</button><button className="button secondary" onClick={() => { setPreferred(nextScope); setNextScope(''); }}>{uiText("切换，保留核对编号")}</button></div></dialog>
  </section>;
}

function OperationsPanels({ owner, scope, recovery, generationRecovery, onExpired, onBusy, onChanged }: { owner: string; scope: OperationScope; recovery: OperationsRecovery; generationRecovery: GenerationRecovery; onExpired: () => void; onBusy: (value: boolean) => void; onChanged?: () => void }) {
  const generation = scope.permissions.includes('generation_restrict') || scope.permissions.includes('appeal_resolve'), financial = scope.roles.some(role => ['observer','operator','finance'].includes(role));
  type Section = 'resources' | 'generation' | 'appeals' | 'appeal-finance' | 'risk';
  const [tab,setTab]=useState<Section>(financial?'resources':'generation'), [busy,setBusy]=useState(false),[caseID,setCaseID]=useState('');
  const choices: [Section,string][]=[];
  if(scope.roles.includes('risk_reviewer') && scope.permissions.includes('risk_review'))choices.push(['risk',uiText('额度风控审议')]);
  if(financial)choices.push(['resources',uiText('任务与冻结核实')]);
  if(generation)choices.push(['generation',uiText('新生成限制与复核')]);
  if(scope.roles.includes('moderator') && scope.permissions.includes('content_appeal_review'))choices.push(['appeals',uiText('内容申诉审议')]);
  if(scope.roles.includes('finance') && scope.permissions.includes('finance'))choices.push(['appeal-finance',uiText('申诉财务转交')]);
  const active=choices.some(([id])=>id===tab)?tab:choices[0]?.[0];
  function select(next:Section){
    if(busy || next===active)return;
    if((recovery.get(owner,scope.id) || generationRecovery.ops(owner,scope.id) || generationRecovery.opsDraft(owner,scope.id)) && !window.confirm(uiText('当前还有待核对操作或未提交说明。切换后在本页内存保留，密码清空；继续切换？')))return;
    setCaseID('');setTab(next);
  }
  const busyChanged=(v:boolean)=>{setBusy(v);onBusy(v);};
  return <>{choices.length>1 && <nav className="generation-panel-tabs" aria-label={uiText("运营业务区域")}>{choices.map(([id,label])=><button key={id} aria-pressed={active===id} className={active===id?'active':''} disabled={busy} onClick={()=>select(id)}>{label}</button>)}</nav>}
    {active==='risk'?<RiskReviewOperations owner={owner} workspace={scope.id} onExpired={onExpired} onBusy={busyChanged}/>:active==='appeals'?<ContentAppealModerator owner={owner} workspace={scope.id} onExpired={onExpired} onBusy={busyChanged}/>:
    active==='appeal-finance'?<ContentAppealFinance owner={owner} workspace={scope.id} onExpired={onExpired} onBusy={busyChanged} openCase={id=>{setCaseID(id);setTab('resources');}}/>:
    active==='generation'?<OperationsGenerationPanel owner={owner} scope={scope} recovery={generationRecovery} onExpired={onExpired} onBusy={busyChanged} onChanged={onChanged}/>:
    active==='resources'?<OperationsWorkspace owner={owner} scope={scope} recovery={recovery} onExpired={onExpired} onBusy={busyChanged} initialCase={caseID}/>:<p className="generation-note">{uiText("当前没有明确授予的处置权限。请重新核对授权。")}</p>}</>;
}

function OperationsWorkspace({ owner, scope, recovery, onExpired, onBusy, initialCase }: { initialCase?: string; owner: string; scope: OperationScope; recovery: OperationsRecovery; onExpired: () => void; onBusy: (value: boolean) => void }) {
  const [tab, setTab] = useState<'exceptions' | 'cases'>('exceptions'), [kind, setKind] = useState('all'), [caseState, setCaseState] = useState('all');
  const [exceptions, setExceptions] = useState<OperationException[]>([]), [cases, setCases] = useState<OperationCase[]>([]), [cursor, setCursor] = useState<string | null>(null);
  const [selected, setSelected] = useState<OperationException | null>(null), [docket, setDocket] = useState<OperationCase | null>(null), [plan, setPlan] = useState<OperationPlan | null>(null);
  const [appealLink,setAppealLink]=useState<FinanceLink|null>(null);
  useEffect(()=>{
    let alive=true;setAppealLink(null);
    if(docket && scope.roles.includes('finance') && scope.permissions.includes('finance')){
      void appealApi.caseLink(scope.id,docket.id).then(link=>{if(alive && link.case_id===docket.id)setAppealLink(link);}).catch(()=>{});
    }
    return ()=>{alive=false;};
  },[scope.id,docket?.id,docket?.version,scope.permissions.join(',')]);
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
        if (!Array.isArray(page.items) || page.items.length > 50 || page.items.some(value => value.workspace_id !== scope.id)) throw new ApiError(uiText('队列响应范围不匹配。'), 200);
        setExceptions(old => more ? mergeOperationPage(old, page.items) : page.items); setCursor(page.next_cursor);
      } else {
        const page = await api.operationCases(query, abort.signal);
        if (!alive.current || ticket !== listGeneration.current) return;
        if (!Array.isArray(page.items) || page.items.length > 50 || page.items.some(value => !operationCaseMatches(value, scope.id))) throw new ApiError(uiText('工单响应范围不匹配。'), 200);
        setCases(old => more ? mergeOperationPage(old, page.items) : page.items); setCursor(page.next_cursor);
      }
    } catch (cause) { if (alive.current && ticket === listGeneration.current) fail(cause); }
    finally { if (alive.current && ticket === listGeneration.current) { readLock.current = false; setLoading(false); } }
  }
  useEffect(() => { listGeneration.current++; listAbort.current?.abort(); readLock.current = false; setCursor(null); setExceptions([]); setCases([]); void load(); }, [tab, kind, caseState]);

  useEffect(()=>{if(initialCase){setTab('cases');void inspect(initialCase);}},[initialCase]);
  async function inspect(id: string, checking = false) {
    if (mutationLock.current || seconds > 0) return;
    const pending = recovery.get(owner, scope.id), pendingAttempt = recovery.attempt(owner, scope.id);
    const ticket = ++detailGeneration.current; detailAbort.current?.abort(); const abort = new AbortController(); detailAbort.current = abort; setDetailLoading(true); setError(null); invalidatePlan();
    try {
      const [value, timeline] = await Promise.all([api.operationCase(scope.id, id, abort.signal), api.operationEvents(scope.id, id, null, abort.signal)]);
      if (!alive.current || ticket !== detailGeneration.current) return;
      if (!operationCaseMatches(value, scope.id, id) || value.resource && value.resource.workspace_id !== scope.id) throw new ApiError(uiText('工单响应范围不匹配。'), 200);
      if (!Array.isArray(timeline.items) || timeline.items.length > 50) throw new ApiError(uiText('审计响应无法确认。'), 200);
      setEvents(timeline.items); setEventsCursor(timeline.next_cursor);
      setDocket(value); setSelected(value.resource || null); setReason(value.reason); setEvidence(value.evidence_ref); setEvidenceConfirmed(false); setInputTokens(''); setOutputTokens(''); setTotalTokens(''); setUnits('');
      if (pending?.kind === 'execute' && value.receipt?.plan_id === pending.planID && clearUnknown(pendingAttempt)) { setMessage(uiText('已从服务端收据确认处置结果。未再次提交操作。')); }
      else if (pending?.kind === 'plan' && pending.caseID === value.id && clearUnknown(pendingAttempt)) { setMessage(uiText('工单状态已核对。上次预演没有自动执行；如需继续，请手动重新预演。')); }
      else if (pending?.kind === 'case_create' && pending.resourceID === value.resource_id && pending.resourceType === value.resource_type && clearUnknown(pendingAttempt)) { setMessage(uiText('已选择此资源的关联工单。没有再次开单，请核对其证据与状态。')); }
      else if (checking && pending) setMessage(uiText('当前仍未找到匹配的执行收据。保持待确认，请稍后再次核对。'));
      detailRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
    } catch (cause) { if (alive.current && ticket === detailGeneration.current) fail(cause); }
    finally { if (alive.current && ticket === detailGeneration.current) setDetailLoading(false); }
  }
  function selectException(value: OperationException) { if (busy || unknown) return; if (value.open_case_id) { void inspect(value.open_case_id); return; } detailGeneration.current++; detailAbort.current?.abort(); setDetailLoading(false); setSelected(value); setDocket(null); setEvents([]); setEventsCursor(null); setReason('manual_review'); setEvidence(''); setError(null); setEvidenceConfirmed(false); invalidatePlan(); detailRef.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' }); }
  async function moreEvents() { if (!docket || !eventsCursor || eventsLoading || busy || events.length >= 200 || seconds > 0) return; const ticket = detailGeneration.current, id = docket.id; setEventsLoading(true); try { const page = await api.operationEvents(scope.id, id, eventsCursor); if (!alive.current || ticket !== detailGeneration.current) return; if (!Array.isArray(page.items) || page.items.length > 50) throw new ApiError(uiText('审计响应无法确认。'), 200); setEvents(old => mergeOperationPage(old, page.items)); setEventsCursor(page.next_cursor); } catch (cause) { if (alive.current && ticket === detailGeneration.current) fail(cause); } finally { if (alive.current) setEventsLoading(false); } }
  async function mutate(kind: 'open' | 'plan' | 'execute', event?: FormEvent) {
    event?.preventDefault(); if (mutationLock.current || unknown || seconds > 0) return;
    const problem = operationEvidenceError(reason, evidence); if (problem) { setLocalError(problem); return; }
    let caseBody: OperationCaseInput | undefined, planBody: OperationPlanInput | undefined, secret = '', attempt: symbol | undefined;
    if (kind === 'open') {
      if (!canOpen || !selected || selected.workspace_id !== scope.id) return;
      caseBody = { workspace_id: scope.id, resource_type: selected.resource_type, resource_id: selected.id, resource_version: selected.resource_version, reason, evidence_ref: evidence };
    } else {
      if (!docket || docket.state !== 'OPEN' || !actions.includes(action) || !allowedOperation(scope, action)) return;
      if (financial && !evidenceConfirmed) { setLocalError(uiText('请先确认已核实外部证据，不能根据页面文字或取消状态推测收费。')); return; }
      if (kind === 'plan') {
        setPlan(null); clearPassword();
        planBody = { workspace_id: scope.id, expected_version: docket.version, action, reason, evidence_ref: evidence };
        if (action === 'charge') {
          if (resource?.resource_type === 'run') { const usage = operationUsage(resource.provider_mode, inputTokens, outputTokens, totalTokens); if (!usage) { setLocalError(uiText('请输入证据中的三个整数计数；输入＋输出必须等于总数，且每项不超过 10 亿。')); return; } planBody.usage = usage; }
          else if (resource?.resource_type === 'task' && /^[1-9][0-9]{0,2}$/.test(units)) planBody.delivered_units = Number(units);
          else { setLocalError(uiText('请输入已核实交付的产物数量。')); return; }
        }
      } else {
        if (!plan || !operationPlanMatches(plan, docket, action) || !executeConfirmed) { setLocalError(uiText('请重新预演，并明确确认本次处置。')); clearPassword(); return; }
        secret = password.current.take(); clearPassword();
        if (new TextEncoder().encode(secret).length < 10 || new TextEncoder().encode(secret).length > 72 || secret.includes('\0')) { secret = ''; setLocalError(uiText('请输入有效的当前密码，需要 10—72 字节。')); return; }
      }
    }
    mutationLock.current = true; setBusy(true); callbacks.current.onBusy(true); setError(null); setMessage(''); setLocalError('');
    try {
      if (kind === 'open' && caseBody) {
        const key = crypto.randomUUID(); attempt = remember({ kind: 'case_create', resourceType: caseBody.resource_type, resourceID: caseBody.resource_id, body: caseBody, key });
        const value = await api.openOperationCase(caseBody, key);
        if (!operationCaseMatches(value, scope.id) || value.resource_id !== caseBody.resource_id || value.resource_type !== caseBody.resource_type) throw new ApiError(uiText('开单结果无法确认。'), 200, true);
        const cleared = recovery.clearIfMatches(owner, scope.id, attempt); if (!alive.current || !cleared) return; setUnknown(null); setDocket(value); setSelected(value.resource || selected); setMessage(uiText('工单已建立。请先预演，不会自动执行任何处置。'));
      } else if (kind === 'plan' && planBody && docket) {
        attempt = remember({ kind: 'plan', caseID: docket.id }); const value = await api.planOperation(docket.id, planBody);
        if (!operationPlanMatches(value, docket, action)) throw new ApiError(uiText('预演结果无法确认。'), 200, true);
        const cleared = recovery.clearIfMatches(owner, scope.id, attempt); if (!alive.current || !cleared) return; setUnknown(null); setPlan(value); clearPassword(); setMessage(uiText('预演已生成，尚未执行。核对影响后再输入当前密码。'));
      } else if (kind === 'execute' && plan && docket) {
        attempt = remember({ kind: 'execute', caseID: docket.id, planID: plan.id });
        const request = { workspace_id: scope.id, expected_version: docket.version, plan_id: plan.id, plan_digest: plan.plan_digest, password: secret }; secret = '';
        // api serializes synchronously into the single network request. Clear
        // the temporary object immediately; never keep a retryable password.
        const response = api.executeOperation(docket.id, request); request.password = '';
        const value = await response;
        if (!operationCaseMatches(value, scope.id, docket.id) || value.receipt?.plan_id !== plan.id) throw new ApiError(uiText('处置结果无法确认。'), 200, true);
        const cleared = recovery.clearIfMatches(owner, scope.id, attempt); if (!alive.current || !cleared) return; setUnknown(null); setDocket(value); setSelected(value.resource || resource || null); setPlan(null); setMessage(uiText('处置已提交，收据已确认。'));
      }
    } catch (cause) {
      const issue = safeOperationError(cause);
      // A definite refusal clears only this attempted mutation. Unknown writes
      // remain in the App-scoped metadata recovery store, without the password.
      if (!issue.uncertain) { recovery.clearIfMatches(owner, scope.id, attempt); if (alive.current) setUnknown(recovery.get(owner, scope.id)); }
      if (alive.current) { if (kind === 'execute') setPlan(null); fail(issue); }
    } finally { secret = ''; password.current.clear(); mutationLock.current = false; if (alive.current) { setBusy(false); setPasswordPresent(false); callbacks.current.onBusy(false); } }
  }
  async function checkUnknown() { if (!unknown) return; if (unknown.kind === 'case_create') { if (tab !== 'cases' || caseState !== 'all') { setCaseState('all'); setTab('cases'); } else await load(); setMessage(uiText('请在工单列表中按资源编号找到关联单并打开；不会重新提交开单。')); } else await inspect(unknown.caseID, true); }
  const controlDisabled = busy || !!unknown || detailLoading;
  return <><OpsError error={error} unknown={!!unknown}/>{unknown && !busy && <div className="ops-policy ops-unknown" role="status"><strong>{uiText("处置结果待核对 · 禁止重复提交")}</strong><p>{unknown.kind === 'case_create' ? uiText("原开单资源：{0}。原输入和操作编号仅保留在本页内存。", unknown.resourceID) : uiText("原工单：{0}。密码已清除，仅保留核对所需编号。", unknown.caseID)}{uiText("离开后重新登录同一账户，可在本页继续核对；关闭或刷新页面会清除此内存记录。")}</p><button className="button secondary" disabled={busy || loading || detailLoading || seconds > 0} onClick={() => void checkUnknown()}><Icon name="refresh" size={13}/>{uiText("只刷新并核对工单")}</button></div>}
    {appealLink && docket && appealLink.case_id===docket.id && <p className="ops-policy">{uiText("关联内容申诉")} <code>{appealLink.appeal_id}</code> {uiText("· 财务处置沿用本工单的原资源和冻结。")}</p>}
    <div className="ops-workbench"><section className="ops-docket" aria-label={uiText("运营队列")}><div className="ops-panel-heading"><h2><Icon name="folder" size={15}/>{uiText("工作队列")}</h2><small>READ / TRIAGE</small></div><div className="ops-tabs">{(['exceptions', 'cases'] as const).map(value => <button key={value} aria-pressed={tab === value} disabled={busy} onClick={() => setTab(value)}>{value === 'exceptions' ? uiText('异常资源') : uiText('核实工单')}</button>)}</div><div className="ops-controls"><label>{tab === 'exceptions' ? uiText('资源') : uiText('状态')}<select aria-label={tab === 'exceptions' ? uiText('异常资源类型') : uiText('工单状态')} disabled={busy} value={tab === 'exceptions' ? kind : caseState} onChange={event => tab === 'exceptions' ? setKind(event.target.value) : setCaseState(event.target.value)}>{(tab === 'exceptions' ? [['all', uiText('全部类型')], ['run', uiText('LLM 运行')], ['task', uiText('媒体任务')]] : [['all', uiText('全部状态')], ['OPEN', uiText('待处置')], ['RESOLVED', uiText('已处置')]]).map(([id, name]) => <option key={id} value={id}>{name}</option>)}</select></label><button className="text-button" disabled={busy || loading || seconds > 0} onClick={() => void load()}><Icon name="refresh" size={12}/>{loading ? uiText('读取中') : uiText('刷新')}</button></div>
      <div className="ops-list">{tab === 'exceptions' ? exceptions.map(value => <button className="ops-row" key={value.id} aria-pressed={selected?.id === value.id && !docket} disabled={busy || !!unknown} onClick={() => selectException(value)}><span className="ops-row-top"><span>{resourceName(value.resource_type)} · {value.provider_mode.toUpperCase()}</span><span className="ops-state">{statusName(value.status)}</span></span><strong>{value.id}</strong><span className="ops-row-meta"><span>{value.model_id}</span><time>{stamp(value.updated_at)}</time></span></button>) : cases.map(value => <button className="ops-row" key={value.id} aria-pressed={docket?.id === value.id} disabled={busy || !!unknown && unknown.kind !== 'case_create' && unknown.caseID !== value.id} onClick={() => void inspect(value.id)}><span className="ops-row-top"><span>{resourceName(value.resource_type)}</span><span className={'ops-state ' + (value.state === 'RESOLVED' ? 'resolved' : '')}>{value.state === 'RESOLVED' ? uiText('已处置') : uiText('待处置')}</span></span><strong>{value.id}</strong><span className="ops-row-meta"><span>{operationReasons[value.reason] || uiText('人工复核')} · v{value.version}</span><time>{stamp(value.updated_at)}</time></span><span className="ops-row-meta"><span>{uiText("资源")} {value.resource_id}</span></span></button>)}</div>
      {!rows.length && <div className="ops-empty"><Icon name="check" size={26}/><h3>{loading ? uiText('正在读取授权范围') : tab === 'exceptions' ? uiText('当前筛选没有异常') : uiText('当前筛选没有工单')}</h3><p>{loading ? uiText('仅获取安全元信息。') : uiText('此处不包含提示词、输出正文或供应商凭据。')}</p></div>}<div className="ops-page-footer"><span>{uiText("已加载")} {rows.length} {uiText("条 · 最多 200 条")}</span>{cursor && rows.length < 200 ? <button className="button secondary" disabled={busy || loading || seconds > 0} onClick={() => void load(true)}>{uiText("加载更多")}</button> : <small>{rows.length >= 200 ? uiText('请缩小筛选后刷新') : uiText('当前页末尾')}</small>}</div></section>
      <section className="ops-case" ref={detailRef} aria-label={uiText("工单处置详情")}><div className="ops-panel-heading"><h2><Icon name="sliders" size={15}/>{docket ? uiText('工单核实') : uiText('资源核实')}</h2><small>{detailLoading ? uiText('读取中…') : docket ? `CASE / v${docket.version}` : 'EVIDENCE FIRST'}</small></div>
        {!resource && !docket ? <div className="ops-empty"><Icon name="layers" size={30}/><h2>{uiText("先选择一条记录")}</h2><p>{uiText("检查状态与证据，建立工单。")}<br/>{uiText("所有业务变动均需预演与二次确认。")}</p></div> : <div className="ops-case-body">
          <dl className="ops-resource"><div className="wide"><dt>{docket ? uiText('工单编号') : uiText('资源编号')}</dt><dd>{docket?.id || resource?.id}</dd></div>{docket && <div className="wide"><dt>{uiText("关联资源")}</dt><dd>{docket.resource_id}</dd></div>}<div><dt>{uiText("项目范围")}</dt><dd>{docket?.project_id || resource?.project_id}</dd></div><div><dt>{uiText("供应模式 / 当前状态")}</dt><dd>{resource ? `${resource.provider_mode.toUpperCase()} / ${statusName(resource.status)}` : uiText('待刷新资源状态')}</dd></div><div><dt>{uiText("资源版本")}</dt><dd>{resource?.resource_version ?? docket?.resource_version}</dd></div><div><dt>{uiText("冻结状态")}</dt><dd>{resource?.billing ? billingStateName[resource.billing.state] : uiText('无关联权益冻结')}</dd></div>{resource?.billing && <><div><dt>{uiText("原冻结额度")}</dt><dd>{formatCredits(resource.billing.reserved_micros)} {uiText("创作点")}</dd></div><div><dt>{uiText("已消费 / 已释放")}</dt><dd>{formatCredits(resource.billing.charged_micros)} / {formatCredits(resource.billing.released_micros)}</dd></div></>}</dl>
          {message && <p className="ops-policy" role="status">{message}</p>}{docket?.receipt && <div className="ops-receipt"><strong><Icon name="check" size={16}/>{uiText("服务端处置收据")}</strong>{operationActions[docket.receipt.action] || uiText('已处置')}<dl><dt>{uiText("收据")}</dt><dd>{docket.receipt.id}</dd><dt>{uiText("计划")}</dt><dd>{docket.receipt.plan_id}</dd><dt>{uiText("执行时间")}</dt><dd>{stamp(docket.receipt.executed_at)}</dd><dt>{uiText("版本")}</dt><dd>{uiText("工单")} {docket.receipt.case_version} {uiText("/ 资源")} {docket.receipt.resource_version} {uiText("/ 账务")} {docket.receipt.billing_version}</dd></dl></div>}
          {docket && <div className="ops-secondary"><button className="button secondary" disabled={busy || detailLoading || seconds > 0} onClick={() => void inspect(docket.id, !!unknown)}><Icon name="refresh" size={13}/>{uiText("核对工单状态")}</button><span className="ops-note">{uiText("当前状态：")}{docket.state === 'OPEN' ? uiText('待处置') : uiText('已处置')}</span></div>}
          {!docket && !canOpen ? <div className="ops-policy">{uiText("当前角色仅可查看安全元信息，不能建立或处置工单。")}</div> : docket?.state === 'RESOLVED' ? <p className="ops-note">{uiText("本工单已结束。收据是已提交结果，不会再次执行同一计划。")}</p> : docket && !actions.length ? <div className="ops-policy">{uiText("当前角色或资源状态不允许任何处置；可刷新核对最新状态。")}</div> : <form className="ops-fields" onSubmit={event => void mutate(docket ? 'plan' : 'open', event)}><h3 className="ops-section-title"><span>01</span>{docket ? uiText('选择处置与证据') : uiText('建立核实工单')}</h3>
            {docket && <label>{uiText("处置动作")}<select aria-label={uiText("处置动作")} disabled={controlDisabled} value={action} onChange={event => { setAction(event.target.value as OperationAction); setEvidenceConfirmed(false); invalidatePlan(); }}>{actions.map(value => <option key={value} value={value}>{operationActions[value]}</option>)}</select></label>}
            <label>{uiText("核实原因")}<select aria-label={uiText("核实原因")} disabled={controlDisabled} value={reason} onChange={event => { setReason(event.target.value as OperationReason); invalidatePlan(); }}>{Object.entries(operationReasons).map(([id, name]) => <option key={id} value={id}>{name}</option>)}</select></label><label>{uiText("外部证据编号")}<input aria-label={uiText("外部证据编号")} value={evidence} disabled={controlDisabled} maxLength={128} autoComplete="off" spellCheck={false} placeholder="ticket:001" onChange={event => { setEvidence(event.target.value); invalidatePlan(); }}/><small>{uiText("仅填写可追溯的短编号，不填写网址、提示词、邮件、密钥或证据正文。")}</small></label>
            {docket && <div className="ops-policy">{action === 'reconcile' ? uiText('仅恢复原任务的查询，不创建新生成、不替换上游任务。') : action === 'no_action' ? uiText('只关闭本工单。任务状态、供应商执行和冻结额度均不改变。') : action === 'release' ? uiText('将按冻结规则释放剩余额度。取消或暂时没有作品，不代表供应商没有执行。') : uiText('按原冻结价格及已核实计量结算，不接受任意收费金额。')}</div>}
            {docket && action === 'charge' && resource?.resource_type === 'run' && <div><p className="ops-usage-note">{resource.provider_mode === 'demo' ? uiText('DEMO · 仅填写已核实的演示 synthetic 计量。') : uiText('LIVE · 必须来自完整、可靠的上游用量证据，不能按文字长度估算。')}</p><div className="ops-usage">{([[uiText('输入 tokens'), inputTokens, setInputTokens], [uiText('输出 tokens'), outputTokens, setOutputTokens], [uiText('总 tokens'), totalTokens, setTotalTokens]] as const).map(([label, value, set]) => <label key={label}>{label}<input aria-label={label} inputMode="numeric" maxLength={10} disabled={controlDisabled} value={value} onChange={event => { set(event.target.value); invalidatePlan(); }}/></label>)}</div></div>}
            {docket && action === 'charge' && resource?.resource_type === 'task' && <label>{uiText("已核实交付数量")}<input aria-label={uiText("已核实交付数量")} inputMode="numeric" maxLength={3} value={units} disabled={controlDisabled} onChange={event => { setUnits(event.target.value); invalidatePlan(); }}/><small>{uiText("按真实交付产物数，不使用请求数量或进度推断。")}</small></label>}
            {docket && financial && <label className="ops-check"><input type="checkbox" checked={evidenceConfirmed} disabled={controlDisabled} onChange={event => { setEvidenceConfirmed(event.target.checked); invalidatePlan(); }}/>{uiText("我已核实外部证据，确认其属于当前资源；人工核销为本次冻结的最终决定。")}</label>}
            {localError && <p className="ops-error" role="alert">{localError}</p>}<button type="submit" className="ops-submit" disabled={controlDisabled || seconds > 0 || !evidence || !!docket && financial && !evidenceConfirmed}>{busy ? uiText('正在处理…') : seconds > 0 ? uiText("{0} 秒后再试", seconds) : docket ? uiText('生成预演 · 尚不执行') : uiText('建立工单 · 尚不处置')}</button>
          </form>}
          {plan && docket && !unknown && <section aria-label={uiText("处置预演与确认")}><h3 className="ops-section-title"><span>02</span>{uiText("核对本次变化")}</h3><div className="ops-plan"><div className="ops-plan-title"><strong>{operationActions[plan.action]}</strong><small>{planSeconds > 0 ? uiText("{0} 秒内有效", planSeconds) : uiText('预演已过期')}</small></div><table className="ops-difference"><thead><tr><th>{uiText("本次变动")}</th><th>{uiText("执行前")}</th><th>{uiText("执行后")}</th></tr></thead><tbody><tr><th>{uiText("新增消费")}</th><td>0</td><td>+ {formatCredits(plan.effect.charged_micros)} {uiText("创作点")}</td></tr><tr><th>{uiText("释放冻结")}</th><td>0</td><td>+ {formatCredits(plan.effect.released_micros)} {uiText("创作点")}</td></tr></tbody></table><div className="ops-plan-foot">{uiText("以上是本次增量，不是钱包余额。")}<br/><strong>{uiText("计划摘要")}</strong> {plan.plan_digest}<br/>{uiText("绑定工单 v")}{plan.case_version} {uiText("/ 资源 v")}{plan.resource_version} {uiText("/ 冻结 v")}{plan.billing_version}</div></div><form className="ops-fields" style={{ marginTop: 18 }} onSubmit={event => void mutate('execute', event)}><h3 className="ops-section-title"><span>03</span>{uiText("密码确认并提交")}</h3><label className="ops-check"><input type="checkbox" checked={executeConfirmed} disabled={busy || planSeconds <= 0} onChange={event => setExecuteConfirmed(event.target.checked)}/>{uiText("我已核对工作区、资源、证据与以上变化，明确执行“")}{operationActions[plan.action]}”。</label><label>{uiText("当前账户密码")}<input ref={passwordInput} aria-label={uiText("当前账户密码")} type="password" autoComplete="current-password" maxLength={72} disabled={busy || planSeconds <= 0} onChange={event => { password.current.set(event.target.value); setPasswordPresent(!!event.target.value); }}/><small>{uiText("密码仅用于本次确认，提交或离开即清空，不进入恢复记录。")}</small></label><button className="ops-submit" type="submit" disabled={busy || planSeconds <= 0 || !executeConfirmed || !passwordPresent || seconds > 0}>{busy ? uiText('提交中…') : uiText('确认执行本次处置')}</button><button className="text-button" type="button" disabled={busy} onClick={invalidatePlan}>{uiText("取消本次预演")}</button></form></section>}
          {docket && <section aria-label={uiText("运营审计时间线")}><h3 className="ops-section-title"><span>LOG</span>{uiText("处置审计")}</h3><div className="ops-audit">{events.map(value => <div className="ops-audit-item" key={value.id}><strong>{operationEventName(value.action)}</strong><small>{stamp(value.created_at)} · v{value.version} · {operationReasons[value.reason_code as OperationReason] || uiText('核实记录')}</small><small>{uiText("证据")} {value.evidence_ref || '—'} {uiText("/ 操作")} {value.operation_id || '—'}</small></div>)}</div>{!events.length && <p className="ops-note">{uiText("刷新工单可读取已提交审计；空列表不代表操作未执行。")}</p>}{eventsCursor && events.length < 200 && <button className="text-button" disabled={busy || eventsLoading || seconds > 0} onClick={() => void moreEvents()}>{eventsLoading ? uiText('读取中…') : uiText('加载更早审计')}</button>}<p className="ops-note">{uiText("已加载")} {events.length} {uiText("条；仅显示封闭元信息，不含证据正文或身份信息。")}</p></section>}
        </div>}
      </section></div><p className="ops-note">{uiText("本区域只处理任务及冻结；工作区新生成复核请切换专用区域。内容重审、账户封禁、角色在线管理和支付退款仍未开放。")}</p></>;
}
