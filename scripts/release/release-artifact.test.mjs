import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { cp, link, mkdir, mkdtemp, readFile, realpath, rm, symlink, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { basename, dirname, join, resolve } from 'node:path';
import { setTimeout as pause } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
import { test } from 'node:test';
import { buildReleaseArtifact, prepareReleasePlan, verifyReleaseArtifact } from './release-artifact.mjs';

const checkout = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');

async function port() {
  const probe = createServer();
  probe.listen(0, '127.0.0.1');
  await once(probe, 'listening');
  const value = probe.address().port;
  await new Promise((ok, bad) => probe.close((e) => (e ? bad(e) : ok())));
  return value;
}
async function exerciseServer(release, state) {
  const plan = await prepareReleasePlan(dirname(release.binaryPath), {
    role: release.identity.role,
    version: release.identity.version,
    sha256: release.manifestSha256,
    state,
    port: await port(),
  });
  await mkdir(plan.cwd, { recursive: true });
  const env = { ...plan.environment };
  for (const k of ['SYSTEMROOT', 'SystemRoot', 'WINDIR', 'TMP', 'TEMP', 'TMPDIR', 'PATH'])
    if (process.env[k]) env[k] = process.env[k];
  const server = spawn(plan.binary, [], { cwd: plan.cwd, env, windowsHide: true, stdio: 'ignore' }),
    exit = once(server, 'exit'),
    base = 'http://' + env.APP_ADDR;
  try {
    const end = Date.now() + 12000;
    let identity;
    do {
      if (server.exitCode !== null) throw Error('Isolated workbench exited before readiness.');
      try {
        const response = await fetch(base + '/readyz', { signal: AbortSignal.timeout(800) });
        if (response.ok) {
          identity = await (await fetch(base + '/release-identity.json')).json();
          break;
        }
      } catch {}
      await pause(80);
    } while (Date.now() < end);
    assert.deepEqual(identity, release.identity);
    const bootstrap = await (await fetch(base + '/api/v1/bootstrap')).json();
    assert.equal(bootstrap.storage, 'memory');
    assert.equal(bootstrap.mode, 'demo');
    const index = await (await fetch(base + '/')).text();
    assert.ok(index.includes('name="studio-release-role" content="' + release.identity.role + '"'));
    assert.ok(index.includes('name="studio-release-version" content="' + release.identity.version + '"'));
    assert.equal((await fetch(base + index.match(/src="(\/assets\/[^"]+\.js)"/)[1])).status, 200);
  } finally {
    if (server.exitCode === null) server.kill();
    await exit;
  }
  return plan;
}
test('real local release artifacts and environment plans', { timeout: 360000 }, async (t) => {
  const parent = await realpath(tmpdir()),
    work = await mkdtemp(join(parent, 'studio-artifact-test-'));
  try {
    const use = async (role) => {
      const supplied = process.env['TEST_RELEASE_' + role.toUpperCase() + '_DIR'];
      return supplied
        ? verifyReleaseArtifact(supplied, { role })
        : buildReleaseArtifact({ role, version: 'check-' + role, source: checkout, out: join(work, role) });
    };
    const baseline = await use('baseline'),
      candidate = await use('candidate');
    await t.test(
      'checks distinct binary and frontend identities, real migration history and package shape',
      () => {
        assert.notEqual(baseline.binarySha256, candidate.binarySha256);
        assert.notEqual(baseline.identity.frontend_sha256, candidate.identity.frontend_sha256);
        assert.notEqual(baseline.artifactId, candidate.artifactId);
        for (const r of [baseline, candidate]) {
          assert.match(r.sourceSha256, /^[a-f0-9]{64}$/);
          assert.match(r.schemaSha256, /^[a-f0-9]{64}$/);
          assert.ok(r.schemaVersion >= 1);
          assert.ok(
            r.manifest.files.every(
              (f) => f.path === 'server' || f.path === 'server.exe' || f.path.startsWith('dist/'),
            ),
          );
        }
      },
    );
    await t.test(
      'both real binaries serve the selected workbench and identity from isolated memory environments',
      async () => {
        const state = join(work, 'run');
        await mkdir(state);
        const a = await exerciseServer(baseline, state),
          b = await exerciseServer(candidate, state);
        assert.notEqual(a.cwd, b.cwd);
        assert.notEqual(a.environment.REFERENCE_DIR, b.environment.REFERENCE_DIR);
        assert.deepEqual(Object.keys(a.environment).sort(), [
          'APP_ADDR',
          'APP_WEB_DIR',
          'ASSET_DIR',
          'PROVIDER_MODE',
          'REFERENCE_DIR',
        ]);
      },
    );
    await t.test(
      'rejects an unexpected role, version, independently pinned manifest, port or overlapping state',
      async () => {
        const src = dirname(baseline.binaryPath);
        for (const args of [{ role: 'candidate' }, { version: 'mismatch' }, { sha256: '0'.repeat(64) }])
          await assert.rejects(verifyReleaseArtifact(src, args));
        await assert.rejects(prepareReleasePlan(src, { state: src, port: 7000 }), /separate/);
        await assert.rejects(prepareReleasePlan(src, { state: join(work, 'run'), port: 0 }), /port/);
        await assert.rejects(
          buildReleaseArtifact({
            source: checkout,
            out: src,
            role: 'baseline',
            version: baseline.identity.version,
          }),
          /already exists/,
        );
      },
    );
    const temp = join(work, 'tamper');
    await cp(dirname(baseline.binaryPath), temp, { recursive: true });
    await t.test(
      'detects a changed static file and refuses unlisted files, even when their names look compiled',
      async () => {
        const p = join(temp, 'dist', 'index.html'),
          original = await readFile(p);
        await writeFile(p, Buffer.concat([original, Buffer.from(' ')]));
        await assert.rejects(verifyReleaseArtifact(temp), /differs/);
        await writeFile(p, original);
        for (const [name, err] of [
          ['.env', /Invalid filename/],
          ['extra.js', /differs/],
        ]) {
          const extra = join(temp, 'dist', 'assets', name);
          await writeFile(extra, 'test fixture');
          await assert.rejects(verifyReleaseArtifact(temp), err);
          await rm(extra);
        }
      },
    );
    await t.test(
      'rejects symlinked directories and hard-linked files within or above a selected release',
      async () => {
        const outside = join(work, 'outside');
        await mkdir(outside);
        const jump = join(temp, 'dist', 'assets', 'linked');
        await symlink(outside, jump, process.platform === 'win32' ? 'junction' : 'dir');
        await assert.rejects(verifyReleaseArtifact(temp));
        await rm(jump);
        const alias = join(work, 'alias');
        await symlink(temp, alias, process.platform === 'win32' ? 'junction' : 'dir');
        await assert.rejects(verifyReleaseArtifact(alias), /without links/);
        const p = join(temp, 'dist', 'index.html'),
          outsideLink = join(outside, 'duplicate.html');
        await link(p, outsideLink);
        await assert.rejects(verifyReleaseArtifact(temp), /without links/);
        await rm(outsideLink);
      },
    );
    await t.test(
      'refuses undeclared manifest metadata, traversal records, and identity transplanted from a different binary',
      async () => {
        const mf = join(temp, 'manifest.json'),
          orig = await readFile(mf),
          m = JSON.parse(orig);
        await writeFile(mf, JSON.stringify({ ...m, unexpected: 'unexpected' }));
        await assert.rejects(verifyReleaseArtifact(temp), /fields/);
        await writeFile(
          mf,
          JSON.stringify({
            ...m,
            files: [...m.files, { path: '../outside', size: 1, sha256: 'a'.repeat(64) }],
          }),
        );
        await assert.rejects(verifyReleaseArtifact(temp), /differs/);
        await writeFile(mf, orig);
        await writeFile(
          join(temp, 'dist', 'release-identity.json'),
          await readFile(join(candidate.distPath, 'release-identity.json')),
        );
        await assert.rejects(verifyReleaseArtifact(temp), /differs/);
      },
    );
    await t.test('an identical source, role and version reproduce the complete pinned artifact', async () => {
      const repeated = await buildReleaseArtifact({
        role: 'baseline',
        version: baseline.identity.version,
        source: checkout,
        out: join(work, 'repeat'),
      });
      assert.equal(repeated.manifestSha256, baseline.manifestSha256);
      assert.deepEqual(repeated.identity, baseline.identity);
    });
  } finally {
    assert.equal(dirname(await realpath(work)), parent);
    assert.ok(basename(work).startsWith('studio-artifact-test-'));
    await rm(work, { recursive: true, force: true, maxRetries: 4, retryDelay: 125 });
  }
});
