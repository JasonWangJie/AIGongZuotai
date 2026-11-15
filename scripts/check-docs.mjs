#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const repositoryRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const requiredDocs = [
  'AGENTS.md',
  'README.md',
  'TODO.md',
  'docs/README.md',
  'docs/planning/TODO.md',
  'docs/wiki/repository-layout.md',
  'docs/planning/README.md',
  'docs/planning/tasks.json',
  'docs/wiki/README.md',
  'docs/wiki/architecture.md',
  'docs/wiki/development.md',
  'docs/wiki/roadmap.md',
  'docs/wiki/backend.md',
  'docs/wiki/frontend.md',
  'docs/wiki/decisions/0001-phase-one.md',
  'docs/changes/README.md',
  'docs/changes/TEMPLATE.md',
  'docs/changes/2026-11-13-001-foundation.md',
];
export const ledgerFields = [
  '目的', '范围', '实际文件', '接口与数据影响', '验证结果', '风险与限制', '回滚方式', '文档同步',
];
const ledgerNamePattern = /^\d{4}-\d{2}-\d{2}-\d{3}-[a-z0-9]+(?:-[a-z0-9]+)*\.md$/;
const normalizePath = value => value.replaceAll('\\', '/');

// Remove code examples and comments before examining actual document links.
function withoutCode(markdown) {
  let fence = null;
  return markdown.replace(/<!--[\s\S]*?-->/g, '').split(/\r?\n/).map(line => {
    const marker = /^ {0,3}(`{3,}|~{3,})/.exec(line);
    if (fence) {
      if (marker && marker[1][0] === fence.character && marker[1].length >= fence.length
        && line.slice(marker[0].length).trim() === '') fence = null;
      return '';
    }
    if (marker) {
      fence = { character: marker[1][0], length: marker[1].length };
      return '';
    }
    return line.replace(/(`+)(.*?)\1/g, '');
  }).join('\n');
}

function destinationAt(text, start) {
  let cursor = start;
  while (cursor < text.length && /\s/.test(text[cursor])) cursor += 1;
  if (text[cursor] === '<') {
    const end = text.indexOf('>', cursor + 1);
    if (end < 0 || text.slice(cursor, end).includes('\n')) return null;
    return text.slice(cursor + 1, end);
  }
  let destination = '';
  let depth = 0;
  while (cursor < text.length) {
    const character = text[cursor];
    if (character === '\\' && cursor + 1 < text.length) {
      destination += text[cursor + 1];
      cursor += 2;
      continue;
    }
    if (/\s/.test(character)) break;
    if (character === '(') depth += 1;
    if (character === ')') {
      if (depth === 0) break;
      depth -= 1;
    }
    destination += character;
    cursor += 1;
  }
  return destination || null;
}

export function extractLocalLinks(markdown) {
  const text = withoutCode(markdown);
  const destinations = [];
  const inline = /!?\[[^\]\n]*\]\(/g;
  for (const match of text.matchAll(inline)) {
    const value = destinationAt(text, match.index + match[0].length);
    if (value) destinations.push(value);
  }
  const references = /^ {0,3}\[[^\]\n]+\]:\s*/gm;
  for (const match of text.matchAll(references)) {
    const value = destinationAt(text, match.index + match[0].length);
    if (value) destinations.push(value);
  }
  return destinations.filter(value => !value.startsWith('#') && !value.startsWith('//')
    && !/^[a-z][a-z\d+.-]*:/i.test(value));
}

export function validateLedgerContent(markdown) {
  const sections = new Map();
  let active = null;
  for (const line of markdown.replace(/<!--[\s\S]*?-->/g, '').split(/\r?\n/)) {
    const heading = /^##\s+(.+?)\s*#*\s*$/.exec(line);
    if (heading) {
      active = heading[1];
      if (!sections.has(active)) sections.set(active, '');
    } else if (active) sections.set(active, `${sections.get(active)}\n${line}`);
  }
  return ledgerFields.flatMap(field => {
    if (!sections.has(field)) return [`缺少章节：${field}`];
    if (!sections.get(field).trim()) return [`章节为空：${field}`];
    return [];
  });
}

export function evaluateChangedPaths(changedPaths) {
  const files = [...new Set(changedPaths.map(normalizePath))];
  if (files.length === 0) return [];
  const missing = [];
  if (!files.includes('README.md')) missing.push('根 README.md');
  if (!files.includes('docs/planning/tasks.json')) missing.push('任务源 docs/planning/tasks.json');
  if (!files.includes('TODO.md')) missing.push('生成的 TODO.md');
  if (!files.includes('docs/planning/TODO.md')) missing.push('生成的完整 docs/planning/TODO.md');
  if (!files.some(file => file.startsWith('docs/wiki/') && file.endsWith('.md'))) {
    missing.push('至少一份 docs/wiki/*.md');
  }
  if (!files.some(file => file.startsWith('docs/changes/')
    && ledgerNamePattern.test(file.slice('docs/changes/'.length)))) {
    missing.push('至少一份编号变更台账（不是索引或模板）');
  }
  return missing.map(item => `变更集合缺少同步项：${item}`);
}

function listMarkdown(directory) {
  if (!fs.existsSync(directory)) return [];
  return fs.readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const absolute = path.join(directory, entry.name);
    if (entry.isDirectory()) return listMarkdown(absolute);
    return entry.isFile() && entry.name.endsWith('.md') ? [absolute] : [];
  });
}

function linkTarget(root, sourceFile, destination) {
  const localPath = decodeURIComponent(destination.split(/[?#]/, 1)[0]);
  if (!localPath) return null;
  return path.resolve(localPath.startsWith('/') ? root : path.dirname(sourceFile),
    localPath.startsWith('/') ? `.${localPath}` : localPath);
}

function git(root, args) {
  try {
    return execFileSync('git', args, { cwd: root, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
  } catch (error) {
    const detail = String(error.stderr || error.message).trim();
    throw new Error(`Git 检查失败（${args[0]}）：${detail}`);
  }
}

function changedFiles(root, options) {
  const parse = output => output.split('\0').filter(Boolean);
  if (options.base !== undefined) {
    git(root, ['rev-parse', '--verify', '--end-of-options', `${options.base}^{commit}`]);
    git(root, ['rev-parse', '--verify', 'HEAD^{commit}']);
    return parse(git(root, ['diff', '--name-only', '-z', '--diff-filter=ACDMRTUXB', `${options.base}...HEAD`, '--']));
  }
  if (options.changed) {
    return [...new Set([
      ...parse(git(root, ['diff', '--name-only', '-z', '--diff-filter=ACDMRTUXB', '--'])),
      ...parse(git(root, ['diff', '--cached', '--name-only', '-z', '--diff-filter=ACDMRTUXB', '--'])),
      ...parse(git(root, ['ls-files', '--others', '--exclude-standard', '-z'])),
    ])];
  }
  return null;
}

// Source-controlled documentation has one home; these three root discovery
// entrypoints and the application's HTML entry are intentional exceptions.
export function validateRepositoryLayout(files) {
  const entries = new Set(['README.md', 'AGENTS.md', 'TODO.md']);
  return [...new Set(files.map(normalizePath))].flatMap(file => {
    if (/\.sql$/i.test(file) && !file.startsWith('db/')) return [`SQL 必须位于 db/：${file}`];
    if (/\.(?:md|markdown|mdx|rst|adoc)$/i.test(file) && !file.startsWith('docs/') && !entries.has(file)) return [`项目文档必须位于 docs/：${file}`];
    if (/\.html$/i.test(file) && !file.startsWith('docs/') && file !== 'apps/workbench/index.html') return [`报告必须位于 docs/：${file}`];
    return [];
  });
}

export function checkDocumentation(root = repositoryRoot, options = {}) {
  const errors = [];
  for (const file of requiredDocs) {
    if (!fs.existsSync(path.join(root, file)) || !fs.statSync(path.join(root, file)).isFile()) {
      errors.push(`缺少必需文档：${file}`);
    }
  }
  const files = [path.join(root, 'README.md'), path.join(root, 'AGENTS.md'), path.join(root, 'TODO.md'),
    ...listMarkdown(path.join(root, 'docs'))]
    .filter(file => fs.existsSync(file) && fs.statSync(file).isFile());
  const linksByFile = new Map();
  for (const file of files) {
    const relative = normalizePath(path.relative(root, file));
    const targets = [];
    for (const destination of extractLocalLinks(fs.readFileSync(file, 'utf8'))) {
      try {
        const target = linkTarget(root, file, destination);
        if (!target) continue;
        targets.push(target);
        const targetRelative = path.relative(root, target);
        if (targetRelative === '..' || targetRelative.startsWith(`..${path.sep}`) || path.isAbsolute(targetRelative)) {
          errors.push(`${relative}：本地链接超出仓库：${destination}`);
        } else if (!fs.existsSync(target)) errors.push(`${relative}：本地链接不存在：${destination}`);
      } catch (error) {
        errors.push(`${relative}：无法解析链接 ${destination}（${error.message}）`);
      }
    }
    linksByFile.set(file, targets);
  }
  const ledgerDirectory = path.join(root, 'docs/changes');
  const allLedgerDocuments = listMarkdown(ledgerDirectory);
  const ledgerFiles = allLedgerDocuments.filter(file => ledgerNamePattern.test(path.basename(file)));
  for (const file of allLedgerDocuments) {
    if (!['README.md', 'TEMPLATE.md'].includes(path.basename(file)) && !ledgerNamePattern.test(path.basename(file))) {
      errors.push(`台账文件名不符合 YYYY-MM-DD-NNN-short-name.md：${normalizePath(path.relative(root, file))}`);
    }
  }
  const indexed = new Set(linksByFile.get(path.join(ledgerDirectory, 'README.md')) || []);
  for (const file of ledgerFiles) {
    const relative = normalizePath(path.relative(root, file));
    for (const problem of validateLedgerContent(fs.readFileSync(file, 'utf8'))) errors.push(`${relative}：${problem}`);
    if (!indexed.has(file)) errors.push(`台账索引未覆盖：${relative}`);
  }
  const ledgerIds = new Map();
  for (const file of ledgerFiles) {
    const id = path.basename(file).slice(0, 14);
    if (ledgerIds.has(id)) errors.push(`台账日期与编号重复：${path.basename(file)} / ${ledgerIds.get(id)}`);
    ledgerIds.set(id, path.basename(file));
  }
  let changes = null;
  try {
    const sourceFiles = git(root, ['ls-files', '--cached', '--others', '--exclude-standard', '-z']).split('\0')
      .filter(file => file && fs.existsSync(path.join(root, file)) && fs.statSync(path.join(root, file)).isFile());
    errors.push(...validateRepositoryLayout(sourceFiles));
    changes = changedFiles(root, options);
    if (changes !== null) errors.push(...evaluateChangedPaths(changes));
  } catch (error) {
    errors.push(error.message);
  }
  return { errors, documents: files.length, ledgers: ledgerFiles.length, changes };
}

function readArguments(args) {
  const options = {};
  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index];
    if (argument === '--changed') options.changed = true;
    else if (argument === '--base') {
      if (!args[index + 1] || args[index + 1].startsWith('--')) throw new Error('--base 需要一个 Git 基线引用');
      options.base = args[++index];
    } else if (argument === '--help' || argument === '-h') options.help = true;
    else throw new Error(`未知参数：${argument}`);
  }
  if (options.changed && options.base !== undefined) throw new Error('--changed 与 --base 不能同时使用');
  return options;
}

function main() {
  try {
    const options = readArguments(process.argv.slice(2));
    if (options.help) {
      console.log('用法：node scripts/check-docs.mjs [--changed | --base <git-ref>]\n默认检查基础文档、本地链接、台账字段与索引。\n--changed：额外检查暂存、工作区与未跟踪变更的 README/Wiki/台账同步。\n--base：额外检查 base...HEAD 的已提交变更；需要存在的基线与 HEAD。');
      return;
    }
    const result = checkDocumentation(repositoryRoot, options);
    if (result.errors.length) {
      console.error(`[docs] 检查失败：${result.errors.length} 项`);
      for (const error of result.errors) console.error(`- ${error}`);
      process.exitCode = 1;
      return;
    }
    console.log(`[docs] 通过：${result.documents} 份文档，${result.ledgers} 份台账；本地链接与必需字段有效。`);
    if (result.changes !== null) console.log(`[docs] 变更集合检查通过：${result.changes.length} 个路径（${options.base !== undefined ? `基线 ${options.base}...HEAD` : '暂存 / 工作区 / 未跟踪'}）。`);
    console.log('[docs] 此检查不能替代对变更语义及验证证据的人工审阅。');
  } catch (error) {
    console.error(`[docs] ${error.message}`);
    process.exitCode = 1;
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main();
