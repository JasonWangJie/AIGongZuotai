import assert from 'node:assert/strict';
import http from 'node:http';
import https from 'node:https';
import { once } from 'node:events';
import { setImmediate as nextTurn } from 'node:timers/promises';
import { before, test } from 'node:test';
import { createLocalIngressTLS, startLocalIngress, requestLocalIngress } from './local-ingress.mjs';

let tls;
before(async () => { tls = await createLocalIngressTLS(); });
const userId = 'usr_' + '1'.repeat(32);
const draft = '/api/v1/projects/project/draft';

function signal() {
  let resolve;
  const promise = new Promise(ok => { resolve = ok; });
  return { promise, resolve };
}

async function backend(t, handler) {
  const server = http.createServer(handler);
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  t.after(async () => {
    const stopped = new Promise(ok => server.close(ok));
    server.closeAllConnections();
    await stopped;
  });
  return 'http://127.0.0.1:' + server.address().port;
}

async function launch(t, target) {
  const proxy = await startLocalIngress({ ...tls, initialTarget: target });
  t.after(() => proxy.close());
  return { proxy, call: (path, options = {}) => requestLocalIngress(proxy.origin, { cert: tls.cert, path, ...options }) };
}

test('maintenance seals new traffic, drains a real mutation and download, and cuts only the established SSE', async t => {
  const startedWrite = signal(), startedDownload = signal(), startedSSE = signal();
  const releaseWrite = signal(), releaseDownload = signal(), disconnectedSSE = signal();
  let mutations = 0;
  const old = await backend(t, async (req, res) => {
    if (req.url === '/write') {
      mutations++;
      const chunks = [];
      for await (const part of req) chunks.push(part);
      startedWrite.resolve();
      await releaseWrite.promise;
      res.end(Buffer.concat(chunks));
    } else if (req.url === '/download') {
      res.writeHead(200, { 'content-type': 'application/octet-stream' });
      res.write('first');
      startedDownload.resolve();
      await releaseDownload.promise;
      res.end('last');
    } else if (req.url === '/events') {
      res.writeHead(200, { 'content-type': 'text/event-stream' });
      res.write('data: initial\n\n');
      res.on('close', disconnectedSSE.resolve);
      startedSSE.resolve();
    } else res.end('old');
  });
  const { proxy, call } = await launch(t, old);
  const write = call('/write', { method: 'POST', body: 'pending mutation' });
  const download = call('/download');
  const events = call('/events').catch(() => 'interrupted');
  await Promise.all([startedWrite.promise, startedDownload.promise, startedSSE.promise]);
  const draining = proxy.maintain({ timeoutMs: 3000, streamGraceMs: 20 });
  assert.equal(proxy.snapshot().phase, 'maintenance');
  assert.equal((await call('/write', { method: 'POST', body: 'second' })).status, 503);
  await disconnectedSSE.promise;
  assert.equal(await events, 'interrupted');
  assert.equal(mutations, 1);
  const deadline = performance.now() + 1000;
  while (proxy.snapshot().inflight === 3 && performance.now() < deadline) await nextTurn();
  assert.equal(proxy.snapshot().inflight, 2);
  assert.throws(() => proxy.candidate(old, { userId, paths: [draft] }));
  releaseWrite.resolve(); releaseDownload.resolve();
  assert.equal((await write).body.toString(), 'pending mutation');
  assert.equal((await download).body.toString(), 'firstlast');
  const result = await draining;
  assert.deepEqual(result, { phase: 'maintenance', inflight: 0, streams: 0, interruptedStreams: 1,
    unknownWrites: 0, verified: 0, total: 0 });
});

test('deadline leaves a finite request and maintenance intact, then explicit drain can finish', async t => {
  const admitted = signal(), release = signal();
  const target = await backend(t, async (_req, res) => {
    res.write('still ');
    admitted.resolve();
    await release.promise;
    res.end('downloading');
  });
  const { proxy, call } = await launch(t, target);
  const download = call('/download');
  await admitted.promise;
  await assert.rejects(proxy.maintain({ timeoutMs: 35, streamGraceMs: 0 }), /Drain unconfirmed/);
  assert.equal((await call('/new')).status, 503);
  assert.throws(() => proxy.candidate(target, { userId, paths: [draft] }));
  release.resolve();
  assert.equal((await download).body.toString(), 'still downloading');
  assert.equal((await proxy.maintain()).inflight, 0);
});

test('write disconnected after upload keeps its upstream slot until the actual server response', async t => {
  const admitted = signal(), release = signal();
  const target = await backend(t, async (req, res) => {
    for await (const _ of req) { /* consume complete command */ }
    admitted.resolve();
    await release.promise;
    res.end('committed');
  });
  const { proxy } = await launch(t, target);
  const request = https.request(proxy.origin + '/write', { method: 'POST', ca: tls.cert, agent: false });
  request.on('error', () => {});
  request.end('original stable key');
  await admitted.promise;
  request.destroy();
  await assert.rejects(proxy.maintain({ timeoutMs: 65, streamGraceMs: 0 }), /Drain unconfirmed/);
  release.resolve();
  assert.equal((await proxy.maintain()).unknownWrites, 0);
});

test('an upstream reset after accepting a write keeps the switch closed without replay', async t => {
  let submissions = 0;
  const target = await backend(t, async (req, res) => {
    for await (const _ of req) { /* emulate commit without reply */ }
    submissions++;
    res.destroy();
  });
  const { proxy, call } = await launch(t, target);
  assert.equal((await call('/write', { method: 'POST', body: 'payload' })).status, 502);
  await assert.rejects(proxy.maintain(), /Drain unconfirmed/);
  assert.equal(proxy.snapshot().unknownWrites, 1);
  assert.equal(submissions, 1);
  assert.throws(() => proxy.candidate(target, { userId, paths: [draft] }));
});

test('TLS, origin, scoped authenticated canary, clean headers and explicit unified opening', async t => {
  let allowDraft = false, ready = false, revoked = false, privateReads = 0;
  const target = await backend(t, (req, res) => {
    if (req.url === '/api/v1/auth/session') {
      const allowed = !revoked && req.headers.cookie === 'studio_session=local-fixture';
      return res.end(JSON.stringify({ auth_enabled: true, account_restricted: false,
        user: { id: allowed ? userId : 'usr_' + '2'.repeat(32) } }));
    }
    if (req.url === draft) {
      privateReads++;
      if (!allowDraft) res.statusCode = 409;
      return res.end('draft');
    }
    if (req.url === '/readyz') return res.end(JSON.stringify({ ready, state: ready ? 'ready' : 'stopping' }));
    if (req.url === '/headers') {
      res.setHeader('set-cookie', 'studio_session=local-fixture; HttpOnly; SameSite=Strict');
      return res.end(JSON.stringify({ host: req.headers.host, origin: req.headers.origin, xff: req.headers['x-forwarded-for'] }));
    }
    res.end('candidate');
  });
  const { proxy, call } = await launch(t);
  const asOwner = { headers: { cookie: 'studio_session=local-fixture' } };
  assert.equal((await call('/release-identity.json')).status, 503);
  assert.throws(() => proxy.candidate(target, { userId, paths: ['/readyz'] }));
  proxy.candidate(target, { userId, paths: ['/readyz', '/release-identity.json', draft] });
  assert.throws(() => proxy.open());
  assert.equal((await call('/release-identity.json')).status, 403);
  assert.equal((await call(draft, { headers: { cookie: 'studio_session=other' } })).status, 403);
  assert.equal(privateReads, 0);
  assert.equal((await call(draft, { ...asOwner, method: 'POST' })).status, 503);
  assert.equal((await call('/api/v1/not-approved', asOwner)).status, 503);
  assert.equal((await call('/release-identity.json', asOwner)).body.toString(), 'candidate');
  assert.equal((await call('/readyz', asOwner)).status, 200);
  assert.equal((await call(draft, asOwner)).status, 409);
  assert.equal(proxy.snapshot().verified, 1);
  ready = true;
  assert.equal((await call('/readyz', asOwner)).status, 200);
  assert.throws(() => proxy.open());
  allowDraft = true;
  revoked = true;
  assert.equal((await call(draft, asOwner)).status, 403);
  revoked = false;
  assert.equal((await call(draft, asOwner)).body.toString(), 'draft');
  assert.equal(proxy.snapshot().verified, 3);
  proxy.open();
  assert.equal((await call('/release-identity.json')).status, 200);
  assert.equal((await call('/headers', { headers: { origin: 'https://foreign.invalid' } })).status, 403);
  const checked = await call('/headers', { headers: { origin: proxy.origin, 'x-forwarded-for': 'spoof' } });
  assert.deepEqual(JSON.parse(checked.body), { host: new URL(target).host, origin: target });
  assert.match(checked.headers['set-cookie'][0], /; Secure$/);
  const wrongTLS = await createLocalIngressTLS();
  await assert.rejects(requestLocalIngress(proxy.origin, { cert: wrongTLS.cert, path: '/readyz' }), /probe failed/);
  await assert.rejects(startLocalIngress({ ...tls, port: Number(new URL(proxy.origin).port) }), { code: 'EADDRINUSE' });
  assert.equal((await call('/readyz')).status, 200);
});
