import { uiText } from './locale';
import { useEffect, useRef, useState } from 'react';
import { useCountdown } from './useCountdown';
import { operationEvidenceError } from './operations';
import { AppealDetail, AppealError, AppealQueue, FinanceStatus, ReceiptNotice, useAppealIO, useAppealList } from './ContentAppealPage';
import { actions, appealApi, outcomeForAction, outcomes, recoveryKey, safeID, textBytes } from './contentAppeals';
import type { AppealAction, AppealPlan, AppealReceipt, FinanceItem, FinanceLink, PendingAppeal } from './contentAppeals';

type Props={owner:string;workspace:string;onExpired:()=>void;onBusy:(v:boolean)=>void};
export function ContentAppealModerator({owner,workspace,onExpired,onBusy}:Props) {
  const io=useAppealIO(recoveryKey('moderator',owner,workspace),onExpired), queue=useAppealList(workspace,undefined,io);
  const [action,setAction]=useState<AppealAction>('record_opinion'), [reply,setReply]=useState(''),[evidence,setEvidence]=useState('');
  const [plan,setPlan]=useState<AppealPlan|null>(null), [confirmed,setConfirmed]=useState(false),[hasPassword,setHasPassword]=useState(false),[planning,setPlanning]=useState(false);
  const [receipt,setReceipt]=useState<AppealReceipt|null>(null), password=useRef<HTMLInputElement>(null), ticket=useRef(0),busyCB=useRef(onBusy);
  busyCB.current=onBusy;
  useEffect(()=>{busyCB.current(planning||io.busy);return()=>busyCB.current(false);},[planning,io.busy]);
  const remaining=useCountdown(plan?Date.parse(plan.expires_at):0,900);
  function invalidate(){++ticket.current;setPlan(null);setConfirmed(false);setHasPassword(false);if(password.current)password.current.value='';}
  function select(id:string){if(io.locked || planning)return;invalidate();setReceipt(null);setReply('');setEvidence('');setAction('record_opinion');void queue.read(id);}
  const validInput=!operationEvidenceError('manual_review',evidence.trim()) && !!reply.trim() && textBytes(reply.trim())<=1024;
  async function preview() {
    const a=queue.detail;if(!a || a.state!=='OPEN' || io.locked || planning || !validInput)return;
    invalidate();setPlanning(true);const t=ticket.current;
    try{
      const p=await appealApi.preview(a.id,{workspace_id:workspace,expected_version:a.version,action,evidence_ref:evidence.trim(),public_reply:reply.trim()});
      if(!io.alive.current || t!==ticket.current)return;
      const expectedOutcome=outcomeForAction[action];
      if(p.workspace_id!==workspace || p.appeal_id!==a.id || p.action!==action || p.outcome!==expectedOutcome || p.expected_version!==a.version || !safeID(p.id) || !p.plan_digest || !(Date.parse(p.expires_at)>Date.now()))throw Error(uiText('预演与当前审核范围或申请版本不匹配。'));
      setPlan(p);
    }catch(e){io.report(e);}finally{if(io.alive.current)setPlanning(false);}
  }
  async function handle(i:PendingAppeal,r:AppealReceipt){
    if(r.workspace_id!==workspace || r.appeal_id!==i.id || r.plan_id!==i.key || r.action!==i.type || !safeID(r.id) || r.outcome!==outcomeForAction[r.action] || r.version!==(i.version || 0)+1)throw Error(uiText('处置回执与原计划不匹配。'));
    if(io.alive.current){setReceipt(r);invalidate();await queue.read(i.id);await queue.list();}
  }
  function execute() {
    if(!plan || !queue.detail || plan.appeal_id!==queue.detail.id || plan.expected_version!==queue.detail.version || !confirmed || !password.current?.value || io.locked || !remaining)return;
    const pw=password.current.value;password.current.value='';setHasPassword(false);
    const i:PendingAppeal={action:'execute',id:plan.appeal_id,type:plan.action,version:plan.expected_version,key:plan.id};
    void io.act(i,async()=>handle(i,await appealApi.execute(i.id,workspace,plan,pw)));
  }
  function readReceipt(){const i=io.pending;if(i?.action==='execute')void io.act(i,async()=>handle(i,await appealApi.opsReceipt(workspace,i.id,i.key)),true);}
  return <section className="appeal-page" aria-label={uiText("内容申诉审议")}><header className="appeal-hero"><div><h2>{uiText("内容申诉审议")}</h2><p>{uiText("核对原审核证据，向申请人给出独立意见。冻结额度交由财务人员另行核实。")}</p></div></header>
  <ReceiptNotice pending={io.pending} busy={io.busy} onRead={readReceipt}/><AppealError error={io.error} pending={!!io.pending}/>
  <div className="appeal-columns"><aside><div className="appeal-heading"><h3>{uiText("工作区申请")}</h3><button className="text-button" disabled={io.locked||planning} onClick={()=>{void queue.list();if(queue.detail)select(queue.detail.id);}}>{uiText("刷新审议队列")}</button></div>
  <AppealQueue items={queue.items} selected={queue.detail?.id} onOpen={select}/>{!queue.items.length && <p>{uiText("当前没有可见的内容申诉。")}</p>}{queue.cursor && <button className="text-button" onClick={()=>void queue.list(true)}>{uiText("读取更早申请")}</button>}</aside><div>
  {queue.detail && <AppealDetail a={queue.detail}/>} {receipt && <p className="appeal-notice">{uiText("已核对处置收据")} <code>{receipt.id}</code></p>}
  {queue.detail?.state==='OPEN' && <section className="appeal-compose"><h3>{uiText("准备审议意见")}</h3><fieldset disabled={io.locked||planning}><label>{uiText("拟议处置")}<select value={action} onChange={e=>{invalidate();setAction(e.target.value as AppealAction);}}>{Object.entries(actions).map(([k,v])=><option key={k} value={k}>{v}</option>)}</select></label>
  <label>{uiText("内部证据编号")}<input value={evidence} maxLength={128} onChange={e=>{invalidate();setEvidence(e.target.value);}} placeholder="ticket:001"/></label>
  <label>{uiText("给申请人的公开意见")}<textarea rows={3} value={reply} maxLength={1024} onChange={e=>{invalidate();setReply(e.target.value);}}/></label>
  <button className="button secondary" disabled={!validInput} onClick={preview}>{uiText("预演内容申诉处置")}</button></fieldset>
  {plan && <div className="appeal-preview"><h3>{uiText("确认本次处置")}</h3><p>{outcomes[plan.outcome]} {uiText("· 原审核决定与原件访问规则保留。")}</p><p className="appeal-prose">{reply}</p><small>{uiText("预演剩余")} {remaining} {uiText("秒")}</small><label className="appeal-check"><input type="checkbox" checked={confirmed} disabled={io.locked} onChange={e=>setConfirmed(e.target.checked)}/>{uiText("我已核对原申请和公开意见")}</label>
  <label>{uiText("当前审核员密码")}<input type="password" autoComplete="current-password" maxLength={72} ref={password} disabled={io.locked} onChange={e=>setHasPassword(!!e.target.value)}/></label>
  <button className="button primary" disabled={!confirmed||!hasPassword||!remaining||io.locked} onClick={execute}>{uiText("确认内容申诉处置")}</button></div>}</section>}</div></div></section>;
}

export function ContentAppealFinance({owner,workspace,onExpired,onBusy,openCase}:Props&{openCase:(id:string)=>void}) {
  const io=useAppealIO(recoveryKey('finance',owner,workspace),onExpired);
  const [rows,setRows]=useState<FinanceItem[]>([]),[cursor,setCursor]=useState<string|null>(null),[link,setLink]=useState<FinanceLink|null>(null);
  const ticket=useRef(0), readTicket=useRef(0),cb=useRef(onBusy);cb.current=onBusy;
  useEffect(()=>{cb.current(io.busy);return()=>cb.current(false);},[io.busy]);
  async function list(more=false){
    const t=++ticket.current;
    try{
      const page=await appealApi.financeList(workspace,more?cursor:null);
      if(t!==ticket.current || !io.alive.current)return;
      if(!Array.isArray(page.items) || page.items.length>50 || !page.items.every(v=>safeID(v.appeal_id)&&safeID(v.project_id)&&safeID(v.resource_id)&&['run','task'].includes(v.resource_type)&&Number.isSafeInteger(v.version)))throw Error(uiText('财务队列响应无法核对。'));
      setRows(old=>[...new Map([...(more?old:[]),...page.items].map(v=>[v.appeal_id,v])).values()].slice(0,200));setCursor(page.next_cursor||null);
    }catch(e){if(t===ticket.current){setRows([]);io.report(e);}}
  }
  useEffect(()=>{void list();return()=>{++ticket.current;++readTicket.current;};},[workspace]);
  function check(id:string,l:FinanceLink){if(l.appeal_id!==id || !safeID(l.case_id) || !['REFERRED','RELEASED','RESOLVED_OTHER'].includes(l.status))throw Error(uiText('财务工单的归属无法核对。'));}
  async function read(id:string){const t=++readTicket.current;setLink(null);try{const l=await appealApi.financeReceipt(workspace,id);check(id,l);if(io.alive.current&&t===readTicket.current)setLink(l);}catch(e){if(t===readTicket.current)io.report(e);}}
  async function handle(i:PendingAppeal,l:FinanceLink){check(i.id,l);if(io.alive.current){++readTicket.current;setLink(l);await list();}}
  function create(v:FinanceItem){if(io.locked)return;const i:PendingAppeal={action:'finance',key:crypto.randomUUID(),id:v.appeal_id};void io.act(i,async()=>handle(i,await appealApi.financeOpen(workspace,i.id,v.version,i.key)));}
  function receipt(){const i=io.pending;if(i?.action==='finance')void io.act(i,async()=>handle(i,await appealApi.financeReceipt(workspace,i.id,i.key)),true);}
  return <section className="appeal-page"><header className="appeal-hero"><div><h2>{uiText("申诉财务转交")}</h2><p>{uiText("复核已转交的原冻结。关联工单后，财务处置仍需单独预演与确认。")}</p></div></header>
  <ReceiptNotice pending={io.pending} busy={io.busy} onRead={receipt}/><AppealError error={io.error} pending={!!io.pending}/><button className="button secondary" onClick={()=>void list()}>{uiText("刷新财务转交")}</button>
  <div className="appeal-rows">{rows.map(v=><article key={v.appeal_id}><div><strong>{v.resource_type==='run'?uiText('文本 Run'):uiText('媒体 Task')}</strong><small>{v.resource_id} {uiText("· 申请")} {v.appeal_id}</small></div>
  {v.case_id?<button className="button secondary" onClick={()=>void read(v.appeal_id)}>{uiText("读取关联工单")}</button>:<button className="button secondary" disabled={io.locked} onClick={()=>create(v)}>{uiText("关联财务核实工单")}</button>}</article>)}</div>
  {!rows.length && <p>{uiText("本工作区暂无财务转交。")}</p>}{cursor && rows.length<200 && <button className="text-button" onClick={()=>void list(true)}>{uiText("读取更早转交")}</button>}
  {link && <div className="appeal-preview"><h3>{uiText("已关联核实工单")}</h3><code>{link.case_id}</code><FinanceStatus value={link}/><button className="button primary" onClick={()=>openCase(link.case_id)}>{uiText("打开财务核实工单")}</button></div>}</section>;
}

