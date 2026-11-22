# 093 · 按工作区查询项目与事务内创建上限

## 目的

PostgreSQL 启动时曾把全库项目加载进应用。登录后的项目列表、生成与上传的项目归属判断、工作区 100 个项目的上限，都依赖这份内存数据。本次使这些入口直接使用持久项目记录，并将最后一个创建名额的竞争交给数据库处理。

## 范围

V16-A4.2，涉及工作区与账户、任务受理、参考上传、事件订阅和验证文档。内存演示继续在应用内保存项目。

## 实际文件

- `internal/studio/auth.go` 处理项目列表、注册登录、归属读取和创建事务。
- `internal/studio/app.go` 与 `internal/studio/references.go` 在生成和参考上传入口读取持久项目。默认演示项目仅在内存模式写入本地映射。
- `internal/studio/recovery.go` 的集成修改移除启动时的全库项目预载。
- `internal/studio/project_scope_test.go` 验证项目可见性、订阅隔离及创建名额竞争。本轮集成同步 README、Wiki、任务源、生成清单与台账索引。

## 接口与数据影响

原项目、账户会话、生成、上传与 SSE 接口继续使用原有参数和响应。PG 工作区项目列表按 `created_at,id` 排序；请求选定的项目必须属于当前工作区。创建项目在同一事务内先锁定工作区行，再检查是否已有 100 项，成功后写入项目和审计。超过数量上限仍返回 HTTP 409、`PROJECT_LIMIT`。沿用 020 数据库格式及现有项目索引。

## 验证结果

| 命令或检查 | 结果 | 证据 |
| --- | --- | --- |
| 集成人串行真实 PG 专项 `TestPostgresProjectScopeWithoutPreload` | 通过，1.87 秒 | 进程启动后新增的项目即时可列出、选择、订阅与受理生成；其他工作区订阅返回 404，应用项目映射保持为空。 |
| 同组 `TestPostgresProjectLimitLocksWorkspace` | 通过，1.36 秒 | 两个独立 PG 连接争夺第 100 个项目名额，数据库报告第二个事务等待第一个，前者提交后竞争方返回 `PROJECT_LIMIT`，最终数量为 100。 |
| `go test ./internal/studio -run '^(TestAuthDisabledAndLimiterBoundaries|TestAdmissionConcurrentRunsAndCrossProjectScope|TestReferenceProjectIsolationAndEditingContract)$' -count=1` | 通过，0.271 秒 | 内存模式鉴权、跨项目准入和参考图编辑隔离。 |

## 风险与限制

每次获取身份项目列表时，需要读取当前工作区的项目。项目创建通过工作区锁串行处理，验收覆盖独立数据库连接间的竞争；应用继续采用单控制写者。十万活跃任务完整启动的测量及 Run、媒体执行缓存验收由 A4.2 联合记录汇总。

## 回滚方式

如需恢复旧行为，需一起恢复项目启动预载、注册和创建后的映射维护、生成与参考上传的原读取路径。持久项目表、现存项目、审计和迁移登记均可继续使用。

## 文档同步

集成人在 `README.md`、`docs/wiki/README.md`、`docs/wiki/paged-recovery.md` 与 `docs/changes/README.md` 关联本记录，并在 `docs/planning/tasks.json`、生成的 TODO 及 `docs/planning/progress.md` 登记 V16-A4.2 证据。
