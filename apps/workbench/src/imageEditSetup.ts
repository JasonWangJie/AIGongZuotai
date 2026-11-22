import { uiText } from './locale';
import type { Model } from './types';
export type EditSetupPreset = { id: string; name: string; aspect_ratio: string; size: string; resolution: string };
export type EditSetup = { gateway: string; hosts: string; credential: string; id: string; name: string; upstream: string; references: number; count: number; promptBytes: number; presets: EditSetupPreset[] };
export function initialEditSetup(): EditSetup {
  return {gateway:'', hosts:'', credential:'media-primary', id:'image-edit', name:uiText('图片编辑'), upstream:'', references:1, count:1, promptBytes:8000,
    presets:[{id:'square',name:uiText('方形'),aspect_ratio:'1:1',size:'',resolution:''}]};
}
const idPattern = /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,63}$/;
const parameterPattern = /^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,31}$/;
function nameOK(v: string) {return !!v.trim() && Array.from(v).length <= 80 && !/[\u0000-\u001f\u007f]/.test(v);}
function bounded(n:number,max:number) {return Number.isInteger(n) && n >= 1 && n <= max;}
function dnsName(host:string) {
  return host.length <= 253 && host.includes('.') && !/^[0-9.]+$/.test(host) && !/(^|\.)(localhost|local|internal)$/.test(host) &&
    host.split('.').every(v=>/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(v));
}
export function editSetupFiles(v: EditSetup) {
  const errors:string[] = [];
  let origin = '';
  try {
    const u=new URL(v.gateway);
    if (u.protocol !== 'https:' || !dnsName(u.hostname) || u.username || u.password || u.search || u.hash || !/^https:\/\/[A-Za-z0-9.-]+(?::[0-9]+)?\/?$/.test(v.gateway)) throw Error();
    origin=u.origin;
  } catch {errors.push(uiText('网关填写 HTTPS 服务根地址，不含 /v1、账号、查询参数或路径。'));}
  const hosts = [...new Set(v.hosts.split(/[,，\s]+/).filter(Boolean).map(h=>h.toLowerCase()))];
  if (hosts.some(h=>!dnsName(h))) errors.push(uiText('结果域名填写完整的公开 DNS 主机名，可用逗号或空格分开。'));
  if (!/^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(v.credential)) errors.push(uiText('凭据标识使用 1 至 128 位字母、数字、点、横线、下划线或冒号。'));
  if (!idPattern.test(v.id) || ['studio-chat','studio-video'].includes(v.id)) errors.push(uiText('公开模型 ID 格式无效，或该 ID 已保留给其他类型。'));
  if (!nameOK(v.name)) errors.push(uiText('填写 1 至 80 字的公开显示名。'));
  if (!v.upstream.trim() || new TextEncoder().encode(v.upstream).length>128 || /[\u0000-\u001f\u007f]/.test(v.upstream)) errors.push(uiText('填写渠道实际提供的模型标识，最多 128 字节。'));
  if (!bounded(v.references,4) || !bounded(v.count,4) || !bounded(v.promptBytes,16000)) errors.push(uiText('参考数和出图数均为 1 至 4 的整数，提示词上限为 1 至 16000 字节。'));
  if (!bounded(v.presets.length,32)) errors.push(uiText('图片模型需要 1 至 32 组完整规格。'));
  const ids=new Set<string>(), combos=new Set<string>();
  v.presets.forEach((p,i)=>{
    const combo=JSON.stringify([p.aspect_ratio,p.size,p.resolution]);
    if (!idPattern.test(p.id) || ids.has(p.id) || !nameOK(p.name) || !/^[1-9][0-9]{0,3}:[1-9][0-9]{0,3}$/.test(p.aspect_ratio) || (p.size && !parameterPattern.test(p.size)) || (p.resolution && !parameterPattern.test(p.resolution)) || combos.has(combo)) errors.push(uiText('规格 ')+(i+1)+uiText(' 的 ID、名称、比例或参数组合无效或重复。'));
    ids.add(p.id); combos.add(combo);
  });
  if (errors.length) return {errors};
  const model={id:v.id,kind:'image',name:v.name.trim(),description:uiText('参考图编辑'),enabled:true,upstream_model:v.upstream.trim(),capabilities:{image_edit_supported:true,max_reference_images:v.references,max_count:v.count,max_prompt_bytes:v.promptBytes,parameter_presets:v.presets.map(p=>({id:p.id,name:p.name.trim(),aspect_ratio:p.aspect_ratio,...(p.size && {size:p.size}),...(p.resolution && {resolution:p.resolution})}))}};
  const catalog=JSON.stringify({schema_version:1,models:[model]},null,2)+'\n';
  const filename='models.image-edit.example.json';
  const env=[uiText('# 单模型隔离实例示例。已有环境请保留原目录和模型，把模型条目合入 models[]。'),
    uiText('# 已有媒体服务需保留原网关及稳定凭据标识，ASSET_ALLOWED_HOSTS 与原值取并集。'),
    'PROVIDER_MODE=live','ASYNC_IMAGE_BASE_URL='+origin,'ASYNC_IMAGE_CREDENTIAL_ID='+v.credential,
    'ASSET_ALLOWED_HOSTS='+hosts.join(','),'MODEL_CATALOG_FILE=config/'+filename,
    uiText('# 仅服务器端单独配置 ASYNC_IMAGE_API_KEY。启用持久交付时按原私有配置设置 MEDIA_DELIVERY_KEY。'),''].join('\n');
  return {errors,catalog,env,model:JSON.stringify(model,null,2),filename};
}
export function editPublicState(model:Model) {
  if (!model.enabled) return uiText('模型已停用或渠道未配置');
  if (!model.capabilities?.image_edit_supported) return uiText('仅文生图');
  return uiText('已声明支持编辑 · 最多 ')+model.capabilities.max_reference_images+uiText(' 张参考图');
}

