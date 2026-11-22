import { createHash, randomUUID } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { readFile, readdir, realpath, stat, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';
import { build } from 'vite';

const web = fileURLToPath(new URL('../', import.meta.url));
const repo = resolve(web, '../..');
const hash = (data) => createHash('sha256').update(data).digest('hex');
const sha = (digest) => [{ algorithm: 'SHA256', checksumValue: digest }];
const go = (...args) =>
  execFileSync('go', args, {
    cwd: repo,
    encoding: 'utf8',
    windowsHide: true,
  }).trim();

async function fileList(root, prefix = '') {
  const files = [];
  for (const entry of await readdir(join(root, prefix), {
    withFileTypes: true,
  })) {
    const name = prefix ? prefix + '/' + entry.name : entry.name;
    if (entry.isDirectory()) files.push(...(await fileList(root, name)));
    else if (entry.isFile()) files.push(name);
    else throw Error('Unexpected linked file in output');
  }
  return files.sort();
}

async function bundledPackages(dist, installed) {
  // Reproduce all chunk bytes without writing over the artifact being inventoried.
  const result = await build({
    root: web,
    logLevel: 'silent',
    build: { write: false },
  });
  const observed = new Set();
  const files = new Map();
  for (const output of (Array.isArray(result) ? result : [result]).flatMap(
    (r) => r.output,
  )) {
    const bytes = output.type === 'chunk' ? output.code : output.source;
    if (files.has(output.fileName)) throw Error('Duplicate emitted file');
    files.set(output.fileName, hash(bytes));
    if (output.type !== 'chunk') continue;
    for (const id of Object.keys(output.modules)) {
      const normalized = id.replaceAll('\\', '/').replaceAll('\0', '');
      const nodeRoot = installed.replaceAll('\\', '/') + '/';
      const at = normalized.indexOf(nodeRoot);
      if (at < 0) {
        if (normalized.includes('/node_modules/'))
          throw Error('Bundle references an untracked modules directory');
        continue;
      }
      const subpath = 'node_modules/' + normalized.slice(at + nodeRoot.length);
      const start =
        subpath.lastIndexOf('node_modules/') + 'node_modules/'.length;
      const parts = subpath.slice(start).split('/');
      const count = parts[0].startsWith('@') ? 2 : 1;
      observed.add(subpath.slice(0, start) + parts.slice(0, count).join('/'));
    }
  }
  const distFiles = await fileList(dist);
  if (files.size !== distFiles.length)
    throw Error('Dist file set differs from the reproduced build');
  const tree = createHash('sha256');
  for (const name of distFiles) {
    const digest = hash(await readFile(join(dist, name)));
    if (files.get(name) !== digest)
      throw Error(
        'Dist bytes differ from reproduced production output: ' + name,
      );
    tree.update(name + '\0' + digest + '\0');
  }
  return { digest: tree.digest('hex'), observed, count: files.size };
}

async function localLicense(dir) {
  const evidence = [];
  for (const e of (await readdir(dir, { withFileTypes: true })).sort((a, b) =>
    a.name.localeCompare(b.name, 'en'),
  )) {
    if (
      !e.isFile() ||
      !/^(licen[cs]e|copying|notice|copyright)(?:$|[-._ ])/i.test(e.name)
    )
      continue;
    const p = join(dir, e.name);
    if ((await stat(p)).size > 2 * 1024 * 1024)
      throw Error('Oversized license evidence file');
    const bytes = await readFile(p);
    const text = bytes.toString('utf8');
    let recognized = '';
    if (/Apache License\s+Version 2\.0/.test(text)) recognized = 'Apache-2.0';
    else if (
      /Permission is hereby granted, free of charge[\s\S]+THE SOFTWARE IS PROVIDED "AS IS"/i.test(
        text,
      )
    )
      recognized = 'MIT';
    else if (
      /Redistribution and use in source and binary forms/i.test(text) &&
      /Neither the name|Neither the copyright holder/i.test(text)
    )
      recognized = 'BSD-3-Clause';
    evidence.push({ name: e.name, digest: hash(bytes), recognized });
  }
  if (!evidence.length) {
    const files = await readdir(dir, { withFileTypes: true });
    const readme = files.find(
      (e) => e.isFile() && /^readme\.md$/i.test(e.name),
    );
    if (readme) {
      const path = join(dir, readme.name);
      if ((await stat(path)).size <= 2 * 1024 * 1024) {
        const body = await readFile(path);
        if (/^#{1,6}\s+licen[cs]e\s*$/im.test(body.toString('utf8'))) {
          evidence.push({
            name: readme.name + ' (contains license section)',
            digest: hash(body),
            recognized: '',
          });
        }
      }
    }
  }
  return evidence;
}

function pkg(name, version, key, extra = {}) {
  return {
    SPDXID: 'SPDXRef-' + hash(key).slice(0, 24),
    name,
    versionInfo: version,
    filesAnalyzed: false,
    downloadLocation: 'NOASSERTION',
    copyrightText: 'NOASSERTION',
    licenseDeclared: 'NOASSERTION',
    licenseConcluded: 'NOASSERTION',
    ...extra,
  };
}

function licenseMetadata(evidence, declared) {
  const ids = new Set(
    evidence.filter((e) => !/^notice/i.test(e.name)).map((e) => e.recognized),
  );
  const fromText = ids.size === 1 ? [...ids][0] : '';
  return {
    licenseDeclared: declared || fromText || 'NOASSERTION',
    licenseComments: evidence.length
      ? evidence.map((e) => e.name + ' SHA256 ' + e.digest).join('; ')
      : 'No top-level license file found in the installed package.',
  };
}

async function inventory(server, dist) {
  const lockBytes = await readFile(join(web, 'package-lock.json'));
  const lock = JSON.parse(lockBytes);
  if (lock.lockfileVersion !== 3)
    throw Error('Expected npm lockfile version 3');
  const installed = await realpath(join(web, 'node_modules'));
  const bundle = await bundledPackages(dist, installed);
  const info = JSON.parse(go('version', '-m', '-json', server));
  if (
    info.Path !== 'aigongzuotai/cmd/server' ||
    go('env', 'GOVERSION') !== info.GoVersion
  )
    throw Error('Expected a studio server built with the active Go toolchain');
  const packages = [],
    relationships = [];
  const relate = (from, type, to) =>
    relationships.push({
      spdxElementId: from,
      relationshipType: type,
      relatedSpdxElement: to,
    });
  const serverPkg = pkg(
    'aigongzuotai/cmd/server',
    info.Main.Version || '(devel)',
    'server',
    {
      checksums: sha(hash(await readFile(server))),
      comment: 'Compiled workbench server.',
    },
  );
  const webPkg = pkg(lock.name, lock.version, 'workbench', {
    checksums: sha(bundle.digest),
    sourceInfo:
      'Production dist tree SHA256 over sorted name NUL file-SHA256 NUL, ' +
      bundle.count +
      ' files. npm lockfile SHA256 ' +
      hash(lockBytes),
  });
  for (const item of [serverPkg, webPkg]) {
    packages.push(item);
    relate('SPDXRef-DOCUMENT', 'DESCRIBES', item.SPDXID);
  }
  const goDir = go('env', 'GOMODCACHE');
  const std = pkg(
    'Go standard library and runtime',
    info.GoVersion,
    'go-runtime',
    licenseMetadata(await localLicense(go('env', 'GOROOT'))),
  );
  packages.push(std);
  relate(serverPkg.SPDXID, 'DEPENDS_ON', std.SPDXID);
  const sums = new Set(
    (await readFile(join(repo, 'go.sum'), 'utf8')).split(/\r?\n/),
  );
  for (const dep of info.Deps || []) {
    if (dep.Replace || !sums.has([dep.Path, dep.Version, dep.Sum].join(' ')))
      throw Error(
        'Go binary dependency differs from locked checksums: ' + dep.Path,
      );
    const escapeGo = (s) => s.replace(/[A-Z]/g, (v) => '!' + v.toLowerCase());
    const dir = join(goDir, escapeGo(dep.Path) + '@' + escapeGo(dep.Version));
    const item = pkg(
      dep.Path,
      dep.Version,
      'go:' + dep.Path + '@' + dep.Version,
      {
        ...licenseMetadata(await localLicense(dir)),
        sourceInfo:
          'Go build information and go.sum module checksum ' + dep.Sum,
      },
    );
    packages.push(item);
    relate(serverPkg.SPDXID, 'DEPENDS_ON', item.SPDXID);
  }
  const locked = Object.entries(lock.packages).filter(
    ([path, entry]) => path && !entry.dev,
  );
  const resolved = new Map();
  for (const [path, entry] of locked) {
    if (
      !path.startsWith('node_modules/') ||
      path.split('/').some((x) => x === '..' || x === '.')
    )
      throw Error('Unsupported npm package location');
    const directory = join(web, path);
    const manifest = JSON.parse(
      await readFile(join(directory, 'package.json'), 'utf8'),
    );
    if (manifest.version !== entry.version || !entry.integrity)
      throw Error('Installed npm version differs from the lockfile');
    if (
      entry.license &&
      typeof manifest.license === 'string' &&
      entry.license !== manifest.license
    )
      throw Error(
        'Installed license declaration differs from the lockfile: ' +
          manifest.name,
      );
    const item = pkg(
      manifest.name,
      entry.version,
      'npm:' + path + '@' + entry.version,
      {
        ...licenseMetadata(
          await localLicense(directory),
          typeof manifest.license === 'string'
            ? manifest.license
            : entry.license,
        ),
        sourceInfo:
          'Production npm resolution. Lockfile archive integrity ' +
          entry.integrity,
        comment: bundle.observed.has(path)
          ? 'An emitted chunk contains module IDs from this installed package.'
          : 'Present in the locked production dependency closure. No separate module ID from this package was observed in emitted chunks.',
      },
    );
    resolved.set(path, item);
    packages.push(item);
  }
  for (const path of bundle.observed)
    if (!resolved.has(path))
      throw Error(
        'Emitted package absent from the production lock closure: ' + path,
      );
  for (const [path, entry] of [['', lock.packages['']], ...locked]) {
    const parent = path ? resolved.get(path) : webPkg;
    for (const name of Object.keys({
      ...entry.dependencies,
      ...entry.optionalDependencies,
    })) {
      let parentPath = path,
        child;
      while (true) {
        child = resolved.get(
          (parentPath ? parentPath + '/' : '') + 'node_modules/' + name,
        );
        if (child || !parentPath) break;
        const pos = parentPath.lastIndexOf('/node_modules/');
        parentPath = pos < 0 ? '' : parentPath.slice(0, pos);
      }
      if (!child && entry.optionalDependencies?.[name]) continue;
      if (!child) throw Error('Unresolved production npm dependency: ' + name);
      relate(parent.SPDXID, 'DEPENDS_ON', child.SPDXID);
    }
  }
  return {
    spdxVersion: 'SPDX-2.3',
    dataLicense: 'CC0-1.0',
    SPDXID: 'SPDXRef-DOCUMENT',
    name: 'AIGongZuotai local server and workbench',
    documentNamespace: 'urn:uuid:' + randomUUID(),
    creationInfo: {
      created: new Date().toISOString().replace(/\.\d{3}Z$/, 'Z'),
      creators: ['Tool: AIGongZuotai-workbench-sbom-1'],
    },
    comment:
      'Local server and web artifacts only. External gateway, database, object service, optional media codecs and deployment images require separate inventories. License evidence records installed declarations and files. Distribution clearance is an independent review.',
    packages,
    relationships,
  };
}

const { values } = parseArgs({
  options: {
    server: { type: 'string' },
    dist: { type: 'string' },
    output: { type: 'string' },
  },
});
if (!values.server || !values.dist || !values.output)
  throw Error('Specify --server, --dist and --output');
const report = await inventory(resolve(values.server), resolve(values.dist));
await writeFile(
  resolve(values.output),
  JSON.stringify(report, null, 2) + '\n',
  { flag: 'wx' },
);
console.log(
  'Inventoried ' +
    report.packages.length +
    ' packages and ' +
    report.relationships.length +
    ' relationships.',
);
