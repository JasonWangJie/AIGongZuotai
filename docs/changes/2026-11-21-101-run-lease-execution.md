# 101 · 聊天执行续租、失权中断与本地执行位

## 目的

V16-B2。已受理聊天在调用模型、输出审核和等待终态持久化期间使用原领取的执行租约。租约失效后立即取消原调用，并由控制写者从数据库确认的公开前缀处理终态。

## 范围

普通 Run 与会话 Run 共用执行入口。PG 模式使用 022 的 owner、epoch、generation、期限和原执行身份进行领取、续租与保存。内存演示继续使用进程内调度。全站仍由单控制写者和 OS 私有目录锁保护。

## 实际文件

- `internal/studio/app.go` 增加独立的内部 `Config.RunLeaseDuration`，默认 30 秒，最小 3 秒，最大 5 分钟。新执行使用已确认的租约，调用模型前和开始输出审核前分别重新确认。增量、检查点、审核与终态持久化均带原领取身份和预期序号。用户取消与关闭走控制写入。
- `internal/studio/run_lease_watch.go` 为每个已在执行的聊天 worker 建立一个续租协程，每个租期的三分之一尝试续租；是否过期由 PG 判断。确认失权先取消模型上下文，再通过控制事务核对原领取、重建数据库中的确认前缀。Outbox 背压仅重试已回滚的中断保存。模型、审核、续租与收尾全部退出后，worker 释放本地执行位。
- `internal/studio/run_lease_watch_test.go` 覆盖内部配置边界、模型阻塞期间的续租、过期中断、迟到输出与 Outbox 背压后的条件重试。对普通 Run，中断提交后向活动订阅发送 `run.interrupted`，测试逐项比对热事件与持久事件的名称、序号及 JSON 内容。开启审核的 Run 沿用 `run.snapshot`。
- 本台账关联[流与执行隔离契约](../planning/v16b-contract.md)及 B2 存储、独立联调、集成台账。

## 接口与数据影响

公开 API、显式继续、Run 幂等键与既有回放协议沿用 B1。续租频率属于执行配置，没有新增部署环境变量。提交确认不明时由控制写者停止外发，后续启动核对 PG 持久事实。未审候选继续留在私有内存。失权后不使用供应端迟到返回的正文、决定或 usage。

## 验证结果

- `TestRunLeaseDurationBounds` 覆盖缺省值、两个有效端点与越界值。
- `TestPostgresRunLeaseHeartbeatAndInterruptBackpressure` 使用阻塞模型和隔离 PG，观察持久截止时间实际延长，再令租约到期并在中断写入时注入 Outbox 满。检查实际调用收到取消、旧供应端仍未退出时和已退出而保存受阻时继续占位，恢复空间后仅提交一次 INTERRUPTED，末次有效前缀可读且原请求只调用一次。
- `go build ./internal/studio` 通过。
- `go test ./internal/studio -run '^(TestRunLeaseDurationBounds|TestRunIdempotencyLifecycleAndReplay|TestBoundedAdmissionAndSlowSubscriber|TestConversationMemoryBranchReplayAndCapacityPreserveHistory)$' -count=1`，四项通过，0.250 秒。
- 集成人串行执行真实 PG 专项。首轮 `TestPostgresRunLeaseHeartbeatAndInterruptBackpressure` 通过，3.92 秒，见 `.runtime/b2-focused.log`。随后补充了热事件与持久 journal 内容一致性的断言，终版定向用例 3.72 秒通过。全量结果见 [103](2026-11-21-103-run-lease-integration.md)。

## 风险与限制

通知节奏受数据库控制连接和本机调度影响，写入仍须满足数据库当时的有效期。失去控制写者或提交确认不明时需要独立启动恢复。内置租期与本机测试不能推断跨实例吞吐或实际供应商服务水平。

## 回滚方式

与 022 格式、写入栅栏共同回退。停机后从匹配版本联备恢复 PG、私有目录和已启用独立密钥，保留此前的执行、计费和审核证据。不得删除代次或迁移登记后启动旧写者。

## 文档同步

集成人负责根 README、Wiki 导航、[任务源](../planning/tasks.json)、生成的 TODO、进度和台账索引，并汇总专项与完整回归证据。
