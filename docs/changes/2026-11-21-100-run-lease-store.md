# 100 · 聊天执行租约与数据库事务

## 目的

V16-B2 使运行中的聊天只有原执行者能提交增量、检查点与最终审核决定。用户取消、进程换代或租约过期后，数据库保存原 Run 已确认的正文前缀，已启动的生成保持原执行身份。

## 范围

涉及 H02、H03、H04、H09，承接 [B1 持久流水位](2026-11-21-096-run-stream-store.md) 和 [V16-B 契约](../planning/v16b-contract.md)。本项包含 022 增量迁移、执行者首领与续约、提交前数据库时钟校验、撤销、控制中断与旧格式恢复。进程内执行和故障注入分别见 [101](2026-11-21-101-run-lease-execution.md)、[102](2026-11-21-102-run-lease-contention.md)、[103](2026-11-21-103-run-lease-integration.md)。

## 实际文件

- `db/migrations/022_run_leases.sql` 新增聊天控制代次表与流头上的持有者、执行轮次、代次、到期时间和格式标记。原 021 行保留原执行身份、正文水位、回放窗口及事件。
- `internal/studio/persistence.go` 在持有数据库控制写锁后轮换启动代次。通用快照与运营账务修订只接受已终态的原业务状态。
- `internal/studio/run_lease_store.go` 实现首领、续约、执行快照、撤销与已提交前缀中断。`internal/studio/run_stream_store.go` 将公开增量及窗口裁剪接入有效租约。
- `internal/studio/operations_cases.go` 在锁定 Run 的运营查询中按原工作区及资源身份先锁适用的权益冻结，随后读取并锁定业务行。预演、执行及离线财务核实共享冻结行优先的顺序。
- `internal/studio/run_stream_store_test.go` 覆盖正文序列、原检查点、审核快照与等待 Outbox 时到期的终态事务。原有 `admission_test.go`、`ledger_test.go`、`moderation_test.go`、`moderation_commit_test.go`、`moderation_integration_test.go`、`operations_cases_test.go`、`outbox_test.go` 和 `recovery_integration_test.go` 改用真实领取建立运行夹具。

## 接口与数据影响

客户端继续使用原 Run、项目作用域与流序号。控制写者每次打开数据库时一次性捕获新的聊天代次。执行写事务对该代次取共享行锁，轮换与在途事务顺序提交。首次领取须满足 022 新 Run、QUEUED、原执行身份为空及预期水位一致。领取身份、RUNNING、适用的权益执行标记和 Outbox 同事务。审核策略从受理时冻结的私有数据库记录读取。

数据库以 `clock_timestamp()` 校验租约。每次追加核对持有者、epoch、generation、原执行身份、原状态、有效期及预期水位。重复增量同时核对窗口内原文。续约保留原轮次和原执行身份。检查点与终态按既有锁序先锁权益冻结，再锁业务行与流头；增量及心跳只取所需锁。业务、账务、审核、流事件和 Outbox 写入后再检查当前期限，终态撤销和最后一次时钟校验在同一条条件更新中完成。

控制取消、退出和重启从锁定的数据库文档及已提交增量重建公开前缀。原请求已启动时，缺少可信交付的权益继续待核实；未交付的审核候选保持隔离。在同一事务里更新业务、审核、权益和 Outbox 并撤销原租约。没有有效租约的执行者无法保存快照；运营与离线权益仍可修订原终态。021 旧流不补造租约，020 无流头的 Run 保留原恢复序号间隔。

## 验证结果

- `go test ./internal/studio -run '^$' -count=1` 通过，加入运营财务锁序专项后的包编译用时 0.244 秒，此命令仅编译。
- 集成人首轮真实 PG 专项确认两连接竞争与过期迟到写入 2.00 秒、有效时取消撤销 0.90 秒、021 升级 1.59 秒、换代进程终止 2.40 秒。该轮也发现领取时从公开文档重建会丢失冻结审核策略；加入私有策略读取后，过期审核拒绝公开或结算用例在独立复测通过，用时 1.92 秒。原始记录为 `.runtime/b2-focused.log` 与 `.runtime/b2-review.log`。
- 集成人最终串行执行 22 项真实 PG 组合专项，全部通过，总用时 62.704 秒。记录见 `.runtime/b2-final-focused.log`。同组的领取 COMMIT 回执丢失、代次切换期间进程终止、心跳及中断背压分别通过，用时 2.05 秒、3.63 秒、3.72 秒。
- `TestPostgresRunLeaseDeadlineDuringTerminalOutboxWait` 通过，用时 2.54 秒。终态已进行审核、权益修改且在等待 Outbox 行锁时，数据库期限到期，业务快照、审核决定、账务分录和 Outbox 均回滚。
- `TestPostgresRunLeaseOperationsAndLateSaveLockOrder` 通过，用时 1.08 秒。运营实际释放与已撤权旧执行者的保存同时等待同一冻结行，第三连接能立即取得业务行锁。放开冻结行后只有运营释放一次，原终态保留，迟到的旧凭据被拒绝。全量回归与联备验收结果由集成人登记于 [103](2026-11-21-103-run-lease-integration.md)。

## 风险与限制

每个公开增量需要数据库提交，运行期间还需定期续约。数据库连接或提交确认异常将停止本进程的执行写入，重启后读取原事务事实。测试范围为本机 PostgreSQL，控制写者及私有目录仍独占。真实供应商收费、外部审核质量和生产容量另行验收。

## 回滚方式

先停机，按同一恢复点还原升级前的 021 数据库、私有目录、目录 owner、启用的独立密钥及对应私有对象版本，再使用匹配的旧程序。切换前核对升级后新增的业务与账务记录。保留既有迁移登记和原执行凭据。

## 文档同步

集成人维护 README、Wiki、[决策 0022](../wiki/decisions/0022-run-execution-leases.md)、[台账索引](README.md)、[任务源](../planning/tasks.json)、两份生成的 TODO 与[推进记录](../planning/progress.md)。本记录登记存储实现与专项证据，整批验收结果见 [103](2026-11-21-103-run-lease-integration.md)。
