# 022 · V07 私有参考素材、权限与编辑受理

日期：2026-11-13。关联批次：**V07**；整批最终状态由集成人按[任务源](../planning/tasks.json)核验。

## 目的

交付可复用的私有参考图片和项目内图片编辑，保护真实内容、所属关系、幂等与原执行身份，使大型编辑请求不进入JSONB或长期占用全局业务锁。

## 范围

上传/列表/原图/缩略图API，真实图片与容量边界接线，工作区和项目ACL，上传与编辑幂等，逐模型编辑能力，受理前不可变请求准备，引用外键与事务审计，数据库迁移、重启恢复及本地测试。底层ReferenceStore由集成人维护，适配器由023记录，前端由024记录；整体浏览器/构建/冒烟由025记录。本批未提交真实付费生成。

## 实际文件

- `internal/studio/references.go`：私有参考模型、上传/列表/读取、语义幂等、配额、PG事务、参考归属解析和无全局锁的编辑准备。
- `internal/studio/references_test.go`：13个测试函数及子例，含真实PG账户/项目/重启、审计回滚、外键、版本栅栏、multipart文件恢复和停机回归。
- `internal/studio/app.go`：ReferenceStore生命周期、私有目录隔离、BodyStore注入、双阶段编辑受理与并发复检；停机取消媒体Query不误写FAILED。
- `internal/studio/model.go`：可选operation/reference_asset_ids、任务快照及深拷贝。
- `internal/studio/catalog.go`、`catalog_test.go`：图片编辑能力及数量、配置校验、旧模型数据升级栅栏专项兼容006。
- `internal/studio/http.go`：参考路由、bootstrap.reference_limits、编辑准备沿请求context执行，忙错误Retry-After。
- `internal/studio/persistence.go`、`internal/studio/migrations/006_references.sql`：参考元数据/幂等/有序任务绑定、复合归属外键、审计动作扩展及格式栅栏。
- `internal/studio/app_test.go`：通用测试应用的ReferenceDir隔离到临时目录。
- `cmd/server/main.go`、`.env.example`、`config/models.example.json`：REFERENCE_DIR和显式编辑能力配置。
- `docs/wiki/references.md`、`docs/wiki/backend.md`、`docs/wiki/models.md`及本台账：接口、能力、存储/恢复与实际边界；根负责README/索引/任务源/OpenAPI。

## 接口与数据影响

新增GET/POST `/api/v1/references`、GET/HEAD `/{id}/content`和`/{id}/thumbnail`。上传单个multipart file，201，成功重放同样201并带Idempotency-Replayed；hash包括项目/规范化文件名/真实字节摘要/长度，不包括浏览器随机boundary。所有读取和编辑引用都校验真实会话所属项目/工作区，URL携带project_id供img标签使用。

bootstrap返回reference_limits；模型capabilities增加image_edit_supported/max_reference_images，image开启时1—4，否则0。TaskInput的operation省略为generate，edit仅允许image且至少1张有序、不重复、同项目引用。新可选字段omitempty保持旧普通生成hash，Task保存operation/引用ID和模型/参数历史快照。

上传元数据、幂等和reference.uploaded审计同事务；任务受理、引用绑定、幂等和task.accepted审计同事务。审计不含文件名、路径、摘要、图像、prompt或凭据。006复合外键强制task/reference/project/workspace一致；旧二进制遇到006拒绝启动。图片和编辑multipart正文先写不可变文件，数据库只记录文件身份、摘要、长度，不能宣称跨介质原子事务。

编辑准备释放app.mu且不持有PG连接锁，完成后重新检查幂等、引用归属、当前占用及缓存限额。相同键并发准备最终只受理一次；已完成准备但未采用的文件计入目录硬限，暂不回收。停机或writer失联取消外部请求后，失败返回不再覆盖原持久状态；已知handle保留查询资格，无handle的SUBMITTING恢复UNKNOWN。

## 验证结果

- 真实PG专项 `go test ./internal/studio -run 'TestPostgresReference|TestPostgresCatalogFormatFence' -count=1 -v`：6函数及子例通过，3.796秒；覆盖004→006和005→006升级、旧loader拒绝、跨账户/项目、上传和任务审计回滚、复合外键、两次重启后保持multipart原字节/键及只查原handle。
- 首轮真实PG全量 `go test ./... -count=1`：通过，studio包55.892秒。PG窗口与其他代理串行，使用独立TEST_DATABASE_URL，没有输出连接凭据。
- `go test ./internal/studio -run 'TestCatalogStrictConfigurationAndStableVersion|TestReference' -count=1`：通过，8.229秒；包含真实HTTP上传/缩略图/Range、随机boundary幂等、满容量重放、跨项目编辑拒绝、全局锁释放、同键并发收敛、能力配置负例、V06固定JSON字面量hash与私有目录嵌套拒绝。
- 根联合复测发现既有恢复用例偶发失败。确定性阻塞Query+App.Close测试在修复前复现真实FAILED误落库（0.42秒）；修复后 `go test ./internal/studio -run 'TestPostgresReferenceShutdown|TestPostgresPersistenceAuthIsolationAndRecovery' -count=10 -timeout=120s` 全部通过，22.882秒。没有通过延长既有断言等待来掩盖失败。
- `go vet ./...`：停机修复后通过。新回归首次调试遇终态缓存未载入造成fixture自身nil解引用/清理锁阻塞，已改用getTaskLocked并在断言前释放锁；随后获得上述确定性失败和修复证据。
- 修复后真实PG全量 `go test ./... -count=1 -timeout=180s`：通过，studio包24.947秒；当前105个studio测试函数加1个cmd测试函数及子例。`node scripts/check-docs.mjs`：51份文档、25份台账通过，链接与必需章节有效。
- 底层真实格式/512缩略图/2GiB硬限、provider25专项及完整HTTP编辑链由023/025记录；不是供应商真实收费验收。Windows race环境限制沿用已有记录。

## 风险与限制

本地单写PG与文件存储，非分布式容量控制。8MiB、16M像素、最大边8192、最多4参考、100条/128MiB工作区限额是本平台政策；工作区字节包含每条原图和缩略图逻辑字节。ReferenceDir全局2GiB/4096文件包括孤立文件；不允许外部writer往该目录写入。上传/编辑准备分别有界4/2，但尚未做生产并发与长期容量验收。

没有删除/GC、对象存储、mask或视频编辑，孤立文件暂保留并计入硬限。memory模式仅元数据内存持有，重启不能找回引用关系，文件仍在磁盘。关闭浏览器上传不是后端撤销；受理不明保留原File/body/key。demo编辑是模拟结果。真实模型效果、素材限制、计费仍是V07/V15的待验收条件，不能把本地fixture通过当公网收费放行。

## 回滚方式

升级前联合备份数据库、REFERENCE_DIR和ASSET_DIR。006应用后旧程序必须拒绝启动，不能删除迁移登记绕过。需要回退时在独立数据库和独立文件目录恢复匹配旧版本备份，保留当前原库、新引用与不可变正文；不要删除尚未核实的任务或编辑文件。暂停新编辑能力可以通过目录重启生效，原handle仍保留安全查询路径。

## 文档同步

[参考素材Wiki](../wiki/references.md)、[后端Wiki](../wiki/backend.md)、[模型Wiki](../wiki/models.md)、配置示例与本台账已同步。根维护README、Wiki/台账索引、V07任务/TODO、OpenAPI及[025联合验收](2026-11-13-025-reference-integration.md)；相关provider/前端记录为023/024。既有018等历史验收记录保留原事实，不回写覆盖。
