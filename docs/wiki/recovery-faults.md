# 分页恢复的进程与提交故障验证

V16-A4.1 的独立故障测试位于 `internal/studio/recovery_process_test.go`，记录见 [088](../changes/2026-11-15-088-recovery-faults.md)。2026-11-21 已在独立 PostgreSQL 16.14 验证目标回执丢失、页边界进程终止与 generation 栅栏，见[089 联合记录](../changes/2026-11-21-089-recovery-acceptance.md)。设计边界见 [A4 契约](../planning/v16a4-contract.md)、[分页恢复](paged-recovery.md)、[私有阶段批读](recovery-private-phases.md)与[完整启动矩阵](recovery-validation.md)。

## 故障如何落在真实恢复路径

测试子进程调用完整生产 `New`。父进程先通过真实 App HTTP 受理媒体生成，环回供应商收到一次 POST 后保持响应未返回；实际 Kill 子进程，保留已经提交的 SUBMITTING 与未知 handle。后续启动必须隔离为 SUBMIT_UNKNOWN，原键只能读取原任务。

Run 检查点按稳定 ID 排序，父进程持有第 129 条的数据库行锁，并通过 `pg_blocking_pids` 确认恢复写者正在等待。此时前 128 条恢复和对应 Outbox 已提交，HTTP 尚未开放；测试在这个边界实际 Kill，重启后检查余下记录恢复、原序号与事件不重复、部分正文保持。

第二个场景仍使用同一个锁边界，但释放锁之前才启用协议代理。代理在真实数据库已返回目标 COMMIT 完成之后、将确认交给客户端之前切断控制连接；同一测试代理转发所有启动连接，只有与第 129 条记录实际行锁等待者 PID 相同的 COMMIT 才丢弃回执，其他连接照常转发。启动必须拒绝且没有 readiness，下一次完整 `New` 读取真正已提交结果，不重复恢复序号或发送供应商 POST。

第三个场景把旧 lease 清理阻塞在第一条任务行。独立连接确认 executor generation 已经变化，而另一未到期的旧 lease owner/epoch 仍存在，再调用原 fenced write。预期拒绝来自 generation 校验，不依赖先清空 owner 或 lease 过期；随后释放锁，完整启动继续。

## 测量与限制

本组检查真实进程、PG 事务、原幂等记录、冻结请求、恢复版本及 Outbox；没有生产注入开关，也不修改审核或存储语义。测试专用 DSN/配置只存临时文件，诊断不打印正文、密钥或绑定参数。

130 条 Run 是用于跨 128 条页边界的确定性夹具，不是容量成绩。完整私有候选矩阵、万条恢复/Outbox 背压、权益以及 S3 联备由相应专项分别验证；本地 HTTP 模拟器不能替代真实供应商验收。A4.1 仍允许全缓存，A4.2 的内存边界和完整容量结论不能由这些故障测试推定。
