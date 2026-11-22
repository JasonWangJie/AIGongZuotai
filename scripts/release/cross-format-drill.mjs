import { randomBytes } from 'node:crypto';
import { execFile } from 'node:child_process';
import { promises as fs } from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { once } from 'node:events';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { parseArgs, promisify } from 'node:util';
import { setTimeout as delay } from 'node:timers/promises';
import { createPitrDrill } from '../recovery/pg-pitr-drill.mjs';
import { checkReleaseFixture, launchReleaseChild, releaseEnvironment } from './release-process.mjs';
import { createLocalIngressTLS, requestLocalIngress, startLocalIngress } from './local-ingress.mjs';
import {
  compareInventory,
  freezeReleaseFrontend,
  inspectRelease,
  inspectTree,
  sha,
  verify,
  verifyPair,
} from './cross-format-evidence.mjs';

const exec = promisify(execFile);
const key = () => randomBytes(24).toString('hex');
let phase = 'release pin verification';
const png = Buffer.from(
  'iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEElEQVR4nGMQWnIYiBggFAAmhgXlAUvQ9QAAAABJRU5ErkJggg==',
  'base64'
);
export const pins = Object.freeze({
  previous: {
    format: 36,
    go: '100a1f278522bc4500fc8bd1dc2e3d723f7cab82b994758d158a2f3543a84ef8',
    binary: 'e0e161e4e08fa42a74ca4380dd2ba78a72cfe08d520a3f951a34d484a9336103',
    dist: '65514d40a505e5aa5f9c70b6d02071c0b5f83ade479ee88f99b196b33a042c5f',
    migrations: '5128b1a48ce374699d85700542b573e44eac2ff726107b14eced499d99c0df19',
  },
  candidate: {
    format: 37,
    go: '4b23ad8fa7b97fdbf41a34823bd1887135f6dd45d913a8381e4a3076f3ea92f5',
    binary: '6fb7b00b8899f84e9acca769bc9201a250c47c308de1524e714a0378ecb20caf',
    dist: '7e3bb48606beda3979b83736c0dfa4ed0b7d76e782704d5cb3c8820460b90713',
    migrations: '8ae862445afbf7cc8f02dad3c7fc8dea9073082d6ce6661e1276a6852b79e1e9',
  },
});
const osEnv = () =>
  Object.fromEntries(
    [
      'SYSTEMROOT',
      'WINDIR',
      'PATH',
      'TEMP',
      'TMP',
      'USERPROFILE',
      'LOCALAPPDATA',
      'APPDATA',
      'GOPATH',
      'GOMODCACHE',
      'GOCACHE',
    ]
      .filter((k) => process.env[k])
      .map((k) => [k, process.env[k]])
  );

async function buildProbe(root, source) {
  const binary = path.join(root, 'cross-format-probe.exe');
  const target = path.join(source, 'internal', 'studio', 'release_process_test.go');
  const own = fileURLToPath(new URL('../../internal/studio/release_process_test.go', import.meta.url));
  const overlay = path.join(root, 'probe-overlay.json');
  await fs.writeFile(overlay, JSON.stringify({ Replace: { [target]: own } }), { flag: 'wx' });
  await exec(
    'go',
    ['test', '-c', '-tags=releasedrill', '-overlay', overlay, '-o', binary, './internal/studio'],
    {
      cwd: source,
      env: {
        ...osEnv(),
        GOTOOLCHAIN: 'local',
        GOENV: 'off',
        GOWORK: 'off',
        GOPROXY: 'off',
        GOSUMDB: 'off',
        CGO_ENABLED: '0',
      },
      windowsHide: true,
      timeout: 180000,
      maxBuffer: 512 * 1024,
    }
  );
  return binary;
}

async function freePort() {
  const server = net.createServer();
  server.listen({ port: 0, host: '127.0.0.1', exclusive: true });
  await once(server, 'listening');
  const port = server.address().port;
  await new Promise((resolve, reject) => server.close((err) => (err ? reject(err) : resolve())));
  return port;
}

function processController(root, probe) {
  let active,
    storageId = '',
    serial = 0;
  async function gate(link, tree, cluster, format, phase) {
    checkReleaseFixture(link.databaseURL, link.drillId);
    const env = {
      ...osEnv(),
      DATABASE_URL: link.databaseURL,
      RELEASE_FIXTURE: root,
      RELEASE_DRILL_ID: link.drillId,
      RELEASE_CHECK_CLUSTER: cluster,
      RELEASE_CHECK_FORMAT: String(format),
      RELEASE_CHECK_PHASE: phase,
      RELEASE_STORAGE_ID: storageId,
      REFERENCE_DIR: path.join(tree, 'references'),
      ASSET_DIR: path.join(tree, 'assets'),
    };
    const { stdout } = await exec(
      probe,
      ['-test.run=^TestReleaseProcessOwnership$', '-test.v', '-test.timeout=25s'],
      { cwd: root, env, timeout: 35000, windowsHide: true }
    );
    const line = stdout.match(/^.*RELEASE_LOCK_FACTS (\{[^\r\n]+\})$/m)?.[1];
    verify(
      line && /--- PASS: TestReleaseProcessOwnership\b/.test(stdout),
      'Missing database and directory lock observations'
    );
    const result = JSON.parse(line);
    verify(result.drillId === link.drillId && result.schemaVersion === format, 'Wrong cluster or schema');
    if (phase !== 'fresh') {
      verify(
        result.databaseID && (!storageId || storageId === result.databaseID),
        'Database and private owner identity changed'
      );
      storageId = result.databaseID;
      verify(
        ['postgresAdvisory', 'referenceOS', 'assetOS'].every((k) => result[k] === (phase === 'free')),
        'Writer or private lock evidence differs'
      );
    }
    return result.business;
  }
  async function start(link, tree, cluster, release, formatBefore, fresh = false) {
    verify(!active, 'Previous writer must exit first');
    await gate(link, tree, cluster, formatBefore, fresh ? 'fresh' : 'free');
    const port = await freePort();
    const child = launchReleaseChild({
      binary: release.binary,
      digest: release.binarySha,
      log: path.join(root, 'cross-server-' + ++serial + '.log'),
      env: releaseEnvironment({
        databaseURL: link.databaseURL,
        assetDir: path.join(tree, 'assets'),
        referenceDir: path.join(tree, 'references'),
        mediaKey: await fs.readFile(path.join(tree, 'delivery.key'), 'utf8'),
        webDir: release.dist,
        port,
      }),
    });
    active = { child, link, tree, cluster, format: release.format };
    await child.launched();
    const until = performance.now() + 90000;
    while (!(await child.listening())) {
      verify(performance.now() < until, 'Owned server did not listen');
      await delay(100);
    }
    await gate(link, tree, cluster, release.format, 'held');
    return 'http://127.0.0.1:' + port;
  }
  async function stop() {
    verify(active, 'No owned writer');
    const { child, link, tree, cluster, format } = active;
    await child.stop();
    active = null;
    return gate(link, tree, cluster, format, 'free');
  }
  async function close() {
    if (active) {
      const p = active;
      try {
        await stop();
      } catch (error) {
        p.child.detach();
        throw error;
      }
    }
  }
  return { start, stop, close };
}

function jsonRequest(method, body, headers = {}) {
  return {
    method,
    headers: { 'Content-Type': 'application/json', ...headers },
    body: Buffer.from(JSON.stringify(body)),
  };
}
function fixtureGraph(models, name) {
  const model = (id) => {
    const entry = models.find((m) => m.id === id && m.enabled);
    verify(entry?.version, 'Missing fixture catalogue model');
    return { model_id: id, model_version: entry.version };
  };
  return {
    name,
    template_id: 'text-image-approval',
    template_version: '1',
    nodes: [
      { id: 'script', kind: 'text', title: 'Scene', prompt: 'Describe the scene', ...model('studio-chat') },
      {
        id: 'picture',
        kind: 'image',
        title: 'Image',
        prompt: 'Illustrate',
        ...model('studio-image'),
        parameters: { count: 1, aspect_ratio: '1:1' },
      },
      { id: 'pick', kind: 'approval', title: 'Approve' },
    ],
    edges: [
      { from: 'script', from_port: 'text', to: 'picture', to_port: 'prompt' },
      { from: 'picture', from_port: 'images', to: 'pick', to_port: 'images' },
    ],
  };
}

class WindowReads {
  constructor(gateway, tls) {
    this.gateway = gateway;
    this.tls = tls;
  }
  async http(route, status, options = {}) {
    const result = await requestLocalIngress(this.gateway.origin, {
      cert: this.tls.cert,
      path: route,
      ...options,
    });
    verify(result.status === status, 'Unexpected HTTP status ' + result.status + ' expected ' + status);
    return result;
  }
  async seed() {
    const account = {
      email: 'cross-' + key() + '@example.test',
      password: key(),
      name: 'Cross-format drill',
    };
    const register = await this.http('/api/v1/auth/register', 201, jsonRequest('POST', account));
    const id = JSON.parse(register.body);
    this.cookie = register.headers['set-cookie']?.find((v) => v.startsWith('studio_session='))?.split(';')[0];
    verify(this.cookie && id.user?.id && id.project?.id, 'Original participant missing');
    this.project = id.project.id;
    this.user = id.user.id;
    this.headers = { Cookie: this.cookie, 'X-Project-ID': this.project };
    this.definitions = '/api/v1/projects/' + this.project + '/workflow-definitions';
    this.models = JSON.parse(
      (await this.http('/api/v1/bootstrap', 200, { headers: this.headers })).body
    ).models;
    const form = new FormData();
    form.append('file', new Blob([png], { type: 'image/png' }), 'cross-format.png');
    const multipart = new Response(form);
    const upload = JSON.parse(
      (
        await this.http('/api/v1/references', 201, {
          method: 'POST',
          headers: {
            ...this.headers,
            'Content-Type': multipart.headers.get('content-type'),
            'Idempotency-Key': key(),
          },
          body: Buffer.from(await multipart.arrayBuffer()),
        })
      ).body
    );
    verify(
      upload.project_id === this.project &&
        upload.url === '/api/v1/references/' + upload.id + '/content?project_id=' + this.project,
      'Private reference belongs to unexpected scope'
    );
    this.image = upload.url;
    this.before = await this.write('Original version');
    // Retained only inside the PG fixture's ACL-protected recovery kit.
    return account;
  }
  async write(name, original) {
    const operationKey = key(),
      graph = fixtureGraph(this.models, name);
    const input = original
      ? { definition_id: original.id, expected_version: original.version, graph }
      : { expected_version: 0, graph };
    const response = await this.http(
      this.definitions,
      201,
      jsonRequest('POST', input, { ...this.headers, 'Idempotency-Key': operationKey })
    );
    const record = JSON.parse(response.body);
    verify(
      record.id &&
        record.project_id === this.project &&
        (!original || record.id === original.id) &&
        record.version === input.expected_version + 1 &&
        record.graph?.name === name,
      'Wrong business write acknowledgement'
    );
    return { id: record.id, version: record.version, graph: record.graph, key: operationKey };
  }
  async receipt(written, present) {
    const response = await this.http(this.definitions + '/receipt', present ? 200 : 404, {
      headers: { ...this.headers, 'Idempotency-Key': written.key },
    });
    if (present) {
      const row = JSON.parse(response.body);
      verify(
        row.id === written.id &&
          row.project_id === this.project &&
          row.version === written.version &&
          JSON.stringify(row.graph) === JSON.stringify(written.graph),
        'Original receipt content differs'
      );
    } else
      verify(
        JSON.parse(response.body).error?.code === 'WORKFLOW_NOT_FOUND',
        'Read-only receipt was not an authenticated missing record'
      );
  }
  async restricted(origin, source, release) {
    const paths = ['/', '/readyz', this.image, this.definitions + '/receipt'];
    this.gateway.candidate(origin, { userId: this.user, paths });
    await this.http(this.image, 403);
    const ready = JSON.parse((await this.http('/readyz', 200, { headers: this.headers })).body);
    verify(ready.ready === true && ready.state === 'ready', 'Writer not ready');
    verify(
      sha((await this.http('/', 200, { headers: this.headers })).body) === release.indexSha,
      'Frontend served by listener differs from frozen artifact'
    );
    const image = await this.http(this.image, 200, { headers: this.headers });
    verify(sha(image.body) === sha(png), 'Original private bytes differ');
    await this.receipt(this.before, true);
    if (source) this.gateway.open();
  }
  async blockedWrite(blockedKey) {
    await this.http(
      this.definitions,
      503,
      jsonRequest(
        'POST',
        { expected_version: 0, graph: fixtureGraph(this.models, 'Withheld submission') },
        { ...this.headers, 'Idempotency-Key': blockedKey }
      )
    );
  }
}

async function runDrill(previousRoot, candidateRoot, stageParent, binDir) {
  verify(process.platform === 'win32', 'Windows PostgreSQL 16 fixture required');
  const previousInput = await inspectRelease(previousRoot),
    candidateInput = await inspectRelease(candidateRoot);
  verifyPair(previousInput, candidateInput, pins);
  phase = 'loopback TLS preparation';
  const tls = await createLocalIngressTLS();
  phase = 'private PostgreSQL fixture preparation';
  const pg = await createPitrDrill({ stageParent, binDir });
  const root = pg.privateRoot,
    live = path.join(root, 'current-platform'),
    backup = path.join(root, 'point-platform'),
    restored = path.join(root, 'restored-platform'),
    kit = path.join(root, 'cross-format-review');
  let runtime, gateway, reads, auth, before, after, recovered, backupInventory, originalCopy, newCopy;
  const withheld = key(),
    restoredWriteProbe = key();
  const started = performance.now();
  try {
    phase = 'private frozen frontend copies';
    const previous = await freezeReleaseFrontend(previousInput, path.join(root, 'web-036'));
    const candidate = await freezeReleaseFrontend(candidateInput, path.join(root, 'web-037'));
    await fs.mkdir(kit, { mode: 0o700 });
    await fs.mkdir(live, { mode: 0o700 });
    for (const name of ['references', 'assets']) await fs.mkdir(path.join(live, name));
    await fs.writeFile(path.join(live, 'delivery.key'), randomBytes(32).toString('base64'), {
      flag: 'wx',
      mode: 0o600,
    });
    phase = 'offline ownership probe build';
    runtime = processController(root, await buildProbe(root, candidateRoot));
    phase = '036 original writer and original receipts';
    await pg.startSource();
    await pg.withSource(async (link) => {
      const origin = await runtime.start(link, live, 'source', previous, previous.format, true);
      gateway = await startLocalIngress({ cert: tls.cert, key: tls.key, initialTarget: origin });
      reads = new WindowReads(gateway, tls);
      verify(
        sha((await reads.http('/', 200)).body) === previous.indexSha,
        'Initial frontend differs from frozen artifact'
      );
      auth = await reads.seed();
      await reads.receipt(reads.before, true);
      verify(
        sha((await reads.http(reads.image, 200, { headers: reads.headers })).body) === sha(png),
        'Original image verification failed'
      );
      await gateway.maintain();
      before = await runtime.stop();
    });
    phase = '036 private backup and physical base';
    backupInventory = await inspectTree(live, backup);
    await pg.takeBaseBackup();
    await pg.createRestorePoint({
      afterPoint: async (link) => {
        phase = '037 migration, scoped inspection and window writes';
        const origin = await runtime.start(link, live, 'source', candidate, previous.format);
        await reads.restricted(origin, true, candidate);
        reads.changed = await reads.write('New revision during upgrade', reads.before);
        reads.created = await reads.write('New definition during upgrade');
        verify(
          reads.created.id !== reads.before.id && reads.changed.version === 2,
          'Window writes collapsed'
        );
        await reads.receipt(reads.changed, true);
        await reads.receipt(reads.created, true);
        phase = '037 drain and source-side receipt reconciliation';
        await gateway.maintain();
        await reads.blockedWrite(withheld);
        // While the general write route stays closed, the authorized reviewer
        // verifies both window receipts and absence of the refused third key.
        await reads.restricted(origin, false, candidate);
        await reads.receipt(reads.changed, true);
        await reads.receipt(reads.created, true);
        await reads.receipt({ key: withheld }, false);
        await gateway.maintain();
        after = await runtime.stop();
        newCopy = await inspectTree(live);
        await fs.writeFile(
          path.join(kit, 'synthetic-handoff.json'),
          JSON.stringify({
            account: auth,
            project: reads.project,
            original: reads.before,
            window: [reads.changed, reads.created],
            withheldKey: withheld,
            image: reads.image,
            drillId: link.drillId,
          }),
          { flag: 'wx', mode: 0o600 }
        );
        await fs.copyFile(path.join(root, 'pgpass.conf'), path.join(kit, 'source.pgpass'), 1);
      },
    });
    phase = 'source WAL archive and offline source preservation';
    await pg.stopSource();
    const offlineControl = path.join(root, 'offline-source', 'global', 'pg_control');
    const preservedControl = sha(await fs.readFile(offlineControl));
    phase = 'physical named-point recovery';
    await pg.restore();
    originalCopy = await inspectTree(backup, restored);
    verify(
      originalCopy.sha256 === backupInventory.sha256 && originalCopy.files === backupInventory.files,
      'Independent private backup changed'
    );
    phase = '036 restored writer; read-only TLS inspection';
    await pg.withRestored(async (link) => {
      const origin = await runtime.start(link, restored, 'restored', previous, previous.format);
      await reads.restricted(origin, false, previous);
      await reads.receipt(reads.changed, false);
      await reads.receipt(reads.created, false);
      await reads.receipt({ key: withheld }, false);
      await reads.blockedWrite(restoredWriteProbe);
      await reads.receipt({ key: restoredWriteProbe }, false);
      await gateway.maintain();
      await reads.http('/readyz', 503, { headers: reads.headers });
      recovered = await runtime.stop();
    });
    phase = 'closed ingress and final evidence verification';
    await pg.stopRestored();
    const window = compareInventory(before, after, recovered),
      e = pg.evidence();
    verify(
      e.promoted &&
        e.source_detached &&
        e.source_archive_detached &&
        e.restored_before === 1 &&
        e.restored_after === 0,
      'Physical target evidence incomplete'
    );
    verify(
      preservedControl === sha(await fs.readFile(offlineControl)) &&
        newCopy.sha256 === (await inspectTree(live)).sha256,
      'Preserved upgraded database or private companions changed'
    );
    verify(
      gateway.snapshot().phase === 'maintenance',
      'Ingress must remain closed awaiting window disposition'
    );
    const result = {
      format: 'studio.cross-format-recovery.v1',
      previous: pins.previous,
      candidate: pins.candidate,
      original_format: previous.format,
      upgraded_format: candidate.format,
      restored_format: previous.format,
      private_backup_files: originalCopy.files,
      private_tree_verified: true,
      private_image_sha256: sha(png),
      database_uuid_preserved: true,
      target_lsn: e.point_lsn,
      source_post_window_lsn: e.post_commit_lsn,
      paused_replay_lsn: e.paused_replay_lsn,
      base_manifest_sha256: e.base_manifest_sha256,
      source_data_and_wal_retained: true,
      original_receipt_restored: true,
      blocked_operation_has_no_receipt: true,
      window_inventory: window,
      ingress_final: 'maintenance',
      elapsed_ms: Math.round(performance.now() - started),
    };
    await fs.writeFile(path.join(kit, 'aggregate.json'), JSON.stringify(result, null, 2) + '\n', {
      flag: 'wx',
      mode: 0o600,
    });
    return result;
  } finally {
    try {
      if (gateway) await gateway.close();
    } finally {
      try {
        if (runtime) await runtime.close();
      } finally {
        await pg.close();
      }
    }
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href) {
  try {
    const { values } = parseArgs({
      options: {
        'previous-source': { type: 'string' },
        'candidate-source': { type: 'string' },
        'stage-parent': { type: 'string' },
        'bin-dir': { type: 'string' },
        'verify-only': { type: 'boolean' },
        run: { type: 'boolean' },
      },
    });
    verify(
      values['previous-source'] &&
        values['candidate-source'] &&
        Boolean(values.run) !== Boolean(values['verify-only']),
      'Choose --verify-only or --run and specify two frozen source roots'
    );
    const old = path.resolve(values['previous-source']),
      next = path.resolve(values['candidate-source']);
    if (values['verify-only']) {
      verifyPair(await inspectRelease(old), await inspectRelease(next), pins);
      console.log(
        JSON.stringify({ verified: true, previous: pins.previous, candidate: pins.candidate }, null, 2)
      );
    } else {
      console.log(
        JSON.stringify(await runDrill(old, next, values['stage-parent'], values['bin-dir']), null, 2)
      );
    }
  } catch (error) {
    // Infrastructure, subprocess and HTTP errors may contain private paths or
    // connection arguments. Only print the explicit local invariant failures.
    console.error(
      'Cross-format drill stopped during ' +
        phase +
        '. ' +
        (error.name === 'EvidenceError'
          ? error.message
          : 'Inspect the private local drill directory and tool prerequisites.')
    );
    process.exitCode = 1;
  }
}
