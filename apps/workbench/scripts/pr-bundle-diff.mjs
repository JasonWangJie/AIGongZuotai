import { createHash } from 'node:crypto';
import { appendFile, lstat, readFile, readdir } from 'node:fs/promises';
import { dirname, join, posix, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import { gzipSync, gunzipSync } from 'node:zlib';
import { JSDOM } from 'jsdom';
import ts from 'typescript';

export const startupBudget = 300 * 1024;
const maxFile = 8 << 20;
const filePattern = /^([a-zA-Z0-9_.-]{1,112})-([a-zA-Z0-9_-]{8})\.js$/;
const sha = (bytes) => createHash('sha256').update(bytes).digest('hex');
const profilePath = fileURLToPath(
  new URL('../../../scripts/perf/workbench-startup-profile.json', import.meta.url),
);

class BundleError extends Error {}
function requireBundle(value, explanation) {
  if (!value) throw new BundleError(explanation);
}

async function boundedFile(path, limit) {
  const stat = await lstat(path);
  requireBundle(
    stat.isFile() && stat.size > 0 && stat.size <= limit,
    '文件类型或大小不符合制品约束',
  );
  const raw = await readFile(path);
  requireBundle(raw.length === stat.size, '读取期间制品发生变化');
  return raw;
}

function publicScript(from, specifier) {
  requireBundle(
    typeof specifier === 'string' &&
      /^(?:\.{1,2}\/|\/?assets\/)[A-Za-z0-9_./-]+\.js$/.test(specifier),
    '模块路径不在本地公开制品内',
  );
  const name = specifier.startsWith('/')
    ? posix.normalize(specifier.slice(1))
    : posix.join(posix.dirname(from), specifier);
  requireBundle(
    posix.dirname(name) === 'assets' && filePattern.test(posix.basename(name)),
    '无法识别此公开 JS 的构建指纹',
  );
  return posix.basename(name);
}

function startupTags(html) {
  // The fragment is detached. Parsing never loads or executes bundled JS.
  const doc = JSDOM.fragment(html);
  requireBundle(!doc.querySelector('base[href]'), '暂不支持自定义 HTML 基址');
  const roots = new Set();
  for (const tag of doc.querySelectorAll('script,link')) {
    if (tag.localName === 'script') {
      const type = (tag.getAttribute('type') || '').trim().toLowerCase();
      if (type && !['module', 'text/javascript', 'application/javascript'].includes(type)) continue;
      const src = tag.getAttribute('src');
      requireBundle(src || !tag.textContent.trim(), '启动页包含未纳入预算的内联脚本');
      if (src && !tag.hasAttribute('nomodule')) roots.add(publicScript('index.html', src));
    } else {
      const rel = (tag.getAttribute('rel') || '').toLowerCase().split(/\s+/);
      if (
        rel.includes('modulepreload') ||
        (rel.includes('preload') && (tag.getAttribute('as') || '').toLowerCase() === 'script')
      ) {
        roots.add(publicScript('index.html', tag.getAttribute('href')));
      }
    }
  }
  requireBundle(roots.size > 0, '启动页缺少可识别的 JS');
  return roots;
}

function importer(bytes, name) {
  const tree = ts.createSourceFile(
    name,
    bytes.toString('utf8'),
    ts.ScriptTarget.Latest,
    false,
    ts.ScriptKind.JS,
  );
  requireBundle(!tree.parseDiagnostics.length, '无法解析公开 JavaScript');
  const deps = [];
  for (const statement of tree.statements) {
    if (!ts.isImportDeclaration(statement) && !ts.isExportDeclaration(statement)) continue;
    if (statement.moduleSpecifier) {
      requireBundle(ts.isStringLiteral(statement.moduleSpecifier), '无法识别静态导入路径');
      deps.push(publicScript('assets/' + name, statement.moduleSpecifier.text));
    }
  }
  return deps;
}

function closure(start, files) {
  const result = new Set(),
    queue = [...start];
  while (queue.length) {
    const name = queue.pop();
    if (result.has(name)) continue;
    requireBundle(files.has(name), '静态导入指向缺失制品');
    result.add(name);
    for (const dep of files.get(name).dependencies) queue.push(dep);
  }
  return result;
}

export async function readStartupProfile(path = profilePath) {
  const profile = JSON.parse(await boundedFile(path, 8192));
  requireBundle(
    profile.schema === 1 &&
      /^[a-zA-Z0-9_./-]+\.md$/.test(profile.evidence) &&
      !profile.evidence.includes('..') &&
      profile.evidence.startsWith('docs/') &&
      Array.isArray(profile.observed_conditional_chunks) &&
      profile.observed_conditional_chunks.length <= 32 &&
      profile.observed_conditional_chunks.every(
        (n) => typeof n === 'string' && /^[A-Za-z0-9_.-]{1,112}$/.test(n),
      ) &&
      new Set(profile.observed_conditional_chunks).size ===
        profile.observed_conditional_chunks.length,
    '无效的认证首页观测清单',
  );
  return profile;
}

export async function inventory(dist, profile, { historical = false } = {}) {
  requireBundle(
    (await lstat(dist)).isDirectory() && (await lstat(join(dist, 'assets'))).isDirectory(),
    '公开制品目录不可用',
  );
  const html = await boundedFile(join(dist, 'index.html'), 1 << 20);
  const roots = startupTags(html.toString('utf8'));
  const scripts = [];
  const directories = [''];
  let fileCount = 0;
  while (directories.length) {
    const subdir = directories.pop();
    for (const entry of await readdir(join(dist, subdir), { withFileTypes: true })) {
      requireBundle(++fileCount <= 4096, '公开文件清单超限');
      requireBundle(entry.isDirectory() || entry.isFile(), '公开目录包含未核验的链接');
      if (entry.isDirectory()) directories.push(posix.join(subdir, entry.name));
      else if (/\.(?:mjs|cjs|js)$/i.test(entry.name)) {
        requireBundle(
          subdir === 'assets' && filePattern.test(entry.name),
          '发现未纳入清单的公开 JS',
        );
        scripts.push(entry);
      }
    }
  }
  scripts.sort((a, b) => a.name.localeCompare(b.name, 'en'));
  requireBundle(scripts.length > 0 && scripts.length <= 256, '公开 JS 数目超限或为空');
  const files = new Map();
  let totalBytes = 0;
  for (const entry of scripts) {
    const match = filePattern.exec(entry.name);
    requireBundle(entry.isFile() && match, '不支持的公开 JS 名称或链接');
    const path = join(dist, 'assets', entry.name),
      raw = await boundedFile(path, maxFile);
    totalBytes += raw.length;
    requireBundle(totalBytes <= 64 << 20, '制品 JS 总体积超限');
    let compressed,
      source = 'sidecar';
    try {
      compressed = await boundedFile(path + '.gz', maxFile + 65536);
      requireBundle(
        gunzipSync(compressed, { maxOutputLength: raw.length }).equals(raw),
        'gzip 侧件与原始 JS 不一致',
      );
    } catch (e) {
      if (e.code !== 'ENOENT' || !historical) throw e;
      compressed = gzipSync(raw, { level: 6 });
      source = 'estimated';
    }
    files.set(entry.name, {
      name: entry.name,
      logical: match[1],
      hash: sha(raw),
      gzipHash: sha(compressed),
      bytes: raw.length,
      gzip: compressed.length,
      source,
      dependencies: importer(raw, entry.name),
    });
  }
  const statics = closure(roots, files);
  const observed = [],
    missing = [];
  for (const group of profile.observed_conditional_chunks) {
    const matched = [...files.values()].filter((f) => f.logical === group);
    if (!matched.length) missing.push(group);
    // Multiple emitted files sharing a stem are counted conservatively; the
    // dynamic profile is an observation, never an assertion about program flow.
    for (const file of matched) observed.push(file.name);
  }
  const included = closure([...statics, ...observed], files);
  for (const file of files.values())
    file.phase = statics.has(file.name)
      ? 'static'
      : included.has(file.name)
        ? 'observed'
        : 'remaining';
  const sum = (phase) =>
    [...files.values()].filter((f) => f.phase === phase).reduce((n, f) => n + f.gzip, 0);
  return {
    html: sha(html),
    entry: [...roots].sort(),
    files: [...files.values()],
    missing,
    static: sum('static'),
    observed: sum('observed'),
    remaining: sum('remaining'),
    startup: sum('static') + sum('observed'),
  };
}

const kib = (n) => (n / 1024).toFixed(2);
const amount = (n) => (n === undefined ? '—' : n + ' B (' + kib(n) + ' KiB)');
const delta = (n, b) =>
  b === undefined
    ? '—'
    : (n > b ? '+' : '') + String(n - b) + ' B (' + (n > b ? '+' : '') + kib(n - b) + ' KiB)';
const publicName = (s) => '`' + s + '`';
const scopes = { static: '静态启动', observed: '条件启动', remaining: '其余分包' };

export function renderDiff(current, base, meta, profile) {
  const lines = ['## 工作台 PR 制品差异', ''];
  lines.push(
    meta.headSHA
      ? `CI 合并结果 ${publicName(meta.headSHA)} 与 PR 基线 ${publicName(meta.baseSHA)} 对比。`
      : '本地现有公开制品的离线对比。',
  );
  if (!base) lines.push('', `基线不可比。${meta.baseReason}。差值留空。`);
  if (meta.currentLock) {
    let locks = `依赖锁 SHA-256：候选 ${publicName(meta.currentLock)}`;
    if (meta.baseLock)
      locks += `，基线 ${publicName(meta.baseLock)}（${meta.baseLock === meta.currentLock ? '相同' : '已改变'}）`;
    lines.push('', locks + '。');
  }
  lines.push('', '| gzip 正文 | 基线 | 候选 | 变化 |', '| --- | ---: | ---: | ---: |');
  for (const [name, key] of [
    ['HTML 指定脚本及静态依赖闭包', 'static'],
    ['已观测条件块及其额外静态依赖', 'observed'],
    ['去重启动集合', 'startup'],
    ['其余独立分包', 'remaining'],
  ]) {
    const previous = base?.[key],
      next = current[key];
    lines.push(`| ${name} | ${amount(previous)} | ${amount(next)} | ${delta(next, previous)} |`);
  }
  const overBudget = current.startup > startupBudget;
  const margin = amount(Math.abs(startupBudget - current.startup));
  lines.push(
    '',
    `**300 KiB 启动预算：${overBudget ? '超出' : '已识别集合在预算内'}**，${amount(current.startup)} / ${startupBudget} B。${overBudget ? '超出 ' + margin + '，本任务将失败。' : '余量 ' + margin + '。'}`,
    '',
    `静态闭包覆盖 HTML 脚本、modulepreload、静态 import 和 re-export。认证首页额外集合依据 H1 冷浏览器观测。运行时行为变化后需重测 H1 并更新观测清单。依据见 ${publicName(profile.evidence)}。`,
  );
  for (const [label, inv] of [
    ['候选', current],
    ['基线', base],
  ]) {
    if (!inv) continue;
    lines.push('', `${label} HTML SHA-256：${publicName(inv.html)}。入口和预加载根如下。`, '');
    for (const file of inv.files.filter((f) => inv.entry.includes(f.name))) {
      lines.push(
        `- ${publicName(file.name)}，原件 SHA-256 ${publicName(file.hash)}，gzip SHA-256 ${publicName(file.gzipHash)}${file.source === 'estimated' ? '（离线估算）' : ''}。`,
      );
    }
    if (inv.missing.length)
      lines.push(
        '',
        `${label}未匹配的观测逻辑名：${inv.missing.map(publicName).join('、')}。请复核改名、合并或重新测量。`,
      );
  }
  if (base?.files.some((f) => f.source === 'estimated'))
    lines.push(
      '',
      '基线缺少 .gz 的文件以相同 gzip level 6 离线估算，逐行标明。历史 HTTP 传输另需实测证据。',
    );
  const groups = new Map();
  for (const [key, inv] of [
    ['base', base],
    ['current', current],
  ]) {
    for (const file of inv?.files || []) {
      if (!groups.has(file.logical)) groups.set(file.logical, { base: [], current: [] });
      groups.get(file.logical)[key].push(file);
    }
  }
  lines.push(
    '',
    '| 逻辑块 | 基线文件、SHA-256 前缀、gzip 字节 | 候选文件、SHA-256 前缀、gzip 字节 | gzip 变化 |',
    '| --- | --- | --- | ---: |',
  );
  const describe = (items) =>
    items
      .map(
        (f) =>
          `${publicName(f.name)} ${publicName(f.hash.slice(0, 12))} · ${scopes[f.phase]} · ${f.gzip} B${f.source === 'estimated' ? '（估算）' : ''}`,
      )
      .join('<br>') || '—';
  const sum = (files) => files.reduce((n, f) => n + f.gzip, 0);
  for (const name of [...groups.keys()].sort()) {
    const row = groups.get(name);
    const change = delta(sum(row.current), base ? sum(row.base) : undefined);
    lines.push(
      `| ${publicName(name)} | ${describe(row.base)} | ${describe(row.current)} | ${change} |`,
    );
  }
  return lines.join('\n') + '\n';
}

async function main() {
  const { values } = parseArgs({
    options: {
      current: { type: 'string' },
      base: { type: 'string' },
      'base-status': { type: 'string' },
      'base-sha': { type: 'string' },
      'head-sha': { type: 'string' },
    },
  });
  requireBundle(
    values.current &&
      ['ready', 'checkout-unavailable', 'build-unavailable'].includes(values['base-status']) &&
      ((!values['base-sha'] && !values['head-sha'] && process.env.GITHUB_ACTIONS !== 'true') ||
        (/^[a-f0-9]{40}$/.test(values['base-sha']) && /^[a-f0-9]{40}$/.test(values['head-sha']))),
    '比较参数不符合 CI 契约',
  );
  const publish = async (s) => {
    if (process.env.GITHUB_STEP_SUMMARY) await appendFile(process.env.GITHUB_STEP_SUMMARY, s);
    else process.stdout.write(s);
  };
  try {
    const profile = await readStartupProfile(),
      current = await inventory(resolve(values.current), profile);
    const meta = {
      baseSHA: values['base-sha'],
      headSHA: values['head-sha'],
      currentLock: sha(
        await boundedFile(join(dirname(resolve(values.current)), 'package-lock.json'), 8 << 20),
      ),
    };
    let base;
    if (values['base-status'] === 'ready' && values.base) {
      try {
        const root = resolve(values.base);
        const lock = sha(await boundedFile(join(dirname(root), 'package-lock.json'), 8 << 20));
        base = await inventory(root, profile, { historical: true });
        meta.baseLock = lock;
      } catch {
        meta.baseReason = '历史制品或锁文件无法验证';
      }
    } else
      meta.baseReason =
        values['base-status'] === 'checkout-unavailable'
          ? '无法读取指定基线提交'
          : '基线依赖安装或构建未成功';
    await publish(renderDiff(current, base, meta, profile));
    if (current.startup > startupBudget) process.exitCode = 1;
  } catch (err) {
    await publish(
      '## 工作台 PR 制品差异\n\n候选制品无法核验。' +
        (err instanceof BundleError ? err.message : '构建文件、依赖锁或 gzip 侧件缺失或损坏') +
        '。本次预算未知，检查失败。\n',
    );
    process.exitCode = 1;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href)
  main().catch(() => {
    process.stderr.write('Invalid bundle comparison invocation.\n');
    process.exitCode = 1;
  });
