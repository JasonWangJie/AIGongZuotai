# 083 · V16-A3.3媒体公平调度集成

## 目的

承接079已验收首次审核分派，推进媒体工作区公平、持久渠道退避和审核许可公平。关联V16/H03/H07/H09，本批2026-11-15开始实施，尚未验收。

## 范围

根负责人负责配置、参考上传、固定观测、版本/API、联合验证与总文档。核心080、HTTP与许可081、独立维护验证082分别记录其实际变更；完整验收和归属见[A3.3契约](../planning/v16a33-contract.md)。

## 实际文件

当前新增本台账与docs/planning/v16a33-contract.md；同步README.md、docs/wiki/README.md、docs/changes/README.md、docs/planning/tasks.json、docs/planning/progress.md和生成双TODO。后续实现文件及证据在本节追加，不把计划文件冒称已经实现。

## 接口与数据影响

计划020私有调度格式，不更改公共生成请求和旧冻结请求；新增两个有界配置。四lane和共享审核原两个许可保持，所有worker仅在真实领取/STARTED提交确认后进行外发；原幂等、权益、隔离和审核决定不被调度层改写。

## 验证结果

本批联合验证尚未完成。079的全量PG368.160s、301前端、10治理、六smoke和性能均为A3.2历史证据，不能复用为A3.3验收。

- 首轮隔离普通Go回归20.523s，唯一失败为新增scope实际占用指标，运行时memory公平接线尚未完成；保留断言，待接线完成复验。
- 新参考PG首次未进入数据库，核心接线期间出现time未使用编译错误；作者完成后重跑。
- `node .runtime/v16a33-check.mjs target '^TestPostgresMediaSchedulerReferenceQueueRejectsWithoutRegistrationAndReplaysFirst$'`通过1.603s（函数1.57s），真实020启动/HTTP审核、429无登记/原键优先、私有身份不入指标、取消等待不释放他人许可。
- 中间todo:sync和check:changes通过177文档/83台账/366路径；后续代码仍在变化，不代表最终治理收口。

## 风险与限制

仍是单控制写者，当前只配置单个Async网关；本批不自动接通真实收费渠道，不宣称多供应商路由或生产容量。公平队列、半开探测、投影一致性及大积压成本必须独立验证。

## 回滚方式

未应用020的环境可保留已验收019版本；应用后只能使用匹配程序或完整停写联备恢复，不删除调度/业务证据、迁移登记或owner绕过栅栏。本批只操作独立测试库，不改用户原开发数据库。

## 文档同步

集成人维护README/Wiki和台账导航、任务源及双TODO；080—082分别维护自身模块说明。当前V16继续in_progress，A3.3 check pending、completed_on为空，其他产品批次保留。

### 根接线进展

实际新增两个严格环境变量，配置缺省继续交给Config归一化，显式空/零/非十进制/越界拒绝且错误不回显原值；更新cmd/server/media_execution_config.go及原测试、.env.example、scripts/lib/process.mjs隔离值。references.go锁外先取同工作区公平输入许可，再打开并核验原件；队列错误原样返回，重入仍优先读原幂等。

新增internal/studio/media_scheduler_observability.go，并在observability.go接入固定四lane和两个审核类别；media_lane_observability_test.go保留原断言并增加实际scope预算检查。新增media_scheduler_reference_test.go验证真实PG/HTTP参考上传满队列拒绝无登记、原键仍重放、固定指标无私有身份和取消等待清理；这些新测试尚未运行。

package.json、apps/workbench/package.json及其package-lock.json、docs/api/openapi.yaml只更新0.20.0元信息，无依赖或公共路径变更。原19SQL摘要单独冻结用于最终比对。

文档启动脚本首次在Wiki当前状态精确替换处因旧文字不同停止；先核对已写版本/README部分，再仅续跑未执行后半段，无重复新增。首次check:changes发现并行创建080—082暂未入索引，已补索引及任务源，待复验；这些只是中间治理结果，不冒称功能测试。

### 集成与大数据量验证进展

严格环境配置根测试通过0.030s。独立PG三函数通过7.970s（工作区公平/取消2.17s、真实429重启4.52s、019升级/维护1.24s）；其源码小夹具后把旧非公开PROCESSING状态改为实际RUNNING，最终全量将重覆盖。

首轮大组未入数据库，核心接线期间checkMemoryReconcileSchedule尚未定义导致编译失败。随后稳定快照的大组194.902s：180条真实custom/generic/auto SQL计划通过173.83s（10k+10k14.12s、100k+100k159.68s），但200k旧019迁移在5.0542081s拒绝（函数21.03s）。定位为openPostgres沿用日常s.do的5秒预算，不能把外层连接10秒误当迁移上限。

核心将启动迁移分离为60秒有界预算，日常业务仍5秒；020采用集合回填并在批量插入后建立二级索引。仅大升级复验通过30.741s（函数30.70s），实际迁移11.2288314s，59个原表摘要/计数保持；夹具原019先按真实旧部署包含默认项目/工作区，比较不豁免合法行。此结果不是完整App加载20万任务或生产容量。

root新增media_scheduler_restore_test.go，实际pg_dump/pg_restore联备PG、两私有目录/owner及交付key；真实429的server_not_before、任务due、渠道冷却/probe_required和四张020表保持，重开严格原ID及轮转都不提前外发，原键只读原任务。新专项通过4.545s（函数4.51s）。在两份已有delivery/review恢复测试增加四张调度表的备份比对；这些增强待联合复验，不改变旧收据/对象版本/权益断言。

deliveryStep和reviewDispatchStep旧测试helper改为显式推进普通poll时间，复用核心仅测试advanceMediaScheduleDue；该helper拒绝缩短服务端deadline、渠道冷却/probe和有效lease，生产按ID执行不再绕due。新恢复测试初写引用不存在的fixturePassword被编译发现，已改为既有测试注册凭据；第二轮普通全组仍在核心errorsAsOperation接线期间构建失败，studio未运行，不声称普通全组已通过。

独立只读复核发现真实下载成功缺渠道恢复事实、整组后件失败沿用首件时间及历史身份冲突静默反复排队；081/080分别修正并补测试，尚待root最终验证。原180计划只证明statement成本和有界推进，082追加完整预选耗时/QueryTracer次数后将单独复验。

### 补充独立复核与最终验证准备

核心8项真实PG测试通过12.897s，覆盖严格到期、单探测竞争、长deadline人工核对、投影回滚、游标/领取COMMIT确认不明和Outbox背压保存原观测。身份冲突隔离/重启与整组迟到429两项通过6.864s；真实下载429后8次实际成功恢复限额通过5.101s。新的query/delivery/review三lane跨工作区真实HTTP公平测试通过6.220s（1.90/2.03/2.25s），使用生产空ID预选，不手工指定B任务绕过调度。

第三轮普通全组25.477s失败只在原lane隔离夹具：同一workspace原先预期两个submit同时执行，新默认workspace上限1正确阻止。给该旧夹具显式设置原测试需要的每workspace/channel上限，保留旧隔离/峰值/调用断言；新公平测试继续使用1。与增强local恢复、review READY/STARTED恢复联合复验19.979s通过，此轮S3子项明确skip（未启用），不是S3验收。新增cooldown恢复强化为App.New之后仍比较完整渠道冷却、probe、版本和有效限额，原4表原样比对继续保留。

最终大积压专项通过174.770s（函数174.73s，10k+10k14.89s、100k+100k159.82s），实际180条EXPLAIN加48次完整生产预选，原始报告保留；单句计划和整轮预选分别统计，完整预选含游标事务与channel/lease读取但不含claim或HTTP。随后独立审查发现缺渠道行FOR UPDATE不能锁缺行的竞态，核心正在添加健康基线行锁及独立连接竞争测试；不更改selector或020 SQL，不将旧测量冒称领取竞争测量。

真实SeaweedFS4.53由根隐藏启动，复用已保留的版本化测试bucket但使用新随机前缀，八个监听只在127.0.0.1。签名PUT两版本、分别按VersionId完整GET、当前GET/Range及匿名403通过；原A3.1/A3.2对象和配置哈希保持。启动脚本被执行后代理追加了静态路径检查，根另外检查实际PID、启动时间、二进制SHA与全部保留路径，明确不把追加检查说成启动前已执行。尚需本批全PG启用S3及结束后的身份核对停机；不删除原数据。

根补齐architecture/development/database-migrations/object-storage/observability说明、Wiki导航和OpenAPI当前能力描述；新增ADR0018。迁移60秒和11.229s成绩不证明App全活跃任务加载完成，现有恢复N+1及5秒边界列入V16后续，不暗中放宽普通事务超时。

### 首轮全量回归发现的兼容问题

完整隔离check已通过：Go studio20.299s、301前端/29文件、10治理、vet与前后端构建（Vite1.81s）。首次全PG启用实际版本化S3及pg_dump/pg_restore，453.333s失败：408个顶层函数通过、13个失败；两项大积压专项仅按opt-in明确跳过，其独立真实结果不受影响。原完整日志另存v16-a33-db-full-first.log，后续不覆盖此失败事实。

失败包括旧显式step未推进普通到期（catalog/参考编辑/旧交付/治理原handle等）、旧同步审核许可测试的等待假设、一个手工任务夹具遗漏provider mode，以及私有候选保存的数据库错误待查。所有旧业务断言保留，禁止用跳过旧测试或缩短服务端冷却通过；由三方分文件定位，PG仍仅由根串行执行。当前仍pending，不因为常规check通过就标本批完成。

旧兼容复验：五项审核用例通过13.092s，六项旧链路（含真实S3）通过11.105s；只在明确下阶段前推进普通due，原assert保留。输入占满审核许可用例改为异步观察同一worker等待期间READY/零外发，释放输入后首次执行，避免用输入HTTP超时释放模拟公平。手工准入夹具明确真实provider mode后通过1.30s。

私有候选问题为真实并发缺陷：初步两函数诊断4.148s仍失败，随后单目标2.793s/2.735s重现SQLSTATE40P01。仅临时内存捕获首错并在测试提取固定类型/锁拓扑，不输出提示词、绑定值或私有URL。循环为selector持lane锁后等workspace，而业务持workspace后等待外键KEY SHARE；正在调整游标锁级别并补确定性双连接测试，禁止用自动重试吞掉死锁。020 SQL保持，完整预选包含锁操作，因此修复后重跑180计划和48预选，早先174.770s报告只保留为修正前历史。

锁修复后三项PG通过4.616s：原准入1.31s、原高频私有候选1.80s、新确定性双连接1.47s。仅两处游标锁改`FOR NO KEY UPDATE`，临时诊断全部移除；业务游标仍串行，不绕过外键或新事务条件。重新执行180计划/48完整预选通过170.780s（14.34/156.39s）；100k组24完整样本中位9.64395ms、P95 16.3019ms、最大16.6752ms，48游标均推进。新原始报告621201字节、SHA256 e4bbd2516c52c50bb9c59018758782f62d87bca1b5a1002b47fceca6687dff4b，先前报告不覆盖。020及001—019、API引用再核对不变；继续最终check和全PG，不以定向结果代替全组。

### 最终联合验收

根串行全PG启用实际版本化S3和pg_dump/pg_restore，通过 **443.228s**。最终完整check通过：Go studio20.350s、301前端/29文件、10治理、vet与构建（Vite1.64s）。所有失败/中间记录保留；末次源码冻结后才执行最终组。新增首次渠道锁测试6.062s和参考取消3.557s均通过；取消错误映射由081负责，保持原键二次回查优先。

六smoke在父环境故意注入12项无效执行配置及无效真实渠道配置时通过，证明隔离不继承这些值；同构建性能bootstrap P95 19.26ms、创建取消P95 11.37ms、demo首delta P95 1761.68ms。最终二进制SHA256 `276e3455c2cfc31cfb131be9ac177165ff1b6e368161b9f1b92b90398ff39923`与五份smoke及性能JSON一致。性能在PG全组及本批S3退出后运行，媒体仅创建取消，不是交付或审核吞吐。

根新增/变更实际文件归属：`internal/studio/media_scheduler_observability.go`、`media_scheduler_reference_test.go`、`media_scheduler_restore_test.go`，`observability.go`、`media_lane_observability_test.go`、`media_lane_integration_test.go`、`media_delivery_integration_test.go`、`media_review_dispatch_integration_test.go`、两份既有delivery/review恢复test；配置入口/测试、`.env.example`、`scripts/lib/process.mjs`、`scripts/load.mjs`、两份package.json与前端锁文件、OpenAPI元信息；README/AGENTS/Wiki/ADR0018/契约/任务源及双TODO。references.go最初接线属083，取消窄修与新test归081，完整范围见各台账。

最终[联合证据](../wiki/assets/media-scheduler-validation-v0.20-2026-11-15.json)、[020/API](../wiki/assets/media-scheduler-schema-v0.20-2026-11-15.json)、[S3](../wiki/assets/media-scheduler-s3-v0.20-2026-11-15.json)和[180计划/48预选原始报告](../wiki/assets/media-scheduler-plans-v0.20-2026-11-15.json)已归档。001—019共61318字节保持，020共9207字节，71路径/83操作/95schema/411引用有效。schema helper初用捆绑Python缺yaml，切换已安装Python3.12后通过，没有安装依赖。

A3.3 check标passed，完整V16仍in_progress、completed_on为空；11/23整批保持。完整App恢复N+1和Outbox启动背压、持久流与fencing及综合测量继续推进。真实渠道、云灾备、生产容量、CGO race、远程CI和新浏览器视觉均不在本批已验范围。

兼容复验实际文件补记：`internal/studio/asset_library_test.go`、`asset_storage_library_test.go`、`catalog_test.go`、`generation_admission_test.go`、`media_delivery_upgrade_test.go`、`references_test.go`只增加普通due推进；`media_moderation_execution_test.go`、`review_dispatch_commit_test.go`及`media_review_dispatch_integration_test.go`保留原断言并适配阶段到期/异步许可等待。`admission_test.go`真实mode夹具、`media_moderation_access_test.go`原高频及确定性死锁回归、selector两处锁修正归080。测试没有改短服务端deadline，也没有删除原测试。归档脚本明确断言10条恢复/S3父子路径PASS、禁止相关SKIP后才写验收结果。

最终治理：todo:sync/check:todo通过（10模块、23批、11整批已验）；check:changes通过181文档/83台账/398变更路径；test:todo 10/10通过，git diff --check通过。020/API在最终文档更新后再校验通过。AGENTS补充游标锁与外键兼容规则，未放宽旧事务或执行门禁。
