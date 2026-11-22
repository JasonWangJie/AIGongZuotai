# 168 · 本人工单、工作区客服答复与原操作收据

## 目的

实名用户可以提交客服问题、补充说明、查看答复并关闭工单。按工作区单独获授客服角色的人员可查阅该区工单，使用当前密码确认回复。提交响应丢失时，可凭原键读取该次操作结果。

## 范围

V22-B 的客服接口、角色、停机授权工具与验证。167 提供 034 工单、消息和收据的事务存储。普通账户的查询范围限于当前工作区的本人工单。客服读取和回复另需启用运营功能，并在目标工作区显式获得 `support`。用户正文只在已授权的工单详情及对应收据出现。

## 实际文件

- `internal/studio/support_ticket_http.go` 注册本人和客服入口，限定请求形状、原键、本人或工作区作用域，接入回复密码核验。
- `internal/studio/operations_access.go`、`internal/studio/operations_admin.go`、`cmd/operations/main.go` 新增专属角色、权限及停机命令参数说明。
- `internal/studio/operations_access_test.go`、`internal/studio/support_ticket_http_test.go` 覆盖新旧角色矩阵、输入界限、工单对话、密码、原版本收据与撤权。
- `docs/api/openapi.yaml` 登记本人和客服共 9 条路径、10 个操作和 13 个新数据结构，并向既有授权信息补充角色及权限取值。`docs/wiki/decisions/0034-support-tickets.md` 记录授权、对话、收据及恢复决策。

## 接口与数据影响

本人基址为 `/api/v1/support/tickets`。GET 列表支持游标，GET `/{id}` 读详情。POST 基址创建工单，正文仅含 `category,subject,body`。分类为 `account|billing|generation|other`，标题最多 120 字符、480 字节，消息最多 2000 字符、6000 字节。POST `/{id}/followup` 带 `expected_version,body`，POST `/{id}/close` 带 `expected_version`。每次写入需 `Idempotency-Key`。GET `/receipt?operation=create|followup|close` 带原键读取原时点版本。列表每页最多 20 条，不含对话正文。

客服基址为 `/api/v1/operations/support/tickets`。GET 列表、详情及 `/receipt?operation=reply` 均须指定 `workspace_id`。POST `/{id}/reply` 携带工作区、预期版本、消息、当前密码和原键；先核该区的专属权限，再执行有界密码验证，最终写入或原键回放时在事务中重核账户、会话、凭据及实时角色。密码不进入业务消息结构、原键摘要或收据。查询请求无需密码。新角色只包含 `support_ticket`，默认的普通项目所有者及既有运营角色不会因此获授客服权限。

服务端从会话取得本人，接收最多 32 KiB 的单份 UTF-8 JSON，按端点拒绝多余、大小写别名、重复及 null 字段。成功创建返回 201，重放返回 200 和 `Idempotency-Replayed: true`；并发更新或关闭后再写返回 409。响应不明时以 GET 原收据核对。客服被撤权后，原工单、原稿回复收据及回复重放均须重新取得有效授权。

## 验证结果

034 工单专项十项通过，共 48.800 秒。三账户真实 Chromium 10.69 秒，停写撤权后在新库恢复 10.57 秒，四种操作丢失提交确认后的原键回读共 11.57 秒。浏览器核对四个版本及原收据，数据库确认追问和答复各自的操作者，权益与媒体均无新增。

`go test -count=1 ./internal/studio -run '^Test(SupportTicketHTTPExactFieldsAndOfflineRole|OperationsRoleMatrixAndClosedInputs|OperationsAdminRejectsInputsBeforeConnection)$'` 本地通过，0.259 秒。覆盖 6000 字节正文的 Unicode 转义、伪造身份或密码字段、超限、内存模式、停机角色验证及双向权限矩阵。`go run ./cmd/operations --help` 已实际输出 `support` 选项。

真实 PostgreSQL/HTTP 复跑入口为 `go test -p 1 -count=1 -v ./internal/studio -run '^TestPostgresSupportTicketHTTPScopedReplyPasswordAndOwnerReceipts$'`。三账户覆盖工单创建、跨区及普通观察员拒绝、错误密码与正确密码、原键回复收据、并发补充冲突、关闭和撤权。编写期间未连接 PostgreSQL，实测由集成人串行登记。

使用 PyYAML 6.0.3 严格解析并拒绝重复键，使用 jsonschema 4.26.0 的 Draft 2020-12 验证 13 个新数据结构及 18 组合法或应拒绝的消息、详情、请求及分页样例。全部 189 个 operationId 唯一；核对了 10 个新操作的 HTTP 方法、路径、必填身份参数及 139 处新增引用。与编辑前的 033 文档逐项比较，原 156 条路径和 201 个原组件保持一致；`OperationsAccess` 唯一变化为角色追加 `support`、权限追加 `support_ticket`，其余字段与文件头一致。

## 风险与限制

客服可在授权期间阅读相应工作区工单的用户自述。此入口用于站内人工交流，不处理退款、额度、审核解除或代替用户执行业务，不投递外部邮件。密码和待确认回复无需离线持久化。相同回复键的再次 POST 仍要当前密码，核对提交结果应使用 GET 原收据。

## 回滚方式

通过既有停机 CLI 的预演、稳定操作键和精确摘要撤销对应工作区的 `support`。原工单和收据继续保留。回退至 033 需恢复升级前的数据库、私有目录和配套软件。

## 文档同步

由集成人同步 README、Wiki、OpenAPI、台账索引、任务源与 TODO，并登记真实 PostgreSQL 和工作台联合验收。
