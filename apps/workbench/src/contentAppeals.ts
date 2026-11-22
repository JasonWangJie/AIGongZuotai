import { uiText } from './locale';
import { request } from './api';
import type { ContentAppealTarget } from './ContentAppealLink';

export const appealReasons = {get decision_error() { return uiText('原决定可能有误'); },get request_review() { return uiText('申请人工审议'); },get billing_concern() { return uiText('申请核实相关冻结'); }};
export const decisions = {get BLOCKED() { return uiText('已限制'); }, get REVIEW() { return uiText('待核实'); }};
export const outcomes = {get UPHELD() { return uiText('维持原处置'); },get OPINION_RECORDED() { return uiText('已记录独立意见'); },get FINANCE_REFERRED() { return uiText('已转交财务核实'); }};
export const actions = {get uphold() { return uiText('维持原处置'); },get record_opinion() { return uiText('记录独立审议意见'); },get refer_finance() { return uiText('转交财务核实冻结'); }};
export type AppealAction = keyof typeof actions;
export const outcomeForAction = {uphold:'UPHELD',record_opinion:'OPINION_RECORDED',refer_finance:'FINANCE_REFERRED'} as const;
export type FinanceResult = {status:'REFERRED'|'RELEASED'|'RESOLVED_OTHER';receipt_id?:string;released_micros?:string};
export type FinanceLink = FinanceResult & {appeal_id:string;case_id:string};
export type FinanceItem = {appeal_id:string;project_id:string;resource_type:'run'|'task';resource_id:string;version:number;created_at:string;case_id?:string};
export type Appeal = {id:string;workspace_id:string;project_id:string;resource_type:'run'|'task';resource_id:string;resource_version:number;moderation_version:number;decision:keyof typeof decisions;decision_reason:string;policy_id:string;policy_version:string;reason_code:keyof typeof appealReasons;state:'OPEN'|'RESOLVED';outcome?:keyof typeof outcomes;version:number;statement?:string;public_reply?:string;finance?:FinanceResult;created_at:string;updated_at:string};
export type AppealPlanInput = {workspace_id:string;expected_version:number;action:AppealAction;evidence_ref:string;public_reply:string};
export type AppealPlan = {id:string;appeal_id:string;workspace_id:string;expected_version:number;action:AppealAction;outcome:keyof typeof outcomes;plan_digest:string;expires_at:string};
export type AppealReceipt = {id:string;workspace_id:string;appeal_id:string;plan_id:string;action:AppealAction;version:number;outcome:keyof typeof outcomes};
type Page<T> = {items:T[];next_cursor?:string|null};
const own='/content-appeals', ops='/operations/content-appeals', enc=encodeURIComponent;
const scopeQuery=(ws:string)=>'?workspace_id='+enc(ws);
const pageQuery=(cursor?:string|null)=>'limit=20'+(cursor?'&cursor='+enc(cursor):'');
const keyHeader=(key?:string):Record<string,string>=>key ? {'Idempotency-Key':key} : {};
const projectHeaders=(project:string,key?:string)=>({'X-Project-ID':project,...keyHeader(key)});
const post=(body:unknown,headers:Record<string,string>={}):RequestInit=>({method:'POST',headers,body:JSON.stringify(body)});
export const appealApi = {
  ownList:(p:string,cursor?:string|null)=>request<Page<Appeal>>(own+'?project_id='+enc(p)+'&'+pageQuery(cursor),{headers:projectHeaders(p)}),
  ownDetail:(p:string,id:string)=>request<Appeal>(own+'/'+enc(id)+'?project_id='+enc(p),{headers:projectHeaders(p)}),
  create:(target:ContentAppealTarget,reason:keyof typeof appealReasons,statement:string,key:string)=>request<Appeal>(own,post({project_id:target.project_id,resource_type:target.resource_type,resource_id:target.resource_id,moderation_version:target.moderation.version,reason_code:reason,statement},projectHeaders(target.project_id,key))),
  ownReceipt:(p:string,key:string)=>request<Appeal>(own+'/receipt?project_id='+enc(p),{headers:projectHeaders(p,key)}),
  opsList:(w:string,cursor?:string|null)=>request<Page<Appeal>>(ops+scopeQuery(w)+'&'+pageQuery(cursor)),
  opsDetail:(w:string,id:string)=>request<Appeal>(ops+'/'+enc(id)+scopeQuery(w)),
  preview:(id:string,body:AppealPlanInput)=>request<AppealPlan>(ops+'/'+enc(id)+'/plans',post(body)),
  execute:(id:string,w:string,plan:AppealPlan,password:string)=>request<AppealReceipt>(ops+'/'+enc(id)+'/execute',post({workspace_id:w,plan_id:plan.id,plan_digest:plan.plan_digest,password})),
  opsReceipt:(w:string,id:string,plan:string)=>request<AppealReceipt>(ops+'/'+enc(id)+'/plans/'+enc(plan)+'/receipt'+scopeQuery(w)),
  financeList:(w:string,cursor?:string|null)=>request<Page<FinanceItem>>(ops+'/finance'+scopeQuery(w)+'&'+pageQuery(cursor)),
  financeOpen:(w:string,id:string,version:number,key:string)=>request<FinanceLink>(ops+'/'+enc(id)+'/finance-case',post({workspace_id:w,expected_version:version},keyHeader(key))),
  financeReceipt:(w:string,id:string,key?:string)=>request<FinanceLink>(ops+'/'+enc(id)+'/finance-case'+scopeQuery(w),{headers:keyHeader(key)}),
  caseLink:(w:string,id:string)=>request<FinanceLink>('/operations/cases/'+enc(id)+'/content-appeal'+scopeQuery(w)),
};
export const safeID=(v:unknown):v is string=>typeof v==='string' && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(v);
export function appealMatches(a:Appeal,w:string,p?:string,id?:string) {
  return !!a && Number.isSafeInteger(a.resource_version) && a.resource_version>0 && a.workspace_id===w && (!p || a.project_id===p) && (!id || a.id===id) && safeID(a.id) && safeID(a.project_id) && safeID(a.resource_id) &&
    ['run','task'].includes(a.resource_type) && Object.hasOwn(decisions,a.decision) && Object.hasOwn(appealReasons,a.reason_code) && Number.isSafeInteger(a.moderation_version) && a.moderation_version>0 &&
    Number.isSafeInteger(a.version) && a.version>0 && (a.state==='OPEN' || a.state==='RESOLVED' && !!a.outcome && Object.hasOwn(outcomes,a.outcome));
}
export const textBytes=(s:string)=>new TextEncoder().encode(s).length;
export type PendingAppeal = {action:'create'|'execute'|'finance';key:string;id:string;type?:string;version?:number};
export function recoveryKey(mode:string,owner:string,w:string,p='') {return 'xingtu-content-appeal:'+JSON.stringify([mode,owner,w,p]);}
export function loadAppealPending(scope:string):PendingAppeal|null {
  const text=sessionStorage.getItem(scope);
  if (!text) return null;
  const i:PendingAppeal=JSON.parse(text);
  if (!i || !['create','execute','finance'].includes(i.action) || !safeID(i.key) || !safeID(i.id)) throw Error(uiText('本页的原操作编号无法核对。'));
  return i;
}

