# 244 · 040 扩展媒体原任务查询并发

## 目的

V17（H03/H07/H08）总体设计按 60,000 个在途任务和每 10 秒一次查询估算 6,000 次/秒。242 已测量 32 个 GET 执行位共享四条 PG 连接。本次解决 32 个进程内查询位及同一冻结渠道最多 32 个并发的硬限制，并限制空闲轮询成本。

## 范围

同一控制写者下的媒体 query 执行、默认配置的兼容、每工作区和每冻结渠道独立上限、数据库格式与离线工具栅栏。既有工作区环形游标、四条执行队列、原 handle 及权益、审核、交付继续按各自规则执行。

## 实际文件

- `.env.example`、`scripts/lib/process.mjs`、`cmd/server/media_execution_config.go` 与同包 `media_execution_config_test.go`、`media_query_capacity_config_test.go` 增加查询配置并隔离烟测进程。
- `internal/studio/app.go`、`media_execution_lanes.go`、`media_http.go`、`task_leases_worker.go` 接入有界原 GET 派发。 `media_scheduler.go`、`media_schedule_select.go`、`media_schedule_memory.go`、`media_schedule_claim.go` 使用按队列生效的预算。
- 同目录 `task_leases.go`、`task_query_pool.go` 在外发前核对交接后的投影、渠道、探测及 242 租约。`media_lane_observability.go`、`media_transport_lanes_test.go`、`media_query_dispatch_test.go` 提供进程聚合计数和边界回归。
- `db/migrations/040_media_query_capacity.sql`、`internal/studio/storage_maintenance.go` 更新格式，维护、账本、运营、订单对账与 PG 归属资产迁移共同使用连续版本栅栏。
- `docs/wiki/media-original-query-capacity.md`、`docs/wiki/database-migrations.md`、`docs/wiki/decisions/0040-original-query-capacity.md` 记录运行与回退条件。

## 接口与数据影响

`MEDIA_QUERY_WORKERS` 可显式设到 1,536。33 以上必须使用 PostgreSQL 和 1—32 条查询数据库连接。新增 `MEDIA_QUERY_WORKSPACE_CONCURRENCY` 与 `MEDIA_QUERY_CHANNEL_CONCURRENCY`，0 继承原设置，正数范围 1—1,536。其他队列的范围及默认沿用原值。040 将 `studio_media_channel_runtime.effective_limit` 的原约束改为 query 最高 1,536，其他队列最高 32。升级仅进行约束替换及校验，旧渠道的额度、冷却、探测及版本保持原值。

高并发档只有一个正常预选派发者。每项持有执行位与工作区、真实渠道预算至实际退出，已领取任务直接交给执行，没有积压队列。交接后首次原 GET 按数据库时钟复核原任务、投影和渠道。公开指标 `studio_media_query_dispatch_attempts_total` 只累计进程派发尝试。

## 验证结果

- Go 1.26.11 隔离 overlay 下，`go test -count=1 -v ./cmd/server ./internal/studio -run '^(TestLargeQuery|TestPostgresLargeOriginalQuerySockets040|TestPostgresLargeQueryHandoffFences040|TestMediaExecutionEnvironmentStrictAndIndependent|TestMediaExecutionConfigurationAndSeparateBudgets|TestMediaQueryDBConnectionEnvironment|TestPooledQuery|TestMediaTransport|TestMediaScheduler|TestMediaLane)'` 通过 20 个顶层测试，包括 040 嵌入迁移与维护栅栏、独立预算、旧公平及渠道退避、运输层取消和重定向。未设置 `TEST_DATABASE_URL`，两个 040 PostgreSQL 入口跳过。
- 同一 overlay 的 `go build ./cmd/...`、`go vet ./cmd/... ./internal/studio` 与隔离脚本 `node --check scripts/lib/process.mjs` 通过。
- 待集成人配置真实隔离 PG 后串行执行 `go test -v -count=1 -timeout=150s ./internal/studio -run '^TestPostgresLarge.*040$'`。首项核对 260 个冻结原 handle、256 个真实同时等待的 GET、四条可借出的独立查询连接、每项唯一终态和空队列派发次数。第二项核对选择后冷却、领取后降额或冷却以及唯一匹配的半开探测。

### 追加同机超额到期对照

`internal/studio/media_query_curve_test.go` 为独立显式测量入口。三档顺序使用 32、64、128 个查询位，固定四条租约连接，每档在独立 schema 登记 512 个唯一原任务，以每秒 320 个到期、上游每项 200 毫秒交付终态投放。三档共用一个回环 HTTP 服务地址与冻结渠道身份，工作区和渠道都配置 128，实际预算取与查询位数的较小值。32 档走原 worker，64 和 128 档走 040 派发。原 241/242 样本与入口沿用原口径。

按真实写出时间和对应到期时间重建待写积压。每档另记录峰值 HTTP、10 毫秒采样的执行位和作用域占位、到期至实际写出分位数、服务耗时、唯一 GET 和完成率、排空时间、同一宿主运行时及 PG 的格式/提交设置。生产页 SQL 实际读取的工作区和任务数在到期前核验，加载期间报告累计检查数与每次领取检查数。

隔离 overlay 的 `go test -count=1 -v ./internal/studio -run '^(TestQueryCurveBacklog040|TestPostgresOriginalQueryRateCurve040)$'` 通过积压重建回归，`go vet ./internal/studio` 通过。未配置测量开关或本机数据库时按条件跳过。集成人以 `STUDIO_MEDIA_QUERY_CURVE=1` 和既有本机 `TEST_DATABASE_URL` 执行 `go test -count=1 -v -timeout=5m ./internal/studio -run '^TestPostgresOriginalQueryRateCurve040$'`，实测后补录聚合。

## 风险与限制

040 替换约束需表锁与已有渠道的约束校验。大档仍有单公平选择器、固定 SQL 池及控制写事务，吞吐取决于真实到期分布、数据库、宿主和供应商。新增并发配置不代表 6,000 次/秒。跨进程配额、60,000 在途及 专用环境 24 小时稳态仍按 V17 原条件验证。

## 回滚方式

当前 040 程序可以停机时恢复低档配置并重启。退回 039 程序需恢复配套停写联备，包括数据库、私有文件及 owner、对应对象版本、已启用密钥。维护、账本与其他离线工具须和恢复格式匹配。

## 文档同步

模块 Wiki、数据库 Wiki 和 040 决策随本变更交付。根 README、Wiki 导航、台账索引、任务源、生成 TODO 与推进记录由集成人结合真实 PG 结果同步。039 到 040 的联备恢复由 245 专项记录。

### 交付前修正 · 饱和工作区后的公平派发

当公平选择器已提交游标、但选中的工作区或渠道已经占满时，大档派发者在本轮继续查看后续候选，单轮最多选择八次，再次遇到本轮第一个被拒任务即休眠。执行位满、无可借 SQL 连接或没有候选时直接等待下次定时或完成通知。涉及 `internal/studio/task_leases_worker.go` 和 `internal/studio/media_query_fair_scan_test.go`。

专项在独立 PG 放入两工作区四个到期原 handle。A 的首个实际 GET 挂起，派发者先遇到 A 的第二项；四秒轮询下 B 的首个 GET 需在观察到 A 满额后 1.33 秒内抵达本机服务，两工作区各自只占一位。释放后四项均须各自完成一次；测试分别计数整个工作区轮转都满、以及空队列时的SQL派发尝试。验收命令为 `go test -p 1 -v -count=1 -timeout 2m ./internal/studio -run '^TestPostgresLargeQueryScopeFairScan040$'`，需隔离回环 `TEST_DATABASE_URL`。隔离 overlay 做普通编译，未提供 PG 时按条件跳过；真实 PG 由集成人串行核实。

## 集成人 PostgreSQL 结果

在 PG 16.14 上逐一运行 `TestPostgresLargeOriginalQuerySockets040`、`TestPostgresLargeQueryHandoffFences040`、`TestPostgresLargeQueryScopeFairScan040` 和 `TestPostgresOriginalQueryRateCurve040`。260 个已登记原任务的门闩用例达到 256 条同时等待的 HTTP GET，四个 SQL 连接均可借出，用例 3.85 秒；冷却、领取后降额和半开探测用例 2.30 秒。加入修正前，工作区 A 占满时 B 的 GET 未能在 1.33 秒内到达。修正后四次原 GET 全部唯一，双工作区占满阶段扫描 5 次，空队列观察期只扫 1 次，用例 16.40 秒。首次失败与修正均使用同一四秒轮询周期。

200 毫秒服务、每秒 320 项到期、四条 SQL 连接的独立三档各含 512 个原 handle，均完整执行且零 POST。配置 32/64/128 查询位时，真实 HTTP 峰值为 32/64/88，整体完成速率 129.27/149.61/159.41 次每秒，到期至写出 P95 为 2035.52/1542.91/1317.95 毫秒，P99 为 2140.85/1570.25/1334.86 毫秒。三档实测均未达到该投放率下的 P95 500 毫秒目标，详见[聚合报告](../wiki/assets/media-query-rate-curve-040.json)。

在 `docs/wiki/media-transport-lanes.md`、`media-execution-lanes.md` 与 `scheduler.md` 标明 V16 旧上限，并链接 040 当前配置及分档报告。
