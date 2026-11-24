# 233 · 一万个同时在线的项目 SSE 订阅

## 目的

V17 验证 232 扩展准入后的工作台项目通知。正式档建立 10,000 个同时占位、可接收持久业务通知的 HTTP/1 连接，覆盖跨页会话复核、单一会话撤销、原游标回放和最终释放。

## 范围

H01、H03、H07。80 个工作区各有 4 个不同登录会话和 125 个正式读者，共 320 个唯一会话，超过 256 个会话的单页上限。服务使用正常鉴权、项目持久通知路由与单控制写者。当前测量不发起模型生成，额外的万活跃模型执行、媒体在途和 24 小时实测另在 V17 验收。

## 实际文件

- `internal/studio/ten_thousand_project_streams_test.go` 提供本机真实服务、HTTP/1 读者、双版草稿广播、退出、原游标及汇总报告。
- `docs/changes/2026-11-23-233-project-sse-fanout.md` 记录本项范围、运行条件和实际结果。

## 接口与数据影响

第一个账户使用工作台实际注册并再登录 3 次。其余 79 个工作区的 316 个具名会话预置在该次随机创建的测试 schema，随机会话值仅在客户端 cookie jar 中使用。每个读者都向本轮服务申请项目 SSE，由正式中间件读取数据库、鉴权、准入并持续复核。注册和登录的限速配置保持原值。

正式档逐一用 80 个工作区的 `GET /api/v1/runtime` 核对在场连接数。满额后一个额外的合法订阅必须收到全局拒绝。至少两个完整复核周期须各实际处理 320 个会话。每个工作区真实保存草稿 v1，全体客户端读取属于自身范围的事件。注销持有原游标的会话后，应观察到其 32 个读者的服务端 EOF、同项目另外 93 个读者持续收到 v2，以及其他工作区保持原版本。用同一账户的有效会话提交原游标，直接读到相邻序号的 v2。最后关闭客户端并等待服务端持有数量降到零。

## 验证结果

针对 232 的隔离 Go overlay，以 `go test -p 1 -count=1` 运行项目通知观察器、新增的原游标作用域/顺序/修订边界及大规模专项。前两项通过，未提供开关时大规模专项跳过，包耗时 0.138 秒。单独显式启用 pilot 并移除数据库配置，入口按预期失败（0.128 秒），不会将该档记为通过。

集成人在上个 PostgreSQL 验收进程实际退出后，使用具备建测试 schema 权限的隔离环回 `TEST_DATABASE_URL`。先执行 pilot，再另行执行 full，各自使用尚不存在的报告文件；只有 full 的真实结果可用于一万读者验收。

```powershell
$env:STUDIO_SSE_FANOUT = 'pilot'
$env:STUDIO_SSE_FANOUT_REPORT = Join-Path $PWD '.runtime/v17-sse-320-pilot.json'
go test -p 1 -count=1 -timeout=20m -run '^TestPostgresTenThousandProjectSubscribers$' -v ./internal/studio
```

正式运行时将开关改为 `full`、报告路径改为新的 `.runtime/v17-sse-10000-full.json`。pilot 是 80 × 4 个读者，每个读者使用不同会话，同样实际越过单页。实现时没有运行 PostgreSQL 或 10,000 个连接。待追加各档的实测数、耗时和结果。

## 风险与限制

服务和客户端均位于本轮环回监听器，只接受本机隔离 PostgreSQL。真实连接受本机 TCP 端口、句柄、内存、CPU 和数据库争用限制。如建连失败，测试输出当次已建立数、目标数、HTTP 状态或底层 OS 错误号；只有完成全部核对才创建汇总报告。可用 `netsh int ipv4 show dynamicport tcp` 与 `Get-NetTCPConnection | Group-Object State` 只读核对 Windows 动态端口范围及连接状态。

若当前机器无法承载，可在一台资源独立的压测机上准备同一冻结源码、匹配 Go 版本和本机隔离 PostgreSQL，重新从上述两个入口运行并注明机器配置与制品身份。试具不接受远端服务目标，不修改系统 TCP 配置。320 与 10,000 两档均需分别记录实际运行结果。

## 回滚方式

移除本条专用试具即可，原准入配置、已受理任务和持久数据独立。测试 schema 由既有集成夹具管理。

## 文档同步

集成人同步 V17 任务源、生成的 TODO、顶层 README、Wiki、台账索引及推进记录，并分别登记正式 PostgreSQL 实测与未满足的容量目标。

## 集成人在 038 的真实 PostgreSQL 预跑
`TestPostgresStreamSessionValidationPositions`、`NaturalExpiryBetweenPages`、`Shared192AndFailure`、`SlowSocketAndOriginalReplay` 和 320 个订阅的 pilot 在同一进程串行通过，包耗时 63.088 秒。80 个工作区、320 个会话全部同时在线，两轮记录 640 次会话核验，全体收到首次广播，注销读者见 EOF，持原游标读到下一版，释放后占位为零。建连 885 ms，广播 781 ms。完整一万读者另行记录。[预跑报告](../wiki/assets/performance-project-sse-320.json)

## 共享事件页之前的一万读者基线

`STUDIO_SSE_FANOUT=full`、`go test -p 1 -count=1 -timeout=20m -run '^TestPostgresTenThousandProjectSubscribers$' -v ./internal/studio`。在每个读者单独按秒查询项目事件的 038 版本，15 分钟截止时已建成 4,358 / 10,000 条，专项失败。包退出码 1。完整广播、撤权及原游标验收未进入，未产生成功报告。

## 039 一万连接复验

039 在同一台 Windows 主机及隔离 PostgreSQL 上，以 80 个工作区各 125 位读者建立 10,000 个真实 HTTP/SSE 订阅。2 次完整复核覆盖 320 个不同会话，共 640 次；10,000 位读者全部收到自己的第一版草稿。注销读者 32 个均见 EOF，同项目其他 93 位收到第二版，原游标成功补读且最后占位归零。建连 6.846 秒、广播 1.117 秒，专项 30.04 秒。[完整报告](../wiki/assets/performance-project-sse-10000-039.json)
