# 文本生成审核与交付隔离

本页记录V14-B1的Go执行边界，关联[052台账](../changes/2026-11-14-052-moderation-execution.md)、[接口契约](../planning/v14b1-contract.md)和[工作台说明](moderation-workbench.md)。默认关闭；启用须使用PostgreSQL及`AUTH_MODE=required`。本地rules只允许demo；HTTP是平台自定义适配协议，当前fixture通过不等于真实审核渠道已验收。V14-B2图片/视频审核和V14-C申诉、封禁、风控不在本次范围，完整V14仍未完成。

## 新请求的两阶段受理

旧单轮`POST /api/v1/runs`与多轮`POST /api/v1/conversations/{id}/runs`先查询原幂等记录。原键原正文已受理时返回原Run，不因当前策略启停、版本或余额变化重做审核或重新生成；同键异正文仍409。

新请求第一阶段完成模型、输入、上下文与资源准入检查，只读准备实际发送的`ChatRequest`。审核在应用锁和数据库事务之外运行。第二阶段重新查询幂等、复核会话CAS、配额和上下文摘要，确认审核的请求与将执行的请求一致，再在原受理事务内保存Run、可选权益冻结、幂等、消息节点、私有政策、初始安全决策及Outbox。输入拒绝、未知或繁忙不会创建Run、消息节点或冻结，也不会消费报价。公开错误为固定消息，不转发审核响应正文。

多轮审核的是裁剪后的完整结构化上下文，而非仅最后一条提示词；该上下文继续同事务保存于`studio_chat_contexts`。单轮由本次内存中的`ChatRequest`及原提示词、模型和能力快照锁定执行，不新增另一份持久输入正文。两者都不会在服务重启后重新发送生成请求。

审核启用时，新媒体生成在原幂等查回之后返回422 `MODERATION_MEDIA_UNSUPPORTED`。已有媒体任务及原键重放保持原流程；这不代表其内容已经通过审核。

## 输出先隔离，提交后交付

启用审核的新Run公开`moderation`：`policy_id`、`policy_version`、`phase="output"`、`state`、独立单调`version`及可选固定`reason_code`。初始为PENDING/version 1，最终为ALLOWED、BLOCKED或REVIEW/version 2。该摘要不是供应商认证或内容绝对安全保证。

Provider仍完整消费上游流与usage，但片段只追加到每Run最多128KiB的私有内存缓冲；不写`Run.Output`、不进入检查点、不发`message.delta`，也不写事件回放环。整段生成完成后在锁外执行输出审核。只有ALLOWED且生成完整成功，才把候选全文、COMPLETED、安全决策及适用的账本结算和Outbox放在同一事务提交。确认提交成功后才替换公开缓存，并发送包含全文的终态`run.snapshot`；未审核文本不经过先发布后撤回的流程。

BLOCKED或审核未知落FAILED，公开输出为空；已开始生成的冻结进入REVIEW，不因有合法usage自动结算，也不按零输出解冻。用户取消、流中断和停机保持原业务终态，未过审内容仍不交付。取消之后迟到的ALLOW不能复活Run。Outbox背压只重试保存原候选事务，不能重新生成；数据库错误或提交确认不明不会提前替换缓存。

## 所有读取与后续上下文

Run列表、单Run快照、SSE回放、消息页与分支预览都只接受`moderation.state=ALLOWED`且业务状态COMPLETED的已审核输出。旧无审核摘要的记录保持原有行为，不将既交付历史伪称已经审核。分支SQL和内存读取同时检查状态，不从未放行输出或私有缓冲拼接预览。

后续普通reply会略去被隔离的整个user/assistant轮次；显式continue必须有可交付的父回复，否则422 `CHAT_CONTINUE_UNAVAILABLE`。新请求仍对最终实际使用的完整上下文审核，所以引用旧无摘要历史也不能绕过当前输入检查。消息节点和分支摘要带相同审核信息，浏览器还按独立审核版本防止旧快照或旧delta重新显示受限内容。

## 持久化、恢复与边界

015保存不可变非秘密政策和仅追加的安全决策历史，与业务受理/终态事务一致；不保存未审核候选正文、审核endpoint、密钥或上游响应。公开摘要不包含规则清单。未完成生成或审核时崩溃，私有候选随进程丢失；恢复原Run为INTERRUPTED/REVIEW且输出为空，不自动重发生成、不重新审核，也不会因为当前配置改为disabled就放行原候选。

本批没有人工查看或放行隔离候选、自动重审或申诉功能。账务人工核实仍遵循[权益账本](ledger.md)与[运营工单](operations-cases.md)，不能把财务release当作内容审核通过。015是格式栅栏；升级前联备数据库和已有私有存储，不能删除迁移记录让旧写者进入。

普通专项覆盖整段ALLOW/BLOCK/REVIEW、所有读取面、同键并发、审核锁外执行、上下文隔离和迟到取消；PG专项与根独立故障验证分别见[052](../changes/2026-11-14-052-moderation-execution.md)及[054联合记录](../changes/2026-11-14-054-text-review-integration.md)。测试过程不使用真实审核渠道、付费模型或外发邮件。
