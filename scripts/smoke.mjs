import assert from 'node:assert/strict';
import { existsSync } from 'node:fs';
import net from 'node:net';
import { binary, start, stopOwnedProcess, isolatedReferenceDirectory, isolatedMailEnvironment, isolatedBillingEnvironment, isolatedOperationsEnvironment, isolatedModerationEnvironment } from './lib/process.mjs';

// Isolated demo process: never calls an existing service or a real gateway.
if (!existsSync(binary)) throw new Error('Run npm run build before npm run smoke.');
const port = await new Promise((resolve, reject) => {
  const server = net.createServer();
  server.on('error', reject);
  server.listen(0, '127.0.0.1', () => {
    const selected = server.address().port;
    server.close(() => resolve(selected));
  });
});
const base = `http://127.0.0.1:${port}`;
const referenceDirectory = isolatedReferenceDirectory();
const server = start(binary, [], {
  env: { ...process.env, ...isolatedMailEnvironment,...isolatedBillingEnvironment,...isolatedOperationsEnvironment,...isolatedModerationEnvironment, APP_ADDR: `127.0.0.1:${port}`, PROVIDER_MODE: 'demo', MODEL_CATALOG_FILE: '', REFERENCE_DIR: referenceDirectory.directory, ASSET_DIR: referenceDirectory.assetDirectory, ASSET_STORAGE_BACKEND: 'local', S3_FORCE_PATH_STYLE: 'false', AUTH_MODE: 'disabled', DATABASE_URL: '', APP_WEB_DIR: 'apps/workbench/dist', METRICS_TOKEN: '', ADMISSION_WORKSPACE_RUNS: '64', ADMISSION_MODEL_RUNS: '32', ADMISSION_WORKSPACE_TASKS: '128', ADMISSION_MODEL_TASKS: '64', ADMISSION_WORKSPACE_STREAMS: '32' },
  stdio: ['ignore', 'pipe', 'pipe'],
});
let startupError;
server.on('error', error => { startupError = error; });
let stderr = '';
server.stderr.on('data', chunk => { stderr = (stderr + chunk).slice(-5000); });
server.stdout.on('data', () => {});
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const req = (path, init = {}) => fetch(`${base}${path}`, { ...init, signal: AbortSignal.timeout(20000) });
const post = (path, body, key = crypto.randomUUID(), extraHeaders = {}) => req(path, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key, Origin: base, ...extraHeaders },
  body: JSON.stringify(body),
});
const jsonOK = async response => {
  const body = await response.text();
  assert(response.ok, `${response.status}: ${body}`);
  return JSON.parse(body);
};
try {
  for (let i = 0; i < 50; i++) {
    if (startupError) throw startupError;
    if (server.exitCode !== null) throw new Error(`Demo server exited: ${stderr}`);
    try { if ((await req('/healthz')).ok) break; } catch { /* startup wait */ }
    if (i === 49) throw new Error('Demo server failed to start.');
    await pause(100);
  }
  const boot = await jsonOK(await req('/api/v1/bootstrap'));
  assert.equal(boot.mode, 'demo');
  assert.equal(boot.storage, 'memory');
  assert.equal((await req('/metrics')).status, 404, 'Metrics are disabled without an explicit operator token');
  const page = await req('/');
  assert.equal(page.status, 200);
  assert.match(await page.text(), /<div id="root"/);
  const unknown = await req('/api/v1/not-a-route');
  assert.equal(unknown.status, 404);
  assert.match(unknown.headers.get('content-type'), /json/);
  const badOrigin = await post('/api/v1/runs', { project_id: 'prj_demo', model_id: 'studio-chat', input: 'test' }, crypto.randomUUID(), { Origin: 'https://untrusted.example' });
  assert.equal(badOrigin.status, 403);

  const runBody = { project_id: 'prj_demo', model_id: 'studio-chat', input: '为我的咖啡品牌写一段创意方向。' };
  const runKey = crypto.randomUUID();
  const created = await post('/api/v1/runs', runBody, runKey);
  assert.equal(created.status, 201);
  const run = await jsonOK(created);
  assert.equal((await jsonOK(await post('/api/v1/runs', runBody, runKey))).id, run.id);
  assert.equal((await post('/api/v1/runs', { ...runBody, input: 'changed' }, runKey)).status, 409);
  const eventsResponse = await req(`/api/v1/runs/${run.id}/events?after=0`);
  assert.match(eventsResponse.headers.get('content-type'), /text\/event-stream/);
  const events = await eventsResponse.text();
  assert.match(events, /event: message.delta/);
  assert.match(events, /event: run.completed/);
  const ids = [...events.matchAll(/^id: (\d+)\r?$/gm)].map(match => Number(match[1]));
  assert(ids.length > 1 && ids.every((id, i) => i === 0 || id > ids[i - 1]), 'SSE sequence must increase');
  const snapshot = await jsonOK(await req(`/api/v1/runs/${run.id}/snapshot`));
  assert.equal(snapshot.status, 'COMPLETED');
  assert(snapshot.output.length > 0);
  const runList = await jsonOK(await req('/api/v1/runs'));
  assert(runList.items.some(item => item.id === run.id));

  const taskBody = { project_id: 'prj_demo', kind: 'image', model_id: 'studio-image', inputs: { prompt: '极简咖啡品牌视觉，绿色与暖白色。' }, parameters: { aspect_ratio: '1:1', count: 1 } };
  const taskKey = crypto.randomUUID();
  const accepted = await post('/api/v1/tasks', taskBody, taskKey);
  assert.equal(accepted.status, 202);
  let task = await jsonOK(accepted);
  assert.equal((await jsonOK(await post('/api/v1/tasks', taskBody, taskKey))).id, task.id);
  assert.equal((await post('/api/v1/tasks', { ...taskBody, inputs: { prompt: 'changed' } }, taskKey)).status, 409);
  const deadline = Date.now() + 20000;
  while (!['SUCCEEDED', 'FAILED', 'CANCELLED'].includes(task.status) && Date.now() < deadline) {
    await pause(200);
    task = await jsonOK(await req(`/api/v1/tasks/${task.id}`));
  }
  assert.equal(task.status, 'SUCCEEDED');
  assert(task.version > 1 && task.artifacts.length > 0);
  const artifact = await req(task.artifacts[0].url);
  assert.equal(artifact.status, 200);
  assert.match(artifact.headers.get('content-type'), /image\/svg\+xml/);
  const video = await jsonOK(await post('/api/v1/tasks', { ...taskBody, kind: 'video', model_id: 'studio-video' }));
  await jsonOK(await post(`/api/v1/tasks/${video.id}/cancel`, {}));
  assert.equal((await jsonOK(await req(`/api/v1/tasks/${video.id}`))).status, 'CANCELLED');
  const draftInMemory = await req('/api/v1/projects/prj_demo/draft');
  assert.equal(draftInMemory.status, 501, 'Cloud drafts must not pretend to persist in memory mode');
  let cursor = '';
  const auditEvents = [];
  do {
    const page = await jsonOK(await req(`/api/v1/audit?scope=project&limit=2${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`));
    assert.equal(page.storage, 'memory');
    auditEvents.push(...page.items);
    cursor = page.next_cursor || '';
    assert(auditEvents.length < 50, 'Audit pagination must terminate');
  } while (cursor);
  assert.equal(new Set(auditEvents.map(event => event.id)).size, auditEvents.length, 'No duplicate audit rows across pages');
  const acceptedRuns = auditEvents.filter(event => event.resource_id === run.id && event.action === 'run.accepted');
  const acceptedTasks = auditEvents.filter(event => event.resource_id === task.id && event.action === 'task.accepted');
  assert.equal(acceptedRuns.length, 1, 'Idempotent run replay must not duplicate audit');
  assert.equal(acceptedTasks.length, 1, 'Idempotent task replay must not duplicate audit');
  const auditJSON = JSON.stringify(auditEvents);
  assert(!auditJSON.includes(runBody.input) && !auditJSON.includes(taskBody.inputs.prompt), 'Audit must omit creative content');
  console.log('PASS: built UI, demo isolation, API 404, origin protection, LLM SSE/history, run/task idempotency conflicts, media completion, asset delivery, cancellation, audit pagination/deduplication/redaction and memory-mode draft boundary.');
} catch (error) {
  console.error(error.stack || error.message);
  if (stderr) console.error(stderr);
  process.exitCode = 1;
} finally {
  stopOwnedProcess(server);
  referenceDirectory.cleanup();
}
