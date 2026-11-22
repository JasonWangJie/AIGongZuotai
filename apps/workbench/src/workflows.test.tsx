// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { WorkflowEditor } from './WorkflowEditor';
import { WorkflowRun } from './WorkflowRun';
import { api, ApiError } from './api';
import { WorkflowWorkbench } from './WorkflowWorkbench';
import { workflowApi } from './workflowApi';
import { inputEdge, loadIntent, templateGraph, workflowScope } from './workflows';
import type { WorkflowDefinition, WorkflowIntent } from './workflows';
import type { Bootstrap } from './types';

const b: Bootstrap = { project:{id:'p',name:'项目'}, workspace:{id:'w',name:'工作区'}, mode:'demo',storage:'postgres',auth_enabled:true,models: [
  {id:'chat',kind:'chat',name:'文本',description:'',version:'v1',enabled:true,capabilities:{parameter_presets:[],max_count:1,max_prompt_bytes:4000,cancel_supported:true}},
  {id:'image',kind:'image',name:'图像',description:'',version:'v2',enabled:true,capabilities:{parameter_presets:[{id:'square',name:'正方形',aspect_ratio:'1:1',size:'1024'}],max_count:2,max_prompt_bytes:4000,cancel_supported:true}} ]};
const definition: WorkflowDefinition = {id:'d',project_id:'p',version:3,graph:templateGraph(b)};
const projectList = () => { vi.spyOn(workflowApi,'definitions').mockResolvedValue({items:[]}); vi.spyOn(workflowApi,'runs').mockResolvedValue({items:[]}); };
let view: HTMLDivElement, root: ReturnType<typeof createRoot>;
beforeEach(() => { vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT',true); sessionStorage.clear(); view=document.createElement('div'); root=createRoot(view); });
afterEach(async()=>{await act(()=>root.unmount()); vi.restoreAllMocks(); vi.unstubAllGlobals();});
const props = {b,user:'u',visible:true,suspended:false,expired:()=>{},onDirty:()=>{}};
async function click(label:string) {
  const button=[...view.querySelectorAll('button')].find(e=>e.textContent?.includes(label)); expect(button, label).toBeTruthy(); await act(()=>button!.click());
}

it('uses declared model versions, bounded counts and typed edges when instantiating a template',()=>{
  const graph=templateGraph(b),[text,image,pick]=graph.nodes;
  expect(text.model_version).toBe('v1'); expect(image.parameters).toEqual({aspect_ratio:'1:1',size:'1024',count:2});
  expect(inputEdge(graph.nodes,image,text.id)).toEqual(graph.edges[0]); expect(inputEdge(graph.nodes,pick,image.id)).toEqual(graph.edges[1]);
  expect(inputEdge(graph.nodes,text,pick.id)).toBeUndefined(); expect(inputEdge(graph.nodes,pick,text.id)).toBeUndefined();
  graph.nodes[0].title='changed';expect(templateGraph(b).nodes[0].title).not.toBe('changed');
});
it.each([{...b,storage:'memory' as const},{...b,auth_enabled:false}])('shows prerequisites and performs zero workflow requests when unavailable',async disabled=>{
  const network=vi.spyOn(workflowApi,'definitions');await act(()=>root.render(<WorkflowWorkbench {...props} b={disabled}/>));
  expect(view.textContent).toContain('工作流需要持久项目和登录会话');expect(network).not.toHaveBeenCalled();
});
it('keeps an unknown save on its original key across remount and resolves only by a read',async()=>{
  projectList();
  const write=vi.spyOn(workflowApi,'save').mockRejectedValue(new ApiError('连接中断',0,true));
  const read=vi.spyOn(workflowApi,'receipt').mockRejectedValueOnce(new ApiError('尚无回执',404)).mockResolvedValue(definition);
  await act(()=>root.render(<WorkflowWorkbench {...props}/>));await click('使用模板');await click('保存固定版本');
  const saved=loadIntent(workflowScope(b,'u'))!;
  expect(saved.key).toBe(write.mock.calls[0][1]);expect(Object.keys(saved).sort()).toEqual(['action','key']);
  expect(loadIntent(workflowScope(b,'other'))).toBeNull();
  await act(()=>root.render(null));await act(()=>root.render(<WorkflowWorkbench {...props}/>));
  await click('只读核对原回执');expect(loadIntent(workflowScope(b,'u'))).toEqual(saved);
  await click('只读核对原回执');expect(read.mock.calls.map(c=>c[1].key)).toEqual([saved.key,saved.key]);
  expect(write).toHaveBeenCalledTimes(1);expect(loadIntent(workflowScope(b,'u'))).toBeNull();expect(view.textContent).toContain('从 v3 启动');
});
it('scopes receipt reads to the project and keeps the original operation key out of the URL',async()=>{
  const fetcher=vi.fn(async()=>Response.json(definition));vi.stubGlobal('fetch',fetcher);
  for (const intent of [{action:'definition',key:'private'}, {action:'start',key:'private'}, {action:'execute',key:'private',run:'r',node:'n'}, {action:'confirm',key:'private',run:'r',node:'n'}] satisfies WorkflowIntent[]) await workflowApi.receipt('a/b',intent);
  expect(fetcher.mock.calls.map(c=>(c as unknown as [string])[0])).toEqual(['/api/v1/projects/a%2Fb/workflow-definitions/receipt','/api/v1/projects/a%2Fb/workflow-runs/receipt','/api/v1/projects/a%2Fb/workflow-runs/r/nodes/n/receipt','/api/v1/projects/a%2Fb/workflow-runs/r/nodes/n/receipt']);
  for(const call of fetcher.mock.calls){const [,init]=call as unknown as [string,RequestInit]; expect(init.method || 'GET').toBe('GET'); expect(init.headers).toMatchObject({'Idempotency-Key':'private','X-Project-ID':'a/b'});}
});


it('copies only the chosen saved card into the selected workflow node on explicit mapping',async()=>{
  const draft={project_id:'p',revision:2,nodes:[{id:'old',title:'旧卡片',note:'已保存的旧版文字'}],updated_at:null};
  vi.spyOn(api,'draft').mockResolvedValue(draft);
  const update=vi.fn(), graph=templateGraph(b);
  await act(()=>root.render(<WorkflowEditor b={b} graph={graph} update={update} disabled={false} report={vi.fn()}/>));
  await click('读取草稿'); expect(update).not.toHaveBeenCalled();
  const target=view.querySelectorAll('.wf-mapping select')[1] as HTMLSelectElement;
  await act(()=>{target.value='frames';target.dispatchEvent(new Event('change',{bubbles:true}));}); await click('将卡片正文写入节点');
  expect(update.mock.calls[0][0].nodes[1].prompt).toBe(draft.nodes[0].note);
  expect(update.mock.calls[0][0].nodes[0].prompt).toBe(graph.nodes[0].prompt); expect(draft.revision).toBe(2);
});
it('quotes the server-frozen text input and sends the displayed quote only after confirmation',async()=>{
  const paid={...b,billing:{enabled:true,mode:'credits' as const,unit:'CREDIT' as const,scale:6 as const}};
  const input={resource_type:'run' as const,run_input:{project_id:'p',model_id:'chat',model_version:'v1',input:'服务端冻结的提示词'}};
  vi.spyOn(workflowApi,'input').mockResolvedValue(input);
  const quote=vi.spyOn(api,'billingQuote').mockResolvedValue({id:'q',workspace_id:'w',project_id:'p',resource_type:'run',model_id:'chat',model_version:'v1',provider_mode:'demo',unit:'CREDIT',scale:6,reserve_micros:'300',expires_at:new Date(Date.now()+60000).toISOString(),created_at:'',price_version:'p1',policy_version:'p1',basis:'chat_tokens',input_per_million_micros:'10',output_per_million_micros:'20',artifact_micros:'0',max_input_tokens:32,max_output_tokens:32,requested_units:1});
  const run={id:'r',project_id:'p',definition_id:'d',definition_version:3,version:7,input:'主题',status:'TEXT_READY',graph:definition.graph,candidates:[]};
  const send=vi.spyOn(workflowApi,'execute').mockResolvedValue(run);
  await act(()=>root.render(<WorkflowRun b={paid} run={run} locked={false} expired={vi.fn()} perform={async(_,write)=>{await write('stable')}} refresh={vi.fn()} openVersion={vi.fn()} report={vi.fn()}/>));
  expect(quote).not.toHaveBeenCalled();expect(send).not.toHaveBeenCalled();
  await click('获取本步报价'); expect(quote).toHaveBeenCalledWith('p',input,expect.any(AbortSignal));expect(send).not.toHaveBeenCalled();
  await click('确认额度并提交');expect(send).toHaveBeenCalledWith('p','stable','r','brief',7,'q');
});
