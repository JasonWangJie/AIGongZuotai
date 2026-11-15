# 2026-11-14-063：文档归档与双入口TODO治理

## 目的

落实用户“项目文档统一放docs、SQL放db、按TODO持续开发”的要求，减少散落说明与过时阶段摘要。保留可发现的根入口，不复制任务状态源。

## 范围

V02/H09已交付治理的增量。三份HTML方案、三份样本说明和完整TODO集中到docs；SQL独立实现与验证见064。本变更不扩大已验收业务范围。

## 实际文件

- `deliverables/*.html` → `docs/deliverables/*.html`（三份原始方案，字节不变）。
- `internal/studio/testdata/{references,media,moderation}/README.md` → `docs/testing/*-fixtures.md`（修正相对链接并补原样本路径）。
- `scripts/tasks.mjs`、`scripts/tasks.test.mjs`、`scripts/check-docs.mjs`：完整清单与根导航同步、全docs链接扫描和源文件目录检查。
- `docs/README.md`、`docs/wiki/repository-layout.md`、`docs/planning/progress.md`及生成的`docs/planning/TODO.md`。
- 根`README.md`、`AGENTS.md`、`TODO.md`，Wiki导航/development/architecture/roadmap/performance/decisions/0001及涉及SQL和样本的说明导航，`docs/planning/README.md`、`tasks.json`、台账索引。
- 010历史台账仅修复SQL链接目标，原实际文件标签与历史验收事实保留。

## 接口与数据影响

无业务API或数据库数据变化。完整TODO迁入docs/planning，根TODO由同一源生成简短导航；两个输出均检查漂移。根README/AGENTS/TODO作为发现入口保留，其他项目说明放docs，SQL放db。源文件目录检查基于Git已跟踪/未忽略文件，不读取忽略的本地运行配置。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| PowerShell Move-Item与Get-FileHash | 通过 | 验证绝对路径在仓库内；三份HTML前后SHA256相同 |
| `npm run todo:sync; npm run test:todo; npm run check:todo; npm run check:changes` | 通过 | 10项治理测试，完整清单/根入口同步，全部docs链接及源目录检查通过；最终状态同步后再复核 |
| `node .runtime/v16-check.mjs check` / `db` | 通过 | 根实际联合check：Go普通8.821s、301前端、构建1.79s；真实PG全量238.022s。隔离环境等价npm run check/test:db，详见067 |

三份HTML校验：建设方案`c41ec3164b84accbbfe58d66326e1b128402b1d3cd8cd8602e7b7fa10c950bcc`；交互蓝图`1507154065981e139d2f738c4d7a9e631fd0acfb3abe48b53951d447ec8ea070`；Go方案`8155ec151cd79f9a0f2c13811a99beb7111a4fd6fdf75e8929f30baf7e6dd353`。

## 风险与限制

外部旧文件书签需更新；目录检查不验证远程链接或报告中的历史信息时效。V14-C1本地已验收，C2/C3、真实渠道和整个产品未完成；本次不重演历史浏览器验收。

## 回滚方式

把六份文档移回原路径并同时恢复引用与生成脚本；保留任务源的新验收事实和本台账，不删除历史记录。SQL回退见064，禁止修改迁移登记或owner。

## 文档同步

README新增统一文档入口、目录与063/064链接；Wiki新增目录规则并修复当前链接；AGENTS更新当前C1状态和目录约束；V02追加本次真实验证，TODO双文件重新生成；台账索引保留059—062历史并纠正062残留实施中字样。

最终补验：根重新按064记录计算17SQL的SHA256/长度，17/17、55486字节全部相符；三个HTML局部href/src目标也均存在。首次TODO同步早于SQL代理移动005而报告缺证据，SQL落地后重新生成通过；保留该短暂并行未就绪事实。没有增加或删改SQL版本。
