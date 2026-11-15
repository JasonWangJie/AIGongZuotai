# 2026-11-15-081：媒体调度观察与审核公平许可

## 目的

实现 V16-A3.3 的传输事实观察和原两个审核许可的有界公平仲裁。

## 范围

关联 V16-A3.3、H03、H07、H09；不在适配器决定持久冷却，不重新提交生成，不增加审核槽位。080 负责调度持久化，根集成负责验收与任务状态。

## 实际文件

- [media_http.go](../../internal/studio/media_http.go)、[provider.go](../../internal/studio/provider.go)：网关结果和错误的私有观察，JSON 排除及既有冻结身份/原请求保护。
- [media_edit.go](../../internal/studio/media_edit.go)、[assets_local.go](../../internal/studio/assets_local.go)：身份与 source/storage 事实分类，磁盘写错误与源读取分离。
- [media_schedule_observation.go](../../internal/studio/media_schedule_observation.go)：有限事实类型、Retry-After 精确边界、超支持范围标记及分类 helper。
- [delivery_provider.go](../../internal/studio/delivery_provider.go)、[media_delivery_observation_test.go](../../internal/studio/media_delivery_observation_test.go)：原生可选真实下载观察，收据复用与本地恢复不虚构 HTTP 成功；2 个新增测试函数。
- [references.go](../../internal/studio/references.go)、[media_scheduler_reference_cancel_test.go](../../internal/studio/media_scheduler_reference_cancel_test.go)：交付前根转交的窄修正；原键二次回查后将许可等待的调用者取消/超时映射为已有安全 503，新增取消/到期 PG 回归。
- [media_moderation.go](../../internal/studio/media_moderation.go)、[media_moderation_http.go](../../internal/studio/media_moderation_http.go)、[media_review_permit.go](../../internal/studio/media_review_permit.go)：CheckObserved 与兼容包装、审核 HTTP 事实、原许可释放接线。
- [media_review_fairness.go](../../internal/studio/media_review_fairness.go)：两类及输入工作区轮转、有界等待和取消竞争清理。
- [media_review_fairness_test.go](../../internal/studio/media_review_fairness_test.go)、[media_schedule_observation_test.go](../../internal/studio/media_schedule_observation_test.go)：各 5 个新测试函数。
- [模块 Wiki](../wiki/media-schedule-transport.md)与本台账；未修改其他代理的调度、迁移或执行核心。

## 接口与数据影响

新增私有 MediaScheduleObservation{Scope,Class,HasServerDelay,RetryNotBefore,DelayExceeded}；MediaHandle/MediaResult/MediaProviderError.Observation 均 json 排除，不改原持久 handle 的摘要。成功响应未带头时的默认 3 秒不等于服务器限流；多行有效头保留最晚值，超过 24 小时或溢出不截短，交给持久调度暂停。source/storage/review 与 gateway 分离；提交和审核不新增自动重试。

交付前独立审查确认 source 成功事实未到调度，会让已降低的渠道容量无法逐步恢复。追加 ImportDeliveryItemObserved 可选方法：真实 GET、完整内容/编码及完整收据都通过才返回 source success；无下载的收据复用/本地恢复返回 nil。核心由 080 接入可选接口，仍保持原必选接口与冻结身份不变。

acquireInputPermit/acquireReviewPermit 在原 2 槽上仲裁，输入/输出交替，输入按工作区轮转；16 输入等待、每工作区 2、最多 10 秒、2 输出等待。容量错误为 operationError429 MEDIA_MODERATION_BUSY，不转换成内容 REVIEW。取消授予竞争和复制/重复 Close 保持单次释放，旧无作用域入口不能插队已有等待者。permitSnapshot 仅报告聚合计数；CheckObserved 不改变共享文本 ModerationDecision 类型，合法内容拒绝仍是协议 success。

独立复核发现参考上传等待取消会把原 context 错误传入通用 HTTP 层并误记内部 500。本批在 references.go 的原键二次回查之后，专门将 context.Canceled/DeadlineExceeded 转为 503 MEDIA_MODERATION_UNAVAILABLE；原已受理答案继续优先，不改通用 writeError 或许可仲裁。

## 验证结果

| 验证 | 实际结果 |
| --- | --- |
| 首轮原 10 个许可测试 | 通过，0.097s。 |
| 原媒体提交/编辑/许可回归 | 通过，0.215s。 |
| 首轮新公平 5 + 观察 4 函数 | 通过，10.259s，包含实际 10 秒等待到期。 |
| 加入真实审核观察后定向回归 | 通过，0.546s；多行 Retry-After 补测后观察 5 函数通过，0.066s。 |
| 最终 10 新 + 37 原函数 | 下方命令通过，11.285s；测试列表确认 47 函数。 |
| go vet ./internal/studio | 通过。 |
| npm run check:docs | 通过：179 份文档、83 份台账；链接与必需字段有效。 |
| 本模块 git diff --check | 通过，仅 Git 的 LF/CRLF 提示。 |
| 追加真实交付观察与原收据/损坏/观察回归 | `go test ./internal/studio -run '^TestMediaDeliveryObservation\|^TestMediaDeliveryReceipt\|^TestMediaDeliveryCompletedCorruption\|^TestMediaScheduleObservation' -count=1` 通过，0.241s；含真实 429、截断响应、坏编码及两种无 GET 恢复。 |
| 追加后的完整模块回归 | 原 47 函数加 2 个交付观察函数通过，11.316s；`go vet ./internal/studio` 再次通过；文档检查再次通过 180 文档/83 台账。 |
| 上传等待取消窄修正 | `go test ./internal/studio -run '^$'` 编译通过，0.032s；新增 `TestPostgresMediaSchedulerReferenceCancelledWaitIsUnavailableAndReplaysFirst` 含 cancel/deadline 子例，本代理未运行 PG，交根串行验收。 |

    go test ./internal/studio -run '^TestMediaFairPermit|^TestMediaScheduleObservation|^TestMediaReviewPermit|^TestMediaModerationHTTP|^TestAsyncMedia|^TestAsyncImageEdit|^TestMediaRetryAfter|^TestMediaTransport' -count=1
    go vet ./internal/studio

追加后完整命令在上述正则前加入 `^TestMediaDeliveryObservation|`；没有运行数据库或外部真实渠道。

并行期间曾因其他模块临时缺 mediaRuleConfig、unused time 无法编译，接齐后复验；没有修改他人文件。一次补丁含同文件两个 update 块被工具拒绝，合并后正常落盘，没有部分生效。

扩大到全部 TestMediaDelivery 普通函数时，唯一失败旧 TestMediaDeliveryMemorySeparatesDownloadFromQueries：5.02s 未进入下载，总组 16.896s。已报告核心/根集成核查显式 step 到期限制及旧测试适配，未删除或放宽断言；后续联合结果由根台账保留，本模块 47 函数成绩不冒充该项已通过。

实际覆盖：输入/输出交替、工作区 A/B 轮转、18 个总等待位置/每输入工作区 2、真实超时、100 轮授予取消竞争、真实 HTTP 在途峰值 2；两类响应与过大/溢出/过期/多行等待头、调用取消、源/磁盘分类和无自动重发。审核 HTTP 的 429/503/401/ALLOW/内容 BLOCK/坏协议六子例均保留原内容决定。

没有运行 PG、真实审核商或真实收费请求；race detector 未执行（当前环境 CGO 关闭且无 gcc），不能把普通并发用例称为 race detector 成绩。所有数据库联合验证由根集成人串行执行。

## 风险与限制

公平只保证本实例有界许可队列中的服务顺序，不保证远端吞吐或跨实例公平。长期占用原 2 槽时等待者会明确超时，不能承诺所有请求 10 秒内开始。只有输出任务的审核事实进入本批持久渠道状态；参考输入共用公平许可，但其 429 尚不更新输出渠道冷却。实际渠道仍待独立验收。

## 回滚方式

遵守整批数据库格式与联备栅栏；不得删原任务、候选、审核决定或密钥来重新提交/重审。

## 文档同步

已新增[media-schedule-transport Wiki](../wiki/media-schedule-transport.md)与本台账，079/077 等历史证据未改写；根维护 README、索引、任务源、TODO 与进度。

联合收口补记：根完整check、最终全PG 443.228s（实际版本化S3/联备启用）、六smoke、同构建回归及治理通过，详见083和联合JSON。本记录的中间待验/失败历史保留；A3.3本地验收，完整V16未完成。
