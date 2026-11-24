# 245 · V17 原媒体查询的 039 升级与 040 停写联备恢复

## 目的

确认 039 已受理的任务、原渠道退避与冻结在 040 升级和停写恢复后可按原凭据核实。

## 范围

关联 V17、H03、H06、H07。在隔离回环端点验证已有媒体任务在扩展查询预算后的执行、项目权限与 CREDIT 冻结。依赖 244 的 040 增量与查询预算、242 的独立查询连接、238 的联备辅助和 083 的渠道退避及半开探测。

## 实际文件

- `internal/studio/media_query_format_restore_test.go` 新增可独立执行的升级与恢复验收。使用上一格式的真实服务可执行文件。
- `docs/changes/2026-11-23-245-media-query-format-restore.md` 记录本档条件和证据。

## 接口与数据影响

真实 039 程序须先在一次性 schema 初始化完整 001 至 039。通过普通注册和项目入口建立两个工作区，在旧写者进程退出后为测试所有者登记本机 CREDIT。重新启动同一旧程序，显式报价并受理三个图像请求。本机 HTTP 渠道分别返回正在生成的原 handle、收到 429 的原 handle，以及缺少 handle 的 202。测试核验旧库已存在三个原请求、渠道降档、服务端等待期限、单次探测要求和无 handle 的 SUBMIT_UNKNOWN / REVIEW。

实际执行 040 SQL，并通过回滚事务检查 query 可持久化 1536、拒绝 1537 和 0，其他三条 lane 继续允许 32 并拒绝 33。确认旧行逐项相同、039 真实二进制在 040 数据库拒绝启动，离线存储维护 dry-run 核实格式。停写备份 PostgreSQL、资产及参考目录、交付密钥，使用独立数据库和独立目录恢复。重启后核验原项目的读与原键响应、另一工作区读访问受拒、未知请求没有重新派发。实际等待原库时钟的截止时间，通过明确的原 handle GET 占住半开探测，另一已到期的原 handle 必须等已有探测成功后才能 GET。统计两次恢复后的 GET，断言上游 POST 总数不增加、原输入与 handle 完整保留，未知冻结保持 REVIEW。

## 验证结果

- 禁用 `TEST_DATABASE_URL` 和专项开关，Go 1.26.11 在隔离 overlay 编译新增用例，默认明确跳过。真实 PostgreSQL 与两个实际程序的升级联备尚待集成人串行执行。
- 单独运行时设置 `MEDIA_QUERY_UPGRADE_TEST=1`、`MEDIA_QUERY_UPGRADE_039_SERVER` 为可信 039 原服务的绝对路径、`TEST_DATABASE_URL` 为隔离回环测试库、`TEST_POSTGRES_BIN_DIR` 为本机 PostgreSQL 工具绝对目录，执行 `go test -p 1 -count=1 -v -timeout 5m -run '^TestPostgresMediaQueryUpgrade039Restore040$' ./internal/studio`。测试库使用独立 schema，备份工具需具有创建临时恢复库的权限。

## 风险与限制

上游由本机可控 HTTP 服务提供，真实供应商行为另验。本项覆盖已受理任务的升级、权限、权益、探测和恢复，未测每秒吞吐、全站多实例、云存储恢复或 24 小时稳态。

## 回滚方式

未应用 040 的环境继续使用配套 039 工具。已升级环境应使用匹配格式的程序，回到 039 需要整组停写旧联备，保留原迁移和私有目录 owner。

## 文档同步

集成人根据实际运行结果同步 README、Wiki、台账索引、任务源和生成的双 TODO。

## 集成人 PostgreSQL 恢复结果

`MEDIA_QUERY_UPGRADE_TEST=1` 使用 [243](2026-11-23-243-format-039-regression.md) 留存的 039 服务及本机 PostgreSQL 16.14 原生备份工具。运行 `TestPostgresMediaQueryUpgrade039Restore040` 耗时 38.85 秒，正常退出。039 本机上游产生 3 次原提交；040 恢复后原任务各自 GET，共 2 次，零新增上游 POST、零重叠探测，1 份无 handle 的原冻结保留 REVIEW。
