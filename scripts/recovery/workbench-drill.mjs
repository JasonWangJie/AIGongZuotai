import { execFile } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { copyFile, lstat, mkdir, readFile, readdir, realpath, rename, stat, writeFile } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs, promisify } from 'node:util';
import { createPitrDrill } from './pg-pitr-drill.mjs';

const execute = promisify(execFile);
const toolsDir = path.dirname(fileURLToPath(import.meta.url));
const repository = path.resolve(toolsDir, '../..');
const { values } = parseArgs({ options: {
  weed: { type: 'string' }, 'bin-dir': { type: 'string' }, 'stage-parent': { type: 'string' },
} });
if (!values.weed) throw new Error('Supply the pinned local weed.exe using --weed.');

async function checksum(file) {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(file)) hash.update(chunk);
  return hash.digest('hex');
}

// Source and destination are separately allocated, private drill directories.
// Verify each copied original, thumbnail, manifest, owner marker and key file.
async function copyVerifiedTree(source, destination) {
  await mkdir(destination);
  let files = 0;
  let bytes = 0;
  const treeHash = createHash('sha256');
  const entries = await readdir(source, { withFileTypes: true });
  for (const entry of entries.sort((a, b) => Buffer.from(a.name).compare(Buffer.from(b.name)))) {
    const from = path.join(source, entry.name);
    const to = path.join(destination, entry.name);
    if (entry.isDirectory() && !entry.isSymbolicLink()) {
      const child = await copyVerifiedTree(from, to);
      treeHash.update(JSON.stringify(['dir', entry.name, child.sha256]) + '\n');
      files += child.files;
      bytes += child.bytes;
    } else if (entry.isFile() && !entry.isSymbolicLink()) {
      await copyFile(from, to);
      const digest = await checksum(from);
      if (digest !== await checksum(to)) throw new Error('A copied private file failed byte verification.');
      const size = (await stat(to)).size;
      treeHash.update(JSON.stringify(['file', entry.name, size, digest]) + '\n');
      bytes += size;
      files++;
    } else {
      throw new Error('Unexpected link or special file in the drill backup.');
    }
  }
  return { files, bytes, sha256: treeHash.digest('hex') };
}

const pg = await createPitrDrill({
  binDir: values['bin-dir'], stageParent: values['stage-parent'] || os.tmpdir(),
});
const root = pg.privateRoot;
const objects = path.join(root, 'object-service');
const source = path.join(root, 'platform-original');
const backup = path.join(root, 'platform-backup');
const restored = path.join(root, 'platform-restored');
const receipt = path.join(root, 'platform-receipt.json');
const password = randomBytes(24).toString('hex');
const phaseOptions = { cwd: repository, windowsHide: true, timeout: 300_000, maxBuffer: 2 * 1024 * 1024 };

async function objectAction(action) {
  const args = [path.join(toolsDir, 'object-store-drill/drill.py'), action, '--root', objects];
  if (action === 'start') args.push('--weed', path.resolve(values.weed));
  try {
    await execute('python', args, phaseOptions);
  } catch {
    throw new Error(`Local object service phase "${action}" failed. Inspect the private drill logs.`);
  }
}

async function detachOwnedDirectory(parent, sourceName, offlineName) {
  const actualParent = await realpath(parent);
  const from = path.join(actualParent, sourceName);
  const to = path.join(actualParent, offlineName);
  if (path.dirname(from) !== actualParent || path.dirname(to) !== actualParent || await realpath(from) !== from) {
    throw new Error('Source must be a direct private directory of this drill.');
  }
  if (!((await lstat(from)).isDirectory())) throw new Error('Source is not a directory.');
  try { await lstat(to); throw new Error('The offline destination already exists.'); }
  catch (error) { if (error.code !== 'ENOENT') throw error; }
  await rename(from, to);
}

async function appPhase(phase, location, { databaseURL, drillId }) {
  const settings = JSON.parse(await readFile(path.join(objects, 's3.json'), 'utf8'));
  const env = { ...process.env };
  for (const key of Object.keys(env)) {
    if (/^(?:PG|TEST_|AWS_|S3_|SMTP_|MAIL_|AUTH_|OPERATIONS_|BILLING_|MODERATION_|MEDIA_MODERATION_|ASYNC_|OPENAI_|DATABASE_URL$)/i.test(key)) delete env[key];
  }
  for (const key of ['TEST_S3_REGION', 'TEST_S3_BUCKET', 'TEST_S3_ENDPOINT', 'TEST_S3_ACCESS_KEY_ID', 'TEST_S3_SECRET_ACCESS_KEY']) {
    if (!settings[key]) throw new Error('The isolated object service configuration is incomplete.');
    env[key] = settings[key];
  }
  const deliveryKey = await readFile(path.join(location, 'delivery.key'), 'utf8');
  Object.assign(env, {
    RECOVERY_DRILL_DATABASE: databaseURL, RECOVERY_DRILL_ID: drillId, RECOVERY_DRILL_PHASE: phase, RECOVERY_DRILL_ROOT: root,
    RECOVERY_DRILL_ASSETS: path.join(location, 'assets'), RECOVERY_DRILL_REFERENCES: path.join(location, 'references'),
    RECOVERY_DRILL_RECEIPT: receipt, RECOVERY_DRILL_USER_PASSWORD: password,
    RECOVERY_DRILL_DELIVERY_KEY: deliveryKey, RECOVERY_DRILL_PRICES: path.join(location, 'price.json'),
  });
  try {
    const testName = 'TestLocalRecoveryDrillPlatform';
    const { stdout } = await execute('go', ['test', '-json', '-tags=recoverydrill', '-count=1', '-timeout=4m', '-run=^' + testName + '$', './internal/studio'], { ...phaseOptions, env });
    const actions = stdout.trim().split(/\r?\n/).map(line => JSON.parse(line))
      .filter(event => event.Package === 'aigongzuotai/internal/studio' && event.Test === testName && event.Action !== 'output')
      .map(event => event.Action);
    if (actions.join(',') !== 'run,pass') throw new Error('The exact application probe did not pass.');
  } catch (error) {
    // Only return the failing test's own high-level diagnosis. Child commands,
    // credentials, private manifests and server logs never enter the report.
    const line = String(error.stdout || '').split(/\r?\n/).flatMap(s => {
      try { return [JSON.parse(s).Output || '']; } catch { return []; }
    }).find(s => /recovery_platform_test\.go:\d+: /.test(s));
    const detail = line?.split(/recovery_platform_test\.go:\d+: /)[1]?.trim();
    const safe = detail && /^[A-Za-z0-9 ,.-]{1,140}$/.test(detail) &&
      ![password, deliveryKey, settings.TEST_S3_ACCESS_KEY_ID, settings.TEST_S3_SECRET_ACCESS_KEY].some(s => detail.includes(s));
    throw new Error(`Workbench phase "${phase}" failed${safe ? ': ' + detail : '.'}`);
  }
}

let objectsStarted = false;
let privateBackup;
try {
  await mkdir(source);
  await writeFile(path.join(source, 'delivery.key'), randomBytes(32).toString('base64'), { mode: 0o600 });
  const price = { schema_version: 1, provider_mode: 'demo', unit: 'CREDIT', scale: 6,
    rates: [{ model_id: 'studio-image', kind: 'image', operation: 'generate', preset_id: 'preset-1', artifact_micros: '200000' }] };
  await writeFile(path.join(source, 'price.json'), JSON.stringify(price));
  await objectAction('start');
  objectsStarted = true;
  await objectAction('seed');
  await pg.startSource();
  await pg.takeBaseBackup();
  await pg.withSource(connection => appPhase('seed', source, connection));
  privateBackup = await copyVerifiedTree(source, backup);
  await pg.createRestorePoint({ afterPoint: connection => appPhase('after_point', source, connection) });
  await pg.stopSource();
  await objectAction('backup');
  objectsStarted = false;
  // Original private paths become unavailable. Restore solely from the copies.
  await detachOwnedDirectory(root, 'platform-original', 'platform-original-offline');
  await detachOwnedDirectory(objects, 'source', 'source-offline');
  const start = performance.now();
  await objectAction('restore');
  objectsStarted = true;
  await objectAction('verify');
  const restoredFiles = await copyVerifiedTree(backup, restored);
  if (restoredFiles.sha256 !== privateBackup.sha256 || restoredFiles.files !== privateBackup.files || restoredFiles.bytes !== privateBackup.bytes) {
    throw new Error('The original and restored private trees differ.');
  }
  await pg.restore();
  await pg.withRestored(connection => appPhase('verify', restored, connection));
  const elapsed = Math.round(performance.now() - start);
  await pg.stopRestored();
  await objectAction('stop');
  objectsStarted = false;
  const report = {
    verified_at: new Date().toISOString(), scope: 'isolated single-host recovery drill',
    postgres: pg.evidence(), private_backup: privateBackup, restore_through_authenticated_read_ms: elapsed,
    platform: { original_account: true, draft_revision: 1, later_revision_excluded: true,
      original_asset_and_thumbnail_versions: true, private_bytes_compared: 4,
      private_download_requires_login: true, original_reservations: 1, charged_micros: '200000' },
  };
  console.log(JSON.stringify(report, null, 2));
} finally {
  try {
    // A launch may have persisted its verified process identity before the
    // command's response reached this coordinator.
    let recordedProcess = false;
    try {
      const state = JSON.parse(await readFile(path.join(objects, 'state.json'), 'utf8'));
      recordedProcess = Boolean(state.running);
    } catch (error) { if (error.code !== 'ENOENT') throw error; }
    if (objectsStarted || recordedProcess) await objectAction('stop');
  }
  finally { await pg.close(); }
}
