# 004 · PostgreSQL、账户会话、项目隔离与恢复

日期：2026-11-13。阶段：持久工作台与真实认证，不再局限于进程内 demo 数据。

## 目的

解决重启丢失任务/历史/幂等、无账户身份与工作区隔离的问题，并在真实异步媒体接入前建立可恢复且拒绝重复提交的持久执行边界。

## 范围

PostgreSQL 可选仓库、单写锁、事务受理、用户注册/登录/注销/session、项目管理与 ACL、PG 分页/缓存淘汰、流式检查点与恢复、媒体 handle/精确请求持久化、数据库故障停止执行。继续兼容默认 memory demo 和旧冒烟；服务仍只监听回环。

## 实际文件

- `go.mod`、`go.sum`：锁定 pgx v5.13.0、x/crypto v0.59.0、媒体 WebP 所需 x/image v0.48.0 及传递依赖。
- `cmd/server/main.go`：DATABASE_URL、AUTH_MODE、媒体和资产环境变量接入，准确启动日志。
- `internal/studio/model.go`、`app.go`、`http.go`：所属工作区/供应模式、恢复执行、检查点、API 鉴权和资产 ACL、SSE/项目作用域，以及有原handle的安全reconcile续查/15秒持久冷却。资产读取经ACL后使用独立只读store，停用网关不丢作品访问。
- `internal/studio/config_paths.go`：私有资产目录与Web构建/Vite public的路径隔离检查。
- `internal/studio/auth.go`：bcrypt、随机会话/token 哈希、限流、注册登录和项目接口。
- `internal/studio/persistence.go`、`repository_queries.go`：持锁连接、事务创建、检查点、恢复、分页与历史按需加载。
- `internal/studio/migrations/001_core.sql`：初始账户/资源/幂等表与索引，编译时内嵌。
- `internal/studio/provider.go`：MediaHandle 身份/RetryAfter、MediaResult 错误/RetryAfter，与媒体代理共享边界；demo回复改为按工作区存储提示说明历史，避免在PG模式错误宣称重启清空。
- `internal/studio/persistence_test.go`：真实 PostgreSQL、会话隔离、恢复、检查点及容量边界测试。
- `docs/wiki/persistence-auth.md`、`docs/wiki/backend.md`、`docs/api/openapi.yaml`、本台账：同步当前实现契约和约束。

媒体 HTTP/资产实现及专项测试由独立媒体台账记录，不将其冒充本模块自有修改。

## 接口与数据影响

新增 `/api/v1/auth/register|login|logout|session`、GET/POST `/api/v1/projects`、带所有权检查的 `/api/v1/assets/{id}`；bootstrap 增加身份、项目列表和 capabilities，storage 支持 postgres。项目选择使用 X-Project-ID 或 SSE project_id 查询参数；创建 body.project_id 验证所属关系。

列表增加 limit/offset 分页元数据，仍保留 items 兼容。Run/Task 增加 workspace_id/provider_mode；重启 LLM 状态 INTERRUPTED，媒体增加 SUBMIT_UNKNOWN/RECONCILE_REQUIRED/PROVIDER_STATE_UNKNOWN/PARTIAL。数据库事务保存创建与幂等，LLM 单秒定时/终态检查点，媒体精确请求与 handle 持久；单实例持锁且数据库失联不回退 memory。

## 验证结果

- `go test ./... -count=1`（设置 TEST_DATABASE_URL，真实 PostgreSQL 18.7）：通过，包含独立 schema 创建与清理。
- `TestPostgresPersistenceAuthIsolationAndRecovery`：注册/会话、隔离、历史越过缓存上限、跨重启幂等、LLM 中断、媒体续查、禁止未知重提、第二写实例拒绝、故障关闭均通过。安全reconcile只Query、不Submit、无ID拒绝、客户端伪造ID拒绝与冷却测试通过。
- `TestPostgresPeriodicAndTerminalCheckpoints`：停顿流定时保存与取消强制保存通过。
- `TestPostgresWriterLossCancelsIdleProviderWithoutHTTPActivity`：没有新HTTP/delta时也检测持锁连接损坏，取消等待中的provider。
- 最大128KiB逐字节事件测试验证恢复seq严格覆盖全部输出/终态；PG测试验证恢复游标高于未检查点游标，重复重启已INTERRUPTED不会再次跳号。
- 历史媒体读取不需要当前网关key，跨账户asset/reconcile拒绝，reconcile冷却跨重启持久。
- `go vet ./...`：通过；原 demo 单元/HTTP 测试仍通过。
- 未配置 TEST_DATABASE_URL 时 PG 用例明确 Skip，不能把该运行视为 DB 验收。
- 本机 race 受 CGO/C 编译器缺失限制；未声称已完成真实收费网关联调、性能压测或多实例容灾。

## 风险与限制

所有 PG 操作依赖同一持锁连接，正确性优先的单写基线存在串行事务/状态锁瓶颈。没有生产 HA、独立迁移/回滚工具、邮箱验证/找回/MFA、共享限流、成员 RBAC、账本支付或人工核实后台。约 1 秒检查点是调度间隔，不是故障中绝对 RPO；崩溃可丢失尚未落库片段。offset 分页在并发新增时可能重复，前端按 ID 去重。未知或配置身份变更任务停留待核实，不能靠新幂等键盲目重发。

## 回滚方式

停止服务，保留 PostgreSQL 和资产数据备份；可以明确切换 AUTH_MODE=disabled 并移除 DATABASE_URL 回到独立 memory demo，但这不是读取原账户数据的降级。禁止删除账户或数据表作为常规回滚。撤回代码时先确认正在执行的真实生成与供应商状态；本轮迁移为新增表，无自动 down migration，数据库回滚由备份/迁移评审决定。

## 文档同步

[持久化认证 Wiki](../wiki/persistence-auth.md)、[后端 Wiki](../wiki/backend.md)、[OpenAPI](../api/openapi.yaml) 已按本轮行为同步。根负责人同步 [README](../../README.md)、[Wiki 索引](../wiki/README.md)、[台账索引](README.md)、启动脚本和 CI，并运行治理检查。002 历史台账保持原先验收事实，不改写历史。
