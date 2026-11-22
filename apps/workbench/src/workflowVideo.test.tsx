// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { api } from './api';
import { validCatalog } from './modelCatalog';
import { WorkflowRun } from './WorkflowRun';
import { WorkflowEditor } from './WorkflowEditor';
import { workflowApi } from './workflowApi';
import { inputEdge, templateGraph, workflowModels } from './workflows';
import type { WorkflowExecution } from './workflows';
import type { Bootstrap, Task, TaskInput, FirstFrame, Model } from './types';
const cap={cancel_supported:false,max_count:1,max_prompt_bytes:4096,parameter_presets:[]};
const video:Model={id:'film',name:'镜头',kind:'video',description:'',enabled:true,version:'v2',capabilities:{...cap,video_first_frame_supported:true,parameter_presets:[{id:'wide',name:'横向',aspect_ratio:'16:9',seconds:5,resolution:'720p'},{id:'portrait',name:'竖向',aspect_ratio:'9:16',seconds:10,resolution:'1080p'}]}};
const b:Bootstrap={project:{id:'p',name:'项目'},workspace:{id:'w',name:'工作区'},mode:'demo',storage:'postgres',auth_enabled:true,models:[
  {id:'t',kind:'chat',name:'文本',description:'',enabled:true,version:'v1',capabilities:cap},
  {id:'i',kind:'image',name:'图像',description:'',enabled:true,version:'v1',capabilities:{...cap,parameter_presets:[{id:'square',name:'方形',aspect_ratio:'1:1'}]}},video]};
const graph=templateGraph(b,true);
const frame:FirstFrame={asset_id:'still',artifact_id:'a',task_id:'image-task',revision:2,content_version:'s2'};
const run:WorkflowExecution={id:'wf',project_id:'p',definition_id:'d',definition_version:1,version:8,graph,input:'主题',status:'VIDEO_READY',candidates:[frame],selected:frame};
const movie:TaskInput={kind:'video',operation:'first_frame',project_id:'p',model_id:'film',model_version:'v2',inputs:{prompt:'已确认文字',workflow_id:'wf',first_frame:frame},parameters:{count:1,seconds:5,aspect_ratio:'16:9',resolution:'720p'}};
const noop=()=>{};
let view:HTMLDivElement, root:ReturnType<typeof createRoot>;
beforeEach(()=>{vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT',true);view=document.createElement('div');root=createRoot(view);});
afterEach(async()=>{await act(()=>root.unmount());vi.restoreAllMocks();vi.unstubAllGlobals();});
const renderRun=(value:WorkflowExecution, boot=b, report=noop)=>act(()=>root.render(<WorkflowRun b={boot} run={value} locked={false} expired={noop} perform={async(_,send)=>{await send('original-video-key')}} refresh={noop} openVersion={noop} report={report}/>));
const click=async(label:string)=>{const e=[...view.querySelectorAll('button')].find(e=>e.textContent===label);expect(e,label).toBeTruthy();await act(()=>e!.click());};
it('requires explicit model capability and keeps both graph versions separate',()=>{
  expect(templateGraph(b).nodes).toHaveLength(3);expect(graph.template_version).toBe('2');expect(graph.nodes).toHaveLength(4);expect(graph.edges).toHaveLength(4);
  expect(inputEdge(graph.nodes,graph.nodes[3],'pick','first_frame')).toEqual(graph.edges[2]);
  expect(inputEdge(graph.nodes,graph.nodes[3],'brief','prompt')).toEqual(graph.edges[3]);expect(inputEdge(graph.nodes,graph.nodes[3],'brief','first_frame')).toBeUndefined();
  const unsupported={...video,capabilities:{...video.capabilities!,video_first_frame_supported:false}};
  expect(workflowModels({...b,models:[unsupported]},'video')).toEqual([]);
  expect(validCatalog({version:'v',models:[{...b.models[1],capabilities:{...b.models[1].capabilities,video_first_frame_supported:true}}]})).toBe(false);
});
it('edits the full video duration preset and independently disconnects its first frame',async()=>{
  const update=vi.fn();await act(()=>root.render(<WorkflowEditor b={b} graph={graph} update={update} disabled={false} report={noop}/>));
  const videoNode=view.querySelectorAll<HTMLButtonElement>('.wf-track button')[3];await act(()=>videoNode.click());
  const field=(label:string)=>[...view.querySelectorAll('.wf-inspector label')].find(el=>el.textContent?.startsWith(label))!.querySelector('select')!;
  const change=async(el:HTMLSelectElement,value:string)=>act(()=>{el.value=value;el.dispatchEvent(new Event('change',{bubbles:true}));});
  await change(field('视频规格与时长'),'portrait');
  expect(update.mock.calls[0][0].nodes[3].parameters).toEqual({count:1,aspect_ratio:'9:16',seconds:10,resolution:'1080p'});
  await change(field('首帧连接'),'');expect(update.mock.calls[1][0].edges).toHaveLength(3);expect(update.mock.calls[1][0].edges).toContainEqual(graph.edges[3]);
});
it.each([{...frame,content_version:'other'},{...frame,artifact_id:'other'},{...frame,revision:3}])('blocks mismatched first-frame identity before an execution or quote',async other=>{
  vi.spyOn(workflowApi,'input').mockResolvedValue({resource_type:'task',task_input:{...movie,inputs:{...movie.inputs,first_frame:other}}});
  const issue=vi.fn(),execute=vi.spyOn(workflowApi,'execute');await renderRun(run,b,issue);
  expect(issue).toHaveBeenCalled();expect([...view.querySelectorAll('button')].some(e=>e.textContent==='确认生成视频')).toBe(false);expect(execute).not.toHaveBeenCalled();
});
it('gets a separate video quote with the complete frozen first frame and executes only on explicit confirmation',async()=>{
  const body={resource_type:'task' as const,task_input:movie};vi.spyOn(workflowApi,'input').mockResolvedValue(body);
  const pricing=vi.spyOn(api,'billingQuote').mockResolvedValue({id:'paid-video',workspace_id:'w',project_id:'p',resource_type:'task',model_id:'film',model_version:'v2',provider_mode:'demo',unit:'CREDIT',scale:6,reserve_micros:'20',expires_at:new Date(Date.now()+60000).toISOString(),created_at:'',price_version:'pr',policy_version:'p1',basis:'delivered_artifact',input_per_million_micros:'0',output_per_million_micros:'0',artifact_micros:'20',max_input_tokens:0,max_output_tokens:0,requested_units:1});
  const execute=vi.spyOn(workflowApi,'execute').mockResolvedValue(run);
  await renderRun(run,{...b,billing:{enabled:true,mode:'credits',unit:'CREDIT',scale:6}});expect(pricing).not.toHaveBeenCalled();
  await click('获取本步报价');expect(pricing).toHaveBeenCalledWith('p',body,expect.any(AbortSignal));expect(execute).not.toHaveBeenCalled();
  await click('确认额度并提交');expect(execute).toHaveBeenCalledWith('p','original-video-key','wf','movie',8,'paid-video');
});
it('previews only confirmed artifacts of the permitted private video task',async()=>{
  const task={...movie,id:'vid',status:'SUCCEEDED',version:1,project_id:'p'} as unknown as Task;
  const value={...run,status:'COMPLETED',video_task_id:'vid',video_task:task,video_artifacts:[{...frame,asset_id:'video-asset',task_id:'vid'},frame]};
  await renderRun(value);expect(view.querySelectorAll('video')).toHaveLength(1);
  expect(view.querySelector('video')!.getAttribute('src')).toBe('/api/v1/library/video-asset/content?project_id=p');
  await renderRun({...value,video_task:undefined});expect(view.querySelector('video')).toBeNull();
  await renderRun({...value,status:'REVIEW'});expect(view.querySelector('video')).toBeNull();
});


it('keeps original selection after a metadata-only revision and uses its frozen input',async()=>{
  vi.spyOn(workflowApi,'input').mockResolvedValue({resource_type:'task',task_input:movie});
  await renderRun({...run,candidates:[{...frame,revision:3}]});
  expect(view.querySelector('.wf-selected a')?.getAttribute('href')).toBe('/api/v1/library/still/content?project_id=p');
  expect(view.textContent).toContain('选图时资产修订 v2');
  expect(view.textContent).toContain('确认生成视频');
});
it.each([{candidates:[]},{candidates:[{...frame,content_version:'replaced'}]},{candidates:[{...frame,artifact_id:'different'}]}])('hides private links and execution for an unavailable original selection',async ({candidates})=>{
  const input=vi.spyOn(workflowApi,'input');
  await renderRun({...run,candidates});
  expect(view.textContent).toContain('原选图内容目前不可读取');
  expect(view.querySelector('.wf-selected img,.wf-selected a')).toBeNull();
  expect([...view.querySelectorAll('button')].some(e=>e.textContent==='确认生成视频')).toBe(false);expect(input).not.toHaveBeenCalled();
});

it('rejects quoted inputs attributed to a previous workflow attempt',async()=>{
  const read=vi.spyOn(workflowApi,'input').mockResolvedValue({resource_type:'task',task_input:{...movie,workflow_attempt_id:'former'}});
  const issue=vi.fn(), quote=vi.spyOn(api,'billingQuote');
  await renderRun({...run,source_workflow_id:'original',attempt:2},b,issue);
  expect(issue).toHaveBeenCalled();expect(quote).not.toHaveBeenCalled();
  expect([...view.querySelectorAll('button')].some(e=>e.textContent==='确认生成视频')).toBe(false);
  read.mockResolvedValue({resource_type:'task',task_input:{...movie,workflow_attempt_id:run.id}});
  await click('重读节点输入');
  expect([...view.querySelectorAll('button')].some(e=>e.textContent==='确认生成视频')).toBe(true);
});
