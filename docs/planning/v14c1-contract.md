# V14-C1 工作区新生成限制与复核

状态：本地链路已验收（2026-11-14）；模块H01/H03/H05/H06/H08/H09，依赖既有V14-A权限、V14-B审核及V12权益。本地子项独立验收，完整V14仍受真实审核渠道和V11邮件依赖约束。

## 本批完整链路

明确授予工作区moderator → 读取当前OPEN状态 → 真实预演/当前密码确认限制新生成 → 用户查看公开说明并申请复核 → moderator预演维持或解除 → 同事务回执/公开答复 → 用户读回结果 → 解除后手动创建新生成。

只限制新run/task/多轮reply、edit和continue的受理。已受理任务、原幂等回放、原handle查询、取消、历史、账单、已批准素材下载及新参考上传继续按原规则。无账户登录封禁、自动惩罚、单次/周期额度策略或内容重审；解除工作区限制不修改B1/B2审核决定，不自动生成或退款。后续C2/C3分别组织，不能以本批勾选整个C。

## 持久化与权限

017新增当前控制、原始事实不可变且解除时间仅一次写入的限制episode、申诉、申诉幂等、预演和执行回执。控制状态OPEN/RESTRICTED，缺记录默认OPEN/version1；限制首次落盘version2，版本预留解除空间。每次新限制创建独立episode，旧申诉/计划/回执不能解除后来限制。每episode每用户最多一份申请，OPEN→RESOLVED，结果UPHELD或LIFTED；解除同事务答复当前episode所有OPEN申请，已结束历史保留。

独立角色moderator具有read/generation_restrict/appeal_resolve；停机CLI显式授予，旧observer/operator/finance不继承。预演与敏感执行复用实时scope、锁外bcrypt、事务内会话/密码版本/grant重核；绑定数据库、当前控制/episode/申诉集合版本及输入，5分钟到期。SAVEPOINT实际执行业务后回滚，执行一次消费计划并写不可变收据/审计，提交确认后才返回；未知响应仅GET原计划收据。

受理和限制均锁定稳定workspace行，读FOR SHARE、变更FOR UPDATE；原幂等优先于门禁，最终受理事务在conversation/quote/wallet之前再次检查。业务拒绝403 GENERATION_RESTRICTED，零新资源、消息、冻结、报价消费或幂等占用。锁外审核/磁盘准备后必须复查；不得将门禁加到worker/save/租约/恢复/结算。关闭运营或内容审核配置也不解除已持久限制。

## 接口

| 接口 | 用途 |
| --- | --- |
| GET /api/v1/workspace/generation-access | 由会话决定工作区，当前状态和公开限制说明 |
| POST /api/v1/restrictions/{id}/appeals | 用户原键幂等申请当前限制复核 |
| GET /api/v1/appeals、/{id} | 当前用户自己的有界列表和详情 |
| GET /api/v1/operations/generation-access | 明确workspace_id及moderator权限的当前状态 |
| GET /api/v1/operations/appeals、/{id} | 授权工作区申请列表和私有详情 |
| POST /api/v1/operations/generation-plans | restrict_future / lift_restriction / uphold_restriction真实预演 |
| POST /api/v1/operations/generation-executions | 当前密码确认，按计划执行 |
| GET /api/v1/operations/generation-plans/{id}/receipt | 只读核对已提交回执，不重发执行 |

GenerationAccess含workspace_id/state/version/restriction?/can_appeal；restriction含id/reason_code/public_notice/created_at，不含内部证据。Appeal含id/workspace_id/restriction_id/state/outcome?/version/reason_code/created_at/updated_at，statement和public_reply仅详情返回。用户原因request_review/restriction_error/issue_resolved；说明至多1024 UTF-8字节，允许空或换行，拒其他控制字符。公开答复1—1024 UTF-8字节，作为限制说明或申诉答复。运营原因restrict/uphold为manual_review/policy_concern/abuse_concern，lift为review_complete/restriction_error，内部evidence_ref遵循既有有限ASCII标识。

新增请求JSON限8KiB、严格字段/编码/重复键；ID、游标、分页及作用域先验证再访问PG。列表、日志、审计均不包含用户说明/答复正文，正文只有申请人与获授详情权限的moderator可读。原提示词、候选文本/媒体、上游URL或密钥不进入新域。

## 前端及通知

普通PG账户有“生成权限与复核”入口，运营台独立风控区域；新页按需加载，恢复仓保留App层。新请求与已有pending原请求分别处理，限制不能清原未知body/key。用户申请未知先只读核对，再允许显式原键请求；执行未知清密码，仅GET回执。迟到响应不能跨用户/工作区或清新attempt；刷新不覆盖未保存说明/答复。

进入、恢复焦点、操作后及页面可见时有界读取持久状态。本批不改project-only Outbox，不选一个项目冒充工作区广播，不声称站内实时推送或外发邮件。解除只恢复新请求入口，不自动重发草稿。

## 验收与后续

真实PG覆盖三类受理、原键优先、无冻结、外部审核期间限制、既有任务继续、首次无行竞争、scope/撤权/密码重核、CAS、申诉并发、旧episode、真实预演回滚、审计/回执故障和COMMIT确认丢失。浏览器完整走限制→申请→预演→答复/解除→新生成，验证390手机、未知和角色边界。升级017先停写联备数据库/私有目录及已启用邮件密钥，所有离线工具精确格式栅栏更新。构建/测试/文档治理完成后再更新任务源本地子项。

C2拟覆盖账户/内容申诉的独立证据与处置流程；C3拟覆盖额度与自动风控策略。范围须分别设计和验收，尤其B1没有保存未审正文，不能假装可以直接恢复旧回复。前端性能拆包有构建实测后留证，生产容量另归V16/V17。

## 最终本地证据

297前端/28文件、8治理、Go vet/构建、真实PG全量282.020s与六套smoke通过；独立首次缺行双向锁、原键/旧任务、跨scope、真实预演、提交ACK丢失恢复、桌面/390手机及017联备比对通过。详见[062](../changes/2026-11-14-062-generation-controls-integration.md)及[汇总](../wiki/assets/generation-controls-validation-v0.15-2026-11-14.json)。本批未以模拟器替代真实审核/AI/SMTP/支付，也未完成C2/C3或整个V14。
