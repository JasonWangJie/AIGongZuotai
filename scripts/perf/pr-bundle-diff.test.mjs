import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { gzipSync } from 'node:zlib';
import {
  inventory,
  readStartupProfile,
  renderDiff,
} from '../../apps/workbench/scripts/pr-bundle-diff.mjs';

const cli = fileURLToPath(
  new URL('../../apps/workbench/scripts/pr-bundle-diff.mjs', import.meta.url),
);
const observed = {
  schema: 1,
  evidence: 'docs/changes/probe.md',
  observed_conditional_chunks: ['signedIn'],
};
const names = {
  entry: 'index-aaaaaaaa.js',
  dep: 'shared-bbbbbbbb.js',
  extra: 'preload-cccccccc.js',
  auth: 'signedIn-dddddddd.js',
  lazy: 'canvas-eeeeeeee.js',
  child: 'sharedAuth-ffffffff.js',
};

async function fixture(t, { sidecars = true, entryCode, overrideHTML } = {}) {
  const root = await mkdtemp(join(tmpdir(), 'studio-pr-dist-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const dist = join(root, 'dist');
  await mkdir(join(dist, 'assets'), { recursive: true });
  await writeFile(join(root, 'package-lock.json'), '{"lockfileVersion":3}');
  const html =
    overrideHTML ||
    '<script src="/assets/' +
      names.entry +
      '" type="module"></script>' +
      '<link href="/assets/' +
      names.extra +
      '" rel="modulepreload">';
  await writeFile(join(dist, 'index.html'), html);
  const files = {
    [names.entry]:
      entryCode ?? 'import "./' + names.dep + '";const unused=()=>import("./' + names.lazy + '");',
    [names.dep]: 'export * from "./' + names.entry + '";', // cycle
    [names.extra]: 'export {x} from "./' + names.dep + '";',
    [names.auth]: 'import "./' + names.dep + '";import "./' + names.child + '";',
    [names.lazy]: 'const text="import outside the real dependency graph";',
    [names.child]: 'export const x=true;',
  };
  for (const [name, code] of Object.entries(files)) {
    await writeFile(join(dist, 'assets', name), code);
    if (sidecars) await writeFile(join(dist, 'assets', name + '.gz'), gzipSync(code, { level: 6 }));
  }
  return dist;
}

test('entry, modulepreload, cyclic re-exports and authenticated extras are counted once', async (t) => {
  const dist = await fixture(t);
  const inv = await inventory(dist, observed);
  const per = (name) => inv.files.find((f) => f.name === name).gzip;
  assert.equal(inv.static, per(names.entry) + per(names.dep) + per(names.extra));
  assert.equal(inv.observed, per(names.auth) + per(names.child));
  assert.equal(inv.startup, inv.static + inv.observed);
  assert.equal(inv.remaining, per(names.lazy));
  assert.deepEqual(inv.missing, []);
  assert.ok(inv.files.every((f) => f.source === 'sidecar' && f.hash.length === 64));
  assert.ok((await readStartupProfile()).evidence.startsWith('docs/changes/'));
});

test('historic missing gzip is labelled and invalid gzip never silently becomes an estimate', async (t) => {
  const dist = await fixture(t, { sidecars: false });
  const historical = await inventory(dist, observed, { historical: true });
  assert.ok(historical.files.every((f) => f.source === 'estimated'));
  await assert.rejects(inventory(dist, observed));
  const current = await inventory(await fixture(t), observed);
  const md = renderDiff(current, historical, {}, observed);
  assert.match(md, /gzip level 6 离线估算/);
  assert.match(md, /0 B \(0\.00 KiB\)/);
  await writeFile(join(dist, 'assets', names.entry + '.gz'), gzipSync('different'));
  await assert.rejects(inventory(dist, observed, { historical: true }));
});

test('removed logical groups remain visible and a renamed observed chunk is identified', async (t) => {
  const old = await inventory(await fixture(t), observed);
  const dist = await fixture(t);
  await rm(join(dist, 'assets', names.auth));
  await rm(join(dist, 'assets', names.auth + '.gz'));
  const cur = await inventory(dist, observed);
  const md = renderDiff(cur, old, {}, observed);
  assert.deepEqual(cur.missing, ['signedIn']);
  assert.match(md, /候选未匹配的观测逻辑名/);
  assert.match(md, /\| \x60signedIn\x60 .+ \| — \| -\d+ B/);
});

test('invalid external or missing startup references cannot be presented as a passing result', async (t) => {
  const remote = await fixture(t, {
    overrideHTML: '<script type="module" src="https://example.invalid/asset.js"></script>',
  });
  await assert.rejects(inventory(remote, observed));
  const missing = await fixture(t, { entryCode: 'import "./absent-11111111.js";' });
  await assert.rejects(inventory(missing, observed));
  const nested = await fixture(t);
  await mkdir(join(nested, 'plugins'));
  await writeFile(join(nested, 'plugins', 'untracked.js'), 'export {}');
  await assert.rejects(inventory(nested, observed));
});

test('a missing baseline does not hide an over-budget candidate or manufacture a delta', async (t) => {
  const dist = await fixture(t, {
    entryCode: 'export default "' + randomBytes(360 * 1024).toString('base64') + '";',
  });
  const output = spawnSync(
    process.execPath,
    [cli, '--current', dist, '--base-status', 'build-unavailable'],
    {
      encoding: 'utf8',
      env: { ...process.env, GITHUB_ACTIONS: 'false', GITHUB_STEP_SUMMARY: '' },
      timeout: 60000,
    },
  );
  assert.equal(output.status, 1);
  assert.match(output.stdout, /基线不可比/);
  assert.match(output.stdout, /300 KiB 启动预算：超出/);
  assert.match(output.stdout, /\| 去重启动集合 \| — \| \d+ B .+ \| — \|/);
});

test('comparison accepts distinct version locks and preserves a valid candidate if checkout is unavailable', async (t) => {
  const base = await fixture(t, { sidecars: false });
  const current = await fixture(t);
  await writeFile(join(dirname(base), 'package-lock.json'), '{"lockfileVersion":2}');
  for (const [status, expected] of [
    ['ready', /已改变/],
    ['checkout-unavailable', /无法读取指定基线提交/],
  ]) {
    const output = spawnSync(
      process.execPath,
      [cli, '--current', current, '--base', base, '--base-status', status],
      {
        encoding: 'utf8',
        env: { ...process.env, GITHUB_ACTIONS: 'false', GITHUB_STEP_SUMMARY: '' },
        timeout: 60000,
      },
    );
    assert.equal(output.status, 0);
    assert.match(output.stdout, expected);
    assert.match(output.stdout, /已识别集合在预算内/);
  }
});
