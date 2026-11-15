import test from 'node:test';
import assert from 'node:assert/strict';
import { validatePlan, renderPlan, renderEntry } from './tasks.mjs';
import { evaluateChangedPaths, validateRepositoryLayout, extractLocalLinks } from './check-docs.mjs';
import path from 'node:path';
const ledger = 'docs/changes/2026-11-13-009-batch-tracking.md';
function fixture() { return { schema_version: 1, updated_on: '2026-11-13', modules: [{ id: 'H01', title: '身份', scope: '身份隔离' }], batches: [{ id: 'V00', title: '测试批次', status: 'done', modules: ['H01'], depends_on: [], scope: '事务测试', acceptance: ['隔离通过'], checks: [{ label: '验证', command: 'test', status: 'passed', note: 'fixture' }], evidence: [{ path: 'README.md', note: '结果' }], ledgers: [ledger], completed_on: '2026-11-13', blocker: null }] }; }
const exists = file => ['README.md', ledger].includes(file);
test('completed batch requires actual evidence, passed checks and a ledger', () => { const plan = fixture(); assert.deepEqual(validatePlan(plan, exists), []); plan.batches[0].checks[0].status = 'pending'; assert.match(validatePlan(plan, exists).join(), /不得标完成/); plan.batches[0].checks[0].status = 'passed'; plan.batches[0].evidence = []; assert.match(validatePlan(plan, exists).join(), /文件证据/); });
test('missing evidence and outside-workspace evidence fail', () => { const plan = fixture(); plan.batches[0].evidence[0].path = '../outside.md'; assert.match(validatePlan(plan, exists).join(), /路径无效/); plan.batches[0].evidence[0].path = 'missing.md'; assert.match(validatePlan(plan, exists).join(), /不存在/); });
test('unknown module, dependency and duplicate ids fail', () => { const plan = fixture(); plan.batches[0].modules = ['H99']; plan.batches[0].depends_on = ['V99']; plan.batches.push(structuredClone(plan.batches[0])); const errors = validatePlan(plan, exists).join(); assert.match(errors, /模块映射/); assert.match(errors, /未知依赖/); assert.match(errors, /重复/); });
test('cycles and unfinished dependencies cannot be accepted', () => { const plan = fixture(); const second = structuredClone(plan.batches[0]); second.id = 'V01'; second.status = 'in_progress'; second.completed_on = null; second.depends_on = ['V00']; plan.batches.push(second); plan.batches[0].depends_on = ['V01']; const errors = validatePlan(plan, exists).join(); assert.match(errors, /成环/); assert.match(errors, /尚未完成/); });
test('blocking conditions and completion dates stay truthful', () => { const plan = fixture(); const batch = plan.batches[0]; batch.status = 'blocked'; assert.match(validatePlan(plan, exists).join(), /外部条件/); batch.blocker = '需要供应配置'; batch.completed_on = null; assert.deepEqual(validatePlan(plan, exists), []); batch.status = 'done'; batch.completed_on = '2026-11-13'; assert.match(validatePlan(plan, exists).join(), /仍有阻碍/); });
test('generated checkboxes follow evidence-backed state deterministically', () => { const plan = fixture(); const first = renderPlan(plan); assert.equal(first, renderPlan(plan)); assert.match(first, /- \[x\] \*\*V00/); plan.batches[0].status = 'in_progress'; assert.match(renderPlan(plan), /- \[ \] \*\*V00/); assert.doesNotMatch(renderPlan(plan), /- \[x\]/); });
test('a code change needs task-source and both generated TODOs synchronized', () => { const files = ['internal/studio/app.go', 'README.md', 'docs/wiki/backend.md', ledger]; assert.match(evaluateChangedPaths(files).join(), /tasks.json/); assert.match(evaluateChangedPaths(files).join(), /TODO.md/); assert.match(evaluateChangedPaths([...files, 'docs/planning/tasks.json', 'TODO.md']).join(), /完整/); assert.deepEqual(evaluateChangedPaths([...files, 'docs/planning/tasks.json', 'TODO.md', 'docs/planning/TODO.md']), []); });
test('an empty change set creates no false documentation requirement', () => assert.deepEqual(evaluateChangedPaths([]), []));

test('canonical TODO preserves evidence targets after moving under docs', () => {
  const plan = fixture();
  const targets = extractLocalLinks(renderPlan(plan)).map(link => path.posix.normalize(path.posix.join('docs/planning', link)));
  assert.ok(targets.includes('docs/planning/tasks.json'));
  assert.ok(targets.includes('README.md'));
  assert.ok(targets.includes(ledger));
  assert.ok(targets.includes('AGENTS.md'));
  const entry = renderEntry(plan);
  assert.match(entry, /\(docs\/planning\/TODO.md\)/);
  assert.match(entry, /- \[x\] \*\*V00/);
  plan.batches[0].status = 'blocked';
  assert.match(renderEntry(plan), /- \[ \] \*\*V00/);
});

test('documentation and SQL layout rejects regressions but retains entrypoints', () => {
  assert.deepEqual(validateRepositoryLayout(['README.md', 'AGENTS.md', 'TODO.md', 'docs/a.md', 'docs/a.html', 'db/migrations/001.sql', 'apps/workbench/index.html', 'internal/testdata/a.png']), []);
  const errors = validateRepositoryLayout(['deliverables/plan.html', 'internal/testdata/README.md', 'internal\\studio\\migrations\\002.sql']);
  assert.equal(errors.length, 3);
  assert.match(errors.join(), /db\//);
  assert.match(errors.join(), /docs\//);
});
