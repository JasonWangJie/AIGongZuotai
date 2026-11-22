# 202 分片批量增量日志与并发回放 PoC

## 目的

验证 V17-I2 在固定合成输入、真实 PostgreSQL 提交和并行回放下的吞吐、排队与延迟。总体方案第 96 行的独立日志测量目标是 10,000 个逻辑 Run 和每秒 100,000 条增量。201 的真实工作台 Run、上游流与 SSE 指标独立记录。

## 范围

本项使用 H05、H07、H08 的日志契约。每个合成 Run 每秒产生 10 条固定、可逐字核验的 ASCII 文本增量。单批最多 256 条，开始收集后最多等待 8 毫秒；队列在每个分片最多接纳 1,024 条。生产者仅保留一个尚未接纳的原事件，队列满时等待下一次投放时隙，过期时隙不追发。每个分片只有一个写连接，多个读者分别并发回放不同分片。代码位于独立 Go 包，默认运行全仓测试时跳过真实数据库专项。

## 实际文件

- `internal/streamjournalpoc/journal.go` 定义 36 字节打包记录、固定分片、单批事务、epoch 栅栏、原批只读核对和有界回放。
- `internal/streamjournalpoc/postgres_test.go` 创建与清理独立随机 schema，核实普通 WAL 表和持久提交设置，并验证事务竞争、回滚、游标作用域和原批核对。
- `internal/streamjournalpoc/measure_test.go` 定义 pilot/full 规模、固定时隙、延迟直方图及非 PG 测试。
- `internal/streamjournalpoc/capacity_test.go` 协调有界生产者、分片写者和分页读者，核对最终数据库清单并生成汇总报告。

## 接口与数据影响

PoC 没有 HTTP 路由或后台服务。单独 schema 中的三张普通 WAL 表分别保存分片提交位置、每 Run 的 epoch/序号和批次正文。一次提交原子推进当前分片位置与涉及 Run 的窄水位，并写一行批次正文。独立 epoch 更新使用相同的分片行锁；同一旧序号的两个竞争事务只有一个可提交。事务中出现任何无效 Run 会回滚整个批次。生产表、业务账号、钱包、审核、模型和工作台容量配置均不参与。

应用观察到 COMMIT 成功，才向并发读者公布分片水位。每个回放页最多取四批并核对顺序、epoch 和实际文本字节。限定某一 Run 的游标还核验数据集、分片和 Run 身份。确认不明不发新写；可用预期位置和整批原字节做只读核对。专项覆盖真正提交后故意扣住应用通知，以及读回同批并拒绝相同位置上不符的原字节。

## 验证结果

开工时按当前 Go 模块复制锁定依赖，在独立目录执行 `go test -count=1 -run '^TestScheduleAndJournalFormat$' ./internal/streamjournalpoc` 通过，包用时 3.335 秒。覆盖两档逐秒排程、批内 Run/epoch/seq 一致性、正文编解码和破损检测、延迟分位上界。真实 PG 测量由集成人在同一隔离数据库串行运行。如下 PowerShell 命令继承集成人提供的 `TEST_DATABASE_URL`，两次使用不同的新报告路径。

```powershell
$env:STREAM_JOURNAL_PROFILE = 'pilot'
$env:STREAM_JOURNAL_REPORT = Join-Path (Get-Location) '.runtime/stream-journal-pilot.json'
go test -count=1 -timeout=5m ./internal/streamjournalpoc -run '^TestPostgresStreamJournal(CommitAndEpoch|Capacity)$' -v

$env:STREAM_JOURNAL_PROFILE = 'full'
$env:STREAM_JOURNAL_REPORT = Join-Path (Get-Location) '.runtime/stream-journal-full.json'
go test -count=1 -timeout=5m ./internal/streamjournalpoc -run '^TestPostgresStreamJournal(CommitAndEpoch|Capacity)$' -v
```

pilot 配置 128 个逻辑 Run，2 个分片、2 个回放连接、1 秒预热、3 秒采样，目标 1,280 条/秒。full 配置 10,000 个逻辑 Run，8 个分片、4 个回放连接、2 秒预热、12 秒恒定采样，目标 100,000 条/秒。另有一条准备与最终核验连接。记录数和各 Run 最终序号、每个读者在最后一次提交前已经读取过真实事件、分片位置和批次数均须吻合。

- `one_second_samples` 按发生时刻列出 12 个实测秒的投放、接纳、COMMIT ACK、完整校验回放、批次和页数。全程累计另含预热和排空。唯一投放只在首次形成该事件时计一次，重复尝试入队计入 `enqueue_attempts`。保留由于调度延误或队列已满产生的目标缺口。
- `latency` 以微秒记录 P50/P95/P99 和实际最大值。排队、源到提交、源到完整读回按实测期首次投放的事件取样，跟踪到排空。数据库事务延迟按实测期获得 ACK 的批次取样。直方图每个二倍区间分 32 档，分位采用档上界并封顶于实际最大值。这里的终点是日志读者，工作台网关字节到 SSE 写出的 P95/P99 另由端到端路径测量。
- `measurement_events_per_second` 表示采样窗口内的实际速率。只有接纳、提交和经验证回放的平均速率全部到达原目标且一致性通过，`measured_target_met` 才为真。`integrity_pass` 单独记录数据是否完整。保留每秒波动、最大观察队列、拥塞时隙、事务延迟和排空时间。全程完整但速率不足时，测试可以通过，报告明确保留未达标结果。
- 记录 OS/架构、Go 运行时、CPU 数、GOMAXPROCS、Go 堆前后值、GC 次数、PG 版本、WAL 配置、同步提交模式等。仅允许 `fsync=on`、`full_page_writes=on` 和能确认本机 WAL 的 `synchronous_commit=on/remote_apply`；所有读写连接另核验同步提交及 fsync。报告不保存 SQL 连接串、外部地址、真实用户标识和正文。

## 风险与限制

本机 PostgreSQL 批次日志是独立工程试验。独立集群的 NATS JetStream、跨可用区副本、断电故障、五分钟保留水位、租约到期和业务终态协调仍需独立验证。epoch 只约束试验中的执行世代；工作台继续使用 021/022 的数据库时钟、审核、取消、权益、原幂等与恢复协议。此次稳定输入是 16 字节文本，读取吞吐使用每分片一次有序扫描，单 Run 游标隔离通过单独专项核验。该结果不表示 10,000 个供应商连接、10,000 个认证浏览器、真实工具流、生产硬件容量或 24 小时稳态。

短时吞吐不足时，先对照投放缺口、分片队列、批大小、提交时间及回放延迟分布，再确定下一项实验。聚合数据不足以归因到具体存储设备、网络或数据库等待事件。

### 202 验收复核附录

初版在连接前只解析 PostgreSQL 配置，也在 CREATE 的应答前登记了 DROP。本次在 `internal/streamjournalpoc/postgres_test.go` 逐一核对 pgx 解析后的主机与全部后备主机，拒绝远端、间接服务配置和 Unix socket。localhost 在夹具内映射为两个数字回环地址，实际连接和取消请求共用的 TCP 拨号入口再次核验数字 IP 与端口。连接关闭在建库结构前就登记；只有确认成功创建以本次随机数命名的 schema 才登记该名称的 DROP。创建冲突或确认丢失时保留数据库状态。

初版统计会把只在预热期读过的读者计入并发，且只核对预建 Run 头总数。`internal/streamjournalpoc/capacity_test.go` 现另外要求每位读者在测量窗口内验证一条实际记录，并在其负责的分片中观察到更晚的窗口内提交，记录为 `readers_overlapping_commits_in_measurement`。最终数据库中的 `NonemptyRuns` 须等于配置 Run 数，完整性才通过。目标速率按每个阶段发生时刻计窗口平均，逐秒波动仍由 `one_second_samples` 给出。预热与排空不增加窗口投放配额；预热接纳、测量期确认的记录按确认时刻计入 ACK 速率。

新建 `internal/streamjournalpoc/review_test.go`。测试用替身拨号器与 SQL 执行器检查非本机主机/后备主机在实际传输前被拒、连接取消可用的地址范围、CREATE 失败后的清理和两次创建的名称隔离。纯计数用例分别覆盖预热期仅读、误用其他分片提交、预建空 Run 和有效重叠。在独立目录执行 `go test -count=1 -run '^(TestJournal(DatabaseLoopback|DialEndpoint|OwnSchemaCleanup|MeasuredReplayAndRunCoverage)|TestScheduleAndJournalFormat)$' -v ./internal/streamjournalpoc`，五项通过，包用时 0.205 秒。`go vet ./internal/streamjournalpoc` 通过。新增判断的 PG pilot/full 由集成人使用上面的命令实测。

并行回放使用同一可信数据库身份消费每个分片的已确认水位，按 Run 的回调过滤由另一个测试校验。此实验没有用户鉴权入口。WAL 证据限于普通日志表和提交设置，写入字节数、硬件刷盘时延与故障恢复需另有观测。

## 回滚方式

停止独立测试即可。每次测试只删除自己创建的随机 schema。重复测试使用新的汇总文件；业务迁移、应用配置和已有报告没有变更。

## 文档同步

由集成人依据实际 pilot/full 报告更新 V17 任务源、进度、Wiki、根 README 与台账索引。202 与 201 分别登记日志和工作台执行路径的观测。

## 独立日志 10 万条每秒正式采样

本机 PostgreSQL 16.14，fsync、full_page_writes、synchronous_commit 均开启。8 个写分片与 4 个并行回放读者，10,000 条逻辑流，预热 2 秒、正式窗口 12 秒。每秒投放与受理各 100,000 条；窗口内确认 100,025.9 条/秒、读回 100,046.8 条/秒。各阶段按其观测时刻分窗，边缘事件可跨窗。全程共 1,400,000 条，原库和回放全部对应，10,000 个流头均非空；投放至提交 ACK 的 P95/P99 上界 11.519/13.567 毫秒，投放至已确认回放的 P95/P99 上界 12.543/14.591 毫秒。两项真实 PG 测试共 14.920 秒，含原批确认、回滚和竞争检查。该结果对应每条 16 字节的固定合成增量和独立日志，不计业务鉴权、实际模型执行、SSE 网络写出或 24 小时稳态。 [逐秒结果、延迟及 WAL 设置](../wiki/assets/performance-stream-journal-poc-037.json)。
