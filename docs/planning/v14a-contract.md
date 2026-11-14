# V14-A 运营异常核实契约

状态：V14-A本地子批次已于2026-11-14验收，证据见047—050。完整V14仍包括后续内容审核、申诉、封禁与风控；V11真实邮件依赖保持。

## 权限与入口

`OPERATIONS_ENABLED=false`默认关闭。启用要求PostgreSQL及账户模式；注册不授予角色。停机CLI按工作区授予observer（read）、operator（read/investigate/case_open）、finance（read/finance/case_open），同人多个角色取并集。浏览器仅显示已授权工作区，不沿用创作项目请求头作为运营权限。

`GET /api/v1/operations/access`实时返回`{enabled,access:[{workspace_id,role,permissions,version}],reason?}`。普通用户没有access；具体资源读写都独立重核角色，不能把入口隐藏作为权限控制。敏感execute先有界bcrypt校验密码，再在事务内核对原散列、凭据版本、会话和当前grant。密码及proof只在当前请求内，不下发proof token，不存草稿/本地缓存。

## 作用域与数据

读接口用`workspace_id`查询参数，写接口放正文。所有列表使用绑定workspace/filter/case的游标，默认20、最大50；API禁止返回提示词、输出正文、邮箱、供应商handle、完整URL及凭据。

- `GET /operations/exceptions?workspace_id=&kind=all|run|task&limit=&cursor=`：异常元数据。
- `GET /operations/cases?workspace_id=&state=all|OPEN|RESOLVED&limit=&cursor=`：运营工单。
- `GET /operations/cases/{id}?workspace_id=`：工单、当前资源元数据及已提交收据。
- `GET /operations/cases/{id}/events?workspace_id=&limit=&cursor=`：封闭结构审计时间线。

以上省略的统一前缀是`/api/v1`。列表返回`{items,next_cursor}`，无下一页为null。

Exception字段：`id,resource_type,workspace_id,project_id,model_id,provider_mode,status,resource_version,billing?,can_reconcile,open_case_id?,updated_at`。Billing沿用当前固定精度快照。

Case字段：`id,workspace_id,project_id,resource_type,resource_id,resource_version,state,version,reason,evidence_ref,created_at,updated_at,resource,receipt`。resource为当前安全Exception快照；receipt未执行为null，执行后含`id,plan_id,action,case_version,resource_version,billing_version,executed_at`。开单时版本与当前资源版本可能不同，必须重新预演，不能根据旧页面猜测。

## 开单、预演、确认

1. `POST /operations/cases`，必须`Idempotency-Key`。正文`{workspace_id,resource_type,resource_id,resource_version,reason,evidence_ref}`。operator或finance可建单，resource_type为run/task，资源必须属于已授权作用域。相同键原输入返回原单，异参冲突；网络未知时先核对列表，不换键自动开新单。
2. `POST /operations/cases/{id}/plans`，正文`{workspace_id,expected_version,action,reason,evidence_ref,usage?,delivered_units?}`。不需要生成任务幂等键，预演不执行业务；校验当前冻结规则及可执行状态，返回5分钟有效计划。
3. `POST /operations/cases/{id}/execute`，正文`{workspace_id,expected_version,plan_id,plan_digest,password}`。显式二次确认；事务中重核权限、会话、版本及计划摘要，业务/工单/账务/审计/Outbox一起提交。已执行计划不能再次执行业务，重复请求明确冲突并从工单读取收据。

Plan字段`id,case_id,case_version,resource_version,billing_version,action,plan_digest,expires_at,effect`；`effect={charged_micros,released_micros,unit,scale}`为本次变动，不是最终钱包余额。摘要绑定数据库身份、用户/会话/grant快照、原输入、工单/资源/账本版本。密码不参与持久化摘要或JSON。

reason闭集为`provider_unknown / delivery_issue / usage_missing / billing_review / manual_review`。evidence_ref使用1—128字节安全ASCII短标识，如`ticket:123`，不接受`://`URL；不把证据正文或秘密填进去。上下游完整证据由运营外部核实，此处只留可追踪标识。

## 动作语义

| 动作 | 权限 | 真实效果 |
| --- | --- | --- |
| reconcile | investigate | 仅恢复有原handle且停驻可核对的媒体任务查询；不重新POST生成 |
| release | finance | 仅处置终态/停驻REVIEW冻结，证据确认后释放剩余；不据取消猜测免费 |
| charge | finance | 依据冻结价格及明确token/交付数核实结算，不接受任意金额，不超过冻结 |
| no_action | investigate | 仅关闭工单并记录原因；不改变任务、供应商状态或权益 |

ChatUsage沿用`{status,source,complete,prompt_tokens,completion_tokens,total_tokens}`；真实来源gateway/reported，demo来源demo/demo。输入输出计数为非负整数且和一致，不能按文字长度推算。新接口不开放赠额、部分冲正、全额退款；既有全额权益冲正仍走停机CLI，不是支付渠道退款。

## 并发、恢复与容量

预演只保留有限计划：每工作区64个有效、全局10000个有效，总计划最多100000；过期未消费超一天每次有界清1000。工单每工作区10000/全局100000上限。权限/版本变更会使计划失效；UI修改输入清除计划。

数据库确认提交后才替换缓存和发布状态，媒体核对须撤销旧lease generation；Outbox背压整体回滚。execute响应不明不自动重发、不进入生成恢复仓，只刷新原工单/收据核对。未知操作离页前有提示；密码在提交后或离页清除。所有敏感操作测试隔离真实SMTP/网关/支付。

013/014数据库格式升级前联备；不得删除迁移/owner绕过旧写者栅栏。原账本分录不可改删；运营审计不写任意正文。实现及验证以047—050和任务源实际证据为准。
