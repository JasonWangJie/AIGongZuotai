import assert from 'node:assert/strict';
import { createHash, randomUUID } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import http from 'node:http';
import net from 'node:net';
import { resolve } from 'node:path';
import { binary, root, start, stopOwnedProcess, isolatedReferenceDirectory, isolatedMailEnvironment, isolatedBillingEnvironment, isolatedOperationsEnvironment, isolatedModerationEnvironment, isolatedExecutionEnvironment } from './lib/process.mjs';

// Real built Go server and real loopback HTTP fixture. No developer database,
// configured upstream, paid model, or browser automation participates.
const directories = isolatedReferenceDirectory();
const catalogPath = resolve(directories.directory, '..', 'conversation-catalog.json');
const fixtureKey = 'local-conversation-fixture-only';
const metricToken = 'conversation-smoke-independent-metrics-token';
const captured = [], checks = [], fixtureErrors = [];
const started = Date.now();
let holdNext = false, server, startError;
const pause = ms => new Promise(done => setTimeout(done, ms));
const fixture = http.createServer(async (req, res) => {
  try {
    assert.equal(req.url, '/v1/chat/completions');
    assert.equal(req.method, 'POST');
    assert.equal(req.headers.authorization, `Bearer ${fixtureKey}`);
    assert.equal(req.headers['idempotency-key'], undefined);
    const parts = []; let bytes = 0;
    for await (const part of req) { bytes += part.length; assert(bytes < 1024 * 1024); parts.push(part); }
    const body = JSON.parse(Buffer.concat(parts).toString('utf8'));
    assert.equal(body.model, 'private-fixture-model'); assert.equal(body.stream, true);
    const answer = `fixture 回答 ${captured.length + 1}：原分支内容保留。`;
    captured.push({ body, answer });
    res.writeHead(200, { 'Content-Type': 'text/event-stream' });
    res.write(`data: ${JSON.stringify({ choices: [{ delta: { content: answer } }] })}\n\n`);
    if (holdNext) { holdNext = false; return; }
    res.end('data: [DONE]\n\n');
  } catch (error) { fixtureErrors.push(error.message); if (!res.headersSent) res.writeHead(500); res.end(); }
});
await new Promise((done, fail) => { fixture.once('error', fail); fixture.listen(0, '127.0.0.1', done); });
const fixtureBase = `http://127.0.0.1:${fixture.address().port}`;
const port = await new Promise((done, fail) => {
  const probe = net.createServer(); probe.once('error', fail);
  probe.listen(0, '127.0.0.1', () => { const selected = probe.address().port; probe.close(() => done(selected)); });
});
const base = `http://127.0.0.1:${port}`;
const request = (path, options = {}) => fetch(base + path, { ...options, headers: { 'X-Project-ID': 'prj_demo', ...options.headers }, signal: AbortSignal.timeout(10000) });
async function json(response) { assert(response.ok, `HTTP ${response.status}: ${await response.clone().text()}`); return response.json(); }
const post = (path, body, key = randomUUID()) => request(path, { method: 'POST', headers: { Origin: base, 'Content-Type': 'application/json', 'Idempotency-Key': key }, body: JSON.stringify(body) });
async function waitRun(id, predicate = run => ['COMPLETED', 'CANCELLED', 'FAILED', 'INTERRUPTED'].includes(run.status)) {
  const deadline = Date.now() + 10000;
  for (;;) {
    assert.deepEqual(fixtureErrors, []);
    const run = await json(await request(`/api/v1/runs/${id}/snapshot`));
    if (predicate(run)) return run;
    if (Date.now() > deadline) throw Error('Conversation run did not reach the expected state');
    await pause(30);
  }
}
const conversation = id => request(`/api/v1/conversations/${id}`).then(json);
const pathMessages = (id, leaf, suffix = '') => request(`/api/v1/conversations/${id}/messages?leaf_id=${leaf}${suffix}`).then(json);
try {
  writeFileSync(catalogPath, JSON.stringify({ schema_version: 1, models: [{ id: 'fixture-chat', name: '上下文验收模型', kind: 'chat', description: '本机fixture，无付费调用', enabled: true, upstream_model: 'private-fixture-model', chat_output_limit_field: 'max_completion_tokens', capabilities: { max_count: 1, max_prompt_bytes: 16000, parameter_presets: [], context_window_tokens: 2048, max_output_tokens: 256 } }] }));
  server = start(binary, [], { env: { ...process.env, ...isolatedMailEnvironment,...isolatedBillingEnvironment,...isolatedOperationsEnvironment,...isolatedModerationEnvironment,...isolatedExecutionEnvironment, APP_ADDR: `127.0.0.1:${port}`, APP_WEB_DIR: resolve(root, 'apps/workbench/dist'), MODEL_CATALOG_FILE: catalogPath, REFERENCE_DIR: directories.directory, ASSET_DIR: directories.assetDirectory, ASSET_STORAGE_BACKEND: 'local', S3_FORCE_PATH_STYLE: 'false',
    DATABASE_URL: '', AUTH_MODE: 'disabled', PROVIDER_MODE: 'live', METRICS_TOKEN: metricToken, NEWAPI_BASE_URL: `${fixtureBase}/v1`, NEWAPI_API_KEY: fixtureKey, NEWAPI_CHAT_MODEL: '', ASYNC_IMAGE_BASE_URL: '', ASYNC_IMAGE_API_KEY: '', ASYNC_IMAGE_MODEL: '', ASYNC_VIDEO_MODEL: '',
    ADMISSION_WORKSPACE_RUNS: '64', ADMISSION_MODEL_RUNS: '32', ADMISSION_WORKSPACE_TASKS: '128', ADMISSION_MODEL_TASKS: '64', ADMISSION_WORKSPACE_STREAMS: '32', TASK_LEASE_SECONDS: '30' }, stdio: ['ignore', 'ignore', 'pipe'] });
  server.once('error', error => { startError = error; }); server.stderr.on('data', () => {});
  const readyDeadline = Date.now() + 10000;
  for (;;) {
    if (startError) throw startError;
    if (server.exitCode !== null) throw Error('Conversation server exited during startup');
    try { const ready = await request('/readyz'); if (ready.ok) { await ready.text(); break; } } catch { /* startup only */ }
    if (Date.now() > readyDeadline) throw Error('Conversation startup timeout');
    await pause(50);
  }
  const catalog = await json(await request('/api/v1/models')); const model = catalog.models[0];
  assert.equal(model.capabilities.context_window_tokens, 2048); assert.equal(model.capabilities.max_output_tokens, 256);
  assert(!JSON.stringify(catalog).includes('private-fixture-model')); assert(!JSON.stringify(catalog).includes('chat_output_limit_field'));
  const newBody = { project_id: 'prj_demo', title: '分支与上下文验收' }, newKey = randomUUID();
  const created = await post('/api/v1/conversations', newBody, newKey); assert.equal(created.status, 201);
  const conv = await json(created); assert.equal(conv.version, 1); assert.equal(conv.head_message_id, null);
  const replay = await post('/api/v1/conversations', newBody, newKey); assert.equal(replay.headers.get('idempotency-replayed'), 'true'); assert.equal((await json(replay)).id, conv.id);
  assert.equal((await post('/api/v1/conversations', { ...newBody, title: '冲突名称' }, newKey)).status, 409);
  const body = (version, parent, input, extra = {}) => ({ expected_version: version, parent_message_id: parent, model_id: model.id, model_version: model.version, operation: 'reply', input, ...extra });
  const first = await json(await post(`/api/v1/conversations/${conv.id}/runs`, body(1, null, '我的项目叫星舟。')));
  const firstDone = await waitRun(first.id); assert.equal(firstDone.status, 'COMPLETED');
  assert.deepEqual(captured[0].body.messages, [{ role: 'user', content: '我的项目叫星舟。' }]);
  assert.equal(captured[0].body.max_completion_tokens, 256); assert.equal(captured[0].body.max_tokens, undefined);
  const secondInput = '根据刚才的项目名写一个标题。';
  const second = await json(await post(`/api/v1/conversations/${conv.id}/runs`, body(first.conversation_version, first.assistant_message_id, secondInput)));
  assert.equal((await waitRun(second.id)).status, 'COMPLETED');
  assert.deepEqual(captured[1].body.messages, [{ role: 'user', content: '我的项目叫星舟。' }, { role: 'assistant', content: firstDone.output }, { role: 'user', content: secondInput }]);
  assert.equal(second.context_summary.included_messages, 3); assert.equal(second.context_summary.omitted_messages, 0);
  checks.push('real structured multi-turn messages and explicit output budget reach the loopback gateway; private model bindings remain server-side');

  const editBody = body(second.conversation_version, null, '项目改名银海，创建独立的新分支。', { edited_message_id: first.user_message_id }); const editKey = randomUUID();
  const edited = await json(await post(`/api/v1/conversations/${conv.id}/runs`, editBody, editKey)); await waitRun(edited.id);
  assert.deepEqual(captured[2].body.messages, [{ role: 'user', content: editBody.input }]);
  const oldPath = await pathMessages(conv.id, second.assistant_message_id); assert.equal(oldPath.items.length, 4); assert.equal(oldPath.items[0].content, '我的项目叫星舟。');
  const editPath = await pathMessages(conv.id, edited.assistant_message_id); assert.equal(editPath.items.length, 2); assert.equal(editPath.items[0].content, editBody.input);
  const latestPage = await pathMessages(conv.id, second.assistant_message_id, '&limit=2'); assert(latestPage.has_more); assert.equal(latestPage.items.length, 2);
  const olderPage = await pathMessages(conv.id, second.assistant_message_id, `&limit=2&before=${encodeURIComponent(latestPage.next_cursor)}`); assert.deepEqual(olderPage.items.map(m => m.id), oldPath.items.slice(0, 2).map(m => m.id));
  const wrongBranch = await request(`/api/v1/conversations/${conv.id}/messages?leaf_id=${edited.assistant_message_id}&before=${encodeURIComponent(latestPage.next_cursor)}`); assert.equal(wrongBranch.status, 400);
  const branches = await json(await request(`/api/v1/conversations/${conv.id}/branches?limit=1`)); assert.equal(branches.items.length, 1); assert(branches.next_cursor);
  const nextBranch = await json(await request(`/api/v1/conversations/${conv.id}/branches?limit=1&cursor=${encodeURIComponent(branches.next_cursor)}`)); assert.equal(nextBranch.items.length, 1);
  const beforeReplay = captured.length; const editReplay = await post(`/api/v1/conversations/${conv.id}/runs`, editBody, editKey); assert.equal(editReplay.headers.get('idempotency-replayed'), 'true'); assert.equal((await json(editReplay)).id, edited.id); assert.equal(captured.length, beforeReplay);
  assert.equal((await post(`/api/v1/conversations/${conv.id}/runs`, { ...editBody, input: 'changed' }, editKey)).status, 409);
  assert.equal((await post(`/api/v1/conversations/${conv.id}/runs`, body(1, edited.assistant_message_id, 'stale'))).status, 409);
  const other = await json(await post('/api/v1/conversations', { project_id: 'prj_demo', title: '另一个会话' }));
  assert.equal((await post(`/api/v1/conversations/${other.id}/runs`, body(1, edited.assistant_message_id, 'cross conversation'))).status, 404);
  const list = await json(await request('/api/v1/conversations?limit=1')); assert.equal(list.items.length, 1); assert(list.next_cursor);
  const listOlder = await json(await request(`/api/v1/conversations?limit=1&cursor=${encodeURIComponent(list.next_cursor)}`)); assert.equal(listOlder.items.length, 1); assert.notEqual(listOlder.items[0].id, list.items[0].id);
  assert.equal((await request(`/api/v1/conversations/${conv.id}`, { headers: { 'X-Project-ID': 'prj_missing' } })).status, 404);
  checks.push('immutable edit branches, branch-bound pagination, stable conversation keysets, parent isolation, CAS and original-key replay without extra generation');

  holdNext = true;
  const beforeHold = await conversation(conv.id);
  const held = await json(await post(`/api/v1/conversations/${conv.id}/runs`, body(beforeHold.version, edited.assistant_message_id, '保留部分回答，用于显式继续验收。')));
  await waitRun(held.id, run => run.status === 'RUNNING' && run.output.length > 0);
  const busy = await post(`/api/v1/conversations/${conv.id}/runs`, body(held.conversation_version, edited.assistant_message_id, 'should be busy')); assert.equal(busy.status, 409); assert.equal((await busy.json()).error.code, 'CONVERSATION_BUSY');
  const cancelled = await json(await post(`/api/v1/runs/${held.id}/cancel`, {})); assert.equal(cancelled.status, 'CANCELLED'); assert(cancelled.output.length > 0);
  const afterStop = await conversation(conv.id); assert.equal(afterStop.active_run_id, null);
  const continued = await json(await post(`/api/v1/conversations/${conv.id}/runs`, body(afterStop.version, held.assistant_message_id, '', { operation: 'continue' })));
  assert.equal((await waitRun(continued.id)).status, 'COMPLETED'); assert.notEqual(continued.id, held.id);
  const continuedRequest = captured.at(-1).body.messages;
  assert.equal(continuedRequest.at(-1).content, '请从上一条回复结束处继续，不要重复已有内容。'); assert.equal(continuedRequest.at(-2).content, cancelled.output);
  assert.equal((await json(await request(`/api/v1/runs/${held.id}/snapshot`))).status, 'CANCELLED');
  checks.push('one active run per conversation, partial stop retained and explicit continuation starts one new request using the original partial answer');

  const budgetConv = await json(await post('/api/v1/conversations', { project_id: 'prj_demo', title: '预算裁剪验收' }));
  const budgetFirst = await json(await post(`/api/v1/conversations/${budgetConv.id}/runs`, body(1, null, 'A'.repeat(900)))); await waitRun(budgetFirst.id);
  const budgetSecond = await json(await post(`/api/v1/conversations/${budgetConv.id}/runs`, body(budgetFirst.conversation_version, budgetFirst.assistant_message_id, 'B'.repeat(900)))); await waitRun(budgetSecond.id);
  assert.equal(budgetSecond.context_summary.omitted_messages, 2); assert.equal(budgetSecond.context_summary.included_messages, 1); assert.equal(captured.at(-1).body.messages.length, 1); assert.equal(captured.at(-1).body.messages[0].content, 'B'.repeat(900));
  assert(budgetSecond.context_summary.input_tokens_estimate + budgetSecond.context_summary.output_tokens_reserved <= 2048);
  const beforeReject = captured.length;
  const tooLarge = await post(`/api/v1/conversations/${budgetConv.id}/runs`, body(budgetSecond.conversation_version, budgetSecond.assistant_message_id, 'Z'.repeat(1800)));
  assert.equal(tooLarge.status, 422); assert.equal((await tooLarge.json()).error.code, 'CONTEXT_TOO_LARGE'); assert.equal(captured.length, beforeReject); assert.equal((await conversation(budgetConv.id)).version, budgetSecond.conversation_version);
  checks.push('whole old turns are omitted with an explicit estimate; newest-input overflow is rejected before upstream calls or conversation mutation');

  const publicAudit = JSON.stringify(await json(await request('/api/v1/audit?limit=100')));
  for (const secret of ['我的项目叫星舟', editBody.input, fixtureKey, 'private-fixture-model']) assert(!publicAudit.includes(secret));
  const metricText = await (await request('/metrics', { headers: { Authorization: `Bearer ${metricToken}` } })).text();
  for (const route of ['conversations', 'conversation', 'conversation_messages', 'conversation_branches', 'conversation_runs']) assert(metricText.includes(`route="${route}"`));
  for (const secret of [conv.id, first.id, fixtureKey, metricToken, '分支与上下文验收']) assert(!metricText.includes(secret));
  assert.deepEqual(fixtureErrors, []);
  checks.push('conversation audit redaction and fixed route labels omit titles, message text, resource identifiers and provider credentials');
  mkdirSync(resolve(root, '.runtime'), { recursive: true });
  writeFileSync(resolve(root, '.runtime/conversation-smoke.json'), JSON.stringify({ version: JSON.parse(readFileSync(resolve(root, 'package.json'), 'utf8')).version, binary_sha256: createHash('sha256').update(readFileSync(binary)).digest('hex'), verified_at: new Date().toISOString(), duration_ms: Date.now() - started, scope: 'Isolated built Go service, memory store and real loopback HTTP chat fixture; no paid upstream or developer database. PostgreSQL durability is tested separately.', provider_requests: captured.length, checks, status: 'passed' }, null, 2) + '\n');
  console.log(`PASS: ${checks.length} conversation/context integration groups; evidence .runtime/conversation-smoke.json.`);
} finally {
  stopOwnedProcess(server);
  fixture.closeAllConnections();
  await new Promise(done => fixture.close(done));
  directories.cleanup();
}
