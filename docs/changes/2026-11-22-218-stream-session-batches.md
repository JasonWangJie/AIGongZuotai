# 218 扩展订阅的共享会话复核

## 目的

在 V17-J4 提高单控制器可接纳的订阅数量，同时控制会话复核对 PostgreSQL 和现有浏览器恢复请求的影响。

## 范围

H01/H05/H07。Run、项目和工作区 SSE 使用原有的资源授权、流游标、工作区预算和同一个全局槽池。默认全局容量 128，工作区默认 32。扩展档适用于单控制写者。

## 实际文件

服务配置和校验涉及 `internal/studio/admission.go`、`internal/studio/app.go`、`cmd/server/main.go`、`cmd/server/main_test.go`、`.env.example`、`scripts/lib/process.mjs`。会话复核和读池涉及 `internal/studio/stream_sessions.go`、`internal/studio/stream_sessions_test.go`、`internal/studio/account_governance_http.go`、`internal/studio/run_replay_pool.go`、`internal/studio/persistence.go`、`internal/studio/observability.go`。同步 README、Wiki、决策、进度、任务源、生成 TODO 及台账索引。

## 接口与数据影响

`ADMISSION_GLOBAL_STREAMS` 接受 1–256。超过 128 时需启用 PostgreSQL、正式会话鉴权和至少一个运行回放读连接。另启一个专用于会话复核的只读连接，核验同一数据库、schema、存储身份、当前执行代次和控制写者。复核按原会话 ID、令牌哈希、账户和凭据版本分组，从当前库检查会话有效期、用户凭据版本与账户限制。原资源授权和每个 HTTP 读者的最后游标继续独立。新增两个无标签总计数，记录完成的轮次与各轮合计的唯一会话数。沿用现有迁移。

## 验证结果

四项监控与配置专项连续四轮通过，包耗时 0.334 秒。覆盖同一会话多读者及不同会话、撤销后迟到挂载、旧查询返回时原登记已释放并由新请求登记、过期结果关闭读者、无依赖或越界配置。命令为 `go test -p 1 -count=4 ./internal/studio -run '^(TestStreamSessionMonitor|TestExpandedStreamAdmissionConfig)'`。环境解析专项通过，包耗时 0.823 秒。实际 192 个授权网络读者、不同用户/会话、读池独立性与原游标回放由补充 PG 专项测量。

## 风险与限制

每一个新 HTTP 连接仍需原认证及项目/资源授权。默认每五秒内开始一次复核；心跳更短时按其间隔执行。等连接和查询共用至多两秒的截止时间，某次无法确认时关闭该次采样的读者。独立连接用于隔离会话核对与运行回放，其生命周期跟随本机控制者。监控数据仅含总数。该档的订阅数与实际生成运行数分别计量，更多设备和 24 小时容量由 V17 后续验收。

## 回滚方式

维护窗口正常停写，恢复全局 128 或移除配置并重启；原数据库、私有文件与 SSE 游标沿用。浏览器通过原身份和原游标重连，订阅重建没有生成操作。

## 文档同步

对应 V17-J4。 [性能记录](../wiki/performance.md) · [持久流决策](../wiki/decisions/0021-durable-run-stream.md) · [任务源](../planning/tasks.json)。

## 复核失败时同步停止读者

独立审查在一个 Go 调度处理器上为同一合成会话挂载 64 个读者。复核明确返回无效后，旧实现先记录轮次，再由异步回调将取消传到各请求。连续三次在本轮复核返回时观察到 64/64 个请求的 `Err()` 仍为空。此观察验证了逐帧权限检查的时间窗口，未观察到真实用户数据泄漏。

每个会话现按已获订阅槽登记其读者的取消函数。复核失败时先标记原会话已撤销，同步取消其全部已登记请求，再记录轮次。其他凭据版本及新挂载的独立会话沿用原身份和登记对象检查。单个读者保留其 HTTP 父上下文，断开只释放自身登记。服务关闭时可在数据库校验尚未返回期间取消读者，结束监控前确认取消动作已完成。已经开始写出的帧仍受原写超时和传输层约束。

在原源码上新增的 `TestStreamSessionMonitorRevokesEveryReaderBeforeRoundReturns` 确实失败。修正后的该项和 `TestStreamSessionMonitorShutdownCancelsReadersWhileValidationIsBusy`，连同原监控、准入和回放池无数据库专项，共八项连续执行二十轮通过，包耗时 1.487 秒。命令为 `go test -p 1 -count=20 ./internal/studio -run '^Test(StreamSessionMonitor.*|ExpandedStreamAdmissionConfig|RunReplayPool(ConfigAndErrorBoundary|QueuedCancellation))$'`。本轮源码覆盖仅涉及 `internal/studio/stream_sessions.go` 与对应测试；实际 PG 网络读者与真实撤权由集成人另验。
