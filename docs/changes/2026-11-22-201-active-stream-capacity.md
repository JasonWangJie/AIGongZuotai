# 201 单控制写者的活跃聊天流容量测量

## 目的

V17-I1 通过正常受理、真实 HTTP 模型增量和已登录用户的 SSE，同时占用单进程现有的 64 个聊天执行位。记录真实增量经过平台解析、PG 持久化和客户端读取的时间。8 流先行检查使用相同路径。

## 范围

H03、H07 的独立本机测量。每轮在新的 PostgreSQL 测试 schema 内注册四个账户，平衡分配两个不同的冻结上游模型和四个工作区。使用正式聊天适配器和仅当前测试进程拥有的环回 HTTP 模型。每个运行产生 12 个有序、非空且可以独立复核的合成增量。

## 实际文件

- `internal/studio/active_stream_capacity_test.go` 负责真实会话、受理、同时占位、额外队列取消、授权实时读取、独立 PG 核对、终态 SSE 重新读取与聚合报告。
- `internal/studio/active_stream_observation_test.go` 定义受控 HTTP 模型、事件核验、度量起止和百分位；含上游帧经正式解析器核对及缺失、乱序等负例。

## 接口与数据影响

通过正式 `POST /api/v1/runs`、取消和 `GET /api/v1/runs/{id}/events?after=0` 访问被测工作台。测试采用本轮规模作为全局执行位上限，每工作区及每模型分别为其四分之一和二分之一。确认全部普通读者已经接到第一个非空增量及最后一个非空增量时，分别核验执行位、模型连接、工作区与模型分布。额外接受一个排队 Run，经原接口取消，验证其未触发模型请求。独立的额外读者在一个事件后暂停读取，普通读者继续到终态；报告实际观察到的慢消费者移除数。只接受环回 PostgreSQL；生产代码、数据库格式和运行限制保持原值。

## 验证结果

037 基线配合独立测试源编译，`go test -count=1 ./internal/studio -run '^TestActiveStreamCapacity(Observation|ModelFrames)$' -v` 两项通过，0.222 秒。纯测试使用内存响应记录器和正式上游流解析器，验证 12 个增量的边界，并拒绝缺失、重复、乱序及错误结束事件。真实 PG 与容量测量由集成人串行运行。

首轮真实 8 流试跑 4.700 秒，到达活跃流和终态检查后，独立 SQL 读取失败。001 的 `studio_runs` 将状态存于 `document->>'status'`，试具误读了顶层 `status`。已修正终态和队列取消两处 SQL，保留各项正文、序号与资源归属断言，并增加仅输出固定阶段、SQLSTATE 或计数的错误分类。本轮失败没有出具通过报告。

在同一已隔离的本机测试库上，先运行 8 流，再以新的报告文件运行 64 流。各次 JSON 文件路径须为已存在目录下的新绝对路径，环境变量 `TEST_DATABASE_URL` 指向本机专用测试库。

```powershell
$env:STUDIO_ACTIVE_STREAMS = '8'
$env:STUDIO_ACTIVE_STREAM_REPORT = Join-Path (Resolve-Path .runtime) 'active-streams-pilot.json'
go test -p 1 -count=1 -timeout 6m ./internal/studio -run '^TestPostgresActiveStreamCapacity$' -v
$env:STUDIO_ACTIVE_STREAMS = '64'
$env:STUDIO_ACTIVE_STREAM_REPORT = Join-Path (Resolve-Path .runtime) 'active-streams-full.json'
go test -p 1 -count=1 -timeout 6m ./internal/studio -run '^TestPostgresActiveStreamCapacity$' -v
```

每个延迟分布列出样本量、P50/P95/P99 和最大值，单位毫秒，按 nearest-rank 计算。起点为受控模型对该增量执行 HTTP 写入前的单调钟，终点为正常客户端读完相同 SSE 事件末尾空行的单调钟。报告分别给出首次、后续及全部增量，以及创建回执、模型接入、SSE 打开、结束信号至终态 SSE、独立 PG 终态核对和终态后再次读取完整日志的耗时。每条流的序号、项目归属、固定正文和唯一终态均须通过才写汇总；报告仅含身份无关的统计值。

## 风险与限制

准入按顺序建立并等该上游真实连接进入，达到全部并发后再开放首个增量。首个增量后有一道全读者屏障和一次排队取消，后续流以至少 100 毫秒的波次间隔释放。此处测量包含两段环回传输、单写者排队和实际 PG 日志及读取，不能直接等同于从平台收包到写出的内部延迟。额外延迟读者的 TCP 或用户态缓冲可能完整容纳这些短帧；零移除应按实报告。单机短时、等额工作区/模型负载不覆盖万活跃流、十万事件/秒、24 小时稳态、真实供应商时延或生产发布。

## 回滚方式

停止独立测试并保留原始报告。测试使用自己注册的临时 schema、临时私有目录和环回进程，结束时按原 PostgreSQL 夹具清理；无业务迁移或生产配置需要回退。

## 文档同步

集成人依据 8/64 流的实际运行结果更新 README、Wiki 性能页、台账索引与 V17 任务项。容量放行、外部设备及正式渠道沿各自的验收口径跟踪。

## 037 单控制写者的 64 个活跃模型流

8 路试跑修正后通过（4.631 秒）。正式档 4 个已认证工作区、2 个上游模型，共 64 个同时活跃的 HTTP 模型流和 65 个同时打开的授权 SSE。64 个运行各读到 12 个非空增量，768 条均与 PG 终态、序号和完整 SSE 回放吻合，零遗漏、乱序或额外模型 POST。每个增量从可控本机模型写出前到普通下游读完相应 SSE 的 P50/P95/P99 为 23.01/44.42/47.23 毫秒。另一个已受理等待项已取消；额外迟读订阅暂停 1,333 毫秒，未触发剔除；全部连接与执行槽最终释放。本次活动波次 1.204 秒，专项共 5.116 秒，仅测当前单写者的局部负载。 [全部分阶段分位与计数](../wiki/assets/performance-active-streams-037.json)。
