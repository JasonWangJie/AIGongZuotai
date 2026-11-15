# 2026-11-15-088：完整启动恢复故障验收

## 目的

为 V16-A4.1 提供独立故障验证，确认分页恢复遇到真实进程终止或提交回执丢失时，下一次完整 `New` 只继续未提交的恢复；确认旧媒体执行授权先于分批清理失效。本记录不把测试实现或编译通过当作 PG 验收完成。

## 范围

使用真实 App 子进程、隔离 PostgreSQL schema、环回 HTTP 供应商与 PostgreSQL 协议代理。通过数据库行锁观察第 129 条 Run 保存边界，不增加生产故障注入配置，不调用局部恢复 helper 冒充完整启动。与 085 私有批读、086 分页恢复、087 跨页矩阵及大积压验收分工；本项不修改生产代码。

## 实际文件

- 新增 `internal/studio/recovery_process_test.go`。
- 新增本台账及 `docs/wiki/recovery-faults.md`。
- 顶层 README、Wiki/台账索引、任务源及生成 TODO 由集成人统一同步。

## 接口与数据影响

无公共 API、运行配置、数据库迁移或持久业务格式变化。测试专用子进程配置写在临时目录，包含私有 DSN，不输出其内容；只在测试二进制中读取。错误与 readiness 文件只记录固定状态或本机监听地址。

故障代理将子进程第一条控制连接接入既有 `commitAckDropProxy`，辅助租约连接直达同一隔离数据库。代理只在目标恢复事务已阻塞于行锁后才启用，避免把只读分页或早期启动事务的 COMMIT 误当业务提交。

## 验证结果

- `go test ./internal/studio -run '^$'`：首次编译通过，0.036s，明确没有执行 PG 测试。
- 待集成人串行执行：`TestPostgresRecoveryProcessKillAtPageBoundary`、`TestPostgresRecoveryProcessCommitAckUnknown`、`TestPostgresRecoveryGenerationFencesBeforeLeaseCleanup`。
- 实现的断言：真实最初 POST 收到后 Kill；第 128 条已提交而第 129 条受阻；进程再次 Kill 或该业务 COMMIT 回执丢失后不开放 HTTP；后续完整启动保留检查点并恰好一次递增恢复序号、写恢复事件；原任务键重放、原请求/上游正文/handle 不变且供应商仅收到最初一次 POST；generation 已提交而旧 lease 尚未清空时，独立旧写者被原租约条件拒绝。
- 当前没有自行运行 PG、启停本地服务或访问真实供应商。真实 PG 结果与最终治理检查由集成人运行后追加。

## 风险与限制

边界数据是 130 条固定 ID Run 与一个真实受理不明的媒体请求；另一旧 lease 使用同隔离库中的合成检查点验证 generation 条件。它不代表十万任务容量、真实供应商账单、媒体/S3 灾备或全站多实例。原媒体实际 POST 只访问受控环回 HTTP 模拟器。权益处理、私有候选矩阵及大积压启动由其他专项覆盖，不由本测试数量推断通过。

## 回滚方式

本变更只增加验证代码和说明。若测试夹具有问题，在后续修正中保留故障事实与原断言，调整可重复观察方式；不放宽生产门禁，不删除迁移、owner、候选或业务记录。schema 清理仅针对本次测试自行创建的隔离 schema，子进程及代理连接先退出。

## 文档同步

本台账与故障 Wiki 同步，链接 A4 契约、085/086/087 对应说明。A4.1 尚待联合验证，A4.2、完整 V16 与实际外部渠道验收保持原待办状态；集成人更新 README、导航、任务源与进度。

根代理实际执行结果：隔离恢复故障与基础字节专项总15.392s退出1。Kill 3.80s、generation 2.52s、基础字节0.82s通过；ACK用例8.21s失败，specific recovery save COMMIT acknowledgement was not dropped。用户随后暂停，尚未诊断，不能声明本项通过。原始日志.runtime/v16a41-faults-first.log保留。
