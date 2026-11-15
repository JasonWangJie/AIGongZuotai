# 媒体审核受理、交付与恢复

本页对应V14-B2的后端执行链。审核引擎、media-v1协议和016表约束见[媒体审核](media-moderation.md)，参考上传与内容入口由[联合集成台账058](../changes/2026-11-14-058-media-review-integration.md)记录。默认关闭；本地fixture和HTTP模拟器不能代替真实审核商验收，完整V14仍进行中。

## 启用与兼容

`MEDIA_MODERATION_MODE=fixtures|http`要求PostgreSQL、`AUTH_MODE=required`和已启用的文本审核。任务prompt经过文本引擎，编辑的有序参考原件及生成结果经过媒体引擎。只有文本审核时，新媒体仍返回422 `MODERATION_MEDIA_UNSUPPORTED`；两者都关闭时保留原行为。bootstrap使用`media_supported/media_mode/media_policy_id/media_policy_version`说明能力，不下发私有政策规则或凭据。

先查原幂等键，再检查当前开关、模型、输入和额度。已受理任务用原政策、模型、参数和不可变提交体，不因修改配置重建请求。旧无审核摘要的资源不追溯审核，也不标成已审核；旧参考用于新的受审编辑时，仍须按新受理政策读取并检查真实原件。关闭当前审核不能放行原隔离候选。

演示审核专用Provider导入仓库的真实PNG和短MP4到私有存储，不用SVG或视频海报代替媒体字节。prompt标记只选择演示样本，决定来自完整字节的审核；样本不是AI生成结果。原未启用审核的演示链不变。

## 两阶段受理

第一次进入`createTaskApproved`完成原键查询、项目归属、模型/参数和引用校验，取得有序引用身份。`taskMediaModerationAdmission.check`在状态锁和数据库事务外检查prompt及每张参考图的真实原件，使用同一文件描述符校验大小、SHA256和格式。输入审核不直接创建Task、不消费报价或冻结额度。

第二次进入受理流程重新查询原键，并重核权限、模型、完整规格、引用和准入。独立摘要覆盖规范化执行输入、私有上游模型及有序参考字节身份；它不替换原HTTP幂等hash。摘要变化返回409 `MEDIA_MODERATION_CONTEXT_CHANGED`。成功后，Task、幂等、冻结政策、初始PENDING摘要、适用的额度冻结、业务审计和Outbox在同一事务写入；提交确认后才进入执行链。

| 前置结果 | HTTP / code | 本次含义 |
| --- | --- | --- |
| 媒体阻止 | 422 `MEDIA_MODERATION_INPUT_BLOCKED` | 未受理生成 |
| 需要核实 | 409 `MEDIA_MODERATION_INPUT_REVIEW` | 未受理生成 |
| 格式或审核大小限制 | 422 `MEDIA_MODERATION_INPUT_INVALID` | 未受理生成 |
| 审核槽位繁忙 | 429 `MEDIA_MODERATION_BUSY` | 未受理生成 |
| 无法取得可信审核结果 | 503 `MEDIA_MODERATION_UNAVAILABLE` | 仅用于前置未受理；后置失败写Task摘要 |

prompt仍使用文本审核的固定错误码。客户端已有受理不明操作时，任何后续前置错误均不能证明先前请求没被接受，仍须保留原body/key核对。

## 私有候选与整组交付

生产PG媒体worker保留原handle查询、私有导入与租约。供应商返回SUCCEEDED/PARTIAL后，按以下顺序处理：

1. 校验产物数量、唯一ID、类型和私有存储身份，暂不赋给公开`Task.Artifacts`。
2. 有效租约条件更新Task为`MODERATING`，进度99、审核PENDING v1；同事务写不可变私有候选，只有安全状态通知可见。
3. 在锁外逐件读取完整原件并审核。候选绑定原件hash、长度、类型及存储身份/对象版本；整个外部检查期间维持续租。
4. 全部ALLOWED才允许原供应商SUCCEEDED/PARTIAL结果交付。任一BLOCKED或REVIEW使整组FAILED且产物仍不可见，不能先发布通过的前缀。
5. 最终事务再次验证owner、epoch、generation、有效期及业务版本，随后写安全决策/历史、Task、可交付资产目录、适用账务与Outbox；确认提交后才替换缓存和发布状态。

公开摘要固定为`moderation:{policy_id,policy_version,phase:'output',state,version,reason_code?}`，决策版本PENDING v1到单次终决策v2；它与Task.version分别单调。公开读取还有防御投影：有摘要的Task只有`ALLOWED`且`SUCCEEDED|PARTIAL`才可含Artifacts。私有候选包括存储路由身份，但不能作为公开下载授权。

权益按真正注册交付的产物计量：全组允许后，供应商PARTIAL可按实际交付数量结算并释放剩余额度。已开始但审核阻止、未知或执行中断保持REVIEW，不以供应商完成推定已经交付。未开始的取消仍沿用原未提交释放规则。运营财务处置不改变审核决定，也不能注册隔离产物。

## 取消、租约和恢复

取消和运营控制写撤销旧租约。迟到ALLOW不能越过版本/epoch/generation条件，更不能复活已取消任务。丢失租约的worker不发布结果；候选写入后重领只停驻REVIEW，不重复查询、审核或生成。

重启看到PENDING且已有候选时，同事务将任务变为FAILED、摘要REVIEW（`process_restarted`），保留原候选并关闭重新核对。没有候选且有原handle的受理任务仍沿原查询恢复规则；无handle的未知提交绝不重发POST。已有终决策按数据库恢复，即使当前审核关闭也不能将BLOCKED/REVIEW变成可见。

COMMIT确认丢失时本进程fail closed，未确认结果不能替换缓存或发公开产物。重启只承认数据库实际已提交状态：候选已提交则保持隔离，最终ALLOW已提交才可恢复可见，BLOCK已提交仍不可见。原幂等重放始终只读原任务，不重新生成。

审核PENDING但尚无候选的原handle异常，可以按服务端`can_reconcile=true`继续查询原任务；`MODERATING`已候选、所有终审核决定、无handle未知提交不可核对。本批没有人工内容放行、自动重审或通过财务操作解除隔离的接口。

## 有界性与运维

沿用每任务最多4项结果、worker并发、准入与单控制写者。媒体引擎的并发/期限和大小限制见[引擎说明](media-moderation.md)；完整媒体通过文件流传递，不放入PG JSON或内存大Base64。长检查仍受任务步骤期限与租约约束，超时宁可保留候选隔离。

016候选纳入私有存储可达集合，包括失败、取消和账务已结束的任务。禁止凭供应商终态删除候选、删owner或改迁移记录绕过隔离。升级/恢复须联备数据库、参考目录、资产目录及所用对象存储，使用匹配016格式的写者与维护CLI；回退旧程序须恢复匹配备份，单独关闭开关不是数据降级。

本模块证据见[056台账](../changes/2026-11-14-056-media-moderation-execution.md)，包括真实PG下请求2项而供应商仅交付1项、完整通过审核后仅结算1项并释放余款的PARTIAL子例。交互与乱序保护见[媒体审核工作台](media-moderation-workbench.md)。根统一执行数据库故障、全量与浏览器验收，详见058；不将本地测试描述为真实渠道、语义准确率或生产容量证明。
