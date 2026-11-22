# 120 · 内容申诉 HTTP 与财务工单核实

## 目的

V14-C2a 让原申请人查看独立复核结果。有原始冻结的申请可以转交财务，用户可从原申请看到已执行的释放凭证和精确 CREDIT 数额。涉及 H02、账户、运营授权、H10 与站内权益。

## 范围

V14-C2a，原冻结与正规财务工单的核实、授权、受理及证据关联。

## 实际文件

- `internal/studio/content_appeal_http.go`、`http.go` 接入本人申请、分页、详情、原键收据及审核员预演、密码执行和原预演收据。用户范围绑定项目和原登录身份。
- `internal/studio/content_appeal_finance.go` 提供财务去敏队列、申请到工单及工单到申请的只读核对。财务主动绑定原工单或新建同资源工单，核验原冻结身份、版本与正数剩余。
- `internal/studio/operations_access.go`、`operations_cases.go` 扩充 moderator 的内容申诉权限和审计枚举，复用已有财务建单容量、审计及 V14-A 预演、密码确认链路。
- `internal/studio/content_appeal_finance_test.go`、`operations_access_test.go`、`moderation_integration_test.go` 验证有价输出被隔离后的独立审核、财务权限、建单和复用原工单、原冻结释放、申请人可见字段。
- 026 表、事务内证据和审议状态机为同批 119 的实现；工作台与浏览器验收见 121。

## 接口与数据影响

| 调用人 | 路径（相对 `/api/v1`） | 行为 |
| --- | --- | --- |
| 申请人 | `POST /content-appeals`、`GET /content-appeals[/<id>]`、`GET /content-appeals/receipt` | 提交携带稳定 `Idempotency-Key`。读取携带 `project_id`；原键补读在请求头使用原键。列表省略申请正文。 |
| moderator | `GET /operations/content-appeals[/<id>]`、`POST /operations/content-appeals/<id>/plans`、`POST /operations/content-appeals/<id>/execute`、`GET /operations/content-appeals/<id>/plans/<plan>/receipt` | 工作区范围内查看申请、预演维持/记录意见/财务转交，密码确认后形成可核对的正式决定。 |
| finance | `GET /operations/content-appeals/finance`、`POST /operations/content-appeals/<id>/finance-case`、同路径 GET、`GET /operations/cases/<case>/content-appeal` | 查看已提交财务核实资格的安全身份，按原键及申请版本建单或复用原工单，双向读取绑定。GET 可带原键核实该绑定是否来自本人原操作。 |
| finance | 原 `/operations/cases/<case>/plans`、`/execute` | 按原工单证据及角色独立预演、密码确认实际释放。 |

工单绑定与审计在同一事务。申请人只获得正式状态 `REFERRED`、`RESOLVED_OTHER` 或 `RELEASED`，完成释放时可见正式收据 ID 和整数字符串 `released_micros`。数据库按原冻结、同作用域不可变 RELEASE 分录、实际执行的账本操作及正式工单收据核对。单独的审核结论、财务转交和释放预演均保持冻结。原 Run/Task、审核版本及候选的隔离继续生效。

## 验证结果

本机 `go test ./internal/studio -run '^(TestContentAppealFinancePublicFields|TestOperationsRoleMatrixAndClosedInputs)$' -count=1` 通过，0.355 秒；`go vet ./internal/studio` 通过。真实 PostgreSQL 专项 `TestPostgresContentAppealFinanceReceiptAndRelease` 使用已受理且隔离的有价文本 Run，覆盖独立 moderator/finance、同资源已有工单与新工单、原键重放/异参、无提前释放、最终准确金额与原输出门禁；026 隔离库实测 3.38 秒通过。三账户 Chromium 与联备 18.95 秒通过，原图保持隔离，实际释放 200000 微 CREDIT。详见 [122 联合验收](2026-11-21-122-content-appeal-integration.md)。

## 风险与限制

CREDIT 为站内权益。输入审核拒绝尚未创建 Run/Task；输出未产生可回读正文时，审议只能依据留存的审核证据与申请说明。真实审核商语义判断、支付渠道退款、账户申诉和自动额度风控另行验收。

## 回滚方式

停写后恢复匹配版本的数据库及私有文件联备，保留已有账本、审核决定、026 申诉和工单绑定。

## 文档同步

README、Wiki、台账导航、任务源及生成 TODO 由集成人同步。
