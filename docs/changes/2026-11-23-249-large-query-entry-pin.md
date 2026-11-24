# 249 · 大档原查询的执行登记

## 目的

V17（H03/H07/H08）在 248 的本机 128 查询位测量中，首次续租前 P95 为 233.79 毫秒，同档已知结果等待应用锁的 P95 为 271.76 毫秒。严格领取后的原 GET 只需在首次续租前登记活动执行数。将这一步移出应用全局锁，使正在提交另一任务结果的控制写者可以与该执行登记并行。

## 范围

沿用 040 单控制写者。仅启用 33 至 1,536 个查询位、共享查询租约连接、已严格领取且有完整原 handle、无已存交付和审核候选的查询使用独立短锁。32 位及以下、提交、交付和首次审核均沿原顺序准备执行。工作区和冻结渠道的预留、探测和冷却、严格领取、首次及心跳续租和 wire 时限继续使用 040 实现。

## 实际文件

- `internal/studio/app.go` 增加任务活动引用计数的互斥锁。
- `internal/studio/task_leases_worker.go` 分离活动引用登记和可能持久化状态的执行准备，清理时核对实际 owner、epoch 和 generation。
- `internal/studio/repository_queries.go` 在容量已满时原子核对活动引用及缓存写入，在清理可逐出的记录前核对其引用数。
- `internal/studio/media_query_entry_test.go` 覆盖大档选择条件、全局锁竞争和最后一个引用清理，提供真实 HTTP、PG 行锁及 COMMIT 回执丢失回归。
- `docs/wiki/media-original-query-capacity.md` 记录执行范围和复测入口。

## 接口与数据影响

引用锁只保护当前执行计数及单项缓存容量判定。涉及两把锁时先取得应用锁，随后取得引用锁，引用锁内执行常数个 map 操作。已领取的私有任务副本独立于公开缓存。原 GET 前仍由数据库按已提交的代次、owner、epoch、预期版本、原 handle、当前调度投影和冻结渠道核验。原结果继续由原控制写连接同事务持久化任务、调度、适用账务及 Outbox，确认后才更新缓存和通知读者。没有增加连接、队列、协程、迁移或公开接口。

## 验证结果

- 使用 Go 1.26.11、隔离 overlay，清空 `TEST_DATABASE_URL` 后运行 `go test -count=1 -v ./internal/studio -run '^(TestLargeQuery.*249|TestPostgresLargeQuery.*249|TestMediaTaskCacheOverlappingLeasePins|TestQueryStageOverlap248|TestPooledQueryLimitsAndWait|TestPooledQueryWireGuard|TestLargeQueryLimits040|TestMediaLaneIsolationMemory)$'`。八项本地回归验证准入、争锁、换代引用、旧档、四 lane、续租预算和写出门禁。两项新增 PostgreSQL 用例按条件跳过。
- 同一 overlay 的 `go vet ./cmd/server ./internal/studio` 和 `go build ./cmd/...` 通过。
- 实库入口为 `go test -count=1 -v -timeout=3m ./internal/studio -run '^(TestPostgresLargeQuery.*249|TestPostgresPooledQueryKnownResponseSaveRetry|TestPostgresLargeQueryHandoffFences040|TestPostgresMediaTaskCachePinSurvivesCancelAndLateIO)$'`。第一项新增用例在应用锁仍持有时等待真实原 GET 到达，再用 PG 行锁制造实际保存回滚及续租排队，通过任务详情及项目通知核对提交前后的版本，并核对真实供应商未支持取消时的 409。第二项在独占控制连接提交已知结果时丢弃实际 COMMIT 回复，核对缓存、事件、释放引用、任务详情的 503 及独立 PG 终态，停机重开后从正常详情读取同一终态。两项均断言只有一次原 GET。
- 性能对照使用相同主机和 248 入口，设置 `STUDIO_MEDIA_QUERY_TIMINGS=1` 后运行 `go test -count=1 -v -timeout=5m ./internal/studio -run '^TestPostgresQueryStage.*248$'`。保留每秒 320 个到期、每档 512 项、固定 200 毫秒服务和 4 条租约连接。隔离开发期间未连接 PostgreSQL，实库回归及曲线由集成人串行执行。

## 风险与限制

此前普通取消与运营核实均可按当前缓存构造新的控制写。若把保存事务移出应用锁，需要先覆盖同任务事务提交到缓存发布之间的全部写入与读取交错。249 把修改限定于首次执行登记，结果保存仍会竞争应用锁。性能判断需同时比较首次续租前等待、结果锁等待、完成率、HTTP 峰值和到期至写出 P95/P99。三档短测仅对应本机单写者，持续 6,000 次/秒和 24 小时仍待原条件验收。

## 回滚方式

将查询执行位调回 32 或以下并按既有方式重开，进入旧准备路径。完整回滚可移除本次补丁。040 格式、联备和私有目录继续按原要求恢复。

## 文档同步

模块 Wiki 和本台账随隔离补丁交付，根 README、Wiki 导航、台账索引、任务源、生成 TODO 与推进记录由集成人依据实际结果同步。

## 集成人实库结果

执行登记、交接、保存回退、取消期间的缓存占位和提交回执丢失五项 PostgreSQL 用例通过，核心包用时 12.723 秒。应用锁由另一任务持有时，已领取任务的原 GET 抵达环回接收端。提交回执丢失后，旧进程停止写入，重开服务读回数据库实际保存的终态。

沿用 248 的定时负载复跑三档和独立心跳，两项用例共 32.902 秒。每档完成 512 个唯一原 GET，新增生成 POST 均为 0。64 位与 128 位档在执行入口到首次续租之间的 P95 均读到 0 毫秒。128 位档全部 512 次合计 1.5343 毫秒，248 的同档 P95 为 233.79 毫秒。零值受计时分辨率影响。32 位沿用原执行路径。

128 位档的已知结果等待应用锁 P95 从 271.76 变为 283.50 毫秒。实测完成 128.65 次/秒，到期至写出 P95 为 1867.95 毫秒，仍超出 500 毫秒目标。每个并发档只有一轮短时本机样本。


[各档分段与到期结果](../wiki/assets/media-query-entry-040.json)
