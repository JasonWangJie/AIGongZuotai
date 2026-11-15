import assert from 'node:assert/strict';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import net from 'node:net';
import { resolve } from 'node:path';
import { binary, root, start, stopOwnedProcess, isolatedReferenceDirectory, isolatedMailEnvironment, isolatedBillingEnvironment, isolatedOperationsEnvironment, isolatedModerationEnvironment, isolatedExecutionEnvironment } from './lib/process.mjs';

const port = await new Promise((done, fail) => {
  const probe = net.createServer(); probe.on('error', fail);
  probe.listen(0, '127.0.0.1', () => { const port = probe.address().port; probe.close(() => done(port)); });
});
const base = `http://127.0.0.1:${port}`, directories = isolatedReferenceDirectory();
const token = randomBytes(32).toString('hex'), checks = [], started = Date.now();
const pause = ms => new Promise(done => setTimeout(done, ms));
let server, startupError;
const request = (path, options = {}) => fetch(base + path, { ...options, signal: AbortSignal.timeout(10000) });
const json = async response => { assert(response.ok, `Unexpected HTTP ${response.status}: ${await response.clone().text()}`); return response.json(); };
const mutate = (path, body, key = randomUUID(), method = 'POST', project = 'prj_demo') => request(path, { method, headers: { Origin: base, 'Content-Type': 'application/json', 'Idempotency-Key': key, 'X-Project-ID': project }, body: JSON.stringify(body) });
const upload = async name => {
  const form = new FormData(); form.append('file', new Blob([readFileSync(resolve(root, 'internal/studio/testdata/references/source.png'))], { type: 'image/png' }), name);
  return json(await request('/api/v1/references', { method: 'POST', headers: { Origin: base, 'Idempotency-Key': randomUUID(), 'X-Project-ID': 'prj_demo' }, body: form }));
};
try {
  server = start(binary, [], { env: { ...process.env, ...isolatedMailEnvironment,...isolatedBillingEnvironment,...isolatedOperationsEnvironment,...isolatedModerationEnvironment,...isolatedExecutionEnvironment, APP_ADDR: `127.0.0.1:${port}`, APP_WEB_DIR: resolve(root, 'apps/workbench/dist'),
    DATABASE_URL: '', AUTH_MODE: 'disabled', PROVIDER_MODE: 'demo', MODEL_CATALOG_FILE: '',
    REFERENCE_DIR: directories.directory, ASSET_DIR: directories.assetDirectory, ASSET_STORAGE_BACKEND: 'local', S3_FORCE_PATH_STYLE: 'false',
    NEWAPI_BASE_URL: '', NEWAPI_API_KEY: '', ASYNC_IMAGE_BASE_URL: '', ASYNC_IMAGE_API_KEY: '', METRICS_TOKEN: token,
    ADMISSION_WORKSPACE_RUNS: '64', ADMISSION_MODEL_RUNS: '32', ADMISSION_WORKSPACE_TASKS: '128', ADMISSION_MODEL_TASKS: '64', ADMISSION_WORKSPACE_STREAMS: '32' }, stdio: ['ignore', 'ignore', 'pipe'] });
  server.on('error', error => { startupError = error; }); server.stderr.on('data', () => {});
  const deadline = Date.now() + 10000;
  for (;;) {
    if (startupError) throw startupError;
    if (server.exitCode !== null) throw Error('Library smoke server exited');
    try { const response = await request('/readyz'); await response.text(); if (response.ok) break; } catch { /* bounded startup */ }
    if (Date.now() > deadline) throw Error('Library smoke startup timeout');
    await pause(50);
  }
  const boot = await json(await request('/api/v1/bootstrap'));
  const reference = await upload('原始河流.png'); await upload('另一张参考.png');
  let page = await json(await request('/api/v1/library?limit=1&source=reference'));
  assert.equal(page.items.length, 1); assert(page.has_more && page.next_cursor);
  const pageTwo = await json(await request('/api/v1/library?limit=1&source=reference&cursor=' + encodeURIComponent(page.next_cursor)));
  assert.equal(pageTwo.items.length, 1); assert.notEqual(page.items[0].id, pageTwo.items[0].id); assert.equal(pageTwo.next_cursor, null);
  const changedFilter = await request('/api/v1/library?state=trashed&source=reference&limit=1&cursor=' + encodeURIComponent(page.next_cursor));
  assert.equal(changedFilter.status, 400);
  const entries = [...page.items, ...pageTwo.items], asset = entries.find(item => item.reference_id === reference.id);
  assert(asset && asset.source === 'reference' && asset.revision === 1);
  const originalURL = asset.content_url;
  const content = await request(originalURL, { headers: { Range: 'bytes=0-7' } });
  assert.equal(content.status, 206); assert.equal(Buffer.from(await content.arrayBuffer()).toString('hex'), '89504e470d0a1a0a');
  const otherProject = await json(await mutate('/api/v1/projects', { name: 'Other project' }));
  assert.equal((await request(`/api/v1/library/${asset.id}`, { headers: { 'X-Project-ID': otherProject.id } })).status, 404);
  assert.equal((await request(originalURL, { headers: { 'X-Project-ID': otherProject.id } })).status, 400);
  checks.push('independent keyset library, filter-bound cursors, private Range content and project isolation');

  const editKey = randomUUID(), edit = { expected_revision: 1, name: '银色河流', tags: ['Night', 'night', 'landscape'] };
  let updated = await json(await mutate(`/api/v1/library/${asset.id}`, edit, editKey, 'PATCH'));
  assert.equal(updated.revision, 2); assert.deepEqual(updated.tags, ['night', 'landscape']);
  let replay = await mutate(`/api/v1/library/${asset.id}`, edit, editKey, 'PATCH');
  assert.equal(replay.headers.get('Idempotency-Replayed'), 'true'); assert.equal((await json(replay)).revision, 2);
  assert.equal((await mutate(`/api/v1/library/${asset.id}`, { ...edit, name: 'changed' }, editKey, 'PATCH')).status, 409);
  assert.equal((await mutate(`/api/v1/library/${asset.id}`, edit, randomUUID(), 'PATCH')).status, 409);
  const invalid = await mutate(`/api/v1/library/${asset.id}`, { expected_revision: 2, name: 'x', tags: [''] }, randomUUID(), 'PATCH'); assert.equal(invalid.status, 422);
  page = await json(await request('/api/v1/library?q=' + encodeURIComponent('银色') + '&tag=night'));
  assert.equal(page.items.length, 1); assert.equal(page.items[0].id, asset.id);
  assert.equal((await json(await request('/api/v1/library?q=' + encodeURIComponent('%_')))).items.length, 0, 'search must treat SQL wildcard bytes literally');
  updated = await json(await mutate(`/api/v1/library/${asset.id}`, { expected_revision: 2, name: '银色河流 · 定稿', tags: ['night'] }, randomUUID(), 'PATCH'));
  assert.equal(updated.revision, 3);
  const model = boot.models.find(item => item.kind === 'image' && item.enabled && item.capabilities.image_edit_supported);
  assert(model);
  const preset = model.capabilities.parameter_presets[0];
  const taskBody = { operation: 'edit', project_id: 'prj_demo', kind: 'image', model_id: model.id, model_version: model.version, inputs: { prompt: 'Local fixture edit for asset registration', reference_asset_ids: [reference.id] }, parameters: { aspect_ratio: preset.aspect_ratio, count: 1, ...(preset.size ? { size: preset.size } : {}), ...(preset.resolution ? { resolution: preset.resolution } : {}) } };
  const taskKey = randomUUID(), task = await json(await mutate('/api/v1/tasks', taskBody, taskKey));
  updated = await json(await mutate(`/api/v1/library/${asset.id}/trash`, { expected_revision: 3 }));
  assert.equal(updated.state, 'trashed'); assert.equal(updated.revision, 4); assert.equal(updated.content_url, null); assert.equal(updated.thumbnail_url, null);
  assert.equal((await request(originalURL)).status, 404); assert.equal((await request(reference.url)).status, 404); assert.equal((await request(reference.thumbnail_url)).status, 404);
  assert(!(await json(await request('/api/v1/references'))).items.some(item => item.id === reference.id));
  assert.equal((await mutate(`/api/v1/library/${asset.id}`, { expected_revision: 4, name: 'Cannot mutate trash', tags: [] }, randomUUID(), 'PATCH')).status, 409);
  assert.equal((await mutate('/api/v1/tasks', taskBody)).status, 404, 'new editing must reject a trashed reference without disclosing its state');
  assert.equal((await json(await mutate('/api/v1/tasks', taskBody, taskKey))).id, task.id, 'accepted task replay must survive later trash');
  assert.equal((await json(await mutate(`/api/v1/library/${asset.id}`, edit, editKey, 'PATCH'))).revision, 2, 'replay returns historical accepted response');
  assert.equal((await json(await request(`/api/v1/library/${asset.id}`))).state, 'trashed', 'old replay must not restore current state');
  const revisions = await json(await request(`/api/v1/library/${asset.id}/revisions?limit=2`));
  assert.deepEqual(revisions.items.map(item => item.revision), [4, 3]); assert(revisions.has_more);
  const older = await json(await request(`/api/v1/library/${asset.id}/revisions?limit=2&before_revision=${revisions.next_before_revision}`));
  assert.deepEqual(older.items.map(item => item.revision), [2, 1]); assert.equal(older.items[1].name, '原始河流.png');
  assert(!JSON.stringify(revisions).includes('content_url'));
  updated = await json(await mutate(`/api/v1/library/${asset.id}/restore`, { expected_revision: 4 }));
  assert.equal(updated.revision, 5); assert.equal(updated.state, 'active'); assert.equal((await request(originalURL)).status, 200);
  checks.push('CAS and immutable metadata revisions, original replay, trash blocks old URLs/new edits, restore and accepted-task continuity');

  const taskDeadline = Date.now() + 15000;
  for (;;) {
    const current = await json(await request(`/api/v1/tasks/${task.id}`));
    if (current.status === 'SUCCEEDED') break;
    assert(!['FAILED', 'RECONCILE_REQUIRED'].includes(current.status));
    if (Date.now() > taskDeadline) throw Error('Demo task did not complete');
    await pause(100);
  }
  const generated = await json(await request('/api/v1/library?source=generated'));
  assert.equal(generated.items.length, 1); assert.equal(generated.items[0].task_id, task.id); assert.equal(generated.items[0].provider_mode, 'demo');
  const demoContent = await request(generated.items[0].content_url); assert.equal(demoContent.status, 200); assert((await demoContent.text()).includes('<svg'));
  const audit = await json(await request('/api/v1/audit?limit=100'));
  assert.equal(audit.items.filter(item => item.action === 'asset.updated').length, 2);
  assert.equal(audit.items.filter(item => item.action === 'asset.trashed').length, 1);
  assert.equal(audit.items.filter(item => item.action === 'asset.restored').length, 1);
  assert(!JSON.stringify(audit).includes('银色河流')); assert(!JSON.stringify(audit).includes('landscape'));
  const metrics = await request('/metrics', { headers: { Authorization: `Bearer ${token}` } }); const metricText = await metrics.text();
  assert(metricText.includes('route="library"')); assert(!metricText.includes(asset.id)); assert(!metricText.includes(reference.id));
  const publicLibrary = JSON.stringify(await json(await request('/api/v1/library')));
  for (const forbidden of ['storage_info', 'object_key', 'sha256', 'bucket', 'blob_', token]) assert(!publicLibrary.includes(forbidden));
  checks.push('automatic generated-asset registration, visible demo provenance, atomic audit deduplication and fixed private-safe metrics');
  mkdirSync(resolve(root, '.runtime'), { recursive: true });
  writeFileSync(resolve(root, '.runtime/library-smoke.json'), JSON.stringify({ version: JSON.parse(readFileSync(resolve(root, 'package.json'), 'utf8')).version, binary_sha256: createHash('sha256').update(readFileSync(binary)).digest('hex'), verified_at: new Date().toISOString(), duration_ms: Date.now() - started, scope: 'Isolated loopback memory/demo service. PostgreSQL and real local S3-compatible service are verified separately. No paid model calls.', checks, status: 'passed' }, null, 2) + '\n');
  console.log(`PASS: ${checks.length} independent asset library lifecycle groups; evidence .runtime/library-smoke.json.`);
} finally { stopOwnedProcess(server); directories.cleanup(); }
