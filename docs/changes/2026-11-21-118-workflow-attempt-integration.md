# 118 · 局部重跑、流程取消与进程中断验收

## 目的

让用户复用已批准的上游成果、核对重跑范围和费用，停止流程后仍能查询每次尝试的原始结果。

## 范围

V19-C 的跨步骤集成，覆盖 H02、H03、H05、H08、H10。结合 115 重跑、116 取消、117 工作台，以及 A/B 的文本、图像、人工选择和视频。

## 实际文件

- `internal/studio/workflow_attempt_integration_test.go`、`workflow_integration_test.go` 覆盖真实进程强杀、事件补读、取消及重跑 COMMIT 丢回执、自 024 升级和 Chromium 用户流程。
- `internal/studio/outbox.go` 产生固定形状的持久事件。`storage_maintenance.go`、`storage_maintenance_delivery_test.go`、`ledger_admin.go`、`operations_admin.go` 保持精确版本。
- `apps/workbench/src/durableEvents.ts`、`durableEvents.test.ts`、`useStudio.ts`、`App.tsx` 将同项目流程或资产变化合并为只读刷新。两张 Chromium 手机实测图保存于 `docs/wiki/assets/workflow-video-mobile-2026-11-21.png` 与 `workflow-attempt-history-2026-11-21.png`。OpenAPI、[决策 0025](../wiki/decisions/0025-workflow-attempts.md)及任务与运行文档同步。

## 接口与数据影响

同一项目的 workflow-runs 下增加重跑预览、重跑、取消及两种原键收据。新尝试有独立身份、继承关系及授权；项目流新增 workflow.updated。回放原请求不重复执行，也不改变原账务。详细接口见 [OpenAPI](../api/openapi.yaml)。

## 验证结果

- 首组实际 PostgreSQL 与 Chromium 59.139 秒通过。025 维护工具拒绝旧库、缺版本与未来版本；024 原始运行与操作收据升级保持。并发原键只派生一个新运行，背压回滚，同一内容的原图与旧选图身份继续可核对。
- 浏览器从图片节点派生新尝试，修改图片指令，单独生成图片、等待选图后取消，并通过两次只读收据核对已提交但在浏览器丢失的响应。首轮 17.30 秒，文本外发一次、图片外发两次。
- 真实 PG 连接在取消/重跑保存收据的 COMMIT 丢失响应时均返回 503，重启后恢复原操作，合计 5.19 秒。另在已经接收图片 POST 的供应端尚未返回 handle 时强杀应用进程，重启后保留原任务及未知状态；取消父流程、读取原事件和拒绝新尝试均无额外生成，4.37 秒。
- 前端持久事件定向 19 项通过，覆盖通知合并、项目切换、去重、资产变更和只读重置。最新专项 60.883 秒，尝试报价 4.87 秒，终版 Chromium 16.09 秒。全量 PostgreSQL 已在冻结的 025 源码与构建上完成，studio 包 959.929 秒通过，启用三套真实工作流浏览器、大恢复、混合负载及本机版本化 S3。npm run check、356 项前端/34 文件、10 项治理及六套隔离冒烟全部通过。

## 风险与限制

用户可停止本地后续步骤。外部已受理的执行结果与费用可能仍未知，需保留原 handle、原任务和冻结。运行与收据均依赖当前 PostgreSQL 和私有文件的可用性。实际供应商、语义质量及生产容量分别验收。

## 回滚方式

停写并保留当前执行、收费与审计证据，恢复升级前同一时间点的 024 数据库、配套私有目录、owner、密钥与对象版本，再启动匹配版本。

## 文档同步

根 README、协作规则、Wiki、OpenAPI、进度、任务源与生成清单、变更索引同步。整批完成由集成人在实际回归后登记。
