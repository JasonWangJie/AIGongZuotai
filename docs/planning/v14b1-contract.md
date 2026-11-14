# V14-B1 文本生成前后审核契约

状态：2026-11-14本地闭环验收通过；真实渠道未验收。V14-B分为B1文本与B2图片/视频；本轮已完成B1本地纵向链路。用户于2026-11-14确认审核服务暂未选型，先做本地闭环，真实渠道单独验收。不得将规则匹配或HTTP模拟器当成生产内容安全能力。

## 范围与配置

横切H01/H03/H05/H06/H07/H08/H09。默认`MODERATION_MODE=disabled`，开启要求PostgreSQL和required鉴权。`rules`只允许demo Provider，用有限确定性规则验收控制链；`http`使用服务端text-v1适配协议，可配置端点、独立密钥及凭据标识。详细协议以[审核引擎](../wiki/moderation.md)及实际实现为准，不假设New API自带此接口。

策略启动加载，冻结非秘密完整快照及摘要；策略修改需重启，不支持热更新。真实审核商选定后另做契约转换与受控验收，密钥只放服务端。候选正文不进入日志、指标、运营安全元信息或浏览器恢复仓。

## 受理与输出

1. 原幂等重放优先于当前策略及开关。新单轮或多轮请求先准备实际ChatRequest；全局锁与数据库事务之外执行输入审核。仅允许结果可继续。
2. 重入后再核验原键、CAS、模型、准入与重构上下文摘要。批准的内容必须与最终冻结发送内容一致；冻结策略、运行、会话、报价冻结、审计及Outbox同事务。
3. 输入阻断返回422 `MODERATION_INPUT_BLOCKED`，输入审核不可用返回503 `MODERATION_UNAVAILABLE`，其他前置错误包括409 `MODERATION_INPUT_REVIEW`、422 `MODERATION_INPUT_INVALID`、429 `MODERATION_BUSY`及409 `MODERATION_CONTEXT_CHANGED`；均明确未受理、未冻结、未调用模型。先前已经受理不明的原请求不能被该错误清除。
4. 开启时模型流只进入128KiB以内的私有内存候选，公开Run.Output保持空；不发未审delta，不写公开检查点。整段审核允许且终态COMPLETED同事务提交成功后才发布完整快照。
5. 阻断、审核未知、生成失败、取消或进程恢复保持正文隔离。已开始生成的冻结保留REVIEW，不因没有公开正文推断免费。未知提交只读原资源，不重新生成。
6. 本轮未持久化未审候选正文，无人工放行或重新审核入口；崩溃丢弃候选、运行中断且审核REVIEW。不会根据新策略或关闭开关放行旧内容。

新媒体生成在原键重放之后明确返回422 `MODERATION_MEDIA_UNSUPPORTED`。B2另处理参考图、视频、私有候选产物与所有下载入口；旧任务、旧资产和nil审核历史不追溯，也不能标成“审核通过”。

多轮会话完整冻结上下文写入`studio_chat_contexts`；旧单轮只保留原prompt/模型快照和当前进程的实际ChatRequest，本轮没有新增旧单轮完整上游请求持久化。运行恢复仅中断/隔离，不重发模型请求。

## 公共数据

Bootstrap增加`moderation={enabled,mode,policy_id?,policy_version?,text_delivery,media_supported}`。text_delivery为stream或after_review；只描述当前新请求能力。

Run、助手MessageNode及适用分支摘要包含可选`moderation={policy_id,policy_version,phase,state,version,reason_code?}`。phase为output，state为PENDING/ALLOWED/BLOCKED/REVIEW。nil为旧资源或未启用；只有ALLOWED且Run COMPLETED才开放正文。资源seq与审核version独立且单调；客户端拒绝旧delta/快照复活隔离内容，隐藏复制和继续操作。

前端使用固定安全说明，不回显命中片段或供应商错误正文。输入错误保留草稿；明确前置失败可以修正后手动提交，原未知请求仍保留原body/key。审核候选不进入后续祖先链、分支预览、历史或SSE。

## 验收与后续

本地验收覆盖：真实PG原幂等/无副作用拒绝、审核锁外执行、ALLOW/BLOCK/UNKNOWN、冻结策略、输出事务回滚、账务、取消/App重启及历史/上下文不泄漏；真实PG COMMIT确认回包丢失恢复（本专项非OS Kill）；HTTP审核fixture严格响应/限额/超时/禁止跳转；前端乱序与桌面手机完整链。015升级先联备PG、私有目录及启用邮件时的队列密钥，不清迁移/owner绕过旧写者。

台账051—054记录实际证据。V14-B1本地、B1真实审核渠道、B2多模态、V14-C和V11外部邮件分别维护；完整V14不能因此直接完成。
