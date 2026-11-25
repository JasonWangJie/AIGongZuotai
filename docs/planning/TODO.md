# 开发 TODO · 按模块横切、按业务纵切

更新时间：2026-11-25。已验收 **14/24 个批次**。这是批次数量，不是工时或整体产品完成百分比；各批工作量不同。

本文件由 [docs/planning/tasks.json](tasks.json) 生成，请修改唯一任务源后运行 `npm run todo:sync`。状态与勾选不能脱离验收证据；规则见 [AGENTS.md](../../AGENTS.md) 和 [任务维护说明](README.md)。

**横切**表示长期负责的技术/产品模块，**纵切**表示用户能实际完成的一条业务链及其完整验收。横向模块包含后续批次时，不能因某个基础能力完成就把整个模块打勾。

## 横向模块

| 模块 | 职责 | 已验收/相关批次 | 批次 |
| --- | --- | --- | --- |
| H01 身份与权限 | 账户、会话、组织权限、身份恢复 | 9/16 | V01、V03、V04、V05、V07、V08、V09、V10、V11、V12、V13、V14、V16、V18、V21、V22 |
| H02 项目与创作数据 | 项目、草稿、版本、协作数据 | 8/10 | V01、V03、V04、V08、V10、V12、V16、V19、V20、V21 |
| H03 模型与执行 | LLM、图片/视频协议、参数能力 | 9/15 | V00、V01、V05、V06、V07、V09、V10、V12、V14、V15、V16、V17、V19、V20、V22 |
| H04 资产与交付 | 上传、私有存储、下载、生命周期 | 3/9 | V01、V07、V08、V14、V15、V16、V18、V20、V22 |
| H05 账务与支付 | 报价、权益账本、冻结结算、订单退款 | 1/4 | V12、V13、V14、V22 |
| H06 审核与运营 | 审计、治理、风控、运营控制台 | 5/9 | V04、V08、V09、V10、V11、V12、V13、V14、V22 |
| H07 可靠性与性能 | Outbox、租约、配额、可观测、容量与灾备 | 8/15 | V01、V05、V06、V07、V08、V09、V10、V11、V12、V14、V15、V16、V17、V18、V22 |
| H08 前端体验 | 工作台、移动布局、可访问性、性能 | 13/21 | V00、V01、V03、V04、V05、V06、V07、V08、V09、V10、V11、V12、V13、V14、V16、V17、V19、V20、V21、V22、V23 |
| H09 工程与交付治理 | TODO、文档、测试、版本、部署 | 13/18 | V00、V01、V02、V03、V04、V05、V06、V07、V08、V09、V10、V11、V12、V14、V16、V18、V22、V23 |
| H10 工作流与专项创作 | DAG、模板、分镜、漫剧、演示文稿 | 1/3 | V19、V20、V21 |

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
- [ ] **V13 订单、支付与退款** — 待外部条件；依赖：V11、V12
- [ ] **V14 审核、风控与运营控制台** — 进行中；依赖：V04、V11、V12
- [ ] **V15 真实网关与产物交付验收** — 待外部条件；依赖：V01、V06
- [x] **V16 分布式流与配额公平调度** — 已验收；依赖：V05、V09；完成：2026-11-21
- [ ] **V17 性能与长稳态放行** — 进行中；依赖：V15、V16
- [ ] **V18 部署、备份与灾备演练** — 进行中；依赖：V09、V11
- [x] **V19 可执行工作流与模板** — 已验收；依赖：V03、V06、V09；完成：2026-11-21
- [ ] **V20 视频编辑与分镜创作** — 待外部条件；依赖：V07、V08、V19
- [ ] **V21 协作与专项创作扩展** — 进行中；依赖：V08、V11、V19
- [ ] **V22 公开收费发布验收** — 进行中；依赖：V07、V08、V10、V13、V14、V15、V17、V18
- [x] **V23 工作台四语与首次访问地区识别** — 已验收；依赖：V00；完成：2026-11-22

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

证据：[首批验收证据](../changes/2026-11-13-001-foundation.md) · [工作台截图](../wiki/assets/workbench-phase1.jpg)

台账：[2026-11-13-001-foundation](../changes/2026-11-13-001-foundation.md) · [2026-11-13-002-go-core](../changes/2026-11-13-002-go-core.md) · [2026-11-13-003-workbench](../changes/2026-11-13-003-workbench.md)

### V01 账户、持久任务与实际媒体协议

状态：**已验收**。模块：H01、H02、H03、H04、H07、H08、H09。依赖：V00。

v0.2单写者本地验收；供应商付费实测单列V15。

验收条件：

- PG账户与项目资源隔离
- 持久幂等、检查点、已知任务续查和未知受理不重提
- 私有资产导入/鉴权与恢复测试、桌面和手机尺寸验收

验证记录：

- 通过：v0.2历史验收；`npm run check; npm run test:db; npm run smoke`。Go31函数及子用例、前端36项、真实PG与浏览器重启验收。真实网关调用未验收，单列V15。

证据：[集成验收证据](../changes/2026-11-13-007-integration-database.md) · [仅本地demo性能基线](../wiki/assets/performance-local-2026-11-13.json)

台账：[2026-11-13-004-persistence-auth](../changes/2026-11-13-004-persistence-auth.md) · [2026-11-13-005-media-gateway](../changes/2026-11-13-005-media-gateway.md) · [2026-11-13-006-accounts-projects](../changes/2026-11-13-006-accounts-projects.md) · [2026-11-13-007-integration-database](../changes/2026-11-13-007-integration-database.md) · [2026-11-13-008-performance-baseline](../changes/2026-11-13-008-performance-baseline.md)

### V02 横向模块与纵向批次治理

状态：**已验收**。模块：H09。依赖：V01。

生成唯一任务数据源和可勾选TODO，之后逐批验收并同步完成标记。

验收条件：

- 所有剩余范围映射到模块与可验收批次
- 完成批次必须有测试证据、台账和完成日期
- 依赖、编号、证据路径和生成文档漂移自动检查

验证记录：

- 通过：治理校验；`npm run test:todo; npm run check:todo; npm run check:changes`。8项治理单测、生成一致性与27份文档/9份台账同步检查通过。完成标记后重新生成并校验。
- 通过：文档与SQL统一目录增量；`npm run todo:sync; npm run check; npm run test:db; npm run check:changes`。完整TODO迁入docs，根入口同源生成；10治理测试、全量check/PG238.022s通过。17SQL共55486字节SHA256不变；三HTML字节不变且链接有效。命令由隔离环境wrapper执行，真实渠道未调用，详见063/064/067。

证据：[8项治理边界测试](../../scripts/tasks.test.mjs) · [批次维护规则](README.md) · [统一docs/db规则、旧路径映射与双入口治理](../wiki/repository-layout.md) · [根任务导航显示已验证子项与最新台账](../changes/2026-11-22-230-todo-entry-acceptance.md)

台账：[2026-11-13-009-batch-tracking](../changes/2026-11-13-009-batch-tracking.md) · [2026-11-14-063-document-layout](../changes/2026-11-14-063-document-layout.md) · [2026-11-14-064-sql-layout](../changes/2026-11-14-064-sql-layout.md) · [2026-11-22-230-todo-entry-acceptance](../changes/2026-11-22-230-todo-entry-acceptance.md)

### V03 项目云端草稿与版本恢复

状态：**已验收**。模块：H01、H02、H08、H09。依赖：V01、V02、V04。

把PG模式的本地草稿升级为项目私有持久草稿；不承诺DAG执行。

验收条件：

- 保存/读取/历史/恢复全链路通过
- 乐观并发冲突保留编辑内容，网络不明保留原幂等键
- 跨项目/账户隔离，重启恢复，memory模式明确本地边界

验证记录：

- 通过：草稿保存、恢复与保护联合验收；`npm run test:db; npm run check; npm run smoke`。真实PG双连接CAS/幂等/权限/重启/历史防改删/审计失败回滚通过；57项前端测试及双窗口冲突、恢复为新版、401重登保留编辑、离开保护与390x844手机页面实测通过。

证据：[草稿契约与边界](../wiki/drafts.md) · [真实PG并发、幂等、权限验证](../../internal/studio/drafts_test.go) · [草稿状态与恢复仓测试](../../apps/workbench/src/draftState.test.ts) · [保存后的云端草稿](../wiki/assets/drafts-phase3.jpg) · [联合验收证据](../changes/2026-11-13-013-batch-integration.md)

台账：[2026-11-13-010-project-drafts](../changes/2026-11-13-010-project-drafts.md) · [2026-11-13-011-drafts-activity-workbench](../changes/2026-11-13-011-drafts-activity-workbench.md) · [2026-11-13-013-batch-integration](../changes/2026-11-13-013-batch-integration.md)

### V04 关键业务审计与活动记录

状态：**已验收**。模块：H01、H02、H06、H08、H09。依赖：V01、V02。

记录项目、运行/任务受理、取消/核对等明确业务动作，提供归属隔离的只读活动记录。

验收条件：

- 关键业务状态与审计同事务提交
- 幂等重放不重复受理记录，审计不包含提示词、输出或凭据
- 游标分页、跨账户拒绝与UI活动记录验证；覆盖范围明确

验证记录：

- 通过：审计事务与活动页联合验收；`npm run test:db; npm run check; npm run smoke`。真实PG全量11.130秒通过，故障事务/候选缓存/无广播、复合游标/权限/去重通过；桌面及390x844手机活动页与服务重启留存实测通过。

证据：[审计覆盖与脱敏边界](../wiki/audit.md) · [权限、分页和事务故障验证](../../internal/studio/audit_test.go) · [真实活动页面](../wiki/assets/activity-phase3.jpg) · [联合验收证据](../changes/2026-11-13-013-batch-integration.md)

台账：[2026-11-13-011-drafts-activity-workbench](../changes/2026-11-13-011-drafts-activity-workbench.md) · [2026-11-13-012-transactional-audit](../changes/2026-11-13-012-transactional-audit.md) · [2026-11-13-013-batch-integration](../changes/2026-11-13-013-batch-integration.md)

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

证据：[持久准入与幂等规则](../wiki/admission.md) · [探针、指标及缓存边界](../wiki/observability.md) · [独立运行冒烟实测](../wiki/assets/runtime-smoke-v0.4-2026-11-13.json) · [v0.4本地性能基线](../wiki/assets/performance-local-v0.4-2026-11-13.json) · [真实PG容量界面](../wiki/assets/runtime-phase4.jpg) · [满载拒绝与冷却界面](../wiki/assets/admission-phase4.jpg) · [390x844容量布局](../wiki/assets/mobile-runtime-phase4.jpg) · [联合验收与实际限制](../changes/2026-11-13-017-runtime-integration.md)

台账：[2026-11-13-014-resource-admission](../changes/2026-11-13-014-resource-admission.md) · [2026-11-13-015-observability](../changes/2026-11-13-015-observability.md) · [2026-11-13-016-runtime-workbench](../changes/2026-11-13-016-runtime-workbench.md) · [2026-11-13-017-runtime-integration](../changes/2026-11-13-017-runtime-integration.md)

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

证据：[配置、能力、历史快照与兼容边界](../wiki/models.md) · [目录、精确参数与真实PG升级/恢复](../../internal/studio/catalog_test.go) · [阻止旧二进制写丢快照的格式版本](../../db/migrations/005_model_snapshots.sql) · [独立目录进程验收](../../scripts/catalog-smoke.mjs) · [最终二进制目录冒烟证据](../wiki/assets/catalog-smoke-v0.5-2026-11-13.json) · [真实浏览器和PG联合核对](../wiki/assets/browser-models-v0.5-2026-11-13.json) · [模型规格桌面界面](../wiki/assets/models-phase5.jpg) · [390x844模型设置](../wiki/assets/mobile-models-phase5.jpg) · [全部验证结果与边界](../changes/2026-11-13-021-model-integration.md)

台账：[2026-11-13-018-model-catalog](../changes/2026-11-13-018-model-catalog.md) · [2026-11-13-019-model-provider](../changes/2026-11-13-019-model-provider.md) · [2026-11-13-020-model-workbench](../changes/2026-11-13-020-model-workbench.md) · [2026-11-13-021-model-integration](../changes/2026-11-13-021-model-integration.md)

### V07 参考素材上传与图片编辑

状态：**待外部条件**。模块：H01、H03、H04、H07、H08、H09。依赖：V06。

上传私有参考素材，完整贯通文档中的图片编辑路径。

验收条件：

- 限额、真实格式、文件大小、归属与引用校验
- 编辑提交原始请求持久化与幂等正确
- 上传→编辑→查询→导入→下载端到端fixture和真实验收

外部条件：真实供应商验收按用户安排保留。打开“图片编辑配置”导出模型条目或独立目录示例，在服务器配置开通的编辑模型、密钥和结果域名，重启后刷新公开目录，再由用户受控上传、编辑并下载结果。

验证记录：

- 通过：私有参考素材与权限边界；`npm run check; npm run test:db`。106个Go测试函数及子例、真实PG25.156秒、前端100项、治理8项、vet/TS/生产构建通过；格式、限额、权限、事务和006升级已验收。
- 通过：编辑协议、重启与结果交付；`npm run smoke; npm run smoke:runtime; npm run smoke:catalog; npm run smoke:references; npm run perf:local; 浏览器验收`。四套隔离冒烟、实际multipart HTTP fixture与PG重启通过；修复停机Query误写FAILED。桌面/390x844验证上传、排序编辑、复用、项目隔离和旧数据保留；demo不能替代真实效果。
- 通过：真实渠道配置准备页；`工作台图片编辑配置单测、浏览器下载与服务端目录加载`。17 项配置专测、三种 Chromium 下载及 390px 布局通过，集成人再次将实际下载的目录加载进独立 Go 服务，读取公开模型能力与 1024x1024 规格，未提交生成。
- 待条件：真实供应商端到端验收；`配置服务端真实模型/凭据后执行受控上传→编辑→查询→导入→下载`。当前没有真实服务端凭据及真实生成验收记录；本地fixture不能替代此条。

证据：[参考素材隔离冒烟](../wiki/assets/reference-smoke-v0.6-2026-11-13.json) · [桌面手机与数据库验收](../wiki/assets/references-browser-v0.6-2026-11-13.json) · [图片编辑工作台](../wiki/assets/references-phase6.jpg) · [390x844参考素材](../wiki/assets/mobile-references-phase6.jpg) · [同版本本地性能基线](../wiki/assets/performance-local-v0.6-2026-11-13.json) · [联合验证与真实待条件](../changes/2026-11-13-025-reference-integration.md) · [配置页下载与实际目录加载](../changes/2026-11-21-110-image-edit-setup.md)

台账：[2026-11-13-022-reference-assets](../changes/2026-11-13-022-reference-assets.md) · [2026-11-13-023-media-edit-provider](../changes/2026-11-13-023-media-edit-provider.md) · [2026-11-13-024-reference-workbench](../changes/2026-11-13-024-reference-workbench.md) · [2026-11-13-025-reference-integration](../changes/2026-11-13-025-reference-integration.md) · [2026-11-21-110-image-edit-setup](../changes/2026-11-21-110-image-edit-setup.md)

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

证据：[目录、版本、权限及恢复契约](../wiki/assets.md) · [真实S3兼容配置、迁移与限制](../wiki/object-storage.md) · [双锁、数据库归属、可达性与计划复核](../wiki/storage-maintenance.md) · [HTTP与真实PG并发/回填/事务测试](../../internal/studio/asset_library_test.go) · [跨进程锁、错库、计划与临时目录apply](../../internal/studio/storage_maintenance_test.go) · [真实SeaweedFS及迁移CLI证据](../wiki/assets/object-storage-v0.7-2026-11-13.json) · [独立资产库二进制冒烟](../wiki/assets/library-smoke-v0.7-2026-11-13.json) · [升级/重启与真实页面验证](../wiki/assets/library-browser-v0.7-2026-11-13.json) · [已持久保存的素材详情](../wiki/assets/library-detail-phase7.png) · [390x844手机素材详情](../wiki/assets/mobile-library-phase7.png) · [根独立集成、问题修复及下一步](../changes/2026-11-13-030-asset-library-integration.md)

台账：[2026-11-13-026-asset-library](../changes/2026-11-13-026-asset-library.md) · [2026-11-13-027-object-storage](../changes/2026-11-13-027-object-storage.md) · [2026-11-13-028-library-workbench](../changes/2026-11-13-028-library-workbench.md) · [2026-11-13-029-storage-maintenance](../changes/2026-11-13-029-storage-maintenance.md) · [2026-11-13-030-asset-library-integration](../changes/2026-11-13-030-asset-library-integration.md)

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

证据：[事务、提交顺序、确认丢失与回放水位](../../internal/studio/outbox_test.go) · [真实连接租约/失效隔离和背压](../../internal/studio/task_leases_test.go) · [真实进程Kill、一次上游POST、原键与SSE恢复](../../internal/studio/durable_process_test.go) · [有界恢复、尾部可达、重试及静默收敛](../../apps/workbench/src/durableEvents.test.ts) · [持久通知协议与耐久边界](../wiki/events.md) · [实际worker与租约配置](../wiki/scheduler.md) · [事务顺序与单控制写者决策](../wiki/decisions/0006-durable-events-and-task-leases.md) · [真实页面与升级记录](../wiki/assets/events-browser-v0.8-2026-11-13.json) · [跨窗口更新保留创作输入](../wiki/assets/events-phase8.png) · [未保存素材编辑保护](../wiki/assets/events-dirty-phase8.png) · [390x844布局](../wiki/assets/mobile-events-phase8.png) · [最终二进制本机回归口径](../wiki/assets/performance-local-v0.8-2026-11-13.json) · [隔离限额/可观测回归](../wiki/assets/runtime-smoke-v0.8-2026-11-13.json) · [模型目录回归](../wiki/assets/catalog-smoke-v0.8-2026-11-13.json) · [参考素材编辑回归](../wiki/assets/reference-smoke-v0.8-2026-11-13.json) · [资产生命周期回归](../wiki/assets/library-smoke-v0.8-2026-11-13.json) · [根独立验收与实际限制](../changes/2026-11-13-034-durable-execution-integration.md)

台账：[2026-11-13-031-durable-events](../changes/2026-11-13-031-durable-events.md) · [2026-11-13-032-task-leases](../changes/2026-11-13-032-task-leases.md) · [2026-11-13-033-durable-events-client](../changes/2026-11-13-033-durable-events-client.md) · [2026-11-13-034-durable-execution-integration](../changes/2026-11-13-034-durable-execution-integration.md)

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

证据：[会话/分支/上下文与PG事务回归](../../internal/studio/conversation_test.go) · [实际进程Kill及部分回答恢复](../../internal/studio/durable_process_test.go) · [独立真实HTTP上下文验收脚本](../../scripts/conversation-smoke.mjs) · [冻结接口与实施边界](v10-contract.md) · [会话领域与010迁移说明](../wiki/conversation.md) · [结构化Provider与预算配置](../wiki/chat-provider.md) · [0.9.0 OpenAPI契约](../api/openapi.yaml) · [五组上下文与分支fixture报告](../wiki/assets/conversation-smoke-v0.9-2026-11-13.json) · [桌面/双窗口/手机浏览器验收](../wiki/assets/conversations-browser-v0.9-2026-11-13.json) · [本地基线与容量限制](../wiki/assets/performance-local-v0.9-2026-11-13.json) · [桌面聊天工作台截图](../wiki/assets/conversations-phase9.jpg) · [390x844手机会话截图](../wiki/assets/mobile-conversations-phase9.jpg)

台账：[2026-11-13-035-conversation-core](../changes/2026-11-13-035-conversation-core.md) · [2026-11-13-036-chat-provider](../changes/2026-11-13-036-chat-provider.md) · [2026-11-13-037-conversations-workbench](../changes/2026-11-13-037-conversations-workbench.md) · [2026-11-13-038-conversation-integration](../changes/2026-11-13-038-conversation-integration.md)

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

证据：[账户安全与011迁移边界](../wiki/account-security.md) · [TLS加密投递与真实渠道恢复条件](../wiki/mail-delivery.md) · [0.10.0 API契约](../api/openapi.yaml) · [桌面与手机浏览器验收](../wiki/assets/account-security-browser-v0.10-2026-11-13.json) · [升级前dump与011后只读数据比对](../wiki/assets/account-security-upgrade-v0.10-2026-11-13.json) · [隔离memory/demo性能口径](../wiki/assets/performance-local-v0.10-2026-11-13.json) · [根全量与本地TLS HTTP验收](../changes/2026-11-13-042-account-security-integration.md)

台账：[2026-11-13-039-account-security](../changes/2026-11-13-039-account-security.md) · [2026-11-13-040-security-mail](../changes/2026-11-13-040-security-mail.md) · [2026-11-13-041-account-security-workbench](../changes/2026-11-13-041-account-security-workbench.md) · [2026-11-13-042-account-security-integration](../changes/2026-11-13-042-account-security-integration.md)

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

证据：[整数、双分录、并发与提交确认丢失测试](../../internal/studio/ledger_test.go) · [报价预检、作用域权限与分页测试](../../internal/studio/billing_http_test.go) · [离线预演、计划摘要、writer锁与收据重放](../../internal/studio/billing_admin_test.go) · [实际HTTP usage到PG结算与REVIEW](../../internal/studio/billing_gateway_test.go) · [实际进程Kill恢复及原键不重发](../../internal/studio/billing_process_test.go) · [非法参考ID不触发PG熔断](../../internal/studio/billing_security_test.go) · [金额、报价作用域、原请求及结算版本保护](../../apps/workbench/src/billing.test.tsx) · [冻结接口与计量契约](v12-contract.md) · [账本领域与事务约束](../wiki/ledger.md) · [配置、012升级与离线处置](../wiki/billing-operations.md) · [钱包和报价交互规则](../wiki/billing-workbench.md) · [0.11.0 API契约](../api/openapi.yaml) · [011至012联备及旧数据比对](../wiki/assets/billing-upgrade-v0.11-2026-11-13.json) · [PG/demo桌面手机报价结算验收](../wiki/assets/billing-browser-v0.11-2026-11-14.json) · [关闭权益的memory/demo基线及容量边界](../wiki/assets/performance-local-v0.11-2026-11-14.json) · [桌面钱包与三单结算截图](../wiki/assets/billing-phase11.jpg) · [390x844报价确认截图](../wiki/assets/mobile-billing-quote-phase11.jpg)

台账：[2026-11-13-043-credit-ledger](../changes/2026-11-13-043-credit-ledger.md) · [2026-11-13-044-provider-usage](../changes/2026-11-13-044-provider-usage.md) · [2026-11-13-045-billing-workbench](../changes/2026-11-13-045-billing-workbench.md) · [2026-11-13-046-billing-integration](../changes/2026-11-13-046-billing-integration.md)

### V13 订单、支付与退款

状态：**待外部条件**。模块：H01、H05、H06、H08。依赖：V11、V12。

支付与权益解耦；真实商户接入需要配置，fixture通过不等于真实收款完成。

验收条件：

- 订单状态机、签名回调、防重放与重复支付处理
- 退款回调/超时核实、权益原子入账与审计
- 商户沙箱及真实受控支付退款验收，用户可见订单记录

外部条件：用户已明确暂缓正式支付接入。恢复时需先选定渠道、测试商户、验签与权威查询协议、退款和已履约权益策略及受控测试窗口。已有本地账务验收保留。

验证记录：

- 通过：V13-A 本人订单意向、冻结商品条款与原键恢复；`033 PostgreSQL、两账户 Chromium、关闭目录后联备恢复、零资金及权益写入`。集成人对冻结的 033 运行 15 项订单与个人资料专项，共 62.099 秒。两账户 Chromium 7.58 秒，关闭目录后的新库恢复 7.53 秒；真实数据库中未产生权益分录或媒体任务。 固定 035 完整 PG 通过，studio 1670.146s。
- 通过：V13-B1 单工作区订单与归一商户资料的离线只读比对；`Go 分类与 CLI、实际 PG 跨工作区与持锁期间两次比对、订单/原收据/钱包和分录保持原值`。034 实际 CLI 与只读数据库快照 3.15 秒，核对交易冲突、退款累计下界、纳秒精度与范围；订单、原收据及七张权益表摘要相同。 固定 035 完整 PG 通过，studio 1670.146s。 035 预先登记合成收退款，编译出的 CLI 在持锁期间核对后全部18表逐行摘要一致（3.12秒）。
- 通过：V13-B2 认证后事务、重复付款通知、退款事实与唯一权益分录；`035 PG 迁移与并发/确认丢失、两账户 Chromium、原回执及联备恢复`。真实本机 PG、Chromium 及恢复专项通过。20连接去重、退款乱序及已花/冻结保护、提交确认丢失、升级、两账户与新库恢复均已核验。含剪辑17项专项82.279秒；035累计 1670.146s。真实渠道输入待选定和验收。 035 新库重放原退款后18表及本人投影保持一致（12.91秒）。
- 待条件：正式商户验签、防重放、权威查询与退款权益核对；`选定渠道的正式签名协议、权威交易查询、退款与权益业务规则及受控集成`。035 已完成认证后事务与本机账务核对。正式商户及测试环境、履约和部分退款规则尚待确定，当前没有公开的写入通知入口。
- 待条件：实际商户沙箱与受控支付退款验收；`指定商户沙箱、真实授权回调、交易查询、账单和受控收退款`。需要用户指定商户渠道、受控测试商户及环境。真实资金操作需独立明确授权。

证据：[受信商品目录、货币与CREDIT单位及用户入口](../wiki/decisions/0033-order-intents.md) · [本人订单意向及当前渠道边界](../changes/2026-11-22-160-order-intent-integration.md) · [受信订单快照与外部材料的本地只读对账边界](../changes/2026-11-22-173-offline-order-reconciliation.md) · [资金事实、权益入账和正式渠道边界](../wiki/decisions/0035-order-payments.md) · [真实账户状态页及恢复证据](../changes/2026-11-22-177-order-payment-integration.md) · [按用户选择暂缓正式支付接入](../changes/2026-11-22-231-payment-channel-deferred.md)

台账：[2026-11-22-157-order-intent-store](../changes/2026-11-22-157-order-intent-store.md) · [2026-11-22-158-order-intent-catalog-http](../changes/2026-11-22-158-order-intent-catalog-http.md) · [2026-11-22-159-order-intent-workbench](../changes/2026-11-22-159-order-intent-workbench.md) · [2026-11-22-160-order-intent-integration](../changes/2026-11-22-160-order-intent-integration.md) · [2026-11-22-173-offline-order-reconciliation](../changes/2026-11-22-173-offline-order-reconciliation.md) · [2026-11-22-174-order-payment-store](../changes/2026-11-22-174-order-payment-store.md) · [2026-11-22-175-order-payment-status-http](../changes/2026-11-22-175-order-payment-status-http.md) · [2026-11-22-176-order-payment-workbench](../changes/2026-11-22-176-order-payment-workbench.md) · [2026-11-22-177-order-payment-integration](../changes/2026-11-22-177-order-payment-integration.md) · [2026-11-22-231-payment-channel-deferred](../changes/2026-11-22-231-payment-channel-deferred.md)

### V14 审核、风控与运营控制台

状态：**进行中**。模块：H01、H03、H04、H05、H06、H07、H08、H09。依赖：V04、V11、V12。

V14-A 运营核实、B1/B2 文本与媒体审核、C1 工作区限制、C2 账户与内容申诉、C3 额度风控均已本地验收。真实审核渠道与 V11 真实邮件依赖继续待验。

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
- 通过：V14-B2 图片与视频审核本地闭环；`npm run check; npm run test:db; TestPostgresMediaModeration*; 六套smoke; perf:local; CUA桌面/手机; OpenAPI引用; check:todo/docs/changes`。2026-11-14本地通过：270前端/26文件、8治理、PG全量192.482s；新增PARTIAL定向1.675s。完整字节/候选/整组隔离、COMMIT ACK丢失、租约恢复、上传/所有下载门禁、GC、016联备保旧、浏览器PNG/MP4/参考编辑/手机通过；白屏和非法PG输入已修。真实渠道与V14-C另验，整批不提前完成。
- 待条件：V14-B2 真实多模态审核渠道；`选型后完成media-v1适配与真实图片/视频、故障及策略验收`。用户暂未选审核商；需要实际接口、服务端私有配置和受控内容，fixture不替代真实渠道。
- 通过：V14-C1 工作区新生成限制与复核本地闭环；`npm run check; npm run test:db; C1真实PG专项; 六套smoke; perf:local; CUA桌面/390手机; 017联备; OpenAPI及todo/docs/changes`。2026-11-14本地完成：297前端/28文件、8治理、PG全量282.020s；原键与三入口门禁、首次缺行竞争、scope/密码/预演/ACK恢复、017联备保旧；浏览器限制→申请→解除答复→手动新生成及手机通过。主JS494.93kB按需分包。C2/C3/真实渠道和完整V14另验。
- 通过：V14-C2 账户及内容申诉证据与处置；`独立证据、权限与可执行决议设计及端到端验收`。026 内容申诉及 027 账户复核分别实测，新旧申请和原决定均可审计。029 最终累计全量 PostgreSQL 1326.832 秒，无失败或跳过；真实账户限制、财务原冻结与审批权限均覆盖。
- 通过：V14-C3 额度与自动风控策略；`冻结额度/频率策略、原键与并发边界、误伤恢复及可观测验收`。028 专项 48.091 秒；原报价和单次核销的浏览器诊断连续三次 46.461 秒。029 全量累计 1326.832 秒无失败或跳过，其中同一已报价请求的复核、显式确认和唯一 0.2 CREDIT 结算用时 25.19 秒。
- 待条件：完整V14依赖放行；`V11真实邮件渠道受控验收与完整V14复核`。用户安排真实邮件后续配置；保留V11依赖，不能用本地fixture勾选整批。
- 通过：V14-C2a 已执行输出内容申诉、独立审议与财务工单；`真实 PG/三账户 Chromium/026 升级及联备、全量回归`。2026-11-22 本地通过。原冻结 200000 微 CREDIT 实际释放，原审核继续隔离；专项 46.659 秒，三账户 Chromium 和停写联备 29.11 秒。026 全量 PG 1110.149 秒通过，370 项前端、生产构建及六套冒烟通过。
- 通过：V14-C2b 账户限制、本人复核与独立目标授权；`027 PostgreSQL、真实浏览器、受理与会话边界、备份恢复、全量回归`。两次独立浏览器覆盖限制、撤销会话和原草稿恢复。029 全量中再次通过 20.29 秒，包含升级与私有联备。027 早期全量仅旧恢复探针超时，保留原失败记录。

证据：[已实施接口与子批次边界](v14a-contract.md) · [scoped权限、密码与审计回滚](../../internal/studio/operations_access_test.go) · [停机授权、预演、稳定键、撤销与版本容量](../../internal/studio/operations_admin_test.go) · [工单、CAS/计划、事务处置与租约隔离](../../internal/studio/operations_cases_test.go) · [权限负例、严格正文、游标/Scope及恢复](../../internal/studio/operations_http_test.go) · [真实PG提交确认丢失、App重启及唯一收据](../../internal/studio/operations_integration_test.go) · [15项前端协议/隔离/未知恢复与密码验证](../../apps/workbench/src/operations.test.tsx) · [联合命令/版本摘要/OpenAPI与测试汇总](../wiki/assets/operations-validation-v0.12-2026-11-14.json) · [真实Go+PG demo桌面/手机交互验收](../wiki/assets/operations-browser-v0.12-2026-11-14.json) · [012到014前联备及旧业务比对](../wiki/assets/operations-upgrade-v0.12-2026-11-14.json) · [隔离demo回归，非生产容量](../wiki/assets/performance-local-v0.12-2026-11-14.json) · [文本本地闭环已验收；真实渠道、多模态及后续批次分开](v14b1-contract.md) · [规则策略冻结、严格配置与PG不可变决策](../../internal/studio/moderation_test.go) · [私有HTTP契约、异常响应/跳转/超时与并发边界](../../internal/studio/moderation_http_test.go) · [输入锁外、无副作用、候选隔离、上下文与取消](../../internal/studio/moderation_execution_test.go) · [PG准入、权益、回滚及关闭策略后的隔离恢复](../../internal/studio/moderation_integration_test.go) · [真实PG COMMIT确认丢失：ALLOW/BLOCK恢复原决策，原键不重发](../../internal/studio/moderation_commit_test.go) · [20项前端审核语义、乱序门禁与受限轮后普通提问](../../apps/workbench/src/moderation.test.tsx) · [联合命令与最终二进制摘要](../wiki/assets/moderation-validation-v0.13-2026-11-14.json) · [桌面/手机输入拒绝、输出隔离、正常新问题与刷新](../wiki/assets/moderation-browser-v0.13-2026-11-14.json) · [014到015前联备及旧业务保持，无自动批准](../wiki/assets/moderation-upgrade-v0.13-2026-11-14.json) · [隔离memory/demo回归，不是审核或PG容量](../wiki/assets/performance-local-v0.13-2026-11-14.json) · [B2本地闭环范围、整组交付与真实渠道独立验收](v14b2-contract.md) · [独立策略与完整字节](../../internal/studio/media_moderation_test.go) · [私有流式协议与失败/完整字节边界](../../internal/studio/media_moderation_http_test.go) · [本地同FD与私有S3实际读取](../../internal/studio/media_moderation_store_test.go) · [混合整组、PARTIAL、输入/故障/权益/租约/恢复](../../internal/studio/media_moderation_execution_test.go) · [真实PG candidate/ALLOW/BLOCK COMMIT ACK丢失](../../internal/studio/media_moderation_commit_test.go) · [参考登记事务、下载门禁、GC及非法ID/UTF8不熔断](../../internal/studio/media_moderation_access_test.go) · [27项媒体状态与安全投影](../../apps/workbench/src/mediaModeration.test.tsx) · [6项实际App渲染，空/受限/上传中参考不白屏](../../apps/workbench/src/creationRender.test.tsx) · [全量命令与最终二进制摘要](../wiki/assets/media-moderation-validation-v0.14-2026-11-14.json) · [桌面/手机/实际MP4播放及修复复验](../wiki/assets/media-moderation-browser-v0.14-2026-11-14.json) · [015→016联备升级与旧数据保留](../wiki/assets/media-moderation-upgrade-v0.14-2026-11-14.json) · [memory/demo回归，非审核或生产容量](../wiki/assets/performance-local-v0.14-2026-11-14.json) · [C1已验收本地范围、依赖与真实渠道边界](v14c1-contract.md) · [状态、容量、申诉与回执](../wiki/generation-controls.md) · [原键、稳定工作区锁与角色](../wiki/generation-admission.md) · [前端原请求和异步读取保护](../wiki/generation-controls-workbench.md) · [限制、内容审核与权益边界](../wiki/decisions/0013-workspace-generation-controls.md) · [HTTP全链、ACK和固定指标路由](../../internal/studio/generation_control_http_test.go) · [SQL状态、不可变证据与双向锁](../../internal/studio/generation_control_test.go) · [预演、维持/解除与审计原子性](../../internal/studio/generation_control_operations_test.go) · [三入口原键、锁外重核和旧任务](../../internal/studio/generation_admission_test.go) · [真实ReactDOM生命周期测试](../../apps/workbench/src/generationLifecycle.test.tsx) · [本批最终验证及失败修正记录](../wiki/assets/generation-controls-validation-v0.15-2026-11-14.json) · [独立角色桌面/390手机完整链](../wiki/assets/generation-controls-browser-v0.15-2026-11-14.json) · [016→017联备与旧数据比对](../wiki/assets/generation-controls-upgrade-v0.15-2026-11-14.json) · [公开答复与权限恢复](../wiki/assets/generation-reply-phase15.jpg) · [最终构建本地回归，非生产容量](../wiki/assets/performance-local-v0.15-2026-11-14.json) · [内容申诉与账户限制的独立验收条件](v14c2-contract.md) · [三账户真实浏览器、原决定、原冻结及联备](../../internal/studio/content_appeal_integration_test.go) · [申诉与运营工单、真实分录的关系](../wiki/decisions/0026-content-appeal.md) · [目标账户授权、原会话及限制轮次边界](../wiki/decisions/0027-account-governance.md) · [额度风控与单次原报价复核的验收边界](v14c3-contract.md) · [账户切换后的首个草稿输入](../changes/2026-11-22-221-account-composer-scope.md) · [对应固定制品在隔离数据库的串行验收进度。](../changes/2026-11-22-227-local-acceptance.md)

台账：[2026-11-14-047-operations-access](../changes/2026-11-14-047-operations-access.md) · [2026-11-14-048-operations-cases](../changes/2026-11-14-048-operations-cases.md) · [2026-11-14-049-operations-workbench](../changes/2026-11-14-049-operations-workbench.md) · [2026-11-14-050-operations-integration](../changes/2026-11-14-050-operations-integration.md) · [2026-11-14-051-text-moderation-engine](../changes/2026-11-14-051-text-moderation-engine.md) · [2026-11-14-052-moderation-execution](../changes/2026-11-14-052-moderation-execution.md) · [2026-11-14-053-moderation-workbench](../changes/2026-11-14-053-moderation-workbench.md) · [2026-11-14-054-text-review-integration](../changes/2026-11-14-054-text-review-integration.md) · [2026-11-14-055-media-moderation-engine](../changes/2026-11-14-055-media-moderation-engine.md) · [2026-11-14-056-media-moderation-execution](../changes/2026-11-14-056-media-moderation-execution.md) · [2026-11-14-057-media-moderation-workbench](../changes/2026-11-14-057-media-moderation-workbench.md) · [2026-11-14-058-media-review-integration](../changes/2026-11-14-058-media-review-integration.md) · [2026-11-14-062-generation-controls-integration](../changes/2026-11-14-062-generation-controls-integration.md) · [2026-11-14-059-generation-controls](../changes/2026-11-14-059-generation-controls.md) · [2026-11-14-060-generation-admission](../changes/2026-11-14-060-generation-admission.md) · [2026-11-14-061-generation-controls-workbench](../changes/2026-11-14-061-generation-controls-workbench.md) · [2026-11-21-119-content-appeal-store](../changes/2026-11-21-119-content-appeal-store.md) · [2026-11-21-120-content-appeal-finance](../changes/2026-11-21-120-content-appeal-finance.md) · [2026-11-21-121-content-appeal-workbench](../changes/2026-11-21-121-content-appeal-workbench.md) · [2026-11-21-122-content-appeal-integration](../changes/2026-11-21-122-content-appeal-integration.md) · [2026-11-22-123-account-restriction-store](../changes/2026-11-22-123-account-restriction-store.md) · [2026-11-22-124-account-governance-auth](../changes/2026-11-22-124-account-governance-auth.md) · [2026-11-22-125-account-governance-workbench](../changes/2026-11-22-125-account-governance-workbench.md) · [2026-11-22-126-account-governance-integration](../changes/2026-11-22-126-account-governance-integration.md) · [2026-11-22-127-credit-risk-store](../changes/2026-11-22-127-credit-risk-store.md) · [2026-11-22-128-credit-risk-review](../changes/2026-11-22-128-credit-risk-review.md) · [2026-11-22-129-credit-risk-workbench](../changes/2026-11-22-129-credit-risk-workbench.md) · [2026-11-22-130-credit-risk-integration](../changes/2026-11-22-130-credit-risk-integration.md) · [2026-11-22-165-content-appeal-fixture-completion](../changes/2026-11-22-165-content-appeal-fixture-completion.md) · [2026-11-22-221-account-composer-scope](../changes/2026-11-22-221-account-composer-scope.md) · [2026-11-22-227-local-acceptance](../changes/2026-11-22-227-local-acceptance.md)

### V15 真实网关与产物交付验收

状态：**待外部条件**。模块：H03、H04、H07。依赖：V01、V06。

使用用户实际已开通模型和服务端配置验收，自动测试不得发付费请求。

验收条件：

- 真实LLM、图片及视频生成/下载/播放
- 原凭据查询、429、超时与结果失效行为核对
- 记录模型参数、配额和证据，不保存密钥

外部条件：未提供本地服务端密钥、已开通模型参数和受控真实调用条件；按media-gateway Wiki配置后再做真实验收，不需要把密钥发到对话或提交仓库。

### V16 分布式流与配额公平调度

状态：**已验收**。模块：H01、H02、H03、H04、H07、H08、H09。依赖：V05、V09。

流、提交、轮询与私有导入按负载拆分，持久聊天日志、有效租约和有界容量保护。A1/A2、A3.1—A3.3、A4.1/A4.2、B1/B2 和 C 已按单控制写者契约本地验收。独立连接验收覆盖旧写者隔离；全站多实例、跨实例共享配额及生产容量另验。

验收条件：

- 连接池/worker和凭据/模型配额有界
- 流日志、快照、回放与fencing在故障下正确
- 任务公平性、供应限流、背压与降级实测

验证记录：

- 通过：V16-A1聊天公平调度与执行预算；`npm run check; npm run test:db; 六套隔离smoke`。301项前端/29文件、10项治理、Go vet/构建、PG全量238.022s、五项独立PG8.777s、六smoke及同构建性能回归通过。原键、取消真实退出、冻结模型别名与权益审核原子性已验；仅单控制写者，A2/A3/B/C仍待办。见067。
- 通过：V16-A2媒体提交与查询隔离；`npm run check; npm run test:db; 六套隔离smoke及媒体HTTP/PG专项`。独立submit/query+交付worker、PG连接与HTTP池；先预算再领取，取消/背压持槽。20函数核心PG37.077s、独立五函数及指标6.255s、全PG318.880s、301前端/10治理及六smoke通过。首轮超时和修正保留071；query仍含导入审核，单写者，A3/B/C待办。
- 通过：V16-A3.1查询到私有交付；`冻结描述/逐件收据、慢下载隔离、PG提交故障、GC/S3及联备恢复验收`。2026-11-14本地验收：018加密清单/计划ID/不可变完整收据、三lane、GC/维护栅栏；全PG301.345s、301前端/10治理、六smoke、真实本机S3、017→018及两种dump/restore通过。075保留首次失败和修正；S3未恢复远端服务/无VersionId，不代表云灾备。A3.2/A3.3/B/C待办。
- 通过：V16-A3.2首次审核分派；`READY/STARTED持久边界、确认不明/失租/取消和整组发布验收`。2026-11-15本地验收：019 READY/STARTED/FINISHED、四lane和共享2许可；全PG368.160s、301前端/10治理、六smoke、真实版本化S3/018→019和READY/STARTED联备通过。完整收据篡改零审核、原幂等/权益保持；079保留首次问题与修正。A3.3/B/C仍待办，不代表真实渠道/云灾备。
- 通过：V16-A3.3渠道公平与退避；`完整check、全PG含实际S3/联备、六smoke、180SQL计划/48完整预选/20万旧任务升级与治理`。2026-11-15本地通过：020四lane公平、冻结预算、冷却/GET探测与共享许可；全PG 443.228s、301前端/10治理、六smoke。100k+100k完整预选P95 16.302ms、20万旧任务迁移11.229s，非App重启/生产容量。首次缺行竞态、真实死锁、晚429等修正见083；A4/B/C继续。
- 通过：V16-A4.1分页恢复与启动背压；`完整New分页/私有状态批量读取、超过10000恢复事件、页边界Kill/ACK、分批旧lease撤销验收`。2026-11-21本地通过：12,002条恢复、9,999条预存事件一次完整New，合计22,001条投影，42.326s，二次启动0.688s；真实Kill/ACK/旧租约、回滚与队列无进展、密钥、冻结模式与跨页权益通过。全PG含本机版本化S3与联备 540.412s、普通check及六smoke通过。A4.2缓存另验。
- 通过：V16-A4.2媒体受理与冷读首项；`双工作区512条RUNNING原handle、生成/图片编辑、权限/额度/原键、PG冷读/取消/领取与内存上限`。2026-11-21本地通过090：基线503，修复后两种操作202，业务额度429、越权404，原键同ID与真实PG领取通过；普通check、全PG和六smoke通过。整体A4.2验收另见094。
- 通过：V16-A4.2PG有界缓存与完整启动容量；`100k活跃+100k历史完整New、峰值缓存/字节、跨512历史受理和pin/冷读/权限/取消/SSE验收`。2026-11-21 本地验收 091—094。10 万活跃加 10 万终态完整 New 专项 7.913s，全量复跑 5.758s，Run/Task 启动缓存各 512 条、项目 0、外发 0 次。专项覆盖取消后在途 I/O、活动 SSE、冷热运营补读、工作区项目名额与 22,001 条事件投影。全量 PG（含本机版本化 S3/联备）586.152s，通过普通 check 与六套 smoke。
- 通过：V16-B1单控制写者聊天增量持久化与断线回放；`021升级与精确栅栏、受理/执行/增量/终态与审核、窗口和重连、真实Kill与提交回执丢失、联备、全PG与六套冒烟`。2026-11-21 本地验收，021 单控制写者、新 Run 逐增量先提交、256 条/1 MiB 窗口、每页32条与审核门禁。升级、旧写者拒绝、真实 Kill/提交回执丢失与备份恢复、原键零重发均通过。最终全 PG 571.526 秒，305 项前端测试、Go 检查与六套冒烟通过。见099。
- 通过：V16-B2执行者租约撤销与迟到写入隔离；`go test ./internal/studio -run 'TestPostgresRunLease|TestPostgresRunStreamBackupRestore|TestPostgresDeliveryOfflineToolsRequireExact022'；npm run test:db、check 及六套冒烟`。2026-11-21 本地验收。独立 PG 连接竞争、到期/撤销、强杀、首次领取提交回执丢失、晚到审核及终态锁等待等 22 项组合通过，62.704 秒。最终全量 studio 包 814.555 秒；305 项前端测试与六套冒烟通过。见 100—103。
- 通过：V16-C综合测量；`pwsh -File scripts/measure-mixed-workload.ps1 -Runs 3；启用 MIXED_WORKLOAD_TEST 的全量 PG 回归`。2026-11-21 本机单控制写者三轮独立进程通过，每轮 6 次聊天、6 次媒体生成 POST、7 次查询 GET 和 6 个受管 PG 会话。A 积压时 B 聊天 9.5—10.8 ms 入执行，同 handle 在 429 后延迟 3.028—3.486 秒再查。原键无重复外发，全量 PG 另复跑混合试具。见 104。

证据：[完整范围与A1实施契约，未验收部分单列](v16-contract.md) · [预算、轮转、原键与取消的当前实现](../wiki/execution-scheduler.md) · [选择、替代方案和测量口径](../wiki/decisions/0014-chat-execution-scheduling.md) · [轮转/取消/5000步不变量与HTTP池](../../internal/studio/execution_scheduler_test.go) · [真实HTTP/PG、模型别名、权益和故障](../../internal/studio/execution_integration_test.go) · [真实队列HTTP指标与不泄漏](../../internal/studio/chat_execution_observability_test.go) · [四项明确拒收与原未知保护](../../apps/workbench/src/executionFeedback.test.ts) · [联合验收及失败修正汇总](../wiki/assets/execution-validation-v0.16-2026-11-14.json) · [新workspace2预算下本地回归，非生产容量](../wiki/assets/performance-local-v0.16-2026-11-14.json) · [A2当前实施契约、文件归属与完整验收条件](v16a2-contract.md) · [A2联合验收、最终构建及失败修正](../wiki/assets/media-lanes-validation-v0.17-2026-11-14.json) · [同构建本地回归，非媒体吞吐或生产容量](../wiki/assets/performance-local-v0.17-2026-11-14.json) · [独立执行预算与PG连接](../wiki/media-execution-lanes.md) · [JSON/编辑提交与GET池隔离](../wiki/media-transport-lanes.md) · [选择、替代方案和阶段边界](../wiki/decisions/0015-media-execution-lanes.md) · [真实App/PG双通道、背压、重启与慢审核](../../internal/studio/media_lane_integration_test.go) · [真实HTTP阻塞、取消、连接复用与POST不重发](../../internal/studio/media_transport_lanes_test.go) · [真实执行观察与固定无私有标签](../../internal/studio/media_lane_observability_test.go) · [严格四MEDIA环境配置，review独立1—2](../../cmd/server/media_execution_config_test.go) · [已验收A3.1契约、恢复与联备边界](v16a31-contract.md) · [A3.1完整联合验证、首次失败修正及范围](../wiki/assets/media-delivery-validation-v0.18-2026-11-14.json) · [17SQL字节不变、新018摘要和API引用](../wiki/assets/media-delivery-schema-v0.18-2026-11-14.json) · [实际本机S3、停止验证及非云灾备边界](../wiki/assets/media-delivery-s3-v0.18-2026-11-14.json) · [同构建memory/demo回归，非交付吞吐](../wiki/assets/performance-local-v0.18-2026-11-14.json) · [018清单/收据/预算和恢复](../wiki/media-delivery.md) · [原生QueryStatus和真实local/S3字节验证](../wiki/media-delivery-provider.md) · [选择、替代项和GC/联备后果](../wiki/decisions/0016-durable-media-delivery.md) · [实际HTTP/PG、恢复篡改、慢审核、整组权益和S3](../../internal/studio/media_delivery_integration_test.go) · [真实dump/restore、私有目录/密钥和保留对象](../../internal/studio/media_delivery_restore_integration_test.go) · [017旧字节保留、018旧写者栅栏](../../internal/studio/media_delivery_upgrade_test.go) · [四COMMIT ACK、原子回滚、旧lease和不可变证据](../../internal/studio/media_delivery_core_test.go) · [计划/收据可达性、双锁和离线精确格式](../../internal/studio/storage_maintenance_delivery_test.go) · [A3.2已验收契约、共享许可与019恢复边界](v16a32-contract.md) · [已实施的首次审核持久证据与共享许可决策](../wiki/decisions/0017-media-review-dispatch.md) · [A3.2完整联合证据、首次失败修正及限制](../wiki/assets/media-review-dispatch-validation-v0.19-2026-11-15.json) · [原18SQL不变、新019与API引用校验](../wiki/assets/media-review-dispatch-schema-v0.19-2026-11-15.json) · [实际本机版本化S3及停止保留证据](../wiki/assets/media-review-dispatch-s3-v0.19-2026-11-15.json) · [同SHA本地demo回归，不代表审核吞吐](../wiki/assets/performance-local-v0.19-2026-11-15.json) · [十九格式、首次审核事务及恢复](../wiki/media-review-dispatch.md) · [共享原2许可与取消实际退出](../wiki/media-review-permits.md) · [独立PG/HTTP及019维护验收](../wiki/media-review-dispatch-validation.md) · [真实HTTP/读取/共享槽/重复Close等10新增函数](../../internal/studio/media_review_permit_test.go) · [READY/STARTED/ALLOWED真实ACK、实际回滚及背压](../../internal/studio/review_dispatch_commit_test.go) · [等待审核期间完整收据及缩略图篡改拒绝](../../internal/studio/review_dispatch_receipts_test.go) · [公平前的四lane隔离、输入共享、竞争、整组权益](../../internal/studio/media_review_dispatch_integration_test.go) · [018→019保旧/零回填与旧writer拒绝](../../internal/studio/media_review_dispatch_upgrade_test.go) · [READY/STARTED、local和真实版本化S3联备](../../internal/studio/media_review_dispatch_restore_test.go) · [019阶段证据和任务版本纳入GC精确计划](../../internal/studio/storage_maintenance_review_test.go) · [A3.3实施范围、事务/恢复、资源上限和完整验收条件](v16a33-contract.md) · [020投影、严格领取、冻结预算和恢复边界](../wiki/media-scheduler.md) · [真实HTTP事实、两槽公平及不重POST](../wiki/media-schedule-transport.md) · [180计划、48完整预选与020维护验收](../wiki/media-scheduler-validation.md) · [公平游标与持久退避的选择、替代及后果](../wiki/decisions/0018-media-fair-scheduling.md) · [冷却/探测、ACK与Outbox背压真实PG](../../internal/studio/media_schedule_postgres_test.go) · [首条渠道冷却与严格领取双连接竞争](../../internal/studio/media_schedule_channel_race_test.go) · [query/delivery/review真实HTTP工作区公平](../../internal/studio/media_schedule_lanes_test.go) · [原参考幂等与等待容量、固定指标及无副作用](../../internal/studio/media_scheduler_reference_test.go) · [真实冷却联备恢复和原键不重发](../../internal/studio/media_scheduler_restore_test.go) · [生产分页SQL与完整预选实测](../../internal/studio/media_scheduler_plan_test.go) · [019升级保旧及20万任务集合迁移](../../internal/studio/media_scheduler_upgrade_test.go) · [A3.3独立联合证据、修正与范围](../wiki/assets/media-scheduler-validation-v0.20-2026-11-15.json) · [180实际SQL计划与48完整生产预选原始数据](../wiki/assets/media-scheduler-plans-v0.20-2026-11-15.json) · [原19SQL保留/020摘要和公开API引用](../wiki/assets/media-scheduler-schema-v0.20-2026-11-15.json) · [实际版本化S3、保留历史对象和身份核对停机](../wiki/assets/media-scheduler-s3-v0.20-2026-11-15.json) · [同构建memory/demo回归，非媒体吞吐](../wiki/assets/performance-local-v0.20-2026-11-15.json) · [取消/超时明确未受理错误、原幂等优先](../../internal/studio/media_scheduler_reference_cancel_test.go) · [A4.1分页恢复验收及A4.2缓存范围](v16a4-contract.md) · [已接线批读、真实PG边界专项与剩余恢复限制；不作为A4整批验收](../wiki/recovery-private-phases.md) · [086分页恢复、满队列的回滚与验证，结果见089](../wiki/paged-recovery.md) · [A4.1完整启动、二次启动、页边界、缓存峰值与事件数的实际测量](../wiki/assets/recovery-large-2026-11-21.json) · [090双工作区512条积压、生成和编辑、原键、权限、真实领取及内存上限](../../internal/studio/media_admission_cache_test.go) · [PG记录与在途执行的保留、项目范围和启动实测口径](../wiki/decisions/0020-bounded-pg-caches.md) · [排队、实际退出、活动读者与停机中断保存](../../internal/studio/run_cache_test.go) · [重叠租约、取消与实际IO退出](../../internal/studio/task_cache_test.go) · [运营提交后冷热Run的同游标回放与审核边界](../../internal/studio/operations_run_replay_test.go) · [冷项目权限、订阅、并发创建与第100项](../../internal/studio/project_scope_test.go) · [10万活跃与10万终态完整启动的页耗时、条数与编码大小](../wiki/assets/recovery-capacity-2026-11-21.json) · [有界缓存实现复跑A4.1的12002条中断与9999条积压](../wiki/assets/recovery-backlog-bounded-2026-11-21.json) · [B1/B2 的交付范围、升级与故障验收条件，设计证据](v16b-contract.md) · [公开增量、持久水位、原游标、审核门禁及旧格式的实施决策](../wiki/decisions/0021-durable-run-stream.md) · [020升级、库提交前强杀/提交回执丢失、原键与真实备份恢复](../../internal/studio/run_stream_recovery_test.go) · [同序号重入、篡改或过期写拒绝和按记录/字节裁剪](../../internal/studio/run_stream_store_test.go) · [跨页与实时交接、旧格式/窗口过期、越权和越前游标](../../internal/studio/run_stream_replay_test.go) · [确认提交后广播、取消、背压及审核输出隔离](../../internal/studio/run_stream_execution_test.go) · [数据库时间、执行代次与老流升级规则](../wiki/decisions/0022-run-execution-leases.md) · [B2 本机 PG 领取、换代强杀、原键与 022 备份恢复](../changes/2026-11-21-103-run-lease-integration.md) · [V16-C 本机两个工作区混合运行的三轮量化样本与配置](../wiki/assets/mixed-workload-2026-11-21.json)

台账：[2026-11-14-065-execution-scheduler](../changes/2026-11-14-065-execution-scheduler.md) · [2026-11-14-066-execution-validation](../changes/2026-11-14-066-execution-validation.md) · [2026-11-14-067-execution-integration](../changes/2026-11-14-067-execution-integration.md) · [2026-11-14-068-media-execution-lanes](../changes/2026-11-14-068-media-execution-lanes.md) · [2026-11-14-069-media-transport-lanes](../changes/2026-11-14-069-media-transport-lanes.md) · [2026-11-14-070-media-lane-validation](../changes/2026-11-14-070-media-lane-validation.md) · [2026-11-14-071-media-lane-integration](../changes/2026-11-14-071-media-lane-integration.md) · [2026-11-14-072-media-delivery](../changes/2026-11-14-072-media-delivery.md) · [2026-11-14-073-media-delivery-provider](../changes/2026-11-14-073-media-delivery-provider.md) · [2026-11-14-074-delivery-storage-maintenance](../changes/2026-11-14-074-delivery-storage-maintenance.md) · [2026-11-14-075-media-delivery-integration](../changes/2026-11-14-075-media-delivery-integration.md) · [2026-11-14-076-media-review-dispatch](../changes/2026-11-14-076-media-review-dispatch.md) · [2026-11-14-077-media-review-permits](../changes/2026-11-14-077-media-review-permits.md) · [2026-11-14-078-media-review-dispatch-validation](../changes/2026-11-14-078-media-review-dispatch-validation.md) · [2026-11-14-079-media-review-dispatch-integration](../changes/2026-11-14-079-media-review-dispatch-integration.md) · [2026-11-15-080-media-scheduler](../changes/2026-11-15-080-media-scheduler.md) · [2026-11-15-081-media-schedule-transport](../changes/2026-11-15-081-media-schedule-transport.md) · [2026-11-15-082-media-scheduler-validation](../changes/2026-11-15-082-media-scheduler-validation.md) · [2026-11-15-083-media-scheduler-integration](../changes/2026-11-15-083-media-scheduler-integration.md) · [2026-11-15-084-recovery-plan](../changes/2026-11-15-084-recovery-plan.md) · [2026-11-15-085-recovery-private-phases](../changes/2026-11-15-085-recovery-private-phases.md) · [2026-11-15-086-paged-recovery](../changes/2026-11-15-086-paged-recovery.md) · [2026-11-15-087-recovery-validation](../changes/2026-11-15-087-recovery-validation.md) · [2026-11-15-088-recovery-faults](../changes/2026-11-15-088-recovery-faults.md) · [2026-11-21-089-recovery-acceptance](../changes/2026-11-21-089-recovery-acceptance.md) · [2026-11-21-090-pg-media-cache-admission](../changes/2026-11-21-090-pg-media-cache-admission.md) · [2026-11-21-091-run-cache-pins](../changes/2026-11-21-091-run-cache-pins.md) · [2026-11-21-092-media-cache-pins](../changes/2026-11-21-092-media-cache-pins.md) · [2026-11-21-093-workspace-project-queries](../changes/2026-11-21-093-workspace-project-queries.md) · [2026-11-21-094-bounded-startup](../changes/2026-11-21-094-bounded-startup.md) · [2026-11-21-095-durable-run-stream-plan](../changes/2026-11-21-095-durable-run-stream-plan.md) · [2026-11-21-096-run-stream-store](../changes/2026-11-21-096-run-stream-store.md) · [2026-11-21-097-run-stream-execution](../changes/2026-11-21-097-run-stream-execution.md) · [2026-11-21-098-run-stream-replay](../changes/2026-11-21-098-run-stream-replay.md) · [2026-11-21-099-run-stream-integration](../changes/2026-11-21-099-run-stream-integration.md) · [2026-11-21-100-run-lease-store](../changes/2026-11-21-100-run-lease-store.md) · [2026-11-21-101-run-lease-execution](../changes/2026-11-21-101-run-lease-execution.md) · [2026-11-21-102-run-lease-contention](../changes/2026-11-21-102-run-lease-contention.md) · [2026-11-21-103-run-lease-integration](../changes/2026-11-21-103-run-lease-integration.md) · [2026-11-21-104-mixed-workload](../changes/2026-11-21-104-mixed-workload.md) · [2026-11-22-134-recovery-barrier-observation](../changes/2026-11-22-134-recovery-barrier-observation.md)

### V17 性能与长稳态放行

状态：**进行中**。模块：H03、H07、H08。依赖：V15、V16。

按原方案固定环境验收，不把当前回环demo报告当生产成绩。

验收条件：

- 万级活跃流和目标媒体在途量，P95/P99分层
- CPU/RSS/FD/GC/数据库等待/慢订阅者与前端基准
- 24小时稳态、突发/故障注入及瓶颈报告

验证记录：

- 通过：V17-A 本机分阶段延迟与同进程 CPU、驻留内存、句柄；`node --test scripts/perf/owned-resource-sampler.test.mjs；npm run perf:local`。本机 Windows 031 实际运行 1000 次 bootstrap、100 次建任务并取消、12 次完整 demo 聊天。三段 P95 分别 19.77、10.67、3412.07 毫秒。观测到的最大驻留工作集 23,560,192 字节、原生句柄峰值 287；各窗口有 6、3、97 个样本。仅回环 memory/demo，数据库等待与 24 小时容量环境另验。
- 通过：V17-B 桌面和窄屏工作台基准；`真实 Chromium 桌面/窄屏各分冷暖缓存，保留全部样本与制品摘要`。033 工作台在真实 Chromium 157 完成 80 次独立浏览器/服务启动，每组 20 次。首个可交互控件的 P95 在桌面冷启动/预热为 319.8/274.0 毫秒，在 390 像素窄屏为 291.0/232.7 毫秒。每次创建可见空会话并独立 GET 核对。冷组静态资源命中均为 0/2，预热均为 2/2。本机回环 memory/demo，窄屏使用桌面浏览器引擎。
- 通过：V17-C 混合负载的 GC、数据库等待和慢订阅者；`三轮本机 PostgreSQL 混合负载、GC 区间、受控锁和快慢订阅者`。三次独立真实 PostgreSQL 混合负载覆盖 18 个阶段快照，每个快照均核对 6 个进程所属数据库连接。三个受控锁等待均从 Lock 恢复 idle，等待语句耗时 11.09、11.05 和 12.26 毫秒。每轮持续读取者按序收到 66 条事件，停读者缓冲到 64 条后只移除一次。
- 通过：V17-D 万条作品索引、可见缩略图与项目切换资源；`真实 PG 一万条作品，真实 Chromium 首屏、深页、视频预览与 30 个项目切换`。035 在本机 PostgreSQL 与 Chromium 上验证 10,000 个独立索引，认证分页 100 次。浏览器实际加载 20 页共 480 条，首屏 24 条索引中挂载 4 张卡，发出 2 个缩略图请求；深翻时最多挂载 10 张卡，追加页后可见锚点偏移 0 CSS 像素。两个真实私有视频先后播放，同时播放峰值为 1。切换 30 个不同项目后，旧订阅、未释放旧播放器和关闭标签页后的服务端活动处理器均为 0。深页至末次切换，强制 GC 后 DOM 少 187 个、JS 堆少 161,888 字节。专项 30.391 秒。 还需固定容量环境进行万活跃流和24小时验收。
- 通过：V17-E 单个活跃回复持续30分钟、交互和原库重开；`标准服务2100秒，真实PG、Chromium、1801个定时增量、独立SSE与30次输入滚动、租约和重开`。035 正式服务对本机兼容端发出一次请求。1,801 个增量持续 1,800.26 秒，Chromium 独立接收序号 1 至 1,802。30 次逐分钟采样均显示新增正文，输入至两帧的 P95 为 30.2 毫秒，滚动至实际位移的 P95 为 31.2 毫秒。PostgreSQL 独立观察 120 次租约，确认终态后从原库、原私有目录重开，25,214 字节回复逐字相同。 037 复测 1806.51 秒通过，1802 个连续序号，P95 输入 29.8 ms、滚动 32.5 ms，原库重开核对 25214 字节。
- 通过：V17-F1 已支持分支的可变高消息列表与有界分页读取；`035真实PG及授权浏览器128节点，分页、边界字节、稳定锚点、键盘与分支/项目切换`。035 本机 PostgreSQL 经正常入口生成单分支 64 轮，Chromium 在 7 页中逐条核对 128 条消息。首屏 20 条仅挂载 4 条，采样峰值 8 条。6 次前插与缩窄至 390 像素时，锚点最大偏移分别为 0.140625 和 0.140625 CSS 像素。键盘跨窗口、项目切换、未发送草稿和迟到读取均通过，浏览器生成 POST 为零。
- 通过：V17-F2 同一条分支一万条历史消息及有界缓存；`正规受理、消息路径一万节点、逐页无重漏、同分支重读、受控缓存与原输入/上下文/权益核对`。本机 PostgreSQL 通过正常受理入口完成同一分支 5,000 轮，逐轮核对上游上下文。Chromium 读取 500 个授权页、索引 10,000 条消息，实测最多挂载 10 行、缓存 120 条正文；首、中、末三处淘汰内容均通过原页重读。显式继续后原库有 10,002 个节点与 5,001 份冻结上下文，原键重放未增加上游调用。专项 219.573 秒。
- 通过：V17-G 普通与压力画布的渲染及交互；`500节点/1000边、最多150可视节点，平移P95≤20毫秒；2000节点/4000边、最多100个详细可视节点，P95≤25毫秒`。在本机 Intel 集显、Chromium 157、1920×1080 下，两档各实际拖动平移 60 秒，期间私有缩略图已解码、已受理 Run 持续活动。500 节点/1,000 边的 3,599 个帧间隔 P95 为 16.8 毫秒，最多 25 个详细节点；2,000 节点/4,000 边的 3,601 个间隔 P95 为 16.8 毫秒，最多 100 个详细节点。两个项目实际保存并重开 4 个修订，编辑与平移新增生成 POST 为零。
- 通过：V17-H1 本机集显、校准网络、冷暖缓存与万条历史；`正常项目和同一万条分支；20 Mbps 双向、80 ms RTT 校准，20 对独立冷暖样本及 300 KiB 初始 JS 传输预算`。037 本机集显，Chromium 157；5,000 轮/10,000 条，冷暖各20个独立样本。冷 JS 166,103 B、暖0；首可交互 P95 冷880.0 / 暖565.8 ms。三次实测 RTT 104.9–107.8 ms、双向19.55–19.71 Mbps，零额外生成；323.944s。高配与真机等另验。
- 待条件：V17-H2 指定参考环境、高配及真实移动设备；`在方案参考设备和20Mbps/80ms RTT网络复跑，另验高配与移动真机，记录浏览器/制品/缓存与数据集`。本机已实测 Intel 集显、系统可见内存约 31.5 GiB。2026-11-22 本机设备清单中便携/Android USB 类为 0，无 adb 命令，显示设备为 Intel 集显及虚拟适配器。另需对指定参考设备、高配机及实际移动设备分别复跑；生产入口另行验收。
- 通过：V17-I1 单控制写者 64 个活跃模型流与授权 SSE；`在真实 PostgreSQL 与本机可控 HTTP 网关上执行 8 路短测、64 路完整测试及原库回放`。真实本机 PG + 授权 HTTP/SSE + 可控上游，8 路及 64 路通过。64 个活跃模型流、768 条持久增量，P95/P99 44.42/47.23ms、零遗漏和额外上游调用；波次 1.204s，5.116s 专项。
- 通过：V17-I2 万条逻辑流的批量持久日志和并发回放 PoC；`隔离 PostgreSQL，记录 WAL 持久设置，10000 条逻辑流按每秒 100000 增量投放至少 10 秒，实测提交、回放、延迟、背压及完整性`。PG 16.14，8 写/4 并行读，10,000 个非空逻辑流。12 秒每秒投放/受理 100,000，全部 140 万增量与 PG 头及回放匹配；投放到确认回放 P95/P99 上界 12.543/14.591ms。14.920s；真实业务工作台及长稳态独立验收。
- 通过：V17-I3 单写者运行回放的只读连接隔离；`go test -count=1 -p 1 -run '^TestPostgresRunReplayPool' -v ./internal/studio`。037 本机 PostgreSQL，7 个专项 29.837 秒；占满与请求取消、控制写入继续、读连接断开恢复、生命周期、审核快照、日志损坏、过期和旧游标、直播交接。服务端可选 1–4 个只读回放连接，默认 0。
- 通过：V17-J1 单控制写者托管子进程的正常会话执行；`执行跨进程模型 I/O 专项、故障与资源释放专项，再通过真实 PostgreSQL 和授权 SSE 核对 2×40 个活动会话`。标准服务在本机启动两个受管子进程，Windows TCP 归属与 PostgreSQL 租约分别核实每个子进程同时持有 40 条模型连接。5 个工作区各 16 条，4 个冻结模型各 20 条。80 个正常授权读者共读到 960 个原序增量，80 笔运行完成结算；模型写出到普通 SSE 读完的 P50/P95/P99 为 33.91/64.86/976.40 毫秒。另一个读者暂停约 2.07 秒。 排队取消未请求模型，已开始取消和单子进程或控制器故障的已执行运行进入 REVIEW。两个子进程全失时，只将两个尚未领取的排队请求交给控制器的一个执行位。含故障场景的标准制品共发出 87 次模型请求；隔离的审核测试宿主另发 1 次，屏蔽决定留下的候选公开增量为 0。两部分合计零额外或重复模型 POST。4 路试跑包 26.646 秒，正式档包 29.582 秒。
- 通过：V17-J2 真实运行增量的跨 Run 批量持久追加；`真实 PostgreSQL 事务、失期/撤销/ACK边界与原公开 SSE；同一并发负载分别开启/关闭，核验原事件 xmin 和全部原序回放`。本机 PostgreSQL 的两组独立新 schema 各运行 64 条正常模型流，每条 12 个增量。开关两侧各有 768 条增量按原序通过授权 SSE 和数据库回放，模型请求各 64 次。事件行的 xmin 显示，关闭时为 768 次写入事务，开启时为 24 次，每次包含 32 个不同 Run，少 744 次（96.875%）。从模型写出前到普通客户端读完相应 SSE 的 P95 分别为 52.28 和 6.83 毫秒，P99 为 73.50 和 8.10 毫秒。在本机控制的 12 个波次下测得，事务计数只覆盖增量插入。8 路试跑 26.606 秒，64 路含业务边界专项共 28.722 秒。 六项原库专项全部通过，共 13.794 秒，覆盖同事务分组与逐项授权、保留窗口与输出上限、并发取消、失期回滚、服务器明确回滚和提交应答丢失后的停写。
- 通过：V17-J3 慢 TCP 读者、原游标与默认共享订阅上限；`go test -p 1 -count=1 -timeout=120s -run '^TestPostgresSSESocket' -v ./internal/studio`。真实本机 TCP 停读、核验实际写截止和快端完整输出；同库授权恢复、五账户及全局/工作区订阅释放由集成人串行验证。 两项真实 PG 与 OS 套接字专项通过（8.00 秒、3.38 秒）。五工作区占满 128 位；正常流及原游标回读校验通过，结束占位 0。
- 通过：V17-J4 共享会话复核与 192 个同时在线的授权订阅；`真实 PG；正常账户、独立回放和授权读连接、192 个真实订阅、撤销指定会话、复核失败关闭及原游标接续`。默认128，扩展档使用有界全局/工作区槽、按会话分组的授权复核和独立读连接。实际 192 客户端及原模型请求以验收数据为准。 228 修正位置列后 192 个已鉴权订阅实测通过（96 个运行、96 个项目）。注销关闭 64 个，复核连接故障关闭其余 128 个，随后重新接入 2 个。慢读者实测 5000.32 ms 触发写截止；原游标补回 12 个增量，缓冲丢弃 0。 040 同制品串行通过 512/1,024 档。1,024 个原模型连接均实测续租、结算和原键核对，普通读者交付 P95/P99 为 193.28/242.55 毫秒。
- 通过：V17-J4 联合运行的模型、增量日志和订阅；`STUDIO_CHAT_CHILD_ACCEPTANCE=integrated-pilot / integrated；go test -p 1 -count=1 -run '^TestPostgresChatChildAcceptance$' -v ./internal/studio`。同一标准制品与数据库同时启用托管模型、批写与扩展订阅；实证模型连接、原库事件、物理提交、普通/迟读者交付与权益。 038 服务程序 SHA256 1cbdd80631db03eba728d6d55bea8abaeb0af84aa5e01cb971f657f62747544f。80 个模型连接、240 个普通授权订阅与 1 个迟读订阅同时在线，960 个原始增量由 37 个真实 PG 事务提交。2880 份普通增量送达 P95 16.45 ms、P99 23.21 ms。80 次正常结算，无意外重复 POST。
- 待条件：固定容量环境的万活跃流、媒体在途与24小时稳态；`分层 P95/P99、CPU/RSS/FD/GC、数据库等待、慢订阅者、24小时与突发故障`。一万授权项目订阅读者以及本机 1,024 个业务模型连接分别通过。040 同一渠道以每秒 320 项到期、200ms 服务实测最高 159.41 次/秒，尚有本地调度和持久化差距。本机 60,000 项正常在途已通过。259 从两个工作区每秒受理 1 项，10 分钟稳态平均在途 604.78 项，实测每秒 57.52 次原 GET。持续 6,000 次原 GET/秒、万活跃模型和固定环境的 24 小时稳态各待实测。
- 通过：万级授权订阅准入、分页会话复核与断线恢复；`STUDIO_SSE_FANOUT=full，go test -p 1 -parallel 1 -count=1 -timeout=20m -run '^TestPostgresTenThousandProjectSubscribers$' -v ./internal/studio`。039 本机 PG 16.14，80 工作区×125 个真实 HTTP 读者，共 320 会话。两轮 640 次会话复核；全体收到首次广播，32 个注销读者见 EOF，同项目 93 个邻居继续；原游标回放成功，占位归零。建连 6846ms、广播 1117ms，专项 30.04s。
- 通过：264 个受管模型连接的业务联合验收；`STUDIO_CHAT_CHILD_ACCEPTANCE=integrated-264，TestPostgresChatChildAcceptance`。8 个实际子进程各 33 条模型连接，6 个冻结模型各 44 条，5 个工作区共 264 个正常读者和 1 个迟读者。3,168 个原始增量、110 个插入事务、264 次结算、0 次意外重提。普通读者 P95/P99 37.11/75.38 毫秒，用例 50.25 秒。
- 通过：原 handle 查询的 PostgreSQL 到期迟滞；`STUDIO_MEDIA_QUERY_MEASURE=1，go test -p 1 -count=1 -run '^TestPostgresMediaOriginalQueryCapacity$' -v ./internal/studio`。PG 16.14，40/s 投放，每档 128 个唯一原 GET 且零 POST；2/16 个 worker 的完成率为 9.48/37.77 次每秒，到期至写出 P95 为 9672.21/34.67 毫秒。用例 23.14 秒。
- 通过：有界查询租约连接池与慢 GET；`STUDIO_MEDIA_QUERY_MEASURE=1，go test -p 1 -parallel 1 -count=1 -timeout=240s -run '^TestPostgresPooled' -v ./internal/studio`。6 项本机 PG 专项 26.930 秒。32 查询执行位共享 4 条数据库连接，128 个原 GET、零 POST，200ms 响应下完成 37.81 次/秒，实际峰值 11。已知回应、超时、锁等待、取消与旧租约失效均已复核。
- 通过：高并发原任务查询与工作区公平派发；`真 PG、原 handle、慢 GET、到期迟滞与 SQL 池、满工作区旁路及空闲轮询上界`。040 正常派发达 256 个实际同时等待的原 GET，四条查询数据库连接可同时借出。32/64/128 位、每秒 320 到期的三档分别完成 129.27/149.61/159.41 次每秒；最高速档到期至发出 P95 1317.95 毫秒。公平场景消除一轮 4 秒的额外等待。
- 通过：039 到 040 的在途媒体及未知受理恢复；`MEDIA_QUERY_UPGRADE_TEST=1，配套 039 服务、PG、pg_dump 与 pg_restore`。真实 039 程序建立三份上游请求。040 升级、旧制品拒绝写、停写联备、独立恢复后两次原 GET、零新 POST、零重叠探测，一份受理未知持续冻结待核实。38.85 秒。
- 通过：正式受理的在途媒体配额与重开；`STUDIO_MEDIA_ADMISSION_TASKS=128/512/4096/60000，各档独立串行`。128、512、4,096、60,000 四档通过。16 个正常注册工作区通过平台接口受理 60,000 项媒体任务，重开前后各重放 60,000 个原键。469 页恢复 59,999 个原句柄和最多 512 项缓存，1 项无句柄的未知受理继续占位，重开后 2 个原 GET、0 个新生成 POST。首次受理与超限检查 1,471.601 秒，全档 2,940.880 秒。
- 通过：原任务查询从预选到提交的分段测量；`STUDIO_MEDIA_QUERY_TIMINGS=1，go test -p 1 -count=1 -timeout=5m -run '^TestPostgresQueryStage.*248$' -v ./internal/studio`。PG 16.14，三档与实际心跳共 34.400 秒。128 档 512 个原 GET，执行入口到首次续租 P95 233.79ms、已知结果等 App.mu P95 271.76ms、控制保存事务 P95 5.93ms、租约池等待 P95 4.61ms。各阶段窗口会重叠。

证据：[各阶段负载、采样口径与环境范围](../wiki/performance.md) · [031 Windows 同一真实服务进程分段 P50/P95/P99、CPU、RSS、句柄及样本量](../wiki/assets/performance-owned-031-2026-11-22.json) · [桌面和窄屏工作台基准](../changes/2026-11-22-171-workbench-frontend-benchmark.md) · [混合负载的 GC、数据库等待和慢订阅者](../changes/2026-11-22-172-mixed-resource-observation.md) · [四种窗口/缓存组合、80份原始毫秒样本和制品身份](../wiki/assets/performance-frontend-033-2026-11-22.json) · [033 三轮实际 GC、数据库等待及持续/停读分发证据](../wiki/assets/performance-mixed-resources-033-2026-11-22.json) · [作品库可见卡片与退出清理](../changes/2026-11-22-181-library-virtual-grid.md) · [万条作品索引及项目切换实测](../changes/2026-11-22-180-private-library-browser-resources.md) · [本机万条索引、浏览器虚拟视口与30个项目资源实测](../wiki/assets/performance-library-035.json) · [033 至 035 实际启动模块、同级 gzip 预算和 Go 直连响应字节](../wiki/assets/performance-startup-js-035.json) · [持久会话 30 分钟回复验收](../changes/2026-11-22-183-durable-long-reply.md) · [标准服务聊天执行期限](../changes/2026-11-22-184-chat-run-timeout.md) · [可核对的本机长回复流](../changes/2026-11-22-185-long-reply-gateway-and-observation.md) · [活跃回复中的可信输入和滚动](../changes/2026-11-22-186-long-reply-browser.md) · [原方案消息、画布、参考设备与现有验收差异](../changes/2026-11-22-190-frontend-acceptance-coverage.md) · [可变行高的消息列表](../changes/2026-11-22-187-variable-height-chat-timeline.md) · [按页读取持久会话正文](../changes/2026-11-22-188-conversation-message-pages.md) · [128条分支消息的浏览器验收](../changes/2026-11-22-189-persistent-conversation-history-browser.md) · [真实30分钟、30个浏览器检查点、1802个接收序号及原库重开](../wiki/assets/performance-long-reply-035.json) · [真实PG与Chromium一百二十八条消息及可见锚点实测](../wiki/assets/performance-conversation-history-035.json) · [可回读的消息正文缓存](../changes/2026-11-22-191-chat-message-body-cache.md) · [持久会话祖先与上下文](../changes/2026-11-22-192-durable-conversation-ancestry.md) · [长会话配置、格式与恢复](../changes/2026-11-22-193-long-history-format-and-restore.md) · [真实万条分支验收](../changes/2026-11-22-194-deep-conversation-acceptance.md) · [单分支5000轮、受理上下文、500页及三次淘汰回读](../wiki/assets/performance-conversation-history-036.json) · [项目画布保存与私有来源](../changes/2026-11-22-195-project-canvas-versions.md) · [自由画布的编辑与无障碍操作](../changes/2026-11-22-196-free-canvas-workbench.md) · [两档真实图的平移测量](../changes/2026-11-22-197-canvas-pan-browser.md) · [画布集成、精确格式与恢复](../changes/2026-11-22-198-project-canvas-integration.md) · [两档各60秒的全部rAF间隔、逐秒真实来源及Intel硬件渲染](../wiki/assets/performance-canvas-037.json) · [受限网络下的冷暖工作台与深历史](../changes/2026-11-22-200-workbench-network-browser.md) · [启动 JavaScript 实际 gzip 传输](../changes/2026-11-22-199-startup-javascript-compression.md) · [64 个同时活跃的模型流和授权 SSE](../changes/2026-11-22-201-active-stream-capacity.md) · [分片批量持久日志与并发回放试验](../changes/2026-11-22-202-sharded-stream-journal-poc.md) · [H1 40 个冷暖样本、双向校准、实际 JS 传输与历史 P95](../wiki/assets/performance-network-037.json) · [64 活跃 Run、768 个增量、逐阶段 HTTP/SSE 分位与完整 PG 回放](../wiki/assets/performance-active-streams-037.json) · [画布稳定来源读取、保存回执修正后两档硬件加速正式复测](../wiki/assets/performance-canvas-037-recheck.json) · [PR 启动资源预算与制品差异](../changes/2026-11-22-203-pr-bundle-diff.md) · [独立 PostgreSQL 持久日志的 12 秒逐秒窗口、四类延迟和完整性](../wiki/assets/performance-stream-journal-poc-037.json) · [运行 SSE 断线回放的只读连接隔离](../changes/2026-11-22-204-run-replay-read-pool.md) · [私管道的受管聊天执行进程](../changes/2026-11-22-205-chat-execution-child-transport.md) · [受管与本地执行共用调度预算](../changes/2026-11-22-206-managed-chat-scheduling.md) · [受管会话的授权 SSE 与真实进程归属验收](../changes/2026-11-22-207-managed-chat-acceptance.md) · [受管聊天执行的集成与故障验收](../changes/2026-11-22-208-managed-chat-integration.md) · [原增量表的跨运行 PostgreSQL 提交](../changes/2026-11-22-209-batched-run-delta-store.md) · [跨运行增量的有界协调与开关](../changes/2026-11-22-210-run-delta-coordinator.md) · [真实聊天流的批量提交前后对照](../changes/2026-11-22-211-batched-delta-acceptance.md) · [真实工作台增量批量提交集成](../changes/2026-11-22-212-business-delta-batch-integration.md) · [长回复虚拟行与原提示滚回核对](../changes/2026-11-22-213-virtualized-live-reply-observation.md) · [037 真实 64 路会话、授权回放和原库物理提交分布](../wiki/assets/performance-batched-deltas-037.json) · [037 两个本机子进程各 40 路实际执行、原库结算与失败边界](../wiki/assets/performance-managed-chat-037.json) · [真实慢 TCP 读者与订阅上限](../changes/2026-11-22-214-sse-socket-acceptance.md) · [共享会话核验与全局订阅容量](../changes/2026-11-22-218-stream-session-batches.md) · [192 个授权订阅、撤销与重连](../changes/2026-11-22-219-authenticated-sse-sessions.md) · [80 个模型流、241 个订阅与批量持久化联合验收](../changes/2026-11-22-220-integrated-chat-sse-acceptance.md) · [对应固定制品在隔离数据库的串行验收进度。](../changes/2026-11-22-227-local-acceptance.md) · [037 实际单流半小时、浏览器交互、租约及原库重开](../wiki/assets/performance-long-reply-037.json) · [共享会话批量复核的返回位置](../changes/2026-11-22-228-stream-session-batch-positions.md) · [038 联合实测，区分源事件、数据库事务与下游交付副本](../wiki/assets/performance-integrated-chat-038.json) · [授权订阅的分页会话复核](../changes/2026-11-23-232-paged-stream-session-admission.md) · [真实项目 SSE 广播与断线恢复](../changes/2026-11-23-233-project-sse-fanout.md) · [320 个不同授权会话在 80 个工作区同时读项目事件的实测](../wiki/assets/performance-project-sse-320.json) · [项目事件按游标共享已提交的通知页](../changes/2026-11-23-237-shared-project-feed.md) · [受管模型执行容量与 264 路联合入口](../changes/2026-11-23-240-managed-chat-capacity.md) · [媒体原 handle 查询的到期与发起延迟](../changes/2026-11-23-241-media-original-query-capacity.md) · [媒体在途、每秒到期、HTTP并发和应用派发迟滞的测量口径](../wiki/media-original-query-capacity.md) · [真实10,000条项目SSE连接、逐作用域广播、撤权与原游标报告](../wiki/assets/performance-project-sse-10000-039.json) · [两种存储的worker档、真实HTTP、到期至各阶段分位及投放守恒](../wiki/assets/media-original-query-capacity-2026-11-23.json) · [264 个真实模型连接、265 个同时授权订阅、业务与账务的联合实测](../wiki/assets/performance-integrated-chat-264-039.json) · [查询执行与数据库连接预算及其故障验收](../changes/2026-11-23-242-query-lease-pool.md) · [32 个原查询执行位和 4 条数据库连接的 128 次 HTTP 到期实测](../wiki/assets/media-pooled-query-capacity-039.json) · [039 同源服务、前端、离线工具的本机累计回归](../changes/2026-11-23-243-format-039-regression.md) · [大档原任务查询与有界派发](../changes/2026-11-23-244-large-original-query.md) · [039 至 040 的原任务恢复与渠道探测](../changes/2026-11-23-245-media-query-format-restore.md) · [经正式接口受理的媒体在途任务](../changes/2026-11-23-246-normal-media-admission-capacity.md) · [32/64/128 原查询位在每秒320个到期下的真实 GET、延迟与积压](../wiki/assets/media-query-rate-curve-040.json) · [128、512、4,096、60,000 四个正常 API 在途配额档的原库、原键与重开核验数据](../wiki/assets/media-normal-admission-040.json) · [查询库等待、续租、原 GET、应用锁及保存事务分别计时](../changes/2026-11-23-248-query-stage-timing.md) · [原 GET 分段、样本、占用和重叠墙钟](../wiki/assets/media-query-stage-timing-040.json) · [原任务查询的并发登记](../changes/2026-11-23-249-large-query-entry-pin.md) · [原查询结果合批的实库对照](../changes/2026-11-23-250-known-query-save-batch.md) · [大档原任务执行登记后的三档分段与到期情况](../wiki/assets/media-query-entry-040.json) · [原结果八项合批与逐项保存的实际终态提交和到期差异](../wiki/assets/media-query-save-batch-040.json) · [准入配额与原库恢复扫描](../changes/2026-11-23-252-admission-thresholds-and-recovery.md) · [旧查询与阈值查询各4096项普通受理的用时、原键与恢复实扫](../wiki/assets/media-admission-thresholds-040.json) · [受管模型全执行位联合验收](../changes/2026-11-23-253-managed-chat-512.md) · [512个实际模型连接、513个授权SSE与原键结算核对](../wiki/assets/performance-integrated-chat-512-040.json) · [040 同源程序累计回归。11 月 24 日暂停时已通过 385 项，22 项跳过，数据库与对象存储干净停机。11 月 25 日从新库重验。](../changes/2026-11-23-251-format-040-regression.md) · [六万项正常媒体受理及原库重开](../changes/2026-11-24-254-normal-admission-60000.md) · [本机 040 完整 `npm run test:db` 退出 0，顶层 789 项通过、31 项跳过、0 项失败，核心包耗时 1601.565 秒。受审核回放快照与工作流取消的 submit、query 均在本次全量中通过。039 原库升级到 040、原视频浏览器编辑与延长、断线核对和版本化对象恢复也已通过。](../wiki/assets/format-040-regression.json) · [原查询预选的公平页对照](../changes/2026-11-24-255-original-query-page.md) · [原查询公平页的真实 ABBA 对照、五种页的 SQL 往返与候选补丁散列](../wiki/assets/media-query-page-255.json) · [同一服务的混合运行与重启核验](../changes/2026-11-24-256-mixed-service-soak.md) · [正常受理任务的多轮原查询](../changes/2026-11-24-257-recurring-original-query.md) · [6 个真实混合窗口、原服务中断和恢复、两工作区原键复核](../wiki/assets/mixed-service-pilot-040.json) · [25 项正常受理、24 个原 handle 三轮的查询到期及网关时间窗](../wiki/assets/recurring-media-query-040.json) · [受管聊天 1,024 路并发对照](../changes/2026-11-24-258-managed-chat-1024.md) · [从同一标准制品实测 512 与 1,024 个原模型连接、授权读者与原租约、权益回放及进程回收](../wiki/assets/performance-integrated-chat-1024-040.json) · [定速媒体受理与完整生命周期](../changes/2026-11-24-259-openloop-media-lifecycle.md) · [两个工作区每秒新建 1 个媒体任务，10 分钟预热后继续投放 10 分钟。稳态平均在途 604.78 项，原句柄实测 57.52 GET/秒。全程 69,312 次原 GET，1,200 次正常受理均排空并取得原键收据，终态为受控上游明确失败。](../wiki/assets/media-openloop-040.json) · [Windows 重启前 646 项通过、29 项跳过、1 项失败，两项原用例的三次定向复验均通过。](../wiki/assets/format-040-regression-interrupted.json) · [截至 253 的前端测试时钟、609 项测试和六组冒烟，保留当时的实库状态。](../wiki/assets/format-040-regression-through253.json) · [原查询保存时的工作区计数成本](../changes/2026-11-25-260-media-workspace-count-experiment.md) · [24 个原句柄按 A、B、B、A 实测 9.597、9.535、9.597、9.534 次原 GET/秒。512 个原句柄按 A、B、B、A 实测 100.293、111.188、83.632、92.994 次原 GET/秒。逐事务 WAL 和无物理探针计时分别记录。](../wiki/assets/media-workspace-count-260.json) · [原 GET 进度保存的 Outbox 分段耗时与事务归属](../changes/2026-11-25-262-outbox-progress-save-timing.md) · [两次分段各核对 69 笔 RUNNING 保存。Outbox 占完整事务累计耗时的 15.94%、17.06%，去重后四步占 12.55%、13.65%。独立物理观测轮的 69 笔任务行版本与对应事件新行具有相同的 xmin。](../wiki/assets/media-query-outbox-262-040.json)

台账：[2026-11-22-161-owned-resource-sampling](../changes/2026-11-22-161-owned-resource-sampling.md) · [2026-11-22-171-workbench-frontend-benchmark](../changes/2026-11-22-171-workbench-frontend-benchmark.md) · [2026-11-22-172-mixed-resource-observation](../changes/2026-11-22-172-mixed-resource-observation.md) · [2026-11-22-180-private-library-browser-resources](../changes/2026-11-22-180-private-library-browser-resources.md) · [2026-11-22-181-library-virtual-grid](../changes/2026-11-22-181-library-virtual-grid.md) · [2026-11-22-183-durable-long-reply](../changes/2026-11-22-183-durable-long-reply.md) · [2026-11-22-184-chat-run-timeout](../changes/2026-11-22-184-chat-run-timeout.md) · [2026-11-22-185-long-reply-gateway-and-observation](../changes/2026-11-22-185-long-reply-gateway-and-observation.md) · [2026-11-22-186-long-reply-browser](../changes/2026-11-22-186-long-reply-browser.md) · [2026-11-22-187-variable-height-chat-timeline](../changes/2026-11-22-187-variable-height-chat-timeline.md) · [2026-11-22-188-conversation-message-pages](../changes/2026-11-22-188-conversation-message-pages.md) · [2026-11-22-189-persistent-conversation-history-browser](../changes/2026-11-22-189-persistent-conversation-history-browser.md) · [2026-11-22-190-frontend-acceptance-coverage](../changes/2026-11-22-190-frontend-acceptance-coverage.md) · [2026-11-22-191-chat-message-body-cache](../changes/2026-11-22-191-chat-message-body-cache.md) · [2026-11-22-192-durable-conversation-ancestry](../changes/2026-11-22-192-durable-conversation-ancestry.md) · [2026-11-22-193-long-history-format-and-restore](../changes/2026-11-22-193-long-history-format-and-restore.md) · [2026-11-22-194-deep-conversation-acceptance](../changes/2026-11-22-194-deep-conversation-acceptance.md) · [2026-11-22-195-project-canvas-versions](../changes/2026-11-22-195-project-canvas-versions.md) · [2026-11-22-196-free-canvas-workbench](../changes/2026-11-22-196-free-canvas-workbench.md) · [2026-11-22-197-canvas-pan-browser](../changes/2026-11-22-197-canvas-pan-browser.md) · [2026-11-22-198-project-canvas-integration](../changes/2026-11-22-198-project-canvas-integration.md) · [2026-11-22-199-startup-javascript-compression](../changes/2026-11-22-199-startup-javascript-compression.md) · [2026-11-22-200-workbench-network-browser](../changes/2026-11-22-200-workbench-network-browser.md) · [2026-11-22-201-active-stream-capacity](../changes/2026-11-22-201-active-stream-capacity.md) · [2026-11-22-202-sharded-stream-journal-poc](../changes/2026-11-22-202-sharded-stream-journal-poc.md) · [2026-11-22-203-pr-bundle-diff](../changes/2026-11-22-203-pr-bundle-diff.md) · [2026-11-22-204-run-replay-read-pool](../changes/2026-11-22-204-run-replay-read-pool.md) · [2026-11-22-205-chat-execution-child-transport](../changes/2026-11-22-205-chat-execution-child-transport.md) · [2026-11-22-206-managed-chat-scheduling](../changes/2026-11-22-206-managed-chat-scheduling.md) · [2026-11-22-207-managed-chat-acceptance](../changes/2026-11-22-207-managed-chat-acceptance.md) · [2026-11-22-208-managed-chat-integration](../changes/2026-11-22-208-managed-chat-integration.md) · [2026-11-22-209-batched-run-delta-store](../changes/2026-11-22-209-batched-run-delta-store.md) · [2026-11-22-210-run-delta-coordinator](../changes/2026-11-22-210-run-delta-coordinator.md) · [2026-11-22-211-batched-delta-acceptance](../changes/2026-11-22-211-batched-delta-acceptance.md) · [2026-11-22-212-business-delta-batch-integration](../changes/2026-11-22-212-business-delta-batch-integration.md) · [2026-11-22-213-virtualized-live-reply-observation](../changes/2026-11-22-213-virtualized-live-reply-observation.md) · [2026-11-22-214-sse-socket-acceptance](../changes/2026-11-22-214-sse-socket-acceptance.md) · [2026-11-22-218-stream-session-batches](../changes/2026-11-22-218-stream-session-batches.md) · [2026-11-22-219-authenticated-sse-sessions](../changes/2026-11-22-219-authenticated-sse-sessions.md) · [2026-11-22-220-integrated-chat-sse-acceptance](../changes/2026-11-22-220-integrated-chat-sse-acceptance.md) · [2026-11-22-227-local-acceptance](../changes/2026-11-22-227-local-acceptance.md) · [2026-11-22-228-stream-session-batch-positions](../changes/2026-11-22-228-stream-session-batch-positions.md) · [2026-11-23-232-paged-stream-session-admission](../changes/2026-11-23-232-paged-stream-session-admission.md) · [2026-11-23-233-project-sse-fanout](../changes/2026-11-23-233-project-sse-fanout.md) · [2026-11-23-237-shared-project-feed](../changes/2026-11-23-237-shared-project-feed.md) · [2026-11-23-240-managed-chat-capacity](../changes/2026-11-23-240-managed-chat-capacity.md) · [2026-11-23-241-media-original-query-capacity](../changes/2026-11-23-241-media-original-query-capacity.md) · [2026-11-23-242-query-lease-pool](../changes/2026-11-23-242-query-lease-pool.md) · [2026-11-23-243-format-039-regression](../changes/2026-11-23-243-format-039-regression.md) · [2026-11-23-244-large-original-query](../changes/2026-11-23-244-large-original-query.md) · [2026-11-23-245-media-query-format-restore](../changes/2026-11-23-245-media-query-format-restore.md) · [2026-11-23-246-normal-media-admission-capacity](../changes/2026-11-23-246-normal-media-admission-capacity.md) · [2026-11-23-248-query-stage-timing](../changes/2026-11-23-248-query-stage-timing.md) · [2026-11-23-249-large-query-entry-pin](../changes/2026-11-23-249-large-query-entry-pin.md) · [2026-11-23-250-known-query-save-batch](../changes/2026-11-23-250-known-query-save-batch.md) · [2026-11-23-251-format-040-regression](../changes/2026-11-23-251-format-040-regression.md) · [2026-11-23-252-admission-thresholds-and-recovery](../changes/2026-11-23-252-admission-thresholds-and-recovery.md) · [2026-11-23-253-managed-chat-512](../changes/2026-11-23-253-managed-chat-512.md) · [2026-11-24-254-normal-admission-60000](../changes/2026-11-24-254-normal-admission-60000.md) · [2026-11-24-255-original-query-page](../changes/2026-11-24-255-original-query-page.md) · [2026-11-24-256-mixed-service-soak](../changes/2026-11-24-256-mixed-service-soak.md) · [2026-11-24-257-recurring-original-query](../changes/2026-11-24-257-recurring-original-query.md) · [2026-11-24-258-managed-chat-1024](../changes/2026-11-24-258-managed-chat-1024.md) · [2026-11-24-259-openloop-media-lifecycle](../changes/2026-11-24-259-openloop-media-lifecycle.md) · [2026-11-25-260-media-workspace-count-experiment](../changes/2026-11-25-260-media-workspace-count-experiment.md) · [2026-11-25-262-outbox-progress-save-timing](../changes/2026-11-25-262-outbox-progress-save-timing.md)

### V18 部署、备份与灾备演练

状态：**进行中**。模块：H01、H04、H07、H09。依赖：V09、V11。

可重复部署、TLS/密钥管理、备份恢复及迁移治理。

验收条件：

- 分环境配置、版本化迁移、灰度与回滚脚本
- PG/PITR与对象存储备份实际恢复
- 多可用区切换与RPO/RTO证据、监控值班手册

验证记录：

- 通过：V18-A 本机物理 PITR 与独立版本化对象备份联合恢复；`node scripts/recovery/workbench-drill.mjs --weed <本机固定版本>`。两次独立 PG16.14 命名点与对象冷备恢复。移离原目录后 23.910 秒读回原账户、草稿 v1、4 份原私有字节及唯一 0.2 CREDIT 结算，目标点后 v2 被排除。
- 通过：V18-B 本机单写者灰度、故障封流与回原制品；`node scripts/release/workbench-release-drill.mjs --previous <原制品> --candidate <候选>`。两个真实同格式制品经本机 TLS 和 PostgreSQL 验证。候选故障封流 2.061 秒，原制品重开 3.478 秒，候选再次开放 3.949 秒。每次均核验三个写锁及原私图、原键收据。
- 通过：V18-D 本机可操作的值班手册；`核对真实 /healthz、/readyz、/metrics 与恢复/故障命令`。集成人在独立本机进程直接执行值班手册 PowerShell，/healthz、/readyz、/metrics 均返回 200，采样 14 条固定指标，无凭据读指标 401，正常退出。两次作者复验亦记录真实 SSE 占用 0→1→0。
- 通过：V18-E 跨格式回退与升级窗口收据对账；`node scripts/release/cross-format-drill.mjs --run --previous-source <冻结036> --candidate-source <冻结037> --stage-parent <本机暂存路径> --bin-dir <PG16工具>`。复核两份真实制品、物理命名恢复点、私有文件与升级窗口两笔写入，维护入口保持封流；尚待集成人串行执行。 本机固定 036→037→036 实测 44229 毫秒；窗口两次成功写入仅保留在新版，旧恢复点缺两份对应收据，自动重提 0。回放停在目标 LSN，目录字节与数据库 UUID 一致，入口最终保持维护。
- 通过：V18-F 同步备机接管与工作台收据；`node scripts/recovery/workbench-sync-standby.mjs --artifact <已核对的本机037 baseline包>`。核实主备的同步落盘和回放、旧进程及写锁退出、独立端口提升、原账户与收据和本机恢复时间。独立可用区仍需另验。 同机 PG16 remote_apply 同步复制。原写进程退出、三锁释放后升主，时间线 1→2，5 份确认收据全部恢复；从停写至业务核对 3639 毫秒，原库已隔离。
- 待条件：分环境部署、灰度/回滚、真实跨可用区放行；`部署环境隔离、真实多区切换和各存储RPO/RTO测量`。需指定独立可用区、对象/WAL备份与恢复目标、业务切换窗口和明确环境授权。V11真实邮件联动另验。

证据：[本机工作台完整恢复命令与证据边界](../wiki/recovery-drill.md) · [最终命名恢复点、WAL、私有树及认证读回的脱敏实测](../wiki/assets/recovery-drill-2026-11-22.json) · [本机实测采样、候选封流、原收据与恢复后写入核对](../wiki/release-operations.md) · [三次真实切换结果、制品摘要、三锁及私有读回结果](../wiki/assets/release-handoff-2026-11-22.json) · [036 到 037 的跨格式回退演练](../changes/2026-11-22-216-cross-format-recovery.md) · [同步备机切换与原业务收据](../changes/2026-11-22-217-postgresql-sync-standby.md) · [对应固定制品在隔离数据库的串行验收进度。](../changes/2026-11-22-227-local-acceptance.md) · [036 到 037 的升级窗口及 036 恢复点实测](../wiki/assets/recovery-cross-format-037.json) · [同步备机接管与业务五份原收据](../wiki/assets/recovery-sync-standby-037.json)

台账：[2026-11-22-131-postgresql-pitr-drill](../changes/2026-11-22-131-postgresql-pitr-drill.md) · [2026-11-22-132-object-store-cold-backup](../changes/2026-11-22-132-object-store-cold-backup.md) · [2026-11-22-133-recovery-platform-integration](../changes/2026-11-22-133-recovery-platform-integration.md) · [2026-11-22-139-release-ingress](../changes/2026-11-22-139-release-ingress.md) · [2026-11-22-140-release-process](../changes/2026-11-22-140-release-process.md) · [2026-11-22-141-local-release-artifacts](../changes/2026-11-22-141-local-release-artifacts.md) · [2026-11-22-142-release-handoff-integration](../changes/2026-11-22-142-release-handoff-integration.md) · [2026-11-22-143-release-operations](../changes/2026-11-22-143-release-operations.md) · [2026-11-22-216-cross-format-recovery](../changes/2026-11-22-216-cross-format-recovery.md) · [2026-11-22-217-postgresql-sync-standby](../changes/2026-11-22-217-postgresql-sync-standby.md) · [2026-11-22-227-local-acceptance](../changes/2026-11-22-227-local-acceptance.md)

### V19 可执行工作流与模板

状态：**已验收**。模块：H02、H03、H10、H08。依赖：V03、V06、V09。

将版本草稿升级为显式执行DAG和可复用模板。

验收条件：

- 节点/边、环与输入类型校验、固定流程版本
- 人工确认、节点重跑、取消和恢复不重复执行收费步骤
- 文本→图像→视频模板完整交付及资产归属

验证记录：

- 通过：V19-A模板固定版本执行与人工选图；`npm run test:db（启用本机S3、大恢复、混合负载和WORKFLOW_BROWSER_TEST）；11项工作流/维护专项；npm run check 各项及六套smoke`。023 本地验收，真实PG全量 881.522 秒、专项 39.793 秒。重新登录选择、已选原件/元数据修订、旧草稿升级、受理 COMMIT 丢回执与私有联备均通过。329 项前端、10 项治理、Go vet/test/build、六套冒烟及文档治理通过。
- 通过：V19-B选中图片作为视频输入；`验证两张已批准图片对应实际视频请求字节或文件版本、私有视频资产和原 handle`。024 已在独立源码快照完成全量 PG 916.294 秒，含本机版本化 S3、大恢复、混合负载及三步/四步 Chromium。双原图 multipart 逐字节核对 21.42 秒，独立收费/回放 8.97 秒，浏览器实际解码私有 MP4 16.90 秒，前端 336 项通过。外部供应商未提交生成请求。
- 通过：V19-C显式重跑、撤销与恢复；`PG 025 专项及全量 test:db（含三个 Chromium 工作流、进程强杀、本机 S3、大恢复与混合负载）；npm run check 及六套 smoke`。最终 PG 专项 60.883 秒，含旧未用报价拒绝且仅新报价受理、并发/撤权/背压、真实 COMMIT 丢回执及受理后强杀。独立 025 最终全量 959.929 秒，356 项前端、10 项治理、编译与六套隔离冒烟通过，三步和四步模板、派生与取消均完成本地验收。

证据：[已核对现有草稿、原方案与新流程的验收范围，仅设计证据](v19-contract.md) · [真实升级、备份恢复、浏览器与子受理提交确认丢失](../../internal/studio/workflow_integration_test.go) · [步骤授权、候选与编排提交边界](../wiki/decisions/0023-project-workflows.md) · [实际 multipart 两张原图、跨重启和项目成品播放](../../internal/studio/workflow_video_integration_test.go) · [首帧协议、文件身份与独立视频能力](../wiki/decisions/0024-video-first-frame.md) · [供应端收件后的真实强杀、项目流恢复、操作丢回执与浏览器](../../internal/studio/workflow_attempt_integration_test.go) · [旧未用报价拒绝、派生尝试的独立冻结](../../internal/studio/workflow_attempt_quote_test.go) · [继承、取消、费用与独立运行](../wiki/decisions/0025-workflow-attempts.md)

台账：[2026-11-21-105-workflow-execution-plan](../changes/2026-11-21-105-workflow-execution-plan.md) · [2026-11-21-106-workflow-store](../changes/2026-11-21-106-workflow-store.md) · [2026-11-21-107-workflow-execution](../changes/2026-11-21-107-workflow-execution.md) · [2026-11-21-108-workflow-workbench](../changes/2026-11-21-108-workflow-workbench.md) · [2026-11-21-109-workflow-integration](../changes/2026-11-21-109-workflow-integration.md) · [2026-11-21-111-video-first-frame-provider](../changes/2026-11-21-111-video-first-frame-provider.md) · [2026-11-21-112-workflow-video](../changes/2026-11-21-112-workflow-video.md) · [2026-11-21-113-workflow-video-workbench](../changes/2026-11-21-113-workflow-video-workbench.md) · [2026-11-21-114-workflow-video-integration](../changes/2026-11-21-114-workflow-video-integration.md) · [2026-11-21-115-workflow-rerun](../changes/2026-11-21-115-workflow-rerun.md) · [2026-11-21-116-workflow-cancellation](../changes/2026-11-21-116-workflow-cancellation.md) · [2026-11-21-117-workflow-attempt-workbench](../changes/2026-11-21-117-workflow-attempt-workbench.md) · [2026-11-21-118-workflow-attempt-integration](../changes/2026-11-21-118-workflow-attempt-integration.md)

### V20 视频编辑与分镜创作

状态：**待外部条件**。模块：H02、H03、H04、H08、H10。依赖：V07、V08、V19。

参考素材、分镜、编辑与延长按模型能力开放。

验收条件：

- 参考片段和角色素材归属与参数约束
- 视频编辑/延长API、版本比较与局部重做
- 真实视频解码/播放/转码、导出成片验收

外部条件：本机剪辑、逐镜生成与替换、原片编辑与延长均已验收。恢复真实验收需要供应商开通相应模型和协议、服务器配置、可授权使用的原片及受控测试窗口。按用户安排配置后的真实环境再验。

验证记录：

- 通过：V20-A 现有私有视频的不可变剪辑、原声裁切与实际成片；`Go 视频执行器真实解码与取消、030 存储/权限、Chromium 下载双版本 MP4、独立画面与音轨核对`。030 累计 PostgreSQL 1461.356 秒，无失败或跳过；独立编解码包 3.163 秒。441 项前端及六套冒烟通过。两次从真实 Chromium 下载的 v1/v2 逐字节相同，两段共 125 帧与原声均已核对；停写联备、新库重开、回收/授权另有专项。
- 通过：V20-B 固定分镜生成、原任务收据和单镜替换；`单镜受理与权益、审核资产与剪辑并发、原库升级/联备；真实 Chromium 取回两个 MP4 并独立解码画面和音轨`。使用正常文生视频通路推进受控网关验收。已确认版本、原键回读、单独报价与显式剪辑提交，真实供应商效果以及原视频编辑/延长另验。 已完成冻结来源、原键与权益原子性、037→038 升级、真实浏览器丢失两次应答及停写恢复。两版 MP4 各 54 帧、2250 ms，目标第二镜音调 440→880 Hz。
- 待条件：真实模型对原视频编辑、延长与逐镜生成；`在用户配置且开通对应能力的真实供应端实测`。需要模型协议、授权测试素材和受控真实执行环境；已交付的 V19 首帧视频生成继续独立保留。
- 通过：V20-C 私有原视频的编辑与延长；`开发受理、权限及不明结果门禁，真实浏览器、冻结正文和备份恢复`。039 本机 PG 16.14、FFmpeg、Chromium 与私有版本化对象服务验收。编辑和延长各显式报价并交付，截断正文后用原键 GET。无响应头时页面调用一次、浏览器发出两条同键同正文 HTTP，服务端只建一条任务、一次上游生成和一次结算。停写联备恢复仍取登记的原对象版本。真实模型另验。

证据：[剪辑入口、可配置项、格式、历史版本与私有下载](../wiki/decisions/0030-video-timelines.md) · [两个真实下载件 125 帧逐帧核验及音轨测量](../wiki/assets/video-edit-render-2026-11-22.json) · [030 升级、存储和权限、浏览器、独立解码、恢复与累计回归](../changes/2026-11-22-148-video-edit-integration.md) · [剪辑稿保存回执与选择框同步](../changes/2026-11-22-182-video-timeline-selection.md) · [固定分镜生成与局部重做集成](../changes/2026-11-22-225-panel-video-integration.md) · [已确认分镜的视频报价与受理](../changes/2026-11-22-222-comic-panel-video.md) · [分镜产物替换指定剪辑镜头](../changes/2026-11-22-223-panel-video-timeline-replacement.md) · [工作台单镜生成与原键核对](../changes/2026-11-22-224-panel-video-workbench.md) · [对应固定制品在隔离数据库的串行验收进度。](../changes/2026-11-22-227-local-acceptance.md) · [038 两版私有成片逐帧及音轨解码](../wiki/assets/panel-video-decoded-038.json) · [单镜视频窄屏表单标签对比度](../changes/2026-11-22-229-panel-video-label-contrast.md) · [私有原视频编辑与延长受理](../changes/2026-11-22-235-video-source-admission.md) · [冻结私有原视频的网关请求](../changes/2026-11-23-234-source-video-provider.md) · [原视频操作与停写联备验收](../changes/2026-11-23-238-source-video-integration-restore.md) · [视频工作室选择原片、报价和查询原收据](../changes/2026-11-23-236-source-video-workbench.md) · [真实浏览器的原视频报价与受理恢复](../changes/2026-11-23-239-source-video-browser.md) · [浏览器报价、提交和原收据查询实测](../wiki/assets/source-video-workbench-039.json) · [无响应头时页面、网络、任务、上游和结算次数](../wiki/assets/source-video-transport-039.json) · [039 同源服务、前端、离线工具的本机累计回归](../changes/2026-11-23-243-format-039-regression.md) · [窄屏报价的完整原片编号](../changes/2026-11-23-247-source-quote-narrow.md) · [039 完整回归、实库跳过项与两个原视频浏览器分支](../wiki/assets/format-039-regression.json) · [040 同源程序累计回归。11 月 24 日暂停时已通过 385 项，22 项跳过，数据库与对象存储干净停机。11 月 25 日从新库重验。](../changes/2026-11-23-251-format-040-regression.md) · [本机 040 完整 `npm run test:db` 退出 0，顶层 789 项通过、31 项跳过、0 项失败，核心包耗时 1601.565 秒。受审核回放快照与工作流取消的 submit、query 均在本次全量中通过。039 原库升级到 040、原视频浏览器编辑与延长、断线核对和版本化对象恢复也已通过。](../wiki/assets/format-040-regression.json) · [截至 253 的前端测试时钟、609 项测试和六组冒烟，保留当时的实库状态。](../wiki/assets/format-040-regression-through253.json) · [Windows 重启前 646 项通过、29 项跳过、1 项失败，两项原用例的三次定向复验均通过。](../wiki/assets/format-040-regression-interrupted.json)

台账：[2026-11-22-144-bounded-video-export](../changes/2026-11-22-144-bounded-video-export.md) · [2026-11-22-145-video-edit-workbench](../changes/2026-11-22-145-video-edit-workbench.md) · [2026-11-22-146-video-timeline-store](../changes/2026-11-22-146-video-timeline-store.md) · [2026-11-22-147-timeline-private-export](../changes/2026-11-22-147-timeline-private-export.md) · [2026-11-22-148-video-edit-integration](../changes/2026-11-22-148-video-edit-integration.md) · [2026-11-22-166-video-export-cleanup-verification](../changes/2026-11-22-166-video-export-cleanup-verification.md) · [2026-11-22-182-video-timeline-selection](../changes/2026-11-22-182-video-timeline-selection.md) · [2026-11-22-222-comic-panel-video](../changes/2026-11-22-222-comic-panel-video.md) · [2026-11-22-223-panel-video-timeline-replacement](../changes/2026-11-22-223-panel-video-timeline-replacement.md) · [2026-11-22-224-panel-video-workbench](../changes/2026-11-22-224-panel-video-workbench.md) · [2026-11-22-225-panel-video-integration](../changes/2026-11-22-225-panel-video-integration.md) · [2026-11-22-227-local-acceptance](../changes/2026-11-22-227-local-acceptance.md) · [2026-11-22-229-panel-video-label-contrast](../changes/2026-11-22-229-panel-video-label-contrast.md) · [2026-11-23-234-source-video-provider](../changes/2026-11-23-234-source-video-provider.md) · [2026-11-22-235-video-source-admission](../changes/2026-11-22-235-video-source-admission.md) · [2026-11-23-236-source-video-workbench](../changes/2026-11-23-236-source-video-workbench.md) · [2026-11-23-238-source-video-integration-restore](../changes/2026-11-23-238-source-video-integration-restore.md) · [2026-11-23-239-source-video-browser](../changes/2026-11-23-239-source-video-browser.md) · [2026-11-23-243-format-039-regression](../changes/2026-11-23-243-format-039-regression.md) · [2026-11-23-247-source-quote-narrow](../changes/2026-11-23-247-source-quote-narrow.md) · [2026-11-23-251-format-040-regression](../changes/2026-11-23-251-format-040-regression.md)

### V21 协作与专项创作扩展

状态：**进行中**。模块：H01、H02、H08、H10。依赖：V08、V11、V19。

团队协作、漫剧流程、PPT可交付文件；分别子批次验收后再开放。

验收条件：

- 成员/邀请/项目角色和协作冲突处理
- 漫剧角色/场景/分镜复用链路
- PPT导出实际可编辑文件与渲染验收；不以入口代替功能

验证记录：

- 通过：V21-C 可编辑文稿、私有图片与真实 PPTX 导出；`真实账号、版本回读与鉴权、Chromium 下载 PPTX、独立解析与渲染原下载文件`。029 全量 PostgreSQL 1326.832 秒，包括浏览器再次下载 10.49 秒。真实专项 46.647 秒，426 项前端、构建和六套冒烟通过。两个修订及首版重开、标题改写回存与 15 页独立打印均核验。
- 通过：V21-A1 站内团队邀请、共享项目角色与多人草稿冲突；`双账户真实 PG/Chromium、成员原键、撤权竞态、旧库升级和独立还原`。032 十四项专项 58.729 秒通过，双账户实际浏览器 14.95 秒、恢复 7.20 秒。471 项前端及六套冒烟通过。 032 全量 PostgreSQL 1639.820 秒通过，无失败或跳过。
- 通过：V21-B1 漫剧角色、场景与分镜复用及可编辑稿；`真实账户与 PG、保存/确认原操作键、旧版私图权限、Chromium 下载三份固定稿与长文续页、独立编辑和渲染`。031 专项 50.116 秒，双版本、作者确认、保存竞争、生成与回收门禁和联备恢复通过。真实下载件在独立编辑器重新打开，5 份 PDF、14 页核对通过。458 项前端和六套冒烟通过， 032 全量 PostgreSQL 1639.820 秒通过，无失败或跳过。

证据：[项目内不可变页数据与导出权限](../wiki/decisions/0029-project-presentations.md) · [用另一套 PPTX 库重开浏览器下载、编辑后核对原件字节](../../scripts/verify-presentation.py) · [双版本 PPTX 独立编辑、嵌图哈希及 15 页可读 PDF](../wiki/assets/presentation-export-2026-11-22.json) · [角色、场景、逐镜编写与作者确认](../wiki/decisions/0031-project-comics.md) · [真实下载、可编辑内容与原私图的核对](../changes/2026-11-22-152-comic-integration.md) · [真实下载的原生页、可编辑标题、私图与长对白续页验收](../wiki/assets/comic-export-2026-11-22.json) · [个人会话与授权项目的共编操作](../wiki/decisions/0032-project-collaboration.md) · [多账户原稿、版本与权限恢复](../changes/2026-11-22-156-project-collaboration-integration.md)

台账：[2026-11-22-135-presentation-store](../changes/2026-11-22-135-presentation-store.md) · [2026-11-22-136-presentation-http-export](../changes/2026-11-22-136-presentation-http-export.md) · [2026-11-22-137-presentation-workbench](../changes/2026-11-22-137-presentation-workbench.md) · [2026-11-22-138-presentation-integration](../changes/2026-11-22-138-presentation-integration.md) · [2026-11-22-149-comic-store](../changes/2026-11-22-149-comic-store.md) · [2026-11-22-150-comic-private-export](../changes/2026-11-22-150-comic-private-export.md) · [2026-11-22-151-comic-workbench](../changes/2026-11-22-151-comic-workbench.md) · [2026-11-22-152-comic-integration](../changes/2026-11-22-152-comic-integration.md) · [2026-11-22-153-project-collaboration-members](../changes/2026-11-22-153-project-collaboration-members.md) · [2026-11-22-154-shared-project-draft](../changes/2026-11-22-154-shared-project-draft.md) · [2026-11-22-155-project-collaboration-workbench](../changes/2026-11-22-155-project-collaboration-workbench.md) · [2026-11-22-156-project-collaboration-integration](../changes/2026-11-22-156-project-collaboration-integration.md)

### V22 公开收费发布验收

状态：**进行中**。模块：H01、H03、H04、H05、H06、H07、H08、H09。依赖：V07、V08、V10、V13、V14、V15、V17、V18。

商业核心发布闸门，专项V20/V21可后续独立发布。

验收条件：

- 支付退款/审核/隐私规则/客服与数据导出真实闭环
- 安全测试、容量/灾备和供应升级回归全部留证
- 部署灰度、回滚演练与运营移交完成再开放公众

验证记录：

- 通过：V22-A 账户资料、安全事件及本人申诉分页下载；`真实 PostgreSQL 两账户密码/会话核验、受限账户及 Chromium 下载四份 JSON 与数据库逐项核对`。集成人对冻结的 033 运行真实账户及 PostgreSQL 专项。会话撤销与账户分区 4.54 秒，申诉隔离与强转义字节分页 4.37 秒，双账户 Chromium 下载 10.11 秒。四份文件分别包含一份本人资料、100 加 12 条安全事件及一份受限账户本人的申诉；数据库逐条核对顺序、范围和原始申诉正文通过。 固定 035 完整 PG 通过，studio 1670.146s。
- 通过：V22-B 客服工单、本人追问和客服公开答复；`两侧真实账户、独立客服授权、访问隔离、原收据、并发和联备恢复`。034 工单专项十项通过，共 48.800 秒。三账户真实 Chromium 10.69 秒，停写撤权后在新库恢复 10.57 秒，四种操作丢失提交确认后的原键回读共 11.57 秒。浏览器核对四个版本及原收据，数据库确认追问和答复各自的操作者，权益与媒体均无新增。 518 项前端及六套冒烟通过。 固定 035 完整 PG 通过，studio 1670.146s。
- 通过：V22-C 工作台固定制品依赖与许可证证据；`离线 Go 和 npm 锁核对、真实重建 dist 比对、SPDX 2.3 官方 Schema、篡改文件拒绝`。034 和 035 各有独立制品清单，均为45包47关系、33个前端输出逐字节一致。035另核对整批实际运行的服务与前端。实际网关、部署镜像、第三方通知及最终分发审查待责任方确认。 另以同一冻结源码逐字节复现 037 服务与 55 个前端文件；SPDX 2.3 独立核对 45 包 47 边、20 对 gzip、22 个 npm 归档内的 434 份安装文件。完整 PG 的制品范围另见 215。
- 通过：V22-D 本地安全与渠道升级证据复核；`032—034 四份已执行日志的 25 个具名用例与同名源测试和制品证据逐项复核`。覆盖会话撤权、权限隔离、数据导出、资金只读、客服及渠道兼容。真实供应商版本、生产升级验证和责任方签收仍待补充。 另以同一冻结源码逐字节复现 037 服务与 55 个前端文件；SPDX 2.3 独立核对 45 包 47 边、20 对 gzip、22 个 npm 归档内的 434 份安装文件。完整 PG 的制品范围另见 215。
- 待条件：公开发布、真实外部渠道、隐私、容量灾备与运营移交；`真实商户/供应商验收、隐私规则确认、完整容量与灾备、受控灰度及运营交接`。需要相应外部环境、渠道配置、实际责任人与发布授权。

证据：[下载入口、数据范围与分页时效](../wiki/account-data-export.md) · [普通及受限账户的浏览器与数据库核对](../changes/2026-11-22-164-personal-data-export-integration.md) · [工单状态与客服授权](../wiki/decisions/0034-support-tickets.md) · [三账户工单、撤权和新库联备恢复](../changes/2026-11-22-170-support-ticket-integration.md) · [本机034制品与锁定依赖许可证据](../wiki/assets/workbench-034.spdx.json) · [复验与真实网关、许可缺项](../wiki/release-dependencies.md) · [本地安全与外部升级验收的证据范围](../wiki/release-security-evidence.md) · [035 固定服务与工作台制品的 SPDX 2.3 清单](../wiki/assets/workbench-035.spdx.json) · [037 制品与 SPDX 复核](../changes/2026-11-22-215-release-evidence-037.md) · [037 制品来源和验证边界](../wiki/release-evidence-037.md) · [037 的 SPDX 2.3 清单](../wiki/assets/workbench-037.spdx.json) · [037 机器核对摘要](../wiki/assets/workbench-037-verification.json)

台账：[2026-11-22-162-account-data-export](../changes/2026-11-22-162-account-data-export.md) · [2026-11-22-163-account-data-export-workbench](../changes/2026-11-22-163-account-data-export-workbench.md) · [2026-11-22-164-personal-data-export-integration](../changes/2026-11-22-164-personal-data-export-integration.md) · [2026-11-22-167-support-ticket-store](../changes/2026-11-22-167-support-ticket-store.md) · [2026-11-22-168-support-ticket-access-http](../changes/2026-11-22-168-support-ticket-access-http.md) · [2026-11-22-169-support-ticket-workbench](../changes/2026-11-22-169-support-ticket-workbench.md) · [2026-11-22-170-support-ticket-integration](../changes/2026-11-22-170-support-ticket-integration.md) · [2026-11-22-178-release-sbom](../changes/2026-11-22-178-release-sbom.md) · [2026-11-22-179-local-release-security-evidence](../changes/2026-11-22-179-local-release-security-evidence.md) · [2026-11-22-215-release-evidence-037](../changes/2026-11-22-215-release-evidence-037.md)

### V23 工作台四语与首次访问地区识别

状态：**已验收**。模块：H08、H09。依赖：V00。

中文简繁、英文和日语的站内界面，按访问 IP 设置初始语言，记忆手动选择。

验收条件：

- 按大陆、港澳台、日本、其他地区选择对应语言，覆盖 IPv4 和 IPv6。
- 登录前后可切换语言；刷新保持手动选择，返回自动模式按地区重新判断。
- 本地验证切换时保留输入及原请求，检查语言包、构建、测试及文档同步。

验证记录：

- 通过：Go 地址归属、代理与服务回归；`go test -p 1 -count=1 ./...`。全包通过；隔离真实配置且未设置 TEST_DATABASE_URL。IPv4/IPv6、受信代理、伪造头、登录前接口、数据表完整性与内置描述身份均有专项断言。
- 通过：界面文案、偏好和交互回归；`npm --prefix apps/workbench test -- --maxWorkers=3`。56 文件、606 项通过。校验三份词典的键与占位符、并发选择、存储异常、鉴权提示、原请求和画布首存基准。
- 通过：静态制品与服务构建；`tsc --noEmit && vite build --outDir <独立临时目录>；go build -o <临时服务> ./cmd/server`。严格类型检查、Vite 生产包和 Go 可执行文件成功。三份词典按选定语言加载。
- 通过：真实浏览器地区与手动选择；`node apps/workbench/scripts/locale-browser-smoke.mjs`。本机 demo 服务、独立私有目录、LOCALE_TRUSTED_PROXIES=127.0.0.1/32、LOCALE_BROWSER_URL 指向该服务。六个地区地址首访、移动端切换保留输入、刷新跳过地区检测、回到自动及受控未登录会话的登录与找回均通过。
- 通过：TODO 与文档治理；`npm run todo:sync && npm run check:todo && npm run check:docs && npm run check:changes`。任务源、生成入口、台账及相关 Wiki 同步。

证据：[五个地区、其他地区、IPv6、代理链及数据完整性](../../internal/localeip/locale_test.go) · [地区地址数据与摘要](../../internal/localeip/SOURCES.txt) · [公开只读协议和模型文案来源](../../internal/studio/locale_http_test.go) · [新浏览器上下文下的地区和语言全流程](../../apps/workbench/scripts/locale-browser-smoke.mjs) · [词典、竞态及浏览器存储回归](../../apps/workbench/src/locale.test.ts) · [四语规则与可信代理配置](../wiki/frontend.md)

台账：[2026-11-22-226-workbench-languages](../changes/2026-11-22-226-workbench-languages.md)
