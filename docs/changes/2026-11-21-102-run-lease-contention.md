# 102 · 聊天执行租约竞争、过期写入与迟到审核

## 目的

V16-B2。验证同一 Run 的首次领取只有一个赢家，已经失权的执行者无法改变已提交正文、审核、权益或项目通知。

## 范围

覆盖两条独立数据库连接上的领取、续租、到期、中断、撤销与迟到写入，以及真实 Go HTTP 处理、环回文本审核和本机 PG 组成的受审聊天链路。

## 实际文件

- `internal/studio/run_lease_integration_test.go` 新增独立连接的争用、过期、撤销和晚审核三个回归。
- 本台账记录 B2 的并发与可见性专项，格式升级、进程 Kill、联备和整批结果由集成人另外登记。

## 接口与数据影响

测试调用 022 的领取、续租、追加、检查点、终态及控制撤销事务。同一控制代次的两个 PG 后端竞争同一个已受理 Run，使用 `pg_stat_activity` 确认两个后端都已抵达行锁，再解除阻塞。截止前已经进入 PG 的写入须在取得锁时依据数据库时钟重新判断有效期。

输出审核用例先真实冻结测试 CREDIT。环回审核服务取得完整候选后停住，独立连接使租约到期，随后服务按协议回复允许。通过现有 Run API、SSE、决策、日志、权益和 Outbox 查询检查最终可见状态。

## 验证结果

专项可用 `go test ./internal/studio -count=1 -run '^TestPostgresRunLease(ContentionExpiryAndLateWrites|CancellationRevokesLiveWriter|ExpiredModerationCannotPublishOrSettle)$'` 复跑。集成人使用隔离 PG 串行实测，竞争与过期 2.00 秒、取消撤销 0.90 秒、晚审核 1.92 秒通过。

- `TestPostgresRunLeaseContentionExpiryAndLateWrites` 核对唯一领取、按 PG 时钟续租、相同增量回放、错误 owner/epoch/generation/执行 ID、行锁等待跨过截止、已到期的相同增量回放与检查点/终态拒绝，以及原前缀中断和单次开始。
- `TestPostgresRunLeaseCancellationRevokesLiveWriter` 先由持有者成功追加并保存检查点，再由控制事务取消，核对迟到的追加、检查点、终态和续租，验证输出、序号、取消 Outbox 和流事件计数。
- `TestPostgresRunLeaseExpiredModerationCannotPublishOrSettle` 核对迟到 ALLOWED 后的 INTERRUPTED、原冻结 REVIEW、无扣费或解冻分录；逐项检查流表、项目通知、HTTP 回放、快照、历史和原键重放，统计模型与输出审核各调用一次。

首轮晚审核在 6.50 秒时失败，输出审核服务未收到请求。领取事务从公开 Run 文档重建执行状态时遗漏了单独存放的原审核政策。修正后在同一事务读取已冻结政策，原断言通过；与旧的输入/输出原子性、流中审核隔离两项 PG 回归一起重跑，3 项用时 5.051 秒。

## 风险与限制

测试中的数据、模型与审核响应均为本机 fixture。控制写连接和私有目录锁仍生效。本专项验证独立连接竞争所经过的实际事务；生产多实例、真实审核语义和容量需分别验收。

## 回滚方式

专项新增和修改的数据均在隔离测试库内。已运行 022 的环境需要停机并恢复配套应用、数据库、私有目录及适用密钥，保留迁移和目录归属记录。

## 文档同步

集成人统一同步根 README、Wiki、台账索引、[任务源](../planning/tasks.json)、生成的 TODO 和进度。验收边界见 [B2 契约](../planning/v16b-contract.md)。
