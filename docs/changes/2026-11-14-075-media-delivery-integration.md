# 2026-11-14-075：V16-A3.1持久私有交付集成

## 目的

让慢下载及当前B2审核占用独立交付通道，状态查询能够继续推进；冻结一次结果集合并保存逐件完整收据，使部分下载和重启恢复不重新生成或混批。

## 范围

V16-A3.1/H03/H07/H09。072核心/018、073网关与收据、074维护与离线栅栏分别记录；本项负责严格配置、固定观测、独立联合验收和根文档。A3.2首次审核分派、A3.3渠道公平退避、B/C及其他产品范围不删减。

## 实际文件

- `cmd/server/media_execution_config.go`、对应测试及`main.go`：严格第三通道范围和私有密钥接线；`internal/studio/media_lane_observability.go`：九个固定序列。
- `internal/studio/media_delivery_integration_test.go`：真实App/HTTP/PG、慢下载及慢审核、恢复/篡改、图片/视频/PARTIAL/BLOCK、权益和真实本机S3；`media_delivery_restore_integration_test.go`：实际pg_dump/pg_restore、local与保留远端对象两种恢复。
- `internal/studio/durable_process_test.go`、`references_test.go`只补测试交付密钥；`asset_storage_library_test.go`保留旧断言并明确三阶段执行。
- `.env.example`、`scripts/lib/process.mjs`、`scripts/load.mjs`：配置、渠道隔离及性能报告口径。根及workbench的`package.json`、`package-lock.json`、`docs/api/openapi.yaml`升0.18.0；没有新增依赖或公共API字段。
- `README.md`、`AGENTS.md`、`docs/changes/README.md`、`docs/wiki/README.md`、`docs/planning/tasks.json`和生成双TODO；规划`v16-contract.md`、`v16a31-contract.md`、`progress.md`；新增ADR0016。
- Wiki的`architecture.md`、`development.md`、`database-migrations.md`、`media-execution-lanes.md`、`media-transport-lanes.md`、`media-gateway.md`、`object-storage.md`、`observability.md`、`performance.md`同步当前三通道、018、配置/联备与测量边界。模块新增`media-delivery.md`、`media-delivery-provider.md`和维护页分别归072—074。
- `docs/wiki/assets/`下新增v0.18的五份smoke、performance、media-delivery-schema/s3/validation JSON。忽略目录`.runtime/`保留实际日志和隔离测试wrappers，凭据只在本地服务端配置，不纳入证据文件。

## 接口与数据影响

新增MEDIA_DELIVERY_WORKERS（默认2，1—16）与独立MEDIA_DELIVERY_KEY（32字节标准base64）。原生异步媒体采用第三通道，PG必须有原密钥；memory不保证重启持久。固定指标增加delivery值，不新增私有身份标签。018私有表和精确离线格式栅栏由072/074实现，无公共签名URL或密文接口。

## 验证结果

本记录保存本批实际验证、失败和修正；不沿用071结果标本批通过。最终收口结果追加如下，模块专项见072—074。

### 首轮实现与验证记录

- root严格MEDIA配置及真实memory/HTTP阶段专项通过：首次cmd0.034s、studio0.118s；补固定delivery指标后cmd0.042s、studio0.113s。
- 真实Async+App的重启/原清单/收据复用、同长篡改不重下、慢审核取消与查询隔离，两PG函数/三子例5.439s通过。
- 图片、多图、视频、PARTIAL和BLOCK整组权益验证及实际pg_dump/pg_restore，两PG函数/五子例10.743s通过：恢复新DB+私有目录+独立key，错key拒绝，稳定UUID与完成收据保留，无重复POST/Query/已完成下载。不是原开发库恢复或生产RPO/RTO。
- 首次根check失败于新增PG测试被skip后的Windows清理：helper在检查TEST_DATABASE_URL前已打开私有os.Root，Skip导致其句柄未交App关闭，TempDir删除失败。改为先检查PG条件再建provider，并为每个测试provider补独立Cleanup；未删断言、不改产品门禁。Go普通首轮22.147s失败记录保留，随后完整check复验。
- 子模块并行接线时首次编译缺少尚未落地的字段/方法，最终接齐后专项通过；072/073/074初次docs仅缺根索引，统一补齐后复验。详见各台账。
- 修正测试句柄后根check通过：Go普通9.666s、301前端/29文件、10治理、Go vet/TS/Vite/Go构建（Vite1.71s），163文档/75台账。后续仅增适用验证与文档，不改已验证产品门禁。
- 独立新SeaweedFS本机服务启用后，首轮真实S3组1.899s失败于旧library用例仅执行Submit/Query两步；原生Query现在正确停在INGESTING且不公开产物。保留原全部生命周期断言，补“清单后无公开产物”断言与单独Delivery步，最终三真实服务函数1.965s通过；新增PG图/视频审核、完整收据与关闭provider后的下载均通过。
- 旧S3 env指向的目录和进程已不存在，未把它当运行证据；下载官方4.53到全新测试目录，官方zip SHA256与实测一致，root另复核binary hash、真实PID及8个loopback监听。初次合并环境准备/启动命令被自动审批拒绝（仅blocked by policy，未执行）；改为独立准备和最小隐藏启动成功，没有删除环境或旧文件。此服务是本机兼容验收，不代表云S3或生产灾备。
- 首轮完整PG289.854s失败：三个实际进程崩溃用例共用的durableProcessConfig，以及旧图片编辑重启用例，尚未配置新增的独立交付密钥，均在启动门禁被拒。仅给测试配置补同一固定测试密钥，保留实际进程Kill、幂等、原handle和正文断言。四项专项9.730s全部通过，随后重跑完整PG；未以skip或放宽门禁消除失败。
- 真实017→018专项1.364s通过；原17份SQL总55486字节与064逐文件SHA256完全一致，新018为3320字节、SHA256 e3899b65a7a66b3ac1e3b136a40eabc08dce550b675fd251bec7dda976143294。API0.18.0保持71路径/83操作/95 schemas，411内部引用全部解析。
- 六套smoke在父环境故意设置无效三lane配置/交付密钥及真实渠道配置时全部通过，子进程显式隔离。当前构建SHA与五份smoke JSON一致，性能回归待最终PG退出后执行；不能把create+cancel当交付吞吐。

### 最终联合复验

- `node .runtime/v16a31-check.mjs target '^TestPostgresMediaDeliveryPrivateBackupRestore$|^TestPostgresMediaDeliveryUpgradeSeventeenPreservesLegacyEvidence$'`：19.417s通过，local联备14.31s、S3保留远端对象联备3.66s、升级1.40s。恢复前后首件完整PG收据逐字段相同，原件/缩略图完整读回匹配；错key拒绝。S3本次未返回VersionId（0/2），不声称版本化服务已验，且没有备份/恢复远端S3服务本身。
- `node .runtime/v16a31-check.mjs db`等价真实隔离`npm run test:db`：最终全量通过，studio **301.345s**；显式启用本机S3、PG工具和独占数据库窗口，包含新增升级、两种恢复及所有旧回归。首轮289.854s失败保留于上文，不删测试。
- `go vet ./...`：最终新增/修正测试全部落地后通过。`git diff --check`通过。原生产源码在完整check后未再修改；随后追加测试均在最终全PG中执行，未把之前的编译当作新测试通过。

## 风险与限制

单控制写者和私有目录排他锁保持。交付仍包含审核，首次审核独立分派待A3.2；真实供应商、审核商和生产容量仍另验。签名URL与密钥不得进入公开JSON、台账或日志，原URL失效不能靠重Query猜测相同产物。

## 回滚方式

018涉及新持久状态，升级前须停写并联备PG/私有目录及独立密钥。代码回退不能删018登记、owner或候选；旧程序写者必须被栅栏拒绝。只在隔离环境验证成套备份恢复，禁止覆盖用户原开发数据来制造通过。

## 文档同步

README最近变更、Wiki契约导航、tasks.json与两份TODO跟随真实状态。A3.1仅在完整实现和验证后标passed；072—075新增记录保留071历史验收与限制，持续推进记录列出完成、当前、剩余、阻塞和下一步。

### 联合收口补充

同构建性能回归通过：bootstrap P95 20.28ms/2482.04rps、创建取消P95 10.76ms、demo首delta P95 1761.79ms（包含同workspace排队）。测试PG结束、独立S3停止后测量，仍非独占机器/生产容量。二进制SHA256为16be6a7150c294b41a40bbe4a4257dc164e94232d5a5d371e3dde616543a9935，与五smoke及性能报告一致。

S3停止前核验独立exe路径及进程创建时间，2026-11-14T15:45:13.7287908Z停止；root复核PID不存在及八端口零监听，数据/配置保留。初次停止防护因JSON时间被解析为DateTime而字符串比较不一致，拒绝执行；改为核对同一UTC ticks后才停止，未误停其他服务。

最终证据见[联合JSON](../wiki/assets/media-delivery-validation-v0.18-2026-11-14.json)、[018/API校验](../wiki/assets/media-delivery-schema-v0.18-2026-11-14.json)、[实际本机S3](../wiki/assets/media-delivery-s3-v0.18-2026-11-14.json)及[性能](../wiki/assets/performance-local-v0.18-2026-11-14.json)。根docs变更检查163文档/75台账、315路径通过，TODO依赖证据有效；最终任务勾选后再执行生成器/治理复验。

A3.1本地check标passed，完整V16仍in_progress、completed_on仍null，11/23整批完成不变；下一项A3.2已完成只读设计，不能标实现。无Go race（本机无GCC/Clang）、本批浏览器视觉、远程CI、真实渠道或公网放行。

勾选后最终`npm run todo:sync`、`npm run check:todo`、`npm run test:todo`（10/10）、`npm run check:changes`（163文档/75台账/315路径）及`git diff --check`均通过。后续A3.2建立新台账，075保留本次已交付事实，不把后续源码成绩反写成v0.18验收。
