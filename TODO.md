# 开发 TODO · 按模块横切、按业务纵切

更新时间：2026-11-14。已验收 **11/23 个批次**。这是批次数量，不是工时或整体产品完成百分比；各批工作量不同。

本文件由 [docs/planning/tasks.json](docs/planning/tasks.json) 生成，请修改唯一任务源后运行 `npm run todo:sync`。状态与勾选不能脱离验收证据；规则见 [AGENTS.md](AGENTS.md) 和 [任务维护说明](docs/planning/README.md)。

**横切**表示长期负责的技术/产品模块，**纵切**表示用户能实际完成的一条业务链及其完整验收。横向模块包含后续批次时，不能因某个基础能力完成就把整个模块打勾。

## 横向模块

| 模块 | 职责 | 已验收/相关批次 | 批次 |
| --- | --- | --- | --- |
| H01 身份与权限 | 账户、会话、组织权限、身份恢复 | 8/15 | V01、V03、V04、V05、V07、V08、V09、V10、V11、V12、V13、V14、V18、V21、V22 |
| H02 项目与创作数据 | 项目、草稿、版本、协作数据 | 6/9 | V01、V03、V04、V08、V10、V12、V19、V20、V21 |
| H03 模型与执行 | LLM、图片/视频协议、参数能力 | 7/15 | V00、V01、V05、V06、V07、V09、V10、V12、V14、V15、V16、V17、V19、V20、V22 |
| H04 资产与交付 | 上传、私有存储、下载、生命周期 | 2/8 | V01、V07、V08、V14、V15、V18、V20、V22 |
| H05 账务与支付 | 报价、权益账本、冻结结算、订单退款 | 1/4 | V12、V13、V14、V22 |
| H06 审核与运营 | 审计、治理、风控、运营控制台 | 5/9 | V04、V08、V09、V10、V11、V12、V13、V14、V22 |
| H07 可靠性与性能 | Outbox、租约、配额、可观测、容量与灾备 | 7/15 | V01、V05、V06、V07、V08、V09、V10、V11、V12、V14、V15、V16、V17、V18、V22 |
| H08 前端体验 | 工作台、移动布局、可访问性、性能 | 10/19 | V00、V01、V03、V04、V05、V06、V07、V08、V09、V10、V11、V12、V13、V14、V17、V19、V20、V21、V22 |
| H09 工程与交付治理 | TODO、文档、测试、版本、部署 | 11/16 | V00、V01、V02、V03、V04、V05、V06、V07、V08、V09、V10、V11、V12、V14、V18、V22 |
| H10 工作流与专项创作 | DAG、模板、分镜、漫剧、演示文稿 | 0/3 | V19、V20、V21 |

## 纵向批次队列

- [x] **V00 工程与演示工作台基线** — 已验收；依赖：无；完成：2026-11-13
- [x] **V01 账户、持久任务与实际媒体协议** — 已验收；依赖：V00；完成：2026-11-13
- [x] **V02 横向模块与纵向批次治理** — 已验收；依赖：V01；完成：2026-11-13
- [x] **V03 项目云端草稿与版本恢复** — 已验收；依赖：V01、V02、V04；完成：2026-11-13
- [x] **V04 关键业务审计与活动记录** — 已验收；依赖：V01、V02；完成：2026-11-13
- [x] **V05 可观测与资源准入** — 已验收；依赖：V04；完成：2026-11-13
- [x] **V06 模型目录与参数能力** — 已验收；依赖：V01；完成：2026-11-13
- [ ] **V07 参考素材上传与图片编辑** — 待外部条件；依赖：V06
- [x] **V08 资产库与对象存储** — 已验收；依赖：V01；完成：2026-11-13
- [x] **V09 持久事件与任务租约隔离** — 已验收；依赖：V04、V05、V08；完成：2026-11-13
- [x] **V10 多轮对话与上下文管理** — 已验收；依赖：V01、V06、V09；完成：2026-11-13
- [ ] **V11 账户安全与自助恢复** — 待外部条件；依赖：V04
- [x] **V12 权益账本与任务结算** — 已验收；依赖：V04、V06；完成：2026-11-14
- [ ] **V13 订单、支付与退款** — 待开始；依赖：V11、V12
- [ ] **V14 审核、风控与运营控制台** — 进行中；依赖：V04、V11、V12
- [ ] **V15 真实网关与产物交付验收** — 待外部条件；依赖：V01、V06
- [ ] **V16 分布式流与配额公平调度** — 待开始；依赖：V05、V09
- [ ] **V17 性能与长稳态放行** — 待开始；依赖：V15、V16
- [ ] **V18 部署、备份与灾备演练** — 待开始；依赖：V09、V11
- [ ] **V19 可执行工作流与模板** — 待开始；依赖：V03、V06、V09
- [ ] **V20 视频编辑与分镜创作** — 待开始；依赖：V07、V08、V19
- [ ] **V21 协作与专项创作扩展** — 待开始；依赖：V08、V11、V19
- [ ] **V22 公开收费发布验收** — 待开始；依赖：V07、V08、V10、V13、V14、V15、V17、V18

## 批次范围与验收

### V00 工程与演示工作台基线

状态：**已验收**。模块：H03、H08、H09。依赖：无。

可运行的 Go/React 演示链路与文档治理。

验收条件：

- 可运行工作台与SSE/媒体任务演示
- 提交幂等、取消和来源保护验证
- README/Wiki/台账及治理检查

验证记录：

- 通过：v0.1历史验收；`npm run check; npm run smoke`。完成记录保留在001—003，不以本轮改写替代历史。

证据：[首批验收证据](docs/changes/2026-11-13-001-foundation.md) · [工作台截图](docs/wiki/assets/workbench-phase1.jpg)

台账：[2026-11-13-001-foundation](docs/changes/2026-11-13-001-foundation.md) · [2026-11-13-002-go-core](docs/changes/2026-11-13-002-go-core.md) · [2026-11-13-003-workbench](docs/changes/2026-11-13-003-workbench.md)

### V01 账户、持久任务与实际媒体协议

状态：**已验收**。模块：H01、H02、H03、H04、H07、H08、H09。依赖：V00。

v0.2单写者本地验收；供应商付费实测单列V15。

验收条件：

- PG账户与项目资源隔离
- 持久幂等、检查点、已知任务续查和未知受理不重提
- 私有资产导入/鉴权与恢复测试、桌面和手机尺寸验收

验证记录：

- 通过：v0.2历史验收；`npm run check; npm run test:db; npm run smoke`。Go31函数及子用例、前端36项、真实PG与浏览器重启验收。真实网关调用未验收，单列V15。

证据：[集成验收证据](docs/changes/2026-11-13-007-integration-database.md) · [仅本地demo性能基线](docs/wiki/assets/performance-local-2026-11-13.json)

台账：[2026-11-13-004-persistence-auth](docs/changes/2026-11-13-004-persistence-auth.md) · [2026-11-13-005-media-gateway](docs/changes/2026-11-13-005-media-gateway.md) · [2026-11-13-006-accounts-projects](docs/changes/2026-11-13-006-accounts-projects.md) · [2026-11-13-007-integration-database](docs/changes/2026-11-13-007-integration-database.md) · [2026-11-13-008-performance-baseline](docs/changes/2026-11-13-008-performance-baseline.md)

### V02 横向模块与纵向批次治理

状态：**已验收**。模块：H09。依赖：V01。

生成唯一任务数据源和可勾选TODO，之后逐批验收并同步完成标记。

验收条件：

- 所有剩余范围映射到模块与可验收批次
- 完成批次必须有测试证据、台账和完成日期
- 依赖、编号、证据路径和生成文档漂移自动检查

验证记录：

- 通过：治理校验；`npm run test:todo; npm run check:todo; npm run check:changes`。8项治理单测、生成一致性与27份文档/9份台账同步检查通过。完成标记后重新生成并校验。

证据：[8项治理边界测试](scripts/tasks.test.mjs) · [批次维护规则](docs/planning/README.md)

台账：[2026-11-13-009-batch-tracking](docs/changes/2026-11-13-009-batch-tracking.md)

### V03 项目云端草稿与版本恢复

状态：**已验收**。模块：H01、H02、H08、H09。依赖：V01、V02、V04。

把PG模式的本地草稿升级为项目私有持久草稿；不承诺DAG执行。

验收条件：

- 保存/读取/历史/恢复全链路通过
- 乐观并发冲突保留编辑内容，网络不明保留原幂等键
- 跨项目/账户隔离，重启恢复，memory模式明确本地边界

验证记录：

- 通过：草稿保存、恢复与保护联合验收；`npm run test:db; npm run check; npm run smoke`。真实PG双连接CAS/幂等/权限/重启/历史防改删/审计失败回滚通过；57项前端测试及双窗口冲突、恢复为新版、401重登保留编辑、离开保护与390x844手机页面实测通过。

证据：[草稿契约与边界](docs/wiki/drafts.md) · [真实PG并发、幂等、权限验证](internal/studio/drafts_test.go) · [草稿状态与恢复仓测试](apps/workbench/src/draftState.test.ts) · [保存后的云端草稿](docs/wiki/assets/drafts-phase3.jpg) · [联合验收证据](docs/changes/2026-11-13-013-batch-integration.md)

台账：[2026-11-13-010-project-drafts](docs/changes/2026-11-13-010-project-drafts.md) · [2026-11-13-011-drafts-activity-workbench](docs/changes/2026-11-13-011-drafts-activity-workbench.md) · [2026-11-13-013-batch-integration](docs/changes/2026-11-13-013-batch-integration.md)

### V04 关键业务审计与活动记录

状态：**已验收**。模块：H01、H02、H06、H08、H09。依赖：V01、V02。

记录项目、运行/任务受理、取消/核对等明确业务动作，提供归属隔离的只读活动记录。

验收条件：

- 关键业务状态与审计同事务提交
- 幂等重放不重复受理记录，审计不包含提示词、输出或凭据
- 游标分页、跨账户拒绝与UI活动记录验证；覆盖范围明确

验证记录：

- 通过：审计事务与活动页联合验收；`npm run test:db; npm run check; npm run smoke`。真实PG全量11.130秒通过，故障事务/候选缓存/无广播、复合游标/权限/去重通过；桌面及390x844手机活动页与服务重启留存实测通过。

证据：[审计覆盖与脱敏边界](docs/wiki/audit.md) · [权限、分页和事务故障验证](internal/studio/audit_test.go) · [真实活动页面](docs/wiki/assets/activity-phase3.jpg) · [联合验收证据](docs/changes/2026-11-13-013-batch-integration.md)

台账：[2026-11-13-011-drafts-activity-workbench](docs/changes/2026-11-13-011-drafts-activity-workbench.md) · [2026-11-13-012-transactional-audit](docs/changes/2026-11-13-012-transactional-audit.md) · [2026-11-13-013-batch-integration](docs/changes/2026-11-13-013-batch-integration.md)

### V05 可观测与资源准入

状态：**已验收**。模块：H01、H03、H07、H08、H09。依赖：V04。

单实例可观测、请求关联、工作区/模型/SSE资源准入及用户可见容量/冷却反馈；分布式配额仍由V16验收。

验收条件：

- 存活与就绪分离，数据库失效不假健康，探针不请求真实模型
- 指标路由/错误标签固定有界，不泄正文/凭据/租户；指标需独立运维令牌
- 工作区及模型限额、终态释放、未知状态占用、幂等重放和重启恢复正确
- 流订阅限额、慢消费者、满载拒绝/恢复及PG隔离负例通过
- 前端真实容量、低频/隐藏暂停刷新、冷却和请求编号，不自动重发收费请求

验证记录：

- 通过：准入、观测与前端源码/构建；`npm run check; npm run test:db`。最终check通过，Go65函数及子例/前端70/治理8、vet/TS/构建；真实PG全量studio14.012秒，含工作区隔离、未知占用/重启与health200/ready503。
- 通过：独立进程与性能回归；`npm run smoke; npm run smoke:runtime; npm run perf:local`。两套冒烟通过，runtime5组报告含二进制摘要；本地bootstrap P95 21.39ms。非真实网关/生产容量成绩。
- 通过：数据库升级与浏览器验收；`pg_dump + psql只读迁移核对；CUA桌面/390x844及独立小限额UI`。升级前备份，版本004与2索引确认。容量/429/冷却/输入保留/无自动重发/手动恢复通过；侧栏关闭23秒无runtime请求；截图保留。复制反馈成功，跨工具剪贴板读回未记通过。
- 通过：契约与交付治理；`OpenAPI严格YAML/引用核对; npm run check:docs; npm run check:changes; npm run check:todo`。OpenAPI25路径/29操作/94引用；README/Wiki/014-017台账与任务源统一，V06-V22仍未完成。

证据：[持久准入与幂等规则](docs/wiki/admission.md) · [探针、指标及缓存边界](docs/wiki/observability.md) · [独立运行冒烟实测](docs/wiki/assets/runtime-smoke-v0.4-2026-11-13.json) · [v0.4本地性能基线](docs/wiki/assets/performance-local-v0.4-2026-11-13.json) · [真实PG容量界面](docs/wiki/assets/runtime-phase4.jpg) · [满载拒绝与冷却界面](docs/wiki/assets/admission-phase4.jpg) · [390x844容量布局](docs/wiki/assets/mobile-runtime-phase4.jpg) · [联合验收与实际限制](docs/changes/2026-11-13-017-runtime-integration.md)

台账：[2026-11-13-014-resource-admission](docs/changes/2026-11-13-014-resource-admission.md) · [2026-11-13-015-observability](docs/changes/2026-11-13-015-observability.md) · [2026-11-13-016-runtime-workbench](docs/changes/2026-11-13-016-runtime-workbench.md) · [2026-11-13-017-runtime-integration](docs/changes/2026-11-13-017-runtime-integration.md)

### V06 模型目录与参数能力

状态：**已验收**。模块：H03、H07、H08、H09。依赖：V01。

有限服务端模型配置、版本化能力与精确预设、原幂等和历史执行快照，完整模型/规格工作台；真实供应商联调另由V15验收。

验收条件：

- 真实模型配置映射与能力版本
- 尺寸/时长/数量在前后端一致校验
- 停用模型保留旧任务身份与读取能力，缓存更新验证

验证记录：

- 通过：目录、快照、Provider与前端源码/真实PG；`npm run check; npm run test:db; npm run build`。Go81函数及子例、前端85、治理8、vet/TS/构建；最终真实PG全量studio14.699秒，包含005格式栅栏、原请求重放与停用恢复。
- 通过：独立进程、接口结构与回归基线；`npm run smoke; npm run smoke:runtime; npm run smoke:catalog; npm run perf:local; OpenAPI结构核对`。三套冒烟通过；目录3组/运行5组。26路径30操作109引用。bootstrap32并发1000次P95 20.80ms；非生产容量成绩。
- 通过：升级与桌面/窄屏联合验收；`本地PG备份/005升级；CUA桌面与390x844；PG快照只读核对`。旧项目任务/草稿v5保留，新任务身份/参数持久；多模型/数量/中文字节/10秒1080p/停用刷新通过，截图及浏览器报告留存。

证据：[配置、能力、历史快照与兼容边界](docs/wiki/models.md) · [目录、精确参数与真实PG升级/恢复](internal/studio/catalog_test.go) · [阻止旧二进制写丢快照的格式版本](internal/studio/migrations/005_model_snapshots.sql) · [独立目录进程验收](scripts/catalog-smoke.mjs) · [最终二进制目录冒烟证据](docs/wiki/assets/catalog-smoke-v0.5-2026-11-13.json) · [真实浏览器和PG联合核对](docs/wiki/assets/browser-models-v0.5-2026-11-13.json) · [模型规格桌面界面](docs/wiki/assets/models-phase5.jpg) · [390x844模型设置](docs/wiki/assets/mobile-models-phase5.jpg) · [全部验证结果与边界](docs/changes/2026-11-13-021-model-integration.md)

台账：[2026-11-13-018-model-catalog](docs/changes/2026-11-13-018-model-catalog.md) · [2026-11-13-019-model-provider](docs/changes/2026-11-13-019-model-provider.md) · [2026-11-13-020-model-workbench](docs/changes/2026-11-13-020-model-workbench.md) · [2026-11-13-021-model-integration](docs/changes/2026-11-13-021-model-integration.md)

### V07 参考素材上传与图片编辑

状态：**待外部条件**。模块：H01、H03、H04、H07、H08、H09。依赖：V06。

上传私有参考素材，完整贯通文档中的图片编辑路径。

验收条件：

- 限额、真实格式、文件大小、归属与引用校验
- 编辑提交原始请求持久化与幂等正确
- 上传→编辑→查询→导入→下载端到端fixture和真实验收

外部条件：V07本地实现、真实PG、协议fixture与浏览器已通过；尚缺服务端已开通的真实图片编辑模型/凭据及受控调用验收条件。按references与media-gateway Wiki配置私有环境及结果域名，确认实际模型支持参考图后执行受控上传→编辑→查询→导入→下载，记录证据后才能完成本批；密钥不需发送到对话或提交仓库。

验证记录：

- 通过：私有参考素材与权限边界；`npm run check; npm run test:db`。106个Go测试函数及子例、真实PG25.156秒、前端100项、治理8项、vet/TS/生产构建通过；格式、限额、权限、事务和006升级已验收。
- 通过：编辑协议、重启与结果交付；`npm run smoke; npm run smoke:runtime; npm run smoke:catalog; npm run smoke:references; npm run perf:local; 浏览器验收`。四套隔离冒烟、实际multipart HTTP fixture与PG重启通过；修复停机Query误写FAILED。桌面/390x844验证上传、排序编辑、复用、项目隔离和旧数据保留；demo不能替代真实效果。
- 待条件：真实供应商端到端验收；`配置服务端真实模型/凭据后执行受控上传→编辑→查询→导入→下载`。当前没有真实服务端凭据及真实生成验收记录；本地fixture不能替代此条。

证据：[参考素材隔离冒烟](docs/wiki/assets/reference-smoke-v0.6-2026-11-13.json) · [桌面手机与数据库验收](docs/wiki/assets/references-browser-v0.6-2026-11-13.json) · [图片编辑工作台](docs/wiki/assets/references-phase6.jpg) · [390x844参考素材](docs/wiki/assets/mobile-references-phase6.jpg) · [同版本本地性能基线](docs/wiki/assets/performance-local-v0.6-2026-11-13.json) · [联合验证与真实待条件](docs/changes/2026-11-13-025-reference-integration.md)

台账：[2026-11-13-022-reference-assets](docs/changes/2026-11-13-022-reference-assets.md) · [2026-11-13-023-media-edit-provider](docs/changes/2026-11-13-023-media-edit-provider.md) · [2026-11-13-024-reference-workbench](docs/changes/2026-11-13-024-reference-workbench.md) · [2026-11-13-025-reference-integration](docs/changes/2026-11-13-025-reference-integration.md)

### V08 资产库与对象存储

状态：**已验收**。模块：H01、H02、H04、H06、H07、H08、H09。依赖：V01。

可检索、管理、备份的独立资产元数据与存储接口。

验收条件：

- 搜索/标签/版本/软删除及恢复
- S3兼容私有对象导入、受鉴权下载和过期处理
- 本地到对象存储迁移、孤立文件回收与权限验收

验证记录：

- 通过：独立资产目录与版本权限；`npm run check; npm run test:db`。129个Go函数及子例、真实PG studio42.534秒、前端118项、治理8项、vet/TS/构建。覆盖目录/来源与审计原子性、CAS并发、原键、不可变修订、回收恢复和007回填/格式栅栏。
- 通过：私有对象存储与迁移；`加载隔离TEST_S3环境后 go test ./internal/studio -run TestS3Real -count=1 -v；go run ./cmd/storage 计划及两次apply`。根独立真实SeaweedFS两项测试通过0.273秒；私有导入、完整读回、缩略图、Range/HEAD、对象缺失、回收恢复、原键不重复生成；CLI复制校验与重入保留原件。不是云生产/真实AI验收。
- 通过：孤立文件维护与工程验收；`npm run smoke; npm run smoke:runtime; npm run smoke:catalog; npm run smoke:references; npm run smoke:library; npm run perf:local; OpenAPI严格校验`。五套冒烟通过。PG维护保护所有引用、默认dry-run、错误owner/数据库/计划拒绝，apply仅测试临时孤立文件。OpenAPI36路径46操作194引用。32并发1000次bootstrap P95 21.80ms，非生产容量。
- 通过：旧库升级、浏览器与文档；`本地PG备份/007升级；CUA桌面与390x844；npm run check:todo; npm run check:changes`。旧5项素材回填，会话/项目/任务保留；元数据历史另存、未保存恢复、回收恢复、检索、实际应用重启通过。手机无横溢、缩略图512x341；修复trash输入与恢复排序。61份文档/30台账同步，详细事实见030。

证据：[目录、版本、权限及恢复契约](docs/wiki/assets.md) · [真实S3兼容配置、迁移与限制](docs/wiki/object-storage.md) · [双锁、数据库归属、可达性与计划复核](docs/wiki/storage-maintenance.md) · [HTTP与真实PG并发/回填/事务测试](internal/studio/asset_library_test.go) · [跨进程锁、错库、计划与临时目录apply](internal/studio/storage_maintenance_test.go) · [真实SeaweedFS及迁移CLI证据](docs/wiki/assets/object-storage-v0.7-2026-11-13.json) · [独立资产库二进制冒烟](docs/wiki/assets/library-smoke-v0.7-2026-11-13.json) · [升级/重启与真实页面验证](docs/wiki/assets/library-browser-v0.7-2026-11-13.json) · [已持久保存的素材详情](docs/wiki/assets/library-detail-phase7.png) · [390x844手机素材详情](docs/wiki/assets/mobile-library-phase7.png) · [根独立集成、问题修复及下一步](docs/changes/2026-11-13-030-asset-library-integration.md)

台账：[2026-11-13-026-asset-library](docs/changes/2026-11-13-026-asset-library.md) · [2026-11-13-027-object-storage](docs/changes/2026-11-13-027-object-storage.md) · [2026-11-13-028-library-workbench](docs/changes/2026-11-13-028-library-workbench.md) · [2026-11-13-029-storage-maintenance](docs/changes/2026-11-13-029-storage-maintenance.md) · [2026-11-13-030-asset-library-integration](docs/changes/2026-11-13-030-asset-library-integration.md)

### V09 持久事件与任务租约隔离

状态：**已验收**。模块：H01、H03、H06、H07、H08、H09。依赖：V04、V05、V08。

业务/审计同事务Outbox、持久feed与范围回放、数据库任务领取/续约/旧执行者隔离及前端有界补齐；保持单控制写者和私有目录锁，全站多实例由V16验收。

验收条件：

- 状态与Outbox同事务，消费去重和断点恢复
- 租约/epoch fencing与超时回收，旧执行者拒绝写入
- 重复投递、宕机、失锁和不明受理故障注入

验证记录：

- 通过：同事务持久事件与恢复；`npm run test:db; Go实际COMMIT确认丢失/并发序号/背压与SSE专项`。根最终真实PG全量studio66.419秒；producer/consumer回滚和确认丢失、10000pending、2048窗口、重启去重、scope与reset未投影水位通过。
- 通过：租约领取与旧执行者隔离；`真实PostgreSQL租约13函数专项；npm run test:db；实际子进程Kill/重启`。13函数专项10.457秒，最终全量包含；8连接竞争、续租/失锁、generation及版本、claim后取消/核对缓存保护、步骤超时、媒体/LLM背压通过。实际Kill后一次POST、SUBMIT_UNKNOWN、原键和游标恢复。
- 通过：前端、升级与联合验收；`npm run check; 五套smoke; npm run perf:local; OpenAPI核对; CUA桌面/390x844; npm run check:changes`。135前端/8治理、vet/TS/构建；300/400资源有界收敛。停机联备并升级001—009，双窗口更新与dirty编辑保留、重载恢复、窄屏/控制台通过。五份报告匹配最终二进制；无生产容量或多实例声明。

证据：[事务、提交顺序、确认丢失与回放水位](internal/studio/outbox_test.go) · [真实连接租约/失效隔离和背压](internal/studio/task_leases_test.go) · [真实进程Kill、一次上游POST、原键与SSE恢复](internal/studio/durable_process_test.go) · [有界恢复、尾部可达、重试及静默收敛](apps/workbench/src/durableEvents.test.ts) · [持久通知协议与耐久边界](docs/wiki/events.md) · [实际worker与租约配置](docs/wiki/scheduler.md) · [事务顺序与单控制写者决策](docs/wiki/decisions/0006-durable-events-and-task-leases.md) · [真实页面与升级记录](docs/wiki/assets/events-browser-v0.8-2026-11-13.json) · [跨窗口更新保留创作输入](docs/wiki/assets/events-phase8.png) · [未保存素材编辑保护](docs/wiki/assets/events-dirty-phase8.png) · [390x844布局](docs/wiki/assets/mobile-events-phase8.png) · [最终二进制本机回归口径](docs/wiki/assets/performance-local-v0.8-2026-11-13.json) · [隔离限额/可观测回归](docs/wiki/assets/runtime-smoke-v0.8-2026-11-13.json) · [模型目录回归](docs/wiki/assets/catalog-smoke-v0.8-2026-11-13.json) · [参考素材编辑回归](docs/wiki/assets/reference-smoke-v0.8-2026-11-13.json) · [资产生命周期回归](docs/wiki/assets/library-smoke-v0.8-2026-11-13.json) · [根独立验收与实际限制](docs/changes/2026-11-13-034-durable-execution-integration.md)

台账：[2026-11-13-031-durable-events](docs/changes/2026-11-13-031-durable-events.md) · [2026-11-13-032-task-leases](docs/changes/2026-11-13-032-task-leases.md) · [2026-11-13-033-durable-events-client](docs/changes/2026-11-13-033-durable-events-client.md) · [2026-11-13-034-durable-execution-integration](docs/changes/2026-11-13-034-durable-execution-integration.md)

### V10 多轮对话与上下文管理

状态：**已验收**。模块：H01、H02、H03、H06、H07、H08、H09。依赖：V01、V06、V09。

持久对话、上下文裁剪、消息分支与继续生成。

验收条件：

- 会话/消息归属和多轮上下文真实传递
- 上下文预算与模型窗口校验
- 编辑分支、停止/继续、刷新与中断恢复，不重发旧请求

验证记录：

- 通过：持久对话、消息树及故障恢复；`npm run test:db; go test ./internal/studio -run TestPostgresConversationActualCrash -count=1`。根真实PG全量studio82.959秒；CAS/作用域/不可变/事务回滚/COMMIT不明和实际Kill保留部分输出、原键不重发通过。
- 通过：上下文预算与真实上游请求结构；`npm run smoke:conversations; Go conversation/provider/catalog tests`。独立Go+HTTP fixture五组通过，7个预期POST；真实messages、分支隔离、整轮裁剪、超限前置拒绝与显式继续核对。未发真实付费请求。
- 通过：工作台及联合验收；`npm run check; 六套隔离smoke; npm run perf:local; CUA桌面/双窗口/390x844; OpenAPI与文档检查`。154项前端/8项治理/Go vet/TS/构建通过；双窗口输入和父节点保护、分支/停止继续/刷新/旧历史及无横溢通过；性能仅本地memory/demo。

证据：[会话/分支/上下文与PG事务回归](internal/studio/conversation_test.go) · [实际进程Kill及部分回答恢复](internal/studio/durable_process_test.go) · [独立真实HTTP上下文验收脚本](scripts/conversation-smoke.mjs) · [冻结接口与实施边界](docs/planning/v10-contract.md) · [会话领域与010迁移说明](docs/wiki/conversation.md) · [结构化Provider与预算配置](docs/wiki/chat-provider.md) · [0.9.0 OpenAPI契约](docs/api/openapi.yaml) · [五组上下文与分支fixture报告](docs/wiki/assets/conversation-smoke-v0.9-2026-11-13.json) · [桌面/双窗口/手机浏览器验收](docs/wiki/assets/conversations-browser-v0.9-2026-11-13.json) · [本地基线与容量限制](docs/wiki/assets/performance-local-v0.9-2026-11-13.json) · [桌面聊天工作台截图](docs/wiki/assets/conversations-phase9.jpg) · [390x844手机会话截图](docs/wiki/assets/mobile-conversations-phase9.jpg)

台账：[2026-11-13-035-conversation-core](docs/changes/2026-11-13-035-conversation-core.md) · [2026-11-13-036-chat-provider](docs/changes/2026-11-13-036-chat-provider.md) · [2026-11-13-037-conversations-workbench](docs/changes/2026-11-13-037-conversations-workbench.md) · [2026-11-13-038-conversation-integration](docs/changes/2026-11-13-038-conversation-integration.md)

### V11 账户安全与自助恢复

状态：**待外部条件**。模块：H01、H06、H07、H08、H09。依赖：V04。

账户验证/恢复、改密、会话撤销、安全活动及TLS加密邮件队列；本地验收通过，实际邮件按用户安排后续配置。

验收条件：

- 邮箱验证、找回密码的一次性令牌与防枚举
- 会话管理/撤销、密码变更后失效策略
- 认证限流、邮件fixture、实际渠道和权限回归

外部条件：用户确认先完成本地链路，真实邮件渠道后续配置。恢复条件：实际SMTP/TLS与发件域名、可信回站origin、服务端凭据及独立队列密钥、指定测试邮箱；受控验实际到达/验证/找回后补证据。fixture不能代替该项。

验证记录：

- 通过：安全领域与会话撤销；`Go及真实PostgreSQL令牌/会话/密码并发/事务专项`。根真实PG全量115.317秒通过；用途/过期/并发单次消费、双连接login/改密竞争、原子撤销/审计、跨账户、旧session升级和旧writer拒绝。
- 通过：加密邮件队列与SMTP模拟器；`SMTP TLS fixture、加密/重试/确认不明/重启与独立API链路`。8普通+4PG邮件函数和根TLS HTTP全链通过；真实DATA后杀子进程，恢复UNKNOWN且无再次投递；密文/容量/有限重试/过期清理通过，无实际外发。
- 通过：账户安全工作台与联合验收；`npm run check; npm run test:db; 隔离smoke; 浏览器; 文档/TODO检查`。173项前端、8项治理、vet/TS/构建、六套冒烟、OpenAPI50路径61操作293引用通过；桌面/390手机、真实撤销测试session/401、链接清理/禁用找回、011联备升级比对及文档通过。
- 待条件：真实邮件渠道验收；`配置实际SMTP渠道后受控验证邮件收取、验证与找回`。用户确认先完成本地链路，真实邮件渠道后续配置；不能以fixture替代实际投递。

证据：[账户安全与011迁移边界](docs/wiki/account-security.md) · [TLS加密投递与真实渠道恢复条件](docs/wiki/mail-delivery.md) · [0.10.0 API契约](docs/api/openapi.yaml) · [桌面与手机浏览器验收](docs/wiki/assets/account-security-browser-v0.10-2026-11-13.json) · [升级前dump与011后只读数据比对](docs/wiki/assets/account-security-upgrade-v0.10-2026-11-13.json) · [隔离memory/demo性能口径](docs/wiki/assets/performance-local-v0.10-2026-11-13.json) · [根全量与本地TLS HTTP验收](docs/changes/2026-11-13-042-account-security-integration.md)

台账：[2026-11-13-039-account-security](docs/changes/2026-11-13-039-account-security.md) · [2026-11-13-040-security-mail](docs/changes/2026-11-13-040-security-mail.md) · [2026-11-13-041-account-security-workbench](docs/changes/2026-11-13-041-account-security-workbench.md) · [2026-11-13-042-account-security-integration](docs/changes/2026-11-13-042-account-security-integration.md)

### V12 权益账本与任务结算

状态：**已验收**。模块：H01、H02、H03、H05、H06、H07、H08、H09。依赖：V04、V06。

六位整数精度、不可变权益账本、生成前报价确认及工作区钱包/流水；可信usage或注册交付产物结算，未知保留待核实。含停机核账/赠额/全额权益冲正，不含真实支付或供应商成本核对。

验收条件：

- 报价版本、冻结/释放/结算/冲正事务与幂等
- 上游usage缺失进入待核实，失败/部分输出政策明确
- 并发超扣、重复结算、退款冲正和对账不平衡测试
- 钱包/流水/冻结单与聊天及媒体报价确认完整可用，输入变化失效和未知原请求保护

验证记录：

- 通过：报价与不可变权益账本；`npm run test:db; Go PostgreSQL ledger/admin targeted tests`。最终真实PG全量studio148.283秒；并发冻结/防超扣、不可变双分录、原幂等、Outbox回滚、COMMIT确认不明、离线预演/摘要/原收据/核账通过。累计结算容量修正后8项PG专项6.956秒。
- 通过：任务与可信计量闭环；`npm run test:db; Go TestPostgresBillingGatewayUsageEndToEnd/TestPostgresBillingActualCrashHoldsAndReplay`。实际HTTP usage到PG涵盖有效、缺失、非法、超预算及不完整流；真正Kill后INTERRUPTED/REVIEW、原键不重复POST、人工证据核实释放。媒体按注册产物结算；本地fixture不等于真实供应商验收。
- 通过：工作区权益界面与联合验收；`npm run check; npm run test:db; 六套smoke; npm run perf:local; CUA桌面/390x844; OpenAPI解析; npm run check:todo; npm run check:changes`。202项前端/22文件、8项治理、vet/TS/构建、六套隔离smoke通过。浏览器PG/demo完成聊天/图像/视频海报三单结算，输入变化撤销报价，390x844无横溢，console无警告错误；012升级保留旧数据。

证据：[整数、双分录、并发与提交确认丢失测试](internal/studio/ledger_test.go) · [报价预检、作用域权限与分页测试](internal/studio/billing_http_test.go) · [离线预演、计划摘要、writer锁与收据重放](internal/studio/billing_admin_test.go) · [实际HTTP usage到PG结算与REVIEW](internal/studio/billing_gateway_test.go) · [实际进程Kill恢复及原键不重发](internal/studio/billing_process_test.go) · [非法参考ID不触发PG熔断](internal/studio/billing_security_test.go) · [金额、报价作用域、原请求及结算版本保护](apps/workbench/src/billing.test.tsx) · [冻结接口与计量契约](docs/planning/v12-contract.md) · [账本领域与事务约束](docs/wiki/ledger.md) · [配置、012升级与离线处置](docs/wiki/billing-operations.md) · [钱包和报价交互规则](docs/wiki/billing-workbench.md) · [0.11.0 API契约](docs/api/openapi.yaml) · [011至012联备及旧数据比对](docs/wiki/assets/billing-upgrade-v0.11-2026-11-13.json) · [PG/demo桌面手机报价结算验收](docs/wiki/assets/billing-browser-v0.11-2026-11-14.json) · [关闭权益的memory/demo基线及容量边界](docs/wiki/assets/performance-local-v0.11-2026-11-14.json) · [桌面钱包与三单结算截图](docs/wiki/assets/billing-phase11.jpg) · [390x844报价确认截图](docs/wiki/assets/mobile-billing-quote-phase11.jpg)

台账：[2026-11-13-043-credit-ledger](docs/changes/2026-11-13-043-credit-ledger.md) · [2026-11-13-044-provider-usage](docs/changes/2026-11-13-044-provider-usage.md) · [2026-11-13-045-billing-workbench](docs/changes/2026-11-13-045-billing-workbench.md) · [2026-11-13-046-billing-integration](docs/changes/2026-11-13-046-billing-integration.md)

### V13 订单、支付与退款

状态：**待开始**。模块：H01、H05、H06、H08。依赖：V11、V12。

支付与权益解耦；真实商户接入需要配置，fixture通过不等于真实收款完成。

验收条件：

- 订单状态机、签名回调、防重放与重复支付处理
- 退款回调/超时核实、权益原子入账与审计
- 商户沙箱及真实受控支付退款验收，用户可见订单记录

### V14 审核、风控与运营控制台

状态：**进行中**。模块：H01、H03、H04、H05、H06、H07、H08、H09。依赖：V04、V11、V12。

运营角色、最小权限、内容审核和异常核实工作流。先交付V14-A工作区运营RBAC/异常工单/预演处置闭环，再推进V14-B生成前后审核、V14-C申诉/封禁及风控；V11真实渠道依赖未放行前不得标整批完成。

验收条件：

- 管理员RBAC与关键操作审计/二次确认
- 生成前/后审核、申诉、封禁与额度风控
- 受理不明的证据核实、原任务恢复与权限负例

验证记录：

- 通过：V14-A 运营权限与离线角色治理；`npm run test:db（含TestPostgresOperations*）；npm run check`。2026-11-14通过：真实PG全量169.657s覆盖scope/multi-role/revoke、密码/凭据重核、审计回滚、停机CLI精确预演及原回执；活动版本为撤权预留容量。
- 通过：V14-A 异常核实工单及事务处置；`npm run test:db（含TestPostgresOperationsUnknownCommitRecoversOneReceipt）`。2026-11-14通过：CAS/幂等/预演过期、原handle查询不POST、旧lease拒写、账务/工单/审计/Outbox回滚；真实PG COMMIT确认丢失后App重启只读唯一收据，重复execute不二次释放。
- 通过：V14-A 工作台及本地联合验收；`npm run check; npm run test:db; 六套smoke; perf:local; CUA桌面/手机; OpenAPI引用校验; check:todo/docs/changes`。2026-11-14通过：217项前端/23文件、8项治理、Go vet/构建及PG全量；六套隔离冒烟；CUA桌面/390手机开单→预演→密码→收据/审计、scope隔离；OpenAPI61路径73操作340引用及文档治理。完整V14仍未验收。
- 通过：V14-B1 文本审核本地闭环；`npm run check; npm run test:db; Go审核PG专项; 六套smoke; perf:local; CUA桌面/手机; OpenAPI引用校验; check:todo/docs/changes`。2026-11-14本地通过：237项前端/24文件、8治理、PG全量170.241s和审核5组8.853s；输入无副作用、冻结策略、全文事务放行/回滚、COMMIT确认丢失后App恢复与原键；浏览器拒绝/隔离/正常新提问纳入1省略2及刷新/响应式；真实审核商未选，B2/C仍待办。
- 待条件：V14-B1 真实文本审核渠道；`选定真实服务后完成text-v1适配、真实响应与故障边界受控验收`。2026-11-14用户确认审核商暂未选型，先本地闭环；需要接口文档、服务端配置与受控测试内容，fixture不代替真实验收。
- 待验证：V14-B2 图片与视频审核；`参考图/产物私有隔离、所有下载门禁、租约恢复与真实多模态审核`。B1仅文本；新媒体在审核开启时拒绝，原已受理任务不重发。候选资产必须纳入存储可达性，待审不能注册已交付或自动结算。
- 待验证：V14-C 申诉、封禁与风控；`申诉状态机、封禁/额度策略、原请求重放边界与浏览器验收`。本子批次后续推进。
- 待条件：完整V14依赖放行；`V11真实邮件渠道受控验收与完整V14复核`。用户安排真实邮件后续配置；保留V11依赖，不能用本地fixture勾选整批。

证据：[已实施接口与子批次边界](docs/planning/v14a-contract.md) · [scoped权限、密码与审计回滚](internal/studio/operations_access_test.go) · [停机授权、预演、稳定键、撤销与版本容量](internal/studio/operations_admin_test.go) · [工单、CAS/计划、事务处置与租约隔离](internal/studio/operations_cases_test.go) · [权限负例、严格正文、游标/Scope及恢复](internal/studio/operations_http_test.go) · [真实PG提交确认丢失、App重启及唯一收据](internal/studio/operations_integration_test.go) · [15项前端协议/隔离/未知恢复与密码验证](apps/workbench/src/operations.test.tsx) · [联合命令/版本摘要/OpenAPI与测试汇总](docs/wiki/assets/operations-validation-v0.12-2026-11-14.json) · [真实Go+PG demo桌面/手机交互验收](docs/wiki/assets/operations-browser-v0.12-2026-11-14.json) · [012到014前联备及旧业务比对](docs/wiki/assets/operations-upgrade-v0.12-2026-11-14.json) · [隔离demo回归，非生产容量](docs/wiki/assets/performance-local-v0.12-2026-11-14.json) · [文本本地闭环已验收；真实渠道、多模态及后续批次分开](docs/planning/v14b1-contract.md) · [规则策略冻结、严格配置与PG不可变决策](internal/studio/moderation_test.go) · [私有HTTP契约、异常响应/跳转/超时与并发边界](internal/studio/moderation_http_test.go) · [输入锁外、无副作用、候选隔离、上下文与取消](internal/studio/moderation_execution_test.go) · [PG准入、权益、回滚及关闭策略后的隔离恢复](internal/studio/moderation_integration_test.go) · [真实PG COMMIT确认丢失：ALLOW/BLOCK恢复原决策，原键不重发](internal/studio/moderation_commit_test.go) · [20项前端审核语义、乱序门禁与受限轮后普通提问](apps/workbench/src/moderation.test.tsx) · [联合命令与最终二进制摘要](docs/wiki/assets/moderation-validation-v0.13-2026-11-14.json) · [桌面/手机输入拒绝、输出隔离、正常新问题与刷新](docs/wiki/assets/moderation-browser-v0.13-2026-11-14.json) · [014到015前联备及旧业务保持，无自动批准](docs/wiki/assets/moderation-upgrade-v0.13-2026-11-14.json) · [隔离memory/demo回归，不是审核或PG容量](docs/wiki/assets/performance-local-v0.13-2026-11-14.json)

台账：[2026-11-14-047-operations-access](docs/changes/2026-11-14-047-operations-access.md) · [2026-11-14-048-operations-cases](docs/changes/2026-11-14-048-operations-cases.md) · [2026-11-14-049-operations-workbench](docs/changes/2026-11-14-049-operations-workbench.md) · [2026-11-14-050-operations-integration](docs/changes/2026-11-14-050-operations-integration.md) · [2026-11-14-051-text-moderation-engine](docs/changes/2026-11-14-051-text-moderation-engine.md) · [2026-11-14-052-moderation-execution](docs/changes/2026-11-14-052-moderation-execution.md) · [2026-11-14-053-moderation-workbench](docs/changes/2026-11-14-053-moderation-workbench.md) · [2026-11-14-054-text-review-integration](docs/changes/2026-11-14-054-text-review-integration.md)

### V15 真实网关与产物交付验收

状态：**待外部条件**。模块：H03、H04、H07。依赖：V01、V06。

使用用户实际已开通模型和服务端配置验收，自动测试不得发付费请求。

验收条件：

- 真实LLM、图片及视频生成/下载/播放
- 原凭据查询、429、超时与结果失效行为核对
- 记录模型参数、配额和证据，不保存密钥

外部条件：未提供本地服务端密钥、已开通模型参数和受控真实调用条件；按media-gateway Wiki配置后再做真实验收，不需要把密钥发到对话或提交仓库。

### V16 分布式流与配额公平调度

状态：**待开始**。模块：H03、H07。依赖：V05、V09。

流、提交、轮询、导入按负载拆分，持久流日志与容量保护。

验收条件：

- 连接池/worker和凭据/模型配额有界
- 流日志、快照、回放与fencing在故障下正确
- 任务公平性、供应限流、背压与降级实测

### V17 性能与长稳态放行

状态：**待开始**。模块：H03、H07、H08。依赖：V15、V16。

按原方案固定环境验收，不把当前回环demo报告当生产成绩。

验收条件：

- 万级活跃流和目标媒体在途量，P95/P99分层
- CPU/RSS/FD/GC/数据库等待/慢订阅者与前端基准
- 24小时稳态、突发/故障注入及瓶颈报告

### V18 部署、备份与灾备演练

状态：**待开始**。模块：H01、H04、H07、H09。依赖：V09、V11。

可重复部署、TLS/密钥管理、备份恢复及迁移治理。

验收条件：

- 分环境配置、版本化迁移、灰度与回滚脚本
- PG/PITR与对象存储备份实际恢复
- 多可用区切换与RPO/RTO证据、监控值班手册

### V19 可执行工作流与模板

状态：**待开始**。模块：H02、H03、H10、H08。依赖：V03、V06、V09。

将版本草稿升级为显式执行DAG和可复用模板。

验收条件：

- 节点/边、环与输入类型校验、固定流程版本
- 人工确认、节点重跑、取消和恢复不重复执行收费步骤
- 文本→图像→视频模板完整交付及资产归属

### V20 视频编辑与分镜创作

状态：**待开始**。模块：H02、H03、H04、H08、H10。依赖：V07、V08、V19。

参考素材、分镜、编辑与延长按模型能力开放。

验收条件：

- 参考片段和角色素材归属与参数约束
- 视频编辑/延长API、版本比较与局部重做
- 真实视频解码/播放/转码、导出成片验收

### V21 协作与专项创作扩展

状态：**待开始**。模块：H01、H02、H08、H10。依赖：V08、V11、V19。

团队协作、漫剧流程、PPT可交付文件；分别子批次验收后再开放。

验收条件：

- 成员/邀请/项目角色和协作冲突处理
- 漫剧角色/场景/分镜复用链路
- PPT导出实际可编辑文件与渲染验收；不以入口代替功能

### V22 公开收费发布验收

状态：**待开始**。模块：H01、H03、H04、H05、H06、H07、H08、H09。依赖：V07、V08、V10、V13、V14、V15、V17、V18。

商业核心发布闸门，专项V20/V21可后续独立发布。

验收条件：

- 支付退款/审核/隐私规则/客服与数据导出真实闭环
- 安全测试、容量/灾备和供应升级回归全部留证
- 部署灰度、回滚演练与运营移交完成再开放公众
