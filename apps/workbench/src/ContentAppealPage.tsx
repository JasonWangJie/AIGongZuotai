import { uiText } from './locale';
import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError } from './api';
import { formatCredits } from './billing';
import { RequestDetails } from './RequestDetails';
import { useCountdown } from './useCountdown';
import { Icon } from './components';
import type { ContentAppealTarget } from './ContentAppealLink';
import { appealApi, appealMatches, appealReasons, decisions, loadAppealPending, outcomes, recoveryKey, safeID, textBytes } from './contentAppeals';
import type { Appeal, FinanceResult, PendingAppeal } from './contentAppeals';
import './content-appeals.css';

export function FinanceStatus({value}:{value:FinanceResult}) {
  if(value.status==='RELEASED' && safeID(value.receipt_id) && /^[1-9]\d{0,29}$/.test(value.released_micros || '')) return <p>{uiText("已释放")} {formatCredits(value.released_micros!)} {uiText("创作点 · 财务收据")} <code>{value.receipt_id}</code></p>;
  if(value.status==='RESOLVED_OTHER' && safeID(value.receipt_id)) return <p>{uiText("财务工单已按其他结论办结 · 收据")} <code>{value.receipt_id}</code></p>;
  return <p>{value.status==='REFERRED'?uiText('已转入财务核实，实际处置以财务收据为准。'):uiText('财务回执尚待核对。')}</p>;
}
export function AppealDetail({a}:{a:Appeal}) {
  return <section className="appeal-detail" aria-label={uiText("内容申诉详情")}><div className="appeal-heading"><h2>{uiText("申请详情")}</h2><span>{a.state==='OPEN'?uiText('待审议'):uiText('已办结')}</span></div>
    <p><code>{a.id}</code></p><h3>{uiText("原审核决定")}</h3><dl><dt>{uiText("原资源")}</dt><dd>{a.resource_type==='run'?uiText('文本 Run'):uiText('媒体 Task')} · <code>{a.resource_id}</code></dd><dt>{uiText("受理证据版本")}</dt><dd>{a.resource_type==='run'?uiText('Run 序号'):uiText('任务版本')} {a.resource_version}</dd><dt>{uiText("输出审核")}</dt><dd>{decisions[a.decision]} · v{a.moderation_version}</dd><dt>{uiText("审核策略")}</dt><dd>{a.policy_id} · {a.policy_version}</dd>{a.decision_reason && <><dt>{uiText("决定原因")}</dt><dd>{a.decision_reason}</dd></>}</dl>
    <h3>{uiText("申请说明")}</h3><p>{appealReasons[a.reason_code]}</p>{a.statement && <p className="appeal-prose">{a.statement}</p>}
    <h3>{uiText("独立审议")}</h3><p>{a.outcome?outcomes[a.outcome]:uiText('等待工作区审核员审议。')}</p>{a.public_reply && <p className="appeal-prose">{a.public_reply}</p>}
    {a.outcome==='FINANCE_REFERRED' && <><h3>{uiText("财务进展")}</h3>{a.finance?<FinanceStatus value={a.finance}/>:<p>{uiText("已提交核实意见，等待财务人员关联工单。")}</p>}</>}
  </section>;
}
export function useAppealIO(scope:string,onExpired:()=>void) {
  const alive=useRef(true), lock=useRef(false), [busy,setBusy]=useState(false);
  const [error,setError]=useState<ApiError|null>(null), [pending,setPending]=useState<PendingAppeal|null>(null), [unavailable,setUnavailable]=useState(false);
  const expire=useRef(onExpired); expire.current=onExpired;
  const report=useCallback((e:unknown)=>{
    if(!alive.current)return;
    const issue=e instanceof ApiError?e:new ApiError(e instanceof Error?e.message:uiText('请求未能完成。'));
    setError(issue);if(issue.status===401 && issue.code!=='INVALID_CREDENTIALS')expire.current();
  },[]);
  useEffect(()=>{alive.current=true;try{setPending(loadAppealPending(scope));}catch(e){setUnavailable(true);report(e);}return()=>{alive.current=false;};},[scope]);
  async function act(intent:PendingAppeal,work:()=>Promise<void>,reading=false) {
    if(lock.current || unavailable)return;
    lock.current=true;setBusy(true);setError(null);
    let saved=false;
    const clear=()=>{if(loadAppealPending(scope)?.key===intent.key)sessionStorage.removeItem(scope);if(alive.current)setPending(loadAppealPending(scope));};
    try {
      const old=loadAppealPending(scope);
      if(!reading && old){setPending(old);return;}
      if(reading && (!old || old.key!==intent.key))throw Error(uiText('原操作编号已有变化，请重新打开。'));
      if(!reading)sessionStorage.setItem(scope,JSON.stringify(intent));
      saved=true;setPending(intent);await work();clear();
    }catch(e){
      if(!saved){if(alive.current)setUnavailable(true);}
      else if(!reading && e instanceof ApiError && !e.uncertain)clear();
      report(e);
    }finally{lock.current=false;if(alive.current)setBusy(false);}
  }
  const cooling=useCountdown(error?.retryAt||0)>0;
  return {alive,busy,error,pending,report,act,locked:busy||!!pending||unavailable||cooling};
}
export function ReceiptNotice({pending,onRead,busy}:{pending:PendingAppeal|null;onRead:()=>void;busy:boolean}) {
  if(!pending || busy)return null;
  return <div className="appeal-notice" role="status"><strong>{uiText("上次操作的回执待核对")}</strong><p>{uiText("保留原编号，可稍后继续核对结果。")}</p><button className="button secondary" onClick={onRead}>{uiText("只读核对原回执")}</button></div>;
}
export function AppealError({error,pending}:{error:ApiError|null;pending:boolean}) {
  return error?<div role="alert" className="appeal-notice"><p>{error.message}</p><RequestDetails error={error} receiptOnly={pending}/></div>:null;
}
export function AppealQueue({items,selected,onOpen}:{items:Appeal[];selected?:string;onOpen:(id:string)=>void}) {
  return <div className="appeal-rows">{items.map(a=><button key={a.id} aria-pressed={selected===a.id} onClick={()=>onOpen(a.id)}><span><strong>{a.resource_type==='run'?uiText('文本输出'):uiText('媒体输出')} · {decisions[a.decision]}</strong><small>{a.resource_id}</small></span><span>{a.state==='OPEN'?uiText('待审议'):uiText('已办结')}</span></button>)}</div>;
}
export function useAppealList(workspace:string,project:string|undefined,io:ReturnType<typeof useAppealIO>) {
  const listTicket=useRef(0),detailTicket=useRef(0), [items,setItems]=useState<Appeal[]>([]), [cursor,setCursor]=useState<string|null>(null), [detail,setDetail]=useState<Appeal|null>(null);
  async function list(more=false) {
    const ticket=++listTicket.current;
    try{
      const page=await (project?appealApi.ownList(project,more?cursor:null):appealApi.opsList(workspace,more?cursor:null));
      if(!io.alive.current || ticket!==listTicket.current)return;
      if(!Array.isArray(page.items) || page.items.length>50 || !page.items.every(a=>appealMatches(a,workspace,project)))throw Error(uiText('列表与当前身份范围不符。'));
      setItems(old=>[...new Map([...(more?old:[]),...page.items].map(a=>[a.id,a])).values()].slice(0,200));setCursor(page.next_cursor||null);
    }catch(e){if(io.alive.current && ticket===listTicket.current){setItems([]);setCursor(null);if(e instanceof ApiError && [401,403,404].includes(e.status)){++detailTicket.current;setDetail(null);}io.report(e);}}
  }
  async function read(id:string) {
    const ticket=++detailTicket.current;setDetail(null);
    try{
      const a=await (project?appealApi.ownDetail(project,id):appealApi.opsDetail(workspace,id));
      if(!io.alive.current || ticket!==detailTicket.current)return;
      if(!appealMatches(a,workspace,project,id))throw Error(uiText('详情与当前身份范围不符。'));
      setDetail(a);
    }catch(e){if(ticket===detailTicket.current)io.report(e);}
  }
  useEffect(()=>{void list();return()=>{++listTicket.current;++detailTicket.current;};},[workspace,project]);
  return {items,cursor:items.length<200?cursor:null,detail,list,read};
}
export function ContentAppealPage({owner,workspace,project,target,clearTarget,onExpired}:{owner:string;workspace:string;project:string;target:ContentAppealTarget|null;clearTarget:()=>void;onExpired:()=>void}) {
  const io=useAppealIO(recoveryKey('owner',owner,workspace,project),onExpired), queue=useAppealList(workspace,project,io);
  const [reason,setReason]=useState<keyof typeof appealReasons>('request_review'), [statement,setStatement]=useState('');
  const source=target?.project_id===project?target:null;
  async function handle(intent:PendingAppeal,result:Appeal) {
    if(!appealMatches(result,workspace,project) || result.resource_id!==intent.id || result.resource_type!==intent.type || result.moderation_version!==intent.version)throw Error(uiText('收据与原资源或审核版本不匹配，请继续核对。'));
    if(io.alive.current){setStatement('');clearTarget();await queue.read(result.id);await queue.list();}
  }
  async function create(){
    if(!source || io.locked || textBytes(statement)>1024)return;
    const i:PendingAppeal={action:'create',id:source.resource_id,type:source.resource_type,version:source.moderation.version,key:crypto.randomUUID()};
    await io.act(i,async()=>handle(i,await appealApi.create(source,reason,statement.trim(),i.key)));
  }
  function receipt(){const i=io.pending;if(i?.action==='create')void io.act(i,async()=>handle(i,await appealApi.ownReceipt(project,i.key)),true);}
  return <section className="appeal-page"><header className="appeal-hero"><Icon name="folder" size={23}/><div><h1>{uiText("我的内容申诉")}</h1><p>{uiText("查看本项目已执行生成的输出审核、独立审议意见及相关财务进展。")}</p></div></header>
  <ReceiptNotice pending={io.pending} busy={io.busy} onRead={receipt}/><AppealError error={io.error} pending={!!io.pending}/>
  {source && <form className="appeal-compose" onSubmit={e=>{e.preventDefault();void create();}}><h2>{uiText("对原审核提出申诉")}</h2><p>{source.resource_type==='run'?uiText('文本 Run'):uiText('媒体 Task')} · <code>{source.resource_id}</code> {uiText("· 输出审核 v")}{source.moderation.version}</p>
  <fieldset disabled={io.locked}><label>{uiText("申诉原因")}<select value={reason} onChange={e=>setReason(e.target.value as keyof typeof appealReasons)}>{Object.entries(appealReasons).map(([k,v])=><option key={k} value={k}>{v}</option>)}</select></label>
  <label>{uiText("补充说明")}<textarea rows={3} value={statement} onChange={e=>setStatement(e.target.value)} maxLength={1024} placeholder={uiText("简述需要核对的事实。请勿填写凭据或粘贴受限内容。")}/></label><small>{textBytes(statement)} {uiText("/ 1024 UTF-8 字节 · 仅本人和授权审核员可读")}</small>
  <div className="appeal-actions"><button className="button primary" disabled={textBytes(statement)>1024}>{uiText("提交内容申诉")}</button><button className="text-button" type="button" onClick={clearTarget}>{uiText("收起申请")}</button></div></fieldset></form>}
  <div className="appeal-columns"><aside><div className="appeal-heading"><h2>{uiText("本项目申请")}</h2><button className="text-button" onClick={()=>{void queue.list();if(queue.detail)void queue.read(queue.detail.id);}}>{uiText("刷新申请")}</button></div>
  <AppealQueue items={queue.items} selected={queue.detail?.id} onOpen={queue.read}/>{!queue.items.length && <p>{uiText("暂无申请。在受限回复或媒体任务中选择“申请内容申诉”。")}</p>}{queue.cursor && <button className="text-button" onClick={()=>void queue.list(true)}>{uiText("读取更早申请")}</button>}</aside>
  {queue.detail?<AppealDetail a={queue.detail}/>:<section className="appeal-empty"><Icon name="folder" size={28}/><p>{uiText("选择申请，查看原审核和独立处置记录。")}</p></section>}</div></section>;
}

