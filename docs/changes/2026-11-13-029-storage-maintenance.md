# 029 · V08 离线存储维护、目录锁与归属

日期：2026-11-13。关联批次：**V08**。代码由根集成人实现，本文档在只读核对实现、CLI和测试后补齐；整批状态由[任务源](../planning/tasks.json)及030联合验收确认。

## 目的

安全处理文件先发布、数据库事务未提交留下的本地孤立文件。维护不能把回收站、终态任务或另一个数据库的私有文件当成垃圾，也不能与应用同时写入目录。

## 范围

私有目录跨进程OS租用锁、数据库UUID与目录归属标记、首次安全绑定、离线可达性扫描、默认dry-run计划、摘要复核后的显式apply及失败计数。本批不删除数据库记录、有效资产或远端S3对象；不是在线自动GC或永久删除功能。

## 实际文件

- `internal/studio/storage_lease.go`：解析目录/已有符号链接、排序去重、OS文件锁、跨进程互斥、部分获取失败释放和幂等Close。
- `internal/studio/storage_ownership.go`：PG storage UUID读取、目录角色标记、首次可达性覆盖核对、错库拒绝、memory写者拒绝持久目录、维护必须已有正确归属。
- `internal/studio/storage_maintenance.go`：001—007版本核对、双锁、全部引用可达性、严格文件名/类型/年龄/大小检查、计划摘要、删除前二次核验和部分完成报告。
- `internal/studio/storage_maintenance_test.go`：实际子进程目录锁、有限文件扫描、真实PG可达集合、错误数据库/目录、dry-run、计划变化及临时目录apply验证。
- `cmd/maintenance/main.go`：独立离线CLI，环境变量与参数、两分钟context、JSON报告和失败退出码；不自动加载.env、不启动应用worker、不迁移数据库。
- `internal/studio/migrations/007_asset_library.sql`、`internal/studio/app.go`：由026集成storage UUID和应用目录锁/归属校验；不另改历史迁移。
- `docs/wiki/storage-maintenance.md`、`docs/wiki/decisions/0005-private-library-lifecycle.md`、`docs/wiki/development.md`、`docs/wiki/architecture.md`及本记录：操作顺序、决定、实现边界和互链。依赖锁文件由027统一维护，根维护README/索引/任务源。

## 接口与数据影响

维护只读取数据库业务数据，要求恰好001—007迁移；不自动升级、不提供浏览器删除接口。应用与维护使用同一目录OS锁和PG session advisory writer锁。007新增唯一storage UUID，两个目录分别以references/assets角色保存`.studio-storage-owner.json`；memory不允许写已有PG标记的目录。锁文件`.studio-writer.lock`保留在磁盘，能否运行由OS锁状态决定。

可达集合读取**所有**reference原图/缩略图、**所有**task的BodyFileID和artifact IDs，不按终态或trash过滤。维护核对对应本地文件及产物收据存在，缺失/异常即拒绝。S3已迁移文件仍保留本地原件和收据，维护不依据远端列举、过期URL或“任务已完成”推断删除。

默认仅报告。apply要求经过检查的64位plan_digest；重新扫描/计算摘要，并在首次删除前逐个重读候选的文件身份、长度、mtime和SHA256，随后检查数据库连接。摘要包含目录、schema、年龄条件、可达集合与候选文件，不是可绕过权限或归属的令牌。删除发生在os.Root内，只移除已核对的直接文件；不递归删除目录。

## 验证结果

- 根实现阶段 `TestStorageLeaseExclusiveAndReleasesPartialAcquisition` 和 `TestMaintenanceScanAndExactPlanFiles` 普通专项通过0.253秒（根报告）；测试包含真实子进程拒绝已持有的目录锁、部分锁释放、锁文件保留、受保护/近期/未知文件区分及取消hash。
- 本模块集成真实PG专项先后包含于 `go test ./internal/studio -run '^TestLibrary|^TestPostgresLibrary|^TestPostgresStorageMaintenance' -count=1 -v -timeout=90s`，通过4.054秒；维护部分1.05秒。加入owner UUID后专项 `^TestLibraryMemoryGlobalLimit|^TestPostgresLibrarySeven|^TestPostgresStorage` 通过2.009秒，维护部分1.47秒。
- 当前代码曾在子代理实际执行的真实PG全量 `go test ./... -count=1 -timeout=180s` 中通过32.302秒，包含127个studio与2个cmd测试函数及子例；`go vet ./...`通过。根正在进行独立最终集成复验，其结果由030记录，不在本台账提前宣称完成。
- 真实PG维护测试证明：活跃DB writer/目录writer拒绝；不同storage UUID拒绝；空库不能接管带已发布内容的目录；dry-run不删除；错误digest和审阅后内容变化拒绝；正确新计划只删除临时测试目录中一个旧孤立文件，并保留原图、缩略图、终态任务正文及产物/收据。
- 符号链接负例在当前Windows环境因创建权限不足未执行，测试明确记录该限制。24小时最小年龄、100000枚举/可达上界、256MiB单候选上限、CLI两分钟截止均由代码限制；没有宣称100000文件或256MiB边界的容量压测。
- 本次文档子任务未执行用户数据apply、远端删除、生产恢复或真实收费请求。测试删除只针对自身临时fixture。
- 文档子任务运行 `node scripts/check-docs.mjs`：新增页面及互链通过结构/路径检查；全仓尚有5个根README/Wiki索引指向的浏览器截图/验收JSON待根集成生成，由030收口，未把此轮检查记为全仓通过。

## 风险与限制

只支持离线、单写服务的本地已知文件回收。默认最低年龄24小时，可设24小时至3650天；总枚举和可达集合分别最多100000项，单候选最多256MiB，超过预算或读库/文件错误则拒绝。未知文件名、目录和非普通文件不会成为候选；已引用文件即使很旧也保留。

apply不是文件系统事务：首次删除前检查全部候选，但开始后取消或IO失败可留下部分完成；JSON报告记录removed_files/removed_bytes，不能据错误自动重放旧计划。外部不遵守OS锁的管理员、文件系统故障和异常链接攻击不属于已验证的安全运行模型。没有远端S3孤立对象删除、用户永久删除、自动保留策略或生产RPO/RTO演练。

首次绑定只允许空目录或数据库可达集合覆盖全部已发布文件。未知已发布文件拒绝绑定，须先备份并人工核对来源/恢复配套元数据，不能删除owner marker、锁文件或迁移记录强行运行。memory没有持久可达集合，不能用于授权回收。

## 回滚方式

维护前停应用并联合备份数据库、REFERENCE_DIR、ASSET_DIR及远端对象/本地收据；保持007 storage UUID和两个目录角色标记成套。dry-run本身不改业务/删除内容，归属首次绑定由正常应用启动完成，维护不会自动接管。

apply已删除文件不能通过资产restore按钮找回；恢复需依靠同一时间点的私有文件备份。若中途失败，保留报告和未完成现场，恢复后重新dry-run并核对新digest。007后回退程序必须使用匹配旧版本的独立数据库和私有目录备份，不删除版本登记或归属标记绕过拒绝；原升级库及新作品继续保留。

## 文档同步

[离线维护Wiki](../wiki/storage-maintenance.md)、[ADR0005](../wiki/decisions/0005-private-library-lifecycle.md)、[开发](../wiki/development.md)、[架构](../wiki/architecture.md)与本台账已同步。[资产库](../wiki/assets.md)及[对象存储](../wiki/object-storage.md)说明上层和内容后端，026/027记录相应代码。根负责README、Wiki/台账索引、任务源/TODO及030独立联合验收，文档子任务不修改这些文件。

