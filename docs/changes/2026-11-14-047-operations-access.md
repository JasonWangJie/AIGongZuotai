# 047 · V14-A工作区运营权限与离线角色管理

- 日期：2026-11-14
- 批次：V14-A（V14仍in_progress）
- 横切：H01身份与权限、H06审核与运营、H09工程治理

## 目的

为异常核实工单提供默认关闭、按工作区授予的运营权限，敏感执行二次密码核验、同事务授权重核和不可变运营审计；角色只由停机CLI管理。注册/所有者没有默认运营身份，不开放HTTP grant。

## 范围

V14-A首个运营纵向子批次的权限与敏感认证基础、离线角色管理及相关运维文档，覆盖H01/H06/H09。完整V14仍为in_progress，审核、申诉、封禁及风控不属于本记录的已交付能力。

## 实际文件

- `internal/studio/operations_access.go`、`operations_access_test.go`：角色并集、access列表、身份与事务权限检查、当前请求私有密码proof、有界bcrypt和审计helper。
- `internal/studio/operations_admin.go`、`operations_admin_test.go`：inspect/grant/revoke、停机writer锁、001..014版本检查、精确计划摘要、稳定操作键及原回执恢复。
- `cmd/operations/main.go`：显式环境CLI，不加载.env、不迁移、不启动任务、不访问供应商；错误不回显连接串。
- `internal/studio/migrations/013_operations_access.sql`：scoped grants、不可变audit、不可变role operation receipt。
- `internal/studio/auth.go`：identity增加只表示入口启用状态的operations capability；不缓存或隐含授予角色。
- `docs/wiki/operations-access.md`：角色表、配置、敏感操作和离线操作流程。
- `docs/wiki/billing-operations.md`、`docs/wiki/storage-maintenance.md`：当前CLI要求完整001—014，保留历史验收事实，并补充V14-A线上核实与联备回退边界。
- `docs/changes/2026-11-14-047-operations-access.md`：本记录及最终联合验收证据补充。
- 根集成人维护Config/env/HTTP注册、014协作、顶层README/Wiki导航/台账索引、任务状态及050联合验收。

## 接口与数据影响

GET `/api/v1/operations/access`读取当前会话实时授权，返回有界scope/role/permissions/version；每次受保护请求查询PG，所有写事务再次校验。权限为read、case_open、investigate、finance；observer只读、operator调查、finance财务，后两者均可开工单。

敏感proof不序列化、不持久化、不返回浏览器；执行事务重新检查用户凭据、会话有效期、原散列及授权。密码计算在事务与全局状态锁外，拒绝非法UTF-8/NUL/超长输入，使用现有认证限速与并发限制。

权限、审计、稳定操作回执同事务。离线预演实际执行后回滚；apply检查数据库身份、精确请求、授权版本/状态、凭据版本和容量。原操作重放只恢复既存回执，不会重新授予已撤销角色。active授权为未来revoke预留一个安全整数修订；最终失效版本禁止再grant。输入严格限定ID及有界opaque值，不把NUL送入PG。

## 验证结果

- `gofmt -w`已应用于本批Go新增文件。
- `go test ./internal/studio ./cmd/operations -run '^TestOperations' -count=1`：2项普通测试通过，初次studio包0.056s；版本容量修正后再次通过0.083s，CLI编译通过。
- 仅加载忽略文件`.runtime/postgres.env`的`TEST_DATABASE_URL`，使用隔离schema执行`go test ./internal/studio -run '^TestPostgresOperations' -count=1 -v`：4项通过，studio包6.382s。
- 上述PG覆盖scope/multi-role/revoke、恶意scope无熔断、password proof fences和bcrypt容量、审计回滚、CLI dryrun/apply/replay/revoke/stale digest/writer fence及不可变证据。
- 专项后补active revision预留与同一offline测试中的边界回归。根集成执行的最终全量PG验证通过，studio包169.657s，包含该新增撤权容量边界；详见[050联合验收](2026-11-14-050-operations-integration.md)。此前6.382s仍只代表专项当时版本，不冒充修改后验证。
- 收尾修正标准章节、当前CLI版本说明及V14-A链接后执行`node scripts/check-docs.mjs`：通过，104份文档、50份台账，本地链接与必需字段有效。
- 未使用主库作为测试数据库，未访问真实供应商、SMTP或支付渠道。生产多因素认证、双人审批及完整V14治理未验证也未实现。

## 风险与限制

OPERATIONS_ENABLED默认false，需PG+required鉴权。CLI必须停机，操作键/证据引用由运维负责留存；当前审计的离线actor为空，通过operation/grant/reason/evidence追踪。没有在线权限编辑、全站管理员、角色继承或跨工作区通配授权。角色认证不自动开放普通用户私有正文/文件。

## 回滚方式

013/014升级按根集成联备并保护writer/schema/owner栅栏。停用入口可恢复只提供普通工作台；回退程序和结构须恢复升级前完整联备，不删除迁移记录、审计或owner强行降级。

## 文档同步

本批新增Wiki与本台账；根README最近变更、Wiki导航、台账索引和唯一任务源由根集成人合并。V14-A完成不等于完整V14验收，最终联合结果见050。
