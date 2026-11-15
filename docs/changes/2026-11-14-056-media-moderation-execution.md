# 056 · V14-B2 媒体受理与整组交付隔离

日期：2026-11-14。关联V14-B2、H01/H03/H04/H05/H06/H07/H08/H09。055负责引擎/016，057负责前端，058负责根集成；完整V14仍进行中。

## 目的

将生成前prompt/参考原件检查与生成后整组媒体放行接入真实Task、租约、持久化和权益链，防止私有导入完成被误当成用户已经取得作品。

## 范围

原键优先、锁外前置审核及重入摘要验证；受理同事务冻结媒体与文本政策；私有候选先落库、完整字节整组检查、最终有租约条件的事务交付；取消、重启、未知提交和账务防御。默认关闭，启用须PG账户及文本审核。同步参考上传、库/下载门禁及GC由058负责，策略/读取helper及016由055负责。

## 实际文件

- `internal/studio/app.go`、`media_moderation_execution.go`：配置接线、两阶段受理、实际引用检查、私有候选和整组最终决定。
- `internal/studio/model.go`、`moderation.go`、`http.go`：Task安全摘要及公开产物投影，bootstrap媒体能力。
- `internal/studio/persistence.go`、`repository_queries.go`：私有冻结记录加载、候选恢复与受理/状态事务helper接线；后者的资产授权修改归058。
- `internal/studio/task_leases.go`、`task_leases_worker.go`：领取加载私有记录、候选阶段fence、续租覆盖检查、提交后缓存发布、候选重领隔离。
- `internal/studio/ledger_hooks.go`、`operations_cases.go`：审核决策与可交付目录/权益同事务，运营路径读取候选身份且不解除内容隔离。
- `internal/studio/media_moderation_execution_test.go`、`media_moderation_commit_test.go`：普通摘要/副本测试及PG前置拒绝、图像/视频结果、混合整组、事务失败、取消/租约/重启、真实提交确认丢失测试。
- `docs/wiki/media-moderation-execution.md`与本台账。共享配置入口、README/索引/任务源、OpenAPI和总体契约由根统一维护。

## 接口与数据影响

Task新增可选`moderation`，PENDING v1到终决策v2，公开产物须ALLOWED且SUCCEEDED/PARTIAL。候选登记后的业务状态为MODERATING、无公开Artifacts；不新增公开放行/重审API。bootstrap增加media_mode及媒体政策标识，两引擎开启才支持新受审媒体。

输入拒绝不创建Task、冻结或消费报价；前置媒体错误码、状态及重新核对边界见[执行Wiki](../wiki/media-moderation-execution.md)。原HTTP幂等hash不变；额外摘要覆盖规范化规格、私有上游模型与有序引用字节身份。

候选内容保存在私有存储，016私有表保存不可变元数据及政策，不把媒体Base64放进Task。最终审核决定、任务、可交付目录、账务和Outbox同事务；决策写入/租约/提交失败不能提前发布缓存。已开始的隔离结果保持REVIEW；财务处置不改变审核结果。

## 验证结果

- 普通专项`go test ./internal/studio -run '^(TestMediaModerationCopy|TestModerated|TestPostgresMediaModeration)' -count=1`通过0.291秒；未设置TEST_DATABASE_URL，PG部分明确跳过，不作为数据库证据。
- 根串行执行首轮5个`TestPostgresMedia*`函数及子例，通过16.626秒：包含本模块3组图像/视频ALLOW/BLOCK/REVIEW及权益、旧参考/文本前置拒绝、候选取消/租约过期/重启；另含根参考/访问/GC/审计与055持久helper。结果由根执行并提供，详见[058](2026-11-14-058-media-review-integration.md)。
- 增补的混合ALLOW+BLOCK整组隔离、决策历史插入失败回滚，根串行PG专项分别通过1.34/1.30秒，包含未交付、无CHARGE、原候选与PENDING保留和缓存/SSE不发布的断言。
- `TestPostgresMediaModerationUnknownCommitRecoversOnlyCommittedDecision`真实PG通过3.245秒，含candidate/ALLOWED/BLOCKED三子例：真实lease claim及生产fenced状态提交，TCP代理在数据库完成COMMIT后丢确认，缓存/SSE不提前公开，重启只恢复已提交决定，原键零新Submit/Query。第一次根运行因fixture把多条lease连接都接到单连接代理而超时；修正为独立lease直连同一测试库、仅控制写连接故障注入后，本模块在根授权独占PG窗口复验通过。未修改产品核心绕过该测试。
- 新测试普通编译专项通过0.064秒（PG跳过）；`go vet ./internal/studio`通过。`npm run check:docs`当时仅报告根待更新的056/057索引覆盖，无断链。最终全量及治理结果由058统一收录。
- 根全量`npm run test:db`通过（studio192.482秒，含修正后ACK三子例），六套smoke通过；此为根提供的结果，见058。随后按根复核要求增补同一WholeBatch函数中的`image-partial-all-allowed`：请求2项、实际私有导入/审核/交付1项，Task与持久摘要ALLOWED/PARTIAL，冻结200、结算100、释放100，目录仅1项可下载，原键重放不再Submit/Query。在独占窗口仅复验该函数，7个子例全部通过1.745秒。该增量只改测试，不改产品核心。

## 风险与限制

本地精确字节fixture不识别未知媒体语义；真实审核商未选型，HTTP为平台自有适配契约。整组首次非ALLOW即隔离，不提供局部发布。已有候选审核中断后不自动重审，人工内容放行仍未实现。原handle但尚无候选的异常查询可恢复，不能把它描述成旧隔离内容重审。候选必须继续占用存储并保留可达性，不能以任务失败推断可删。控制写者、PG串行写路径与既有容量限制不变。

## 回滚方式

关闭当前审核只改变新请求，不能放行既有BLOCKED/REVIEW或重发原生成。016升级及回退需数据库与私有目录/对象联备和匹配程序；禁止删候选/决策/迁移登记/owner绕过边界。不直接将数据库摘要改为ALLOWED，不用财务释放冒充内容批准。

## 文档同步

[执行Wiki](../wiki/media-moderation-execution.md)互链[媒体引擎](../wiki/media-moderation.md)、[媒体工作台](../wiki/media-moderation-workbench.md)与058。根统一更新README、Wiki/台账索引、任务源/TODO及[冻结契约](../planning/v14b2-contract.md)，本台账保留真实渠道待验与V14完整范围尚未完成的事实。
