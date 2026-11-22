import { execFile } from 'node:child_process';
import http from 'node:http';
import https from 'node:https';
import { finished } from 'node:stream/promises';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';

const execute = promisify(execFile);
const loopback = '127.0.0.1';
const sessionPath = '/api/v1/auth/session';
const safeRead = new Set(['GET', 'HEAD']);
const methods = new Set(['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS']);
const hopHeaders = new Set(['connection', 'keep-alive', 'proxy-authenticate', 'proxy-authorization',
  'te', 'trailer', 'transfer-encoding', 'upgrade', 'forwarded', 'via']);

function localOrigin(value, scheme) {
  const url = new URL(value);
  if (url.protocol !== scheme || url.hostname !== loopback || !url.port || url.origin !== value) {
    throw new Error('Expected a literal loopback origin and an explicit port');
  }
  return url;
}

function validPath(value) {
  return typeof value === 'string' && value.length <= 4096 && value.startsWith('/') &&
    !value.startsWith('//') && !/[\s\\#\x00-\x1f\x7f]/.test(value);
}

function bounded(value, min, max) {
  if (!Number.isInteger(value) || value < min || value > max) throw new Error('Invalid ingress bound');
  return value;
}

function cleanHeaders(headers) {
  const nominated = new Set(String(headers.connection || '').toLowerCase().split(',').map(s => s.trim()));
  return Object.fromEntries(Object.entries(headers).filter(([name]) =>
    !hopHeaders.has(name) && !nominated.has(name) && !name.startsWith('x-forwarded-')));
}

function refuse(res, status) {
  if (res.destroyed || res.headersSent) return;
  res.writeHead(status, { 'cache-control': 'no-store', 'content-length': 0, connection: 'close' });
  res.end();
}

export async function createLocalIngressTLS() {
  const source = fileURLToPath(new URL('./localcert/main.go', import.meta.url));
  try {
    const { stdout } = await execute('go', ['run', source], { timeout: 60_000, maxBuffer: 16 << 10, windowsHide: true });
    return JSON.parse(stdout);
  } catch {
    throw new Error('Could not generate a private, temporary loopback TLS certificate');
  }
}

// Strictly bounded control-plane GET. The traffic path below streams large
// files and SSE directly. Never follow a redirect or use the ambient CA set.
async function collect(client, options, body, limit) {
  return new Promise((resolve, reject) => {
    const request = client.request(options, res => {
      const chunks = [];
      let bytes = 0;
      res.on('data', chunk => {
        bytes += chunk.length;
        if (bytes > limit) res.destroy(new Error('Probe body exceeds limit'));
        else chunks.push(chunk);
      });
      res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks) }));
      res.on('error', () => reject(new Error('Probe response was incomplete')));
    });
    request.on('error', () => reject(new Error('Loopback probe failed')));
    request.end(body);
  });
}

export function requestLocalIngress(origin, { cert, path, method = 'GET', headers = {}, body, timeoutMs = 10_000 }) {
  const url = localOrigin(origin, 'https:');
  if (!cert || !validPath(path) || !methods.has(method)) throw new Error('Invalid local probe');
  return collect(https, { hostname: loopback, port: url.port, path, method, headers, ca: cert, agent: false,
    rejectUnauthorized: true, signal: AbortSignal.timeout(bounded(timeoutMs, 1, 120_000)) }, body, 10 << 20);
}

async function candidateUser(target, cookie, signal) {
  if (!cookie || cookie.length > 8192) return null;
  try {
    const data = await collect(http, { hostname: loopback, port: target.port, path: sessionPath, method: 'GET',
      headers: { cookie }, agent: false, signal: AbortSignal.any([signal, AbortSignal.timeout(4000)]) }, undefined, 128 << 10);
    if (data.status !== 200) return null;
    const session = JSON.parse(data.body);
    return session.auth_enabled === true && session.account_restricted === false ? session.user?.id : null;
  } catch {
    return null;
  }
}

// A disconnected client may have sent a complete mutation. Keep consuming
// the real upstream response so that draining cannot outrun that handler.
function relay(req, res, target, entry) {
  return new Promise(resolve => {
    const headers = cleanHeaders(req.headers);
    headers.host = target.host;
    if (req.headers.origin) headers.origin = target.origin;
    const upstream = http.request({ hostname: loopback, port: target.port, path: req.url, method: req.method,
      headers, agent: false, maxHeaderSize: 32 << 10 }, incoming => {
      const status = incoming.statusCode;
      entry.stream = safeRead.has(req.method) && /^text\/event-stream(?:;|$)/i.test(incoming.headers['content-type'] || '');
      let readyBytes = 0;
      const readyChunks = [];
      if (entry.checkReady) incoming.on('data', chunk => {
        readyBytes += chunk.length;
        if (readyBytes <= 2048) readyChunks.push(chunk);
      });
      const outgoing = cleanHeaders(incoming.headers);
      if (outgoing['set-cookie']) {
        outgoing['set-cookie'] = outgoing['set-cookie'].map(v => /;\s*secure(?:;|$)/i.test(v) ? v : v + '; Secure');
      }
      if (!res.destroyed) {
        res.writeHead(status, outgoing);
        res.flushHeaders();
      }
      incoming.on('end', () => {
        let verifiedReady = !entry.checkReady;
        if (entry.checkReady && readyBytes <= 2048) {
          try {
            const health = JSON.parse(Buffer.concat(readyChunks));
            verifiedReady = health.ready === true && health.state === 'ready';
          } catch { /* A malformed health response cannot clear the gate. */ }
        }
        resolve({ complete: incoming.complete, status, verifiedReady });
      });
      incoming.on('error', () => { res.destroy(); resolve({ complete: false }); });
      incoming.on('close', () => {
        if (!incoming.complete) { res.destroy(); resolve({ complete: false }); }
      });
      if (res.destroyed) incoming.resume();
      else {
        // Continue reading an already submitted mutation if its client leaves.
        res.once('close', () => { if (!res.writableFinished) incoming.resume(); });
        incoming.pipe(res);
      }
    });
    upstream.on('error', () => {
      if (res.headersSent) res.destroy();
      else refuse(res, 502);
      resolve({ complete: false });
    });
    entry.interrupt = () => { upstream.destroy(); res.destroy(); };
    req.once('aborted', () => upstream.destroy());
    res.once('close', () => {
      if (!res.writableFinished && (safeRead.has(req.method) || !req.complete)) upstream.destroy();
    });
    req.pipe(upstream);
  });
}

// Controllers are direct module callers. There is no HTTP control route or
// durable token that could reopen a failed/abandoned transition.
export async function startLocalIngress({ cert, key, initialTarget, port = 0 }) {
  bounded(port, 0, 65535);
  if (!cert || !key) throw new Error('Local ingress requires a TLS certificate and key');
  let target = initialTarget ? localOrigin(initialTarget, 'http:') : null;
  let phase = target ? 'open' : 'maintenance';
  let permit = null;
  let unknownWrites = 0;
  let interruptedStreams = 0;
  let draining = false;
  const active = new Set();
  const sockets = new Set();
  const listeners = new Set();
  const changed = () => { for (const fn of [...listeners]) fn(); };
  let origin;
  function snapshot() {
    return { phase, inflight: active.size, streams: [...active].filter(entry => entry.stream).length,
      interruptedStreams, unknownWrites, verified: permit?.verified.size || 0, total: permit?.paths.size || 0 };
  }
  async function handle(req, res) {
    if (req.headers.host !== new URL(origin).host || (req.headers.origin && req.headers.origin !== origin) ||
        req.headers['sec-fetch-site'] === 'cross-site' || !validPath(req.url) || !methods.has(req.method)) return refuse(res, 403);
    if (phase === 'closed' || phase === 'maintenance' || active.size >= 64) return refuse(res, 503);
    const trial = phase === 'candidate' ? permit : null;
    if (trial && (req.method !== 'GET' || !trial.paths.has(req.url))) return refuse(res, 503);
    const destination = target;
    const entry = { stream: false, controller: new AbortController(), checkReady: !!trial && req.url === '/readyz' };
    active.add(entry);
    const sent = finished(res, { readable: false, cleanup: true }).then(() => true, () => false);
    try {
      if (trial) {
        if (await candidateUser(destination, req.headers.cookie, entry.controller.signal) !== trial.userId ||
            phase !== 'candidate' || permit !== trial || req.destroyed) return refuse(res, 403);
      }
      const response = await relay(req, res, destination, entry);
      if (!response.complete && !safeRead.has(req.method)) unknownWrites++;
      const delivered = await sent;
      if (trial && phase === 'candidate' && permit === trial) {
        if (response.complete && delivered && response.status === 200 && response.verifiedReady) trial.verified.add(req.url);
        else trial.verified.delete(req.url);
      }
    } finally {
      active.delete(entry);
      changed();
    }
  }
  const server = https.createServer({ cert, key, minVersion: 'TLSv1.2', maxHeaderSize: 16 << 10,
    headersTimeout: 10_000, requestTimeout: 120_000, keepAliveTimeout: 5000 }, (req, res) => {
    handle(req, res).catch(() => { if (res.headersSent) res.destroy(); else refuse(res, 502); });
  });
  server.maxConnections = 128;
  server.on('connection', socket => { sockets.add(socket); socket.once('close', () => sockets.delete(socket)); });
  server.on('upgrade', (_req, socket) => socket.destroy());
  server.on('connect', (_req, socket) => socket.destroy());
  server.on('clientError', (_err, socket) => socket.destroy());
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen({ host: loopback, port, exclusive: true }, () => { server.off('error', reject); resolve(); });
  });
  origin = 'https://' + loopback + ':' + server.address().port;
  function waitForEmpty(timeoutMs) {
    if (!active.size) return Promise.resolve(true);
    return new Promise(resolve => {
      function done(empty) { clearTimeout(timer); listeners.delete(wake); resolve(empty); }
      const wake = () => { if (!active.size) done(true); };
      const timer = setTimeout(() => done(!active.size), timeoutMs);
      listeners.add(wake);
    });
  }
  async function maintain({ timeoutMs = 10_000, streamGraceMs = Math.min(250, timeoutMs) } = {}) {
    bounded(timeoutMs, 1, 120_000);
    bounded(streamGraceMs, 0, timeoutMs);
    if (draining || phase === 'closed') throw new Error('Ingress transition is busy or closed');
    draining = true;
    phase = 'maintenance';
    permit = null;
    // An admitted GET may first announce SSE after maintenance began.
    const since = performance.now();
    const interruptStreams = () => {
      for (const entry of active) {
        if (entry.stream && !entry.interrupted) {
          entry.interrupted = true; interruptedStreams++; entry.interrupt?.();
        }
      }
    };
    try {
      await waitForEmpty(streamGraceMs);
      interruptStreams();
      const poll = setInterval(interruptStreams, 20);
      try {
        const empty = await waitForEmpty(Math.max(0, timeoutMs - (performance.now() - since)));
        if (!empty || unknownWrites) throw new Error('Drain unconfirmed; ingress remains in maintenance');
        target = null;
        return snapshot();
      } finally { clearInterval(poll); }
    } finally { draining = false; }
  }
  function candidate(value, { userId, paths }) {
    if (draining || phase !== 'maintenance' || target || active.size || unknownWrites) throw new Error('A confirmed drain is required');
    const endpoint = localOrigin(value, 'http:');
    if (!/^usr_[a-f0-9]{32}$/.test(userId) || !Array.isArray(paths) || paths.length < 1 || paths.length > 8 ||
        !paths.every(validPath) || !paths.some(p => p.startsWith('/api/v1/') && !p.startsWith('/api/v1/auth/')) ||
        !paths.includes('/readyz') || new Set(paths).size !== paths.length) throw new Error('Invalid authenticated candidate probe plan');
    target = endpoint;
    permit = { userId, paths: new Set(paths), verified: new Set() };
    phase = 'candidate';
  }
  function open() {
    if (phase !== 'candidate' || active.size || permit.verified.size !== permit.paths.size) throw new Error('Candidate checks are incomplete');
    phase = 'open'; permit = null;
  }
  async function close() {
    phase = 'closed'; permit = null;
    const stopped = new Promise(resolve => server.close(() => resolve()));
    for (const entry of active) { entry.controller.abort(); entry.interrupt?.(); }
    for (const socket of sockets) socket.destroy();
    await stopped;
  }
  return { origin, snapshot, maintain, candidate, open, close };
}
