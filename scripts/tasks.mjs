import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const source = 'docs/planning/tasks.json';
const target = 'docs/planning/TODO.md';
const labels = { todo: '待开始', in_progress: '进行中', blocked: '待外部条件', done: '已验收' };
const clean = value => String(value).replaceAll('|', '\\|').replaceAll('\n', ' ');
const link = (file, title = file) => `[${clean(title)}](${file})`;
export function validatePlan(plan, exists = file => fs.existsSync(path.resolve(repository, file))) {
  const errors = [];
  const fail = message => errors.push(message);
  const nonempty = value => typeof value === 'string' && value.trim().length > 0;
  const relative = file => nonempty(file) && !file.includes('\\') && !path.posix.isAbsolute(file) && !/^[A-Za-z]:/.test(file) && !file.split('/').some(part => part === '..' || part === '.') && !file.includes('#');
  const evidenceFile = (file, context) => { if (!relative(file) || !exists(file)) fail(`${context}：证据文件不存在或路径无效：${file}`); };
  if (plan?.schema_version !== 1 || !/^\d{4}-\d{2}-\d{2}$/.test(plan?.updated_on || '')) fail('任务源版本或更新日期无效');
  if (!Array.isArray(plan?.modules) || !plan.modules.length || !Array.isArray(plan?.batches) || !plan.batches.length) return [...errors, '缺少模块或批次'];
  const moduleIDs = new Set();
  for (const module of plan.modules) {
    if (!/^H\d{2}$/.test(module.id) || moduleIDs.has(module.id)) fail(`模块编号无效或重复：${module.id}`);
    moduleIDs.add(module.id);
    if (!nonempty(module.title) || !nonempty(module.scope)) fail(`${module.id}缺少标题或职责`);
  }
  const batches = new Map();
  for (const batch of plan.batches) {
    if (!/^V\d{2}$/.test(batch.id) || batches.has(batch.id)) fail(`批次编号无效或重复：${batch.id}`);
    batches.set(batch.id, batch);
    if (!Object.hasOwn(labels, batch.status)) fail(`${batch.id}状态无效`);
    if (!nonempty(batch.title) || !nonempty(batch.scope)) fail(`${batch.id}缺少标题或交付范围`);
    if (!Array.isArray(batch.modules) || !batch.modules.length || batch.modules.some(id => !moduleIDs.has(id))) fail(`${batch.id}模块映射无效`);
    if (!Array.isArray(batch.depends_on) || new Set(batch.depends_on).size !== batch.depends_on.length) fail(`${batch.id}依赖无效或重复`);
    if (!Array.isArray(batch.acceptance) || !batch.acceptance.length || batch.acceptance.some(value => !nonempty(value))) fail(`${batch.id}缺少验收标准`);
    for (const field of ['checks', 'evidence', 'ledgers']) if (!Array.isArray(batch[field])) fail(`${batch.id}.${field}必须为数组`);
    for (const check of batch.checks || []) if (!nonempty(check.label) || !nonempty(check.command) || !nonempty(check.note) || !['passed', 'pending', 'blocked', 'failed'].includes(check.status)) fail(`${batch.id}验证记录无效`);
    for (const item of batch.evidence || []) { if (!nonempty(item.note)) fail(`${batch.id}证据缺少说明`); evidenceFile(item.path, batch.id); }
    for (const file of batch.ledgers || []) { if (!/^docs\/changes\/\d{4}-\d{2}-\d{2}-\d{3}-[a-z0-9-]+\.md$/.test(file)) fail(`${batch.id}台账路径格式无效`); evidenceFile(file, batch.id); }
    if (batch.status === 'blocked' && !nonempty(batch.blocker)) fail(`${batch.id}待外部条件必须写明缺少什么及恢复方式`);
    if (batch.status === 'done') {
      if (!/^\d{4}-\d{2}-\d{2}$/.test(batch.completed_on || '')) fail(`${batch.id}完成必须记录日期`);
      if (!batch.checks?.length || batch.checks.some(check => check.status !== 'passed')) fail(`${batch.id}尚未全部验证通过，不得标完成`);
      if (!batch.evidence?.length || !batch.ledgers?.length) fail(`${batch.id}完成必须有文件证据和台账`);
      if (batch.blocker) fail(`${batch.id}仍有阻碍，不得标完成`);
    } else if (batch.completed_on !== null) fail(`${batch.id}未完成不得设置完成日期`);
  }
  const visiting = new Set(), visited = new Set();
  function visit(id) {
    if (visiting.has(id)) { fail(`批次依赖成环：${id}`); return; }
    if (visited.has(id)) return;
    const batch = batches.get(id); if (!batch) return;
    visiting.add(id);
    for (const dependency of batch.depends_on || []) {
      if (!batches.has(dependency)) { fail(`${id}引用未知依赖${dependency}`); continue; }
      if (batch.status === 'done' && batches.get(dependency).status !== 'done') fail(`${id}依赖${dependency}尚未完成`);
      visit(dependency);
    }
    visiting.delete(id); visited.add(id);
  }
  for (const id of batches.keys()) visit(id);
  return errors;
}

export function renderPlan(plan) {
  const link = (file, title = file) => `[${clean(title)}](${path.posix.relative(path.posix.dirname(target), file)})`;
  const done = plan.batches.filter(batch => batch.status === 'done').length;
  const lines = ['# 开发 TODO · 按模块横切、按业务纵切', '', `更新时间：${plan.updated_on}。已验收 **${done}/${plan.batches.length} 个批次**。这是批次数量，不是工时或整体产品完成百分比；各批工作量不同。`, '',
    `本文件由 ${link(source)} 生成，请修改唯一任务源后运行 \`npm run todo:sync\`。状态与勾选不能脱离验收证据；规则见 ${link('AGENTS.md')} 和 ${link('docs/planning/README.md', '任务维护说明')}。`, '',
    '**横切**表示长期负责的技术/产品模块，**纵切**表示用户能实际完成的一条业务链及其完整验收。横向模块包含后续批次时，不能因某个基础能力完成就把整个模块打勾。', '', '## 横向模块', '',
    '| 模块 | 职责 | 已验收/相关批次 | 批次 |', '| --- | --- | --- | --- |'];
  for (const module of plan.modules) {
    const related = plan.batches.filter(batch => batch.modules.includes(module.id));
    lines.push(`| ${module.id} ${clean(module.title)} | ${clean(module.scope)} | ${related.filter(batch => batch.status === 'done').length}/${related.length} | ${related.map(batch => batch.id).join('、')} |`);
  }
  lines.push('', '## 纵向批次队列', '');
  for (const batch of plan.batches) lines.push(`- [${batch.status === 'done' ? 'x' : ' '}] **${batch.id} ${batch.title}** — ${labels[batch.status]}；依赖：${batch.depends_on.join('、') || '无'}${batch.completed_on ? `；完成：${batch.completed_on}` : ''}`);
  lines.push('', '## 批次范围与验收', '');
  for (const batch of plan.batches) {
    lines.push(`### ${batch.id} ${batch.title}`, '', `状态：**${labels[batch.status]}**。模块：${batch.modules.join('、')}。依赖：${batch.depends_on.join('、') || '无'}。`, '', batch.scope, '', '验收条件：', '');
    batch.acceptance.forEach(value => lines.push(`- ${value}`));
    if (batch.blocker) lines.push('', `外部条件：${batch.blocker}`);
    if (batch.checks.length) { lines.push('', '验证记录：', ''); for (const check of batch.checks) lines.push(`- ${check.status === 'passed' ? '通过' : check.status === 'failed' ? '失败' : check.status === 'blocked' ? '待条件' : '待验证'}：${check.label}；\`${check.command}\`。${check.note}`); }
    if (batch.evidence.length) lines.push('', '证据：' + batch.evidence.map(item => link(item.path, item.note)).join(' · '));
    if (batch.ledgers.length) lines.push('', '台账：' + batch.ledgers.map(file => link(file, path.basename(file, '.md'))).join(' · '));
    lines.push('');
  }
  return lines.join('\n');
}

export function renderEntry(plan) {
  const done = plan.batches.filter(batch => batch.status === 'done').length;
  return ['# 开发 TODO · 导航', '',
    `更新时间：${plan.updated_on}。已验收 **${done}/${plan.batches.length} 个批次**，不代表整体完成百分比。`, '',
    `完整范围、依赖、验收与证据统一在 ${link(target, '开发 TODO')}。唯一状态源为 ${link(source)}；运行 \`npm run todo:sync\` 同步本入口与完整清单，不直接勾选。`, '',
    ...plan.batches.map(batch => `- [${batch.status === 'done' ? 'x' : ' '}] **${batch.id} ${batch.title}** — ${labels[batch.status]}`), '',
  ].join('\n');
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const plan = JSON.parse(fs.readFileSync(path.join(repository, source), 'utf8'));
    const errors = validatePlan(plan);
    if (errors.length) throw new Error(errors.join('\n'));
    const outputs = [[target, renderPlan(plan)], ['TODO.md', renderEntry(plan)]];
    if (process.argv[2] === '--write') { for (const [file, generated] of outputs) fs.writeFileSync(path.join(repository, file), generated); console.log(`[todo] 已生成 ${outputs.map(([file]) => file).join('、')}`); }
    else {
      for (const [file, generated] of outputs) {
        if (!fs.existsSync(path.join(repository, file)) || fs.readFileSync(path.join(repository, file), 'utf8') !== generated) throw new Error(`${file}与任务源不一致，请运行npm run todo:sync；不要直接勾选生成文件。`);
      }
      console.log(`[todo] 通过：${plan.modules.length}模块、${plan.batches.length}批次、${plan.batches.filter(batch => batch.status === 'done').length}批已验收；依赖与完成证据有效。`);
    }
  } catch (error) { console.error(`[todo] ${error.message}`); process.exitCode = 1; }
}
