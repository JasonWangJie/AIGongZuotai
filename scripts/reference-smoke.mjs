import assert from 'node:assert/strict';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import net from 'node:net';
import { resolve } from 'node:path';
import { binary, root, start, stopOwnedProcess, isolatedReferenceDirectory, isolatedMailEnvironment, isolatedBillingEnvironment, isolatedOperationsEnvironment, isolatedModerationEnvironment } from './lib/process.mjs';

// Dedicated memory/demo process, private upload tree and random loopback port.
// No actual gateway, developer database or credentials enter this scenario.
const port = await new Promise((done, fail) => {
  const probe = net.createServer(); probe.on('error', fail);
  probe.listen(0, '127.0.0.1', () => { const port = probe.address().port; probe.close(() => done(port)); });
});
const base = `http://127.0.0.1:${port}`;
const runtime = resolve(root, '.runtime');
mkdirSync(runtime, { recursive: true });
const referenceDirectory = isolatedReferenceDirectory();
const directory = referenceDirectory.directory;
const metricsToken = randomBytes(32).toString('hex');
const imageBytes = readFileSync(resolve(root, 'internal/studio/testdata/references/source.png'));
const checks = [], started = Date.now();
const pause = ms => new Promise(done => setTimeout(done, ms));
let server, startError;
const request = (path, options = {}) => fetch(base + path, { ...options, signal: AbortSignal.timeout(15000) });
const json = async response => { assert(response.ok, `HTTP ${response.status}: ${await response.clone().text()}`); return response.json(); };
const post = (path, body, key = randomUUID(), project = 'prj_demo') => request(path, { method:'POST', headers:{Origin:base,'Content-Type':'application/json','Idempotency-Key':key,'X-Project-ID':project},body:JSON.stringify(body) });
const upload = (key, bytes = imageBytes, name = 'reference.png', project = 'prj_demo', extraFile = false) => {
  const form = new FormData(); form.append('file',new Blob([bytes],{type:'image/png'}),name);
  if (extraFile) form.append('file',new Blob([bytes],{type:'image/png'}),'extra.png');
  return request('/api/v1/references',{method:'POST',headers:{Origin:base,'Idempotency-Key':key,'X-Project-ID':project},body:form});
};
try {
  server = start(binary,[],{env:{...process.env,...isolatedMailEnvironment,...isolatedBillingEnvironment,...isolatedOperationsEnvironment,...isolatedModerationEnvironment,APP_ADDR:`127.0.0.1:${port}`,APP_WEB_DIR:resolve(root,'apps/workbench/dist'),
    REFERENCE_DIR:directory, ASSET_DIR: referenceDirectory.assetDirectory, ASSET_STORAGE_BACKEND: 'local', S3_FORCE_PATH_STYLE: 'false',MODEL_CATALOG_FILE:'',DATABASE_URL:'',AUTH_MODE:'disabled',PROVIDER_MODE:'demo',METRICS_TOKEN:metricsToken,
    NEWAPI_BASE_URL:'',NEWAPI_API_KEY:'',NEWAPI_CHAT_MODEL:'',ASYNC_IMAGE_BASE_URL:'',ASYNC_IMAGE_API_KEY:'',
    ADMISSION_WORKSPACE_RUNS:'64',ADMISSION_MODEL_RUNS:'32',ADMISSION_WORKSPACE_TASKS:'128',ADMISSION_MODEL_TASKS:'64',ADMISSION_WORKSPACE_STREAMS:'32'},stdio:['ignore','ignore','pipe']});
  server.on('error',error=>{startError=error;}); server.stderr.on('data',()=>{});
  const deadline=Date.now()+10000;
  for (;;) {
    if(startError)throw startError;
    if(server.exitCode!==null)throw Error('Reference smoke server exited');
    try { const r=await request('/readyz');await r.text();if(r.ok)break; } catch { /* startup */ }
    if(Date.now()>deadline)throw Error('Reference smoke startup timeout');
    await pause(50);
  }
  const boot=await json(await request('/api/v1/bootstrap'));
  assert.equal(boot.reference_limits.max_file_bytes,8<<20);
  const model=boot.models.find(m=>m.kind==='image'&&m.enabled&&m.capabilities.image_edit_supported);
  assert(model,'Default demo image editing must be available');
  const key=randomUUID(), firstResponse=await upload(key), asset=await json(firstResponse);
  assert.equal(firstResponse.status,201); assert.equal(asset.content_type,'image/png');
  assert.equal(asset.width,960);assert.equal(asset.height,640);assert.equal(asset.byte_size,imageBytes.length);
  assert(!JSON.stringify(asset).includes('blob_'),'Internal blob identity exposed');
  assert.equal((await json(await upload(key))).id,asset.id,'Fresh multipart boundary must retain upload identity');
  assert.equal((await upload(key,imageBytes,'renamed.png')).status,409);
  assert.equal((await upload(randomUUID(),Buffer.from('<svg>not an allowed image</svg>'))).status,422);
  assert.equal((await upload(randomUUID(),Buffer.alloc((8<<20)+1))).status,413);
  const duplicateParts=await upload(randomUUID(),imageBytes,'reference.png','prj_demo',true);
  assert([400,422].includes(duplicateParts.status),'Multiple file parts must be rejected');
  const list=await json(await request('/api/v1/references?limit=100'));
  assert.equal(list.items.length,1);
  const original=await request(asset.url), originalData=Buffer.from(await original.arrayBuffer());
  assert.equal(original.status,200);assert.equal(original.headers.get('content-type'),'image/png');
  assert.match(original.headers.get('cache-control')||'',/private/);assert.equal(original.headers.get('x-content-type-options'),'nosniff');
  assert.deepEqual(originalData,imageBytes);
  const preview=await request(asset.thumbnail_url), previewData=Buffer.from(await preview.arrayBuffer());
  assert.equal(preview.status,200);assert.equal(previewData.readUInt32BE(16),512);assert.equal(previewData.readUInt32BE(20),341);
  const head=await request(asset.thumbnail_url,{method:'HEAD'});assert.equal(head.status,200);assert.equal((await head.arrayBuffer()).byteLength,0);
  checks.push('private upload identity, exact bytes, full-format and size rejection, bounded PNG preview and HEAD');

  const secondProject=await json(await post('/api/v1/projects',{name:'参考素材隔离验收'}));
  assert.equal((await request(asset.url,{headers:{'X-Project-ID':secondProject.id}})).status,400,'Conflicting URL/header project must fail');
  assert.equal((await request(`/api/v1/references/${asset.id}/content?project_id=${secondProject.id}`)).status,404);
  const {id:presetId,name:presetName,...preset}=model.capabilities.parameter_presets[0];
  const body={project_id:'prj_demo',kind:'image',operation:'edit',model_id:model.id,model_version:model.version,
    inputs:{prompt:'保留山谷构图，将光线调整为温暖晨光。',reference_asset_ids:[asset.id]},parameters:{...preset,count:1}};
  assert.equal((await post('/api/v1/tasks',{...body,inputs:{...body.inputs,reference_asset_ids:[]}})).status,422);
  assert.equal((await post('/api/v1/tasks',{...body,inputs:{...body.inputs,reference_asset_ids:[asset.id,asset.id]}})).status,422);
  assert.equal((await post('/api/v1/tasks',{...body,operation:'generate'})).status,422);
  assert.equal((await post('/api/v1/tasks',{...body,project_id:secondProject.id},randomUUID(),secondProject.id)).status,404);
  const taskKey=randomUUID(),task=await json(await post('/api/v1/tasks',body,taskKey));
  assert.equal(task.operation,'edit');assert.deepEqual(task.reference_asset_ids,[asset.id]);
  assert.equal((await json(await post('/api/v1/tasks',body,taskKey))).id,task.id);
  assert.equal((await post('/api/v1/tasks',{...body,inputs:{...body.inputs,prompt:'changed'}},taskKey)).status,409);
  checks.push('project isolation, ordered private references, edit validation and original task idempotency');

  let result=task;const completionDeadline=Date.now()+20000;
  while(!['SUCCEEDED','FAILED','PARTIAL','CANCELLED','RECONCILE_REQUIRED'].includes(result.status)){
    if(Date.now()>completionDeadline)throw Error('Demo edit did not finish');
    await pause(150);result=await json(await request(`/api/v1/tasks/${task.id}`));
  }
  assert.equal(result.status,'SUCCEEDED');assert.equal(result.provider_mode,'demo');assert.equal(result.artifacts.length,1);
  assert.equal((await request(result.artifacts[0].url)).status,200);
  const audit=await json(await request('/api/v1/audit?limit=100'));
  assert.equal(audit.items.filter(e=>e.action==='reference.uploaded').length,1);
  assert(!JSON.stringify(audit).includes('reference.png'),'Original filename must not enter audit details');
  const metrics=await (await request('/metrics',{headers:{Authorization:`Bearer ${metricsToken}`}})).text();
  for(const route of ['references','reference_content','reference_thumbnail']) assert(metrics.includes(`route="${route}"`));
  for(const privateValue of [asset.id,'reference.png',task.id,metricsToken]) assert(!metrics.includes(privateValue),'Unbounded or private upload metric label');
  checks.push('demo edit completion, visible demo artifact, single upload audit and redacted metadata');
  writeFileSync(resolve(runtime,'reference-smoke.json'),JSON.stringify({version:JSON.parse(readFileSync(resolve(root,'package.json'),'utf8')).version,
    binary_sha256:createHash('sha256').update(readFileSync(binary)).digest('hex'),verified_at:new Date().toISOString(),duration_ms:Date.now()-started,
    scope:'Isolated loopback memory/demo upload and edit workbench. Actual multipart gateway and private result import are tested by the separate Go HTTP fixture. No real paid supplier call.',checks,status:'passed'},null,2)+'\n');
  console.log(`PASS: ${checks.length} reference/upload/edit integration groups; evidence .runtime/reference-smoke.json.`);
} finally {
  stopOwnedProcess(server);
  referenceDirectory.cleanup();
}
