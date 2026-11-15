# 目录与文档归属

2026-11-14按用户持续开发要求整理目录。项目文档放在 `docs/`，SQL 放在 `db/`。这次迁移不变更数据库格式、接口或业务行为。

| 内容 | 当前路径 | 说明 |
| --- | --- | --- |
| 总体方案与离线蓝图 | `docs/deliverables/` | 从根deliverables移动，三个HTML字节及SHA256不变 |
| 任务状态源 | `docs/planning/tasks.json` | 唯一可编辑状态源 |
| 完整任务清单 | `docs/planning/TODO.md` | 含范围、依赖、验证、证据，由任务源生成 |
| 持续推进记录 | `docs/planning/progress.md` | 每轮记录进度和阻塞，不替代任务源 |
| 测试样本说明 | `docs/testing/*-fixtures.md` | 说明集中，样本仍在internal/studio/testdata供测试读取 |
| SQL与嵌入入口 | `db/migrations/`、`db/migrations.go` | 随Go二进制嵌入；运行时不从当前目录加载SQL |
| Wiki、ADR、验收与台账 | `docs/wiki/`、`docs/changes/` | 既有历史验收事实保留 |

根目录保留 `README.md`（项目发现与启动）、`AGENTS.md`（工具和协作者自动发现规则）及 `TODO.md`（生成的简短进度导航）。这是三个有明确用途的入口；详细新文档必须进入docs。应用的 `apps/workbench/index.html` 是程序入口，不是报告。

旧台账中的“实际文件”路径代表变更当时的位置，不能改写成新路径冒充历史；导航链接改指当前文件。SQL统一迁移见[064](../changes/2026-11-14-064-sql-layout.md)，文档与治理迁移见[063](../changes/2026-11-14-063-document-layout.md)。外部旧书签需改用新路径，不保留重复报告副本。

`npm run todo:sync` 同步完整清单和根入口。`npm run check:todo` 同时检查两份生成结果。`npm run check:docs` 扫描整个docs的Markdown链接，并检查Git已跟踪/未忽略的源文件目录；已删除文件不参与，忽略的运行日志、依赖、构建产物不参与。检查不读取真实凭据，不遍历本地数据库。

## 回退

目录变更可逆，但应同时恢复引用、嵌入入口和治理脚本，不单独换回某一文件。已有数据库版本登记及私有目录owner不需要调整，不得删除。后续新增SQL必须放db并接受原有严格迁移检查；旧二进制仍不能绕格式栅栏写入新数据库。
