# 060 · V14-C1 新生成受理与moderator权限

日期：2026-11-14。关联V14-C1及H01/H03/H05/H06/H07/H09；017领域、HTTP/前端和联合验收分别由协作模块登记。完整V14仍保持进行中。

## 目的

将工作区新生成限制可靠接入三种生成入口和报价，保留已有幂等、执行、恢复与账本责任；为限制及复核操作提供独立的moderator授权，不扩张旧角色权限。

## 范围

旧单轮、多轮回复/分支/继续、图片/视频及编辑任务的快速门禁、锁外准备后的重核、最终事务门禁；报价拒绝；独立角色和审计闭集。C1不新增额度单次上限，不拦截上传、历史下载、旧任务Query/取消/核对/结算，不提供内容放行或全账号封禁。

## 实际文件

- `internal/studio/generation_admission.go`：App快查wrapper；SQL共享锁helper归`generation_control.go`领域模块唯一维护。
- `internal/studio/app.go`、`conversation.go`：三入口原幂等之后的检查，审核重入及图片编辑spool回锁检查，多轮事务先锁工作区。
- `internal/studio/persistence.go`：单轮Run与Task创建事务先做工作区门禁，再冻结额度或插入资源。
- `internal/studio/billing_http.go`：新报价快查与报价事务复核，不修改原报价价格或原幂等语义。
- `internal/studio/operations_access.go`、`operations_admin.go`：moderator权限矩阵、CLI角色校验文案及运营审计闭集；后者017格式栅栏行由根维护。
- `internal/studio/operations_access_test.go`、`generation_admission_test.go`：闭集矩阵、真实PG准入/竞态/原任务兼容/角色撤权整合。
- `docs/wiki/generation-admission.md`与本台账。根统一维护README、API、导航/台账索引和任务源，017迁移及限制/申诉领域由对应模块维护。

## 接口与数据影响

新增受理拒绝为403 `GENERATION_RESTRICTED`。同键同body已受理记录先返回原资源；同键异body仍409。新请求拒绝不创建资源、消息、冻结、已消费报价或幂等记录。旧有效报价不保证当前准入，但冻结价格不变。运营开关关闭不能删除已有持久限制。

最终创建事务在会话/报价/钱包之前锁workspace行，与限制写的排他锁一致，覆盖首次无策略行的竞争。角色新增moderator，仅read、generation_restrict和appeal_resolve；既有角色不扩大。审计扩展六个生成限制/复核动作与restriction/appeal资源闭集，不记录正文或密码。没有修改旧worker保存/恢复/审核交付/财务结算规则。

## 验证结果

- 普通定向编译及`TestOperationsRoleMatrixAndClosedInputs`、`TestOperationsAdminRejectsInputsBeforeConnection`通过0.105秒；本次未设置TEST_DATABASE_URL，PG跳过不计入数据库证据。
- 根授权独占测试窗口，使用`.runtime/v14b-test.mjs`隔离邮件/权益/运营/审核外部环境，仅运行本模块四个PG函数，首轮全过8.292秒。
- `TestPostgresGenerationAdmissionOriginalReplayAndExistingWork`通过2.34秒：三入口和新报价拒绝、原key重放与异参409、旧quote未消费、直接repository最终门禁、无新增消息/冻结/幂等、已接受文本/媒体继续结算、参考上传与读取可用，以及关闭运营重启后限制仍生效。
- `TestPostgresGenerationAdmissionRechecksAfterModeration`与`...RechecksAfterEditPreparation`各通过1.66秒：在锁外审核/磁盘准备期间提交限制，回锁后拒绝，零新生成资源或冻结。
- `TestPostgresOperationsModeratorScopedPermissionsAndOfflineRole`通过2.59秒：旧角色没有新权限、停机CLI预演/提交第四显式角色、同人四角色可读、跨工作区拒绝、撤权后旧密码proof不能继续执行，业务拒绝不熔断数据库。
- `go vet ./internal/studio`通过；本模块收口时文档检查仅待根将059/060加入台账索引，无本地断链。最终治理结果由根统一记录。
- 全量、HTTP领域、前端和浏览器结果由根联合台账另行记录，不以本次四项替代完整C1验收。

## 风险与限制

快查增加短PG事务，最终创建再核验；这是明确的正确性边界，未宣称极限吞吐达成。现有单控制写者和全局应用状态锁仍保留。新生成限制不等同于停用账号，不追溯取消已受理任务或解除审核隔离。客户端先前已经受理不明时，后续403不能清空原恢复操作。默认memory不提供持久限制能力。

## 回滚方式

关闭运营管理或审核开关不会解除既有新生成限制；解除必须走受权的当前限制决议。017数据库使用匹配写者，回退旧程序须停机并恢复匹配的数据库/私有文件联备到隔离环境。禁止删restriction/control/审计/迁移或owner绕过既有状态。

## 文档同步

[准入Wiki](../wiki/generation-admission.md)与[生成限制领域](../wiki/generation-controls.md)互链，说明角色与受理边界、原请求保留及现有任务兼容。根统一更新README、Wiki/台账索引、任务源/TODO与联合验证；本模块不提前标记完整V14完成。

## 根联合验收补充

2026-11-14，根已完成README/Wiki/台账索引、任务源/TODO同步，C1本地子项通过；297项前端、Go vet/构建、真实PG全量282.020s、六套隔离smoke、017联备数据比对、桌面/390手机限制→申请→解除答复→手动新生成及文档治理均通过。前述待集成事项以[062最终记录](2026-11-14-062-generation-controls-integration.md)和其JSON证据为准；完整V14仍进行中，真实渠道、C2/C3及生产能力未提前放行。
