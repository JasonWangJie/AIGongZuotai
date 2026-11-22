import { uiText } from './locale';
import { useState } from 'react';
import { Icon } from './components';
import { editPublicState, editSetupFiles, initialEditSetup } from './imageEditSetup';
import type { EditSetup, EditSetupPreset } from './imageEditSetup';
import type { Bootstrap } from './types';
import './image-edit-setup.css';

function download(name:string, text:string, mime:string) {
  const url=URL.createObjectURL(new Blob([text],{type:mime})), a=document.createElement('a');
  a.href=url; a.download=name; document.body.append(a); a.click(); a.remove(); window.setTimeout(()=>URL.revokeObjectURL(url),1000);
}
type Props = {b:Bootstrap; refresh:()=>void; loading:boolean};
export function ImageEditSetupPage({b,refresh,loading}:Props) {
  const [config,setConfig]=useState(initialEditSetup), [checked,setChecked]=useState(false);
  const [target,setTarget]=useState<'merge'|'isolated'>('merge');
  const images=b.models.filter(m=>m.kind==='image');
  const files=editSetupFiles(config), existing=b.models.some(m=>m.id===config.id);
  function set<K extends keyof EditSetup>(key:K,value:EditSetup[K]) {setConfig(c=>({...c,[key]:value}));}
  function field(key:'gateway'|'hosts'|'credential'|'id'|'name'|'upstream', label:string, hint?:string) {
    return <label>{label}<input value={config[key]} autoComplete="off" spellCheck={false} placeholder={hint} onChange={e=>set(key,e.target.value)}/></label>;
  }
  function number(key:'references'|'count'|'promptBytes',label:string,max:number) {
    return <label>{label}<input type="number" min="1" max={max} step="1" value={Number.isNaN(config[key])?'':config[key]} onChange={e=>set(key,e.target.valueAsNumber)}/></label>;
  }
  function presetField(i:number,key:keyof EditSetupPreset,label:string) {
    const p=config.presets[i];return <label key={key}>{label}<input value={p[key]} onChange={e=>set('presets',config.presets.map((v,n)=>n===i?{...v,[key]:e.target.value}:v))}/></label>;
  }
  function addPreset() {
    let n=config.presets.length+1;
    while(config.presets.some(p=>p.id==='preset-'+n)) n++;
    set('presets',[...config.presets,{id:'preset-'+n,name:uiText('新规格'),aspect_ratio:'',size:'',resolution:''}]);
  }
  const conflict=target==='merge' && existing;
  const available=checked && !files.errors.length && !conflict && (target !== 'merge' || b.models.length < 32);
  return <section className="image-edit-setup"><header><h1>{uiText("图片编辑配置")}</h1><p>{uiText("准备服务端配置文件，重启后从当前项目核对已加载的图片编辑能力。")}</p></header>
    <div className="ies-grid"><div className="ies-form"><fieldset><legend>{uiText("媒体渠道")}</legend>
    {field('gateway',uiText('异步媒体服务根地址'),'https://media.example.com')}
    {field('hosts',uiText('可信结果或跳转域名'),'cdn.example.com, images.example.com')}<small>{uiText("同网关域名自动允许。其他主机需逐个填写完整域名。")}</small>
    {field('credential',uiText('服务端凭据标识'),'media-primary')}<small>{uiText("这是用于识别同一服务凭据的稳定名称。密钥仅在服务端设置。")}</small>
    </fieldset><fieldset><legend>{uiText("图片模型")}</legend><div className="ies-fields">{field('id',uiText('工作台模型 ID'))}{field('name',uiText('公开显示名'))}</div>
    {field('upstream',uiText('上游已开通的编辑模型'),uiText('填写供应商提供的模型标识'))}
    <div className="ies-fields">{number('references',uiText('参考图上限'),4)}{number('count',uiText('出图数上限'),4)}{number('promptBytes',uiText('提示词字节上限'),16000)}</div>
    </fieldset><fieldset><legend>{uiText("可用规格组合")}</legend>
    {config.presets.map((p,i)=><div key={i} className="ies-preset"><div className="ies-preset-head"><strong>{p.name || uiText('图片规格')}</strong><button type="button" className="text-button" disabled={config.presets.length===1} onClick={()=>set('presets',config.presets.filter((_,n)=>n!==i))} aria-label={uiText('移除规格 ')+(i+1)}>{uiText("移除")}</button></div>
    <div className="ies-fields">{presetField(i,'id',uiText('规格 ID'))}{presetField(i,'name',uiText('规格名称'))}{presetField(i,'aspect_ratio',uiText('画幅比例'))}{presetField(i,'size',uiText('size（选填）'))}{presetField(i,'resolution',uiText('resolution（选填）'))}</div></div>)}
    <button type="button" className="button secondary" disabled={config.presets.length>=32} onClick={addPreset}><Icon name="plus" size={14}/>{uiText("添加规格")}</button></fieldset>
    <fieldset><legend>{uiText("导出与应用")}</legend><label>{uiText("目标环境")}<select value={target} onChange={e=>setTarget(e.target.value as typeof target)}>
    <option value="merge">{uiText("加入现有服务")}</option><option value="isolated">{uiText("建立隔离测试服务")}</option></select></label>
    <p>{target==='merge'?uiText('把新增模型条目合并到服务器原目录的 models 数组。保留现有模型、模型路径、网关和凭据标识；仅将新结果域名追加到原白名单。'):uiText('将示例目录另存至独立实例的 config/，将环境片段合并到该实例的环境配置。')}</p>
    <button type="button" className="button primary" onClick={()=>setChecked(true)}>{uiText("校验并预览配置")}</button>
    {checked && <div aria-live="polite">{files.errors.map(e=><p className="ies-error" key={e}>{e}</p>)}{conflict && <p className="ies-error">{uiText("当前目录已使用此公开模型 ID。为新增模型选择独立 ID。")}</p>}{target==='merge' && b.models.length>=32 && <p className="ies-error">{uiText("当前目录已经达到 32 个模型上限。")}</p>}</div>}
    {available && <div className="ies-export"><p>{uiText("校验通过。下载的文件保存在本机，服务配置需手动合并。")}</p>
    {target==='merge' && <><button type="button" className="button secondary" onClick={()=>download('image-edit.model-entry.json',files.model!,'application/json')}>{uiText("下载新增模型条目")}</button>
    <details><summary>{uiText("查看要合并的模型条目")}</summary><pre>{files.model}</pre></details></>}
    <button type="button" className="button secondary" onClick={()=>download(files.filename!,files.catalog!,'application/json')}>{uiText("下载独立目录示例")}</button>
    <button type="button" className="button secondary" onClick={()=>download('image-edit.env.example',files.env!,'text/plain')}>{target==='merge'?uiText('下载隔离实例环境片段'):uiText('下载环境片段')}</button>
    <details><summary>{uiText("查看隔离实例环境片段")}</summary><pre>{files.env}</pre></details></div>}
    <p>{uiText("在服务器私有环境中设置")} <code>ASYNC_IMAGE_API_KEY</code>{uiText("。启用持久交付时需保留独立的")} <code>MEDIA_DELIVERY_KEY</code> {uiText("和私有目录。手动重启服务后刷新本页公开目录，再由你在图像工作室上传参考图、确认规格并进行受控实测。")}</p>
    </fieldset></div><aside><h2>{uiText("当前公开目录")}</h2><p>{b.mode==='live'?uiText('真实渠道模式'):uiText('本地演示模式')} · {images.length} {uiText("个图片模型")}</p>
    {images.map(m=><article key={m.id}><strong>{m.name}</strong><code>{m.id}</code><span>{editPublicState(m)}</span>{m.version && <small>{uiText("规格版本")} {m.version}</small>}</article>)}
    {!images.length && <p>{uiText("当前服务尚未公布图片模型。")}</p>}<button className="button secondary" disabled={loading} onClick={refresh}><Icon name="refresh" size={14}/>{loading?uiText('正在读取目录…'):uiText('刷新公开目录')}</button>
    <p>{uiText("目录来自当前服务。真实编辑效果需使用已开通模型另行验收。")}</p></aside></div></section>;
}

