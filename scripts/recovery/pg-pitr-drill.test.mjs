import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { mkdir, mkdtemp, readFile, rmdir, unlink, writeFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { assertRecoveryMarkers, parseLSN, windowsRestoreCommand } from './pg-pitr-drill.mjs';

const target = 'v18a_' + 'b7'.repeat(12);
const boundary = {
  before: 1,
  after: 0,
  paused: 'paused',
  target,
  expected: target,
  replay: '200000/11',
  point: '200000/11',
  postCommit: '200000/12'
};

test('LSN ordering preserves the complete unsigned 64-bit WAL position', () => {
  assert.equal(parseLSN('0/0'), 0n);
  assert.equal(parseLSN('FFFFFFFF/FFFFFFFF'), (1n << 64n) - 1n);
  assert.equal(parseLSN('200000/10') + 1n, parseLSN('200000/11'));
  assert.ok(parseLSN('1/0') > parseLSN('0/FFFFFFFF'));
});

test('invalid WAL positions are rejected', () => {
  for (const input of ['0', '0/', '0/1 trailing', '0/-1', '0/100000000', '100000000/0', '0/zz']) {
    assert.throws(() => parseLSN(input));
  }
});

test('a paused named point includes pre-target commit and excludes post-target commit', () => {
  assert.doesNotThrow(() => assertRecoveryMarkers(boundary));
});

test('the marker and server target must match even at a valid replay LSN', () => {
  for (const changes of [
    {before: 0}, {after: 1}, {before: '1'}, {paused: 'pause requested'},
    {target: 'another_point'}, {target: 'unsafe', expected: 'unsafe'}
  ]) {
    assert.throws(() => assertRecoveryMarkers({...boundary, ...changes}));
  }
});

test('target evidence rejects WAL on either side of the accepted interval', () => {
  for (const replay of ['200000/10', '200000/12', '200000/13']) {
    assert.throws(() => assertRecoveryMarkers({...boundary, replay}));
  }
});

test('archive command copies binary WAL and returns failure when a requested segment is absent', {
  skip: process.platform !== 'win32'
}, async (t) => {
  const scripts = path.dirname(fileURLToPath(import.meta.url));
  const fixture = await mkdtemp(path.join(scripts, 'wal command test-'));
  const archive = path.join(fixture, 'archive');
  const redo = path.join(fixture, 'pg_wal');
  const name = '0000000100000000000000AA';
  const complete = path.join(archive, name);
  const replay = path.join(redo, 'RECOVERYXLOG');
  t.after(async () => {
    for (const file of [replay, complete]) {
      try { await unlink(file); } catch (e) { if (e.code !== 'ENOENT') throw e; }
    }
    for (const folder of [redo, archive, fixture]) {
      try { await rmdir(folder); } catch (e) { if (e.code !== 'ENOENT') throw e; }
    }
  });
  await mkdir(archive);
  await mkdir(redo);
  const bytes = Buffer.from([0, 26, 10, 13, 255, 67, 0]);
  await writeFile(complete, bytes, { flag: 'wx' });
  const template = windowsRestoreCommand(archive);
  const shell = path.join(process.env.SystemRoot, 'System32', 'cmd.exe');
  const run = (segment) => {
    const command = template.replace('%f', segment).replace('%p', path.relative(fixture, replay));
    return promisify(execFile)(shell, ['/d', '/q', '/c', command], {
      cwd: fixture, windowsHide: true, windowsVerbatimArguments: true, timeout: 5000
    });
  };
  await run(name);
  assert.deepEqual(await readFile(replay), bytes);
  await unlink(replay);
  await assert.rejects(run('00000002.history'), error => error.code === 1);
  await assert.rejects(readFile(replay), { code: 'ENOENT' });
});
