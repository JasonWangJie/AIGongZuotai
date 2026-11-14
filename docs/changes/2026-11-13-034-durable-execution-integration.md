# 034 · V09持久事件与任务租约联合验收

日期：2026-11-13。版本：0.8.0。批次V09，横向H01/H03/H06/H07/H08/H09。031—033记录领域实现，本记录负责根集成和独立最终验收。

## 目的

把业务事务、可恢复通知、媒体领取与前端补齐连成可验证链路，避免宕机漏通知、过期执行者覆盖新状态和恢复时重复调用付费模型。

## 范围

Outbox/持久feed、租约与generation隔离、有界客户端恢复、服务端配置和指标、实际进程故障测试、旧开发库升级、浏览器验收与文档治理。维持单控制写者及私有目录锁，本批不放行全站多实例、逐token耐久日志或公众收费运营。

## 实际文件

- `cmd/server/main.go`、`cmd/server/execution_config_test.go`：严格解析TASK_LEASE_SECONDS，默认30、十进制5—300；非法值不回显原配置。
- `internal/studio/observability.go`、`execution_observability.go`、`execution_observability_test.go`：固定动作标签的持久通知/租约进程计数，8并发累加与隐私负例。
- `internal/studio/storage_maintenance.go`：维护工具格式检查与计划摘要要求001—009，保留007数据库UUID/目录身份；未知格式拒绝维护。
- `internal/studio/durable_process_test.go`：真实子进程服务、实际HTTP供应商fixture、Kill/重启、原键及重放游标联合测试。
- `.env.example`、`package.json`：租约参数及根0.8.0；前端版本和锁文件由033维护。
- `docs/api/openapi.yaml`：工作区持久SSE、opaque游标、reset/元数据schema、背压错误与Audit资源类型补齐；仍36路径46操作。
- `AGENTS.md`：事件事务、资源版本/通知序号、背压、租约失效隔离、单写者边界写入规则。
- `docs/wiki/decisions/0006-durable-events-and-task-leases.md`、`architecture.md`、`backend.md`、`persistence-auth.md`、`storage-maintenance.md`、`observability.md`、`performance.md`、`roadmap.md`：决策、配置、升级与下一步。
- 根README、Wiki/台账索引、`docs/planning/tasks.json`与生成`TODO.md`；`docs/wiki/assets/`的v0.8五份运行报告、浏览器JSON及三张PNG。
- Outbox、任务worker、客户端源码分别列在031—033，根复审和全量验证覆盖其最终版本。

## 接口与数据影响

008引入同事务Outbox、项目counter、持久feed和checkpoint；009引入任务owner/epoch/until与控制generation。项目通知seq与Task.Version/Run.Seq分离；消费checkpoint不是浏览器ACK。公开事件仅资源元数据，无提示词、输出、名称标签、上传正文或私有凭据。

`EVENT_BACKLOG_FULL`表示本次业务和事件事务一起回滚；未消费事件不删除，消费者继续排空。上游已完成时只重试保存原结果，不能重新调用模型。COMMIT确认丢失仍视为受理不明，停止写者后从持久状态恢复，不推断回滚。无handle的SUBMITTING过期或重启只进入待核实。

PG工作区流发送resource.changed、stream.reset及兼容task.updated；opaque游标绑定数据库/用户/工作区/项目。memory事件仍不跨重启持久，run专用token流仍使用原数字seq。客户端补读只GET，保留编辑/未知请求，分批扫描已加载非终态资源；未浏览的全量历史不自动加载。

`studio_durable_events_total`及`studio_task_leases_total`是固定标签的进程计数，不代表全局待消费数、浏览器确认或集群所有权。控制业务连接仍持advisory lock，固定worker辅助连接只领取/续约；目录排他锁未取消。

## 验证结果

### 根独立后端与故障验收

- `npm run test:db`最终通过：studio66.419秒、cmd/server0.032秒。启用真实PostgreSQL18.7隔离测试schema，覆盖原业务及新增Outbox、租约、实际进程故障。仓库现有155个Go测试函数；未配置的真实S3专项、helper直调及Windows受限项仍按自身条件skip，不能说155项全部实际执行。
- `TestPostgresCrashAfterSupplierAcceptanceDoesNotResubmitAndReplaysEvents`单独通过1.651秒并包含于最终全量：供应商fixture收到POST后不返回，子进程已持久SUBMITTING；实际Kill后重启得到SUBMIT_UNKNOWN；原幂等键返回原任务；崩溃前SSE cursor补收到恢复通知。两进程合计只有1次供应商POST。
- 首次代理全量仅新子进程fixture漏配AsyncImageCredentialID失败；补齐私有测试身份后专项通过。根首轮全量62.492秒通过，独立审阅修正缓存边界后再跑最终66.419秒；不把失败运行标记成功。
- 031实际验证8个PG函数及普通游标子例：事务回滚、并发提交序号、10000 pending背压/排空、2048窗口、重复消费/重启、producer和consumer真实COMMIT确认丢失、scope拒绝、reset不能越过未投影pending。
- 032最终租约专项13函数（2普通+11PG）10.457秒通过：8独立连接竞争、续租/失锁、epoch/generation/版本拒绝、取消/核对、实际无缓存worker、过期提交停放及媒体/LLM保存背压不重发。根最终全量包含这些测试。
- 根配置/指标专项通过cmd0.101秒、studio0.072秒；严格数字/边界/敏感值不回显，固定标签并发正确。

### 独立审阅与修复

独立代理发现并确定性复现两项问题。第一项：旧claim在续租前替换缓存，会在控制面已取消/核对后让缓存倒退。已删除提前替换，只有fenced事务提交后更新业务缓存；新增交错用例验证DB/缓存保留新版本、Provider调用为0。第二项：超过256已加载非终态资源时overflow/reset反复从头补读。改为独立惰性扫描游标，失败批最多8项，后续reset合并不重头。

最终前端135项/18文件通过；300资源及300通知用例historyReads=1、uniqueFetched=300，尾44项可达，完成后推进两分钟无新增读取；400项中途失败后从原批继续。另修正媒体步骤上下文到期误触发全局数据库熔断。独立代理复审两项原问题均已闭环，未发现新阻断；该复审为静态检查，实际执行结果以上述测试为准。

### 数据库升级与页面

- 应用停止期间联备`.runtime/backups/pre-v0.8-20261113-202003/`：database.dump63516字节，pg_restore --list成功；reference/assets及007 owner身份同时复制。manifest记录时间与SHA256；未进行全量恢复演练，备份不进入源码。
- 启动最终Go开发服务后，开发库001—007正常增量升级008/009。只读SQL确认版本1—9、待消费0，新demo任务SUCCEEDED/v15。旧账户/会话、月光项目、既有任务和5项资产保留；新任务产物注册后共6项。
- 真实双窗口：副窗口创建demo图像任务，主窗口无需点击刷新即看到完成，主窗口创作输入保持不变。副窗口把原素材v8改名保存v9时，主窗口未保存名称仍保留；再恢复原名称为v10，主窗口列表自动显示v10。
- 本轮自行输入的验收草稿已清空/放弃，原素材名称和标签恢复；没有删除原作品。页面重载后会话/项目/新任务仍在，实时已连接，未新增重复生成。
- 1280×900桌面和390×844手机尺寸检查通过：document390、main380/client380，无横向溢出；不是移动实机测试。最终重载后的console warning/error为0，临时viewport已重置，保留主预览，关闭本轮副窗口。
- [浏览器记录](../wiki/assets/events-browser-v0.8-2026-11-13.json)、[自动补齐与输入保留](../wiki/assets/events-phase8.png)、[素材修改保留](../wiki/assets/events-dirty-phase8.png)、[手机布局](../wiki/assets/mobile-events-phase8.png)。真实进程Kill/游标恢复另由上面Go测试验证，没有宣称浏览器完成整机断电演练。

### 构建、接口与回归基线

根`npm run check`通过：治理8项、Go普通回归studio6.112秒、vet、前端135项、严格TS与生产构建；69份文档/34份台账结构和链接有效。JS gzip120.11kB，CSS gzip15.90kB。

五套隔离冒烟均通过：总链路、runtime5组、catalog3组、reference3组、library3组。全部强制demo/local、隔离目录且不连接主数据库。四份专项报告与性能报告中的SHA256均匹配最终server二进制4d239a6fe2d411772f6864f0885731b5081b23f6d9e0de4b3070baa023e9ed2d。

OpenAPI严格重复键、全部本地引用、operationId唯一、JSON Schema2020-12及路径参数核对通过：36路径、46操作、202引用/40唯一、274 schema、99参数出现。

本机隔离性能基线：1000请求/32并发bootstrap P95 22.05ms、2038.88请求/秒；任务创建+取消P95 14.22ms；demo首delta P95 57.26ms。含同机客户端开销，未测PG Outbox/租约高负载、TLS或真实上游，不宣称提速或达到万级容量。[性能报告](../wiki/assets/performance-local-v0.8-2026-11-13.json)、[runtime](../wiki/assets/runtime-smoke-v0.8-2026-11-13.json)、[catalog](../wiki/assets/catalog-smoke-v0.8-2026-11-13.json)、[reference](../wiki/assets/reference-smoke-v0.8-2026-11-13.json)、[library](../wiki/assets/library-smoke-v0.8-2026-11-13.json)均保留独立版本。

## 风险与限制

仍有单控制连接、全局业务锁和Outbox计数行的串行成本，尚未验证生产PG吞吐；领取隔离不等于全站多实例。参考/编辑正文及S3收据仍依赖本地私有目录。逐token持久日志、公平调度、分布式配额、跨实例扇出和灾备由后续批次验收。

步骤租约无法撤销已发送的供应商请求。未知提交保留待核实，真实供应商不支持的取消不能伪造成功。前端保护仅本页内存，刷新/关闭前仍需保存；窗口保留有限，reset按快照补读而非恢复完整历史事件。

Go race仍受本机CGO/C编译器条件限制，Linux CI配置存在但本轮未远程运行；真实S3服务本批未复跑（存储逻辑未改），沿用V08专项证据，不把普通PG测试当云服务验收。没有真实付费AI、公网发布或生产容量声明，V07/V15继续待外部条件。

## 回滚方式

保留新库和私有文件，停写后在独立位置恢复匹配程序版本的联备。008/009与旧程序不兼容；禁止删除schema登记、owner或租约字段强行降级。维护工具同步要求001—009。未知任务的原请求及幂等键保持，回退不是重新收费生成的理由。

## 文档同步

031—034、README、Wiki/模块和ADR0006、规则、OpenAPI、任务源与TODO同步。V09实际验收后标done，累计9/23；V07真实供应商待条件不提前完成，V16全站多实例仍未完成。最终`npm run check:changes`通过69份文档、34份台账与297个变更路径；`npm run check:todo`通过10模块/23批次/9批已验收。

下一步：V10先完成持久会话/消息及上下文预算，再完成编辑分支、停止/继续和流式恢复；V07/V15并行准备真实网关私有配置与受控验收；随后V11账户恢复、V12精确账本、V13支付。V16另做跨实例和真实负载，继续按批实现、验收、留证、勾选。
