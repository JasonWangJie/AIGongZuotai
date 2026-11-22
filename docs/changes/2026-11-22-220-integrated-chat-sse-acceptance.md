# 2026-11-22-220 聊天子进程、持久批写与授权订阅的同场验收

## 目的

在同一个标准服务进程和一次生成波次中，核对模型实际输出、数据库保存的唯一增量及多个授权读者收到的逐份副本。生产批写合并事务，服务端仍须向同一运行的每个读者交付完整的原序内容和终态。

## 范围

V17-J4 本机联合专项，覆盖 H03 模型与执行、H07 可靠性与性能。依赖 204 的独立回放读连接、205—207 的受管子进程、209—211 的批写及物理事务核对、218 的共享会话复核。原 207 的 `pilot` 和 `full` 档继续各用一位普通读者，保留原账务与失权专项。

## 实际文件

- `internal/studio/chat_child_acceptance_test.go` 给同一个标准服务进程验收增加两个显式档位，保留独立登录、报价、幂等重读、真实子进程、账务、隔离和恢复检查。
- `internal/studio/chat_child_integrated_acceptance_test.go` 核对读者身份与波次、受保护的固定数值指标、全局与工作区同时占位，并复用 `observeBatchTransactions` 检查原增量和实际提交。
- 本台账记录命令、预期口径和实测边界。

## 接口与数据影响

`STUDIO_CHAT_CHILD_ACCEPTANCE=integrated-pilot` 使用 8 路模型、24 位普通读者和 1 位迟读者。`integrated` 使用 80 路、两个各 40 槽的真实 OS 子进程、5 个工作区和 4 个冻结模型。测试只向其启动的回环标准服务发请求，模型只连接本机合成网关。标准服务显式使用 `RUN_DELTA_BATCHING_ENABLED=true`、`ADMISSION_GLOBAL_STREAMS=256`、`ADMISSION_WORKSPACE_STREAMS=64` 和 `RUN_REPLAY_READ_CONNECTIONS=2`。该变更涉及测试与台账。

正式档由 240 位普通读者分别读取 12 条增量及 1 条完成终态。另 1 位同账户读者在收到第一条增量后停读，直到全体普通读者完成。持有全部模型连接时，读取五个工作区各自的 `/api/v1/runtime`（预期 49/48/48/48/48），交叉核对总占位 241、按会话复核的轮数和检查的会话数。操作系统连接归属、当前持久租约与原模型 ID 共同确认两组 40 路执行。

报告分别记录 960 条原 PostgreSQL 增量、其实际 insert xid 数与每组 distinct Run 分布，2,880 份普通增量交付及 240 份普通终态，以及迟读者的 12 份增量和 1 份终态。每份按相应运行、连续序号、完整原文和终态核对；每项原模型仅可进入一次。数据库观察另外逐行核对原文、顺序和规范摘要，并与协调器的成功数、组事务数及单项尝试数交叉验证。延迟从合成网关开始写原帧量到客户端收到完整 SSE 帧，报告分别给普通、迟读和全部 2,892 次增量交付的样本数、P50、P95、P99 与最大值。每个运行完成后的原历史重读以及后续故障阶段单独核对。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test -p 1 -count=1 -run '^(TestChatChildIntegratedReaderBarrier\|TestChatChildIntegratedMetrics\|TestActiveStreamCapacityObservation\|TestActiveStreamCapacityModelFrames\|TestBatchedDeltaTransactionEvidence\|TestChatChildAcceptanceQueuedSnapshot)$' -v ./internal/studio` | 通过 | 独立稀疏目录通过 Go overlay 编译并运行 6 项，包耗时 0.169 秒。覆盖多读者同 Run、单读者重复交付、错误身份和序号、原 SSE 内容与终态、物理提交直方图、排队快照和真实内存服务的受保护指标。 |
| `go test -p 1 -count=1 -timeout=10m -run '^TestPostgresChatChildAcceptance$' -v ./internal/studio` | 待集成人串行实测 | Windows 且显式设置隔离的回环 `TEST_DATABASE_URL`，用当前源 `go build -o <新绝对路径> ./cmd/server`，将其绝对路径置入 `STUDIO_CHAT_CHILD_SERVER`。先选 `integrated-pilot`，再选 `integrated`，每次使用新的绝对 `STUDIO_CHAT_CHILD_REPORT` 路径。每份报告只在全部断言通过后创建。 |

## 风险与限制

上述正式档数量是验收约束，实际提交组形和端到端分位以该档独立报告为准。迟读阶段测客户端应用层停读，TCP 写截止另见 214。两个回放连接是进程配置的池容量。内核归属检查使用 Windows 本机接口；外部供应商、跨主机和生产容量仍需在对应环境实测。

## 回滚方式

移除两个新增测试档和联合观测辅助，恢复原 207 测试文件。继续使用原有 pilot/full 即可验证旧口径。此项没有迁移或服务状态变更。

## 文档同步

由集成人合入时维护根 README 最近变更、Wiki 性能页、台账索引和 V17 的任务证据。完整联合档的报告应单列物理提交和交付副本，并注明本机设备与测试条件。
