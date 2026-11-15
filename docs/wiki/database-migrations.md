# SQL 迁移目录与嵌入加载

数据库SQL统一保存在[db/migrations](../../db/migrations)，当前格式为001—020共20份，020已按083本地联合验收；历史019验收见[079](../changes/2026-11-14-079-media-review-dispatch-integration.md)。064曾将001—017从旧`internal/studio/migrations`按相同字节移入；A3.1新增018私有交付，A3.2新增019首次审核派发，不修改历史SQL。020新增调度投影与渠道运行态，实施与验证见[080](../changes/2026-11-15-080-media-scheduler.md)及[083](../changes/2026-11-15-083-media-scheduler-integration.md)。

## 目录与编译

| 位置 | 作用 |
| --- | --- |
| [db/migrations.go](../../db/migrations.go) | `db` 包通过 `//go:embed migrations/*.sql` 导出 `Migrations embed.FS`。 |
| [db/migrations](../../db/migrations) | 唯一的独立 SQL 文件目录；数字前缀保留既有执行顺序。 |
| [internal/studio/persistence.go](../../internal/studio/persistence.go) | `migrationFiles = db.Migrations`，继续使用原迁移加载器。 |

嵌入文件系统内的名称仍为 `migrations/001_core.sql` 等，未变成 `db/migrations/...`。现有 `fs.Glob`、`ReadFile` 和旧版本升级测试继续使用原逻辑路径。SQL 在构建时进入二进制，运行时不从当前目录读取 SQL；部署已构建程序无需另找旧路径。更改或新增 SQL 后必须重新构建。

## 执行与版本保护

启用 PostgreSQL 时，应用先取得原有单写者 advisory lock，再在启动事务内调用原 `applyMigrationsFrom`。文件按名称排序，检查版本前缀为正整数且不重复；已登记版本跳过，未登记脚本和版本登记在同一事务执行。数据库含当前程序不认识的已应用版本时拒绝启动。

064目录搬迁当时没有改变017格式；018新增加密交付描述和有序计划/完整收据，019增加私有首次审核派发。当前开发维护/账本/角色与PG资产迁移入口要求精确001—020，原019工具不得作用于020。迁移台账SHA是内容校验证据，**不是运行时脚本摘要栅栏**；studio_schema_versions仍只存版本与时间，不宣称加载器能发现既有脚本被改写。

018升级前须停写并联备PG、两类私有目录/owner、S3对象版本及已启用队列/交付密钥。原生phased网关缺少独立MEDIA_DELIVERY_KEY时，在启动迁移前拒绝；已有交付必须保留原key。旧017写者遇018必须拒绝，不能删除登记绕过。真实独立库dump/restore及故障证据见[075](../changes/2026-11-14-075-media-delivery-integration.md)，不代表原开发现场恢复。

019沿用全部联备要求，不回填旧候选READY。只有新候选和READY同事务提交才可首次审核；STARTED已确认或确认不明恢复时保持REVIEW。旧018写者及工具须拒绝019，不能删派发记录、候选、迁移或owner降级。新格式的真实升级/联备验收独立记录于079，不沿用018成绩。

## 开发与验证

新增 SQL 应放入此目录，保持唯一版本前缀，并在涉及数据格式变化时同步离线维护工具的版本栅栏、迁移测试和升级说明。不要将测试用旧版本 `fstest.MapFS` 当成第二套真实 SQL 来源：测试从同一嵌入文件系统筛选历史脚本。

本次沿用模型、参考、资产库、账户四组真实 PostgreSQL 升级与旧写者拒绝测试，另外编译所有 Go 包并执行 `go vet ./...`。具体执行结果见 [064 台账](../changes/2026-11-14-064-sql-layout.md)，其中数据库环境未就绪的失败也保留记录。当前平台仍是单控制写者，本目录调整不构成多实例迁移协调或生产灾备验收。

## 回退与联备

仅撤销本次源码目录调整时，可以把完全相同的 SQL 移回旧源码位置并恢复旧的嵌入声明，再重新构建；无需对数据库执行降级。它不授权回退到不认识 017 的旧业务二进制，也不能删迁移登记或私有目录 owner 标记绕过格式保护。

实际升级、数据恢复与旧版本业务回退仍需同时考虑数据库、私有资产/参考目录及已启用邮件队列密钥，见 [账户持久化](persistence-auth.md)、[私有存储维护](storage-maintenance.md) 和 [生成限制](generation-controls.md)。开发与交付约定见 [开发文档](development.md)。

020新增四张调度表，集合回填后建索引，保留001—019原字节；启动迁移独立60秒超时，日常5秒事务不变。集成人实际019→020升级含10万活跃与10万历史任务，迁移11.2288314秒、59张原表摘要不变；完整App恢复仍另有规模限制，此处不是20万活跃任务重启验收。新冷却、截止时间、游标和探测事实随PG备份恢复，不能删表清退避；验收及首次5秒超时修正见[083](../changes/2026-11-15-083-media-scheduler-integration.md)。

