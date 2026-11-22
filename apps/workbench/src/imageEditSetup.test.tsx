// @vitest-environment jsdom
import { act } from 'react';
import { createRoot } from 'react-dom/client';
import { expect, it, vi } from 'vitest';
import { ImageEditSetupPage } from './ImageEditSetupPage';
import { editPublicState, editSetupFiles, initialEditSetup } from './imageEditSetup';
import type { Bootstrap, Model } from './types';

const valid = () => ({...initialEditSetup(),gateway:'https://media.example.net',hosts:'CDN.example.net,cdn.example.net',upstream:'vendor/edits-v2'});
it('generates strict catalog v1 and an isolated .env fragment without secret assignments',()=>{
  const files=editSetupFiles(valid()); expect(files.errors).toEqual([]);
  const cat=JSON.parse(files.catalog!);expect(cat.schema_version).toBe(1);expect(cat.models).toEqual([JSON.parse(files.model!)]);
  expect(cat.models[0].capabilities).toMatchObject({image_edit_supported:true,max_reference_images:1,max_count:1,parameter_presets:[{id:'square',name:'方形',aspect_ratio:'1:1'}]});
  expect(files.env).toContain('ASYNC_IMAGE_BASE_URL=https://media.example.net\n');expect(files.env).toContain('ASSET_ALLOWED_HOSTS=cdn.example.net\n');expect(files.env).toContain('MODEL_CATALOG_FILE=config/'+files.filename);
  expect(files.env).not.toMatch(/^(?:ASYNC_IMAGE_API_KEY|MEDIA_DELIVERY_KEY|NEWAPI_|DATABASE_URL)/m);
});
it.each(['https://media.example.net/v1','https://user:password@media.example.net','https://media.example.net?token=private','https://media.example.net#x','http://media.example.net','https://127.0.0.1','https://media.example.net\nFAKE=1'])('rejects non-root or unsafe gateway %s',gateway=>{
  const files=editSetupFiles({...valid(),gateway});expect(files.errors.length).toBeGreaterThan(0);expect(files.env).toBeUndefined();
});
it.each(['https://cdn.example.net','*.example.net','localhost','127.0.0.1','cdn.example.net:443','bad..example.net'])('rejects invalid result host %s',hosts=>{
  expect(editSetupFiles({...valid(),hosts}).catalog).toBeUndefined();
});
it('validates model bounds, deduplicates complete specs and keeps optional size and resolution',()=>{
  for(const override of [{id:'studio-video'},{upstream:'a'.repeat(129)},{references:0},{references:5},{count:2.1},{promptBytes:16001},{credential:'abc\nS=1'}]){
    expect(editSetupFiles({...valid(),...override}).errors.length).toBeGreaterThan(0);
  }
  const value=valid(), p=value.presets[0];expect(editSetupFiles({...value,presets:[p,{...p,id:'new'}]}).model).toBeUndefined();
  expect(editSetupFiles({...value,presets:[{...p,aspect_ratio:'1/1'}]}).model).toBeUndefined();
  expect(JSON.parse(editSetupFiles({...value,presets:[{...p,size:'1024x1024',resolution:'hd'}]}).model!).capabilities.parameter_presets[0]).toMatchObject({size:'1024x1024',resolution:'hd'});
});
it('reports capability from the public catalog even before any service configuration is applied',()=>{
  const m={enabled:true,capabilities:{image_edit_supported:true,max_reference_images:3}} as Model;
  expect(editPublicState(m)).toContain('3 张');expect(editPublicState({...m,enabled:false})).toContain('已停用');expect(editPublicState({...m,capabilities:undefined})).toBe('仅文生图');
});
it('prevents an existing public ID from being exported in merge mode and sends no configuration requests',async()=>{
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT',true);const fetcher=vi.fn();vi.stubGlobal('fetch',fetcher);
  const b={models:[{id:'image-edit',name:'现有模型',kind:'image',enabled:true}],mode:'demo'} as Bootstrap;
  const el=document.createElement('div'),root=createRoot(el);
  try {
    await act(()=>root.render(<ImageEditSetupPage b={b} refresh={()=>{}} loading={false}/>));
    const fill=async(label:string,data:string)=>{
      const field=[...el.querySelectorAll('label')].find(e=>e.textContent?.startsWith(label))!.querySelector('input')!;
      await act(()=>{Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,'value')!.set!.call(field,data);field.dispatchEvent(new Event('input',{bubbles:true}));});
    };
    await fill('异步媒体服务根地址','https://media.example.net');await fill('上游已开通的编辑模型','vendor/edits');
    await act(()=>[...el.querySelectorAll('button')].find(e=>e.textContent==='校验并预览配置')!.click());
    expect(el.textContent).toContain('当前目录已使用此公开模型 ID'); expect(el.textContent).not.toContain('下载新增模型条目');
    await fill('工作台模型 ID','image-collection');expect(el.textContent).toContain('下载新增模型条目'); expect(fetcher).not.toHaveBeenCalled();
    expect(el.querySelector('input[type=password],input[type=file]')).toBeNull();
  } finally {await act(()=>root.unmount());vi.unstubAllGlobals();}
});

