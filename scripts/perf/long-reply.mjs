import { createHash, randomBytes } from 'node:crypto';
import { access, lstat, readdir, realpath, writeFile } from 'node:fs/promises';
import { constants } from 'node:fs';
import { createServer } from 'node:http';
import { createServer as portProbe } from 'node:net';
import { dirname, isAbsolute, join, resolve } from 'node:path';
import { setTimeout as pause } from 'node:timers/promises';
import { pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import { binary, isolatedReferenceDirectory, start, web } from '../lib/process.mjs';
import { artifact, serviceEnv } from './frontend.mjs';

const prompt = '本机持续回复验收';
const model = 'local-long-reply';
const demo = { projectId: 'prj_demo' };
const hash = (bytes) => createHash('sha256').update(bytes).digest('hex');
const check = (condition, label) => {
  if (!condition) throw new Error(label);
};

export function plan(quick) {
  const durationMs = quick ? 12_000 : 30 * 60_000;
  const intervalMs = quick ? 500 : 1000;
  const segments = Array.from({ length: 1 + durationMs / intervalMs }, (_, index) => {
    const n = String(index).padStart(4, '0');
    return '段' + n + '\n.' + n + '\n';
  });
  return { durationMs, intervalMs, segments, expectedText: segments.join('') };
}

async function listen(server) {
  return new Promise((ok, fail) => {
    server.once('error', fail);
    server.listen(0, '127.0.0.1', () => {
      server.off('error', fail);
      ok(server.address().port);
    });
  });
}

function closeServer(server) {
  return new Promise((resolveClose) => {
    server.close(resolveClose);
    server.closeAllConnections?.();
  });
}

async function until(fn, signal, ms, label) {
  const untilMs = performance.now() + ms;
  while (performance.now() < untilMs) {
    signal.throwIfAborted();
    const value = await fn();
    if (value) return value;
    await pause(50, undefined, { signal });
  }
  throw Error(label);
}

function publicURL(origin, path) {
  const url = new URL(path, origin);
  check(url.origin === origin && url.hostname === '127.0.0.1', 'Unexpected service origin');
  return url;
}

async function get(origin, path, signal, access, stream = false) {
  const headers = { 'X-Project-ID': access.projectId };
  if (access.cookie) headers.Cookie = access.cookie;
  const response = await fetch(publicURL(origin, path), {
    headers,
    redirect: 'error',
    signal: stream ? signal : AbortSignal.any([signal, AbortSignal.timeout(5000)]),
  });
  check(response.ok, 'Local service read failed');
  return response;
}

async function schedule(at, signal) {
  while (performance.now() < at)
    await pause(Math.max(1, Math.ceil(at - performance.now())), undefined, { signal });
}

// The parent retains the write end until the owned process group has exited.
// The only accepted message is the literal ASCII startup grant, once.
export function watchParent(input, controller) {
  const expected = Buffer.from('start\n', 'ascii');
  const grant = Promise.withResolvers();
  let offset = 0;
  const failed = () => controller.abort(new Error('Parent control pipe closed or invalid'));
  const release = () => grant.resolve();
  const timer = setTimeout(failed, 15_000);
  function bytes(chunk) {
    if (
      !Buffer.isBuffer(chunk) ||
      chunk.length > expected.length - offset ||
      !chunk.equals(expected.subarray(offset, offset + chunk.length))
    ) {
      failed();
      return;
    }
    offset += chunk.length;
    if (offset === expected.length) {
      clearTimeout(timer);
      release();
    }
  }
  controller.signal.addEventListener('abort', release, { once: true });
  input.on('data', bytes);
  input.once('end', failed);
  input.once('close', failed);
  input.once('error', failed);
  if (input.destroyed || input.readableEnded) failed();
  if (controller.signal.aborted) release();
  input.resume();
  return {
    started: grant.promise,
    close() {
      clearTimeout(timer);
      controller.signal.removeEventListener('abort', release);
      input.off('data', bytes);
      input.off('end', failed);
      input.off('close', failed);
      input.off('error', failed);
      input.pause();
      input.destroy();
    },
  };
}

function gateway(measurement, secret, signal, abort, status) {
  const times = [];
  let readerOpen = false,
    browserOpen = false,
    hasRequest = false;
  let posts = 0,
    done = 0,
    startUTC,
    endUTC;
  const server = createServer((req, res) => {
    res.once('close', () => {
      if (!res.writableFinished && !signal.aborted)
        abort(new Error('Upstream disconnected before completion'));
    });
    async function respond() {
      check(
        req.method === 'POST' &&
          req.url === '/v1/chat/completions' &&
          req.headers.authorization === 'Bearer ' + secret &&
          !req.headers['idempotency-key'],
        'Local adapter request differed',
      );
      check(++posts === 1, 'Unexpected second provider submission');
      const parts = [];
      let size = 0;
      for await (const chunk of req) {
        size += chunk.length;
        check(size <= 16_384, 'Provider request too large');
        parts.push(chunk);
      }
      const body = JSON.parse(Buffer.concat(parts).toString('utf8'));
      check(
        body.stream === true &&
          body.model === model &&
          body.messages?.length === 1 &&
          body.messages[0].role === 'user' &&
          body.messages[0].content === prompt &&
          body.max_completion_tokens === 32768 &&
          !body.stream_options,
        'Actual model request differed',
      );
      res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store' });
      res.flushHeaders();
      hasRequest = true;
      status('provider-requested');
      // Wait for positive confirmation from the native browser EventSource
      // and the independent reader. A silent or unauthorized reader fails here.
      await until(() => readerOpen && browserOpen, signal, 20_000, 'Run SSE readers did not open');
      status('readers-ready');
      const first = performance.now();
      for (let i = 0; i < measurement.segments.length; i++) {
        // Leave a small transport margin on the last tick, so downstream
        // first-to-last wall time can independently satisfy the full target.
        const last = i === measurement.segments.length - 1;
        const at = first + i * measurement.intervalMs + (last ? 250 : 0);
        await schedule(Math.max(at, (times.at(-1) ?? -Infinity) + 0.8 * measurement.intervalMs), signal);
        signal.throwIfAborted();
        check(!res.destroyed, 'Upstream was cancelled before completion');
        if (!i) startUTC = new Date().toISOString();
        times.push(performance.now());
        if (i > 0 && i % 300 === 0) status('upstream-segment-' + i);
        const frame = JSON.stringify({ choices: [{ delta: { content: measurement.segments[i] } }] });
        await new Promise((ok, fail) =>
          res.write('data: ' + frame + '\n\n', (err) => (err ? fail(err) : ok())),
        );
        if (!i) status('stream-active');
      }
      endUTC = new Date().toISOString();
      done++;
      res.end('data: [DONE]\n\n');
    }
    respond().catch(() => {
      abort(new Error('Local provider failed'));
      res.destroy();
    });
  });
  // Node's request timeout covers incoming request bodies. The stream itself
  // uses the Go Run deadline and this owned fixture's overall deadline.
  server.headersTimeout = 10_000;
  server.requestTimeout = 15_000;
  return {
    server,
    requested: () => hasRequest,
    readerReady: () => {
      readerOpen = true;
    },
    browserReady: () => {
      browserOpen = true;
    },
    times,
    progress: () => ({ sent: times.length, total: measurement.segments.length }),
    counts: () => ({
      post_count: posts,
      done_markers: done,
      first_segment_utc: startUTC,
      last_segment_utc: endUTC,
    }),
    close: () => closeServer(server),
  };
}

export function timing(times, required, interval) {
  check(times.length >= 2 && times.every(Number.isFinite), 'Missing stream timing');
  const span = times.at(-1) - times[0];
  const gaps = times.slice(1).map((v, i) => v - times[i]);
  check(
    span >= required && Math.min(...gaps) >= 0 && Math.max(...gaps) <= interval * 5,
    'Stream duration or cadence did not meet target',
  );
  const bins = [];
  for (const at of times) {
    const i = Math.floor((at - times[0]) / 60_000);
    bins[i] = (bins[i] ?? 0) + 1;
  }
  return {
    first_to_last_ms: +span.toFixed(2),
    min_gap_ms: +Math.min(...gaps).toFixed(2),
    max_gap_ms: +Math.max(...gaps).toFixed(2),
    increments_per_elapsed_minute: bins,
  };
}

export async function verifyEvents(body, runID, segments, times) {
  const decoder = new TextDecoder('utf-8', { fatal: true });
  let pending = '',
    received = 0,
    completed = false;
  for await (const chunk of body) {
    pending += decoder.decode(chunk, { stream: true });
    let i;
    while ((i = pending.indexOf('\n\n')) !== -1) {
      const block = pending.slice(0, i);
      pending = pending.slice(i + 2);
      if (block.startsWith(':')) continue;
      const lines = block.split('\n');
      const field = (k) => lines.find((l) => l.startsWith(k + ': '))?.slice(k.length + 2);
      const name = field('event'),
        payload = JSON.parse(field('data'));
      const seq = Number(field('id'));
      check(
        !completed && payload.run_id === runID && payload.seq === received + 1 && seq === payload.seq,
        'Run SSE identity or sequence differed',
      );
      if (name === 'message.delta') {
        check(
          received < segments.length && payload.delta === segments[received],
          'Run SSE fragment differed',
        );
        times.push(performance.now());
        received++;
      } else {
        check(
          name === 'run.completed' && payload.status === 'COMPLETED' && received === segments.length,
          'Run ended without the complete reply',
        );
        completed = true;
      }
    }
    check(pending.length < 4096, 'Unbounded SSE frame');
  }
  check(!decoder.decode() && pending === '' && completed, 'Incomplete platform SSE');
  return received + 1;
}

async function readRun(origin, g, measurement, signal, access) {
  await until(g.requested, signal, 30_000, 'Browser did not create an upstream request');
  const history = await (await get(origin, '/api/v1/runs?limit=2', signal, access)).json();
  const [run] = history.items;
  check(
    history.items.length === 1 && /^run_[a-f0-9]{32}$/.test(run.id) && run.project_id === access.projectId,
    'Unexpected initial Run history',
  );
  const response = await get(origin, '/api/v1/runs/' + run.id + '/events?after=0', signal, access, true);
  check(response.headers.get('content-type')?.startsWith('text/event-stream'), 'Missing Run SSE response');
  g.readerReady();
  const times = [];
  const seq = await verifyEvents(response.body, run.id, measurement.segments, times);
  return { id: run.id, times, seq };
}

async function verifyFinal(origin, measurement, reader, ui, signal, access) {
  check(
    ui.runId === reader.id && /^conv_[a-f0-9]{32}$/.test(ui.conversationId),
    'Browser and listener observed different resources',
  );
  const prefix = '/api/v1/conversations/' + ui.conversationId;
  const [run, page, conversation, list] = await Promise.all(
    [
      '/api/v1/runs/' + reader.id + '/snapshot',
      prefix + '/messages?limit=20',
      prefix,
      '/api/v1/runs?limit=2',
    ].map(async (path) => (await get(origin, path, signal, access)).json()),
  );
  const [user, assistant] = page.items;
  check(
    run.status === 'COMPLETED' &&
      run.output === measurement.expectedText &&
      run.seq === reader.seq &&
      run.provider_mode === 'live' &&
      run.project_id === access.projectId &&
      run.conversation_id === ui.conversationId,
    'Final Run differed',
  );
  check(
    page.items.length === 2 &&
      !page.has_more &&
      user.role === 'user' &&
      user.content === prompt &&
      assistant.role === 'assistant' &&
      assistant.run_id === reader.id &&
      assistant.content === measurement.expectedText &&
      assistant.status === 'COMPLETED' &&
      assistant.seq === reader.seq,
    'Stored conversation reply differed',
  );
  check(
    conversation.node_count === 2 &&
      !conversation.active_run_id &&
      list.items.length === 1 &&
      list.items[0].id === reader.id,
    'Unexpected additional execution',
  );
}

export function validateDrillDSN(raw) {
  let u;
  try {
    u = new URL(raw);
  } catch {
    throw Error('Invalid isolated database');
  }
  check(
    ['postgres:', 'postgresql:'].includes(u.protocol) &&
      ['127.0.0.1', '[::1]', 'localhost'].includes(u.hostname) &&
      !u.hash &&
      /^\/[A-Za-z0-9_-]+$/.test(u.pathname) &&
      u.searchParams.getAll('search_path').length === 1 &&
      /^studio_test_[a-f0-9]{32}$/.test(u.searchParams.get('search_path')) &&
      [...u.searchParams.keys()].every((k) => ['search_path', 'sslmode'].includes(k)) &&
      u.searchParams.getAll('sslmode').length <= 1 &&
      [null, 'disable', 'require'].includes(u.searchParams.get('sslmode')),
    'Invalid isolated database',
  );
}

async function persistentSettings(env) {
  const dsn = env.LONG_REPLY_TEST_DATABASE_URL;
  if (!dsn) return;
  validateDrillDSN(dsn);
  const asset = env.LONG_REPLY_TEST_ASSET_DIR,
    ref = env.LONG_REPLY_TEST_REFERENCE_DIR;
  check(
    asset && ref && isAbsolute(asset) && isAbsolute(ref),
    'Independent private fixture directories required',
  );
  const paths = await Promise.all(
    [asset, ref].map(async (dir) => {
      check(
        (await lstat(dir)).isDirectory() && (await readdir(dir)).length === 0,
        'Fresh private fixture directories required',
      );
      return realpath(dir);
    }),
  );
  check(paths[0] !== paths[1], 'Duplicate fixture directory');
  const password = env.LONG_REPLY_TEST_PASSWORD,
    deliveryKey = env.LONG_REPLY_TEST_DELIVERY_KEY;
  check(
    password &&
      password.length >= 12 &&
      password.length <= 72 &&
      /^[A-Za-z0-9+/]{43}=$/.test(deliveryKey ?? ''),
    'Private test credentials required',
  );
  return { dsn, asset: paths[0], ref: paths[1], deliveryKey, password };
}

async function register(origin, pg, signal) {
  if (!pg) return demo;
  const email = 'long-stream@example.test',
    password = pg.password;
  const response = await fetch(publicURL(origin, '/api/v1/auth/register'), {
    method: 'POST',
    redirect: 'error',
    headers: { 'Content-Type': 'application/json', Origin: origin },
    body: JSON.stringify({ email, password, name: '持续回复验收' }),
    signal: AbortSignal.any([signal, AbortSignal.timeout(8000)]),
  });
  check(response.status === 201, 'Fixture registration failed');
  const value = await response.json(),
    cookie = response.headers
      .getSetCookie()
      .find((c) => c.startsWith('studio_session='))
      ?.split(';')[0];
  const projectId = value.project?.id;
  check(cookie && /^prj_[a-f0-9]{32}$/.test(projectId), 'Missing fixture identity');
  return { projectId, cookie, identity: { email, password, projectId } };
}

async function ownedService(binaryPath, dist, providerOrigin, secret, controller, signal, pg, onChild, run) {
  const listener = portProbe(),
    port = await listen(listener);
  await closeServer(listener);
  const addr = '127.0.0.1:' + port,
    origin = 'http://' + addr;
  const dirs = isolatedReferenceDirectory();
  const catalog = join(dirname(dirs.directory), 'catalog.json');
  let child,
    exit,
    ended = false,
    started = false,
    stopping = false;
  try {
    const modelConfig = {
      id: 'studio-chat',
      name: '本机持续回复',
      kind: 'chat',
      description: '环回测试模型',
      enabled: true,
      upstream_model: model,
      chat_output_limit_field: 'max_completion_tokens',
      capabilities: {
        max_count: 1,
        max_prompt_bytes: 16000,
        parameter_presets: [],
        context_window_tokens: 65536,
        max_output_tokens: 32768,
      },
    };
    await writeFile(catalog, JSON.stringify({ schema_version: 1, models: [modelConfig] }), {
      flag: 'wx',
      mode: 0o600,
    });
    const env = {
      ...serviceEnv(addr, dist, dirs),
      PROVIDER_MODE: 'live',
      MODEL_CATALOG_FILE: catalog,
      NEWAPI_BASE_URL: providerOrigin + '/v1',
      NEWAPI_API_KEY: secret,
      CHAT_RUN_TIMEOUT_SECONDS: '2100',
    };
    if (pg)
      Object.assign(env, {
        DATABASE_URL: pg.dsn,
        AUTH_MODE: 'required',
        ASSET_DIR: pg.asset,
        REFERENCE_DIR: pg.ref,
        MEDIA_DELIVERY_KEY: pg.deliveryKey,
      });
    signal.throwIfAborted();
    child = start(binaryPath, [], { cwd: dirname(dirs.directory), env, stdio: ['ignore', 'ignore', 'pipe'] });
    child.once('spawn', () => onChild?.('go', child.pid));
    let log = '';
    child.stderr.on('data', (b) => {
      log = (log + b.toString()).slice(-2048);
      if (log.includes('local workbench started') && log.includes('address=' + addr + ' ')) started = true;
    });
    exit = new Promise((resolveExit) => {
      child.once('close', () => {
        ended = true;
        if (!stopping) controller.abort(new Error('Owned Go service stopped'));
        resolveExit();
      });
      child.once('error', () => controller.abort(new Error('Owned Go service error')));
    });
    await until(() => started, signal, 15_000, 'Owned service did not bind');
    const access = await register(origin, pg, signal);
    const boot = await (await get(origin, '/api/v1/bootstrap', signal, access)).json();
    check(
      boot.mode === 'live' &&
        boot.storage === (pg ? 'postgres' : 'memory') &&
        boot.project.id === access.projectId,
      'Wrong service mode',
    );
    const identity = await artifact(binaryPath, dist);
    check(
      hash(Buffer.from(await (await get(origin, '/', signal, access)).arrayBuffer())) ===
        identity.index_sha256,
      'Served HTML differs',
    );
    return await run(origin, { ...identity, dist_index_sha256: identity.index_sha256 }, access);
  } finally {
    stopping = true;
    if (child && !ended && child.pid) {
      child.kill();
      await Promise.race([exit, pause(3000, undefined, { ref: false })]);
      if (!ended) child.kill('SIGKILL');
    }
    if (exit) await exit;
    dirs.cleanup();
  }
}

export async function main(argv, status = () => {}, onChild) {
  const { values } = parseArgs({
    args: argv,
    options: {
      quick: { type: 'boolean' },
      server: { type: 'string' },
      dist: { type: 'string' },
      out: { type: 'string' },
      chrome: { type: 'string' },
    },
    strict: true,
    allowPositionals: false,
  });
  const { observeLongReply } = await import('../../apps/workbench/scripts/long-reply-browser.mjs');
  const measurement = plan(Boolean(values.quick));
  const pg = await persistentSettings(process.env);
  check(values.quick || pg, 'Full measurement requires the isolated Go database fixture');
  if (values.out) {
    const path = resolve(values.out);
    await access(dirname(path), constants.W_OK);
    try {
      await lstat(path);
      throw Error('Report destination already exists');
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
    }
  }
  const controller = new AbortController(),
    stop = () => controller.abort(new Error('Benchmark interrupted'));
  const signal = AbortSignal.any([controller.signal, AbortSignal.timeout(measurement.durationMs + 100_000)]);
  const controlMode = process.env.LONG_REPLY_TEST_CONTROL_PIPE;
  check(controlMode === undefined || controlMode === '1', 'Invalid parent control setting');
  const parent = controlMode === '1' ? watchParent(process.stdin, controller) : undefined;
  const secret = randomBytes(32).toString('hex');
  const g = gateway(measurement, secret, signal, (reason) => controller.abort(reason), status);
  process.once('SIGINT', stop);
  process.once('SIGTERM', stop);
  try {
    if (parent) {
      status('waiting-parent-start');
      await parent.started;
      signal.throwIfAborted();
      status('parent-started');
    }
    const providerOrigin = 'http://127.0.0.1:' + (await listen(g.server));
    const report = await ownedService(
      resolve(values.server || binary),
      resolve(values.dist || join(web, 'dist')),
      providerOrigin,
      secret,
      controller,
      signal,
      pg,
      onChild,
      async (origin, artifacts, access) => {
        status('service-ready');
        const read = readRun(origin, g, measurement, signal, access);
        const view = observeLongReply({
          origin,
          signal,
          expectedText: measurement.expectedText,
          durationMs: measurement.durationMs,
          progress: g.progress,
          segments: measurement.segments,
          browserReady: g.browserReady,
          onBrowserStart: (pid) => onChild?.('chromium', pid),
          chromePath: values.chrome || process.env.WORKFLOW_CHROME,
          identity: access.identity,
          projectId: access.projectId,
        });
        let reader, ui;
        try {
          [reader, ui] = await Promise.all([read, view]);
        } catch (error) {
          controller.abort(error);
          await Promise.allSettled([read, view]);
          throw error;
        }
        await verifyFinal(origin, measurement, reader, ui, signal, access);
        const gatewayCounts = g.counts();
        check(
          gatewayCounts.post_count === 1 && gatewayCounts.done_markers === 1,
          'Provider did not complete exactly once',
        );
        return {
          format: 'workbench.long-reply.v1',
          profile: values.quick ? 'quick-12-seconds' : 'wall-clock-30-minutes',
          artifacts,
          provider_mode: 'live-loopback-fixture',
          storage: pg ? 'postgres' : 'memory',
          run_timeout_seconds: 2100,
          target_wall_ms: measurement.durationMs,
          target_interval_ms: measurement.intervalMs,
          output_bytes: Buffer.byteLength(measurement.expectedText),
          output_sha256: hash(measurement.expectedText),
          segments: measurement.segments.length,
          final_sequence: reader.seq,
          upstream: { ...gatewayCounts, ...timing(g.times, measurement.durationMs, measurement.intervalMs) },
          platform_sse: timing(reader.times, measurement.durationMs, measurement.intervalMs),
          complete_run_snapshot: true,
          complete_conversation_message: true,
          browser: ui.measurements,
        };
      },
    );
    signal.throwIfAborted();
    if (values.out)
      await writeFile(resolve(values.out), JSON.stringify(report, null, 2) + '\n', {
        flag: 'wx',
        mode: 0o600,
      });
    return report;
  } finally {
    controller.abort();
    await g.close();
    parent?.close();
    process.off('SIGINT', stop);
    process.off('SIGTERM', stop);
  }
}

export function longReplyFailureLocation(error) {
  const match = String(error?.stack || '').match(/(?:^|[/\\])(long-reply-browser|long-reply)\.mjs:(\d+):\d+/m);
  const location = match ? match[1] + ' line ' + match[2] : 'driver';
  if (error?.code === 'ERR_ASSERTION' && Number.isSafeInteger(error.actual) && Number.isSafeInteger(error.expected)) {
    return location + ' observed ' + error.actual + ' expected ' + error.expected;
  }
  return location;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main(process.argv.slice(2), (phase) => console.error('long-reply ' + phase))
    .then((r) =>
      console.log(
        'long-reply completed profile=' + r.profile + ' segments=' + r.segments + ' bytes=' + r.output_bytes,
      ),
    )
    .catch((error) => {
      // Never echo an exception message, raw stack, file path or response body.
      console.error('Long reply benchmark failed. ' + longReplyFailureLocation(error));
      process.exitCode = 1;
    });
}
