// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { api, ApiError } from './api';
import { PanelVideoWorkbench } from './PanelVideoWorkbench';
import { ComicWorkbench } from './ComicWorkbench';
import { comicApi, type ComicVersion } from './comics';
import { panelVideoApi, matchesReplacement, sameSpec, ticketSlot, validPreview, videoSpec } from './panelVideos';
import type { Attempt, Preview } from './panelVideos';
import { timelineApi, type TimelineVersion } from './videoTimeline';
import type { Bootstrap, Model } from './types';
import type { AssetEntry } from './libraryState';
import type { BillingQuote } from './billing';

const scope = { project: 'project', version: 2, panel: 'panel' }, owner = 'author';
const preset = { id: 'wide', name: '宽屏', aspect_ratio: '16:9', seconds: 5, resolution: '720p', size: '1280x720' };
const model: Model = { id: 'movie', version: '2', kind: 'video', name: '本机视频', enabled: true, description: '',
  capabilities: { cancel_supported:false, max_count:1, max_prompt_bytes:4000, parameter_presets:[preset] } };
const boot: Bootstrap = { project:{id:scope.project,name:'项目'}, workspace:{id:'ws',name:'空间'},
  mode:'demo', storage:'postgres', models:[model], user:{id:owner, name:'作者', email:''} };
const spec = videoSpec(scope, model, preset);
const preview: Preview = { ...spec, attempt:1, prompt:'固定镜头正文', billing_enabled:false };
const attempt: Attempt = { ...spec, project_id:scope.project, comic_version:2, panel_id:'panel', attempt:1,
  task_id:'task',status:'SUCCEEDED',created_at:'now' };
const base: TimelineVersion = { id:'timeline',project_id:scope.project,version:1,created_at:'now',
  document:{ title:'剪辑',clips:[
    {id:'first',title:'镜一',asset_id:'old-a',asset_revision:1,in_ms:0,out_ms:750},
    {id:'second',title:'镜二',asset_id:'old-b',asset_revision:1,in_ms:0,out_ms:750},
  ] } };
const result: TimelineVersion = { ...base, version:2, document:{ ...base.document,
  clips:[base.document.clips[0],{...base.document.clips[1],asset_id:'video',asset_revision:7,in_ms:500,out_ms:1750}] } };
const asset = {id:'video',project_id:scope.project,source:'generated',task_id:'task',artifact_id:'artifact',
  kind:'video',name:'原成片',tags:[],state:'active',revision:7,content_type:'video/mp4',content_url:'/authorized'} as unknown as AssetEntry;
const lost = new ApiError('回执未知',0,true);
let holder: HTMLDivElement, root: ReturnType<typeof createRoot>;
beforeEach(() => {
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT',true); sessionStorage.clear();
  holder = document.createElement('div'); document.body.append(holder); root = createRoot(holder);
  vi.spyOn(panelVideoApi,'list').mockResolvedValue({items:[]});
});
afterEach(async () => {
  await act(() => root.unmount()); holder.remove(); vi.restoreAllMocks(); vi.unstubAllGlobals();
});
const draw = (who = owner, version = 2, panel = 'panel') => act(() => root.render(
  <PanelVideoWorkbench key={JSON.stringify([who,version,panel])} owner={who} bootstrap={boot}
    scope={{...scope,version,panel}} title="第一镜" onExpired={() => {}}/>));
function button(name: string) { return [...holder.querySelectorAll('button')].find(el => el.textContent === name); }
async function click(name: string) {
  const b = button(name); expect(b, name).toBeDefined(); expect(b!.matches(':disabled')).toBe(false);
  await act(() => b!.click());
}
async function input(label: string, value: string) {
  const l = [...holder.querySelectorAll('label')].find(el => el.firstChild?.textContent === label);
  const control = l?.querySelector('select,input') as HTMLInputElement | HTMLSelectElement;
  expect(control, label).toBeTruthy();
  await act(() => {
    const select = control instanceof HTMLSelectElement;
    Object.getOwnPropertyDescriptor(select ? HTMLSelectElement.prototype : HTMLInputElement.prototype,'value')!.set!.call(control,value);
    control.dispatchEvent(new Event(select ? 'change' : 'input',{bubbles:true}));
  });
}
function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>(r => {resolve = r;});
  return {promise,resolve};
}

it('keeps the complete fixed preset and rejects mismatched model, money, source or expiry', () => {
  expect(spec.parameters).toEqual({aspect_ratio:'16:9',count:1,seconds:5,size:'1280x720',resolution:'720p'});
  const billing = { ...boot, billing:{enabled:true,mode:'credits',unit:'CREDIT',scale:6} } as Bootstrap;
  const quote = {id:'q', workspace_id:'ws',project_id:scope.project,model_id:'movie',model_version:'2',provider_mode:'demo',
    unit:'CREDIT',scale:6,reserve_micros:'200000',resource_type:'task',expires_at:new Date(Date.now()+60000).toISOString()} as BillingQuote;
  const paid = {...preview,billing_enabled:true,quote};
  expect(validPreview(paid,spec,billing)).toBe(true);
  for (const patch of [{project_id:'other'}, {model_version:'3'}, {reserve_micros:'NaN'}, {expires_at:'2000-01-01'}, {provider_mode:'live'}]) {
    expect(validPreview({...paid,quote:{...quote,...patch} as BillingQuote},spec,billing)).toBe(false);
  }
  expect(sameSpec(preview,{...spec,parameters:{...spec.parameters,seconds:10}})).toBe(false);
});

it('creates only on confirmation and reads an uncertain attempt exclusively using its quote key',async () => {
  const q = vi.spyOn(panelVideoApi,'quote').mockResolvedValue(preview);
  const post = vi.spyOn(panelVideoApi,'generate').mockRejectedValue(lost);
  const get = vi.spyOn(panelVideoApi,'receipt')
    .mockResolvedValueOnce({...attempt,panel_id:'other'}).mockResolvedValue(attempt);
  await draw(); await click('预览提示词与报价');
  expect(post).not.toHaveBeenCalled(); expect(holder.textContent).toContain(preview.prompt);
  await click('确认生成此镜视频');
  const original = post.mock.calls[0][1]; expect(original).toMatchObject({expected_attempt:1,key:q.mock.calls[0][1],spec});
  await draw('other'); expect(button('按原键核对生成')).toBeUndefined();
  await draw(owner,2,'other'); expect(button('按原键核对生成')).toBeUndefined();
  await draw(); await click('按原键核对生成'); expect(button('按原键核对生成')).toBeDefined();
  await click('按原键核对生成'); expect(holder.textContent).toContain('已核对生成回执');
  expect(post).toHaveBeenCalledTimes(1); expect(get.mock.calls).toEqual([[scope,original.key],[scope,original.key]]);
  expect(sessionStorage.getItem(ticketSlot(owner,scope))).toBeNull();
});

it('prevents a late quote from a different fixed version from enabling the new selection',async () => {
  const pending = deferred<Preview>(); vi.spyOn(panelVideoApi,'quote').mockReturnValue(pending.promise);
  await draw(); await click('预览提示词与报价'); await draw(owner,3);
  await act(() => pending.resolve(preview));
  expect(holder.textContent).not.toContain(preview.prompt); expect(button('确认生成此镜视频')).toBeUndefined();
});

it('offers generation only for the visible, confirmed and unedited comic within the authenticated project', async () => {
  const book: ComicVersion = { project_id:scope.project,version:2,created_at:'now',confirmed:true,
    document:{title:'分镜',story:'故事',characters:[{id:'actor',name:'角色',description:''}],
      scenes:[{id:'scene',name:'场景',description:''}],
      panels:[{id:scope.panel,title:'镜头',scene_id:'scene',character_ids:['actor'],action:'行走',dialogue:''}]} };
  const current = vi.spyOn(comicApi,'current').mockResolvedValueOnce({...book,confirmed:false}).mockResolvedValue(book);
  vi.spyOn(comicApi,'history').mockResolvedValue({items:[]});
  const drawBook = (key: string, show = true, identity = owner) => act(() => root.render(
    <ComicWorkbench key={key} owner={identity} project={scope.project} enabled visible={show} bootstrap={boot} onExpired={()=>{}} onDirty={()=>{}}/>));
  await drawBook('unconfirmed'); expect(button('预览提示词与报价')).toBeUndefined();
  await drawBook('confirmed'); expect(button('预览提示词与报价')).toBeDefined();
  await drawBook('confirmed',false); expect(button('预览提示词与报价')).toBeUndefined();
  await drawBook('confirmed'); await input('镜头标题','未保存的新标题'); expect(button('预览提示词与报价')).toBeUndefined();
  await drawBook('foreign',true,'other'); expect(button('预览提示词与报价')).toBeUndefined();
  expect(current).toHaveBeenCalledTimes(3);
});

it('blocks a second generation or a write that cannot first retain its original key',async () => {
  const q = vi.spyOn(panelVideoApi,'quote').mockResolvedValue(preview);
  const post = vi.spyOn(panelVideoApi,'generate');
  await draw(); await click('预览提示词与报价');
  vi.spyOn(Storage.prototype,'setItem').mockImplementation(() => { throw Error('storage unavailable'); });
  await click('确认生成此镜视频'); expect(post).not.toHaveBeenCalled();
  expect(q).toHaveBeenCalledOnce(); expect(holder.querySelector('[role="alert"]')?.textContent).toBeTruthy();
});

it('requires explicitly chosen delivered MP4 and exact clip and reconciles the original replacement',async () => {
  vi.mocked(panelVideoApi.list).mockResolvedValue({items:[attempt]});
  vi.spyOn(api,'library').mockResolvedValue({items:[asset,{...asset,id:'foreign',project_id:'elsewhere'},
    {...asset,id:'other-task',task_id:'different'},{...asset,id:'removed',state:'trashed'}],next_cursor:null,has_more:false,limit:24});
  const summary = {id:base.id,project_id:scope.project,title:'目标稿',version:1,clip_count:2,created_at:'now'};
  vi.spyOn(timelineApi,'list').mockResolvedValue({items:[summary]}); vi.spyOn(timelineApi,'versions').mockResolvedValue({items:[summary]});
  vi.spyOn(timelineApi,'version').mockResolvedValue(base);
  const post = vi.spyOn(panelVideoApi,'replace').mockRejectedValue(lost), get = vi.spyOn(timelineApi,'receipt').mockResolvedValue(result);
  await draw(); await click('读取该次交付成片');
  const options = [...holder.querySelectorAll('option')].map(option => option.value);
  for (const invalid of ['foreign','other-task','removed']) expect(options).not.toContain(invalid);
  await input('交付成片','video'); await input('目标剪辑稿','timeline'); await input('目标镜头','second');
  await input('新入点（毫秒）','500'); await input('新出点（毫秒）','1750'); await click('替换此镜头');
  expect(post).toHaveBeenCalledTimes(1);
  const ticket = post.mock.calls[0][1]; expect(ticket).toMatchObject({clip:'second',timeline:'timeline',version:1,task_id:'task',asset_revision:7,in_ms:500,out_ms:1750});
  expect(matchesReplacement(base,scope,ticket)).toBe(false);
  await draw(owner,3); expect(button('按原键核对替换')).toBeUndefined();
  await draw(); await click('按原键核对替换');
  expect(get).toHaveBeenCalledWith('project',ticket.key); expect(holder.textContent).toContain('已核对替换回执 · v2');
  expect(post).toHaveBeenCalledTimes(1);
});
