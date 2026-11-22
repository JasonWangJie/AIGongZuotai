# 091 · PostgreSQL 聊天缓存与执行、订阅占位

## 目的

V16-A4.2。其他工作区的 Run 占满缓存时，有可用业务额度和等待位的用户仍能创建普通聊天和会话轮次。已受理 Run 在排队、执行、等待保存以及向订阅者发送事件时保留同一份记录。

## 范围

工作区聊天受理、已有 Run 的冷读与终态补读、执行与 SSE 占位，以及停止进程前的中断检查点。

## 实际文件

- `internal/studio/app.go` 用原调度器的排队和执行状态保护 Run。取消已排队的请求后可释放记录。正在调用供应端的请求等 worker 实际退出后释放。关闭时保留已领取但尚未写入终态的 Run，最终中断保存因 Outbox 满而回滚后，先确认消费了一批已有事件，再重试中断事务。
- `internal/studio/conversation.go` 的会话受理与普通 Run 共用 PG 准入边界。两种入口继续先检查原幂等键，继续在受理事务确认后入队。
- `internal/studio/repository_queries.go` 在应用锁保护下按需读取 Run。普通冷读仅在有空槽时填入闲置缓存，调度器和活动 SSE 可各自在已有的并发上限内暂时占用额外槽。
- `internal/studio/http.go` 从登记 SSE 订阅起保留对应记录，直到该处理函数返回。客户端消费过慢时，移除发送通道与释放读取者占位分别执行；另一个客户端断线不会释放仍在使用该记录的订阅者。
- `internal/studio/run_cache_test.go` 覆盖两工作区的普通聊天与会话轮次、队列取消、供应端延迟退出、越权冷读、两个慢 SSE 客户端先后断开、原键重放、终态快照补读和关闭期间的最终检查点。

## 接口与数据影响

公开 API 与数据库格式沿用现有定义。PG 聊天受理仍受工作区、模型准入、调度预算、审核与权益规则约束。内存模式继续使用原有记录条数上限。关闭时的 Outbox 投影仍按已有消费序号和保留窗口执行。

## 验证结果

- 集成人串行执行真实 PostgreSQL 专项 `TestPostgresRunCachePinsExecutionAndSubscribers`，1.58 秒通过。测试将闲置缓存设为 1，分别验证实际 worker 未退出时取消、慢读者移出发送队列后仍持有记录、最后一个读者退出后释放，以及冷读终态回放。
- `go test ./internal/studio -run '^(TestRunIdempotencyLifecycleAndReplay|TestReplayWindowSnapshotAndLastEventIDPrecedence|TestBoundedAdmissionAndSlowSubscriber|TestConversationMemoryBranchReplayAndCapacityPreserveHistory)$' -count=1`，0.445 秒通过。
- 关闭修正后再次运行后两项普通回归，0.309 秒通过。新增 `TestPostgresRunCacheShutdownRetainsPickedRunOnBackpressure` 已编译。它控制在领取完成后、开始执行前关闭，验证满 10,000 条真实待投影事件时，最终只提交一次中断检查点。集成人串行实跑 1.11 秒通过，包含页已满时的真实回滚、投影旧事件和唯一一次中断提交。

## 风险与限制

闲置缓存已满时，重复查询同一个冷门 Run 会再次读取 PG。慢读者、排队请求与在途执行各有独立上限，实际缓存条数可以超过闲置预算。数据库不可用或消费未取得进展时，恢复须依赖下次启动读取持久状态。持久 token 流、跨实例执行和生产容量继续另行验收。

## 回滚方式

按原文件范围撤回聊天缓存准入、保留与释放、关闭阶段背压处理及对应测试，再执行原有回归。无需修改迁移或私有文件。回滚后 PG 将再次使用原全局缓存长度限制聊天受理。

## 文档同步

批次状态和完整启动容量由集成人在[任务源](../planning/tasks.json)、[恢复说明](../wiki/paged-recovery.md)与[A4 契约](../planning/v16a4-contract.md)统一记录，并同步 README、Wiki 导航和台账索引。
