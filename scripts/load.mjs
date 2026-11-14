import assert from 'node:assert/strict';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import net from 'node:net';
import os from 'node:os';
import { resolve } from 'node:path';
import { performance } from 'node:perf_hooks';
import { binary, root, start, stopOwnedProcess, isolatedReferenceDirectory, isolatedMailEnvironment, isolatedBillingEnvironment, isolatedOperationsEnvironment, isolatedModerationEnvironment } from './lib/process.mjs';

// A reproducible single-machine regression baseline, never a production capacity claim.
// There is intentionally no remote target option: this owns an isolated demo process.
if (!existsSync(binary)) throw new Error('Run npm run build first.');
const port = await new Promise((resolvePromise, reject) => {
  const probe = net.createServer();
  probe.on('error', reject);
  probe.listen(0, '127.0.0.1', () => {
    const value = probe.address().port;
    probe.close(() => resolvePromise(value));
  });
});
const base = `http://127.0.0.1:${port}`;
const referenceDirectory = isolatedReferenceDirectory();
const child = start(binary, [], {
  env: { ...process.env, ...isolatedMailEnvironment,...isolatedBillingEnvironment,...isolatedOperationsEnvironment,...isolatedModerationEnvironment, APP_ADDR: `127.0.0.1:${port}`, PROVIDER_MODE: 'demo', MODEL_CATALOG_FILE: '', REFERENCE_DIR: referenceDirectory.directory, ASSET_DIR: referenceDirectory.assetDirectory, ASSET_STORAGE_BACKEND: 'local', S3_FORCE_PATH_STYLE: 'false', AUTH_MODE: 'disabled', DATABASE_URL: '', METRICS_TOKEN: '', ADMISSION_WORKSPACE_RUNS: '64', ADMISSION_MODEL_RUNS: '32', ADMISSION_WORKSPACE_TASKS: '128', ADMISSION_MODEL_TASKS: '64', ADMISSION_WORKSPACE_STREAMS: '32' },
  stdio: ['ignore', 'pipe', 'pipe'],
});
let startupError;
let log = '';
child.on('error', error => { startupError = error; });
child.stdout.on('data', () => {});
child.stderr.on('data', chunk => { log = (log + chunk).slice(-2000); });
const sleep = ms => new Promise(resolvePromise => setTimeout(resolvePromise, ms));
const request = (path, options = {}) => fetch(`${base}${path}`, { ...options, signal: AbortSignal.timeout(30000) });
const post = (path, body) => request(path, { method: 'POST', headers: { Origin: base, 'Content-Type': 'application/json', 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify(body) });
const round = n => Math.round(n * 100) / 100;
function distribution(values) {
  const sorted = [...values].sort((a, b) => a - b);
  const percentile = p => round(sorted[Math.max(0, Math.ceil(sorted.length * p) - 1)] ?? 0);
  return { samples: sorted.length, min_ms: percentile(0), p50_ms: percentile(.5), p95_ms: percentile(.95), p99_ms: percentile(.99), max_ms: percentile(1) };
}
async function measure(name, count, concurrency, operation) {
  let next = 0;
  const latency = [];
  const started = performance.now();
  await Promise.all(Array.from({ length: concurrency }, async () => {
    for (;;) {
      const index = next++;
      if (index >= count) return;
      const before = performance.now();
      await operation(index);
      latency.push(performance.now() - before);
    }
  }));
  const duration = performance.now() - started;
  const result = { name, concurrency, elapsed_ms: round(duration), operations_per_second: round(count / duration * 1000), ...distribution(latency) };
  console.log(JSON.stringify(result));
  return result;
}
try {
  for (let i = 0; i < 80; i++) {
    if (startupError) throw startupError;
    if (child.exitCode !== null) throw new Error(`Isolated server stopped: ${log}`);
    try { if ((await request('/healthz')).ok) break; } catch { /* bounded startup wait */ }
    if (i === 79) throw new Error('Isolated server did not start.');
    await sleep(100);
  }
  const bootstrap = await (await request('/api/v1/bootstrap')).json();
  assert.equal(bootstrap.mode, 'demo');
  assert.equal(bootstrap.storage, 'memory');
  for (let i = 0; i < 50; i++) await (await request('/healthz')).arrayBuffer();
  const results = [];
  results.push(await measure('bootstrap-http', 1000, 32, async () => {
    const response = await request('/api/v1/bootstrap');
    assert.equal(response.status, 200);
    await response.arrayBuffer();
  }));
  results.push(await measure('task-create-and-cancel', 100, 8, async () => {
    const response = await post('/api/v1/tasks', { project_id: 'prj_demo', kind: 'image', model_id: 'studio-image', inputs: { prompt: '本地性能基线' }, parameters: { aspect_ratio: '1:1', count: 1 } });
    assert.equal(response.status, 202);
    const task = await response.json();
    const cancelled = await post(`/api/v1/tasks/${task.id}/cancel`, {});
    assert.equal(cancelled.status, 200);
    await cancelled.arrayBuffer();
  }));
  const firstDelta = [];
  results.push(await measure('llm-full-demo-stream', 12, 4, async () => {
    const started = performance.now();
    const response = await post('/api/v1/runs', { project_id: 'prj_demo', model_id: 'studio-chat', input: '为本地测试写一段创意方向' });
    assert.equal(response.status, 201);
    const run = await response.json();
    const stream = await request(`/api/v1/runs/${run.id}/events`);
    assert.equal(stream.status, 200);
    const reader = stream.body.getReader();
    const decoder = new TextDecoder();
    let received = '';
    let first = true;
    for (;;) {
      const part = await reader.read();
      if (part.done) break;
      received += decoder.decode(part.value, { stream: true });
      assert(received.length < 256 * 1024, 'bounded demo output');
      if (first && received.includes('event: message.delta')) { firstDelta.push(performance.now() - started); first = false; }
    }
    assert.match(received, /event: run.completed/);
    assert(!first, 'stream has a first delta');
  }));
  results.push({ name: 'llm-first-delta-including-submit', ...distribution(firstDelta) });
  const report = {
    measured_at: new Date().toISOString(),
    build: { version: JSON.parse(readFileSync(resolve(root, 'package.json'), 'utf8')).version, binary_sha256: createHash('sha256').update(readFileSync(binary)).digest('hex'), go: execFileSync('go', ['version'], { cwd: root, encoding: 'utf8', windowsHide: true }).trim() },
    scope: 'Isolated loopback HTTP/1.1, memory storage, authentication disabled, demo providers; server and load client share this machine.',
    limitations: ['Not a gateway, database, TLS, cluster, 10000-stream or production SLO test.', 'Latency includes Node fetch and same-machine client scheduling; no CPU/RSS/FD profiling included.', 'Task case measures create plus cancel; demo stream duration is deliberately paced.'],
    environment: { platform: os.platform(), release: os.release(), arch: os.arch(), logical_cpus: os.cpus().length, cpu: os.cpus()[0]?.model, memory_bytes: os.totalmem(), node: process.version },
    results,
  };
  mkdirSync(resolve(root, '.runtime'), { recursive: true });
  const output = resolve(root, '.runtime/performance-local.json');
  writeFileSync(output, JSON.stringify(report, null, 2) + '\n');
  console.log(`Baseline saved: ${output}`);
} catch (error) {
  console.error(error.stack || error.message);
  process.exitCode = 1;
} finally { stopOwnedProcess(child); referenceDirectory.cleanup(); }
