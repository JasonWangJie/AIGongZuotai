# 128 · 原报价风控复核与一次显式豁免

## 目的

V14-C3 让确实触发站内权益规则的报价持有人申请一次复核。独立审核员可以批准本人在原报价有效期内显式提交同一请求一次，用户可从原申请追踪批准、过期和实际核销结果。

## 范围

H01、H02、H03、H05、H08、H09。仅针对启用本地 CREDIT 权益的持久实名账户、原项目和有明确所有人的报价。工作区审核授权使用独立角色 `risk_reviewer` 与权限 `risk_review`。

## 实际文件

- `internal/studio/risk_appeal_store.go`、`risk_appeal_http.go` 提供本人项目下的原报价资格、申请/列表/详情/原键回执，以及按工作区授权的审议队列。
- `internal/studio/risk_appeal_decision.go` 实现预演回滚、独立密码确认、期限复查、正式收据和具体豁免的事务签发。`http.go` 注册路由。
- `internal/studio/operations_access.go`、`operations_admin.go`、`cmd/operations/main.go` 提供独立角色的停机授权、权限校验与审计。原 reviewer 以外的运营角色权限各自保留。
- `internal/studio/risk_appeal_test.go`、`operations_access_test.go` 验证角色矩阵、本人复核、财务精确性、未知提交和单次核销。028 字段及实际受理路径见 127。

## 接口与数据影响

| 调用人 | 相对 `/api/v1` 路径 | 准入及可见数据 |
| --- | --- | --- |
| 本人 | `GET /risk/eligibility?project_id=…&quote_id=…` | 读取真实原报价、有效策略、当前已冻结数及分钟受理量，输出规则与金额；当前没有触发时返回不可申请。 |
| 本人 | `POST /risk/appeals`、`GET /risk/appeals[/<id>]`、`GET /risk/appeals/receipt` | 申请使用稳定 Idempotency-Key；GET 显式绑定原项目，收据使用原键。读取列表省略陈述和答复。 |
| risk_reviewer | `GET /operations/risk-appeals[/<id>]`、`POST /operations/risk-appeals/<id>/plans`、`POST …/<id>/execute`、`GET …/<id>/plans/<plan>/receipt` | 只在获授工作区审议；自己的申请必须由另一人处理。计划可批准或拒绝，密码确认、账本策略及真实效果同事务核验。 |

申请冻结用户、工作区、项目、原报价、请求摘要、原冻结额与价格版本、策略版本、按顺序触发的规则及原到期时间。金额为六位精度 CREDIT 微单位的十进制字符串。每个报价和策略版本只有一份申请。原键重放优先于当前资格、报价使用与到期检查。

批准有独立持久收据，确认本身产生一张原报价的单次豁免。用户显式提交仍受额度余额、审核、账户限制、原输入、幂等及其他准入约束。最终冻结、资源与核销在同一业务事务，背压实际回滚后可读取未用许可。已经批准的申请显示 `AVAILABLE`、`EXPIRED`、`INACTIVE` 或 `CONSUMED`，过期后仍可读取原申请和原收据。相同受理键回放原已接受资源。审批和查询均不会发起生成。

## 验证结果

本机 `go test ./internal/studio -run '^TestOperationsRoleMatrixAndClosedInputs$' -count=1` 通过，0.234 秒。真实 PostgreSQL 专项由集成人串行执行，命令为 `go test ./internal/studio -run '^TestPostgresRiskAppeal' -count=1 -v`。

- `TestPostgresRiskAppealOriginalQuoteAndSinglePermit` 从真实 429 开始，验证资格、拒绝自审、密码确认、预演无许可、背压回滚、批准后显式原生成和一次冻结。
- `TestPostgresRiskAppealExpiryAndIndependentDenial` 用数据库时钟等待原报价到期，读取原申请、拒绝过期批准，独立记录拒绝答复。
- `TestPostgresRiskAppealLostCommitReceipts` 在原申请和批准的实际 PostgreSQL COMMIT 处分别断开确认回包。重启后只以 GET 核对原申请或原审批收据，检查许可数量、零新生成与零冻结。

## 风险与限制

豁免只覆盖审核时留存的规则及原策略修订。若新增不同的阻挡、原业务被限制、余额不足或报价失效，普通受理仍可能拒绝。实际政策配置、真实支付及生产风控准确率各自验收。

## 回滚方式

停写并联备 028 数据库及配套私有文件、owner、密钥与对象版本。退回 027 需恢复同一时点的旧联备。已提交的报价、冻结、核销和审批收据按真实交易核对。

## 文档同步

集成人同步 README、Wiki、OpenAPI、变更导航、任务源和生成 TODO。前端与浏览器验收归 129/130，027 验证独立计数。
