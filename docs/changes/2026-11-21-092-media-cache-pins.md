# 092 · PostgreSQL 媒体任务执行期间的缓存保留

## 目的

090 已将新媒体任务的受理与全局缓存容量分开。worker 领取未缓存任务后，提交、交付检查点和审核仍会写回缓存。取消或运营处置可以先撤销租约，此时供应商调用或审核保存仍可能在执行。本次按实际执行中的领取次数保留任务，并在执行退出后回收超出闲置上限的记录。

## 范围

V16-A4.2，涉及 H02/H03/H04/H09。覆盖 PostgreSQL 模式四种媒体执行通道、取消和运营处置后的缓存行为，以及确认保存、渠道身份冲突停放和任务冷读。延续 [090 媒体受理与冷读](2026-11-21-090-pg-media-cache-admission.md)。

## 实际文件

- `internal/studio/app.go` 增加按任务 ID 统计的在途领取数，由应用互斥锁保护。
- `internal/studio/task_leases_worker.go` 在已取得通道预算的执行入口登记领取，返回时核对 generation、epoch 和 owner，再清理该次执行的缓存租约。该清理晚于供应商调用、审核、终态保存及心跳退出。
- `internal/studio/repository_queries.go` 统一处理已提交任务的缓存写入、版本比较及闲置记录淘汰；`internal/studio/media_schedule_identity.go` 的身份冲突处理也使用此入口。
- `internal/studio/task_cache_test.go` 新增真实 PostgreSQL 取消与迟到 I/O 用例，以及同任务双租约和运营决定的独立回归。本文登记该子项的验证事实。

## 接口与数据影响

沿用 020 数据格式和既有 HTTP、幂等、审核、账务及 Outbox 协议。任务冷读在应用互斥锁内完成，缓存拒绝用较低的任务版本替换较高版本。领取事务使用独立连接，领取时取得的快照不会直接覆盖已经提交的取消。业务状态由 PostgreSQL 事务确认后写回，撤销旧租约后仍在执行的调用继续占用原通道。

## 验证结果

| 命令或检查 | 结果 | 观测 |
| --- | --- | --- |
| `go test ./internal/studio -run '^TestMediaTaskCacheOverlappingLeasePins$' -count=1` | 通过，0.291 秒 | 旧租约退出时保留新租约和较高版本；运营撤销后仍保留在途记录，最后一个调用返回后恢复闲置上限。 |
| 集成人串行运行 `TestPostgresMediaTaskCachePinSurvivesCancelAndLateIO` | 通过，1.36 秒 | 闲置上限为 1，执行冷任务时缓存临时达到 2。取消提交后原供应商调用仍阻塞，淘汰未移除该任务。调用返回后缓存回到 1，重新从 PG 读取同一取消版本。供应商仅收到一次 submit，query 次数为 0。 |
| 同次串行运行 `TestPostgresMediaReviewDispatchFinalBackpressureNeverRechecks` | 通过，4.00 秒 | 真实 PG Outbox 背压使首次审核的最终保存回滚。执行期间保留审核通道和许可；背压解除后提交原决定，审核 HTTP 仅调用一次。 |

上述两项 PG 结果可在本地隔离验证输出 `.runtime/a42c-recovery.log` 核对。整批构建、全量 PG、六套冒烟和完整容量测量由集成台账登记。

## 风险与限制

`MaxRecords` 约束闲置媒体任务条数。实际执行可临时增加缓存，额外条数至多为四通道中实际执行的不同任务数；同一任务的重叠租约共享一个缓存条目。取消、过期或已返回的 HTTP 响应均不能提前释放仍在审核或保存的执行占位。该上界按记录数计算，每条任务及私有交付元数据的大小会影响进程内存。PG 模式继续使用单控制写者和私有目录锁，外部供应商及生产容量另验。

## 回滚方式

停机后按本子项范围撤回在途登记、身份冲突和 worker 写回使用的缓存逻辑及专项测试，保留 090 的持久受理、冷读和原 SQL 栅栏。回滚不需要迁移数据库；旧写回路径可能在执行冷任务时使缓存超过预期。既有任务、冻结正文、资产及完整收据继续保留。

## 文档同步

集成人统一维护根 README 最近变更、[分页恢复 Wiki](../wiki/paged-recovery.md)、[A4 契约](../planning/v16a4-contract.md)、[台账索引](README.md)、[任务源](../planning/tasks.json)、生成的 TODO 与[推进记录](../planning/progress.md)。
