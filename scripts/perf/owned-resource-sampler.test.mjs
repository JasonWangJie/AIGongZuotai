import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { setTimeout as pause } from 'node:timers/promises';
import test from 'node:test';
import { startOwnedResourceSampler } from './owned-resource-sampler.mjs';

test('sampling accepts a spawned child object only', async () => {
  for (const other of [process.pid, process, { pid: process.pid }, new URL('http://localhost')]) {
    await assert.rejects(startOwnedResourceSampler(other), /directly spawned child/);
  }
});

test('one owned process supplies per-phase CPU, resident memory and native counters', { timeout: 25_000 }, async () => {
  // One bounded CPU burst in a disposable process; no HTTP, database or load client.
  const fixture = spawn(process.execPath, ['-e', `
    process.stdin.setEncoding('utf8');
    process.stdin.on('data', () => {
      const until = Date.now() + 500;
      const before = process.cpuUsage();
      const resident = Buffer.alloc(4 * 1024 * 1024, 1);
      while (Date.now() < until && process.cpuUsage(before).user < 30000) resident[0] ^= 1;
      process.stdout.write('done\\n');
    });
    process.stdout.write('ready\\n');
  `], { windowsHide: true, stdio: ['pipe', 'pipe', 'ignore'] });
  const closed = once(fixture, 'close');
  let monitor;
  try {
    await once(fixture.stdout, 'data');
    monitor = await startOwnedResourceSampler(fixture);
    const { result, resources } = await monitor.phase(async () => {
      const end = once(fixture.stdout, 'data');
      fixture.stdin.write('work\n');
      await end;
      await pause(260);
      return 42;
    });
    assert.equal(result, 42);
    assert.ok(Date.parse(resources.started_at) <= Date.parse(resources.ended_at));
    if (process.platform === 'win32' || process.platform === 'linux') {
      assert.equal(resources.status, 'observed');
      assert.ok(resources.samples >= 3);
      assert.ok(resources.sample_span_ms >= 250);
      assert.ok(resources.cpu_time_delta_ms > 0);
      assert.ok(resources.peak_rss_bytes_observed > 0);
      assert.equal(resources.metric_samples.rss, resources.samples);
      if (process.platform === 'win32') {
        assert.equal(monitor.source, 'windows_process');
        assert.ok(resources.peak_windows_handles_observed > 0);
        assert.equal(resources.peak_linux_fds_observed, null);
      } else {
        assert.equal(monitor.source, 'linux_procfs');
        assert.ok(resources.peak_linux_fds_observed > 0);
        assert.equal(resources.peak_windows_handles_observed, null);
      }
    } else {
      assert.equal(resources.status, 'unsupported_platform');
      assert.equal(resources.samples, 0);
      assert.equal(resources.peak_rss_bytes_observed, null);
    }
    await assert.rejects(monitor.phase(() => { throw Error('fixture operation'); }), /fixture operation/);
    fixture.stdin.end();
    await closed;
    const afterExit = await monitor.phase(() => 'complete');
    assert.equal(afterExit.resources.status, 'process_exited');
    assert.equal(afterExit.resources.samples, 0);
    await monitor.close();
    await assert.rejects(monitor.phase(() => 1), /invalid owned sampler phase/);
  } finally {
    try { await monitor?.close(); }
    finally { if (fixture.exitCode === null && fixture.signalCode === null) fixture.kill(); await closed; }
  }
});
