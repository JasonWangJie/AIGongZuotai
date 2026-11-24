# 242 · 媒体原任务查询的独立 PostgreSQL 连接预算

## 目的

V17（H03/H07/H08）的规划规模为 60,000 个媒体任务在途，按每 10 秒轮询约需 6,000 次查询/秒。241 在 16 个查询 worker、16 条查询租约连接、每秒 40 个到期请求下实测 37.77 次/秒。原实现即使正在等待 HTTP 也持有数据库连接。本次允许分别设置两种资源的预算。

## 范围

单控制写者、原 handle 只读查询、固定四条执行队列。工作区轮转、渠道冷却、探测、预选及严格领取仍走原路径。查询 SQL 连接池同时服务预选、领取和续租，HTTP 执行位及工作区、渠道预算保留至本次执行实际退出。默认值 0 沿用每个查询 worker 持有一条连接的方式。

## 实际文件

- `.env.example`、`cmd/server/media_execution_config.go`、`cmd/server/media_query_pool_config_test.go`、`scripts/lib/process.mjs` 登记配置、解析边界及隔离进程默认值。
- `internal/studio/app.go`、`media_execution_lanes.go`、`task_leases.go`、`media_schedule_claim.go`、`task_leases_worker.go`、`task_query_pool.go` 控制预算、领取、续租和最终保存。
- 同目录的 `media_http.go`、`media_delivery_execution.go`、`task_query_pool_test.go`、`media_query_capacity_test.go` 接入原 GET、冻结交付及边界和容量测试。
- `docs/wiki/media-original-query-capacity.md` 增加当前路径、配置及测量入口。

## 接口与数据影响

`MEDIA_QUERY_DB_CONNECTIONS` 默认 0。PostgreSQL 模式接受 1 到当前 `MEDIA_QUERY_WORKERS`，最高 32。模式与上限在启动时核验。原查询领取成功后保存所读版本、原状态与完整私有 handle。外发前及心跳在取得代次行、任务行和适用的探测行锁后，以数据库当前时间核对原租约、预期业务版本、状态及完整 handle。连接等待有截止时间，最近确认租约的保守本机截止时间到达后取消 HTTP。原生适配器在调用 GET 以及取得传输连接时再检查本机截止状态。

结果仍由控制写事务保存。启用共享池的 GET 保存前通过 `NOWAIT` 取得代次及任务行锁，再检验原身份、版本及实到期。锁争用时在应用锁外等待，保留原响应并重试保存。公开 Task、账务、审核、Outbox、迁移及备份格式沿用当前版本。

## 验证结果

- 隔离 Go overlay，清除 `TEST_DATABASE_URL` 及 `STUDIO_MEDIA_QUERY_MEASURE` 后运行 `go test -count=1 -v ./cmd/server ./internal/studio -run '^(TestPooledQuery|TestMediaQueryDBConnectionEnvironment|TestMediaExecutionEnvironmentStrictAndIndependent|TestMediaExecutionConfigurationAndSeparateBudgets|TestPostgresPooled|TestAsyncMediaRejectsChangedSavedIdentity|TestMediaRetryAfterAndConfiguration)'`。两包共七个用例通过，包括旧配置、范围、队列超时及已失效查询无法进入本地 HTTP 接收端。六个 PG 入口按条件跳过。
- `go vet ./cmd/server ./internal/studio` 使用同一 overlay 通过，`node --check scripts/lib/process.mjs` 通过。
- 待集成人在隔离 PostgreSQL 串行执行 `STUDIO_MEDIA_QUERY_MEASURE=1 go test -v -count=1 -timeout=180s ./internal/studio -run '^TestPostgresPooled'`。用例覆盖 32 个 HTTP 位及 4 条查询连接、慢 GET 期间可立即借齐 4 个不同 backend、锁等待跨过真实到期、版本/handle/owner/epoch/代次失效、外部 503 冷却、已知响应只重试保存及四条连接全部借出时的真实 HTTP 取消。容量档保持 241 的 128 个原任务、每秒 40 个到期、200 毫秒响应、工作区与渠道上限 16。

## 风险与限制

真实 PG 档、真实供应商及固定宿主 24 小时稳态仍待各自验证。查询 worker 与单渠道预算上限仍为 32。共享连接不足会延后预选，续租无法及时完成会终止原 GET，后续只有仍具原 handle 且可调度的任务可按原规则再查询。传输与数据库分属两个系统，已写出的 GET 可以被对端接收，取消只能停止本次等待和拒绝迟到保存。

## 回滚方式

停止测试服务，将 `MEDIA_QUERY_DB_CONNECTIONS` 设为 0 或取消设置，再启动原版本或当前版本的默认路径。维持数据库和私有目录的单写者交接，沿用原 handle 和租约恢复。

## 文档同步

模块 Wiki 随本记录交付。根 README、Wiki 导航、台账索引、任务源、生成 TODO 与进度由集成人根据实际运行结果同步。V17 的 60,000 个媒体任务、6,000 次查询/秒及 24 小时稳态按总体方案验收。

## 集成人验收

已实际运行上述六项 PostgreSQL 测试，命令另加 `-p 1 -parallel 1 -timeout=240s`，耗时 26.930 秒，全部通过。32 个 HTTP 位与 4 条租约连接的容量试验共有 128 个唯一 GET，完成 37.81 次/秒、实测峰值 11，零意外请求。其他五项分别覆盖慢 GET 可释放全部四条 PG 连接、锁等待及身份变更、上游错误保留原 handle、保存争用只重试已知响应、连接耗尽使待续租查询终止。[容量数据](../wiki/assets/media-pooled-query-capacity-039.json)
