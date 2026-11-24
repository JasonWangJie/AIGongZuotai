# 240 · 受管模型执行上限与 264 路联合验收入口

## 目的

V17、H03/H07。原产品最多启动 4 个受管聊天进程，每个最多 64 个执行位。扩大明确启用的本机受管拓扑，使常规会话能在相同的控制写者、冻结上游模型和工作区预算下越过 256 个受管位，并提供可核实每个实际模型请求的联合测量入口。

## 范围

启动入口、应用配置与进程池使用同一套显式拓扑校验。`CHAT_CHILD_PROCESSES` 可选 1–8，`CHAT_CHILD_SLOTS` 可选 1–64，两项一起配置；两项省略时受管模式关闭。最高合计 512 个受管执行位。父进程继续统一分配、核验租约、处理增量确认和权益。受管槽仅用于具有冻结上下文的会话，新普通 Run 仍走本地工人。单工作区执行默认最多 2，同一冻结上游模型默认按本地工人数配置。

原联合压测新增显式档 `integrated-264`，保留原 4 路/80 路及三读者档。新档使用 5 个正式注册且有离线额度的工作区、6 个不同冻结模型、8 个各有 33 槽的真实受管进程。264 个授权读者和一个额外迟读连接，同时观察 264 个实际回环上游 HTTP 流；每个 Run 发送 12 个独立增量及终态。端口归属、租约 owner、原持久行与事务、逐帧送达、原键回放及逐笔结算均需匹配才会输出报告。

## 实际文件

- `cmd/server/chat_children_config.go`、`cmd/server/chat_children_config_test.go`
- `internal/studio/chat_child_scheduling.go`、`chat_child_pool.go`、`chat_child_protocol.go`、`chat_child_server.go`、`chat_child_scheduling_test.go`、`run_delta_coordinator.go`
- `internal/studio/chat_child_acceptance_test.go`、`chat_child_integrated_acceptance_test.go`、`chat_child_gateway_test.go`、`chat_child_moderation_acceptance_test.go`、`chat_child_observation_test.go`、本台账

## 接口与数据影响

现有配置增加合法取值。HTTP 接口、数据库格式、支付或用户权益规则无变更。工作区、模型与本地并发均按原预算。增量队列独立限制在 320 项、8 MiB，只有提交成功才能交付。

## 验证结果

Go 1.26.11 隔离 overlay 编译 `cmd/server` 与 `internal/studio`。命令解析、真实 8 个子进程握手、512 个独占槽、第 513 个拒绝、释放换票、损失一个子进程后的 448 槽、原共享公平/故障预算、264 档预算分布、联合指标、网关合同及批写边界测试通过。8 进程资源边界用例消耗约 0.35 秒，仅握手与占用执行位。标准服务已构建。正式 PostgreSQL 测量默认跳过，显式指定 264 档但缺少隔离数据库时按预期拒绝。真实 264 档由集成人串行执行。

```powershell
$trial = Join-Path $PWD ('.runtime/chat-240-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $trial | Out-Null
$env:STUDIO_CHAT_CHILD_SERVER = Join-Path $trial 'server.exe'
$env:STUDIO_CHAT_CHILD_REPORT = Join-Path $trial 'integrated-264.json'
$env:STUDIO_CHAT_CHILD_ACCEPTANCE = 'integrated-264'
go build -p 1 -o $env:STUDIO_CHAT_CHILD_SERVER ./cmd/server
if ($LASTEXITCODE) { throw '标准服务构建失败' }
go test -p 1 -parallel 1 -count=1 -timeout 12m -v -run '^TestPostgresChatChildAcceptance$' ./internal/studio
```

正式入口要求 Windows 可读取受测子进程的本机 TCP 归属，`TEST_DATABASE_URL` 指向隔离的环回 PostgreSQL，报告为新的绝对路径。受控上游只监听环回。启动 1 个控制器、8 个子进程，正常波次同时占用 264 个上游连接与 265 个用户 SSE，随后执行取消、单个子进程退出、控制器中止、原库重启及审核对照。成功条件包括 3,168 条原始增量入库、264 笔 SETTLED、各授权读者全序号/正文/终态相同、网关零重复模型 POST，及聚合延迟/落库事务数。上述数字是待执行的正式门槛。

## 风险与限制

| 容量环节 | 当前可核对的界限与待测项 |
| --- | --- |
| 调度与模型 | 受管位最多 512，本地工人最多 64；一个冻结上游跨工作区最多 64，别名共用执行预算。模型目录合计最多 32 条。源见 `chat_child_scheduling.go`、`execution_scheduler.go`、`catalog.go`。264 档使用 6 个上游各 44 条；真实供应商配额和出口另验。 |
| 增量与回放 | 单控制写者；跨 Run 批写有一个保存工作者，每事务最多 32 个 Run、256 KiB，排队及写入中合计 320 项/8 MiB，等待上限 5 秒。Run 回放最多 4 个专用读连接，每页 32 行。源见 `run_delta_coordinator.go`、`run_delta_batch_store.go`、`run_replay_pool.go`、`run_stream_replay.go`。264 档记录真实 xmin 与 P95/P99。 |
| 媒体 | 方案按 60,000 个在途任务、10 秒间隔及平均 200ms 查询计算约 6,000 查询/秒、1,200 个同时执行的查询。工作区未终态 Task 默认上限 128；查询/提交各最多 32 工人，delivery 最多 16、review 最多 2，还有工作区及渠道预算。源见 `admission.go`、`media_execution_lanes.go`。需另测真实受理至终态、查询节拍、渠道配额、队列与交付能力。 |
| 恢复和长稳态 | 原库恢复每页最多 128 条、16 MiB，活跃 Run 以中断恢复，缺 handle 的不明任务保守处理。现有恢复专项曾覆盖 1,024 个活跃 Run 和 98,976 个活跃 Task 的预置数据库。源见 `recovery.go`、`recovery_media_phases.go`、`recovery_capacity_test.go`。下一阶段需在固定制品/主机下按抵达率持续建立真实工作负载，记录 24 小时内的 CPU/RSS/句柄/GC/WAL/存储增长、恢复前后业务数据及模型链路延迟。 |

原方案在 10,000 个实际模型活跃流下要求增量 P95≤50ms、P99≤150ms。264 档的时钟从回环网关写帧前到客户端读完授权 SSE。当前扩展的配置槽、万条逻辑日志、万条项目订阅及单次恢复分别有独立口径；万活跃模型、60,000 媒体在途与 24 小时混合稳态仍待独立采样。

## 回滚方式

先停止新派发并排空或按原规则中断在途执行，确认子进程退出。将进程数配置恢复到 1–4 或移除两项配置，再使用旧制品启动。原格式、原租约中断流程及原键回放均沿用既有路径。

## 文档同步

集成人同步 V17 任务源、生成的 TODO、根 README、调度/性能 Wiki、台账索引和推进记录；264 档、万活跃模型及 24 小时稳态的结果分别登记。

## 039 真实联合测量

8 个受管进程各实际持有 33 个模型连接。6 个冻结上游各 44 个，5 个工作区同时共 264 个正常 SSE 读者及 1 个迟读者。逐一核实 3,168 个原始增量来自 110 个 PostgreSQL 提交事务；普通读者交付 P95 为 37.11 毫秒、P99 为 75.38 毫秒。264 个正常 Run 均成功结算，故障阶段保留原执行证据，零额外或重复 POST。正式用例 50.25 秒。 制品 SHA-256 为 `89cf30d8eace86ac3db5f80f87db8c432580db6d5516592c1a6d644d10851594`。[原始聚合](../wiki/assets/performance-integrated-chat-264-039.json)
