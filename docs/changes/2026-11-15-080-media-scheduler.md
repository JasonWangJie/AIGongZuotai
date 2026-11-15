# 2026-11-15-080：媒体公平调度与持久退避

## 目的

V16-A3.3在四个执行通道上增加工作区轮转、冻结渠道预算和持久退避；保持未知提交、交付清单与首次审核的已有安全边界。

## 范围

核心执行及020调度持久事实。081负责HTTP观测与共享审核许可，082负责独立验证与维护，083由集成人记录全量验收。当前实现中，未宣称V16完成。

## 实际文件

- `db/migrations/020_media_schedule.sql`
- `internal/studio/media_scheduler.go`、`media_schedule_store.go`、`media_schedule_select.go`、`media_schedule_claim.go`、`media_schedule_memory.go`、`media_schedule_identity.go`、`media_schedule_delivery.go`
- `internal/studio/media_schedule_test.go`、`media_schedule_postgres_test.go`、`media_schedule_identity_test.go`、`media_schedule_review_test.go`、`media_schedule_delivery_test.go`、`media_schedule_lanes_test.go`、`media_schedule_channel_race_test.go`
- `internal/studio/app.go`、`task_leases.go`、`task_leases_worker.go`、`persistence.go`、`ledger_hooks.go`、`media_execution_lanes.go`、`repository_queries.go`、`operations_cases.go`
- `internal/studio/media_delivery_execution.go`、`media_moderation_execution.go`、`media_execution_lanes_test.go`、`media_moderation_execution_test.go`
- `docs/wiki/media-scheduler.md`

## 接口与数据影响

新增私有任务调度、工作区及通道游标、渠道运行状态。原001—019不修改；020要求对应格式栅栏及联备。公开任务/幂等/审核和账务语义不变。新增每工作区和冻结渠道的执行预算配置。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| 静态读取现有契约与执行链 | 通过 | 发现混合due查询和缺少结构化渠道观测，实施中 |
| `go test ./internal/studio -run '^Test(MediaScheduler\|MediaScheduleUnrepresentable\|MediaExecutionMemory\|MediaLaneMetrics)' -count=1` | 通过 | 0.105s，范围配置、预算、轮转、稳定退避/迟成功、strict ID及旧memory隔离 |
| `go test ./internal/studio -run '^TestMediaScheduleOpaqueIdentity' -count=1` | 通过 | 0.032s，旧opaque保守共享预算和冲突区分 |
| `go test ./internal/studio -run '^$'; go vet ./internal/studio` | 通过 | 编译0.031s；静态检查退出0 |
| root串行8个`TestPostgresMediaSchedule...`核心故障函数 | 通过 | 12.897s：cursor/claim/业务观察ACK丢失、回滚、Outbox背压、probe竞争、持久冷却/strict ID、长截止人工核对 |
| root串行身份恢复/真实多件晚429两项PG | 通过 | 6.864s，identity1.86s、晚4294.97s；整组隔离与权益REVIEW |
| root执行082大积压计划和大库升级 | 部分首次失败后通过 | 计划180项173.83s；首次200k升级受普通do的5秒中止。set-based回填、bulk建二级索引及专用60秒启动预算后复验30.741s，实际迁移11.2288314s、59旧表保持；普通请求仍5秒 |
| root串行源HTTP成功恢复PG | 通过 | 5.101s，真实首429及8次完整GET成功恢复有效limit，原请求/清单不变，无重复POST或Query |
| root串行首次渠道行竞争及原子性复验 | 通过 | 6.062s；独立连接commit/rollback2.92s，原ProjectionRollback1.41s、Outbox1.69s；新健康基线未被失败观察改变 |
| query/delivery/review实际业务公平专项编译 | 通过 | 协作代理新增三子例，仅编译0.035s；真实双账户API与无ID生产selector，PG待root串行 |
| 其余三lane实际公平、全量PG | 未运行/待汇总 | 083记录最终执行结果，不以编译替代PG验收 |
| `npm run check:docs` | 通过 | 181份文档、83份台账，本轮新增Wiki与本台账有效 |
| TODO及全量构建 | 待汇总 | 待083集成 |

并行接线曾出现unused time、`checkMemoryReconcileSchedule`及测试辅助名尚未定义的编译失败；后续完整普通编译已通过，不把这些中间构建当验收。首次旧memory隔离失败来自新默认每workspace1正确限制了旧测试同workspace双submit；root为该lane隔离夹具显式配置原试验所需scope上限，保留全部峰值断言。严格ID不再跳过due，旧连续步骤测试仅通过`advanceMediaScheduleDue`推进普通poll，不能越过真实服务端截止、冷却、probe或活动lease。

独立只读审查还发现源成功缺少真实HTTP观察、多件审核以第一件时间锚定晚到429、冲突身份活跃空转。分别补可选Observed导入接口、实际后件响应时间及控制事务明确隔离；不可把完成收据复用当HTTP成功，也不把合法旧opaque身份当损坏。

最终静态复核发现首次渠道行不存在时，`SELECT FOR UPDATE`不能锁住缺行，可能跨越另一任务首次冷却提交。strict claim现先插入按配置硬上限的健康调度基线，再锁行重核；健康行不是HTTP成功证据。新增独立连接测试在真实PG锁等待上分别提交/回滚首次观察；原事务回滚/背压测试改为验证健康基线未变，保留业务原子性断言。root串行复验6.062s通过；该修正未修改020和selector，既有大型计划对应的生产选择SQL保持。

全量后补充修正：真实后台5ms用例重现40P01，确认selector的lane `FOR UPDATE`与业务workspace更新/外键`KEY SHARE`形成循环。`media_schedule_select.go`两处游标锁改`FOR NO KEY UPDATE`，保持互斥并兼容外键锁；020不变。`media_moderation_access_test.go`保留原高频用例，增加确定性双连接阻塞后业务更新/游标推进；`admission_test.go`只补手工夹具真实provider mode。三项根PG4.616s通过，全部临时内存诊断字段及输出已删除。大预选因锁开销属于测量范围而重跑，不用前述历史成绩替代最终验收；详情083。

## 风险与限制

仍为单控制写者；生产媒体适配器仍单配置网关。固定活跃集合的公平等待与集合大小相关，没有无限租户的绝对时延保证。真实供应商/生产容量、V16-B/C另验。

大库升级测量是`openPostgres`迁移，不是20万活跃任务的完整`App.New`恢复容量。既有`store.restore`仍全量读取活跃Task并逐条加载阶段私有记录，受普通5秒预算约束；本批没有把它伪称为有界惰性恢复。分批恢复、异常隔离与Outbox满队列的完整链路应由后续V16恢复项独立实现和验收。

## 回滚方式

020上线后旧写者须拒绝。停写并联备PG、私有目录及密钥；不可删除迁移或owner回退。回滚只能使用匹配版本的完整联备。

## 文档同步

- README、任务源、台账索引由root统一同步。
- 模块Wiki由080维护，完整验收证据由083汇总。

联合收口补记：根完整check、最终全PG 443.228s（实际版本化S3/联备启用）、六smoke、同构建回归及治理通过，详见083和联合JSON。本记录的中间待验/失败历史保留；A3.3本地验收，完整V16未完成。
