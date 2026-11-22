# 211 聊天增量批量提交前后对照

## 目的

V17-J2 复测普通聊天运行的增量提交成本。对同一组模型输出，分别验证用户实时收到的正文、PostgreSQL 最终保存的正文以及断线回放，再比较两轮增量插入实际使用的事务数。老读者、审核和受理动作继续走原服务入口。

## 范围

每轮独立建立并迁移一个空 schema，注册四个工作区、使用两个冻结模型，连接同一个本机 PostgreSQL 实例。先关后开 `Config.RunDeltaBatching`，每次等待前一个服务关闭、测试清理完成。模型通过本机 HTTP 发出 12 波合成短段，各波内每个运行的内容和顺序固定。原有 201 配置和命令继续使用默认单条提交。8 路用于小档排错，正式档为 64 路同时进行的真实 API 运行。

## 实际文件

- `internal/studio/active_stream_capacity_test.go` 提取 201 的真实账户、业务受理、模型、SSE、排队取消、延迟读者、持久核验与分位测量供两轮共用。
- `internal/studio/batched_delta_acceptance_test.go` 运行对照、查询实际插入事务、核对调度器统计以及固定输出摘要，包含事务分布证据的拒绝用例。
- `internal/studio/batched_delta_rollback_test.go` 在两条真实领取的 Run 涉及的同一条多行 INSERT 上注入已知回滚，验证两条各自单独确认和回放。
- `internal/studio/run_stream_execution_test.go`、`internal/studio/moderation_execution_test.go` 复用原运行中取消、取消遭到背压、批准/阻断/待核/主动取消四类输出审核断言。
- `internal/studio/durable_process_test.go`、`internal/studio/run_stream_recovery_test.go` 将开关传给实际进程，保持原热读者连接到退出，在恢复之前从独立 PG 连接核对故障事务的原行。
- `docs/changes/2026-11-22-211-batched-delta-acceptance.md` 记录本次验收口径。

## 接口与数据影响

复用已有普通运行受理、取消、授权 SSE 与原键重放接口，211 仅改测试和文档。开启生产批量写入的开关归 210。回滚夹具的语句触发器、计数序列和临时锁仅属于独立测试 schema。为确定两条模型回调进入同一待测事务，只在这一个故障子用例中将组批窗口放宽至 250 毫秒。容量对照使用产品组批窗口。

每轮按此次真实运行 ID 和 `name='message.delta'` 限定数据，用独立连接的 `count(*)`、`count(distinct run_id)`、`count(distinct xmin::text)` 与按 xmin 分组的每事务事件数、不同 Run 数互校。关闭时要求每行一个事务。开启时至少一个事务必须包含多个不同 Run，且实际增量事务数须严格下降。新段经顺序和逐字节核对后再计算两轮可比的 SHA-256，报告只保存固定摘要与汇总统计。

## 验证结果

在隔离目录以 209、210 实际源文件作为 Go 编译 overlay，关闭模块代理，清除 PG 地址和验收开关。以下定向测试通过，包耗时 0.236 秒；补入原热连接在故障前仍打开的检查后，终版定向复跑 0.231 秒。两条显式 PG 入口在无开关时均跳过。事务分布校验执行八组有效和拒绝用例，另执行 201 的 SSE 解析与模型帧测试。

```powershell
go test -p 1 -count=1 -overlay .\validation-overlay.json ./internal/studio -run '^Test(BatchedDeltaTransactionEvidence|ActiveStreamCapacity(Observation|ModelFrames)|PostgresBatchedDelta(Acceptance|BusinessBoundaries))$' -v
```

实际 PG 专项由集成人在其本机隔离实例串行执行。对已经合入 209、210、211 的源码，保持原 `TEST_DATABASE_URL` 指向获准的本机测试数据库，报告路径须绝对且尚未存在。每次正式命令均独立运行两个开关状态。

```powershell
$env:STUDIO_DELTA_BATCH_STREAMS = '8'
$env:STUDIO_DELTA_BATCH_REPORT = Join-Path (Resolve-Path .runtime) 'delta-batch-211-pilot.json'
go test -p 1 -count=1 -timeout 12m ./internal/studio -run '^TestPostgresBatchedDelta(Acceptance|BusinessBoundaries)$' -v

$env:STUDIO_DELTA_BATCH_STREAMS = '64'
$env:STUDIO_DELTA_BATCH_REPORT = Join-Path (Resolve-Path .runtime) 'delta-batch-211-full.json'
go test -p 1 -count=1 -timeout 12m ./internal/studio -run '^TestPostgresBatchedDelta(Acceptance|BusinessBoundaries)$' -v
```

`Acceptance` 报告包含每轮实测活动流/POST 数、首段、后续及所有段的端到端分位、增量事务节省比例、吞吐、排队等待分布、超时/拒绝/待处理数与迟读者情况。吞吐取所有普通读者收到的非空段数除以首波开放至末条抵达的实测时长。端到端 P50/P95/P99 用同一单调钟从每个模型段写出前计到授权 SSE 客户端读完该事件空行，分别报告首段、后续和全部。

`BusinessBoundaries` 检查同批两条数据库增量实际回滚后只存原段、没有提前对热读者发布、原键仍指原运行；运行中取消等待已经开始的提交，取消已知回滚后原调用仍能完成。输出审核期间候选保持私有，四类终态与原断言一致。进程故障覆盖第二段提交前终止与 PostgreSQL 已提交、客户端丢失确认，故障前热 SSE 不显示待确认段，重启后从数据库恢复原前缀，原键重放不会再调用模型。该进程用例只驱动一个运行，实际跨 Run 提交确认故障由 209 存储专项独立验证。PG 命令和 JSON 均须由集成人核实后追加实测事实。

## 风险与限制

比较仅统计本轮新插入的增量行。启动、领取、快照、终态和项目 Outbox 等事务不在分母中。固定每波至少 100 毫秒的间隔保留在吞吐窗口内。先关后开的单机对照会受到缓存与系统负载影响，延迟分位照实记录。迟读者使用真实网络连接但短帧可能全部留在本机缓冲区。此次容量上界为 64 个同时执行的模型请求，未测万活跃、持续峰值、真实供应商或生产站点。

## 回滚方式

停止测试，保留已产生的报告。测试清理独立 schema 和私有临时目录，原有 201 测量仍可独立运行。生产批量写入按 209/210 的规则关闭后回到原单条追加路径。

## 文档同步

集成人依据正式实测更新 README、性能 Wiki、台账索引及 V17 任务进度，并登记两轮完整分位、事务数和业务故障专项结果。

## 集成人串行 PostgreSQL 结果

本机 PostgreSQL 的两组独立新 schema 各运行 64 条正常模型流，每条 12 个增量。开关两侧各有 768 条增量按原序通过授权 SSE 和数据库回放，模型请求各 64 次。事件行的 xmin 显示，关闭时为 768 次写入事务，开启时为 24 次，每次包含 32 个不同 Run，少 744 次（96.875%）。从模型写出前到普通客户端读完相应 SSE 的 P95 分别为 52.28 和 6.83 毫秒，P99 为 73.50 和 8.10 毫秒。在本机控制的 12 个波次下测得，事务计数只覆盖增量插入。8 路试跑 26.606 秒，64 路含业务边界专项共 28.722 秒。 [业务报告](../wiki/assets/performance-batched-deltas-037.json)。
