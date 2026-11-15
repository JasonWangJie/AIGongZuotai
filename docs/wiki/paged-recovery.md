# 分页恢复与启动背压

实施中，尚未完整验收。对应[A4契约](../planning/v16a4-contract.md)和[086台账](../changes/2026-11-15-086-paged-recovery.md)，复用[085私有阶段批读](recovery-private-phases.md)。

启动先检查控制写锁、私有目录归属与必要密钥，然后用短事务切换媒体generation，撤销旧执行授权；旧lease按稳定ID每128项独立事务清理。辅助连接和worker不在这个过程中执行生成。随后分页恢复项目/Run/Task，全部成功后才开放工作线程与HTTP。

每页最多128项，基础JSON与私有元数据累计16 MiB。SQL窗口按ID计算基础正文前缀体积，不合本页的正文返回NULL，下一页从最后实际完成ID继续；单条超限明确失败，不跳过资源。私有阶段预算不足只缩小只读页，不触发数据库熔断或重发外部请求。

每个需要恢复的资源继续使用原事务钩子，在各自5秒内原子保存业务、账务、审核、调度与通知。只有确认提交才安装缓存和推进游标。页间消费Outbox；若保存遇到EVENT_BACKLOG_FULL，先实际回滚、退出store.mu，再排空队列并读取未提交记录，避免重复递增版本。提交确认不明立即退出，由下次启动读持久事实，不盲目复用修改后的对象。

未完成LLM仍转INTERRUPTED，未审内容隔离；无handle的SUBMITTING仍转SUBMIT_UNKNOWN。原handle、交付清单、收据、READY/STARTED、原模型及冷却截止时间都保持原规则。恢复不授权重POST、下载替换收据或重审已STARTED候选。

私有recoveryStats记录实际页数、最大编码页与缓存条数，仅供本地验证；不是公共容量指标。A4.1仍保留全缓存，A4.2才处理pin/冷读/全局512误限。旧validateMediaDeliveryStore仍要扫描必要密钥状态；整体十万New容量另验。
