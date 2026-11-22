# 2026-11-22-135 项目演示文稿的不可变版本与原图引用

## 目的

让项目成员按版本保存可编辑的标题、正文、备注和图片页，在保存响应中断后能用原键核对。之后有人重命名、回收或隔离原图时，历史文字应能打开，原图的每次使用仍遵守项目当前权限。

## 范围

V21-C 横跨 H01 身份与权限、H02 项目与创作数据、H08 前端体验及 H10 工作流与专项创作。本记录覆盖 H01 的会话与项目权限核验、H02 的持久修订，以及 H10 演示文稿对原图的引用。H08 浏览器编辑与 OOXML 导出由同批前端记录说明。

## 实际文件

- `db/migrations/029_presentations.sql` 新增文稿头、版本、每页原图关联及保存回执。
- `internal/studio/presentation_store.go` 实现文稿校验、保存、原键核对、指定版读取、项目列表及历史列表。
- `internal/studio/presentation_store_assets.go` 实现项目内原件查证、冻结及读取时复核。
- `internal/studio/presentation_store_test.go` 覆盖正文大小、资源与身份边界、独立写连接和版本容量。
- `docs/changes/2026-11-22-135-presentation-store.md` 记录本变更。

## 接口与数据影响

- 新建使用 `expected_version=0`，编辑使用同一文稿 ID 和读取时的版本。事务复核本人会话、账户状态及项目，锁定项目后先查该身份和作用域的原键。相同输入返回原版本，异参冲突、CAS 失败或原图不可用均不增加版本。
- 文稿含 1–30 页，页 ID 唯一，主题可选 paper、midnight、mint，布局可选 title、content、image。文稿标题、页标题各限 120 个 Unicode 码点，每页正文 4,000、备注 2,000。服务端规范化后的输入文稿 JSON 限 256 KiB，超过时返回 413。数据库对 `document::text` 另留到 320 KiB，容纳冻结时加入的至多四枚 SHA-256 及 JSONB 文本格式空格。
- 每项目至多 64 份文稿，全库 4,096 份，每份 128 个版本。达到写入上限后仍可读取各版与准确的原键回执。每次新建在数据库锁内核对全库数量，列表与历史按稳定游标翻页，默认 20 项、最多 50 项。
- 一个图片页绑定一个项目图库 ID、保存时的元数据修订和实际原件 SHA-256。每稿最多四个图片页，重复来源仍占页额度。PNG/JPEG 每张至多 8 MiB，最大累计 32 MiB。参考图保存时检查原尺寸不超过 1,600 万像素，生成图的实际字节及尺寸由 HTTP 原件读取环节验证，四图实际像素合计至多 6,400 万。
- 每次读图或取得导出清单，都重核来源、项目、当前回收与审核状态及内容版本。生成图必须来自成功或部分成功任务的公开产物，引用登记须匹配真实原件。元数据改名不改同一原件的历史绑定。当前不可用时，历史返回原文字及 `asset_available=false`，需要整稿图片的导出拒绝。
- 029 对历史版本、原图关联与保存回执禁止更新和删除；头版本按一步递增。旧 001–028 迁移和计费、生成、审核业务数据保持原有格式。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run '^TestPresentation' -count=1 -v` | 通过，0.392s | 三项非 PG 测试；含多字节与转义正文恰好 256 KiB、超 1 字节拒绝，以及字段、页数和图数校验。 |
| `TestPostgresPresentationVersionsScopeAndFrozenAsset`、`TestPostgresPresentationIndependentWritersCASAndReceipts`、`TestPostgresPresentationCapacityStillReadsAndReplays` | 集成人串行通过，分别 2.23、2.10、2.22 秒 | 真实项目参考图、回收/恢复/摘要变动、并发写连接、128 版及原键核对均通过。 |
| `TestPostgresPresentationUpgrade028PreservesDraftAndQuote` | 集成人串行通过，2.54 秒 | 由 028 迁移文件集建库，原项目草稿、回执、报价、所属人、风险策略与既有权限的规范化 JSON 均与升级前一致。四张文稿表均空，旧 028 写者拒绝新格式。 |
| 集成专项日志 `.runtime/v21c-first-pg.log` | 组合通过，46.647 秒 | 同组包含原版本联备恢复、真实已审批生成图与受限账户、保存确认丢失、真实浏览器导出及维护格式栅栏。 |

## 风险与限制

采用有界版本数与项目容量，达到限额后可继续查阅旧稿。冻结关系保存身份、修订和摘要，恢复数据库时也需要原参考目录、生成文件或登记的私有对象版本。可用性依赖这些原件及其当前可见状态。演示文稿存储不调用生成或审核供应商。

## 回滚方式

停止新写入后保全当前数据库、`REFERENCE_DIR`、`ASSET_DIR`、登记的对象版本及配置密钥。若须退回 028，应还原升级前同一套联备，并使用与该格式匹配的程序和维护工具。保留 029 副本以便核对升级后新建的文稿及回执。文稿恢复用例见 `internal/studio/presentation_http_test.go` 中的 `TestPostgresPresentationBackupOldVersion`，本批联合验证由 138 记录。

## 文档同步

同批集成人统一更新根 README、Wiki、台账索引和任务进度，关联 V21-C 的存储、HTTP、前端以及集成验收记录。版本设计列在 [ADR 0029](../wiki/decisions/0029-project-presentations.md)，联合验证列在 [138](2026-11-22-138-presentation-integration.md)。整批状态以任务源和集成人实测为准。
