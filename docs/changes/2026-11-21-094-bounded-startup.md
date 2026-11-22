# 094 · 有界缓存与完整启动联合验证

## 目的

在 PostgreSQL 中验证高积压下的完整 App 启动、跨工作区业务准入、执行/读者保留、项目权限和运营后的冷热一致性。对应 V16-A4.2，由 090—093 提供受理、运行与项目能力。

## 范围

H01/H02/H03/H04/H07/H09。覆盖恢复、未知提交、取消、审核、运营账务快照、工作区创建、执行与订阅争用，以及完整本机 PG 集成回归。沿用 020 数据格式与 089 的恢复规则。

## 实际文件

恢复与容量涉及 `internal/studio/recovery.go`、同目录的 `recovery_integration_test.go`、`recovery_large_test.go` 和 `recovery_capacity_test.go`。运营回放涉及 `operations_cases.go` 和 `operations_run_replay_test.go`。`ledger_test.go` 改为从数据库核实重启后的权益结果。执行缓存与项目路径见 [091](2026-11-21-091-run-cache-pins.md)、[092](2026-11-21-092-media-cache-pins.md)、[093](2026-11-21-093-workspace-project-queries.md)。README、Wiki、A4 契约、任务源、两份 TODO、进度及台账索引同步。

## 接口与数据影响

PG 启动按页恢复全部应处理的业务，预热量受现有记录数配置限制。项目列表与范围实时读库。运营账务版本提交后，已缓存 Run 的回放环登记同序号快照；审核公开门禁沿用已有决定。无新增公开接口或迁移。

## 验证结果

- 隔离 PostgreSQL 16.14、Windows、Go 1.26.11。PG 用例由集成人串行执行。首组项目、聊天、已有缓存受理及跨页恢复 13.839s 通过。第二组重跑 089 与媒体占位 56.340s 通过，12,002 个中断资源、9,999 条原有事件，完整启动 36.198s，缓存峰值各 512 条，22,001 条事件全部投影，二次启动无重复。[恢复测量](../wiki/assets/recovery-backlog-bounded-2026-11-21.json)
- `RECOVERY_CAPACITY_TEST=1 go test -count=1 -v -timeout 45m ./internal/studio -run '^TestPostgresBoundedCacheCompleteStartup$'`。在所有 020 触发器和约束启用的条件下分批装载 10 万活跃（1,024 Run，98,976 媒体）与 10 万终态（各 5 万）。正常生产入口 `New` 7.913s，装载另计 50.082s。8 页 Run、774 页 Task，读取均值 4.945ms、最大 27.657ms，每页最多 128 条、72,570 编码字节。Run 与 Task 缓存峰值各 512，项目为 0，启动驻留业务载荷重编码 752,252 字节。保留 98,848 个原 handle，128 个无 handle 提交隔离，1,024 个 Run 中断，1,152 条事件全部投影，外发 0 次。[容量测量](../wiki/assets/recovery-capacity-2026-11-21.json)
- 独立复核修复了 worker 领取与停机相遇时可能漏存 Run，以及热 Run 的旧终态游标可能漏掉运营账务更新。真实队列满时停机保存和冷热运营 SSE 补读专项各 1.11s、2.36s；连同媒体清理、运营租约、审核、项目上限与运行占位，组合 11.798s 通过。
- 第一次全量 PG 在 136.570s 退出失败。两个权益测试直接构造闲置缓存上限为零的 App，恢复后从内存取 Run。其中一个因此发生空指针错误。两项断言改为冷读同一持久 Run，检查真实的权益状态。定向运行 `TestPostgresLedger(MediaPartialAndUnknownRecovery|ConcurrentAdminReplayAndUnknownCommit)`，3.307s 通过。
- `npm run check` 51.04s 通过，含 Go vet、测试与构建、301 项前端测试和 10 项任务治理测试。同一构建串行运行六套本地冒烟，基础 8.92s、runtime 1.06s、catalog 1.62s、references 7.69s、library 7.38s、conversations 1.28s，均退出 0。
- 最终 `npm run test:db` 在隔离 PG 上启用 `RECOVERY_CAPACITY_TEST=1` 和 `RECOVERY_LARGE_TEST=1`，studio 包 586.152s 通过，命令退出 0。包含本机版本化 S3 和真实 pg_dump/pg_restore。20 万条记录的完整启动复测 5.758s，装载另计 50.176s；另一场景处理 22,001 条事件用时 36.248s，二次启动 0.307s。两组启动均无外发。既有 A3.3 的大规模调度计划和 019 升级基准未重跑，本机 Windows 权限也未允许运行两处符号链接子例。

## 风险与限制

上述容量样本的原 handle 在未来到期，启动时有实际中断处置与旧 handle 保留，未发真实生成请求。编码大小排除 Go 堆和操作系统开销。结果仅适用于该本机负载；V16-B 的持久增量回放与 V16-C 综合负载仍待验。

## 回滚方式

联同 091—093 撤回本次代码增量可恢复到 090。继续保留 PG、原绑定目录、密钥与实际对象，无需降级 SQL。回滚后旧版会再次预载项目和活跃记录，且旧 SSE 回放环缺少运营快照。

## 文档同步

见[缓存决策](../wiki/decisions/0020-bounded-pg-caches.md)、[分页恢复 Wiki](../wiki/paged-recovery.md)、[A4 契约](../planning/v16a4-contract.md)及[任务源](../planning/tasks.json)。
