# 116 · 停止工作流并核对原子任务

## 目的

V19-C 允许项目用户停止原流程的后续节点，并在刷新或重新登录后查看原取消结果和原子资源的真实状态。适用 H02、H03、H05、H10。运行已完成或失败时，详情仍可查询。等待人工选图和待核实流程均可主动停止。[交付契约](../planning/v19-contract.md)

## 范围

V19-C 原父流程取消、原子资源与权益状态、并发撤销和可核对收据。

## 实际文件

- `internal/studio/workflow_cancel.go`、`workflow_http.go` 提供取消、原键收据、父版本比较、活动子资源核验及事务提交后的缓存通知。
- `internal/studio/run_lease_store.go`、`repository_queries.go` 复用既有控制代际、账务锁、持久文本前缀、媒体记录和租约存储。
- `internal/studio/workflow_cancel_test.go` 覆盖待审受理、执行中私人候选、事务回滚、取消回放、排队和提交不明媒体及原句柄续查。025 迁移及重跑属于本批独立记录。

## 接口与数据影响

`POST /api/v1/projects/{project}/workflow-runs/{workflow}/cancel` 接收 `expected_version` 和独立的 `Idempotency-Key` 请求头。项目、会话、版本、取消回执、父阶段与父 Outbox 事件在同一事务核对并提交；可本地停止的子资源，其审核、权益、审计、Outbox 与租约变更一并提交。返回持久运行详情。`GET /cancel/receipt` 使用原路径、原键请求头补读。相同请求回放原回执，复用原键变更版本会冲突。

| 取消时子状态 | 结果 |
| --- | --- |
| 尚无已受理子资源，或用户正等待人工确认 | 父流程进入 CANCELLED。锁外输入审核或私有正文准备晚完成时，受理事务再次核父状态，不能新建子任务。 |
| 聊天排队或执行中 | 读取原提交前缀，撤销旧租约，停止本地执行。未交付的受审候选继续隔离，已执行权益按原规则待核实。运行槽在实际执行退出时释放。 |
| 媒体已排队、未越过提交界线 | 本地取消子任务并阻断后续提交。 |
| 媒体处于 SUBMITTING 且尚无 handle | 保留原任务和请求，撤销旧写租约，进入 SUBMIT_UNKNOWN。已发上游的请求仍可能生效；原业务保持待核实。 |
| 媒体已有 handle、已进入交付审核，或原待核实 | 原任务继续按其持久证据查询、交付或待核实。父流程已停止，旧子结果无法启动后续节点。实际交付与收费仍从原子任务读取。 |

## 验证结果

新增真实 PG 专项 `TestPostgresWorkflowCancelDuringInputReview`、`TestPostgresWorkflowCancelTextRollbackAndReceipt`、`TestPostgresWorkflowCancelQueuedUnknownAndKnownMedia`，均使用隔离配置和本机测试供应方。可用 `go test ./internal/studio -run '^TestPostgresWorkflowCancel' -count=1 -v` 复跑。首组真实 PG 与 Chromium 联合专项耗时 59.139 秒，包含进程强制结束后的持久事件核对（4.37 秒）、取消及派生操作提交确认丢失后的只读收据（5.19 秒）。加强权益断言覆盖排队释放、提交未知保留 REVIEW、原 handle 交付后按原件结算。最终真实 PostgreSQL 专项 60.883 秒通过，含加强后的权益断言；冻结 025 源码的完整 studio PostgreSQL 回归 959.929 秒通过。联备、最终 Chromium 及其余验证见 [118 集成记录](2026-11-21-118-workflow-attempt-integration.md)。

## 风险与限制

供应商没有公开取消接口的任务可能继续在上游执行。父状态、子状态与账务各保留原始决定。没有可用 handle 的不明生成停止自动请求，并保留其冻结风险；新尝试仍受独立授权和安全重跑门禁约束。适用范围为单控制写者 PostgreSQL 环境。

## 回滚方式

停写后使用匹配版本、同一时间点的数据库与私有存储联备恢复。已有取消回执、选图和新旧尝试保留，不能删除 025 登记强制旧程序写入。

## 文档同步

README、Wiki、进度、台账索引及生成 TODO 由集成人同步。
