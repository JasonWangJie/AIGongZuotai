# 124 · 按目标账户授权、限制会话与申诉 HTTP

## 目的

V14-C2b 将实名账户的限制和申诉与创作工作区权限分开。受限用户可以重新登录、使用账户安全功能、查询自己的限制、申请复核和核对原回执。获授权的运营人员只能处理所分配的具体账户。

## 范围

H01、H02、H03、H05、H08、H09；本地 PostgreSQL、单控制写者、已启用实名账户。运营接口还要求 `OPERATIONS_ENABLED`。原角色和工作区生成限制维持各自权限和修订。

## 实际文件

- `internal/studio/account_governance_http.go`、`http.go`、`auth.go` 提供账户复核、运营目标列表、密码执行、受限登录响应、默认拒绝的工作台路由。
- `internal/studio/account_governance_admin.go`、`operations_admin.go`、`cmd/operations/main.go` 扩展停机管理命令，复用控制写者排他锁、精确格式栅栏和联备身份。
- `internal/studio/app.go`、`conversation.go`、`persistence.go`、`billing_http.go` 在真实生成及报价受理事务中重核原登录会话、当前控制状态。`outbox_http.go`、`run_stream_replay.go` 随 SSE 订阅检查原会话。
- `internal/studio/account_governance_http_test.go` 运行离线授权、受限工作台、申诉、原键、实际 SSE 及输入审核阻塞后撤权的回归。`workflow_store_test.go`、`workflow_video_test.go` 适配内部调用签名。

## 接口与数据影响

| 调用人 | 接口（相对 `/api/v1`） | 行为 |
| --- | --- | --- |
| 本人 | `GET /account/access`，`POST/GET /account/appeals`，`GET /account/appeals/{id}`，`GET /account/appeals/receipt` | 原限制轮次、本人陈述和正式答复。提交和创建收据读取携带同一 Idempotency-Key。 |
| 指定运营人员 | `GET /operations/account-targets`，目标下的状态、`/appeals`、`/appeals/{id}` | 只列此人当前有效的运营目标，权限每次从 PG 核实。 |
| 指定运营人员 | 目标下 `POST /plans`、`POST /execute`、`GET /plans/{plan}/receipt` | 设限、维持或解除，预演绑定版本、目标及身份；执行重新验证密码。未知结果按原计划只读查询。 |

停机环境先运行 `go run ./cmd/operations -action grant -user <operator_user_id> -target-user <target_user_id> -operation-key <stable_key> -reason <reason_code> -evidence <reference>` 预演，审核后使用相同参数及 `-apply -plan-digest <digest>`。只读 `inspect` 仅给定两名不同用户的 ID；撤权使用新操作键和相同预演步骤。专属授权不接受工作区或角色参数。数据库保留原操作请求摘要、实际收据与独立离线审计；重放已撤销的原授权不会产生新权限。

普通请求按入口处的当前会话与控制状态判断。已开始的普通读取、传输和一般业务处理允许完成。三类新生成和权益报价另在受理事务锁用户并重核原 session；预审期间设限且之后解除也无法以旧会话外发或冻结。已提交的生成继续按原任务、租约、权益运行。账户状态切换撤销目标旧会话；受限状态登录只返回本人及安全能力。原 SSE 在有界订阅内按心跳与 5 秒中的较短间隔读原会话，关闭时等待已开始的数据库检查；数据库排队、单次 DB 检查和已开始的慢写会增加实际退出时间。已进入网络缓冲的字节可能已交付。

## 验证结果

`go test ./internal/studio -run '^(TestAccountRestrictedRoutesAndTargetGrantInputs|TestOperationsRoleMatrixAndClosedInputs)$' -count=1` 通过，0.309 秒。`go vet ./internal/studio ./cmd/operations` 通过。`TestPostgresAccountGovernanceOfflineGrantReplay` 和 `TestPostgresAccountGovernanceAdmissionAndOpenStream` 由集成人在 027 隔离实例串行运行。

## 风险与限制

操作人员只持有按人分配的治理权。一次受限登录、预演或申诉不会变更既有产物审核或收费。受限状态与密码找回、工作区生成限制和内容审核分别存储。真实邮件、审核商与生产多实例另行验收。

## 回滚方式

停写并联备 027 数据库、私有文件、owner、对象版本及相应密钥。需要回到 026 时恢复同一时间点的完整旧联备并核对账户会话、专属授权、限制和申诉证据。

## 文档同步

由集成人同步 README、Wiki、OpenAPI、台账导航及唯一任务源，并分别登记 026、027 的实测结果。
