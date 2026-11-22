import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { PassThrough } from 'node:stream';
import { once } from 'node:events';
import { resolve } from 'node:path';
import test from 'node:test';
import { longReplyFailureLocation, plan, timing, validateDrillDSN, verifyEvents, watchParent } from './long-reply.mjs';

test('long reply failure location exposes owned source lines and numeric counts only', () => {
  const failure = { stack: 'at observe (/private-path/long-reply-browser.mjs:487:12)',
    code: 'ERR_ASSERTION', actual: 0, expected: 1 };
  assert.equal(longReplyFailureLocation(failure), 'long-reply-browser line 487 observed 0 expected 1');
  assert.equal(longReplyFailureLocation({ ...failure, actual: 'private response' }), 'long-reply-browser line 487');
  assert.equal(longReplyFailureLocation({ stack: 'failure at unknown-private-location' }), 'driver');
});

test('quick and full use distinct real lengths and bounded unique segments', () => {
  const full = plan(false),
    quick = plan(true);
  assert.equal(full.durationMs, 1_800_000);
  assert.equal(quick.durationMs, 12_000);
  for (const p of [full, quick]) {
    assert.equal(p.segments.length, 1 + p.durationMs / p.intervalMs);
    assert.equal(new Set(p.segments).size, p.segments.length);
    assert.ok(Buffer.byteLength(p.expectedText) < 128 * 1024);
  }
});

test('database fixture accepts only the unique loopback test schema', () => {
  const url =
    'postgres://local@127.0.0.1:5432/test?sslmode=disable&search_path=studio_test_' + 'a'.repeat(32);
  assert.doesNotThrow(() => validateDrillDSN(url));
  for (const bad of [
    url.replace('127.0.0.1', '192.0.2.1'),
    url.replace('studio_test_', 'other_'),
    url + '&search_path=public',
    url + '&host=192.0.2.1',
    url + '&options=invalid',
    url.split('?')[0],
  ]) {
    assert.throws(() => validateDrillDSN(bad), /isolated database/);
  }
});

test('rejects a burst following a wait and a stream shorter than its target', () => {
  assert.equal(timing([100, 1100, 2101], 2000, 1000).first_to_last_ms, 2001);
  assert.throws(() => timing([0, 1, 7000], 7000, 1000));
  assert.throws(() => timing([0, 900, 1800], 2000, 1000));
});

test('checks each real SSE field across arbitrary UTF-8 boundaries and a single terminal', async () => {
  const segments = ['段一\n', '段二\n'];
  const frame = (name, seq, body) =>
    'id: ' + seq + '\nevent: ' + name + '\ndata: ' + JSON.stringify({ run_id: 'r', seq, ...body }) + '\n\n';
  const first = frame('message.delta', 1, { delta: segments[0] });
  const second = frame('message.delta', 2, { delta: segments[1] });
  const terminal = frame('run.completed', 3, { status: 'COMPLETED' });
  const split = (data) => Array.from(Buffer.from(data), (b) => Buffer.from([b]));
  assert.equal(
    await verifyEvents(split(': connected\n\n' + first + second + terminal), 'r', segments, []),
    3,
  );
  for (const data of [
    first + first + terminal,
    first + terminal,
    second + first + terminal,
    first + second,
    first + second + terminal + terminal,
  ]) {
    await assert.rejects(verifyEvents(split(data), 'r', segments, []));
  }
});

test('parent must grant startup explicitly and retain its control pipe', async () => {
  for (const message of ['start\n', 'wrong\n', 'start\nx', '']) {
    const input = new PassThrough(),
      controller = new AbortController(),
      watch = watchParent(input, controller);
    let granted = false;
    watch.started.then(() => {
      granted = !controller.signal.aborted;
    });
    if (message === 'start\n') {
      input.write(Buffer.from('sta'));
      await Promise.resolve();
      assert.equal(granted, false);
      input.write(Buffer.from('rt\n'));
      await watch.started;
      assert.equal(granted, true);
    } else if (message) input.write(Buffer.from(message));
    const ended = once(input, 'end');
    input.end();
    await ended;
    await watch.started;
    assert.equal(controller.signal.aborted, true);
    if (message !== 'start\n') assert.equal(granted, false);
    watch.close();
  }
});

// Optional real-process proof. Only the child sends PIDs, over its private
// inherited IPC handle, so no identifiers or command lines enter the report.
test(
  'parent EOF before startup and during a real memory stream leaves no owned processes',
  {
    skip: !process.env.LONG_REPLY_CANCEL_TEST_SERVER,
    timeout: 45_000,
  },
  async () => {
    const binaryPath = resolve(process.env.LONG_REPLY_CANCEL_TEST_SERVER);
    const entry = new URL('./long-reply.mjs', import.meta.url).href;
    const script = `
    import {main} from ${JSON.stringify(entry)};
    try {
      await main(['--quick','--server',process.argv[1]],
        phase=>process.send({phase}),(name,pid)=>process.send({name,pid}));
      process.exitCode=2;
    } catch {process.exitCode=1;}
    finally {process.disconnect();}
  `;
    for (const running of [false, true]) {
      const env = { LONG_REPLY_TEST_CONTROL_PIPE: '1' };
      for (const key of [
        'PATH',
        'HOME',
        'USERPROFILE',
        'SystemRoot',
        'WINDIR',
        'TMP',
        'TEMP',
        'TMPDIR',
        'LOCALAPPDATA',
        'WORKFLOW_CHROME',
      ]) {
        if (process.env[key]) env[key] = process.env[key];
      }
      const child = spawn(process.execPath, ['--input-type=module', '--eval', script, binaryPath], {
        env,
        windowsHide: true,
        stdio: ['pipe', 'ignore', 'ignore', 'ipc'],
      });
      const phases = new Set(),
        pids = new Map();
      let cutAt;
      child.on('message', (message) => {
        if (message.name) {
          pids.set(message.name, message.pid);
          return;
        }
        phases.add(message.phase);
        if (message.phase === 'waiting-parent-start') {
          if (running) child.stdin.write('start\n');
          else {
            cutAt = performance.now();
            child.stdin.end();
          }
        }
        if (message.phase === 'stream-active') {
          cutAt = performance.now();
          child.stdin.end();
        }
      });
      const safety = setTimeout(() => {
        child.stdin.destroy();
      }, 20_000);
      let code;
      try {
        [code] = await once(child, 'close');
      } finally {
        clearTimeout(safety);
      }
      assert.equal(code, 1);
      assert.ok(
        cutAt > 0 && performance.now() - cutAt < 10_000,
        'EOF did not stop the owned invocation promptly',
      );
      assert.equal(phases.has('parent-started'), running);
      assert.equal(phases.has('readers-ready'), running);
      assert.equal(phases.has('stream-active'), running);
      assert.deepEqual([...pids.keys()].sort(), running ? ['chromium', 'go'] : []);
      for (const pid of pids.values()) {
        assert.ok(Number.isInteger(pid) && pid > 0);
        assert.throws(() => process.kill(pid, 0), { code: 'ESRCH' });
      }
    }
  },
);
