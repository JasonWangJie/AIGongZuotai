# 2026-11-14-072：V16-A3.1 媒体持久私有交付核心

## 目的

原 Query worker 同步等待下载、导入与审核，慢文件会挤占原任务查询；崩溃后也没有独立保存结果集合的阶段。新增加密不可变清单、逐件完成收据和第三执行通道，让恢复只继续原交付，避免查询更换结果或重复生成。

## 范围

关联 V16-A3.1、H03/H07。覆盖内存/PG 分阶段执行、租约控制事务、私有格式 018、来源加密、收据、取消/运营缓存、B2 衔接与核心故障验收。适配器与存储 receipt 由 073 负责，维护/格式工具由 074 负责，配置观测、实际 App 联合测试及全量收口由 075 负责。本批不完成 A3.2/A3.3、持久 token 流、生产容量或完整 V16。

## 实际文件

- `internal/studio/media_delivery.go`：独立密钥、AES-GCM、冻结描述验证、私有记录及复制。
- `internal/studio/media_delivery_store.go`：私有记录读取、不可变/版本校验、同事务保存、启动密钥与适配器门禁。
- `internal/studio/media_delivery_execution.go`：查询交接、逐件收据检查点、PG 保存背压与内存 attempt/version 边界。
- `internal/studio/media_delivery_core_test.go`：加密/配置、真实 COMMIT ACK 四边界、事务原子与 lease/不可变回归。
- `internal/studio/media_delivery_upgrade_test.go`：同一 embedFS 的 001—017 实际建库、原 handle/已隔离候选/未知提交旧态、018 加载器升级及 017 旧写者拒绝；不删除迁移登记模拟旧库。
- `internal/studio/app.go`、`media_execution_lanes.go`：配置归一、第三 worker、内存分阶段、取消确认后的私有缓存同步。
- `internal/studio/media_execution_lanes_test.go`：保留旧两通道断言并补第三通道默认/边界/预算独立验证。
- `internal/studio/task_leases.go`、`task_leases_worker.go`：第三连接池、按已登记交付领取、lease 内持久交接及迟到写入隔离。
- `internal/studio/persistence.go`、`ledger_hooks.go`、`repository_queries.go`、`operations_cases.go`：恢复和私有读取、同事务保存钩子、nil 状态保留及运营缓存。
- `db/migrations/018_media_delivery.sql`：交付与 item 表、完整收据单次写入和 header 不可变触发器。
- `docs/wiki/media-delivery.md` 与本台账。

## 接口与数据影响

新增 `Config.MediaDelivery.EncryptionKey` 和 `MediaScheduling.DeliveryWorkers`（默认 2，1—16）。PG 分阶段适配器自动启用、必须独立 32 字节标准 base64 密钥；已有交付记录关闭供应商也不能绕过原密钥。内存 Async 自动走同阶段，可用进程临时密钥但无恢复承诺。旧非分阶段 demo 不伪称支持本批。

三 lane 分别 submit/query/delivery；预算覆盖整步实际返回。公开 Task 不增加来源、密文或收据字段。018 私有表冻结加密描述与计划 ID，状态 READY/COMPLETE/PARKED、私有版本独立；完整 `AssetInfo` 只能登记一次。PG 已有收据但完成标记缺失直接 REVIEW，不重新下载。B2 候选恢复不可重审，只有整组允许后的终态事务注册资产并结算。

业务保存顺序为任务 lease fence、私有交付、B2、资源库/权益、Outbox。旧 private=nil 保存加载原记录；取消/运营提交确认后才同步私有缓存。COMMIT 确认不明不更新缓存或通知，重启读取数据库事实。原受理幂等保持优先，不新增生成重发。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run '^TestNonexistentA3Compile$'` | 通过，0.033s | 初次核心接线编译；未执行测试。 |
| `go test ./internal/studio -run '^TestMediaDelivery(Cipher\|Encryption)'` | 通过，0.038s；新增启动前门禁后被下项覆盖 | 两函数覆盖 AAD 七类变更、可变进度不破坏密文、四件长 URL、超限与规范 base64、独立凭据；缺失/非法 key 在数据库迁移前拒绝，非 PG。 |
| `node .runtime/v16-check.mjs target '^TestPostgresMediaDeliveryUnknownCommitPreservesFourBoundaries$'` | 通过，5.970s | 1 函数/4 子例，真实 COMMIT 成功后 TCP 丢 ACK：manifest、receipt、candidate、final；缓存/SSE不提前更新，重启只认原已提交事实，原键不外发。独占隔离 55433 测试实例。 |
| `node .runtime/v16-check.mjs target '^TestPostgresMediaDeliveryAtomicFencesAndImmutableEvidence$'` | 通过，1.268s | 真实 Outbox 满回滚清单/收据，未提交时零计划/资产文件，恢复仅 Query 原 handle 冻结新单次回应（零 Submit/Import）；旧 lease、nil 私有保留、取消 PARKED 缓存/迟结果及 SQL 不可变验证通过。 |
| `node .runtime/v16-check.mjs target '^TestPostgresMediaDeliveryUpgradeSeventeenPreservesLegacyEvidence$'` | 通过，1.364s | 从相同 embedFS 的 001—017 建真实旧库，当前加载器升018；任务状态、input/handle/submission/body、幂等、政策/候选规范 JSON 逐字节相同，真实 PNG 保留，018 两表空；旧017写者拒绝，App 原 handle 仅1次Query/0Submit，旧隔离候选 HTTP 404、未知提交不执行。未删迁移登记或修改旧 SQL；独占隔离55433实例。 |
| `go test ./internal/studio -run '^(TestRun\|TestTask\|TestConversation\|TestBounded\|TestChat\|TestModeration\|TestMedia\|TestAdmission\|TestCancel\|TestBilling\|TestCatalog\|TestReference\|TestAsset)' -count=1` | 通过，7.623s | 核心新普通测试、三预算边界与既有相关普通回归；未选择 TestPostgres，不冒充 PG 验收。 |
| `go vet ./internal/studio` | 通过 | 最终冻结源码静态检查，无诊断，进程 exit0。 |
| 所属文件 `git diff --check` | 通过 | 无空白错误；LF/CRLF 提示不影响结果。 |
| `npm run check:docs` 首轮 | 失败，3 项 | 根集成索引尚未覆盖 072/073/074；没有缺失标准章节或模块链接错误。已交根同步索引，保留首次事实。 |
| `npm run check:docs` 索引整合后复验 | 通过 | 163 份文档、75 份台账，本地链接与必需字段有效；保留首次索引失败事实。 |
| 全量 PG | 待集成 | 不使用 A2 或其他代理旧成绩代替本次验证；最终 [075](2026-11-14-075-media-delivery-integration.md) 记录联合证据。 |

## 风险与限制

交付仍包含首次同步审核，独立审核分派属于 A3.2；渠道公平、退避、自适应和积压索引测量属于 A3.3。单控制写者、私有目录锁与既有恢复限制保留。018 有 READY 索引但未作大样本 EXPLAIN，不宣称扫描成本隔离。来源 URL 可能带签名，始终仅存加密清单；丢失交付密钥无法恢复该清单。真实模型/审核商未验收，fixture 不代表生产容量。

## 回滚方式

受控停机并联备数据库、ASSET_DIR、REFERENCE_DIR、交付密钥和其他已启用队列密钥。格式已到 018 时必须使用理解该格式的匹配二进制或恢复同一联备，不能只回退代码至 017，不能清迁移/owner/候选/收据。已登记交付不再 Query 换批，未知提交不再次 POST；不可变收据的存储 locator 不允许由旧 S3 迁移工具改写。

## 文档同步

- README、Wiki 导航、台账索引、任务源/TODO由根统一同步至 075，本子项不改他人归属。
- 新增[媒体持久私有交付](../wiki/media-delivery.md)，记录配置、数据、恢复、GC、联备与后续边界。
- 与 073/074/075 的适配器、维护与联合验收交叉登记；V16 不标整批完成。

联合收口补记：根最终全PG301.345s通过，包含本页全部新增PG函数及升级；Go vet、构建和docs亦通过。模块最初“待集成”的交接事实保留，最终结果和限制见075。
