# 172 · 双工作区混合负载的 GC、数据库等待和订阅背压观测

## 目的

V17-C 补齐 [104 混合负载](2026-11-21-104-mixed-workload.md) 的资源观测。在相同的六次聊天、六个媒体任务和六个阶段内记录 Go 回收、PG 会话状态和等待类型。两项单列的小实验给出故意制造等待与慢读时的可核对结果。

## 范围

沿用两工作区、原生异步媒体、审核、真实 PostgreSQL、一次限流后按原 handle 恢复和重开后原键重放。正常请求、同工作区拒绝、另一工作区取得进展及供应方调用次数仍按原断言验收。额外实验排在最后一个阶段采样之后。

## 实际文件

- `internal/studio/mixed_workload_test.go` 在原有样点记录 GC、PG 及进程内慢订阅计数，给两次启动的应用分配各自的连接标记。
- `internal/studio/mixed_workload_resources_test.go` 实现观测窗口、连接身份核对、受控锁等待、快慢热订阅者。
- `internal/studio/mixed_workload_observation_test.go` 验证 GC 环形历史边界、报告标签、回环目标与订阅队列的顺序和容量。
- `docs/changes/2026-11-22-172-mixed-resource-observation.md` 记录复跑方式和证据口径。

## 接口与数据影响

沿用原测试名及报告入口。每轮 JSON 的 `samples[]` 仍有六个按顺序命名的样点和 `elapsed_ms`。每个样点新增三类数据。

- `gc_since_previous_sample` 以同一 Go 进程两次 `runtime.ReadMemStats` 之差记录完成周期数和暂停总纳秒。注册两个账户后的首个样点建立零基线。每个完成周期的停顿合计来自 `PauseNs`，最多取本窗口内最近 256 个周期，分别报告样本数、已观察最大值及环覆盖标志。Go 重开 App 时继续沿用原 GC 窗口。
- `managed_pg_activity` 报告登记及实见连接数、`state_counts` 和 `wait_event_type_counts`。固定类别只列非零项，空等待记为 `none`，未知类型归 `other`。逐次核对本次 App 标记、实际连接 PID、后端启动时刻、数据库、用户与 client backend 类型。新 App 单独登记。应用控制写连接和四个媒体 lane 池纳入计数，观察连接及其他客户端排除。报告不输出连接身份、SQL、原始等待名称或 DSN。
- `app_slow_buffer_full_total` 和 `app_slow_write_timeout_total` 为当前 App 自创建起的有界队列溢出及写超时累计值。重开后的计数从新实例开始。

`controlled_pg_lock` 使用两条登记的独立试具连接。等待者实际处于 `active/Lock` 且 PG 确认阻塞者是指定试具连接后释放事务锁，再核等待者已完成且回到 `idle`。记录前后分类计数及等待语句端到端毫秒，时间含客户端往返和观测开销。`controlled_run_fanout` 通过实际 Run 热订阅的登记与发送代码，同时驱动立即读取者和停读者。分别核对事件序号、断开前实存条数、队列容量和一次溢出剔除。专用测试记录不进入业务历史。

## 验证结果

- `go test -count=1 ./internal/studio -run '^TestMixedObservation' -v` 通过三个无数据库专项，包耗时 0.229 秒。覆盖非零历史基线、跨环及覆盖、零 GC、闭集标签、目标限制、健康读者保持有序和停读者达容量后剔除。
- 真实 PG 由集成人在其他 PG 进程退出后串行执行。Windows 在配置隔离回环 `TEST_DATABASE_URL` 后运行 `./scripts/measure-mixed-workload.ps1 -Runs 3`，报告写入当次私有 `.runtime/mixed-workload-*/report.json` 的 `trials[].workload`；脚本继续记录测试进程的峰值工作集。单轮可设置 `MIXED_WORKLOAD_TEST=1`、`MIXED_WORKLOAD_REPORT` 为独立输出文件，运行 `go test -p 1 -count=1 -timeout 2m ./internal/studio -run '^TestPostgresMixedWorkspaceWorkload$' -v`。本台账编写时尚无本批真实 PG 数字。

集成人运行三个窄测，0.141 秒通过，随后执行 `scripts/measure-mixed-workload.ps1 -Runs 3`，三个子进程全部成功退出。三次独立真实 PostgreSQL 混合负载覆盖 18 个阶段快照，每个快照均核对 6 个进程所属数据库连接。三个受控锁等待均从 Lock 恢复 idle，等待语句耗时 11.09、11.05 和 12.26 毫秒。每轮持续读取者按序收到 66 条事件，停读者缓冲到 64 条后只移除一次。 六阶段累计观测的进程 GC 周期分别为 25、23、23 次，暂停计数器差值分别为 2.2382、0、1.5915 毫秒。第二轮实测计数器差值为零，实际记录精度不足以据此推断没有短暂停。阶段快照与刻意阻塞的语句分别统计。 [完整原始报告](../wiki/assets/performance-mixed-resources-033-2026-11-22.json)

## 风险与限制

六次 PG 快照容易错过短等待，空等候或 `Client` 可来自正常空闲连接。GC 归属以运行时发布计数的采样边界为准，包含同进程测试 HTTP 服务，不能将跨边界的回收停顿精确分摊给单个请求。两条独立试具连接的刻意锁争用和两个本地热订阅通道仅验证等待观测及背压行为。该实验未测网络慢读写超时、PG 服务进程资源、自然业务锁等待分布、生产 P99、万级活跃流或 24 小时稳态。

## 回滚方式

维持默认未启用的 `MIXED_WORKLOAD_TEST` 即跳过混合试具。本变更全部位于测试和文档，单独移除新增观测可恢复 104 报告结构。锁实验结束时回滚测试事务，关闭专用连接。

## 文档同步

集成人按实际运行结果统一更新根 README、[性能 Wiki](../wiki/performance.md)、[任务源](../planning/tasks.json)、TODO、进度与台账索引。
