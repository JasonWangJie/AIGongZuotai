# 本机单写者工作台值班交接

**先判定当前谁在接请求，再决定能否换版。** 发布入口处于维护期时会直接返回 503；后台进程仍可能健康。记录采样时间、入口阶段、制品摘要、应用错误码和请求编号。业务回执、对象版本、恢复点与账务核对放在受限交接记录中。指标或工单摘要只留阶段、数量和结论。

## 首次响应与复测

在有相应权限的采集终端，令 `PROBE_ORIGIN` 指向已核对归属的本机工作台地址，`METRICS_TOKEN` 由该环境的秘密配置提供。以下命令适用 PowerShell 7；访问 TLS 入口时使用受信任的本机证书。候选入口只允许指定账户和探针，指标应在已核对的后台地址采集。

```powershell
$releaseOrigin = $env:PROBE_ORIGIN
$health = Invoke-WebRequest "$releaseOrigin/healthz" -NoProxy -TimeoutSec 5 -SkipHttpErrorCheck
$ready = Invoke-WebRequest "$releaseOrigin/readyz" -NoProxy -TimeoutSec 5 -SkipHttpErrorCheck
$health.StatusCode; $health.Content
$ready.StatusCode; $ready.Content
$metrics = Invoke-WebRequest "$releaseOrigin/metrics" -NoProxy -TimeoutSec 5 -SkipHttpErrorCheck -Headers @{Authorization=('Bearer '+$env:METRICS_TOKEN)}
$metrics.StatusCode
if ($metrics.StatusCode -eq 200) {
  $wanted = '^studio_(ready |active_streams |slow_consumers_total\{|chat_scheduler_(queued|running) |media_lane_reserved\{|durable_events_total\{|credit_risk_rejections_total\{)'
  $metrics.Content -split '\r?\n' | Where-Object { $_ -match $wanted }
}
```

| 实际观察 | 接下来核对什么 | 何时继续维护 |
| --- | --- | --- |
| 后台连接失败，或 `/healthz` 未返回 200 和 `status=alive` | 对照编排者持有的进程句柄、监听端口、二进制摘要、退出状态。端口现有占用者须单独辨认。 | 找不到预期进程、监听者身份有歧义、路径/摘要不符。 |
| 后台健康 200，`/readyz` 为 503 且 `state=stopping` | 旧写者正在退出；在发布入口核对维护状态、在途请求和进程退出结果。 | 还有有限请求未结束、写响应未知、进程尚存或任一排他锁未释放。 |
| 后台健康 200，`/readyz` 为 503 且 `state=database_unavailable` | 对照该次数据库错误的请求编号和数据库日志。使用已指定的库，按有权只读连接检查库身份、锁等待、可用空间与连接状态；恢复依赖后再让匹配程序重启。 | 数据库仍在熔断、库身份或私有目录归属不清。 |
| 两个探针都 200，业务仍报错 | 用原身份读取原业务回执及私图。按下面的错误码、队列、审核、存储或权限分流。 | 验收账户仍无法取得预期的原收据/原图，或读到了另一份数据。 |
| `/metrics` 返回 401 或 404 | 401 表示独立令牌缺失/错误；后台未配置令牌时返回 404。确认请求打到正确后台、令牌版本和鉴权头。 | 仅有指标采集异常时结合业务探针判断。判断发布必需的证据缺失时继续维护。 |

就绪是进程最近观测到的数据库状态，业务请求和独立心跳才会发现后续故障。两次采样间记录进程是否重启，累计数只在同一进程生命周期可比较。[探针与指标](observability.md)

## 队列满、订阅中断与待核实任务

| 症状 | 可核实的证据与动作 |
| --- | --- |
| 业务 503 `EVENT_BACKLOG_FULL` | 本次产生新事件的事务已回滚。获准的只读数据库终端可两次执行 `SELECT pending,retained FROM studio_outbox_limits WHERE singleton;`。记录时间、是否有新写入以及 pending 的变化，再观察 `studio_durable_events_total{action="projected"}`。后者只计本进程已投影项。若停新写后 pending 持续不降或出现序号断层，保持维护，保全事件与 checkpoint。 |
| 客户端迟迟收不到事件 | 对比 `studio_active_streams`、`studio_slow_consumers_total{reason="buffer_full"}` 与 `reason="write_timeout"`。先查原资源当前版本。断线用原流的 `Last-Event-ID` 恢复；项目通知收到 `stream.reset` 时按原作用域重新读取资源。补读现有结果时保留原操作键。 |
| 聊天排队或媒体无进展 | 看 `studio_chat_scheduler_queued`、`studio_chat_scheduler_running` 和相应 `*_limit`。分开比较 `studio_media_lane_reserved` 与 `studio_media_lane_limit` 的 submit/query/delivery/review 四个 lane，再看 `studio_media_schedule_deferrals_total{lane,reason}` 中 capacity/cooldown 的变化。冷却保持原截止时间；同一任务可反复产生 deferral。通过已登录用户的 `GET /api/v1/runtime` 核对其工作区持久占用。取消后真实 I/O 或终态保存未退出时仍会占槽。 |
| 已外发但缺结果，或账本冻结为 REVIEW | 在有权限的工作区核对原 Run/Task、资源版本、原操作键与对应回执、审核状态和冻结单。存在原 handle 且服务端 `can_reconcile` 允许时，由已有核对流程查询同一任务。缺 handle 的 SUBMIT_UNKNOWN、已 STARTED 的审核、提交回执未知均保留原证据和已有冻结。涉及财务处置时按原资源开正式工单，财务独立预演并核对收据。 |
| 新收费受理被拒绝 | `studio_credit_risk_rejections_total{rule="held_limit"}` 和 `rule="rate_limit"` 是本进程的拒绝次数。核对具体项目的原报价、申请人、有效期、是否消费和冻结状态；用户救济只走原报价的受限流程。进程计数不能证明某笔额度已释放。 |

`studio_cached_*` 只统计有界缓存。队列、订阅和审核分别有容量；告警判断应对照该环境配置、同一进程的连续样本和原请求结果。[公平调度](media-scheduler.md) · [运营核实](operations-cases.md) · [内部额度核对](billing-operations.md)

## 私有文件与版本化对象

1. 先用原账户、原项目、原资源版本做受保护的 GET，分别核对原件和缩略图。若已回收或隔离，记录该状态；权限正确的拒绝不能当作文件丢失。账户应当看到原件时，比较收到的实际字节长度及 SHA-256 与已登记的身份。
2. 对应 S3 时，检查受限配置中的 endpoint、region、bucket、prefix 与原存储身份，再按收据中的原 VersionId 检查对象。相同 key 的当前版本可能已经改变。对应本地文件时，分别检查 `ASSET_DIR`、`REFERENCE_DIR` 的可用空间、权限、DB UUID 和目录角色。核对时保留已登记的原文件、完成标记和所有权记录。
3. 需要离线检查时，先停流、确认旧进程退出并取得下节三锁证据。仓库根目录的 `go run ./cmd/maintenance` 默认只生成计划；正式核账可用 `go run ./cmd/ledger --action inspect --workspace $workspaceId`。使用指定环境的独立私有配置，核对报告的格式、身份和实际缺失项。此处只用预演/检查，操作记录不附带私有文件定位或凭据。

原收据的字节、原对象版本、目录角色或所需密钥任一无法核实，保持维护并选择匹配的联备副本。[私有目录维护](storage-maintenance.md) · [交付收据](media-delivery.md)

## 同格式交接与跨格式回退

下列命令在仓库根目录运行，仅用于演练私有临时环境。两个制品目录由当班人员提供；先分别用已登记的制品清单摘要检查，再运行演练。

```powershell
node scripts/release/release-artifact.mjs verify --artifact $previousDir --role baseline --sha256 $previousManifestDigest
node scripts/release/release-artifact.mjs verify --artifact $candidateDir --role candidate --sha256 $candidateManifestDigest
node scripts/release/workbench-release-drill.mjs --previous $previousDir --candidate $candidateDir
```

1. 对照两个制品的 schema version 和迁移 SHA。相同时才可原库交接。入口进入维护后，新的普通请求应得 503。在途下载或写操作须实际结束；SSE 宽限到期可断开订阅。排空超时或有后端写回执未知，记录原请求并继续维护。
2. 等待自己启动的旧进程正常退出。确认 PostgreSQL advisory writer 锁、参考目录 OS 锁与资产目录 OS 锁能同时取得，再释放检查锁并启唯一新写者。`.studio-writer.lock` 文件在退出后可保留。任一锁不符或端口属于未知进程，停止交接并留存现场。
3. 候选先限指定账户只读访问。按固定制品身份、`ready=true/state=ready`、原业务回执及原私图字节逐项检查。匿名访问候选受限探针应被拒；未列入白名单的请求应返回 503。业务值与预期一致后才统一开放，再核对一次。任选一项失败均回到维护，按同一顺序退回已固定的兼容制品。
4. 若新版本已迁移数据库，保全停流时刻的数据库与 WAL、两类私有目录及 owner、有关对象版本、交付和已启用的邮件队列密钥。将升级前对应时间点的联备恢复到独立目录与数据库。对照数据库身份、时间线/目标 LSN、恢复点、制品格式和业务快照。按恢复点至停流的时间窗，用原操作回执、草稿/文稿版本、任务/审核及 CREDIT journal/冻结单核对新增写入；列明保留、需恢复和仍未知的笔数，由业务负责人确认处理。时间窗未核清时继续维护，保留现状副本。

2026-11-22 的 V18-B 本机联合演练中，候选能读原私图和收据，但有一条故意缺失的必验路径。入口在 2061 毫秒的交接检查后拒绝统一放行。按步骤退回原制品、放行并复核共 3478 毫秒；再次切换候选、放行并复核共 3949 毫秒。三次均实际取得数据库锁和两个目录锁。计时包括停流、停旧写者、启新写者和检查，成功项还包括开放后复核。值班时依据本次探针与锁的结果决定是否放行，生产停机目标另行测量。[本机联合演练报告](assets/release-handoff-2026-11-22.json) · [142 台账](../changes/2026-11-22-142-release-handoff-integration.md)

V18-A 本机报告中，恢复点后的草稿 v2 被排除，原账户读回 v1、四份私有字节和唯一一笔 200,000 微 CREDIT 结算。两次从开始恢复到认证读回分别为 21.016 秒和 23.910 秒。异地 RPO、RTO 与生产告警阈值须取自对应环境的独立测量与约定。[恢复命令与联备](recovery-drill.md) · [脱敏恢复报告](assets/recovery-drill-2026-11-22.json) · [迁移版本](database-migrations.md)

## 交班记录

交班时填写环境、采样起止时间、旧/新制品摘要、入口阶段、就绪状态、两次指标差值、三锁核验结果、原业务/私图核对结果、恢复点至停流的新写差额，以及尚待谁核对哪项证据。写明下一步是保持维护、核对指定回执、恢复副本或允许统一开放。仅将去敏状态与总数带入通用值班记录。

## 复核跨格式退回及同步接管

值班者可用本地演练确认退回前后能读到哪些收据，以及在何时实际释放旧写锁。跨格式退回留在维护状态，按恢复点与停流点清点差额；主备接管记录已确认写入和恢复后的可读时刻。[本机演练](recovery-drill.md)。
