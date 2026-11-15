# 2026-11-14-076：V16-A3.2 首次审核持久分派核心

## 目的

将首次媒体审核从 query/delivery 执行步骤独立出来，同时保留旧候选不得自动重审的恢复边界。持久区分尚未启动和已启动审核，避免只是更换 goroutine。

## 范围

关联 V16-A3.2、H03/H07。新019、四通道领取、READY/STARTED/FINISHED、首次候选交接、恢复/取消/背压和整组结果。当前实施中，A3.1 的075成绩不代替本批验证。共享引擎许可由077实现，维护/独立验收由078实现，根配置/观测与联合收口由079负责。

## 实际文件

- `db/migrations/019_media_review_dispatch.sql`：首次派发私有表、版本约束与不可变触发器，001—018保持原字节。
- `internal/studio/review_dispatch.go`、`review_dispatch_store.go`、`review_dispatch_execution.go`：私有类型/完整候选摘要、同事务首次派发、一次STARTED、当前lease执行/取消及原决定保存背压。
- `internal/studio/review_dispatch_receipts.go`：审核前私有缩略图完整字节校验，独立于生成provider。
- `internal/studio/review_dispatch_test.go`、`review_dispatch_commit_test.go`、`review_dispatch_receipts_test.go`：时间身份/有界期限、真实ACK/实际回滚/最终背压、READY完整收据与缩略图篡改。
- `internal/studio/app.go`、`media_execution_lanes.go`、`task_leases.go`、`task_leases_worker.go`：第四预算/worker/PG连接、共享许可先于claim、四通道互斥、活动审核取消。
- `internal/studio/media_moderation_execution.go`：首次候选READY交接；原件、完整AssetInfo与缩略图核验后才调用持有许可的审核。
- `internal/studio/persistence.go`、`ledger_hooks.go`、`repository_queries.go`、`operations_cases.go`：私有加载与恢复、lease/B2/派发/权益/Outbox原子保存、提交确认后复制缓存。
- `internal/studio/media_delivery_store.go`：仅仍需Import的READY交付要求phased生成provider；全部收据已齐且有019派发时可独立审核，原密钥门禁保留。
- `internal/studio/media_execution_lanes_test.go`、`media_moderation_execution_test.go`、`media_lane_integration_test.go`：保留旧断言并显式拆READY与review步骤；慢调用和取消槽位断言归独立review预算。
- `internal/studio/media_delivery_upgrade_test.go`：仍用确切001—017建旧库并保持原字节/候选/幂等等断言，当前升级期望改为完整连续embed版本集合；075历史事实不改。
- `docs/wiki/media-review-dispatch.md` 与本台账。

## 接口与数据影响

新增 `MediaScheduling.ReviewWorkers` 默认2、范围1—2及固定第四lane；新私有表不含来源URL、正文或密钥。新候选与READY同事务，STARTED确认提交后才审核，旧候选不补READY。公开Task继续MODERATING/PENDING，无新增浏览器凭据或重试操作。

首次READY必须带当前任务lease且同事务首次插入候选，STARTED必须带有效lease/预期版本/一次attempt；FINISHED与B2、018、权益、资产和Outbox同事务。STARTED实际回滚可继续首次审核，已提交ACK丢失保守隔离；最终背压不重审。阶段变更递增Task.Version，GC摘要相应失效。

完整收据比较包括Delivery标记、时间、缩略图身份及S3 locator/version；相同原件SHA但manifest改变不能批准，缩略图实际字节也检查。review不需要生成provider，关闭/不兼容审核只隔离。整组期限按件数乘以读取2分钟和审核60秒，加30秒保存余量，最大12分30秒；单件周期也有3分钟上限。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run '^TestNonexistentA32Compile$'` | 通过，0.033s | 首轮新类型和四lane接线编译，未执行测试。 |
| `go test ./internal/studio -run '^(TestMediaReviewDispatchTimestampIdentityAndTimeout\|TestMediaExecutionConfigurationAndSeparateBudgets)$'` | 通过，0.037s | 四预算边界、同瞬间不同时区等价、真实微秒改变拒绝、整组有界期限；随后篡改夹具调整编译短专项通过0.030s。 |
| 078首次PG交叉验收 | 失败，23.693s，后修复 | STARTED后终态与取消误报409：对PG timestamptz的Location做reflect结构比较。改为严格同一瞬间Time.Equal，attempt/version仍必须匹配；078/079保留原失败与复验。 |
| `node .runtime/v16a32-check.mjs target '^(TestPostgresMediaModerationWholeBatchReleaseAndBilling\|TestPostgresMediaModerationPendingFenceCancelAndRestart\|TestPostgresMediaLaneReviewHoldsOwnLaneUntilCancelledResultReturns)$'` | 通过，8.835s | 3函数/10子例，图/视频allow/block/review及PARTIAL权益，取消/失租/重启不晚放行，真实HTTP延迟清理保留review槽。PG窗口通知出现交错，079登记协调事实；其后PG全部由根统一执行，最终串行全量收口。 |
| 完整收据交叉审阅 | 发现并修复 | 原B2只比原件和部分定位字段，新READY间隙可能改变Delivery/CreatedAt/Thumbnail而被允许。已改完整身份比较和缩略图实际字节校验；新增5项故障分支交根执行。 |
| 新篡改测试首轮 | 失败，夹具已修 | `newDeliveryHTTPFixture(t,0)`误阻塞slot0，五子例尚未到READY就触发原5秒截止；改为-1不阻塞，未修改截止或产品代码。复验由根执行。 |
| 旧017升级兼容测试编辑编译 | 首次失败，已修复 | 替换格式断言时遗留多余右括号，测试未运行；删除后编译短专项0.041s通过。属于新批测试修改，不改075历史记录。 |
| 根统一执行新ACK/STARTED回滚与local联备组 | 通过，16.763s，中间证据 | READY/STARTED/ALLOWED三处真实COMMIT ACK，STARTED实际回滚与旧候选不得补READY；同组local READY/STARTED真实dump/restore通过，S3该轮明确skip。此成绩在完整收据修正前，最终全量必须重新覆盖。 |
| 根执行`TestPostgresMediaReviewDispatchFinalBackpressureNeverRechecks`及旧017升级 | 通过，分别1.97s、1.51s | 实际HTTP决定返回后最终事务被Outbox回滚，review与引擎许可保留，清除背压只保存原决定，HTTP仍仅1次；升级保持全部旧态/字节/幂等断言并验证完整当前版本集合。子用例耗时由根回报。 |
| 根复验`TestPostgresMediaReviewDispatchRejectsReadyReceiptTampering` | 通过，5子例7.46s | 去Delivery标记、改manifest时间、改thumbnail身份、改thumbnail字节和缺失thumbnail均REVIEW、零审核HTTP、零公开/注册；READY后关闭生成provider仍可执行私有校验。保留首轮slot0误配失败。 |
| 根019 READY/STARTED local及真实S3恢复 | 通过，11.55s | S3原件/缩略图4/4真实VersionId保持；READY首次审核、STARTED隔离，未重POST/Query/下载。根同组018local/S3恢复8.77s（2/2版本）、实际图视频S3审核1.99s通过。 |
| 上述S3组合首轮 | 失败，整体30.086s | 唯一失败为旧真实S3测试假设DeleteObject会删除冻结版本；versioned bucket实际只创建delete marker。根保留版本可读断言并精确删除夹具自建版本后复验通过，详情079；没有修改本模块生产逻辑掩盖失败。 |
| `go vet ./internal/studio` | 通过 | 当前冻结核心及测试静态检查exit0，无诊断。 |
| `npm run check:docs`、所属文件`git diff --check` | 通过 | 172份文档、79份台账，本地链接/必需字段与空白检查通过。 |
| 全量/治理 | 待集成 | 不沿用075结果，最终以079为准。 |

## 风险与限制

完整V16仍进行中；渠道公平、许可饥饿与降级属于A3.3，持久token流和生产容量另验。真实审核商仍未选型。默认关闭的审核与旧候选隔离规则保留，STARTED确认不明不会自动重审。

## 回滚方式

019升级后使用匹配二进制或恢复同一联备；联备PG、私有目录/owner、交付及已启用队列密钥。禁止删除迁移、候选或派发记录强退018，旧018写者必须拒绝019。

## 文档同步

- 根负责README、索引、任务源及TODO，避免并行覆盖。
- 新增[首次审核分派](../wiki/media-review-dispatch.md)，记录交接/恢复、共享许可、完整收据、有界期限和019联备边界；契约为`docs/planning/v16a32-contract.md`。
- [共享许可](../wiki/media-review-permits.md)由077负责，独立验证与维护由078负责，联合证据由079汇总，V16仍进行中。

联合收口补记：根最终check、全PG368.160s（实际版本化S3及联备启用）、六smoke、构建和文档治理通过，详见079。本文中间失败/待集成交接事实保留，完整V16不标完成。
