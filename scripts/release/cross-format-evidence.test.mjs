import test from 'node:test';
import assert from 'node:assert/strict';
import os from 'node:os';
import path from 'node:path';
import fs from 'node:fs/promises';
import { compareInventory, freezeReleaseFrontend, inspectTree, verifyPair } from './cross-format-evidence.mjs';

const pins = {
  previous: { format: 36, go: 'old-code', binary: 'old', dist: 'A', migrations: '01' },
  candidate: { format: 37, go: 'new-code', binary: 'new', dist: 'B', migrations: '02' },
};
function pair() {
  return [
    ['previous', [['1', 'one']]],
    [
      'candidate',
      [
        ['1', 'one'],
        ['2', 'two'],
      ],
    ],
  ].map(([k, migrations]) => {
    const p = pins[k];
    return {
      format: p.format,
      binarySha: p.binary,
      goSha: p.go,
      frontend: { sha256: p.dist },
      migrationSha: p.migrations,
      migrations,
    };
  });
}
test('only the two pinned formats with the same original migration bytes qualify', () => {
  const [previous, candidate] = pair();
  assert.doesNotThrow(() => verifyPair(previous, candidate, pins));
  for (const changed of [
    { binarySha: 'other' },
    { goSha: 'other' },
    { frontend: { sha256: 'changed' } },
    { migrationSha: 'bad' },
    { format: 36 },
    {
      migrations: [
        ['1', 'different'],
        ['2', 'two'],
      ],
    },
  ]) {
    assert.throws(() => verifyPair(previous, { ...candidate, ...changed }, pins));
  }
  assert.throws(() => verifyPair(candidate, previous, pins));
});

test('compare actual old, retained and restored records, including untouched money and run counts', () => {
  const snapshot = (versions) => ({ versions, runs: 0, tasks: 0, journals: 0, reservations: 0 });
  const result = compareInventory(snapshot(1), snapshot(3), snapshot(1));
  assert.equal(result.window_writes_confirmed_on_preserved_source, 2);
  assert.equal(result.unreviewed_window_writes, 2);
  for (const [i, changes] of [
    [1, { versions: 2 }],
    [2, { versions: 3 }],
    [1, { runs: 1 }],
    [1, { tasks: 1 }],
    [2, { journals: 1 }],
    [2, { reservations: 1 }],
    [0, { versions: null }],
  ]) {
    const rows = [snapshot(1), snapshot(3), snapshot(1)];
    Object.assign(rows[i], changes);
    assert.throws(() => compareInventory(...rows));
  }
});

test('private companion copy is byte-identical and an existing destination is never overwritten', async (t) => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'cross216-test-'));
  t.after(() => fs.rm(dir, { recursive: true, force: true }));
  const source = path.join(dir, 'old'),
    backup = path.join(dir, 'backup');
  await fs.mkdir(path.join(source, 'refs'), { recursive: true });
  await fs.writeFile(path.join(source, 'refs', 'fixture.png'), Buffer.from([9, 5, 4]));
  await fs.writeFile(path.join(source, 'owner'), 'original');
  const proof = await inspectTree(source, backup);
  assert.deepEqual(await inspectTree(backup), proof);
  await assert.rejects(inspectTree(source, backup));
  await fs.writeFile(path.join(source, 'owner'), 'modified');
  assert.notEqual((await inspectTree(source)).sha256, proof.sha256);
  assert.equal((await inspectTree(backup)).sha256, proof.sha256);
});

test('companion tree refuses a directory link before copying outside files', async (t) => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'cross216-link-'));
  t.after(() => fs.rm(dir, { recursive: true, force: true }));
  const source = path.join(dir, 'src'),
    outside = path.join(dir, 'outside');
  await fs.mkdir(source);
  await fs.mkdir(outside);
  await fs.writeFile(path.join(outside, 'keep'), 'outside');
  await fs.symlink(outside, path.join(source, 'linked'), process.platform === 'win32' ? 'junction' : 'dir');
  await assert.rejects(inspectTree(source, path.join(dir, 'copy')));
  assert.equal(await fs.readFile(path.join(outside, 'keep'), 'utf8'), 'outside');
});

test('an unchanged index cannot hide a changed JS file when staging the serving directory', async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'cross216-web-'));
  t.after(() => fs.rm(root, { recursive: true }));
  const dist = path.join(root, 'dist');
  const asset = 'entry.js';
  const index = '<script src="./entry.js"></script>';
  await fs.mkdir(dist);
  await fs.writeFile(path.join(dist, 'index.html'), index);
  await fs.writeFile(path.join(dist, asset), 'const version = 1;');
  const checked = { dist, frontend: await inspectTree(dist) };
  const previous = await freezeReleaseFrontend(checked, path.join(root, 'private-web-036'));
  await fs.writeFile(path.join(dist, asset), 'const version = 2;');
  assert.equal(await fs.readFile(path.join(dist, 'index.html'), 'utf8'), index);
  assert.equal(await fs.readFile(path.join(previous.dist, asset), 'utf8'), 'const version = 1;');
  assert.deepEqual(await inspectTree(previous.dist), checked.frontend);
  await assert.rejects(
    freezeReleaseFrontend(checked, path.join(root, 'another-web-copy')),
    /Frontend changed before private staging completed/
  );
});
