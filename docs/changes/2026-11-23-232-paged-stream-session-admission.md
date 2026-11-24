# 232　万级授权订阅的准入和会话分页复核

## 目的

V17、H03/H07。单控制写者原有 256 个订阅槽和 256 个会话的单次查询上限，无法承接总体方案中一万条带授权订阅的业务流。本条负责全局与工作区准入、会话自然到期、撤权和复核故障。正常模型执行、日志吞吐、SSE 网络送达及持续 24 小时的成绩分别按实测归档。

## 范围

保留全局默认 128、工作区默认 32、工作区最大 128。显式设置 `ADMISSION_GLOBAL_STREAMS` 可选 1—16,384；全局超过 128 仍需 PostgreSQL、必需鉴权及至少一个专用回放连接。扩展配置另用一条独立数据库读连接核验会话。在每个工作区最多 128 个槽的情况下，一万订阅至少需要 79 个工作区。

## 实际文件

- `internal/studio/admission.go`、`cmd/server/main.go` 共享程序与环境入口的全局上限，`.env.example` 说明配置边界。
- `internal/studio/auth.go` 在原鉴权查询中取得数据库当前时间和会话到期时间，并换算为进程单调时钟的保守截止时间。
- `internal/studio/stream_sessions.go` 为扩展订阅读者设置原会话截止时间，对活跃的不同会话逐页复核。
- `internal/studio/stream_sessions_test.go`、`cmd/server/main_test.go`、`internal/studio/stream_session_acceptance_test.go` 覆盖配置边界、万身份分页与错位授权、失败和迟到、会话过期以及原账户隔离。

## 接口与数据影响

沿用 Run、项目事件及其原游标。保留当前账户、工作区、项目、有权资源的普通鉴权，以及现有全局和工作区拒绝响应。数据库格式与原键保持原样。

每轮只对本轮开始时已订阅的不同会话取快照，每页最多 256 个身份，由同一专用连接串行执行。有效性按本页的会话散列、会话 ID、用户、凭据版本与原位置逐个对应；重新认证后新登记的同名会话有独立的登记对象。一次查询出错或返回行映射异常会关闭本页和余下未核实的原订阅。已查到失效的身份在该页返回时立即取消。现有计数器分别统计已完成轮次及实际交给核验函数的身份数。

新一轮按心跳与 5 秒中的较小值触发，全轮共用此周期与 2 秒中的较小值作为截止时间，其中包括等待专用连接和各页事务的时间。原查询超时后，即使物理连接仍在清理，后续轮次也能按自己的截止时间取消未核实订阅。每一时刻最多一个核验调用在执行或清理。自然到期按当前请求取得的数据库有效期取消；已写入浏览器或套接字的内容无法收回，进行中的写操作仍受既有 5 秒写截止约束。

## 验证结果

首次无 PG 运行中，监视器专项通过；命令行配置旧断言把全局 4,097 视为越界。按各参数实际范围分别核验，保留四类业务资源 4,096 上限与工作区订阅 128 上限。

新增纯 Go 试具登记 10,000 个不同身份，逐项核对 40 页、每页首尾的无效决定、完整序号映射与一轮共用的绝对截止。另一组用例分别让第二页失败、返回短结果和迟到，核对未验证订阅关闭、已验证订阅保留。额外阻塞一次已超时查询的清理，随后登记的会话也会到期关闭，实际执行的复核调用总数始终为一。

清除进程级 `TEST_DATABASE_URL` 后，在完整源码上用 Go overlay 执行 `go test -p 1 -count=3 -v -run '^(TestStreamSession.*|TestExpandedStreamAdmissionConfig|TestAdmission.*)$' ./internal/studio ./cmd/server`，两包分别用时 1.325 秒和 0.562 秒，三次全部通过。相同 overlay 下 `go vet ./internal/studio ./cmd/server` 通过，含新增 PostgreSQL 用例的编译检查。

集成人在隔离 PostgreSQL 串行运行 `go test -p 1 -count=1 -timeout=4m -run '^TestPostgresStreamSession' -v ./internal/studio`。新增 `TestPostgresStreamSessionNaturalExpiryBetweenPages` 在一次周期复核后将其中一人的会话设为 1.5 秒有效，要求它在下一次周期复核前自行关闭，到期后的新请求返回 401，另一账户继续读到自己的项目草稿事件。真实套接字与容量以集成人的独立报告为准。

## 风险与限制

分页快照与登记的内存上界随准入槽数增长。在数据库或运行时持续过载时，未能在整轮时限内完成复核的订阅会关闭，可携原游标在重新鉴权后读取。配置上限是保护边界；实际活跃模型吞吐、媒体在途、跨主机配额及长稳态仍须按 V17 原门槛测量。

## 回滚方式

在目标环境按既有流程停写并恢复原配置和服务版本。回退至旧制品时，全局配置需落入其允许范围。数据库、历史事件、已受理运行和游标均可沿用；客户端经原入口核验后继续读取。

## 文档同步

集成人同步根 README、Wiki、台账索引、进度、V17 任务源和生成的两份 TODO，并追加实际 PostgreSQL 与容量证明。[原 192 读者验收](2026-11-22-219-authenticated-sse-sessions.md) · [复核位置契约](2026-11-22-228-stream-session-batch-positions.md)

## 集成人在 038 的真实 PostgreSQL 预跑
`TestPostgresStreamSessionValidationPositions`、`NaturalExpiryBetweenPages`、`Shared192AndFailure`、`SlowSocketAndOriginalReplay` 和 320 个订阅的 pilot 在同一进程串行通过，包耗时 63.088 秒。80 个工作区、320 个会话全部同时在线，两轮记录 640 次会话核验，全体收到首次广播，注销读者见 EOF，持原游标读到下一版，释放后占位为零。建连 885 ms，广播 781 ms。完整一万读者另行记录。[预跑报告](../wiki/assets/performance-project-sse-320.json)
