import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import test from 'node:test';
import { createStandbyBranch, standbyCovers, verifyCutoverFence } from './pg-sync-standby.mjs';
import { countOriginalReceipts } from './workbench-sync-standby.mjs';
import { assertPromotedReleaseBinding } from '../release/release-process.mjs';

test('promoted binding requires the same drill and original private credentials on a different local port', () => {
  const id = 'pitr_' + 'a'.repeat(32);
  const source =
    'postgresql://drill_' + 'e'.repeat(16) + ':' + '1'.repeat(64) + '@127.0.0.1:32001/studio?sslmode=disable';
  const next = source.replace(':32001', ':32002');
  assert.doesNotThrow(() => assertPromotedReleaseBinding(source, next, id, id));
  for (const candidate of [
    source,
    next.replace('127.0.0.1', '192.0.2.10'),
    next.replace('1'.repeat(64), '2'.repeat(64)),
    next.replace('e'.repeat(16), 'f'.repeat(16)),
    next + '&hostaddr=127.0.0.1',
  ]) {
    assert.throws(() => assertPromotedReleaseBinding(source, candidate, id, id));
  }
  assert.throws(() => assertPromotedReleaseBinding(source, next, id, 'pitr_' + 'd'.repeat(32)));
});

const barrier = 'ADEF01/58';
const source = {
  replicationCount: 1,
  localRecovery: false,
  commit: 'remote_apply',
  names: 'FIRST 1 (studio_drill_standby)',
  fsync: true,
  fullPages: true,
  scram: true,
  state: 'streaming',
  sync: 'sync',
  name: 'studio_drill_standby',
  localClient: true,
  system: '18379274568462329',
  flush: 'ADEF01/60',
  replay: barrier,
};
const standby = {
  fsync: true,
  recovery: true,
  receiver: 'streaming',
  fromSource: true,
  system: source.system,
  flush: barrier,
  replay: barrier,
};

test('sync requires one expected local sender and actual receiver flush and replay at the original barrier', () => {
  assert.equal(standbyCovers(source, standby, barrier), true);
  for (const key of ['flush', 'replay']) {
    assert.equal(standbyCovers({ ...source, [key]: 'ADEF01/57' }, standby, barrier), false);
    assert.equal(standbyCovers(source, { ...standby, [key]: null }, barrier), false);
  }
  for (const change of [
    { replicationCount: 2 },
    { sync: 'potential' },
    { state: 'catchup' },
    { commit: 'local' },
    { names: '*' },
    { system: '1' },
    { name: 'other' },
    { localRecovery: true },
    { localClient: false },
    { fsync: false },
    { fullPages: false },
    { scram: false },
  ]) {
    assert.equal(standbyCovers({ ...source, ...change }, standby, barrier), false);
  }
  for (const change of [
    { recovery: false },
    { system: '1' },
    { fromSource: false },
    { receiver: 'stopped' },
    { fsync: false },
  ]) {
    assert.equal(standbyCovers(source, { ...standby, ...change }, barrier), false);
  }
});

test('a missing old exit or any still held owner lock prevents even the SQL cutover phase', async () => {
  const exit = { graceful: true, exitCode: 0 };
  const locks = { postgresAdvisory: true, referenceOS: true, assetOS: true };
  assert.doesNotThrow(() => verifyCutoverFence(exit, locks));
  let calls = 0;
  const branch = createStandbyBranch({
    step: async (from, to, fn) => {
      assert.equal(from, 'sync_ready');
      assert.equal(to, 'sync_promoted');
      await fn();
    },
    sql: () => {
      calls++;
      throw new Error('unexpected database call');
    },
  });
  for (const proof of [
    { exit: null, locks },
    { exit: { ...exit, exitCode: 1 }, locks },
    { exit, locks: null },
    ...Object.keys(locks).map((field) => ({ exit, locks: { ...locks, [field]: false } })),
  ]) {
    await assert.rejects(
      branch.fenceAndPromote(async () => proof),
      /three free locks/,
    );
  }
  assert.equal(calls, 0);
});

test('receipt reconciliation counts every confirmed operation and distinguishes absence, corruption and authorization', () => {
  const bytes = Buffer.from('one immutable accepted receipt');
  const digest = createHash('sha256').update(bytes).digest('hex');
  const good = { status: 200, body: bytes },
    changed = { status: 200, body: Buffer.from('another') };
  const accepted = Array.from({ length: 3 }, () => ({ digest }));
  assert.deepEqual(countOriginalReceipts(accepted, [good, good, good]), {
    confirmed: 3,
    recovered: 3,
    missing: 0,
    mismatched: 0,
  });
  assert.deepEqual(countOriginalReceipts(accepted, [changed, good, { status: 404 }]), {
    confirmed: 3,
    recovered: 1,
    missing: 1,
    mismatched: 1,
  });
  assert.throws(() => countOriginalReceipts(accepted, [good, good, { status: 403 }]), /could not read/);
  assert.throws(() => countOriginalReceipts(accepted, [good, good]), /incomplete/);
});
