import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
import net from 'node:net';
import { resolve } from 'node:path';
import { binary, root, start, stopOwnedProcess, isolatedReferenceDirectory, isolatedMailEnvironment, isolatedBillingEnvironment, isolatedOperationsEnvironment, isolatedModerationEnvironment } from './lib/process.mjs';

// Isolated demo process and a temporary private catalog. No existing database,
// development configuration or paid provider is used by this test.
const port = await new Promise((done, fail) => {
  const probe = net.createServer(); probe.on('error', fail);
  probe.listen(0, '127.0.0.1', () => { const port = probe.address().port; probe.close(() => done(port)); });
});
const base = `http://127.0.0.1:${port}`;
const referenceDirectory = isolatedReferenceDirectory();
mkdirSync(resolve(root, '.runtime'), { recursive: true });
const catalogPath = resolve(root, '.runtime', `catalog-smoke-${randomUUID()}.json`);
const capabilities = (maxCount, presets = []) => ({ max_count: maxCount, max_prompt_bytes: 16000, parameter_presets: presets });
const model = (id, kind, caps, enabled = true) => ({ id, name: `验收 ${id}`, kind, description: '本地目录验收，无真实模型调用', enabled, upstream_model: `private-route-${id}`, capabilities: caps });
const square = { id: 'square', name: '方图', aspect_ratio: '1:1', size: '1024x1024' };
const wide = { id: 'wide', name: '横图', aspect_ratio: '3:2', size: '1536x1024' };
const cinema = { id: 'cinema', name: '横屏短片', aspect_ratio: '16:9', resolution: '720p', seconds: 5 };
const portrait = { id: 'portrait', name: '竖屏短片', aspect_ratio: '9:16', resolution: '1080p', seconds: 8 };
const config = { schema_version: 1, models: [model('notes-chat', 'chat', capabilities(1)), model('poster-image', 'image', capabilities(2, [square, wide])), model('cinema-video', 'video', capabilities(1, [cinema, portrait])), model('retired-image', 'image', capabilities(1, [square]), false)] };
const params = ({ id, name, ...values }, count = 1) => ({ ...values, count });
let server, startError;
const checks = [], started = Date.now();
const delay = ms => new Promise(done => setTimeout(done, ms));
const request = (path, options = {}) => fetch(base + path, { ...options, signal: AbortSignal.timeout(15000) });
const json = async response => { assert(response.ok, `Unexpected HTTP ${response.status}: ${await response.clone().text()}`); return response.json(); };
const post = (path, body, key = randomUUID()) => request(path, { method: 'POST', headers: { Origin: base, 'Content-Type': 'application/json', 'Idempotency-Key': key }, body: JSON.stringify(body) });
async function launch() {
  startError = undefined;
  server = start(binary, [], { env: { ...process.env, ...isolatedMailEnvironment,...isolatedBillingEnvironment,...isolatedOperationsEnvironment,...isolatedModerationEnvironment, APP_ADDR: `127.0.0.1:${port}`, APP_WEB_DIR: resolve(root, 'apps/workbench/dist'), MODEL_CATALOG_FILE: catalogPath, REFERENCE_DIR: referenceDirectory.directory, ASSET_DIR: referenceDirectory.assetDirectory, ASSET_STORAGE_BACKEND: 'local', S3_FORCE_PATH_STYLE: 'false',
    DATABASE_URL: '', AUTH_MODE: 'disabled', PROVIDER_MODE: 'demo', METRICS_TOKEN: '', NEWAPI_BASE_URL: '', NEWAPI_API_KEY: '', NEWAPI_CHAT_MODEL: '', ASYNC_IMAGE_BASE_URL: '', ASYNC_IMAGE_API_KEY: '', 
    ADMISSION_WORKSPACE_RUNS: '64', ADMISSION_MODEL_RUNS: '32', ADMISSION_WORKSPACE_TASKS: '128', ADMISSION_MODEL_TASKS: '64', ADMISSION_WORKSPACE_STREAMS: '32' }, stdio: ['ignore', 'ignore', 'pipe'] });
  server.on('error', error => { startError = error; });
  server.stderr.on('data', () => {});
  const deadline = Date.now() + 10000;
  while (Date.now() < deadline) {
    if (startError) throw startError;
    if (server.exitCode !== null) throw new Error('Catalog server exited during startup');
    try { const ready = await request('/readyz'); await ready.text(); if (ready.ok) return; } catch { /* startup only */ }
    await delay(50);
  }
  throw new Error('Catalog server startup timeout');
}
try {
  writeFileSync(catalogPath, JSON.stringify(config));
  await launch();
  const response = await request('/api/v1/models');
  const etag = response.headers.get('etag'), catalog = await json(response);
  assert(etag && catalog.version && catalog.models.length === 4);
  assert.match(response.headers.get('cache-control') || '', /private/);
  assert(!JSON.stringify(catalog).includes('private-route-'), 'Private upstream model mapping leaked');
  const boot = await json(await request('/api/v1/bootstrap'));
  assert.equal(boot.catalog_version, catalog.version); assert.deepEqual(boot.models, catalog.models);
  assert.equal((await request('/api/v1/models', { headers: { 'If-None-Match': etag } })).status, 304);
  checks.push('versioned public catalog, private mappings, bootstrap consistency and ETag revalidation');

  const imageModel = catalog.models.find(m => m.id === 'poster-image');
  const videoModel = catalog.models.find(m => m.id === 'cinema-video');
  const imageBody = { project_id: 'prj_demo', kind: 'image', model_id: imageModel.id, model_version: imageModel.version, inputs: { prompt: '本地目录图片验收' }, parameters: params(wide, 2) };
  assert.equal((await post('/api/v1/tasks', { ...imageBody, parameters: { ...params(wide, 2), aspect_ratio: '1:1' } })).status, 422);
  assert.equal((await post('/api/v1/tasks', { ...imageBody, parameters: params(wide, 3) })).status, 422);
  assert.equal((await post('/api/v1/tasks', { ...imageBody, model_id: 'retired-image', model_version: catalog.models.find(m => m.id === 'retired-image').version })).status, 422);
  const stale = await post('/api/v1/tasks', { ...imageBody, model_version: 'stale-version' });
  assert.equal(stale.status, 409); assert.equal((await stale.json()).error.code, 'MODEL_VERSION_CHANGED');
  const key = randomUUID(), image = await json(await post('/api/v1/tasks', imageBody, key));
  assert.equal(image.model_name, imageModel.name); assert.equal(image.model_version, imageModel.version); assert.deepEqual(image.parameters, params(wide, 2));
  assert.equal((await json(await post('/api/v1/tasks', imageBody, key))).id, image.id);
  assert.equal((await post('/api/v1/tasks', { ...imageBody, parameters: params(square) }, key)).status, 409);
  const videoBody = { project_id: 'prj_demo', kind: 'video', model_id: videoModel.id, model_version: videoModel.version, inputs: { prompt: '本地目录视频验收' }, parameters: params(portrait) };
  assert.equal((await post('/api/v1/tasks', { ...videoBody, parameters: { ...params(portrait), seconds: 5 } })).status, 422);
  const video = await json(await post('/api/v1/tasks', videoBody)); assert.deepEqual(video.parameters, params(portrait));
  const chatModel = catalog.models.find(m => m.kind === 'chat');
  const run = await json(await post('/api/v1/runs', { project_id: 'prj_demo', model_id: chatModel.id, model_version: chatModel.version, input: '本地多模型对话验收' }));
  assert.equal(run.model_name, chatModel.name); assert.equal(run.model_version, chatModel.version);
  const runtime = await json(await request('/api/v1/runtime'));
  assert(runtime.tasks.models.some(m => m.model_id === imageModel.id));
  assert(!runtime.tasks.models.some(m => m.model_id === 'studio-image'), 'Catalog-free model injected into capacity');
  await json(await post(`/api/v1/runs/${run.id}/cancel`, {}));
  await json(await post(`/api/v1/tasks/${image.id}/cancel`, {}));
  await json(await post(`/api/v1/tasks/${video.id}/cancel`, {}));
  checks.push('custom model execution identities, exact parameter combinations, version rejection, idempotency and dynamic capacity');

  config.models[1].enabled = false;
  config.models[2].capabilities.parameter_presets = [cinema];
  writeFileSync(catalogPath, JSON.stringify(config));
  assert.equal((await request('/api/v1/models', { headers: { 'If-None-Match': etag } })).status, 304, 'File edits must not silently mutate a running catalog');
  stopOwnedProcess(server);
  await launch();
  const updatedResponse = await request('/api/v1/models', { headers: { 'If-None-Match': etag } });
  assert.equal(updatedResponse.status, 200);
  const updated = await json(updatedResponse); assert.notEqual(updated.version, catalog.version);
  assert.notEqual(updatedResponse.headers.get('etag'), etag);
  assert.equal(updated.models.find(m => m.id === imageModel.id).enabled, false);
  assert.equal((await post('/api/v1/tasks', { ...imageBody, model_version: updated.models.find(m => m.id === imageModel.id).version })).status, 422);
  checks.push('immutable running configuration, restart activation, changed ETag and retired model rejection');

  writeFileSync(resolve(root, '.runtime/catalog-smoke.json'), JSON.stringify({ version: JSON.parse(readFileSync(resolve(root, 'package.json'), 'utf8')).version,
    binary_sha256: createHash('sha256').update(readFileSync(binary)).digest('hex'), verified_at: new Date().toISOString(), duration_ms: Date.now() - started,
    scope: 'Isolated memory/demo catalog process; PostgreSQL historical replay is covered by the separate database tests. No paid provider or production capacity claim.', checks, status: 'passed' }, null, 2) + '\n');
  console.log(`PASS: ${checks.length} model catalog integration groups; evidence .runtime/catalog-smoke.json.`);
} finally { stopOwnedProcess(server); rmSync(catalogPath, { force: true }); referenceDirectory.cleanup(); }
