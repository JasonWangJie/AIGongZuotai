# 2026-11-22-210：跨 Run 持久增量组批与逐项确认

## 目的

在多个聊天流同时产生正文时，让单控制写者把不同 Run 的增量交给同一次 PostgreSQL 保存。每个 Run 继续按自己的租约、预期序号和提交结果决定可见前缀。

## 范围

V17-J2，关联 H03 模型执行、H05 公开事件和 H07 性能。209 负责数据库事务和逐项判定，210 负责配置、受理增量、等待、生命周期、指标和非数据库回归。211 和集成人通过真实 PostgreSQL、业务入口及模型/SSE 验证联合行为。

## 实际文件

- `internal/studio/run_delta_coordinator.go` 实现单个后台保存协程、有限 FIFO、撤回和原请求结果交接。
- `internal/studio/run_delta_metrics.go`、`internal/studio/observability.go` 输出固定名称和固定类别的汇总计数、当前占用与等待时间直方图。
- `internal/studio/app.go` 接入配置核验、执行前创建、等待退出和已取得租约的未审核输出回调。
- `cmd/server/main.go`、`.env.example`、`scripts/lib/process.mjs` 解析并说明显式配置，常规隔离进程恢复默认值。
- `internal/studio/run_delta_coordinator_test.go` 验证容量、逐项收据、取消、停机及监控。
- `docs/changes/2026-11-22-210-run-delta-coordinator.md` 记录本次边界和验证。

## 接口与数据影响

`RUN_DELTA_BATCHING_ENABLED` 默认 `false`，显式启用需要配置 PostgreSQL。启用后只有成功领取 022 租约的未审核 Run 公开增量可以进入协调器。审核候选继续走原私有积累与终态审议；公开 SSE 序号和浏览器接口沿用现有契约。210 不新增数据库表或持久字段。

每个 Run 最多一条尚未确认的增量，协调器最多持有 320 项及 8 MiB 原始正文，含已经交给存储的项。容量不足时，原模型回调等待空位，后续正文继续受背压。单次工作选择 FIFO 前缀，最多 32 个不同 Run、256 KiB 正文。空闲时从最早请求算起，以 2 毫秒为选组目标，达到数量或字节界限可提前派出。尚未派出的请求等待容量和存储调度共用五秒预算，取消或超时的项从队列移除。

组批调用在 App 状态锁外执行。交给 209 之前可以确定撤回，之后即使应用或调用方已取消，也要等该 Run 的存储结果。209 明确确认后才把增量写入热缓存、发布 SSE，205 才可能确认子进程的该段。单条取消和终态写入继续使用原 streamWrite 屏障，能取得确实已提交的前缀。存储错误及确认不明不给当前请求成功结果，协调器不重投正文或模型请求。应用停机先取消，再等待协调器和执行协程实际退出，最后关闭控制写连接。

受保护的 `/metrics` 提供 `studio_run_delta_batch_commits_total`、`studio_run_delta_batch_appended_events_total`、`studio_run_delta_batch_solo_attempts_total` 和 `studio_run_delta_batch_confirmed_events_total`。前三者来自 209 的实际结果，最后一项含可确认的原单条或幂等收据。`studio_run_delta_batch_rejections_total` 的固定原因有 store、queue_timeout、cancelled、duplicate，其中 store 含确认不明。`studio_run_delta_batch_pending` 计等待和存储中项目。`studio_run_delta_batch_wait_seconds` 的样本是每条实际交库增量从申请入队到派出的耗时，包含等前批的时间，当前批数据库耗时另测。指标不含运行、租户、输入、模型或动态错误标签。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test ./internal/studio -run '^TestRunDeltaCoordinator' -count=1 -v -timeout=60s` | 通过，0.186 秒 | 在包含 204、205、206 的 037 主树，以 Go overlay 引入 209、210。四项定向测试包含条数、字节数、容量归还、去重、逐项回执及关闭时已派与未派的区别。 |
| 协调器、既有受管调度/配置、观测配置及 204 环境解析重复十轮 | 通过，studio 0.201 秒，cmd/server 0.805 秒 | 使用 `go test -count=10 -timeout=90s ./internal/studio ./cmd/server` 配合 `-run '^(TestRunDeltaCoordinator.*\|TestObservability.*\|TestManagedChat.*\|TestChatChildren.*\|TestRunReplayReadConnectionsEnv)$'`。协调器测试通过 Go 并发测试时钟确定性推进等待期限，保存结果由受控函数返回。 |
| `go test ./internal/studio -run '^TestMetricsAuthenticationRequestIDAndBoundedSensitiveLabels$' -count=1` | 通过，0.389 秒 | 既有 HTTP 指标鉴权与敏感标签门禁。新测试另外核对固定计数及直方图。 |
| `go build ./cmd/server` 后运行标准二进制 | 通过 | 非法布尔值和显式启用但缺少 PostgreSQL 均以退出码 1 拒绝启动，未打开服务。 |
| Node 导入标准隔离环境并覆盖继承配置 | 通过 | 父环境值为 `true` 时，普通 smoke 环境显式恢复 `false`。 |
| 使用最终 209 存储源码重跑同一组合二十轮 | 通过，studio 0.515 秒，cmd/server 4.012 秒 | 最终签名和提交结果计量一致。 |

本组未运行 PostgreSQL、浏览器、外部供应商或重型负载。独立协调器的选组时限、合成收据及测试时钟不代表真实 PostgreSQL 吞吐或端到端延迟。

## 风险与限制

2 毫秒是等待同批伙伴的目标，前一批正在保存时可能更晚派出。五秒只约束尚未交给存储的请求。209 按原控制写者规则在取得连接互斥后限制单次数据库操作时长，已知回滚或需要原单项核验时可能另走单条事务。其他已确认项目的结果也可能等待该次调用返回。业务终态仍依据该 Run 的实际持久前缀，活跃槽位不会因本地等待期限提前释放。该选项不增加控制写者数量、执行位或 SSE 容量。

## 回滚方式

关闭 `RUN_DELTA_BATCHING_ENABLED`，按现有单控制写者步骤排空、停止并重启。启用期间保存的增量沿用 021/022 格式，原 Run、序号、租约和账务记录保留。确认不明的请求按现有恢复流程核对，不重发模型生成。

## 文档同步

集成人根据 209、210、211 的分项结果维护 README、Wiki、台账索引及任务源，分别登记协调器验证、真实事务证据和业务负载结果。

## 集成人串行 PostgreSQL 结果

本机 PostgreSQL 的两组独立新 schema 各运行 64 条正常模型流，每条 12 个增量。开关两侧各有 768 条增量按原序通过授权 SSE 和数据库回放，模型请求各 64 次。事件行的 xmin 显示，关闭时为 768 次写入事务，开启时为 24 次，每次包含 32 个不同 Run，少 744 次（96.875%）。从模型写出前到普通客户端读完相应 SSE 的 P95 分别为 52.28 和 6.83 毫秒，P99 为 73.50 和 8.10 毫秒。在本机控制的 12 个波次下测得，事务计数只覆盖增量插入。8 路试跑 26.606 秒，64 路含业务边界专项共 28.722 秒。 [业务报告](../wiki/assets/performance-batched-deltas-037.json)。
