// Run against a built workbench. Only this loopback fixture is reachable.
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { until, withLocalBrowser } from './local-browser.mjs';

const dist = process.argv[2] ? resolve(process.argv[2]) : fileURLToPath(new URL('../dist/', import.meta.url));
const projects = ['first', 'second', 'other'].map(id => ({ id, name: id }));
const users = ['owner', 'visitor'].map(id => ({ id, name: id, email: id + '@example.invalid' }));
const signedOut = { auth_enabled: true, user: null };
let user = null, nextBootstrap = null, nextSession = null, unexpectedWrites = 0;

function session() {
  if (!user) return signedOut;
  const allowed = user === users[0] ? projects.slice(0, 2) : projects.slice(2);
  return { auth_enabled: true, user, project: allowed[0], projects: allowed, workspace: { id: user.id, name: user.name } };
}

function gate() {
  return { received: Promise.withResolvers(), reply: Promise.withResolvers() };
}

function json(res, value, status = 200) {
  res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
  res.end(JSON.stringify(value));
}

async function serve(req, res) {
  const path = new URL(req.url, 'http://localhost').pathname;
  if (path === '/api/v1/auth/login' && req.method === 'POST') {
    let body = '';
    for await (const chunk of req) body += chunk;
    user = users.find(candidate => candidate.email === JSON.parse(body).email);
    return json(res, session());
  }
  if (req.method !== 'GET') { unexpectedWrites++; return json(res, {}, 405); }
  if (path === '/api/v1/auth/session') {
    const held = nextSession;
    if (held) {
      nextSession = null;
      held.received.resolve();
      return json(res, await held.reply.promise);
    }
    return json(res, session());
  }
  if (path === '/api/v1/bootstrap') {
    const identity = session();
    const project = identity.projects?.find(row => row.id === (req.headers['x-project-id'] || identity.project.id));
    if (!project) return json(res, {}, 403);
    const held = nextBootstrap;
    if (held) {
      nextBootstrap = null;
      held.received.resolve();
      await held.reply.promise;
    }
    return json(res, { ...identity, project, storage: 'postgres', mode: 'demo', models: [] });
  }
  if (path.endsWith('/runs') || path.endsWith('/tasks')) return json(res, { items: [] });
  if (path.startsWith('/api/')) return json(res, {}, 404);
  if (!/^\/(?:assets\/[\w.-]+)?$/.test(path)) return json(res, {}, 404);
  const file = path === '/' ? 'index.html' : path.slice(1);
  const type = file.endsWith('.html') ? 'text/html' : file.endsWith('.css') ? 'text/css' : 'text/javascript';
  const bytes = await readFile(join(dist, file));
  res.writeHead(200, { 'Content-Type': type });
  res.end(bytes);
}

async function login(page, identity) {
  await page.fill('#auth-email', identity.email);
  await page.fill('#auth-password', 'loopback-fixture-only');
  await page.button('进入创作空间', true);
}

// Let the very first input fall between the DOM commit that enables the field
// and a later passive effect. Use the same native setter/input event as fill().
async function fillOnEnable(page, project, draft, held) {
  await held.received.promise;
  await until(() => page.evaluate(() => document.querySelector('#creation-prompt')?.disabled), 'disabled composer while loading');
  await page.evaluate((project, draft) => {
    const field = document.querySelector('#creation-prompt');
    window.composerSample = null;
    const observer = new MutationObserver(() => {
      if (field.disabled) return;
      observer.disconnect();
      const correctProject = document.querySelector('#project-select').value === project;
      const initiallyEmpty = field.value === '';
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(field, draft);
      field.dispatchEvent(new Event('input', { bubbles: true }));
      window.composerSample = { correctProject, initiallyEmpty, immediateBytes: new TextEncoder().encode(field.value).length };
    });
    observer.observe(field, { attributes: true, attributeFilter: ['disabled'] });
  }, project.id, draft);
  held.reply.resolve();
  await until(() => page.evaluate(() => window.composerSample && !document.querySelector('#project-select').disabled), 'project loaded after first input');
  const sample = await page.evaluate(() => window.composerSample);
  assert.deepEqual(sample, { correctProject: true, initiallyEmpty: true, immediateBytes: Buffer.byteLength(draft) });
  await checkDraft(page, project, draft);
}

async function checkDraft(page, project, draft) {
  const snapshot = await page.evaluate((id, expected) => {
    const field = document.querySelector('#creation-prompt');
    const counter = document.querySelector('.composer-label span:last-child');
    return {
      project: document.querySelector('#project-select')?.value === id,
      enabled: field?.disabled === false,
      matches: field?.value === expected,
      bytes: counter ? Number(counter.textContent.split('/')[0].trim()) : null,
    };
  }, project.id, draft);
  assert.deepEqual(snapshot, { project: true, enabled: true, matches: true, bytes: Buffer.byteLength(draft) });
}

async function recheck(page) {
  await page.evaluate(() => window.dispatchEvent(new Event('focus')));
}

async function expire(page) {
  user = null;
  await recheck(page);
  await until(() => page.evaluate(() => !!document.querySelector('#auth-email') && !document.querySelector('#creation-prompt')), 'expired identity hides draft');
}

async function ready(page, project) {
  await until(() => page.evaluate(id => {
    const picker = document.querySelector('#project-select');
    return picker?.value === id && !picker.disabled;
  }, project.id), 'requested project finished loading');
}

const server = createServer((req, res) => { serve(req, res).catch(() => json(res, {}, 500)); });
await new Promise(done => server.listen(0, '127.0.0.1', done));
const deadline = AbortSignal.timeout(60_000);
const activeGates = [];
const hold = () => { const value = gate(); activeGates.push(value); return value; };
// A gate must also settle if navigation/Chromium fails before its request.
deadline.addEventListener('abort', () => {
  for (const g of activeGates) { g.received.resolve(); g.reply.resolve(signedOut); }
}, { once: true });
try {
  const origin = 'http://127.0.0.1:' + server.address().port;
  await withLocalBrowser({ url: origin, onlyOrigin: true, signal: deadline }, async open => {
    const page = await open();
    const first = nextBootstrap = hold();
    await login(page, users[0]);
    await page.button('图像工作室');
    await fillOnEnable(page, projects[0], '测'.repeat(11), first);

    // An old identity refresh for the first project finishes after the same
    // owner edits the second one. Observe when the old answer was actually read.
    await page.evaluate(() => {
      const fetchOriginal = window.fetch.bind(window);
      window.fetch = async (...args) => {
        const response = await fetchOriginal(...args);
        if (new URL(response.url).pathname === '/api/v1/auth/session') {
          const originalText = response.text.bind(response);
          response.text = async () => {
            const text = await originalText();
            if (!JSON.parse(text).user) requestAnimationFrame(() => { window.oldComposerIdentityRead = true; });
            return text;
          };
        }
        return response;
      };
    });
    const delayed = nextSession = hold();
    await recheck(page);
    await delayed.received.promise;
    const second = nextBootstrap = hold();
    await page.fill('#project-select', projects[1].id);
    const replacement = '新的项目输入';
    await fillOnEnable(page, projects[1], replacement, second);
    delayed.reply.resolve(signedOut);
    await until(() => page.evaluate(() => window.oldComposerIdentityRead), 'old project identity response consumed');
    await checkDraft(page, projects[1], replacement);

    await expire(page);
    await login(page, users[0]);
    await ready(page, projects[1]);
    await checkDraft(page, projects[1], replacement);
    await expire(page);
    await login(page, users[1]);
    await ready(page, projects[2]);
    await checkDraft(page, projects[2], '');
    assert.equal(unexpectedWrites, 0);
    console.log('PASS: first input at login and project switch, late previous read, same-owner restoration, other-owner isolation, zero creative writes.');
    await page.close();
  });
} finally {
  for (const g of activeGates) g.reply.resolve(signedOut);
  server.closeAllConnections();
  await new Promise(done => server.close(done));
}
