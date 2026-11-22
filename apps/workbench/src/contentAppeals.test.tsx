// @vitest-environment jsdom
import {act} from 'react';
import {createRoot} from 'react-dom/client';
import {afterEach,beforeEach,expect,it,vi} from 'vitest';
import {ApiError} from './api';
import {AppealDetail,ContentAppealPage,FinanceStatus} from './ContentAppealPage';
import {ContentAppealFinance,ContentAppealModerator} from './ContentAppealOperations';
import {ContentAppealContext,ContentAppealLink} from './ContentAppealLink';
import {appealApi,recoveryKey} from './contentAppeals';
import type {Appeal,AppealPlan} from './contentAppeals';
import type {ModerationSummary} from './moderation';

const decision:ModerationSummary={state:'BLOCKED',phase:'output',version:3,policy_id:'policy',policy_version:'1',reason_code:'policy_block'};
const a:Appeal={id:'appeal',workspace_id:'w',project_id:'p',resource_type:'task',resource_id:'task',resource_version:7,moderation_version:3,decision:'BLOCKED',decision_reason:'policy_block',policy_id:'policy',policy_version:'1',reason_code:'request_review',state:'OPEN',version:1,statement:'仅申请人和审核员能看到的说明',created_at:'2026-01-01',updated_at:'2026-01-01'};
const target={project_id:'p',resource_type:'task' as const,resource_id:'task',moderation:decision};
const noop=()=>{}, props={owner:'u',workspace:'w',project:'p',clearTarget:noop,onExpired:noop,target};
let root:ReturnType<typeof createRoot>,view:HTMLDivElement;
beforeEach(()=>{vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT',true);sessionStorage.clear();view=document.createElement('div');document.body.append(view);root=createRoot(view);});
afterEach(async()=>{await act(()=>root.unmount());view.remove();vi.restoreAllMocks();vi.unstubAllGlobals();});
const render=(node:React.ReactNode)=>act(()=>root.render(node));
async function click(s:string){const btn=[...view.querySelectorAll('button')].find(b=>b.textContent===s);expect(btn,s).toBeTruthy();await act(()=>btn!.click());}
async function setValue(selector:string,text:string){const e=view.querySelector(selector) as HTMLInputElement|HTMLTextAreaElement|HTMLSelectElement;expect(e).toBeTruthy();await act(()=>{const proto=e instanceof HTMLSelectElement?HTMLSelectElement.prototype:e instanceof HTMLInputElement?HTMLInputElement.prototype:HTMLTextAreaElement.prototype;Object.getOwnPropertyDescriptor(proto,'value')!.set!.call(e,text);e.dispatchEvent(new Event(e instanceof HTMLSelectElement?'change':'input',{bubbles:true}));});}
it.each([{state:'ALLOWED'},{state:'PENDING'},{phase:'input'},{reason_code:'execution_interrupted'}])('offers no appeal for unexecuted or non-review evidence',async change=>{
  await render(<ContentAppealContext.Provider value={{project:'p',open:noop}}><ContentAppealLink id="task" type="task" project="p" status="FAILED" moderation={{...decision,...change} as ModerationSummary}/></ContentAppealContext.Provider>);
  expect(view.querySelector('button')).toBeNull();
});
it('binds an entry to an existing failed output in the active project',async()=>{
  const open=vi.fn();const show=(p:string,status:string)=>render(<ContentAppealContext.Provider value={{project:'p',open}}><ContentAppealLink id="task" type="task" project={p} status={status} moderation={decision}/></ContentAppealContext.Provider>);
  await show('another','FAILED');expect(view.querySelector('button')).toBeNull();await show('p','RUNNING');expect(view.querySelector('button')).toBeNull();await show('p','FAILED');await click('申请内容申诉');expect(open).toHaveBeenCalledWith(target);
});
it('keeps only the original identity after lost creation and reads the scoped receipt across remounts',async()=>{
  vi.spyOn(appealApi,'ownList').mockResolvedValue({items:[]});const write=vi.spyOn(appealApi,'create').mockRejectedValue(new ApiError('网络未确认',0,true));vi.spyOn(appealApi,'ownDetail').mockResolvedValue(a);const read=vi.spyOn(appealApi,'ownReceipt').mockResolvedValue(a);
  await render(<ContentAppealPage {...props}/>);await setValue('textarea',a.statement!);await click('提交内容申诉');
  const key=recoveryKey('owner','u','w','p'), pending=JSON.parse(sessionStorage.getItem(key)!);expect(pending.key).toBe(write.mock.calls[0][3]);expect(sessionStorage.getItem(key)).not.toContain(a.statement);
  await render(null);await render(<ContentAppealPage {...props} target={null}/>);await click('只读核对原回执');
  expect(read).toHaveBeenCalledWith('p',pending.key);expect(write).toHaveBeenCalledTimes(1);expect(sessionStorage.getItem(key)).toBeNull();expect(view.querySelector('.appeal-detail')?.textContent).toContain(a.statement);
});
it('rejects another project in a list and keeps an unrelated source receipt unresolved',async()=>{
  vi.spyOn(appealApi,'ownList').mockResolvedValue({items:[{...a,project_id:'foreign'}]});vi.spyOn(appealApi,'ownReceipt').mockResolvedValue({...a,resource_id:'other'});
  const key=recoveryKey('owner','u','w','p');sessionStorage.setItem(key,JSON.stringify({action:'create',key:'same',id:'task',type:'task',version:3}));
  await render(<ContentAppealPage {...props}/>);await click('只读核对原回执');
  expect(view.querySelector('.appeal-rows button')).toBeNull();expect(view.textContent).not.toContain(a.statement);expect(sessionStorage.getItem(key)).toContain('same');
});
it('never interprets an opinion or referral as an executed release',async()=>{
  await render(<AppealDetail a={{...a,state:'RESOLVED',outcome:'FINANCE_REFERRED'}}/>);expect(view.textContent).not.toContain('已释放');await render(<FinanceStatus value={{status:'RELEASED',receipt_id:'official',released_micros:'200000'}}/>);expect(view.textContent).toContain('已释放 0.2 创作点');await render(<FinanceStatus value={{status:'RESOLVED_OTHER',receipt_id:'other'}}/>);expect(view.textContent).not.toContain('已释放');
});
it('rejects a moderator plan belonging to another workspace without requesting a password',async()=>{
  vi.spyOn(appealApi,'opsList').mockResolvedValue({items:[a]});vi.spyOn(appealApi,'opsDetail').mockResolvedValue(a);
  vi.spyOn(appealApi,'preview').mockResolvedValue({id:'plan',workspace_id:'foreign',appeal_id:a.id,expected_version:1,action:'record_opinion',outcome:'OPINION_RECORDED',plan_digest:'digest',expires_at:new Date(Date.now()+60000).toISOString()});
  await render(<ContentAppealModerator owner="op" workspace="w" onBusy={noop} onExpired={noop}/>);await act(()=>view.querySelector<HTMLButtonElement>('.appeal-rows button')!.click());
  await setValue('.appeal-compose input','ticket:1');await setValue('.appeal-compose textarea','独立意见');await click('预演内容申诉处置');expect(view.querySelector('input[type=password]')).toBeNull();expect(view.textContent).toContain('不匹配');
});
it('retains just the moderator plan id on an unknown execution and does not send the password twice',async()=>{
  const plan:AppealPlan={id:'plan',workspace_id:'w',appeal_id:a.id,expected_version:1,action:'record_opinion',outcome:'OPINION_RECORDED',plan_digest:'digest',expires_at:new Date(Date.now()+60000).toISOString()};
  vi.spyOn(appealApi,'opsList').mockResolvedValue({items:[a]});vi.spyOn(appealApi,'opsDetail').mockResolvedValue(a);vi.spyOn(appealApi,'preview').mockResolvedValue(plan);
  const write=vi.spyOn(appealApi,'execute').mockRejectedValue(new ApiError('回执待核对',0,true)),receipt=vi.spyOn(appealApi,'opsReceipt').mockResolvedValue({id:'receipt',workspace_id:'w',appeal_id:a.id,plan_id:'plan',action:'record_opinion',version:2,outcome:'OPINION_RECORDED'});
  await render(<ContentAppealModerator owner="op" workspace="w" onBusy={noop} onExpired={noop}/>);await act(()=>view.querySelector<HTMLButtonElement>('.appeal-rows button')!.click());
  await setValue('.appeal-compose input','ticket:1');await setValue('.appeal-compose textarea','独立意见');await click('预演内容申诉处置');await act(()=>view.querySelector<HTMLInputElement>('input[type=checkbox]')!.click());await setValue('input[type=password]','private-password');await click('确认内容申诉处置');
  const saved=sessionStorage.getItem(recoveryKey('moderator','op','w'));expect(saved).toContain('plan');expect(saved).not.toMatch(/private-password|独立意见|digest/);expect(view.querySelector<HTMLInputElement>('input[type=password]')?.value).toBe('');
  await click('只读核对原回执');expect(receipt).toHaveBeenCalledWith('w','appeal','plan');expect(write).toHaveBeenCalledTimes(1);
});
it('renders a redacted finance queue and uses only the original receipt after unknown linking',async()=>{
  vi.spyOn(appealApi,'financeList').mockResolvedValue({items:[{appeal_id:'appeal',project_id:'p',resource_type:'task',resource_id:'task',version:2,created_at:'today',statement:a.statement} as never]});const open=vi.spyOn(appealApi,'financeOpen').mockRejectedValue(new ApiError('回执待核对',0,true)),receipt=vi.spyOn(appealApi,'financeReceipt').mockResolvedValue({appeal_id:'appeal',case_id:'case',status:'REFERRED'});const openCase=vi.fn();
  await render(<ContentAppealFinance owner="finance" workspace="w" onBusy={noop} onExpired={noop} openCase={openCase}/>);expect(view.textContent).not.toContain(a.statement);await click('关联财务核实工单');await click('只读核对原回执');expect(receipt).toHaveBeenCalledWith('w','appeal',open.mock.calls[0][3]);await click('打开财务核实工单');expect(openCase).toHaveBeenCalledWith('case');expect(open).toHaveBeenCalledTimes(1);
});
it('uses scoped GET receipts and strips passwords from preview and receipt parameters',async()=>{
  const f=vi.fn((_url:string,_init:RequestInit)=>Promise.resolve(Response.json({})));vi.stubGlobal('fetch',f);
  await appealApi.ownReceipt('p','opaque-key');await appealApi.opsReceipt('w','a','plan');await appealApi.financeReceipt('w','a','link-key');
  expect(f.mock.calls.map(c=>c[0])).toEqual(['/api/v1/content-appeals/receipt?project_id=p','/api/v1/operations/content-appeals/a/plans/plan/receipt?workspace_id=w','/api/v1/operations/content-appeals/a/finance-case?workspace_id=w']);
  expect(new Headers(f.mock.calls[0][1].headers).get('Idempotency-Key')).toBe('opaque-key');expect(new Headers(f.mock.calls[2][1].headers).get('Idempotency-Key')).toBe('link-key');
});


it.each([['INVALID_CREDENTIALS',false],['AUTH_REQUIRED',true]])('keeps password correction separate from an expired session',async(code,expires)=>{
  const plan:AppealPlan={id:'plan',workspace_id:'w',appeal_id:a.id,expected_version:1,action:'record_opinion',outcome:'OPINION_RECORDED',plan_digest:'digest',expires_at:new Date(Date.now()+60000).toISOString()};
  vi.spyOn(appealApi,'opsList').mockResolvedValue({items:[a]});vi.spyOn(appealApi,'opsDetail').mockResolvedValue(a);vi.spyOn(appealApi,'preview').mockResolvedValue(plan);
  vi.spyOn(appealApi,'execute').mockRejectedValue(new ApiError('需核对身份',401,false,String(code)));const expired=vi.fn();
  await render(<ContentAppealModerator owner="op" workspace="w" onBusy={noop} onExpired={expired}/>);await act(()=>view.querySelector<HTMLButtonElement>('.appeal-rows button')!.click());
  await setValue('.appeal-compose input','ticket:1');await setValue('.appeal-compose textarea','独立意见');await click('预演内容申诉处置');await act(()=>view.querySelector<HTMLInputElement>('input[type=checkbox]')!.click());await setValue('input[type=password]','fixture');await click('确认内容申诉处置');
  expect(expired.mock.calls.length).toBe(expires?1:0);expect(view.querySelector<HTMLInputElement>('input[type=password]')?.value).toBe('');expect(sessionStorage.getItem(recoveryKey('moderator','op','w'))).toBeNull();
});
