import assert from 'node:assert/strict';
import { createHash, randomBytes, randomUUID } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import net from 'node:net';
import { resolve } from 'node:path';
import { binary, root, start, stopOwnedProcess, isolatedReferenceDirectory, isolatedMailEnvironment, isolatedBillingEnvironment, isolatedOperationsEnvironment, isolatedModerationEnvironment, isolatedExecutionEnvironment } from './lib/process.mjs';

// A separate loopback demo process, with its own metrics token and tiny limits.
// Never connects to the development database, a real model or a remote target.
if (!existsSync(binary)) throw new Error('Run npm run build before npm run smoke:runtime.');
const port = await new Promise((resolvePort, reject) => {
  const probe = net.createServer(); probe.on('error', reject);
  probe.listen(0, '127.0.0.1', () => { const selected = probe.address().port; probe.close(() => resolvePort(selected)); });
});
const base = `http://127.0.0.1:${port}`;
const referenceDirectory = isolatedReferenceDirectory();
const token = randomBytes(32).toString('hex');
const server = start(binary, [], { env: { ...process.env, ...isolatedMailEnvironment,...isolatedBillingEnvironment,...isolatedOperationsEnvironment,...isolatedModerationEnvironment,...isolatedExecutionEnvironment, APP_ADDR: `127.0.0.1:${port}`, PROVIDER_MODE: 'demo', MODEL_CATALOG_FILE: '', REFERENCE_DIR: referenceDirectory.directory, ASSET_DIR: referenceDirectory.assetDirectory, ASSET_STORAGE_BACKEND: 'local', S3_FORCE_PATH_STYLE: 'false', AUTH_MODE: 'disabled', DATABASE_URL: '', METRICS_TOKEN: token,
  ADMISSION_WORKSPACE_RUNS: '1', ADMISSION_MODEL_RUNS: '1', ADMISSION_WORKSPACE_TASKS: '2', ADMISSION_MODEL_TASKS: '1', ADMISSION_WORKSPACE_STREAMS: '2' }, stdio: ['ignore', 'pipe', 'pipe'] });
let startError, log = '';
server.on('error', error => { startError = error; });
server.stdout.on('data', () => {});
server.stderr.on('data', chunk => { log = (log + chunk).slice(-8000); });
const streams = [];
const checks = [];
const pause = ms => new Promise(resolvePause => setTimeout(resolvePause, ms));
const request = (path, options = {}) => fetch(base + path, { ...options, signal: options.signal || AbortSignal.timeout(15000) });
const post = (path, body, key = randomUUID()) => request(path, { method: 'POST', headers: { Origin: base, 'Content-Type': 'application/json', 'Idempotency-Key': key }, body: JSON.stringify(body) });
async function json(response) { assert(response.ok, `Unexpected HTTP ${response.status}`); return response.json(); }
async function rejected(response, code) {
  assert.equal(response.status, 429);
  assert.equal(response.headers.get('retry-after'), '3');
  const body = await response.json();
  assert.equal(body.error.code, code);
  assert(body.error.request_id && body.error.request_id === response.headers.get('x-request-id'), 'Error and response correlation IDs agree');
}
async function until(predicate, message) {
  const end = Date.now() + 5000;
  while (Date.now() < end) { if (await predicate()) return; await pause(50); }
  throw new Error(message);
}
async function openStream(path) {
  const controller = new AbortController();
  const response = await request(path, { signal: controller.signal });
  const entry = { controller, response }; streams.push(entry);
  assert.equal(response.status, 200); assert.match(response.headers.get('content-type'), /text\/event-stream/);
  return entry;
}
const started = Date.now();
try {
  await until(async () => {
    if (startError) throw startError;
    if (server.exitCode !== null) throw new Error('Isolated server exited before readiness');
    try { return (await request('/readyz')).ok; } catch { return false; }
  }, 'Isolated server did not become ready');
  assert.equal((await json(await request('/healthz'))).status, 'alive');
  const ready = await json(await request('/readyz')); assert.equal(ready.ready, true);
  assert.equal((await request('/metrics')).status, 401);
  assert.equal((await request('/metrics', { headers: { Authorization: 'Bearer invalid-local-test-token' } })).status, 401);
  checks.push('separate liveness/readiness and protected metrics');

  const privatePath = '/api/v1/private-path-' + randomUUID();
  const unknown = await request(privatePath, { headers: { 'X-Request-ID': 'untrusted-client-request-id' } });
  assert.equal(unknown.status, 404);
  const correlation = unknown.headers.get('x-request-id');
  assert(correlation && correlation !== 'untrusted-client-request-id' && correlation.length <= 128);

  // Long demo input keeps the run nonterminal without adding test-only server flags.
  const secretPrompt = 'smoke-private-content-' + randomUUID();
  const runBody = { project_id: 'prj_demo', model_id: 'studio-chat', input: secretPrompt + '保持运行以验证准入。'.repeat(200) };
  const runKey = randomUUID();
  const run = await json(await post('/api/v1/runs', runBody, runKey));
  await rejected(await post('/api/v1/runs', runBody), 'ADMISSION_WORKSPACE_LIMIT');
  assert.equal((await json(await post('/api/v1/runs', runBody, runKey))).id, run.id);
  assert.equal((await post('/api/v1/runs', { ...runBody, input: 'different' }, runKey)).status, 409);
  const runtime = await json(await request('/api/v1/runtime'));
  assert.equal(runtime.scope, 'workspace'); assert.equal(runtime.runs.active, 1); assert.equal(runtime.runs.limit, 1);
  const runStream = await openStream(`/api/v1/runs/${run.id}/events`);
  const reader = runStream.response.body.getReader();
  const decoder = new TextDecoder(); let frames = '';
  while (!frames.includes('event: message.delta')) { const part = await reader.read(); assert(!part.done); frames += decoder.decode(part.value); assert(frames.length < 128000); }
  runStream.controller.abort(); await reader.cancel().catch(() => {});
  await json(await post(`/api/v1/runs/${run.id}/cancel`, {}));
  const nextRun = await json(await post('/api/v1/runs', runBody));
  await json(await post(`/api/v1/runs/${nextRun.id}/cancel`, {}));
  checks.push('workspace run limit, replay under full capacity, conflict, cancellation release, SSE flush');

  const imageBody = { project_id: 'prj_demo', kind: 'image', model_id: 'studio-image', inputs: { prompt: secretPrompt }, parameters: { aspect_ratio: '1:1', count: 1 } };
  const imageKey = randomUUID();
  const image = await json(await post('/api/v1/tasks', imageBody, imageKey));
  await rejected(await post('/api/v1/tasks', imageBody), 'ADMISSION_MODEL_LIMIT');
  assert.equal((await json(await post('/api/v1/tasks', imageBody, imageKey))).id, image.id);
  const video = await json(await post('/api/v1/tasks', { ...imageBody, kind: 'video', model_id: 'studio-video' }));
  await rejected(await post('/api/v1/tasks', imageBody), 'ADMISSION_WORKSPACE_LIMIT');
  const occupied = await json(await request('/api/v1/runtime'));
  assert.equal(occupied.tasks.active, 2); assert.equal(occupied.tasks.limit, 2);
  assert.equal(occupied.tasks.models.find(item => item.model_id === 'studio-image').active, 1);
  await json(await post(`/api/v1/tasks/${image.id}/cancel`, {}));
  const nextImage = await json(await post('/api/v1/tasks', imageBody));
  await json(await post(`/api/v1/tasks/${nextImage.id}/cancel`, {}));
  await json(await post(`/api/v1/tasks/${video.id}/cancel`, {}));
  checks.push('model and workspace media limits, exact replay and terminal release');

  await until(async () => (await json(await request('/api/v1/runtime'))).streams.active === 0, 'Run stream slot did not release');
  const first = await openStream('/api/v1/workspaces/ws_demo/events');
  await openStream('/api/v1/workspaces/ws_demo/events');
  await rejected(await request('/api/v1/workspaces/ws_demo/events'), 'ADMISSION_STREAM_LIMIT');
  assert.equal((await json(await request('/api/v1/runtime'))).streams.active, 2);
  first.controller.abort(); await first.response.body.cancel().catch(() => {});
  await until(async () => (await json(await request('/api/v1/runtime'))).streams.active === 1, 'Workspace stream slot did not release');
  await openStream('/api/v1/workspaces/ws_demo/events');
  for (const entry of streams) { entry.controller.abort(); if (!entry.response.body.locked) await entry.response.body.cancel().catch(() => {}); }
  await until(async () => (await json(await request('/api/v1/runtime'))).streams.active === 0, 'All stream slots must release');
  checks.push('bounded workspace subscriptions and disconnect recovery');

  const metrics = await request('/metrics', { headers: { Authorization: `Bearer ${token}` } });
  assert.equal(metrics.status, 200); assert.match(metrics.headers.get('content-type'), /text\/plain/);
  const text = await metrics.text(); assert(text.includes('# HELP ') && text.includes('# TYPE '));
  for (const value of [token, secretPrompt, privatePath, correlation, run.id, image.id, 'ws_demo', 'prj_demo']) assert(!text.includes(value), 'Metrics must not contain secrets or unbounded identifiers');
  assert(!log.includes(token) && !log.includes(secretPrompt) && !log.includes(privatePath), 'Server logs must not include secret content or raw request paths');
  assert(text.endsWith('\n'));
  checks.push('bounded metric labels and redacted logs, server-owned request correlation');
  mkdirSync(resolve(root, '.runtime'), { recursive: true });
  writeFileSync(resolve(root, '.runtime/runtime-smoke.json'), JSON.stringify({ version: JSON.parse(readFileSync(resolve(root, 'package.json'), 'utf8')).version, binary_sha256: createHash('sha256').update(readFileSync(binary)).digest('hex'), verified_at: new Date().toISOString(), duration_ms: Date.now() - started, scope: 'Isolated loopback memory/demo executable with tiny admission limits; no database, real AI or production capacity test.', checks, status: 'passed' }, null, 2) + '\n');
  console.log(`PASS: ${checks.length} runtime integration groups; evidence .runtime/runtime-smoke.json (no credentials).`);
} catch (error) {
  console.error(error.message); process.exitCode = 1;
} finally {
  for (const entry of streams) entry.controller.abort();
  stopOwnedProcess(server);
  referenceDirectory.cleanup();
}
