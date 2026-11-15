# 2026-11-14-079：V16-A3.2首次审核分派集成

## 目的

在持久私有交付之后增加有证据的首次审核交接，让慢审核释放下载执行资源，并在重启和确认不明时保持不重审、不提前公开。

## 范围

V16-A3.2，H03/H07/H09。核心076、共享许可077、维护及独立验收078各自登记；本项负责严格配置、固定指标、联合证据及根文档。A3.3公平退避、B持久流、C综合测量保留完整范围。

## 实际文件

开工新增docs/planning/v16a32-contract.md和本记录，更新README、Wiki/台账导航、任务源及生成双TODO/推进记录。后续实际配置/观测/集成文件在本逻辑交付前追加；不把规划文件当功能实现。

已接线`cmd/server/media_execution_config.go`及测试、`internal/studio/media_lane_observability.go`、`.env.example`、`scripts/lib/process.mjs`和`load.mjs`的第四lane/共享许可口径；等待核心字段接齐再编译。根与workbench的package.json、workbench/package-lock.json及OpenAPI版本0.19.0，新增ADR0017。修正075索引残留“实施中”为已验收，保留该批独立历史结果。

实际集成新增`internal/studio/media_review_dispatch_restore_test.go`（由许可代理按root归属实现，root实跑），修改`media_delivery_integration_test.go`的四阶段验收及`asset_storage_test.go`的版本化丢失模拟。README、AGENTS、Wiki/台账索引、tasks.json、双TODO、progress、v16a32契约和ADR0017同步；Wiki development/database-migrations/media-delivery/media-moderation/object-storage/observability/architecture及performance按当前行为或验收更新。v0.19新JSON证据归docs/wiki/assets，旧075证据保持；原始日志/私有配置留忽略的.runtime。

## 接口与数据影响

计划增加第四review执行通道、MEDIA_REVIEW_WORKERS严格1—2、与参考输入共享原审核许可；019私有READY/STARTED/FINISHED证据、精确离线栅栏和联备同步。现有公开MODERATING/PENDING足够，不计划增加凭据或私有派发接口。

## 验证结果

本批联合验收中，不沿用075成绩。原子READY/STARTED、共享许可和四通道已实现；本节保留设计、实现、实际命令、失败和修正，最终全部齐备前不勾选。

首次版本同步辅助脚本误指根package-lock.json（实际只存在apps/workbench/package-lock.json），在更新两个package.json后失败；改为已核实的前端锁路径并补齐一致版本，没有新增/删除依赖或创建虚假根锁文件。该辅助脚本失败不是产品测试结果，尚未声称v0.19构建通过。

### 中间验证与修正

- 根严格四MEDIA配置专项通过0.030s。首轮全普通9.914s失败，仅078新增memory入口触发既有“审核须PG且required鉴权”的门禁；保持生产要求，将完整SlowHTTP断言归入真实PG入口后复验，未以memory旁路宣称真实配置可用。
- 078首轮真实PG23.693s发现核心终态及取消409：STARTED时间经PG读回的Location不同，reflect.DeepEqual误判同一时间点变化。076改为Time.Equal及nullable身份检查，实际微秒变化仍拒绝；没有削弱attempt、版本或冻结身份。
- 核心三个旧B2函数/10子例8.835s、078六新函数/整组四子例15.003s分别通过。两次窗口授权通知交错，不能仅据消息断定实际进程是否重叠，未观察到写锁失败；后续所有PG仅由root启动并等待终态，最终以根串行全量为准，不靠猜测并发状态。
- 077共享许可10新增+9旧普通函数0.310s、vet通过；首次并行缺字段和HTTP阻塞reader测试等待顺序问题保留077。尚无本批全量验收。

根同步原`media_delivery_integration_test.go`：保留原全量断言并增加导入后READY/PENDING、不公开/不结算检查、第四review步骤；慢审核期间第二任务可完成导入，审核占review而非delivery。取消现在主动撤销HTTP请求，验证本地执行及时结束再释放预算，迟到上游ALLOW仍不可复活；不将远端处理未返回与本地连接已退出混淆。

根以独占PG执行上述兼容两函数/五子例，8.714s通过。另执行新增`media_review_dispatch_restore_test.go`及核心ACK/回滚三函数，16.763s通过：local READY/STARTED真实pg_dump/pg_restore、关闭生成和下载服务、原收据/UUID/key/幂等及权益保持；READY首次审核、STARTED隔离，零重POST/Query/下载。真实S3子例本次明确skip，待新服务。READY/STARTED/ALLOWED三处真实COMMIT ACK丢失，以及STARTED实际回滚/旧候选不可补造READY均通过。

078后续只读发现等待审核期间manifest-only/thumbnail变更仍可能在原件hash不变时通过现有检查；交076补完整冻结收据与实际字节验证，追加篡改/零审核外发测试。上述结果是修正前的中间证据，必须在修正后的最终全量重新覆盖。

### 完整收据与实际版本化S3

- 修正后新增五篡改子例首轮误将HTTP下载fixture设为阻塞slot0，35.776s组失败；同组升级1.51s、最终Outbox背压1.97s通过。仅将新fixture改为不阻塞的-1，保留5秒期限和所有断言，五种篡改7.46s通过：标记、时间、缩略身份、字节及缺失均REVIEW且零审核外发。
- 本机S3复用已校验4.53二进制及保留数据，新隔离bucket开启版本化，实际两次同key写入生成不同VersionId且分别读回一致；旧桶与旧证据保持。初次旧4卷容量耗尽，只扩本机上限至32卷/每卷64MB，不删旧对象；重启初始化首probe超5秒后按30秒有界probe重验通过。非生产容量/云灾备。
- 新真实S3组30.086s仅一项旧测试失败：旧无版本服务用例以普通DeleteObject模拟数据丢失，新版本化桶只增加delete marker，原冻结版本仍正确返回200。root在`asset_storage_test.go`保留原404/不得回落本地断言，增加delete marker后绑定版本仍读出原字节检查，再仅删除本测试隔离前缀自己创建的明确VersionId模拟实际丢失；单项复验0.200s通过。
- 该组其他测试全部通过：真实图/视频S3审核与provider关闭下载1.99s；018联备local/S3共8.77s，原件/缩略图2/2真实VersionId保持；019 READY/STARTED/S3联备11.55s，S3完整收据4/4真实VersionId保持；完整收据五篡改7.46s。没有恢复S3远端服务或丢弃原数据。

### 根完整check

`node .runtime/v16a32-check.mjs check`（隔离真实渠道，不注入TEST_DATABASE_URL/TEST_S3_*）通过：Go普通studio9.952s、301前端/29文件、10治理、Go vet、严格TS/Vite1.67s及Go生产构建；172文档/79台账。主JS仍495.68kB/gzip157.13kB，没有布局变化或新浏览器视觉验收。此后生产Go及019冻结，最终全PG含真实S3/pg_dump恢复由root单独运行。

原18SQL共58806字节与064及075逐文件SHA证据一致；新019为2512字节、SHA256 4b438ba07ed1d3d991d0b45582a47b7e24ef18313f3d78ca0afe36ad73787864。API0.19.0仍为71路径/83操作/95 schemas，411内部引用全部解析；没有新增公共审核重试接口。

## 风险与限制

旧候选不能补造READY，STARTED确认不明或恢复不得重审。许可获取与lease/取消需一起验证；本批不解决媒体租户公平/渠道退避，也不宣称多实例或真实审核准确率。

## 回滚方式

019一旦应用不能运行018写者，须使用匹配二进制或成套停写联备；不得删迁移、派发、候选、owner或交付收据绕过。019已在独立验收数据库实施，未对用户原开发数据库应用。

## 文档同步

根负责人维护README、Wiki与台账导航、tasks.json和双TODO，076—078拥有各自模块说明。完成条件是实现、必要验证、台账和文档同时齐备，整个V16状态仍进行中。

### 最终联合收口

`node .runtime/v16a32-check.mjs db --s3`最终全量PG通过，studio **368.160s**；全程root独占启动，真实版本化S3与TEST_POSTGRES_BIN_DIR显式启用，包含两种交付联备、019三种审核恢复及全部旧测试。当前修正后的源码/019已冻结，初次失败与中间结果全部保留。

六smoke在父环境故意设置无效四MEDIA/交付key及五CHAT、审核/邮件/权益/运营配置时通过；同构建性能bootstrap P95 20.59ms、创建取消P95 11.51ms、demo首delta P95 1761.26ms（含排队）。SHA256 02b6628b211ae1efc13e48050419dd98cb0fcada10588be43c38b6b22c2c5f41与五smoke和性能JSON一致。真实S3 PID53612于2026-11-14T16:42:18.7776749Z停止，root复核进程不存在/8端口零监听；旧新数据与配置、075证据保留。

最终[联合证据](../wiki/assets/media-review-dispatch-validation-v0.19-2026-11-15.json)、[019/API](../wiki/assets/media-review-dispatch-schema-v0.19-2026-11-15.json)、[版本化S3](../wiki/assets/media-review-dispatch-s3-v0.19-2026-11-15.json)与[性能](../wiki/assets/performance-local-v0.19-2026-11-15.json)分别留存。A3.2 check标passed，完整V16仍in_progress/completed_on=null，11/23整批不变；下一项A3.3已完成只读设计，未标实现。Go race、远程CI、本批浏览器视觉、真实渠道及生产容量均未验收。

最终文档治理：todo:sync、check:todo、10项test:todo、check:changes通过（172文档、79台账、348变更路径），git diff --check通过，仅既有LF/CRLF提示。
