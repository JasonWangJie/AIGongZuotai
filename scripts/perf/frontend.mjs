import { createHash } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { readFile, readdir } from 'node:fs/promises';
import { createServer } from 'node:net';
import { arch, availableParallelism, cpus, platform, release, totalmem } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { setTimeout as sleep } from 'node:timers/promises';
import { parseArgs } from 'node:util';
import { binary, web, isolatedReferenceDirectory, start } from '../lib/process.mjs';
import { measureBrowser } from './workbench-browser.mjs';

export function distribution(values) {
  if (!values.length || values.some((v) => !Number.isFinite(v) || v < 0 || v > 30000)) {
    throw Error('Invalid performance sample.');
  }
  const sorted = values.toSorted((a, b) => a - b);
  const nearest = (p) => sorted[Math.ceil(sorted.length * p) - 1];
  return { p50: nearest(0.5), p95: nearest(0.95) };
}

async function fileHash(file) {
  const h = createHash('sha256');
  for await (const chunk of createReadStream(file)) {
    h.update(chunk);
  }
  return h.digest('hex');
}

// Canonical identity covers the served production HTML, JS, CSS and all lazy chunks.
export async function artifact(server, dist) {
  const html = await readFile(join(dist, 'index.html'), 'utf8');
  if (
    !/<script\b[^>]*type="module"[^>]*src="\/assets\/[^"]+\.js"/.test(html) ||
    html.includes('/src/main.tsx')
  ) {
    throw Error('Production Vite dist required.');
  }
  const h = createHash('sha256');
  let files = 0;
  async function walk(prefix) {
    const entries = (await readdir(join(dist, prefix), { withFileTypes: true })).sort((a, b) =>
      a.name < b.name ? -1 : a.name > b.name ? 1 : 0
    );
    for (const e of entries) {
      const name = (prefix ? prefix + '/' : '') + e.name;
      if (e.isDirectory()) {
        await walk(name);
        continue;
      }
      if (!e.isFile() || ++files > 2048) {
        throw Error('Unsupported dist layout.');
      }
      h.update(name + '\0' + (await fileHash(join(dist, name))) + '\0');
    }
  }
  await walk('');
  return {
    server_sha256: await fileHash(server),
    dist_sha256: h.digest('hex'),
    dist_files: files,
    index_sha256: createHash('sha256').update(html).digest('hex'),
  };
}

async function availablePort() {
  return new Promise((ok, fail) => {
    const sock = createServer();
    sock.once('error', fail);
    sock.listen(0, '127.0.0.1', () => {
      const { port } = sock.address();
      sock.close((err) => (err ? fail(err) : ok(port)));
    });
  });
}

export function serviceEnv(addr, dist, dirs) {
  const env = {};
  for (const key of [
    'SystemRoot',
    'WINDIR',
    'TEMP',
    'TMP',
    'TMPDIR',
    'PATH',
    'HOME',
    'USERPROFILE',
    'LOCALAPPDATA',
  ]) {
    if (process.env[key]) {
      env[key] = process.env[key];
    }
  }
  return {
    ...env,
    APP_ADDR: addr,
    APP_WEB_DIR: dist,
    AUTH_MODE: 'disabled',
    PROVIDER_MODE: 'demo',
    BILLING_MODE: 'disabled',
    DATABASE_URL: '',
    ASSET_DIR: dirs.assetDirectory,
    REFERENCE_DIR: dirs.directory,
    ASSET_STORAGE_BACKEND: 'local',
  };
}

async function get(origin, path) {
  const response = await fetch(origin + path, {
    redirect: 'error',
    signal: AbortSignal.timeout(5000),
  });
  if (response.status !== 200) {
    throw Error('Owned server verification failed.');
  }
  return response;
}

async function withEmptyService(server, dist, identity, signal, run) {
  const port = await availablePort();
  const addr = '127.0.0.1:' + port;
  const origin = 'http://' + addr;
  const dirs = isolatedReferenceDirectory();
  const child = start(server, [], {
    cwd: dirname(dirs.assetDirectory),
    env: serviceEnv(addr, dist, dirs),
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  // The OS handle belongs to this invocation. Only probe the endpoint after
  // this child announced its own successful listen (a port race fails here).
  let started = false;
  let line = '';
  child.stderr.on('data', (buf) => {
    line = (line + buf.toString()).slice(-2048);
    if (line.includes('local workbench started') && line.includes(addr)) {
      started = true;
    }
  });
  const exited = new Promise((ok) => {
    child.once('exit', ok);
    child.once('error', ok);
  });
  let stopped = false;
  exited.then(() => {
    stopped = true;
  });
  const stop = () => {
    if (!stopped && child.pid) {
      child.kill();
    }
  };
  signal.addEventListener('abort', stop, { once: true });
  try {
    for (let i = 0; i < 100 && !started; i++) {
      signal.throwIfAborted();
      if (stopped) {
        throw Error('Owned demo server failed to bind or start.');
      }
      await sleep(50);
    }
    if (!started) {
      throw Error('Owned demo server startup timed out.');
    }
    const boot = await (await get(origin, '/api/v1/bootstrap')).json();
    if (boot.mode !== 'demo' || boot.storage !== 'memory') {
      throw Error('Demo memory service required.');
    }
    const html = await (await get(origin, '/')).arrayBuffer();
    if (createHash('sha256').update(Buffer.from(html)).digest('hex') !== identity.index_sha256) {
      throw Error('Served artifact mismatch.');
    }
    const history = async () =>
      (await (await get(origin, '/api/v1/conversations?limit=2')).json()).items;
    if ((await history()).length !== 0) {
      throw Error('Expected a pristine conversation list.');
    }
    const result = await run(origin);
    const saved = await history();
    if (
      saved.length !== 1 ||
      saved[0].version !== 1 ||
      saved[0].node_count !== 0 ||
      saved[0].title !== '本机浏览器基准会话'
    ) {
      throw Error('Created conversation did not match the visible result.');
    }
    return result;
  } finally {
    signal.removeEventListener('abort', stop);
    stop();
    await Promise.race([exited, sleep(1000, undefined, { ref: false })]);
    if (!stopped && child.pid) {
      child.kill('SIGKILL');
    }
    await Promise.race([exited, sleep(2000, undefined, { ref: false })]);
    if (!stopped) {
      throw Error('Owned demo service did not exit; retained its isolated directories.');
    }
    dirs.cleanup();
  }
}

export async function main(argv) {
  const { values } = parseArgs({
    args: argv,
    options: {
      server: { type: 'string', default: binary },
      dist: { type: 'string', default: join(web, 'dist') },
      chrome: { type: 'string' },
      samples: { type: 'string', default: '20' },
    },
  });
  const n = Number(values.samples);
  if (!/^[1-9][0-9]?$/.test(values.samples) || n > 40) {
    throw Error('Use 1 to 40 samples per group.');
  }
  const server = resolve(values.server);
  const dist = resolve(values.dist);
  const identity = await artifact(server, dist);
  const cancel = new AbortController();
  const stop = () => cancel.abort(Error('Benchmark interrupted.'));
  process.once('SIGINT', stop);
  process.once('SIGTERM', stop);
  const before = new Date().toISOString();
  const cases = [
    [1440, 900, false],
    [390, 844, true],
    [1440, 900, true],
    [390, 844, false],
  ];
  const groups = cases.map(([width, height, warm]) => ({
    width,
    height,
    cache: warm ? 'primed' : 'fresh',
    samples: [],
  }));
  let browser;
  try {
    for (let round = 0; round < n; round++) {
      for (let index = 0; index < cases.length; index++) {
        const g = (index + round) % cases.length;
        const [width, height, warm] = cases[g];
        const result = await withEmptyService(server, dist, identity, cancel.signal, (origin) =>
          measureBrowser({
            origin,
            width,
            height,
            warm,
            chromePath: values.chrome,
            signal: cancel.signal,
          })
        );
        if (browser && JSON.stringify(browser) !== JSON.stringify(result.browser)) {
          throw Error('Browser changed between samples.');
        }
        browser = result.browser;
        const row = {};
        for (const [k, v] of Object.entries(result.sample)) {
          if (!Number.isFinite(v) || v < 0 || v > 30000) {
            throw Error('Incomplete browser timings.');
          }
          row[k] = Math.round(v * 10) / 10;
        }
        groups[g].samples.push(row);
      }
    }
    const finalArtifact = await artifact(server, dist);
    if (JSON.stringify(finalArtifact) !== JSON.stringify(identity)) {
      throw Error('Artifacts changed while measuring.');
    }
    for (const g of groups) {
      g.percentiles_ms = Object.fromEntries(
        Object.keys(g.samples[0])
          .filter((k) => k.endsWith('_ms'))
          .map((k) => [k, distribution(g.samples.map((row) => row[k]))])
      );
    }
    return {
      format: 'studio.workbench-frontend-benchmark.v1',
      started_at: before,
      finished_at: new Date().toISOString(),
      host: {
        os: platform(),
        release: release(),
        arch: arch(),
        logical_cpus: availableParallelism(),
        cpu: cpus()[0]?.model,
        memory_mib: Math.round(totalmem() / 1048576),
        node: process.version,
      },
      artifact: identity,
      browser,
      conditions: {
        backend: 'loopback memory/demo',
        headless: true,
        gpu_disabled: true,
        network_throttling: 'none',
        viewport_mode: 'CSS pixels; deviceScaleFactor 1; desktop pointer for both widths',
        browser_profile:
          'new Chromium process, profile, isolated context and demo server per sample',
        cache:
          'fresh = first navigation; primed = homepage ready, about:blank, then same-origin navigation; asset counts recorded',
        workload:
          'empty conversation list; trusted click to open chat; create one empty conversation and verify by GET',
        ready_control:
          'visible enabled refresh-history button after bootstrap; renderer animation-frame hit test',
        click_time:
          'trusted click capture to first matching animation-frame hit test; scrolling and mobile menu preparation excluded',
        percentile: 'nearest rank; round-robin interleaved strata',
        samples_per_group: n,
      },
      groups,
    };
  } finally {
    process.off('SIGINT', stop);
    process.off('SIGTERM', stop);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main(process.argv.slice(2))
    .then((report) => console.log(JSON.stringify(report, null, 2)))
    .catch((err) => {
      // Never print captured server output, response bodies, paths or request data.
      const message = String(err.message || '');
      const safe =
        /^(Chromium command timed out: [A-Za-z.]+|Timed out waiting for [\w ]+|[A-Za-z][A-Za-z 0-9;:._=-]{0,130})$/.test(
          message
        );
      console.error(
        'Frontend benchmark failed. ' +
          (safe ? message : 'Check local artifacts, browser and owned process availability.')
      );
      process.exitCode = 1;
    });
}
