# 026 · V08 独立资产目录与元数据版本

日期：2026-11-13。关联批次：**V08**；完整批次状态以根负责人核验[任务源](../planning/tasks.json)为准。

## 目的

把上传参考和任务产物登记到独立可管理的资产目录，支持项目内搜索、名称/标签、历史、软删除恢复，同时维持原内容、权限、原受理幂等与执行请求不变。

## 范围

资产目录API与数据模型，PG原子注册和元数据CAS/幂等/审计、不可变修订，回收站对新旧内容入口及编辑引用的约束，007迁移与回填。接线根实现的目录租用锁/数据库归属、027存储接口；对象存储实现由027、前端由028、离线维护由029、根联合验收由030记录。

## 实际文件

- `internal/studio/asset_library.go`：公开/私有目录模型、稳定来源身份、原子登记、内存容量、分页搜索、详情与权限查询。
- `internal/studio/asset_library_mutations.go`：metadata/trash/restore CAS与幂等、不可变历史读取、私有内容与旧asset ID授权。
- `internal/studio/asset_library_test.go`：普通HTTP与真实PG生命周期、搜索、限额、事务失败、并发、回填与格式栅栏测试。
- `internal/studio/migrations/007_asset_library.sql`：目录、修订、幂等、复合来源外键、不可变触发器、历史回填、审计扩展和storage UUID。
- `internal/studio/app.go`：AssetStorage/StorageLease初始化与关闭、目录归属检查、内存容量保留、生成结果登记。
- `internal/studio/model.go`：Artifact.StorageInfo仅服务端可见，json省略；源内容API不泄漏存储locator。
- `internal/studio/http.go`、`references.go`、`repository_queries.go`：library路由，旧下载active约束、引用选择/新编辑约束、参考上传同事务注册。
- `internal/studio/persistence.go`：任务创建/状态结果与目录同事务，保留有/无审计事件两类状态写入的正确事务语义。
- `internal/studio/app_test.go`、`admission_test.go`、`audit_test.go`、`catalog_test.go`、`drafts_test.go`、`persistence_test.go`、`provider_test.go`、`observability_test.go`、`references_test.go`：应用测试补齐独立临时存储目录，显式重启目录保留；旧006测试不误把后续最大版本当成缺006。
- `docs/planning/v08-contract.md`、`docs/wiki/assets.md`、`docs/wiki/backend.md`、`docs/wiki/references.md`及本记录；README、索引、TODO、OpenAPI由根维护。

## 接口与数据影响

新增项目scope的GET library、entry、revisions和content/thumbnail；PATCH entry；POST trash/restore。列表keyset游标绑定全部filter，q/tag按普通文本；Entry显示来源/当前元数据/provider mode，隐藏存储身份。新目录ID按reference或task+artifact派生，避免demo共享artifact ID合并不同任务。

写操作必需Idempotency-Key和expected_revision。重放优先于CAS和新值校验并返回原响应；同键异参409，版本冲突409，非法元数据422。每资产1000修订且为trash预留restore槽，回收站内须先恢复再编辑。PG事务原子写当前值、不可变修订、幂等及闭集审计，审计不含名称/标签。

007回填旧引用和产物为revision1，不伪造审计。目录注册与上传/任务状态同事务；源内容私有storage_info只保存服务端存储收据。旧reference/asset内容入口查询active目录；软删除不改已受理任务的原引用/请求文件/handle。应用同时拿目录OS锁和PG单writer锁，并验证storage UUID归属，防止另一数据库误维护同一目录。

## 验证结果

- 普通Library4组首轮通过0.212秒；补恢复预留后5组通过，覆盖CAS/原键重放、完整历史、回收恢复、字面量搜索、同时间cursor、独立demo来源与旧下载权限。
- 初次007真实迁移暴露旧tasks表没有created_at实体列，改为读取document创建时间并以updated_at兜底；同时处理artifacts JSON null。旧库种子补齐input/handle/next_poll必需列，没有修改已交付001。
- 新增目录注册失败注入进一步发现错误复用“必须有audit事件”的事务助手会拦截正常轮询保存；已改为任务自身事务，产物、目录和可选审计一致提交，不放宽原审计助手约束。
- `go test ./internal/studio -run '^TestLibrary|^TestPostgresLibrary|^TestPostgresStorageMaintenance' -count=1 -v -timeout=90s`：全部通过4.054秒，含4个PG目录测试及子例和根维护专项。两个独立PG连接验证CAS冲突与同键收敛，故障注入验证source/catalog与审计事务回滚。
- 首轮真实PG全量 `go test ./... -count=1 -timeout=180s`：通过32.270秒；该轮在新增storage UUID绑定前编译，后续归属接线需独立复验。`go vet ./...`首轮通过。
- 接入storage UUID后首轮仅旧库fixture使用非法blob_old失败，可达性检查正确拒绝；修正测试为合法64位摘要ID，保留安全检查不绕过。针对归属/007回填/内存恢复预留专项通过2.009秒。
- 最终真实PG全量 `go test ./... -count=1 -timeout=180s`：通过32.302秒，当前127个studio测试函数和2个cmd测试函数及子例。目录模块10函数含4个PG函数及故障子例；最后源码`go vet ./...`通过。文档检查的5项待完成均是根维护的026—030索引覆盖，模块链接/八章节有效，由030整体验收收口。
- 存储、CLI、前端和联合构建的最终证据由027—030记录；本记录不把未完成的公网/生产容量或真实供应商验收标为已通过。

## 风险与限制

仍为单写PG与串行连接，metadata事务会锁工作区行保护跨资产幂等。PG列表独立于任务缓存，但名称包含检索尚无全文索引/大库压测。memory限制4096资产、16384修订，预留恢复空间，重启不保留目录。回收站不等于释放磁盘/对象费用，没有永久删除或二进制内容版本。

生成或上传文件先落存储，DB失败可留下孤立文件；只有离线可达性维护能按约定处理。trash、任务引用和原始请求正文必须保留。旧单artifactID接口可由同作用域另一个active条目授权，新entryURL严格独立。固定公共demo SVG不构成私有作品。目录权限和S3收据不能代替生产身份、对象存储灾备及公开运营放行。

## 回滚方式

升级前联合备份数据库、原参考/请求文件、产物目录及对象存储/收据。007后旧程序拒绝新格式，禁止删除迁移登记或owner marker强行降级。回退在独立数据库/目录恢复匹配版本备份，保留当前资产历史和不可变内容，不删除尚在回收站或任务中的文件。停止应用后再执行维护，目录锁和PG锁不能手动绕过。

## 文档同步

[资产Wiki](../wiki/assets.md)和[冻结接口](../planning/v08-contract.md)与本台账同步。根统一维护README、Wiki/台账索引、OpenAPI、V08任务与TODO及030联合验收；存储/前端/维护台账为027—029，既有022—025保留V07当时的验证与外部待条件事实。
