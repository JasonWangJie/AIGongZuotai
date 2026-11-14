# 运营异常队列与工单

本页记录V14-A的异常核实链路。完整V14仍包含后续内容审核、封禁、申诉及风控，本子批次不能代表整个运营控制台已完成。入口权限和停机授权见[运营权限](operations-access.md)，前端交互见[运营工作台](operations-workbench.md)，API字段见[V14-A契约](../planning/v14a-contract.md)，实现证据见[048台账](../changes/2026-11-14-048-operations-cases.md)。

## 范围与最小数据

默认关闭；开启必须有PG、真实账户会话及明确的工作区角色。运营工作区由`workspace_id`指定，每个请求事务内重新检查当前会话和目标授权，不沿用创作项目头作为运营权限。workspace owner和注册用户不会自动拥有运营权限。

异常队列包括FAILED/INTERRUPTED的Run、FAILED/SUBMIT_UNKNOWN/RECONCILE_REQUIRED/PROVIDER_STATE_UNKNOWN的Task，以及Billing为REVIEW的资源。只有当前异常或待核实资源能开单。查询使用工作区范围和部分索引，分页默认20、最大50；复合游标绑定用户、工作区、路由和筛选。队列只返回资源ID、项目、模型、demo/live、状态、版本、Billing及是否可核对；不读取提示词或输出到列表响应，也不返回邮箱、原始handle、URL和凭据。

同一资源同时只能有一个OPEN工单；队列的`open_case_id`可定位已开工单。建单必须有Idempotency-Key；同键原输入返回原工单和当前安全资源信息，异参409。读取详情/列表/时间线都重新检查授权，已撤权账户不能继续读取旧工单。

## 预演与明确确认

动作仅四种：

| 动作 | 权限 | 效果 |
| --- | --- | --- |
| reconcile | investigate | 有原handle的SUBMIT_UNKNOWN/RECONCILE_REQUIRED恢复为QUEUED，后续只查询原受理任务 |
| charge | finance | 对REVIEW按冻结价格及明确token/交付数量结算，不能任意填写金额 |
| release | finance | 外部证据确认后释放REVIEW剩余冻结，不根据取消推断免费 |
| no_action | investigate | 关闭工单并保留审计，不改变任务、输出、供应商或余额 |

operator或finance都能建单；finance本身不包含investigate，因此不能借财务角色执行reconcile或no_action。未开放新赠额、全额/部分冲正入口；原全额权益冲正仍使用[停机账本工具](billing-operations.md)，不是支付渠道退款。

reason固定为`provider_unknown/delivery_issue/usage_missing/billing_review/manual_review`。证据仅允许1—128字节ASCII短标识，如`ticket:123`；拒绝含`://`的URL、自由正文和NUL。完整上下游证据在外部核实，不进入工单正文。运营JSON限8KiB、要求application/json、拒绝重复键、大小写别名和未知字段；usage只有明确的计量/来源字段，不能借`reason`传入任意正文。

预演使用真实PG事务的SAVEPOINT执行相同业务函数，并强制验证延迟约束，然后回滚SAVEPOINT。报价/账本操作键、分录、状态、资产与Outbox均不保留模拟结果，只在外层事务保存计划及`case.previewed`审计。没有供应商调用；媒体reconcile只模拟调度状态改变。

计划绑定数据库UUID、操作者/会话/凭据版本、grant快照、工单版本、资源状态与版本、Billing版本及动作输入，5分钟到期。`effect.charged_micros/released_micros`为本次增量，不是余额或累计值。计划摘要为64位小写十六进制；密码不参与计划持久化。界面修改输入、资源更新、角色/会话变化均需重新预演。

执行提交当前密码。bcrypt在应用状态锁和业务事务之外执行，但事务内必须再次检查原密码散列、凭据版本、会话、当前grant和计划绑定。证明仅存在于当前请求内，API不会签发可复用proof token。错误密码、过期/更改计划、旧CAS及撤权不能执行。

## 一个事务，一份回执

执行把真实业务变更、账本、工单RESOLVED/version、计划消费、不可变receipt、运营审计和业务Outbox放在同一PG事务。commit确认成功后才替换缓存。reconcile撤销原Task租约owner/epoch；finance处理parked Task时也递增epoch并清除owner/until，旧worker的迟到结果不能登记产物或覆盖财务决定。

每个工单只有一份receipt，每个计划最多消费一次；另一个同版本计划在工单结束后冲突。同计划重复execute明确409，不自动再次执行业务。执行响应不明时，前端清除密码，只读取原工单/receipt核对；不能把它放入生成任务的自动重试路径。数据库提交确认丢失仍沿既有fail-closed策略处理，恢复后以持久receipt为准。

no_action只关闭工单，甚至不会用数据库旧检查点覆盖内存中尚未checkpoint的输出。财务处置沿用[账本政策](ledger.md)：仅终态或停驻REVIEW可人工决定，自动查询中的PROVIDER_STATE_UNKNOWN不能提前释放。人工release是原冻结最终决定，之后用户重新核对取得作品也不重新收费。

GET工单返回当前安全资源快照；receipt包含计划、动作、工单/资源/Billing版本和执行时间。case events展示`case.created/previewed/executed`等闭集结构事件、短原因/证据、版本与时间；不展示邮箱、密码、供应商原始错误正文。审计失败、Outbox背压或业务失败整体回滚，不能只关闭工单而遗漏账务。

## 容量、版本与实际边界

每工作区最多10,000工单、全库100,000；每工作区64有效计划、全库10,000有效计划，总计划100,000。未消费且过期超过一天的计划每次清理最多1,000个；已消费计划及receipt保留。达到容量返回明确429，需要后续归档治理，不假装无限存储。工单/资源递增前检查JavaScript安全整数上限。

当前仍为单控制写者，权限锁顺序先用户/会话/grant，再业务资源；预演和执行在应用锁内完成有限事务，未引入多实例分布式运营权限。游标是有界实时列表，不是跨多页的一致性快照；资源状态更新后前端仍须按资源版本合并。

013权限与014工单为新格式。升级前联备PG、私有资产/参考目录及启用邮件时的独立队列密钥；回退恢复升级前独立库/目录，不能删除迁移登记或owner绕过栅栏。没有真实供应商、SMTP或支付调用；PG和模拟器验证不代表生产审核、投诉处理或支付合规已验收。
