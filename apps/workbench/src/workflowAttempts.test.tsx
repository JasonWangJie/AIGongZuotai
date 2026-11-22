// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { ApiError } from './api';
import { WorkflowAttemptControls } from './WorkflowAttemptControls';
import { WorkflowWorkbench } from './WorkflowWorkbench';
import { affectedWorkflowNodes } from './workflowAttempts';
import { workflowApi } from './workflowApi';
import type { RerunPreview } from './workflowApi';
import { templateGraph, workflowScope } from './workflows';
import type { WorkflowExecution, WorkflowIntent } from './workflows';
import type { Bootstrap } from './types';

const b:Bootstrap={workspace:{id:'w',name:'工作区'},project:{id:'p',name:'项目'},auth_enabled:true,mode:'demo',storage:'postgres',models:[]};
const graph=templateGraph(b,true), saved:WorkflowExecution={id:'original',project_id:'p',definition_id:'d',definition_version:4,graph,input:'原主题',version:13,status:'COMPLETED',
  text_run_id:'original-text',image_task_id:'original-image',video_task_id:'original-video',candidates:[],selected:{asset_id:'original-pick',artifact_id:'a',task_id:'original-image',revision:3,content_version:'c'},attempt:1};
const next={...saved,id:'second',status:'IMAGE_READY',source_workflow_id:'original',from_node:'frames',attempt:2};
const preview:RerunPreview={source_workflow_id:'original',source_version:13,from_node:'frames',affected_nodes:['frames','pick','movie'],inherited_nodes:['brief']};
const noop=()=>{};
let view:HTMLDivElement,root:ReturnType<typeof createRoot>;
beforeEach(()=>{vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT',true);sessionStorage.clear();view=document.createElement('div');root=createRoot(view);});
afterEach(async()=>{await act(()=>root.unmount());vi.restoreAllMocks();vi.unstubAllGlobals();});
const render=(r:WorkflowExecution=saved)=>act(()=>root.render(<WorkflowAttemptControls run={r} expired={noop} locked={false} openSource={noop} perform={async(_,send)=>{await send('same-key');}}/>));
async function click(label:string){const e=[...view.querySelectorAll('button')].find(e=>e.textContent===label);expect(e,label).toBeTruthy();await act(()=>e!.click());}
async function pick(id:string){const e=view.querySelector('select')!;await act(()=>{e.value=id;e.dispatchEvent(new Event('change',{bubbles:true}));});}
it('derives downstream scope from explicit edges and does not preview or mutate on opening a run',async()=>{
  expect(affectedWorkflowNodes(saved,'pick').map(n=>n.id)).toEqual(['pick','movie']);expect(affectedWorkflowNodes(saved,'unknown')).toEqual([]);
  const read=vi.spyOn(workflowApi,'preview'),post=vi.spyOn(workflowApi,'rerun');await render();await click('查看重跑与取消');
  expect(read).not.toHaveBeenCalled();expect(post).not.toHaveBeenCalled();expect(view.textContent).toContain('original-image');
});
it('checks the server preview before opening a new attempt and keeps the changed prompt separate from original results',async()=>{
  vi.spyOn(workflowApi,'preview').mockResolvedValue(preview);const write=vi.spyOn(workflowApi,'rerun').mockResolvedValue(next);
  await render();await click('查看重跑与取消');await pick('frames');expect(view.querySelector('.wf-range')?.textContent).toContain('original-pick');
  const e=view.querySelector('textarea')!;await act(()=>{Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype,'value')!.set!.call(e,'新提示词');e.dispatchEvent(new Event('input',{bubbles:true}));});
  expect(write).not.toHaveBeenCalled();await click('确认创建新尝试');
  expect(write).toHaveBeenCalledWith('p','same-key','original',{expected_version:13,from_node:'frames',prompt:'新提示词'});expect(saved.graph.nodes[1].prompt).not.toBe('新提示词');
});
it.each([{...preview,source_version:12},{...preview,source_workflow_id:'outside'},{...preview,affected_nodes:['pick']},{...preview,inherited_nodes:['brief','frames']}])('blocks changed or incomplete server preview',async value=>{
  vi.spyOn(workflowApi,'preview').mockResolvedValue(value);await render();await click('查看重跑与取消');await pick('frames');expect(view.textContent).toContain('预览与当前运行范围不一致');expect(view.textContent).not.toContain('确认创建新尝试');
});
it('requires a second explicit cancellation decision and binds it to the current resource version',async()=>{
  const write=vi.spyOn(workflowApi,'cancel').mockResolvedValue({...saved,status:'CANCELLED'});
  await render({...saved,status:'WAITING_APPROVAL'});await click('查看重跑与取消');await click('准备取消此运行');expect(write).not.toHaveBeenCalled();
  await render({...saved,status:'VIDEO_READY',version:14});expect(view.textContent).not.toContain('确认取消此运行');await click('准备取消此运行');await click('确认取消此运行');
  expect(write).toHaveBeenCalledWith('p','same-key','original',14);
});
it('uses operation-specific read-only receipt paths with a header key',async()=>{
  const fetcher=vi.fn(async()=>Response.json(next));vi.stubGlobal('fetch',fetcher);
  await workflowApi.receipt('p',{action:'rerun',key:'opaque-key',run:'original',node:'frames'});await workflowApi.receipt('p',{action:'cancel',key:'opaque-key',run:'original'});
  const calls=fetcher.mock.calls as unknown as [string,RequestInit][];
  expect(calls.map(c=>c[0])).toEqual(['/api/v1/projects/p/workflow-runs/original/rerun/receipt','/api/v1/projects/p/workflow-runs/original/cancel/receipt']);
  expect(calls.every(([url,init])=>!url.includes('opaque-key') && (init.method||'GET')==='GET' && new Headers(init.headers).get('Idempotency-Key')==='opaque-key')).toBe(true);
});
it('keeps an unknown rerun blocked on its original key when another source appears in the receipt',async()=>{
  vi.spyOn(workflowApi,'definitions').mockResolvedValue({items:[]});vi.spyOn(workflowApi,'runs').mockResolvedValue({items:[]});
  const intent:WorkflowIntent={action:'rerun',key:'original-key',run:'original',node:'frames'};sessionStorage.setItem(workflowScope(b,'u'),JSON.stringify(intent));
  vi.spyOn(workflowApi,'receipt').mockResolvedValue({...next,source_workflow_id:'wrong'});
  const write=vi.spyOn(workflowApi,'rerun');
  await act(()=>root.render(<WorkflowWorkbench b={b} visible suspended={false} expired={noop} onDirty={noop} user="u"/>));await click('只读核对原回执');
  expect(JSON.parse(sessionStorage.getItem(workflowScope(b,'u'))!)).toEqual(intent);expect(view.textContent).toContain('重跑回执与原运行或起点不一致');expect(write).not.toHaveBeenCalled();
});


it('coalesces a scoped event into reads without replacing unsaved graph edits or issuing POST',async()=>{
  vi.useFakeTimers();
  try {
    const definitions=vi.spyOn(workflowApi,'definitions').mockResolvedValue({items:[]}),runs=vi.spyOn(workflowApi,'runs').mockResolvedValue({items:[]}),write=vi.spyOn(workflowApi,'save');
    const show=(v:number)=>act(()=>root.render(<WorkflowWorkbench b={b} visible suspended={false} expired={noop} onDirty={noop} user="u" refreshVersion={v}/>));
    await show(0);
    const template=[...view.querySelectorAll('button')].find(e=>e.textContent?.includes('使用模板'))!;
    await act(()=>template.click());
    const field=view.querySelector('.wf-editor>label input') as HTMLInputElement;
    await act(()=>{Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value')!.set!.call(field,'保留未保存编辑');field.dispatchEvent(new Event('input',{bubbles:true}));});
    await show(1);await show(2);await act(()=>vi.advanceTimersByTimeAsync(260));
    expect(definitions).toHaveBeenCalledTimes(2);expect(runs).toHaveBeenCalledTimes(2);expect(field.value).toBe('保留未保存编辑');expect(write).not.toHaveBeenCalled();
  } finally {vi.useRealTimers();}
});
it('permits explicit cancellation of a review state and does not expose direct retry',async()=>{
  await render({...saved,status:'REVIEW'});await click('查看重跑与取消');expect(view.textContent).not.toContain('重跑起点');await click('准备取消此运行');
  expect(view.textContent).toContain('已结算金额保留');expect(view.textContent).toContain('待核实冻结按原账本处理');expect(view.textContent).toContain('确认取消此运行');
});

it('keeps an unknown cancellation read-only during a receipt outage and shows no resubmit advice',async()=>{
  vi.spyOn(workflowApi,'definitions').mockResolvedValue({items:[]});vi.spyOn(workflowApi,'runs').mockResolvedValue({items:[]});
  const intent:WorkflowIntent={action:'cancel',key:'cancel-key',run:'original'};
  sessionStorage.setItem(workflowScope(b,'u'),JSON.stringify(intent));
  vi.spyOn(workflowApi,'receipt').mockRejectedValue(new ApiError('收据服务暂不可用',503,true,undefined,'trace',Date.now()+10000));
  const write=vi.spyOn(workflowApi,'cancel');
  await act(()=>root.render(<WorkflowWorkbench b={b} visible suspended={false} expired={noop} onDirty={noop} user="u"/>));
  await click('只读核对原回执');
  expect(view.textContent).toContain('后续节点是否已停止以原回执为准');expect(view.textContent).toContain('再次核对回执');
  expect(view.textContent).not.toContain('原请求重试');expect(view.textContent).not.toContain('手动重试');
  expect(sessionStorage.getItem(workflowScope(b,'u'))).toBe(JSON.stringify(intent));expect(write).not.toHaveBeenCalled();
});
