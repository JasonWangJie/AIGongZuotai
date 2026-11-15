# 权益配置与离线核账

V12使用平台创作点，单位`CREDIT`、六位小数。1点等于1000000 micros；HTTP的`*_micros`金额以micro为单位传十进制整数字符串，Go/PG使用受限整数，前端用BigInt格式化为点数。创作点不代表人民币，不将异步供应商文档中未定义单位的quota/cost转换为费用。真实支付订单由V13完成。

## 启用与价格

默认`BILLING_MODE=disabled`，不自动赠送额度，不给旧任务追扣。启用`credits`必须同时配置PostgreSQL、`AUTH_MODE=required`和`BILLING_PRICE_FILE`。本地示例：

```powershell
$env:BILLING_MODE='credits'
$env:BILLING_PRICE_FILE='config/prices.demo.example.json'
$env:MODEL_CATALOG_FILE='config/models.example.json'
$env:PROVIDER_MODE='demo'
npm run dev:db
```

示例价格仅适配示例模型目录和demo。live必须另建`provider_mode=live`的服务端价格文件，并根据实际合同确定平台价；demo文件会被拒绝用于live。价格文件最多128KiB/512个SKU，重复键、未知字段、科学计数法、小数、溢出、重复SKU均拒绝。模型/操作/精确规格组合没有价就拒绝报价，不猜测相近价格。价格修改后重启；未过期的旧报价按原价格承诺，当前模型能力仍需有效。

报价保存10分钟，绑定工作区、项目、模型版本、会话及完整请求hash；报价不冻结也不执行。确认生成时冻结最大额度，与任务/Run/幂等/审计/Outbox同事务。报价最多64个工作区有效未消费项、全局10000项；过期未消费项有界清理。前端修改输入、规格或上下文后重新报价，受理不明则必须重试原body/key/quote，不能换键。

聊天使用服务端输出预算和上下文预算计算最大冻结，最终只认完整有效usage，合并输入和输出费用后一次向上取整到micro。上下文估算只用于裁剪，不能拿字符数结算。媒体按实际导入并登记的产物数结算，部分交付释放未交付部分。已经执行而失败、取消、断流、无usage、用量超过预算、受理不明等进入REVIEW，保留剩余冻结等待证据。该政策是平台权益规则，不是对供应商成本的证明。

## 离线工具

[cmd/ledger](../../cmd/ledger/main.go)只读取当前终端`DATABASE_URL`，不自动加载.env、不迁移、不发邮件或AI请求、不处理真实支付。当前数据库须恰好包含001—017，工具与工作台竞争同一个PG writer锁，必须先停机。工具不访问私有文件，不需伪装取得文件删除权限。

```powershell
go run ./cmd/ledger --action inspect --workspace "实际工作区ID"
go run ./cmd/ledger --action grant --workspace "实际工作区ID" --amount-micros 100000000 --operation-key "local-demo-grant-001" --reason "demo-fixture" --evidence "fixture:local-demo-001"
```

第二条默认只预演：用真实事务检查余额、幂等、约束和对账，最后回滚。报告的`result`是模拟结果，只有退出0且`applied=true`才代表提交。生成的journal/operation ID在预演中不稳定，不能当正式凭证。`plan_digest`绑定数据库UUID、精确操作及钱包/冻结单版本；核对额度和原因后，用相同参数加`--apply --plan-digest "该次64位摘要"`提交。状态变化会拒绝旧计划，必须重新预演。

操作键保持稳定；确认不明时以完全相同请求重试，已提交操作返回原收据，不重复赠送/释放/冲正。精确重放不受后来钱包变化阻断；该收据是操作当时快照，当前余额须再inspect。不能变更金额后沿用旧键。原因和证据填无敏感信息的短标识，不填提示词、收件人、密钥或供应商URL。

```powershell
# 已结算消费的一次全额冲正；不是支付渠道退款
go run ./cmd/ledger --action refund --reservation "实际冻结单ID" --operation-key "review-refund-001" --reason "approved-refund" --evidence "ticket:001"
# REVIEW记录在证据核对后释放
go run ./cmd/ledger --action resolve --reservation "实际冻结单ID" --decision release --operation-key "review-release-001" --reason "verified-no-delivery" --evidence "ticket:002"
# REVIEW聊天按核对的真实用量和原价格结算
go run ./cmd/ledger --action resolve --reservation "实际冻结单ID" --decision charge --prompt-tokens 120 --completion-tokens 80 --usage-source gateway --operation-key "review-charge-001" --reason "verified-usage" --evidence "ticket:003"
# 媒体核对使用 --delivered-units；不能与token证据混合
```

全部变更默认预演，正式执行也检查对账，失败整体回滚。只允许一次全额权益冲正，不开放任意负扣/手工改余额。REVIEW核实不允许直接指定任意结算金额，必须用原价格和有效证据计算且不超过冻结；真实资金退款仍需支付模块及渠道核对。

## V14-A线上核实边界

默认关闭的运营工单提供按工作区授权的REVIEW核实入口，规则见[运营权限](operations-access.md)、[异常队列与工单](operations-cases.md)及[V14-A契约](../planning/v14a-contract.md)。finance角色可在终态或停驻任务的外部证据核对后预演charge/release，敏感执行须输入当前密码并在同一事务重核权限、计划和版本；自动执行或查询中的任务不能提前人工结算或释放。

线上入口不开放赠额、全额/部分权益冲正或支付退款；原grant/refund仍使用上述停机账本CLI。人工charge/release为原冻结的最终决定，后续符合条件的原任务核对找回更多作品不会新开冻结或二次扣费。响应不明只核对原工单回执，不自动重复提交敏感处置。真实文本/媒体审核渠道、申诉、封禁与风控仍待后续验收或实现，完整V14仍在进行中。

## V14-B1文本隔离与账务

启用[文本审核](moderation.md)时，新文本输入检查在受理前完成；确定拒绝或无法确认通过，不创建生成与权益冻结。已开始生成后，输出BLOCKED/REVIEW、取消或执行中断保留账务REVIEW；用户看不到候选正文不能证明供应商没有产生费用。只有安全审核ALLOWED且Run为COMPLETED，才在业务、审核决定、账本和Outbox事务确认提交后公开正文。

财务charge/release只处理原冻结，不改变内容审核决定，也不恢复已丢弃的文本候选。没有人工内容放行接口，不能以财务结案、模式关闭或更换政策绕过隔离。B1阶段仅交付文本本地链路；当前仅开启文本审核而没有配套B2媒体审核时，新媒体生成仍明确拒绝。历史文本、任务和资产不追溯。

## V14-B2媒体隔离与账务

启用[媒体审核](media-moderation.md)时，任务提示词和实际参考字节在受理前检查；拒绝或未知不创建本次生成及冻结。生成产物先登记016私有候选，MODERATING期间没有公开交付。只有全部返回产物通过且最终状态为SUCCEEDED/PARTIAL，才在同一事务注册公开产物并按实际交付数量结算；上游PARTIAL不是跳过审核的理由。

任一候选BLOCKED/REVIEW、取消、恢复或执行不完整时，整批结果保持隔离；已经开始执行的冻结保持REVIEW，不因为用户看不到结果而推定免费。财务charge/release不改变审核决定、候选可达性或下载权限。已有候选不借reconcile重新审核；有原handle但尚无候选且满足现有核对条件的停驻任务仍可Query原任务，不能再次Submit。真实审核商未选，本地fixture不证明真实语义效果，联合证据见[058](../changes/2026-11-14-058-media-review-integration.md)。

## 核账、升级和恢复

inspect检查所有相关journal双分录平衡、账户余额与分录总额、剩余冻结与冻结账户、累计消费/冲正与流水及冻结单一致。发现不平衡只报告并阻断变更，不自动改余额掩盖问题。大账本全量核账是有30秒超时的离线维护，未宣称海量财务报表性能。

012升级前停写并备份数据库、私有参考/资产目录及启用邮件时的队列密钥。升级后不得删版本记录绕过格式栅栏，不删除或修改已提交分录。回滚使用配套升级前备份；启用后有新账务时，恢复旧备份会丢失后续业务，必须停写核对与补偿，不能直接退版上线。

V14-A再增加013授权/审计和014工单/预演/回执，当时ledger、operations和存储维护CLI要求完整001—014；历史边界与联合验证保留在[050集成记录](../changes/2026-11-14-050-operations-integration.md)。V14-B1增加015冻结政策和审核决定，当时格式为001—015，历史验证见[054](../changes/2026-11-14-054-text-review-integration.md)。B2交付时增加016媒体候选与决定，三个CLI均要求精确001—016，不能使用旧工具操作新格式。

升级仍须停写联备，保留账本、运营回执、审核决定和私有候选；关闭`OPERATIONS_ENABLED`仅关闭入口，不撤销已提交处置，关闭审核也不会放行既有隔离结果。回退需匹配升级前的独立数据库、私有目录、S3对象/收据及必要密钥，并核对升级后的新增业务，不删除迁移登记或owner绕过栅栏。本批联合结果见[058集成记录](../changes/2026-11-14-058-media-review-integration.md)。

实施与验证见[046台账](../changes/2026-11-13-046-billing-integration.md)、[V12契约](../planning/v12-contract.md)及[TODO](../../TODO.md)。本地单控制写者、固定数量页面、有限价格表是当前边界，不能替代公众收费站点的真实支付、审核、合规、分布式容量或灾备验收。

## V14-C1当前格式

017保存工作区生成控制、限制轮次、用户复核申请、预演及不可变回执。三个离线CLI同步要求精确001—017；仍须停写联备PG、私有目录/owner、S3对象与收据及必要队列密钥，不删除迁移绕过旧写者。限制或解除不改变冻结/账务、媒体审核状态或文件可达性。详见[ADR0013](decisions/0013-workspace-generation-controls.md)和[062联合台账](../changes/2026-11-14-062-generation-controls-integration.md)。
