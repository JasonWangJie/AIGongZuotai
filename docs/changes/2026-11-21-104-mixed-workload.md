# 104 · 双工作区聊天与媒体混合负载测量

## 目的

V16-C。在同一后端同时运行聊天与媒体，用固定负载比较健康请求、跨工作区争用和渠道暂时限流时的等待与恢复，给后续容量验证留下可复跑的基线。

## 范围

一个 Go 应用、一个真实 PostgreSQL 写者、两个经 HTTP 注册的工作区。共 6 次聊天执行、6 个原生异步图片任务。媒体提交、查询、私有下载和首次输出审核均通过本机 HTTP fixture。聊天提供方有可控返回屏障，文本审核采用本机规则。实际 PG 执行仍由集成人串行负责。

## 实际文件

- `internal/studio/mixed_workload_test.go` 增加显式选择运行的 `TestPostgresMixedWorkspaceWorkload`，采集按阶段的账号无关汇总，可写出单次 JSON。
- `scripts/measure-mixed-workload.ps1` 在 Windows 编译独立测试二进制，串行执行三轮并采集测试进程峰值工作集。集成人将实际三轮数据汇总到 `docs/wiki/assets/mixed-workload-2026-11-21.json`。
- `docs/changes/2026-11-21-104-mixed-workload.md` 记录混合试具、复跑方法、测量范围及结果。

## 接口与数据影响

复用现有聊天、媒体、审核、租约、轮询及原键受理接口。每轮使用独立测试 schema 和私有目录。健康阶段先完成 A 的一对聊天和媒体。争用阶段占住 A 的聊天、一次真实媒体 POST 的返回和首次媒体审核，在 A 形成积压后提交 B 的聊天与媒体，验证 B 的聊天完成且媒体到达待审核。A 等待队列满时观察真实 429，排空后重用原拒绝键受理。

渠道降级阶段由环回网关在 B 的原 handle GET 上返回一次 429 及 `Retry-After: 1`，验证持久冷却、真实时钟等待和同一个 handle 的成功 GET。此时 A 的一个聊天仍在执行。随后完整停止并重开应用，核对已批准媒体、原 Run 的 INTERRUPTED 与原键回放，统计每类实际调用次数。

## 验证结果

设置 `TEST_DATABASE_URL` 指向隔离测试数据库，`MIXED_WORKLOAD_TEST=1` 开启专项，`MIXED_WORKLOAD_REPORT` 可设置为单轮 JSON 的绝对路径。从仓库根目录运行 `go test ./internal/studio -run '^TestPostgresMixedWorkspaceWorkload$' -count=1 -v`。多轮使用不同报告路径且确认上一进程已退出。未开启开关时，普通测试会跳过这项测量。

Windows 上可设置环回 `TEST_DATABASE_URL` 后执行 `pwsh -File scripts/measure-mixed-workload.ps1 -Runs 3`。脚本复用同一个已记录 SHA-256 的测试二进制，输出带主机配置、每轮工作集采样和应用数据的汇总 JSON。

三轮使用同一测试二进制、PostgreSQL 16.14 和 Intel Core Ultra 7 155U。每轮两项 A 聊天排队时，A、B 各一项同时执行，B 请求至提供方入口 9.5—10.8 ms，A 第一个排队请求 3174.1—3220.0 ms。各阶段均观察到 6 个受管 PG 会话。每轮聊天执行 6 次，媒体生成 POST 6 次，状态 GET 7 次（包括一次 429），下载和首次输出审核各 6 次；同 handle 两次查询间隔 3.028—3.486 秒，整 App 重开 207—221 ms。原键没有重复外发。三个独立测试进程观测到的峰值工作集分别为 91.9、38.9、39.0 MiB，含测试客户端与 HTTP fixture。完整逐请求、各阶段通道、Go 堆与 Windows 工作集见[原始报告](../wiki/assets/mixed-workload-2026-11-21.json)。

首次运行中，Go 试具通过，但 Windows PowerShell 5.1 未保留已退出子进程的退出码，采样器以失败收尾。改为在子进程存活时保留其进程句柄，重新编译并从第一轮开始串行执行，三次均退出 0。混合试具在终版全量 PG 中再次通过，21.66 秒。

## 风险与限制

每个标签记录自客户端发起受理 HTTP 至提供方适配器入口的耗时，含受理和排队。固定屏障人为造成慢调用，单项样本适合比较本机相同条件下的结果。Go 堆、Sys、goroutine 和工作进程采样包括测试 HTTP 服务。受管 PG 会话按控制连接与四个媒体 lane 的实际后端 PID 从 `pg_stat_activity` 核对；单次采样无法说明样点间的峰值。真实渠道、万级流、长稳态及生产限额按 V17 另验。

## 回滚方式

测试退出时清理 schema 和私有目录。停用 `MIXED_WORKLOAD_TEST` 即可跳过。该试具对运行中的应用和生产数据库无变更。

## 文档同步

集成人统一更新根 README、Wiki、台账索引、[任务源](../planning/tasks.json)、生成的 TODO 和进度，三轮证据关联 [V16 综合测量契约](../planning/v16-contract.md)。
