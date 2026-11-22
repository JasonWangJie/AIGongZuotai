# 204 单控制写者的 Run 回放读连接隔离

## 目的

H01、H05、H08 / V17-I3。Run SSE 断线补读原先与模型增量追加、租约续期等业务共用控制连接。需要集中补读多个历史页的部署可以给回放分配有限的独立数据库会话，让一页较慢的查询不会独占控制连接。

## 范围

仅 PostgreSQL 的 `readRunStreamPage` 使用新配置。默认值为零。配置为一至四时，控制连接在绑定数据库与私有目录后为回放核对一个实际只读会话，其余槽按需建立。注册订阅、访问身份核验、冷资源加载、热通知、Run 执行及其事务仍沿用原有路径。

## 实际文件

- `internal/studio/run_replay_pool.go` 管理连接、请求期限、事务绑定、对临时断开的处理及关闭回收。
- `internal/studio/persistence.go` 复用现有单写者锁标识，提供只触发一次的停机保护，关闭回放连接后再关闭控制连接。
- `internal/studio/app.go`、`cmd/server/main.go`、`.env.example` 增加配置校验、启动和失败清理。`scripts/lib/process.mjs` 将常规隔离测试进程的默认读连接数固定为零，可由专项覆盖。
- `internal/studio/run_stream_replay.go` 将请求上下文交给回放事务，只有成功提交的整页才从存储层返回。
- `internal/studio/run_replay_pool_test.go`、`internal/studio/run_stream_replay_test.go`、`cmd/server/run_replay_config_test.go` 覆盖配置、连接、错误范围及 SSE 语义。
- `docs/api/openapi.yaml` 与 `docs/wiki/decisions/0021-durable-run-stream.md` 补充首个回放页不可用和启用方式。

## 接口与数据影响

服务端环境项 `RUN_REPLAY_READ_CONNECTIONS` 接受单个十进制数字 0 至 4。默认走原控制连接。非零时需有 PostgreSQL，并预留对应物理连接数。回放页使用同一控制数据库的连接参数；确认原主库目标后才将读会话设置为默认只读。页内在只读可重复读事务中核对库名、schema、存储 UUID、当前 Run 执行代次和原控制会话仍持有的数据库独占锁，再用同一事务核验资源所属工作区、项目、窗口与公开正文。

开启后，从等待连接到成功提交一页共用五秒期限，也服从更早的请求取消。等待超时、读连接短暂断开或数据库暂时不足仅影响该次读取。第一批响应发出前返回 HTTP 503 和 `RUN_REPLAY_UNAVAILABLE`，已开始的流则结束。客户端用原序号重新 GET。数据库绑定失效、存储头与正文不符、事件缺口沿用 `DATABASE_UNAVAILABLE` 停止执行。取消会让等待者及时离开；如驱动仍在清理旧套接字，该物理槽保持占用，清理结束才允许建立替代连接。服务关闭会收回所有槽。

没有新增表、迁移、公开写接口或数据库账号。真实租约、审核、原幂等、计费和 Outbox 的提交仍在原路径。开启该选项后，首个历史读取以外的认证和资源查找仍可能等待控制连接。

## 验证结果

在 037 隔离源码使用本机缓存的 Go 1.26.11，清除测试数据库环境变量。执行 `go test -count=1 -run '^TestRunReplay(PoolConfigAndErrorBoundary|PoolQueuedCancellation|ReadConnectionsEnv)$' -v ./internal/studio ./cmd/server`，三个非 PostgreSQL 专项通过；两个包含完整专项的包均编译成功，分别用时 0.209 秒和 0.868 秒。覆盖缺省、越界及内存配置、有界等待期间取消、临时连接与严重存储错误分类。

真实 PostgreSQL 专项需集成人在独立测试数据库串行执行 `go test -count=1 -p 1 -run '^TestPostgresRunReplayPool' -v ./internal/studio`。本记录撰写时尚未运行这些专项。测试会实际占满双读连接，核对并发原租约增量可提交、读事务前后水位不变、等待者期限、在执行的查询可取消；另测试终止指定读会话后的替换、控制会话或锁丢失、错误存储身份、执行代次变更、缺失日志和启动后续阶段失败回收。真实 HTTP 用例以同一组既有断言覆盖跨工作区拒绝、多页历史和热流交接、超前游标、旧格式与过期游标重置；独立审核专项核对许可前公开快照为空。真实容量和延迟仍须另测。

## 风险与限制

每次回放读取多一次数据库绑定检查；连接配置会增加数据库会话占用。会话按需重建，临时断开时当前页面可能需要用户或订阅客户端按原游标重新读取。现有单控制写者、执行并发和订阅数上限继续有效。此项不提供跨实例订阅、只读副本、故障自动切主或一万活跃流容量数据。

## 回滚方式

将 `RUN_REPLAY_READ_CONNECTIONS` 设为 `0` 或去掉后重启，即恢复历史页共用控制连接的方式。037 数据、原游标和持久执行证据均可继续使用。

## 文档同步

模块决策、环境样例与 OpenAPI 已补入对应说明。顶层 README、Wiki 导航、台账索引及任务源由集成人按实际 PostgreSQL 结果汇总。

## 集成人真实 PostgreSQL 验收

RUN_REPLAY_READ_CONNECTIONS 可设 1–4，默认 0。启用时，运行 SSE 回放在同一数据库绑定和控制代次下使用有界的只读重复读事务。集成人实际串行执行 7 个 PostgreSQL 专项，全部通过，29.837 秒。占满时控制写入继续，请求可取消；读连接失联、控制锁或代次不匹配、审核快照、完整性错误、旧记录、过期游标及回放切换直播均分别验证。

## 204 冻结源累计预检

截至 204 的独立冻结源码通过 npm run check，590 项前端测试、169 模块构建、Go vet 和普通测试全部完成。六套内存/本机隔离冒烟均退出 0；20 个 JavaScript gzip 侧件与最终 JS 全部逐字节吻合。scripts/perf 的 Node 测试默认 19 过、1 项条件跳过，给定本次构建 server 的 LONG_REPLY_CANCEL_TEST_SERVER 后单独重跑对应六项全部通过。含真实 PG 的长时累计测试另行计时。
