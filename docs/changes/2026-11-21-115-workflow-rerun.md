# 115 · 从选定节点显式创建新的工作流尝试

## 目的

V19-C 在三节点和四节点工作流中支持预览局部重跑。用户决定重跑的起点、核对受影响的后续节点，可更改起点指令，再创建新的项目运行。流程停止、接口和浏览器故障验收另见本批配套台账。

## 范围

V19-C 运行继承、原始及派生输入、报价身份、原键与事务。

## 实际文件

- `db/migrations/025_workflow_attempts.sql` 增加来源运行与修订、代次、起点、独立图和只读继承外键；扩展停止、重跑收据、原子项目通知和受理不明审计的受限取值。
- `internal/studio/workflow_rerun.go`、`workflow_rerun_http.go`、`workflow_http.go`、`workflow.go`、`workflow_store.go` 负责预览、代次、并发创建、原键回放与收据。
- `workflow_outcomes.go`、`workflow_steps_store.go`、`workflow_video.go`、`workflow_execution.go` 保留原任务的唯一归属与通知，复核公开脚本、可见图片与人工选定的原始字节身份。
- `internal/studio/model.go`、`app.go`、`billing_http.go` 和 `workflow_attempt_quote.go` 将新尝试绑定到实际输入和报价。回归位于 `workflow_rerun_test.go`、`workflow_attempt_quote_test.go`。

## 接口与数据影响

项目运行新增 `GET /rerun/preview?from_node=节点ID`、`POST /rerun`、`GET /rerun/receipt`。预览返回来源运行、修订和按依赖排列的继承/重跑节点 ID。创建需幂等键、原运行修订和起点；生成节点可改自己的提示指令，文本起点也可改运行输入。新记录保留固定定义、模型、规格、渠道、有效运行图以及来源运行的确切版本。相同原键及参数读回同一尝试，改变请求获得冲突。已撤销会话不能使用原键重放。

| 重跑起点 | 新尝试继承的事实 | 初始状态 |
| --- | --- | --- |
| 文本 | 固定定义和模型，使用确认后的运行输入 | `TEXT_READY` |
| 图片 | 原文本 Run 和实际公开脚本 | `IMAGE_READY` |
| 人工选图 | 原文本、图片 Task 和原候选集合；用户重新选择 | `WAITING_APPROVAL` |
| 视频 | 原文本、图片 Task 及人工选定的同一私有内容版本 | `VIDEO_READY` |

每个重跑范围中的生成步骤获得新的私有子键。继承字段与独占子外键分开，原尝试的迟到结果只能查到原业务所有者。操作不会自动产生新收费步骤。原执行可能仍在供应商运行、无提交回执、需核对或文本传输出错时，服务保留原资源并拒绝新尝试。用户选择内容相同的新尝试也需使用自身的报价。新尝试的 Run/Task 输入多一项 `workflow_attempt_id`，报价摘要与受理事务都校验它。没有标记的原始请求保留旧幂等摘要。

运行创建、分支创建、子受理关联、公开终态与人工确认在业务事务中写 `workflow.updated`。Outbox 达上限时这些业务写入一并回滚，恢复容量后可用原键再次提交。PG 重跑读取按项目和最多三个原子业务索引查证；每个项目的 128 次运行总额包括所有分支。

## 验证结果

- `go test ./internal/studio -run '^TestWorkflow(RerunPreviewUsesPortsNotEditorOrder|VideoInputBindsChosenOriginalAndScript|GraphValidatesPortsBoundsAndDAG)$' -count=1` 在报价接线后通过，包耗时 0.281 秒。
- 集成人首轮真实 PG 组合用时 59.139 秒，`TestPostgresWorkflowRerunForksOnlyAffectedNodes` 1.18 秒，`TestPostgresWorkflowRerunRefusesUnresolvedChildrenAndSerializesOriginalKey` 1.22 秒，`TestPostgresWorkflowRerunBackpressureAndAmbiguousCancelledChildren` 1.11 秒，`TestPostgresWorkflowRerunRejectsRevokedSessionEvenForReplay` 1.58 秒。涵盖四种起点、继承外键、不同选图、迟到回调、真实双连接同键竞争、受理不明、撤销会话、背压回滚和原键恢复。记录于 `.runtime/v19c-focused-pg.log`。
- 集成人最终真实 PG 组合 60.883 秒，`TestPostgresWorkflowRerunNeedsNewTextAndImageQuotes` 4.87 秒通过。同项目、相同模型和参数的旧未用报价仍未消费；直接普通提交拒绝，文本和图片的新报价各产生一笔原冻结。四项存储回归分别再次通过，用时 1.98、4.53、2.28、1.56 秒；实际浏览器 16.09 秒、重跑及停止的 COMMIT 回执丢失 4.15 秒。记录于 `.runtime/v19c-final-focused-pg.log`。所有五项 `^TestWorkflow` 非 PG 单元测试另通过，用时 0.217 秒。

## 风险与限制

代次表示父子深度，同一来源可以有多个独立分支，区分分支需使用运行 ID。已发送且无法证实终态的资源仍需原运营核对。工作流未提供跨供应商强制取消或上游费用证明。

## 回滚方式

回滚需停机恢复升级前同一时点的 024 数据库、私有目录、owner、适用密钥及对象版本，再运行匹配的程序；核对 025 期间新增的尝试、权益和交付。

## 文档同步

集成人维护根 README、[Wiki](../wiki/README.md)、[任务源](../planning/tasks.json)、[工作流契约](../planning/v19-contract.md)、[台账索引](README.md)、生成 TODO 和推进记录。真实审核商、外部媒体账号与生产收费需独立验收。
