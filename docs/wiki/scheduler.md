# 持久任务领取与执行租约

V09 已将 PostgreSQL 媒体任务从内存扫描调度改为数据库领取。实际 App 的图片/视频 worker 使用独立 PG 连接竞争到期任务，并在写入结果时验证执行权。本批保留单写控制面和本机私有目录锁；不能将这项改动称为多实例部署、高可用或供应商“恰好一次”执行。

## 当前执行边界

| 环节 | 当前实现 |
| --- | --- |
| 业务控制面 | 原 session advisory lock 的 PG 连接；受理、取消、核对、结果、资产登记、审计、Outbox 同事务写入 |
| 媒体 worker | `TaskWorkers` 个独立 PG 连接；每个连接同时领取/执行一个媒体步骤，领取和续租仅修改私有租约字段 |
| 数据库领取 | 按 `next_poll,id` 排序，`FOR UPDATE SKIP LOCKED`，只取当前 provider mode 且未终态、未停放、到期、无有效租约的任务 |
| 工作内容 | 无 handle 时只提交原受理快照；已有 handle 时只查询原网关任务；一次领取最多一次 Submit 或 Query |
| 运行结果 | 在控制连接同一事务验证 owner、epoch、数据库期限、进程 generation、原业务 version/status；成功后更新缓存并发送通知 |
| 本地开发 | memory 仍使用原有有界内存队列/扫描；没有持久租约保证 |
| LLM | 仍为进程内流式执行和周期检查点；本批不将 token 流迁到分布式日志 |

领取不依赖任务是否还在内存缓存。`SKIP LOCKED` 允许其他连接继续处理未锁行，不保证跨工作区公平；租户/模型准入继续生效，公平调度属于 V16。

## 数据与配置

迁移 `009_task_leases.sql` 增加 `studio_tasks.lease_owner/lease_epoch/lease_until`、到期任务部分索引和单行 `studio_task_executor.generation`。字段均私有，不进入任务 JSON、SSE、日志或指标标签。租约 epoch 只增不减，与公开的 Task.Version 分离；领取、释放、控制撤销都可能增加 epoch，不能将其解释为执行次数。

`TASK_LEASE_SECONDS` 默认 30，合法范围 5—300 秒；续租周期固定为期限的三分之一，不另开放环境开关。期限使用 PostgreSQL `clock_timestamp()`，不使用事务起始时间或浏览器时钟。媒体步骤总上下文仍为 2 分钟，provider 自己更短的超时继续有效。领取/续租单次数据库操作最多 5 秒。

每个媒体 worker 增加一个数据库连接，默认 4 个，另有原控制连接。启动时连接数须纳入 PG 容量预算；本批没有连接代理或多实例容量结论。`next_poll` 沿用服务端计划时间，运维仍应保持机器时钟同步；租约失效判断不依赖应用机器时钟。

## 状态与故障处理

1. 新任务受理时保存目录版本、上游模型、原参数和不可变 `MediaSubmission`。JSON 原字节或编辑 multipart 文件身份不由 worker 按当前目录重新构造。
2. 领取后，无 handle 的正常任务先将 `SUBMITTING` 和版本落库，再在外部请求前重新验证租约；完成后保存原受理 ID。提交结果含糊、进程停止或租约失效且无 handle 时不会再次 POST。
3. 到期重新领取 `SUBMITTING` 且无 handle 的任务时，写 `SUBMIT_UNKNOWN` 并停放。历史 live 任务若缺少执行快照，写 `RECONCILE_REQUIRED`。停放任务继续计入资源占用。
4. 已知 handle 的任务只查询，查询临时错误保留 handle 并按原错误分类与 Retry-After 计划后续查询。供应商状态、素材丢失、取消能力沿用[媒体网关](media-gateway.md)契约。
5. 续租失去执行权会取消该步上下文。即使 provider 忽略取消后返回成功，旧结果也不会写入业务记录、资产库或 Outbox。SQL 写入拒绝返回内部 `TASK_LEASE_LOST`，不把正常竞争误判为数据库故障。
6. 真实 worker 数据库故障使服务停止执行并失去就绪；业务提交响应不明仍按原持久层熔断，不把不明 COMMIT 当普通重试。恢复操作需要修复数据库后重启。

取消和人工核对通过可信控制事务清除租约并增加 epoch。Demo 取消后旧结果不能覆盖 `CANCELLED`；live 网关未提供可靠取消确认，仍返回取消不支持。人工核对只使用已知原 handle；不允许客户端补供应商 ID，也不创建新生成请求。

启动顺序为：取得数据库/目录写锁 → 校验存储归属 → 投影已有 Outbox → 恢复任务/中断流 → 激活新 generation 并撤销旧租约 → 启动 worker 与事件投影器。恢复未知提交同样增加 epoch；已知 handle 延续查询，旧目录停用不会改写快照。数据库迁移登记阻止旧版本程序回写新格式。

## 事件背压与外部副作用

任务结果、资产目录和 [Outbox](events.md) 共同提交。Outbox 达到上限时，初始 `SUBMITTING` 保存被拒绝就不会调用供应商。若供应商已返回 handle/结果，则在当前执行期间保留该结果、续租、释放全局状态锁后等待，只重试原保存事务，不再次 Submit/Query。步骤上下文或服务停止后仍无法确认提交，则保留原持久状态；无 handle 的恢复路径停放待核实。

LLM 同样只有 RUNNING 事务成功后才请求模型；完成结果遇事件背压时只重试终态保存，直到成功或服务停止。用户先成功提交的取消终态优先，不能被完成重试覆盖。错误响应不明仍熔断。此等待占用原 worker，形成有界背压，不额外无限创建 goroutine。

Fencing 的边界是本平台事务。上游 HTTP 不接受本平台 epoch，无法强制撤销已发出的外部请求；只对最初提交发送一次，仍使用原供应商幂等键保护。查询导入的本地/S3私有文件可能在失去租约前已创建；旧结果不会登记为可访问资产，孤立文件按[停机维护](storage-maintenance.md)规则处理，不能因此删除合法资产或重新生图。

## 验证与观测

`go test ./internal/studio -run 'Test(Postgres)?TaskLease' -count=1 -v` 最终通过 13 个函数（2个普通、11个真实PG，10.457秒）。覆盖8连接竞争只得一个 owner；到期不可续活；epoch/version/generation 拒绝旧写且资产/Outbox无副作用；取消/核对撤销；过期提交不重发；续租失效取消与迟到结果丢弃；长查询持续续租；实际 worker 执行无缓存任务；媒体/LLM 背压只重试保存。详细证据见[032](../changes/2026-11-13-032-task-leases.md)。部分函数包含多个场景。

交叉审阅补齐并验证了三处边界：结果事务持有 generation 行共享锁，轮换必须等待其完成；领取后即使控制操作已提交取消/核对，旧 worker 也不能在续租前覆盖缓存；步骤上下文期限到期不误判为全局数据库故障。缓存仅在有权事务成功后替换，拒绝旧写时不回滚其他执行者的较新缓存。

集成人最终 `npm run test:db` 通过，studio66.419秒、cmd/server0.032秒，包含上述最终代码与真实进程强制退出恢复；全仓联合证据见[034](../changes/2026-11-13-034-durable-execution-integration.md)。

`studio_task_leases_total{action="claimed|renewed|lost|rejected"}` 是本进程累计操作次数，重启清零，只经受保护的 `/metrics` 暴露；没有任务、模型、租户、提示词或凭据标签。它不是当前活跃租约数，也不是全局集群统计。

尚未实现：多实例控制面、独立跨主机 worker、跨区域任务接管、集中私有文件卷调度、租户公平优先级、任务级死信操作台、10k并发负载与长稳态演练。结果文件仍受本机目录身份和排他锁约束，不能仅增加进程副本就上线分布式执行。

## 设计依据

队列领取参考 PostgreSQL 的 [`SELECT … SKIP LOCKED`](https://www.postgresql.org/docs/current/sql-select.html)；数据库实际时间语义参考 [`clock_timestamp()`](https://www.postgresql.org/docs/current/functions-datetime.html)；控制面的 session advisory lock 语义参考[显式锁文档](https://www.postgresql.org/docs/current/explicit-locking.html)。本批没有另加 Redis/NATS/Temporal 作为第二任务真相源。
