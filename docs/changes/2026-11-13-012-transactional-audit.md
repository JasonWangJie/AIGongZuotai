# 012 · V04 事务业务审计与活动查询

日期：2026-11-13。关联批次：**V04**；状态由根负责人在[任务登记](../planning/tasks.json)和生成的 TODO 中统一核对。

## 目的

为已授权的项目、生成受理、取消、原任务核对及云草稿保存建立可追溯活动记录，解决“业务成功但审计缺失”的事务断层。它不是计费、消费账单或完整账户安全审计。

## 范围

PostgreSQL 同事务审计 INSERT、按工作区/项目隔离的只读 keyset 分页、封闭字段脱敏、memory 512 条循环记录、迁移版本加载，以及事务失败/重放/权限测试。协作接入 V03 云草稿保存审计与四个 HTTP handler；草稿自身实现见独立 010 台账。

## 实际文件

- `internal/studio/audit.go`：封闭 AuditEvent/AuditDetails、同事务写入 helper、memory 环、复合游标及只读查询。
- `internal/studio/migrations/002_audit.sql`：审计表、动作约束、复合所属外键、分页索引。
- `internal/studio/persistence.go`：内嵌迁移按版本加载，资源/幂等与审计同事务，带审计的状态事务。
- `internal/studio/app.go`：受理、取消和核对的审计接线；取消/核对使用候选副本，提交后才替换内存并广播。
- `internal/studio/auth.go`：项目创建、注册默认项目与审计同事务；不新增登录/注销审计。
- `internal/studio/http.go`：GET audit、操作者上下文传递、云草稿四个 handler 挂载。
- `internal/studio/audit_test.go`：字段最小化、memory 上限、复合游标、权限、重启和 PG 审计故障注入。
- `docs/api/openapi.yaml`：V03/V04 实际接口合并，共 22 路径/26 操作。
- `docs/wiki/audit.md`、`docs/wiki/backend.md`、本台账：实现、验证、边界及回滚说明。

## 接口与数据影响

新增 `GET /api/v1/audit`，scope=project/workspace、limit=1..100、最大 1024 字符 cursor，返回 items/next_cursor/storage。按 `(created_at,id)` 排他游标分页，绑定当前工作区/项目。所有读操作继承原会话与所属关系检查。

动作固定为 project.created、run.accepted、task.accepted、run.cancelled、task.cancelled、task.reconcile_requested、draft.saved。只记录内部 ID、操作者类型、动作、时间与受限状态/模型/修订计数，不存 prompt/output/正文/邮箱/key/password/cookie/幂等键。创建事务同时保存业务、幂等和审计；重放与无状态变化的重复取消不插入重复记录。PG 模式持久，memory 最多 512 条且重启丢失。

## 验证结果

- `go test ./...`、`go vet ./...`：通过。
- 设置真实隔离 TEST_DATABASE_URL，`go test ./... -count=1`：第一轮全量通过，约 10.379 秒。
- 候选副本修正后 `go test ./internal/studio -run TestPostgresAudit -count=1 -v`：通过，约 3.334 秒；覆盖同时间 keyset、权限/重启及项目/注册/受理/取消/核对故障回滚。
- 通过真实数据库 trigger 拒绝审计 INSERT，验证没有孤立业务行、幂等行或未审计的状态提交；任务取消/核对保留旧内存并且不广播。
- 云草稿代理另行执行真实 PG 的并发 CAS、幂等、修订不可变和审计失败回滚专项通过，见 010。
- 未执行生产容量、真实付费生成或防篡改认证；无 TEST_DATABASE_URL 的测试会跳过 PG，不作为数据库验收证据。

## 风险与限制

业务活动只覆盖本台账列出的动作；登录尝试、账户安全、后台自动状态、每次模型调用及账务不在本轮完整覆盖中。数据库管理员仍可修改数据，不宣称不可篡改/WORM。PG 历史未配置自动保留期；memory 最旧记录会覆盖。数据库故障熔断 503，无静默异步补审计或 memory 降级。应用仍是单写、回环本地阶段。

## 回滚方式

停止服务并保留数据库与资产备份。迁移 002/003 已应用后不能直接回退旧二进制，尤其不能删除 schema_versions 记录或审计/修订数据来绕过版本检查。若确需回退，使用匹配旧版本的备份恢复到独立数据库，再验证身份/业务数据；当前库保留以便核对。没有自动 down migration，也不自动清理用户数据。

## 文档同步

[审计 Wiki](../wiki/audit.md)、[OpenAPI](../api/openapi.yaml)与本台账已同步。根负责人统一更新 README、Wiki/台账索引、V04 任务状态与 TODO，并执行最终治理检查。此模块未并发编辑根任务登记或索引；历史 002/004 台账不改写。
