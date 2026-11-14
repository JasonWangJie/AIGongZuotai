import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';

const raw = process.env.STUDIO_TEST_URL;
if (!raw) throw new Error('Set STUDIO_TEST_URL to an isolated local AUTH_MODE=required + PROVIDER_MODE=demo server.');
const base = new URL(raw);
if (!['127.0.0.1', 'localhost', '[::1]'].includes(base.hostname)) throw new Error('Integration tests only allow a local server.');
const suffix = randomUUID().replaceAll('-', '');
const password = 'Test-' + randomUUID();
function client() {
  let cookie = '';
  return async (path, options = {}) => {
    const { expected = 200, body, project, ...rest } = options;
    const headers = { 'Content-Type': 'application/json', Origin: base.origin, ...(cookie ? { Cookie: cookie } : {}), ...(project ? { 'X-Project-ID': project } : {}) };
    const response = await fetch(new URL('/api/v1' + path, base), { ...rest, headers: { ...headers, ...rest.headers }, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(12000) });
    assert.equal(response.status, expected, `${rest.method || 'GET'} ${path}: unexpected status ${response.status}`);
    const setCookie = response.headers.get('set-cookie');
    if (setCookie) { assert.match(setCookie, /httponly/i, 'Session cookie must be HttpOnly'); cookie = setCookie.split(';')[0]; }
    const text = await response.text();
    return text ? JSON.parse(text) : null;
  };
}

const a = client(), b = client();
const anonymous = await a('/auth/session');
assert.equal(anonymous.auth_enabled, true); assert.equal(anonymous.user, null);
await a('/bootstrap', { expected: 401 });
const registered = await a('/auth/register', { method: 'POST', expected: 201, body: { email: `workbench-a-${suffix}@example.invalid`, password, name: '前端联调 A' } });
assert.ok(registered.user.id); const first = registered.project.id;
const boot = await a('/bootstrap');
assert.equal(boot.mode, 'demo', 'No model requests are allowed against a live provider during this test.');
assert.equal(boot.storage, 'postgres'); assert.equal(boot.auth_enabled, true);
assert.equal(boot.user.id, registered.user.id);
const second = await a('/projects', { method: 'POST', expected: 201, body: { name: '前端联调第二项目' } });
const switched = await a('/bootstrap', { project: second.id }); assert.equal(switched.project.id, second.id);
assert.equal((await a('/projects')).items.length, 2);
const run = await a('/runs', { method: 'POST', expected: 201, headers: { 'Idempotency-Key': randomUUID() }, body: { project_id: first, model_id: 'studio-chat', input: '前端联调：简短描述一个创作灵感。' } });
assert.equal(run.project_id, first);
const runList = await a('/runs', { project: first }); assert.ok(runList.items.some(value => value.id === run.id));
assert.equal((await a('/runs', { project: second.id })).items.length, 0);
const task = await a('/tasks', { method: 'POST', expected: 202, headers: { 'Idempotency-Key': randomUUID() }, body: { project_id: second.id, kind: 'image', model_id: 'studio-image', inputs: { prompt: '前端项目隔离测试：月光山谷。' }, parameters: { aspect_ratio: '1:1', count: 1 } } });
assert.equal(task.project_id, second.id);
assert.equal((await a('/tasks', { project: first })).items.length, 0);
assert.ok((await a('/tasks', { project: second.id })).items.some(value => value.id === task.id));
const other = await b('/auth/register', { method: 'POST', expected: 201, body: { email: `workbench-b-${suffix}@example.invalid`, password, name: '前端联调 B' } });
assert.notEqual(other.workspace.id, registered.workspace.id);
assert.equal((await b('/runs')).items.length, 0);
await b(`/runs/${run.id}/snapshot`, { expected: 404 });
await b(`/tasks/${task.id}`, { expected: 404 });
await b('/bootstrap', { project: first, expected: 404 });
await a('/auth/logout', { method: 'POST' });
assert.equal((await a('/auth/session')).user, null);
await a('/bootstrap', { expected: 401 });
await a('/auth/login', { method: 'POST', body: { email: `workbench-a-${suffix}@example.invalid`, password } });
assert.equal((await a('/bootstrap', { project: second.id })).project.id, second.id);
assert.ok((await a('/tasks', { project: second.id })).items.some(value => value.id === task.id));
await a('/auth/logout', { method: 'POST' }); await b('/auth/logout', { method: 'POST' });
console.log('PASS: cookie registration/login/logout, PostgreSQL bootstrap, project creation/switch/list isolation, account resource isolation, expired-session 401, re-login history.');
console.log('Fixture accounts use workbench-a/b-<random>@example.invalid in the isolated test database; no credentials were logged and no live provider was called.');
