import { createHash } from 'node:crypto';
import { constants } from 'node:fs';
import fs from 'node:fs/promises';
import path from 'node:path';

export const sha = (data) => createHash('sha256').update(data).digest('hex');
export function verify(ok, message) {
  if (!ok) {
    const error = new Error(message);
    error.name = 'EvidenceError';
    throw error;
  }
}
const compare = (a, b) => (a < b ? -1 : a > b ? 1 : 0);

async function plain(target, directory) {
  const info = await fs.lstat(target);
  verify(
    !info.isSymbolicLink() && (directory ? info.isDirectory() : info.isFile()),
    'Unsafe or missing fixture entry'
  );
  verify(
    path.resolve(await fs.realpath(target)).toLowerCase() === path.resolve(target).toLowerCase(),
    'Linked fixture component'
  );
  return info;
}

// Used for both pinned dist directories and offline private companion trees.
// Only a newly created destination may be filled. Hash both sides of each copy.
export async function inspectTree(root, destination) {
  await plain(root, true);
  if (destination) await fs.mkdir(destination, { mode: 0o700 });
  const files = [];
  let bytes = 0,
    entriesSeen = 0;
  async function visit(relative, depth) {
    verify(depth <= 16, 'Fixture tree too deep');
    const entries = await fs.readdir(path.join(root, relative), { withFileTypes: true });
    for (const entry of entries.sort((a, b) => compare(a.name, b.name))) {
      verify(++entriesSeen <= 4096, 'Fixture tree has too many entries');
      const name = path.posix.join(relative, entry.name);
      const source = path.join(root, name);
      const target = destination && path.join(destination, name);
      if (entry.isDirectory()) {
        await plain(source, true);
        if (target) await fs.mkdir(target, { mode: 0o700 });
        await visit(name, depth + 1);
      } else {
        const info = await plain(source, false);
        bytes += info.size;
        verify(
          files.length < 2048 && info.size <= 32 * 1024 * 1024 && bytes <= 256 * 1024 * 1024,
          'Fixture tree exceeds bounds'
        );
        const digest = sha(await fs.readFile(source));
        if (target) {
          await fs.copyFile(source, target, constants.COPYFILE_EXCL);
          verify(sha(await fs.readFile(target)) === digest, 'Private copy bytes changed');
        }
        files.push([name, info.size, digest]);
      }
    }
  }
  await visit('', 0);
  files.sort((a, b) => compare(a[0], b[0]));
  return { files: files.length, bytes, sha256: sha(JSON.stringify(files)) };
}

export async function inspectRelease(root) {
  await plain(root, true);
  const migrationRoot = path.join(root, 'db', 'migrations');
  await plain(migrationRoot, true);
  const names = (await fs.readdir(migrationRoot)).sort(compare);
  verify(names.length >= 1 && names.length <= 999, 'Invalid migration count');
  const migrations = [];
  for (const [index, name] of names.entries()) {
    verify(
      new RegExp('^' + String(index + 1).padStart(3, '0') + '_[A-Za-z0-9_-]+\\.sql$').test(name),
      'Migration sequence differs'
    );
    const file = path.join(migrationRoot, name),
      info = await plain(file, false);
    verify(info.size > 0 && info.size <= 4 * 1024 * 1024, 'Invalid migration size');
    migrations.push([name, sha(await fs.readFile(file))]);
  }
  const binary = path.join(root, '.runtime', 'server.exe'),
    dist = path.join(root, 'apps', 'workbench', 'dist');
  const binInfo = await plain(binary, false);
  verify(binInfo.size > 0 && binInfo.size <= 128 * 1024 * 1024, 'Invalid binary size');
  const frontend = await inspectTree(dist);
  // Freeze the actual inputs of the standard server and all Go code that may
  // run while building its separately tagged ownership probe (including TestMain).
  const code = [];
  let codeEntries = 0;
  async function collect(relative, depth = 0) {
    verify(depth <= 24, 'Go source tree too deep');
    const absolute = path.join(root, relative);
    await plain(absolute, true);
    for (const entry of await fs.readdir(absolute, { withFileTypes: true })) {
      verify(++codeEntries <= 16384, 'Go source tree too large');
      const next = path.posix.join(relative, entry.name);
      if (entry.isDirectory()) await collect(next, depth + 1);
      else if (/\.(go|c|h|s|syso)$/i.test(entry.name)) {
        const file = path.join(root, next),
          info = await plain(file, false);
        verify(info.size < 8 * 1024 * 1024 && code.length < 8192, 'Go source exceeds bounds');
        code.push([next, sha(await fs.readFile(file))]);
      } else verify(!entry.isSymbolicLink(), 'Linked Go input');
    }
  }
  for (const folder of ['internal', 'cmd/server', 'db']) await collect(folder);
  for (const file of ['go.mod', 'go.sum']) {
    await plain(path.join(root, file), false);
    code.push([file, sha(await fs.readFile(path.join(root, file)))]);
  }
  code.sort((a, b) => compare(a[0], b[0]));
  return {
    binary,
    dist,
    format: names.length,
    migrations,
    frontend,
    goSha: sha(JSON.stringify(code)),
    indexSha: sha(await fs.readFile(path.join(dist, 'index.html'))),
    migrationSha: sha(JSON.stringify(migrations)),
    binarySha: sha(await fs.readFile(binary)),
  };
}

export async function freezeReleaseFrontend(release, destination) {
  const copied = await inspectTree(release.dist, destination);
  verify(copied.sha256 === release.frontend.sha256, 'Frontend changed before private staging completed');
  return {
    ...release,
    dist: destination,
    indexSha: sha(await fs.readFile(path.join(destination, 'index.html'))),
  };
}

export function verifyPair(oldRelease, newRelease, pins) {
  for (const [release, pin] of [
    [oldRelease, pins.previous],
    [newRelease, pins.candidate],
  ]) {
    verify(
      release.format === pin.format &&
        release.binarySha === pin.binary &&
        release.goSha === pin.go &&
        release.frontend.sha256 === pin.dist &&
        release.migrationSha === pin.migrations,
      'Toolchain, migration or frontend evidence differs from the frozen release'
    );
  }
  verify(
    newRelease.format === oldRelease.format + 1 &&
      newRelease.binarySha !== oldRelease.binarySha &&
      JSON.stringify(newRelease.migrations.slice(0, -1)) === JSON.stringify(oldRelease.migrations),
    'Original migrations are not a byte-identical prefix'
  );
}

export function compareInventory(original, candidate, restored) {
  for (const state of [original, candidate, restored]) {
    verify(
      state &&
        ['versions', 'runs', 'tasks', 'reservations', 'journals'].every(
          (k) => Number.isSafeInteger(state[k]) && state[k] >= 0
        ),
      'Incomplete database inventory'
    );
  }
  verify(
    original.versions === 1 && candidate.versions === 3 && restored.versions === 1,
    'Window revision counts differ'
  );
  for (const k of ['runs', 'tasks', 'reservations', 'journals'])
    verify(
      original[k] === 0 && candidate[k] === 0 && restored[k] === 0,
      'Unexpected generated or financial effect'
    );
  return {
    original_revisions: 1,
    window_writes_confirmed_on_preserved_source: 2,
    original_revisions_read_from_rollback: 1,
    window_receipts_absent_from_rollback: 2,
    automatic_resubmissions: 0,
    unreviewed_window_writes: 2,
  };
}
