# V16-A3.2 · 首次审核持久分派

状态：2026-11-15本地联合验收通过，2026-11-14开始实施。依赖A3.1已通过，075及v0.18证据保持历史事实；本批不能沿用其测试成绩标完成。完整A3.3/B/C及其他产品范围继续保留。

## 业务交接

新B2输出先在原有效任务租约事务内保存完整私有候选和首次审核READY，确认提交后结束原query/delivery步骤。review通道取得执行预算、共享审核许可、独立PG连接和新lease，事务提交STARTED且确认成功后才读真实字节并调用审核；每批最多启动一次，按原候选顺序检查整组。

所有新B2输出均走同一交接，包括demo与非phased provider；未启用审核的任务保留原终态路径。只改变已具备能力的审核调度，不新增真实审核商或公开新状态。

## 019及恢复

新增私有studio_media_review_dispatch表，保存task/scope、不可变candidate_digest/policy_digest/result_status、READY/STARTED/FINISHED、版本及一次attempt身份/开始结束时刻；不含URL、密钥、提示词或文件正文。result_status冻结SUCCEEDED/PARTIAL，避免MODERATING状态覆盖原批次交付含义。

- READY只能与首次候选同事务新增，不回填旧018候选；没有新派发证据的旧候选保持原REVIEW隔离。
- READY重启可以进行尚未开始的首次审核；STARTED提交确认丢失、宕机或重领只能转REVIEW，不再次调用审核。
- STARTED事务实际回滚且零审核外发，仍可从已提交READY取得首次执行；不能与已提交但ACK丢失混淆。
- 同一批只有一个STARTED；不提供审核到第几件后自动续审。中断、文件不一致或渠道未知整组隔离。
- 取消、失租及旧版本拒绝迟到放行。审核完成后保存背压只重试原决定，不重新读取并调用审核。
- ALLOWED整组终态、派发FINISHED、delivery COMPLETE、资产、权益、审计和Outbox同事务；未确认不得更新缓存/事件。
- READY/STARTED仍占未完成任务与冻结权益，REVIEW不自动收费或释放。财务处置不改内容决定。

019为正式增量，原001—018 SQL字节保持；旧018写者与所有离线工具精确栅栏联动。新记录不引入文件正文，GC继续保留候选/018收据全部可达项，阶段转换增加任务版本使旧维护计划失效。联备PG、私有目录/owner、交付及已启用队列密钥、对应对象版本。

## 资源边界

MEDIA_REVIEW_WORKERS默认2、显式十进制1—2。新增第四lane及独立PG辅助连接，预算在claim之前；取消及保存阻塞不提前腾出仍工作的槽。指标只增加固定review标签，不增加用户/渠道/URL标签。

复用现有mediaModerationEngine的两个审核许可，与新参考输入合计最多2个实际请求。review先非阻塞取得同一引擎许可，再claim/STARTED，审核方法持有许可执行、不二次申请；未取得许可保持READY，不写STARTED、不制造check_busy永久隔离。许可覆盖整组，执行预算覆盖最后保存和清理；不得为输出另开池放大总并发。

关闭审核或当前引擎与冻结策略不兼容时，READY明确进入隔离，不因无限等待许可挂起，也不批准旧内容。工作区/渠道公平与许可饥饿策略由A3.3验收，本批不能冒称公平性或生产吞吐提升。

## 并行文件归属

| 归属 | 文件与职责 |
| --- | --- |
| architecture / 076 | 新review_dispatch系列、019、app/lease/persistence/delivery/B2交接/ledger hook/私有加载、核心故障测试，模块Wiki与本台账 |
| go_streaming / 077 | media_moderation.go的共享许可及内部持有许可检查、专属引擎/HTTP测试；不改执行核心 |
| media_lane_validation / 078 | 维护/账本/运营/S3迁移的019精确栅栏，独立新升级/维护测试及review_dispatch_integration_test.go；不改核心或root文件 |
| root / 079 | 严格环境配置及server接线、固定观测、隔离脚本、独立联备/联合验证、版本/API、规则/README/Wiki/任务源/双TODO/台账索引 |

原A3.1测试不删除、不放宽；需要改为显式四阶段的旧断言，由root或原文件归属者集中修改并保留失败事实。PG一律按数据库串行，随机schema不能绕过全库控制锁；仅root分配PG窗口。

## 必需验收

慢审核占满时submit/query/delivery仍推进；参考输入占满共享许可时输出保持READY且审核零外发；固定worker/连接和真实请求峰值不越界，无无界等待goroutine。

真实PG覆盖READY、STARTED和最终决定的COMMIT ACK丢失、实际回滚、竞争领取、旧lease/取消、Outbox背压、进程恢复；STARTED之后不重审。图/视频/PARTIAL、整组BLOCK和权益/下载访问检查；READY文件篡改、S3读取失败、策略变更保持隔离。018→019保留旧字节/幂等/候选，旧程序拒绝；新READY可以首次执行，旧候选不得补造READY。

全量check/PG、适用真实本机S3和联备、六smoke、同构建性能及文档/TODO治理通过后才标A3.2 check passed。完整V16继续in_progress，真实渠道和生产容量另验。
