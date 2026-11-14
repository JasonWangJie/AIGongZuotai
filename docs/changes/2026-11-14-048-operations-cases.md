# 048 · V14-A 异常工单、事务预演与执行

日期：2026-11-14。关联V14-A，完整V14保持in_progress；047负责权限，049负责前端，050汇总独立集成。

## 目的

让已授权运营人员通过有限异常元数据、可核对预演和密码二次确认，恢复原媒体任务查询或处理待核实冻结，并在未知提交后通过持久工单回执核对结果。

## 范围

异常队列、开单幂等、工单/审计分页、5分钟计划、SAVEPOINT真实预演、单工单CAS执行、媒体原handle核对、REVIEW charge/release及no_action。没有开放赠额、退款、内容审核、封禁或申诉；不将本子批次标作完整V14完成。

## 实际文件

- `internal/studio/operations_cases.go`：安全资源投影、工单/计划/回执领域、事务业务模拟与执行、提交后缓存更新。
- `internal/studio/operations_http.go`：7条业务路由方法、8KiB严格JSON、范围游标、最小元数据和闭集时间线。
- `internal/studio/operations_cases_test.go`、`operations_http_test.go`：普通验证及真实PG领域/HTTP/角色/回滚/租约负例。
- `internal/studio/migrations/014_operations_cases.sql`：工单、开单幂等、计划、不可变回执、异常与分页索引。
- `docs/wiki/operations-cases.md`及本记录；根维护routes/config、README/索引、契约、OpenAPI、版本栅栏和任务源。
- `docs/wiki/backend.md`、`observability.md`、`ledger.md`：同步V14-A默认关闭、事务执行和线上核实边界；当前固定路由桶为63，保留V12的56桶历史记录。

## 接口与数据影响

新增operations exceptions、cases列表/创建/详情/events、plans和execute。所有作用域为显式workspace_id，事务重新检查grant；operator或finance可开单，reconcile/no_action需investigate，charge/release需finance。请求不接受任意URL/提示词/证据正文，密码只当前请求使用。

预演在真实业务SAVEPOINT中执行并强制延迟约束，再完整回滚模拟结果，外层仅保留有限计划和审计。摘要绑定数据库/actor/session/grant、工单/资源/账务版本及动作；执行同事务提交业务、账本、Outbox、审计、计划消费及receipt。只有commit成功后才更新缓存；no_action不替换旧检查点。媒体恢复与财务决策撤销旧租约epoch/owner，迟到结果被fence拒绝。

## 验证结果

- 普通`TestOperationsInputAndCursorBoundaries`通过0.049秒；固定原因、opaque证据、limit及跨用户/工作区/筛选/路由游标拒绝。`TestOperationsStrictJSONDoesNotAcceptAmbiguousOrLargeRequests`通过，覆盖重复/大小写字段、usage私密reason、超8KiB、非对象和非JSON媒体类型。
- 真实PG首5领域函数通过9.123秒：真实dryrun不留分录/操作键；release+receipt+重复拒绝；权限撤销/重授/凭据变更/过期/资源与case CAS；并发两个计划仅关闭一次；no_action不覆盖内存输出；审计失败整体回滚且不发布候选缓存；有/无handle核对边界。
- 真实PG两HTTP函数通过4.316秒：跨工作区授权、最小字段、复合游标、开单重放/冲突、详情/时间线、撤权立即拒读、错误密码、按冻结价格charge、finance不继承investigate、64有效计划上限。
- 只读审查发现execute路径ID可能含NUL导致PG编码熔断，已在任何plan SQL前校验case ID及64位小写hex摘要；真实HTTP复验通过2.713秒，拒绝路径NUL后ready仍200、DB未熔断。
- 媒体补充2函数通过3.984秒：实际持久worker对原handle Query=1/Submit=0；finance决定后epoch递增、owner清空，旧lease保存返回TASK_LEASE_LOST且不登记产物。
- 初次014迁移与并行013重复定义audit_case索引，已协调仅保留013定义并重跑通过。普通`go test ./...`通过（studio7.870秒，未设置TEST_DATABASE_URL，PG明确skip）；`go vet ./...`通过。未触及主开发库或真实外部服务。最终独立COMMIT ACK丢失、全仓PG与浏览器由050记录，不能把子模块结果冒充整批验收。
- 根独立全量PG验收通过169.657秒；该结果由集成人提供，联合验证及适用范围见[050台账](2026-11-14-050-operations-integration.md)，与上面的子模块专项分别记录。

## 风险与限制

工单与计划都有硬上限，历史归档尚未实现。当前单控制写者事务与应用锁保持，不宣称运营多实例或全量审核系统。外部证据仍需人工核实，短标识不是供应商真实性证明；人工释放后后续找回作品不会再次收费。真正邮件、供应商和支付验收仍独立，测试没有真实外发或付费调用。

## 回滚方式

停止服务，将升级013/014前的PG及私有目录联备恢复至独立库/目录；启用邮件时一并保全原密钥。核对owner、数据库身份及迁移集合后启动匹配版本。禁止删除迁移/owner或直接删除工单、账本、不可变回执“回滚”。

## 文档同步

[异常工单Wiki](../wiki/operations-cases.md)与[权限](../wiki/operations-access.md)、[前端](../wiki/operations-workbench.md)、[V14-A契约](../planning/v14a-contract.md)、[账本](../wiki/ledger.md)互链。根统一同步README、导航/台账索引、OpenAPI、任务源及050联合验证；完整V14和真实外部依赖保持独立状态。
