# 运行状态与可观测

本页对应 **V05**，记录本地单进程已经实现的健康端点、工作区状态、受保护指标、请求编号与 SSE 保护。准入计数和配置见[资源准入](admission.md)，接口见 [OpenAPI](../api/openapi.yaml)。这些能力不等于集群调度、生产容量验收或真实供应商健康探测。

## 存活和就绪分开

| 端点 | 当前行为 | 依赖 |
| --- | --- | --- |
| `GET /healthz` | HTTP 200、`{"status":"alive"}`；数据库失败或停止接单后仍可返回 | 仅表示进程仍能响应 HTTP |
| `GET /readyz` | 200 或 503，`{ready,state}`；state 为 `ready`、`stopping`、`database_unavailable` | 读取应用上下文和数据库熔断原子状态 |
| `GET /api/v1/runtime` | 当前认证工作区跨项目占用与上限 | PostgreSQL 聚合完整持久非终态，或内存缓存统计 |
| `GET /metrics` | 默认 404；配置独立运维令牌后可读取 Prometheus 文本 | 进程内计数和有界缓存，无供应商调用 |

就绪检查不主动 Ping 数据库或请求模型，API 的前置检查也不再额外 Ping。数据库实际查询及独立心跳仍会在错误时停止执行，禁止失联后退回内存继续受理。心跳大约每秒开始一次检测，但同一连接锁排队及最长 5 秒数据库操作超时可以延长检测窗口；`ready` 是**最近已观测状态**，不是网络此刻一定可达的证明。队列满或某个工作区额度满不直接把整个进程标为不就绪。

这些端点仍遵守当前回环 Host 与同源保护。`healthz`/`readyz`不要求账户会话，不能据此绕过站点的本地访问边界。

## 工作区状态契约

`GET /api/v1/runtime` 沿用账户会话和当前项目请求头，但统计范围是该账户的**整个工作区**。同一工作区切换项目仍看到同一组容量；客户端不能用 `workspace_id` 参数切换到别人的统计。返回示例：

```json
{
  "scope": "workspace",
  "ready": true,
  "state": "ready",
  "runs": {
    "active": 1, "limit": 64, "queued": 0, "oldest_queued_seconds": 0,
    "models": [{"model_id":"studio-chat","kind":"chat","active":1,"limit":32,"queued":0,"oldest_queued_seconds":0}]
  },
  "tasks": {
    "active": 0, "limit": 128, "queued": 0, "oldest_queued_seconds": 0,
    "models": [
      {"model_id":"studio-image","kind":"image","active":0,"limit":64,"queued":0,"oldest_queued_seconds":0},
      {"model_id":"studio-video","kind":"video","active":0,"limit":64,"queued":0,"oldest_queued_seconds":0}
    ]
  },
  "streams": {"active":1,"limit":32}
}
```

`models` 为数组。`active` 包含所有非终态：排队、执行、受理不明和待核实；终态才释放占用。`queued` 只统计当前为 QUEUED 的记录，可能包括供应商已接收但仍在排队的媒体任务。`oldest_queued_seconds` 是这些记录最早 `created_at` 至今的时长，不能解释成准确的状态停留时间或预计完成时间。`active - queued` 也不能当作真实运行数量。

工作区 API 不返回全局队列或其他工作区的活跃数。服务停止或数据库熔断时返回标准 503 错误，前端可显示上次成功快照并明确已过期；不能用猜测的占用数代替失败响应。

## 独立运维指标令牌

`METRICS_TOKEN` 留空时 `/metrics` 返回 404。启用时要求 32—256 位非空白可见 ASCII 字符，并且不能与 LLM、异步媒体 API Key、S3 SecretAccessKey/SessionToken、SMTP密码或邮件队列密钥相同。令牌只保存在服务端环境中，不进入 `VITE_*`、页面或请求查询参数。

采集器通过 `Authorization: Bearer <独立运维令牌>` 请求端点。缺少或错误令牌返回 401 和 `WWW-Authenticate`。普通用户即使已经登录，也不能读取这些全局指标。校验使用固定长度 SHA-256 摘要的常量时间比较，不记录 Authorization、Cookie 或令牌值。

响应为 `text/plain; version=0.0.4; charset=utf-8`，不缓存。指标采用固定 HELP/TYPE、累计直方图桶、`+Inf` 桶与末尾换行；这些格式约束依据 [Prometheus 官方文本规范](https://prometheus.io/docs/instrumenting/exposition_formats/)核验。

| 指标 | 含义与固定标签 |
| --- | --- |
| `studio_http_requests_total` | handler 完成的请求次数；`route`、`method`、`status`（状态类别） |
| `studio_http_request_duration_seconds` | handler 完整耗时直方图；固定 route/method 与桶边界 |
| `studio_http_errors_total` | 固定 `error_class` 分类，不使用原错误码或消息作标签 |
| `studio_active_streams` | 进程当前占用的 SSE 槽位 |
| `studio_slow_consumers_total` | `reason=buffer_full` 的订阅淘汰，或 `write_timeout` 的 HTTP 传输超时 |
| `studio_ready` | 最近观测的就绪状态，1 或 0 |
| `studio_cached_active_resources` | 有界进程缓存中的非终态资源；固定kind与模型桶（3个历史模型+3个other系列） |
| `studio_cached_queued_resources` | 缓存中当前 QUEUED 的资源；固定kind与模型桶（3个历史模型+3个other系列） |
| `studio_cached_oldest_queued_seconds` | 上述 QUEUED 记录自最早平台受理时间以来的时长 |

资源指标的 `cached` 名称有意保留：它们不执行全库聚合，**不包含已经从进程缓存淘汰的 PostgreSQL 待核实记录**。不能拿这些全局缓存数替代 PostgreSQL 准入事实；当前工作区 runtime API 会读取完整的持久占用。全库运维聚合、采集存储和告警服务尚未交付。

请求计数和延迟在 handler 返回后更新，当前采集请求会出现在后续采样中。SSE 延迟包含整个连接寿命，不能当成首字延迟或普通 API 延迟；应按固定 route 分开查看。所有计数器随进程重启清空。

## 有界基数与安全诊断

当前请求标签词表为63个路由类别（含other）、8个方法类别和5个状态类别；不认识的方法/路径统一归为 `other`。错误仅使用固定分类，模型标签保留 `studio-chat/image/video` 三个历史系列，自定义及已移除模型按固定kind归入 `model="other"` 的三个系列。直方图边界固定，存储使用固定数组；活跃工作区 SSE 计数表只保留当前连接且受全局128槽位约束，释放后删除空条目。

原始 URL、查询串、workspace/user/project/run/task ID、提示词、输出、Cookie、Authorization、供应商地址/模型名称和 API Key 均不进入指标标签。服务端生成 `X-Request-ID: req_<32位十六进制>`，忽略调用方传入的同名值；JSON 业务错误同时提供 `error.request_id`。标准静态文件或 Range 错误仍可通过响应头取得请求编号。

结构化 HTTP 日志只写服务端请求编号、固定 route/method、状态码、固定 error_class 和耗时。默认只有 5xx 用 Warn 输出，其余为 Debug，默认不产生逐请求访问日志 I/O；测试可注入启用 Debug 的 logger 核验脱敏。本批不提供全访问日志环境开关，也没有建设集中日志存储。

## SSE 与文件传输

每个工作区 SSE 槽位默认 32，可通过准入配置调整；全局仍为 128。工作区超限返回 `429 ADMISSION_STREAM_LIMIT`，全局超限返回 `503 STREAM_LIMIT`，两者带 `Retry-After: 3`。计数在连接结束时释放，错误响应写入前已经释放计数互斥锁，慢客户端不会在拒绝路径占住全局连接锁。

事件缓冲满时从订阅表移除订阅并关闭其事件通道；已有有限缓冲可被排空。每次 SSE 写入/刷新有 5 秒写期限，写超时结束连接。计数区分缓冲满淘汰与传输超时，不把普通客户端主动断线当慢消费者。

观测包装器提供 `Flush`、`FlushError`、`Unwrap`，使 [Go ResponseController](https://pkg.go.dev/net/http#ResponseController) 仍能把写期限传到原始响应器。它也保留底层 `io.ReaderFrom`，避免 `ServeContent`/`io.CopyN` 的大图片或视频传输退化；无底层快速接口时使用不会递归的复制回退。

## 已验证与未完成

2026-11-13 已通过 11 个观测测试函数：10 个不依赖数据库的功能测试，以及 1 个真实 PostgreSQL 集成测试。覆盖独立令牌与缺省关闭、输入请求编号不可冒用、120 个任意路径/方法归并、日志/指标脱敏、直方图累计及 `+Inf`、重启清空、工作区统计隔离、SSE 槽位释放、慢订阅淘汰、写期限/Flush、ReaderFrom 快路径与回退、拒绝响应不持连接锁、默认成功请求不写访问日志。

真实 PostgreSQL 测试创建两个不同账户（分别 1 和 2 个未完成运行），确认 runtime 不越权；随后关闭真实数据库写者连接，验证独立看门狗停止执行、healthz 仍 200、readyz/runtime 变为 503，错误仍有请求编号。未调用真实付费 Provider。

这些测试验证当前单进程边界，不代表万级 SSE、完整生产监控/告警、长稳压测或多区域故障演练。完整集成证据见[017 台账](../changes/2026-11-13-017-runtime-integration.md)，本模块变更见[015 台账](../changes/2026-11-13-015-observability.md)。

V06新增目录路由与自定义/历史模型固定桶，验证不会遗漏占用或泄露自由配置的模型ID，见[021台账](../changes/2026-11-13-021-model-integration.md)。V05原验收记录保留。

V07新增固定`references`、`reference_content`、`reference_thumbnail`类别，507存储满归为固定`unavailable`。文件名、哈希、reference ID及私有路径不作为标签；当前尚无每目录磁盘使用量指标。上传与预览的权限/错误标签由`npm run smoke:references`验证，详见[025](../changes/2026-11-13-025-reference-integration.md)。

V08增加library、library_entry、library_revisions、library_trash、library_restore、library_content和library_thumbnail七个固定类别。名称、标签、对象key和资产ID均不进入标签，配置回归见030。

## V09执行观测

`studio_durable_events_total{action="projected|reset"}`计本进程已成功提交的feed投影和已写出的reset；启动前drain不计在该运行循环中。它不是未消费积压、浏览器确认或全局累计。`studio_task_leases_total{action="claimed|renewed|lost|rejected"}`记录本进程成功领取/续租、续租失租和条件写拒绝。所有类别固定，资源ID、owner、epoch、cursor、名称和提示词均不进入标签，进程重启清零。

新参数`TASK_LEASE_SECONDS`为5—300的十进制秒数，默认30，续租间隔为其三分之一；参数校验失败不打印原值。它不解除单写控制面或目录锁。事件保留、背压和租约的实际验证见034。

## V10 会话路由观测

增加conversations/conversation/conversation_messages/conversation_branches/conversation_runs五个固定路由类别，总43类。标题、消息、分支ID、会话ID或私有模型映射不进入指标标签。上下文估算在已授权Run摘要里提供，不作为计费指标；上下文接入与隔离fixture验收见[038](../changes/2026-11-13-038-conversation-integration.md)。

## V11账户路由

新增9个固定路由桶：email_verification_request、email_verification_confirm、password_reset_request、password_reset_confirm、password_change、account_sessions、account_session_revoke、account_sessions_revoke_all、account_security_events；总计52个桶（含other）。标签不包含邮箱、token、会话ID或游标。SMTP状态暂由数据库受控运维查看，本批没有新增邮件送达率或逐账户指标。

## V12权益路由

新增billing_wallet、billing_entries、billing_reservations、billing_quotes四个固定桶，总56类。金额、workspace、operation key、报价与游标不成为标签；本批未增加财务或供应商成本指标。离线核账按[操作说明](billing-operations.md)检查，不拿进程HTTP计数代替账本。

## V14-A 运营路由

在V12的56类基础上，新增operations_access、operations_exceptions、operations_cases、operations_case、operations_case_events、operations_case_plan、operations_case_execute七个固定桶，V14-A交付时共63类。7个路径对应8项HTTP操作，工单列表与创建共用一个路由桶；运营功能默认关闭也不改变标签词表。用户/工作区/工单ID、提示词、输出、邮箱、供应商handle、证据标识、密码和计划摘要均不作为指标标签，不使用原始URL作为标签。HTTP计数不代表账务效果、回执或运营审计完成；相关证据见[异常工单](operations-cases.md)与[050验收](../changes/2026-11-14-050-operations-integration.md)。

## V14-C1生成权限与复核路由

当前增加10个固定桶，共73类：generation_access、generation_appeal_create、generation_appeals、generation_appeal、operations_generation_access、operations_appeals、operations_appeal、operations_generation_plan、operations_generation_execute、operations_generation_receipt。只由注册路由模板映射；工作区/限制/申请/计划ID、正文、公开答复、证据、密码或原URL均不成为标签。真实HTTP指标回归见[062](../changes/2026-11-14-062-generation-controls-integration.md)。这些计数不等于申诉解决率、已读保证或生产容量。

## V16-A1聊天执行指标

受保护`/metrics`增加11个无标签固定系列：`studio_chat_scheduler_queued`、`running`、`workspaces`、`workers_limit`、`queue_limit`、`workspace_concurrency_limit`、`model_concurrency_limit`、`workspace_queue_limit`、`dispatched_total`、`wait_seconds_total`、`wait_seconds_max`（后10项均沿用同一`studio_chat_scheduler_`前缀）。只覆盖当前进程聊天执行，running包含输出审核及终态持久背压，不等于活跃网关连接数；最大等待不是P95/P99，不记录租户、私有模型或提示词。普通工作区runtime不返回这些全局统计。详细语义见[调度Wiki](execution-scheduler.md)与[067](../changes/2026-11-14-067-execution-integration.md)。

## V16-A2媒体执行指标

增加`studio_media_lane_reserved`、`studio_media_lane_limit`、`studio_media_lane_steps_total`三个族，仅有固定`lane="submit"/"query"`两值，共六系列。reserved从领取前预留至整个步骤返回，包含辅助连接领取、上游请求、下载导入、审核及持久背压；不是网关活跃连接数。steps累计已领取执行步骤，不是POST次数、任务完成数或计费使用量；重启清零。不扫描PG全队列，不包含工作区、任务、模型、handle或原URL。普通runtime接口不增加全局信息，访问仍需要独立运维令牌。验收见[071](../changes/2026-11-14-071-media-lane-integration.md)。

## V16-A3.1交付指标

上述三族新增固定delivery值，当前共九系列。原生query只含查询与清单事务，delivery包含下载/导入、整组审核及保存；旧非phased provider仍可能在query内导入。reserved覆盖真实执行直到退出，取消不提前释放；steps不是下载件数或HTTP次数。指标访问边界和私有身份禁入不变，根真实慢下载HTTP指标测试观察delivery=1、query=0，见[075](../changes/2026-11-14-075-media-delivery-integration.md)。

## V16-A3.2首次审核指标

A3.2第四通道增加固定review值，三族共十二系列；前节九系列为v0.18口径。review的reserved包括共享许可、领取、首次审核和最终保存/退出；引用输入也使用同一审核引擎，因此此数不是引擎全部活跃请求量。steps仍是步骤次数，不等于媒体件数、审核HTTP次数或计费数量。没有新增工作区、文件、policy、attempt或URL标签。独立真实HTTP审核阻塞时，review=1而query/delivery可归零，验证进度见[078](../changes/2026-11-14-078-media-review-dispatch-validation.md)和[079](../changes/2026-11-14-079-media-review-dispatch-integration.md)。

## V16-A3.3公平与许可观测

在原十二系列之外新增23个固定系列：四lane的`studio_media_scope_reserved`、`studio_media_schedule_examined_total`、`studio_media_schedule_claims_total`各4项；`studio_media_schedule_deferrals_total`仅四lane乘capacity/cooldown两个原因，共8项；`studio_media_review_waiting`仅input/output两项，共享`studio_media_review_permits`无标签1项。这些是本进程计数和当前占用，不查询全数据库任务总量；反复遇到同一任务会重复计入deferrals，不能当独立受影响任务数。

scope_reserved覆盖工作区和冻结渠道预算，实际执行未退出不得因取消提前释放。review_permits包含输入/输出共同持有的物理槽，waiting只是有界仲裁等待；不等于远端活跃HTTP数或吞吐。没有工作区、渠道指纹、模型、任务、请求键或原URL标签。真实参考入队拒绝/原键重放与聚合指标专项已运行，最终联合验收以[083](../changes/2026-11-15-083-media-scheduler-integration.md)为准。
