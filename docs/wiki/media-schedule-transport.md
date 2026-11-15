# 媒体调度观察与审核公平许可

本页记录 V16-A3.3 的适配器和审核仲裁。持久渠道冷却、任务选择与退避由[本批契约](../planning/v16a33-contract.md)定义，传输模块只报告事实，不重发生成或审核。旧[审核共享许可](media-review-permits.md)和[HTTP 通道隔离](media-transport-lanes.md)保留历史实现与验收记录。

## 传输事实

私有 MediaScheduleObservation 包含 Scope、Class、HasServerDelay、RetryNotBefore、DelayExceeded。Scope 为 gateway/source/storage/review，Class 为 success/rate_limit/temporary/protocol/identity/cancelled。Handle、Result、MediaProviderError 上的 Observation 均排除 JSON 序列化，不改变原 handle 的持久字节和身份摘要；对象中没有 URL、渠道凭据、工作区或原始错误正文。

HTTP 429 记录 rate_limit，408/5xx 为 temporary，401/403 或冻结身份不匹配为 identity，无法验证的响应为 protocol。取消不代表服务器故障。源下载与目的磁盘写入分别记录 source/storage；磁盘写失败不冒充来源网络失败，也不污染生成网关冷却。

原生交付提供可选 ImportDeliveryItemObserved，只有本次确实 GET、完整读取并验证编码，且完整交付收据验证成功，才报告 source success。完成收据复用和从已有本地原件补全收据均不制造网络成功；坏编码、截断响应、429 仍保留原失败事实。观察沿本次 context 传递，不写私有资产清单，也不增加一次下载。

Retry-After 接受非负十进制秒和 HTTP 日期。缺失/非法头仍保留旧 3 秒兼容查询间隔，但 HasServerDelay 为 false；显式 0 与过去日期也保留为真实头，最低轮询间隔由调度层约束。多行有效头取最晚时刻。支持上限为 24 小时，超过或数值溢出设置 DelayExceeded，绝不截短到 24 小时后探测；无法表示的巨大日期不伪造较早时刻。核心根据该事实暂停自动执行并保留原任务。

POST 429、5xx、断连与不完整受理响应的原业务分类不变，观察不授权重新 POST。GET 不兼容时不换模型、密钥或来源；HTTP 没有新增自动重试。STARTED 审核收到 429/未知仍保持 REVIEW，不重审。

## 审核观察

permit.CheckObserved 返回原 ModerationDecision 和独立观察，Check 继续保留兼容包装。合法 HTTP 内容 BLOCK/REVIEW 是一次成功的协议调用，Class 为 success，不能把内容拒绝当作渠道故障；429、超时及协议错误另行分类。每件仍完整校验原件与响应身份，整组观察由调度层合并最晚等待及实际失败事实。

只有输出任务的渠道观察进入本批持久调度。新参考输入尚无任务，使用相同容量公平，但本批没有根据其审核 HTTP 429 更新输出的持久渠道冷却；这项边界不能描述为所有审核来源共享持久限流状态。

## 原两个槽位上的公平

生产输入调用 acquireInputPermit(ctx,workspaceID)，输出调用 acquireReviewPermit；工作区由已认证作用域传入。输出/输入两类在同时等待时交替，输入类别再按工作区轮转，同工作区内 FIFO。无另一类等待时允许继续使用空闲槽，不预留空槽浪费容量。

输入最多 16 个等待者，每工作区最多 2 个；输出最多 2 个等待者，实际不超过 review worker。等待最多 10 秒，容量满或超时返回 429 MEDIA_MODERATION_BUSY 业务错误，不生成内容 REVIEW/BLOCK 决定；调用 context 取消返回取消错误。等待不占 App 状态锁或事务，输出须在许可返回并确认领取/STARTED后才调用审核。

参考上传在原幂等键二次回查后，将调用者取消或请求截止转换为已有 503 MEDIA_MODERATION_UNAVAILABLE，明确本次尚未受理，不误记内部 500。原已受理键即使当前 context 已取消也优先返回原答案；等待取消不会注册参考记录、调用审核 HTTP 或归还他人持有的许可。

整个引擎仍共用原两个物理许可，输入/输出总在途检查不超过 2。等待者使用调用者已有执行栈和有界通知，不启动等待 goroutine。取消与授予竞争时，未领取的许可会归还；关闭活动许可仍须等真实读取/HTTP退出。复制许可与重复 Close 保留一次释放语义。旧 Check/tryAcquire 仅兼容旧测试及内部调用，不能插队已有作用域等待者；生产路径由应用层接入作用域接口。

permitSnapshot 只提供 InputWaiting、OutputWaiting、InFlight 聚合数，不输出私有工作区标签。公平保证是当前有界队列的服务顺序，不承诺所有请求在 10 秒内完成：两个检查长时间占用时，等待请求会明确超时。跨实例、远端服务吞吐和生产容量另验。

## 验证

见[081 台账](../changes/2026-11-15-081-media-schedule-transport.md)。新增测试包括两类与工作区轮转、总队列/每工作区上限、实际 10 秒到期、100 轮授予取消竞争、真实 HTTP 峰值 2，以及网关/源/存储/审核观察、超大等待、隐私序列化与零自动重试。旧媒体协议、许可、编辑与通道测试继续执行；PG 和完整批次验收由根集成人串行执行，不以本模块普通测试替代。

2026-11-15联合验收通过：完整check、全PG 443.228s、实际版本化S3与联备、六smoke和同构建回归；详见[083](../changes/2026-11-15-083-media-scheduler-integration.md)。历史专项和边界保留，完整V16继续推进。
