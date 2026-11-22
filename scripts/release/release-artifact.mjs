import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { constants, existsSync } from 'node:fs';
import {
  chmod,
  copyFile,
  lstat,
  mkdir,
  mkdtemp,
  open,
  readFile,
  readdir,
  realpath,
  rm,
  writeFile,
} from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';

const format = 1;
const roles = ['baseline', 'candidate'];
const versionPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
const hashPattern = /^[a-f0-9]{64}$/;
const maxFiles = 2048,
  maxBytes = 300 * 1024 * 1024;
const binName = process.platform === 'win32' ? 'server.exe' : 'server';
const json = (value) => JSON.stringify(value) + '\n';
const digest = (value) => createHash('sha256').update(value).digest('hex');
const compare = (a, b) => (a < b ? -1 : a > b ? 1 : 0);

function check(condition, message) {
  if (!condition) throw Error(message);
}
function validLabel(role, version) {
  check(
    roles.includes(role) && versionPattern.test(version || ''),
    'Expected baseline or candidate and a safe explicit version.',
  );
}
function child(root, path) {
  const rel = relative(root, path);
  return !!rel && !isAbsolute(rel) && rel !== '..' && !rel.startsWith('..' + sep);
}
function safePath(name) {
  return (
    typeof name === 'string' &&
    name.length <= 340 &&
    name
      .split('/')
      .every(
        (x) =>
          /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,120}$/.test(x) &&
          !/[. ]$/.test(x) &&
          !/^(?:con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i.test(x),
      )
  );
}
function assetAllowed(path) {
  return (
    path === 'index.html' ||
    /^assets\/[A-Za-z0-9_.-]+-[A-Za-z0-9_-]{8}\.js\.gz$/.test(path) ||
    /^assets\/(?:[A-Za-z0-9_.-]+\/)*[A-Za-z0-9_.-]+\.(?:css|js|png|jpg|jpeg|webp|avif|svg|gif|ico|woff2?)$/.test(
      path,
    )
  );
}
async function regularFile(path, read) {
  const before = await plain(path, 'file');
  check(relative(resolve(path), await realpath(path)) === '', 'File resolves through a link.');
  const handle = await open(path, constants.O_RDONLY | (constants.O_NOFOLLOW || 0));
  try {
    const opened = await handle.stat();
    check(
      opened.isFile() && opened.nlink === 1 && opened.dev === before.dev && opened.ino === before.ino,
      'File changed before reading.',
    );
    const result = await read(handle);
    const after = await plain(path, 'file');
    check(
      after.dev === opened.dev &&
        after.ino === opened.ino &&
        after.size === opened.size &&
        after.mtimeMs === opened.mtimeMs,
      'File changed while reading.',
    );
    return result;
  } finally {
    await handle.close();
  }
}
async function hashFile(path) {
  return regularFile(path, async (handle) => {
    const h = createHash('sha256'),
      buffer = Buffer.allocUnsafe(128 * 1024);
    for (;;) {
      const { bytesRead } = await handle.read(buffer, 0, buffer.length);
      if (!bytesRead) break;
      h.update(buffer.subarray(0, bytesRead));
    }
    return h.digest('hex');
  });
}
async function plain(path, type) {
  const s = await lstat(path);
  check(
    !s.isSymbolicLink() && (type === 'dir' ? s.isDirectory() : s.isFile() && s.nlink === 1),
    'Expected a regular ' + type + ' without links: ' + basename(path),
  );
  return s;
}
async function cleanDirectory(path) {
  path = resolve(path);
  let cursor = path;
  const chain = [];
  for (;;) {
    chain.push(cursor);
    const up = dirname(cursor);
    if (up === cursor) break;
    cursor = up;
  }
  for (const point of chain.reverse()) await plain(point, 'dir');
  return realpath(path);
}
async function walk(
  root,
  allowed,
  { dirsAllowed = () => true, skip = () => false, rejectEmpty = true } = {},
) {
  let size = 0;
  const files = [];
  await plain(root, 'dir');
  async function visit(rel) {
    const current = rel ? join(root, ...rel.split('/')) : root;
    const entries = await readdir(current, { withFileTypes: true });
    check(!rejectEmpty || !rel || entries.length > 0, 'Unexpected empty directory: ' + rel);
    for (const entry of entries.sort((a, b) => compare(a.name, b.name))) {
      const name = rel ? rel + '/' + entry.name : entry.name;
      if (skip(name, entry)) continue;
      check(safePath(name), 'Invalid filename in artifact or build input.');
      const path = join(root, ...name.split('/'));
      if (entry.isDirectory()) {
        await plain(path, 'dir');
        check(dirsAllowed(name), 'Unexpected directory: ' + name);
        await visit(name);
      } else {
        check(allowed(name), 'Unexpected file: ' + name);
        const s = await plain(path, 'file');
        size += s.size;
        check(
          files.length < maxFiles && s.size > 0 && size <= maxBytes,
          'Build input or artifact exceeds the file limits.',
        );
        files.push({ path: name, size: s.size, sha256: await hashFile(path) });
      }
    }
  }
  await visit('');
  return files.sort((a, b) => compare(a.path, b.path));
}
async function copyChecked(src, dst, entries) {
  for (const row of entries) {
    const out = join(dst, ...row.path.split('/'));
    await mkdir(dirname(out), { recursive: true });
    await copyFile(join(src, ...row.path.split('/')), out, constants.COPYFILE_EXCL);
    check((await hashFile(out)) === row.sha256, 'Input changed while preparing an artifact.');
  }
}
const frontendSources = new Set([
  'index.html',
  'package.json',
  'package-lock.json',
  'tsconfig.json',
  'vite.config.ts',
]);
async function snapshot(source, target) {
  await cleanDirectory(join(source, 'apps', 'workbench'));
  const inputs = [];
  async function add(path) {
    const s = await plain(join(source, path), 'file');
    check(s.size > 0 && s.size <= 32 * 1024 * 1024, 'Unexpected source file size.');
    inputs.push({ path, size: s.size, sha256: await hashFile(join(source, path)) });
  }
  for (const path of ['go.mod', 'go.sum', ...[...frontendSources].map((n) => 'apps/workbench/' + n)])
    await add(path);
  const web = 'apps/workbench/src';
  const webfiles = await walk(
    await cleanDirectory(join(source, web)),
    (p) => /\.(tsx?|css|svg|png|jpg|jpeg|webp|avif|woff2?|json)$/.test(p),
    { rejectEmpty: false },
  );
  inputs.push(...webfiles.map((r) => ({ ...r, path: web + '/' + r.path })));
  const goRoots = ['cmd/server', 'internal/studio', 'db'];
  // Since format 030 the workbench server imports its local video executor.
  // An older source checkout can still supply its original smaller tree.
  if (existsSync(join(source, 'internal', 'videoexport'))) goRoots.push('internal/videoexport');
  for (const dir of goRoots) {
    const files = await walk(
      await cleanDirectory(join(source, dir)),
      (path) =>
        path.endsWith('.go') ||
        (dir === 'db' && /^migrations\/[0-9]+_[A-Za-z0-9_-]+\.sql$/.test(path)) ||
        (dir === 'internal/studio' && /^testdata\/moderation\/[A-Za-z0-9_-]+\.(png|mp4)$/.test(path)),
      {
        skip: (p, e) =>
          e.isDirectory()
            ? p.startsWith('testdata/') && p !== 'testdata/moderation'
            : p.endsWith('_test.go') ||
              !(
                p.endsWith('.go') ||
                (dir === 'db' && p.endsWith('.sql')) ||
                (dir === 'internal/studio' && p.startsWith('testdata/moderation/') && /\.(png|mp4)$/.test(p))
              ),
        rejectEmpty: false,
      },
    );
    inputs.push(...files.map((r) => ({ ...r, path: dir + '/' + r.path })));
  }
  check(
    inputs.some((r) => r.path === 'cmd/server/main.go') &&
      inputs.some((r) => r.path === 'apps/workbench/src/App.tsx') &&
      inputs.some((r) => r.path === 'db/migrations.go'),
    'Unexpected checkout: workbench, migrations and cmd/server source are required.',
  );
  inputs.sort((a, b) => compare(a.path, b.path));
  await copyChecked(source, target, inputs);
  return digest(json(inputs));
}
function buildEnv() {
  const env = {};
  for (const k of [
    'PATH',
    'Path',
    'PATHEXT',
    'SystemRoot',
    'WINDIR',
    'SystemDrive',
    'ComSpec',
    'USERPROFILE',
    'HOME',
    'LOCALAPPDATA',
    'APPDATA',
    'TMP',
    'TEMP',
    'TMPDIR',
    'GOMODCACHE',
    'GOCACHE',
    'GOPATH',
  ])
    if (process.env[k]) env[k] = process.env[k];
  return {
    ...env,
    GOWORK: 'off',
    GOENV: 'off',
    GOPROXY: 'off',
    GOSUMDB: 'off',
    GOTOOLCHAIN: 'local',
    CGO_ENABLED: '0',
    NODE_ENV: 'production',
    npm_config_offline: 'true',
    npm_config_audit: 'false',
    npm_config_fund: 'false',
    npm_config_update_notifier: 'false',
  };
}
function run(program, args, cwd, env, step) {
  return new Promise((ok, bad) => {
    const p = spawn(program, args, { cwd, env, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe'] });
    let output = '';
    const collect = (b) => {
      output = (output + b.toString()).slice(-40000);
    };
    p.stdout.on('data', collect);
    p.stderr.on('data', collect);
    p.on('error', (e) => bad(Error(step + ' executable unavailable (' + e.code + ').')));
    p.on('close', (code) => {
      if (code === 0) ok(output.trim());
      else
        bad(
          Error(
            step +
              ' failed (' +
              code +
              ').' +
              (/ENOTCACHED|cache mode is 'only-if-cached'|module lookup disabled|toolchain not available/i.test(
                output,
              )
                ? ' Populate the locked local dependency/toolchain cache before the offline build.'
                : ''),
          ),
        );
    });
  });
}
function npmCLI() {
  const paths = [
    join(dirname(process.execPath), 'node_modules', 'npm', 'bin', 'npm-cli.js'),
    resolve(dirname(process.execPath), '..', 'lib', 'node_modules', 'npm', 'bin', 'npm-cli.js'),
    '/usr/share/nodejs/npm/bin/npm-cli.js',
  ];
  const cli = paths.find(existsSync);
  check(cli, 'Cannot find the installed npm CLI beside Node.');
  return cli;
}
async function schemaFingerprint(tree) {
  const dir = join(tree, 'db', 'migrations'),
    names = (await readdir(dir)).sort(compare),
    h = createHash('sha256');
  check(names.length > 0 && names.length <= 999, 'Missing migration history.');
  for (const [i, n] of names.entries()) {
    check(
      new RegExp('^' + String(i + 1).padStart(3, '0') + '_[A-Za-z0-9_-]+\\.sql$').test(n),
      'Migration history must be continuous from 001.',
    );
    h.update(n + '\0');
    h.update(await readFile(join(dir, n)));
    h.update('\0');
  }
  return { schema_version: names.length, schema_sha256: h.digest('hex') };
}
async function goForSource(tree, env) {
  const mod = await readFile(join(tree, 'go.mod'), 'utf8'),
    wanted = mod.match(/^go ([0-9]+\.[0-9]+\.[0-9]+)\s*$/m)?.[1];
  check(wanted, 'A pinned Go toolchain is required in go.mod.');
  const local = JSON.parse(
    await run(
      'go',
      ['env', '-json', 'GOVERSION', 'GOMODCACHE', 'GOOS', 'GOARCH'],
      tmpdir(),
      env,
      'Find local Go toolchain',
    ),
  );
  if (local.GOVERSION === 'go' + wanted) return 'go';
  const cached = join(
    local.GOMODCACHE,
    'golang.org',
    'toolchain@v0.0.1-go' + wanted + '.' + local.GOOS + '-' + local.GOARCH,
    'bin',
    process.platform === 'win32' ? 'go.exe' : 'go',
  );
  check(existsSync(cached), 'Install or cache Go ' + wanted + ' before the offline release build.');
  return cached;
}
function buildID(role, version, sourceSHA, schema) {
  return (
    'studio-' +
    role +
    '-' +
    version +
    '-' +
    sourceSHA +
    '-' +
    schema.schema_version +
    '-' +
    schema.schema_sha256
  );
}
async function expectGo(binary, role, version, sourceSHA, schema) {
  const env = buildEnv(),
    cwd = tmpdir();
  const output = await run('go', ['version', '-m', binary], cwd, env, 'Inspect Go metadata');
  check(
    /(?:^|\n)\s*path\s+aigongzuotai\/cmd\/server(?:\s|$)/.test(output),
    'Executable is not the workbench Go server.',
  );
  const id = await run('go', ['tool', 'buildid', binary], cwd, env, 'Inspect Go build ID');
  check(id === buildID(role, version, sourceSHA, schema), 'Go build ID disagrees with the selected release.');
}
function identity(role, version, source, binary, frontend, schema) {
  const core = {
    format,
    role,
    version,
    source_sha256: source,
    binary_sha256: binary,
    frontend_sha256: frontend,
    schema_version: schema.schema_version,
    schema_sha256: schema.schema_sha256,
  };
  return { ...core, artifact_id: digest(json(core)) };
}
async function boundedJSON(path, cap) {
  const s = await plain(path, 'file');
  check(s.size > 0 && s.size <= cap, 'Invalid manifest or release identity size.');
  const bytes = await regularFile(path, (handle) => handle.readFile());
  try {
    return { value: JSON.parse(bytes), bytes };
  } catch {
    throw Error('Invalid manifest or release identity JSON.');
  }
}
async function safeOutput(source, out) {
  const abs = resolve(out);
  check(abs !== source && !child(abs, source), 'Release output must be separate from checkout roots.');
  check(
    !child(source, abs) || child(join(source, '.runtime'), abs),
    'Under the checkout, use a new .runtime child for release output.',
  );
  await cleanDirectory(dirname(abs));
  try {
    await lstat(abs);
    throw Error('Artifact destination already exists.');
  } catch (e) {
    if (e.code !== 'ENOENT') throw e;
  }
  return abs;
}
export async function buildReleaseArtifact({ source, out, role, version }) {
  validLabel(role, version);
  source = await cleanDirectory(source);
  out = await safeOutput(source, out);
  const scratchParent = await realpath(tmpdir()),
    scratch = await mkdtemp(join(scratchParent, 'studio-release-'));
  const ready = join(scratch, 'package'),
    tree = join(scratch, 'source');
  try {
    await mkdir(ready);
    await mkdir(tree);
    const sourceSHA = await snapshot(source, tree),
      schema = await schemaFingerprint(tree),
      env = buildEnv();
    const web = join(tree, 'apps', 'workbench');
    const dist = join(ready, 'dist');
    const userConfig = join(scratch, 'user-npmrc'),
      globalConfig = join(scratch, 'global-npmrc');
    await writeFile(userConfig, '');
    await writeFile(globalConfig, '');
    await run(
      process.execPath,
      [
        npmCLI(),
        'ci',
        '--offline',
        '--include=dev',
        '--ignore-scripts',
        '--no-audit',
        '--no-fund',
        '--userconfig',
        userConfig,
        '--globalconfig',
        globalConfig,
      ],
      web,
      env,
      'Locked offline frontend install',
    );
    await run(
      process.execPath,
      [join(web, 'node_modules', 'typescript', 'bin', 'tsc'), '--project', 'tsconfig.json', '--noEmit'],
      web,
      env,
      'TypeScript',
    );
    await run(
      process.execPath,
      [join(web, 'node_modules', 'vite', 'bin', 'vite.js'), 'build', '--outDir', dist],
      web,
      env,
      'Vite',
    );
    const binary = join(ready, binName),
      go = await goForSource(tree, env);
    await run(
      go,
      [
        'build',
        '-mod=readonly',
        '-buildvcs=false',
        '-trimpath',
        '-ldflags=-buildid=' + buildID(role, version, sourceSHA, schema),
        '-o',
        binary,
        './cmd/server',
      ],
      tree,
      env,
      'Build Go workbench',
    );
    await expectGo(binary, role, version, sourceSHA, schema);
    const index = join(dist, 'index.html');
    await plain(index, 'file');
    const html = await readFile(index, 'utf8');
    check(html.split('</head>').length === 2, 'Built frontend has no unique document head.');
    const marker =
      '<meta name="studio-release-role" content="' +
      role +
      '"><meta name="studio-release-version" content="' +
      version +
      '"><meta name="studio-source-sha256" content="' +
      sourceSHA +
      '">';
    await writeFile(index, html.replace('</head>', marker + '</head>'));
    const built = await walk(dist, assetAllowed, {
      dirsAllowed: (p) => p === 'assets' || p.startsWith('assets/'),
    });
    check(
      built.some((x) => x.path === 'index.html') &&
        built.some((x) => x.path.endsWith('.js')) &&
        built.some((x) => x.path.endsWith('.css')),
      'Incomplete compiled frontend.',
    );
    const frontend = digest(json(built)),
      binarySHA = await hashFile(binary),
      meta = identity(role, version, sourceSHA, binarySHA, frontend, schema);
    await writeFile(join(dist, 'release-identity.json'), json(meta), { flag: 'wx' });
    const files = await walk(
      ready,
      (p) =>
        p === binName ||
        p === 'dist/release-identity.json' ||
        (p.startsWith('dist/') && assetAllowed(p.slice(5))),
    );
    const manifest = {
      format,
      role,
      version,
      platform: process.platform,
      arch: process.arch,
      node_version: process.versions.node,
      source_sha256: sourceSHA,
      ...schema,
      identity: meta,
      files,
    };
    await writeFile(join(ready, 'manifest.json'), json(manifest), { flag: 'wx' });
    // Publish the manifest last. A concurrent build cannot replace a previously prepared directory.
    await mkdir(out, { mode: 0o700 });
    await copyChecked(ready, out, files);
    await chmod(join(out, binName), 0o755);
    await copyFile(join(ready, 'manifest.json'), join(out, 'manifest.json'), constants.COPYFILE_EXCL);
    return verifyReleaseArtifact(out, { role, version, sha256: digest(json(manifest)) });
  } finally {
    check(
      dirname(await cleanDirectory(scratch)) === scratchParent &&
        basename(scratch).startsWith('studio-release-'),
      'Unexpected build scratch path.',
    );
    await rm(scratch, { recursive: true, force: true, maxRetries: 5, retryDelay: 120 });
  }
}
export async function verifyReleaseArtifact(dir, { role, version, sha256 } = {}) {
  const root = await cleanDirectory(dir);
  const data = await boundedJSON(join(root, 'manifest.json'), 1024 * 1024),
    m = data.value,
    manifestSHA = digest(data.bytes);
  check(
    m &&
      typeof m === 'object' &&
      Object.keys(m).sort().join(',') ===
        'arch,files,format,identity,node_version,platform,role,schema_sha256,schema_version,source_sha256,version' &&
      /^\d+\.\d+\.\d+$/.test(m.node_version),
    'Unexpected release manifest fields.',
  );
  if (sha256 !== undefined)
    check(
      hashPattern.test(sha256) && sha256 === manifestSHA,
      'Manifest SHA-256 does not match the trusted pin.',
    );
  check(
    m &&
      m.format === format &&
      m.platform === process.platform &&
      m.arch === process.arch &&
      hashPattern.test(m.source_sha256) &&
      Number.isSafeInteger(m.schema_version) &&
      m.schema_version > 0 &&
      m.schema_version <= 999 &&
      hashPattern.test(m.schema_sha256),
    'Invalid or incompatible release manifest.',
  );
  validLabel(m.role, m.version);
  if (role !== undefined) check(role === m.role, 'Release role mismatch.');
  if (version !== undefined) check(version === m.version, 'Release version mismatch.');
  const actual = await walk(
    root,
    (p) =>
      p === 'manifest.json' ||
      p === binName ||
      p === 'dist/release-identity.json' ||
      (p.startsWith('dist/') && assetAllowed(p.slice(5))),
    { dirsAllowed: (p) => p === 'dist' || p === 'dist/assets' || p.startsWith('dist/assets/') },
  );
  const files = actual.filter((v) => v.path !== 'manifest.json');
  check(
    Array.isArray(m.files) && json(files) === json(m.files),
    'Artifact file set or SHA-256 differs from the manifest.',
  );
  const binary = files.find((v) => v.path === binName),
    frontend = files
      .filter((v) => v.path.startsWith('dist/') && v.path !== 'dist/release-identity.json')
      .map((v) => ({ ...v, path: v.path.slice(5) }));
  check(
    binary &&
      frontend.some((r) => r.path === 'index.html') &&
      frontend.some((r) => r.path.endsWith('.js')) &&
      frontend.some((r) => r.path.endsWith('.css')),
    'Missing executable or built workbench.',
  );
  const expected = identity(m.role, m.version, m.source_sha256, binary.sha256, digest(json(frontend)), m);
  const embedded = await boundedJSON(join(root, 'dist', 'release-identity.json'), 2048);
  check(
    json(m.identity) === json(expected) && json(embedded.value) === json(expected),
    'Release identity does not match the executable and frontend bytes.',
  );
  await expectGo(join(root, binName), m.role, m.version, m.source_sha256, m);
  return {
    binaryPath: join(root, binName),
    distPath: join(root, 'dist'),
    artifactId: expected.artifact_id,
    binarySha256: expected.binary_sha256,
    sourceSha256: expected.source_sha256,
    manifestSha256: manifestSHA,
    schemaVersion: m.schema_version,
    schemaSha256: m.schema_sha256,
    format,
    identity: expected,
    manifest: m,
  };
}
export async function prepareReleasePlan(dir, { role, version, sha256, state, port } = {}) {
  const release = await verifyReleaseArtifact(dir, { role, version, sha256 });
  const parent = await cleanDirectory(state),
    root = dirname(release.binaryPath);
  check(
    root !== parent && !child(root, parent) && !child(parent, root),
    'Runtime state must be separate from release files.',
  );
  check(
    /^\d+$/.test(String(port)) && Number(port) > 0 && Number(port) <= 65535,
    'Expected an explicit loopback port from 1 to 65535.',
  );
  const site = join(parent, release.identity.role);
  for (const d of [site, join(site, 'references'), join(site, 'assets')]) {
    try {
      await cleanDirectory(d);
    } catch (e) {
      if (e.code !== 'ENOENT') throw e;
    }
  }
  const env = {
    APP_ADDR: '127.0.0.1:' + Number(port),
    APP_WEB_DIR: release.distPath,
    REFERENCE_DIR: join(site, 'references'),
    ASSET_DIR: join(site, 'assets'),
    PROVIDER_MODE: 'demo',
  };
  return {
    role: release.identity.role,
    version: release.identity.version,
    artifact_id: release.artifactId,
    manifest_sha256: release.manifestSha256,
    binary: release.binaryPath,
    cwd: site,
    environment: env,
  };
}
function options(args, names) {
  const out = {};
  for (let i = 0; i < args.length; i += 2) {
    const flag = args[i],
      val = args[i + 1];
    check(
      flag?.startsWith('--') &&
        names.includes(flag.slice(2)) &&
        val &&
        !val.startsWith('--') &&
        !Object.hasOwn(out, flag.slice(2)),
      'Invalid or duplicate release argument.',
    );
    out[flag.slice(2)] = val;
  }
  return out;
}
function required(o, names) {
  check(
    names.every((k) => Object.hasOwn(o, k)),
    'Missing required release option.',
  );
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [action, ...args] = process.argv.slice(2);
    let result;
    if (action === 'build') {
      const o = options(args, ['role', 'version', 'source', 'out']);
      required(o, ['role', 'version', 'source', 'out']);
      result = await buildReleaseArtifact(o);
    } else if (action === 'verify') {
      const o = options(args, ['artifact', 'sha256', 'role', 'version']);
      required(o, ['artifact']);
      result = await verifyReleaseArtifact(o.artifact, o);
    } else if (action === 'plan') {
      const o = options(args, ['artifact', 'sha256', 'role', 'version', 'state', 'port']);
      required(o, ['artifact', 'state', 'port']);
      result = await prepareReleasePlan(o.artifact, o);
    } else
      throw Error(
        'Use build --source DIR --out NEW_DIR --role baseline|candidate --version ID, verify --artifact DIR [--sha256 HEX], or plan --artifact DIR --state DIR --port PORT.',
      );
    const { manifest: _manifest, ...output } = result;
    console.log(JSON.stringify(output, null, 2));
  } catch (e) {
    console.error(e.message);
    process.exitCode = 1;
  }
}
