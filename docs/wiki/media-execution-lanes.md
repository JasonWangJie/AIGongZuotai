# 媒体提交与查询交付通道

040 为已登记上游 handle 的 GET 新增专用并发和 SQL 连接预算。原提交、交付和审核的限制继续适用。下文保留各阶段已测条件。[当前查询配置](media-original-query-capacity.md)

本页保留A2的双通道交付范围；当前A3.1原生异步适配器新增独立delivery通道，见[持久私有交付](media-delivery.md)。原submit/query配置和租约保护继续有效，但当前默认预算为2+2+2；旧非phased provider仍可能在query步骤内执行导入。

V16-A2 在现有单控制写者中隔离媒体提交与查询交付的执行预算、固定 worker 和租约连接。核心普通回归通过 7.529s，PG 租约/B2/lane 联合 20 函数通过 37.077s，静态检查通过；联合全量验收由根另记。实际记录见 [068 台账](../changes/2026-11-14-068-media-execution-lanes.md)；HTTP 池另见[媒体连接池](media-transport-lanes.md)。完整 V16 仍由[分批契约](../planning/v16-contract.md)及任务源管理。

## 配置

| 环境变量 | Go 字段 | 默认 | 合法范围 |
| --- | --- | --- | --- |
| `MEDIA_SUBMIT_WORKERS` | `Config.MediaScheduling.SubmitWorkers` | 2 | 1—32 |
| `MEDIA_QUERY_WORKERS` | `Config.MediaScheduling.QueryWorkers` | 2 | 1—32 |
| `MEDIA_DELIVERY_WORKERS` | `Config.MediaScheduling.DeliveryWorkers` | 2 | 1—16 |

显式空值、零、非十进制及越界值拒绝启动；未设置才用默认。Go 内部旧 `TaskWorkers=N` 仅为尚未设置的submit/query提供 `max(1, ceil(N/2))` 和 `max(1, floor(N/2))` 默认；delivery另默认2。A2历史默认总数为2+2，当前A3.1为2+2+2；旧值1也会得到1+1+2，不能解释为总并发1。新字段显式设置后优先使用，规范化后的`TaskWorkers`反映三个通道之和；没有新增公开旧字段环境变量。

这不是资源受理上限、钱包额度、供应商 QPS 或每工作区公平策略。调整容量经受控重启生效；不重新构造已受理任务的模型、凭据、正文或 handle。

## 领取和执行

当前先检查私有交付描述：READY交付只归delivery，不再Query；其余任务按已冻结的`MediaHandle.ID`分成提交和查询。不能用公开`QUEUED`状态分组，因为网关已受理任务也可能处于QUEUED。SQL使用真实序列化的大写`ID`，将null和空值归提交侧。

每步先保留对应预算，再取得该通道的 PostgreSQL 连接和领取租约；容量或连接暂不可用时直接结束本轮，任务留在原到期记录中。不会先写 SUBMITTING 再等执行容量。两组租约连接独立，提交占满不能占用查询连接。旧内部单步入口也走预算及 lane SQL 过滤；直接租约执行辅助入口有同一预算门禁。

内存模式不再使用单 `taskQueue` 或额外定时派发 goroutine。固定 worker 在短持 `App.mu` 时按 `NextPoll, ID` 从有界任务缓存选择到期、未执行的对应记录，网络调用在锁外完成。每个内存任务同时只能有一个私有执行 attempt；核对、取消后旧版本结果不能覆盖新状态，旧 attempt 也不能清理新执行标记。

预算一直保留到整步真实返回，包括供应商调用、文件导入、媒体审核、终态事务背压和心跳 goroutine 退出。用户取消或运营撤销租约不提前腾出仍有工作的预算；迟结果仍受原租约 owner/epoch/generation/预期版本保护。结果保存失败仅重试保存，不重新 Submit/Query。

## 恢复与内容、权益边界

- 无 handle 的 SUBMITTING 仍走原未知隔离逻辑，禁止自动再次 POST；内存辅助执行也不会重发这种记录。
- 已有 B2 私有候选的重新领取继续隔离，不自动重审。没有候选且原 handle 可核对时，仍只查询原任务。
- 整组审核、资产注册、权益结算、审计与 Outbox 同事务规则保持。BLOCK/REVIEW 不公开产物，财务处置不放行内容。
- 原幂等回放仍优先；本批不新增受理拒绝码、资源版本或数据库迁移，也不取消已受理任务。
- 控制写者与私有目录锁继续保留；两个连接池不构成全站多实例能力。

## 统计与限制

`a.mediaExecution.snapshot()` 并发安全且不需要业务锁，仅含提交/查询两类预算占用、配置上限及已领取执行步骤数。占用包含领取和保存阶段；步骤数可以包含无需外发的恢复隔离，不等于上游请求数。没有 workspace、model、credential、handle 或 URL。根运维接口使用固定 lane 值，不向普通工作区接口暴露全局统计。

查询交付通道仍包含 Query、私有下载导入及审核；慢下载会占用该通道，不能称为独立查询、导入、审核三段。HTTP Transport 的连接上限也不能替代 HTTP/2 请求并发预算。媒体多租户公平、阶段持久交接与容量测量留给 A3。

现有 `studio_tasks_due_leases(next_poll,id)` 是混合通道部分索引。lane 过滤仍可能扫描其他通道的到期行；A2 验证 worker、连接和执行预算隔离，不宣称数据库扫描成本或负载时延隔离。本轮未做 EXPLAIN 大样本验收。A3 应在冻结数据分布下测 `EXPLAIN (ANALYZE, BUFFERS)`，比较按 handle 非空/空划分的两个部分索引，并验证通用预备计划是否能使用其谓词；需要新索引时新增迁移，不能改写 009 历史 SQL。

所有 fixture 仅在本地隔离环境运行，不代表真实网关、生产容量或 V16 整批验收。
