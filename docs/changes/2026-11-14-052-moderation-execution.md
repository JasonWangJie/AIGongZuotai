# 052 · V14-B1 文本受理与输出交付隔离

日期：2026-11-14。关联V14-B1；政策引擎/015由051负责，前端053，根联合验证054。完整V14仍保持进行中。

## 目的

为新文本生成提供生成前检查和整段输出交付门禁，保留原幂等、上下文冻结、账本、恢复和同事务通知语义，避免未审核片段经SSE、检查点或历史读取提前到达用户。

## 范围

旧单轮与多轮文本请求的两阶段受理；非秘密政策同事务冻结；私有有界输出缓冲；终态整段放行；Run/消息/分支/context读取防御；取消、重启和事务失败隔离；启用审核时新媒体明确拒绝。默认关闭且启用须PG+账户认证。不实现图片/视频审核、人工内容放行、自动重审、申诉、封禁或真实审核商接入验收。

## 实际文件

- `internal/studio/app.go`、`moderation_execution.go`：配置接线、原键优先、锁外输入审核、上下文摘要复核、私有候选缓冲及提交后交付。
- `internal/studio/model.go`、`conversation.go`：公开安全摘要、输出释放判定、消息/context隔离；保留旧无摘要记录。
- `internal/studio/persistence.go`：受理和终态事务调用政策/安全决策helper，恢复未完成Run为无正文REVIEW。
- `internal/studio/http.go`、`repository_queries.go`、`conversation_http.go`：bootstrap能力、Run列表/快照/SSE/消息页/分支SQL与内存读取防御。
- `internal/studio/moderation_execution_test.go`、`moderation_integration_test.go`：普通并发/隔离/HTTP审核故障及PG事务、账务、恢复专项。
- `docs/wiki/moderation-execution.md`与本台账。015及引擎文件由051维护；根维护入口环境、OpenAPI、README、索引和任务源。

## 接口与数据影响

bootstrap新增`moderation`能力；Run、assistant消息和分支新增可选审核摘要。启用的新文本输出不再逐token交付，而是在完整成功且审核ALLOWED、同事务提交后发送终态`run.snapshot`。没有新公开内容放行API。新媒体返回422 `MODERATION_MEDIA_UNSUPPORTED`，原请求重放不受影响。

输入检查失败无Run、消息、冻结或报价消费。生成开始后的审核失败、未知或取消保持正文隔离及适用的账务REVIEW；终态原事务包括业务、账本、政策决策和Outbox，失败不发布候选缓存。多轮完整context持久；单轮保持本次内存ChatRequest及原prompt/模型能力快照，重启不重新生成。未审核候选只在内存，015不存用户正文、endpoint或密钥。

## 验证结果

- 7个`TestModerated*`普通函数通过0.280秒，包含ALLOW/BLOCK/REVIEW三子例；覆盖Run列表、快照、SSE、消息、分支、后续context、原幂等/新媒体拒绝、输入检查无副作用、同键并发一次生成、检查不持全局锁、审核HTTP未知及取消后的迟到ALLOW。
- 普通`go test ./...`通过（studio8.333秒）；本次未配置`TEST_DATABASE_URL`，PG测试明确跳过，不能作为数据库验收。`go vet ./...`通过。
- 根串行执行`TestPostgresModerat*`共5个函数及子例通过8.853秒，包含本模块3个PG领域函数：输入拒绝不消费报价/改变会话树，输出决策与账务结算、审核决策写入失败整体回滚且不发布缓存、重启仍隔离并原键重放；同时包含政策持久化和根独立真实COMMIT确认丢失子例。此为根提供的联合执行结果，详见[054](2026-11-14-054-text-review-integration.md)，不与普通测试混计。

## 风险与限制

候选输出最多128KiB/活跃Run，仅存在内存；进程崩溃会丢弃未审核正文，无自动重审/重发。只读摘要与本地规则不等于真实合规审核。旧无摘要历史不追溯审核，新多轮仍检查实际发送的完整上下文。完整媒体审核和人工内容处置留到后续子批次。当前控制写者、事务及容量限制保持，未宣称分布式交付或极限性能达成。

## 回滚方式

关闭当前审核只影响新请求，不能放行已受理的隔离内容或重新发送原请求。015数据库必须由支持该版本的程序写入；若要退回旧程序，停止服务并将升级前数据库与私有目录联备恢复到独立环境，核对存储身份。不得删除迁移记录或直接将ALLOWED写入数据库绕过决策事务。

## 文档同步

[执行Wiki](../wiki/moderation-execution.md)、[前端说明](../wiki/moderation-workbench.md)和[冻结契约](../planning/v14b1-contract.md)互链；根统一更新README、导航/台账索引、任务源与054。真实审核渠道、V14-B2和V14-C仍独立待验，V14整批不提前勾选。
