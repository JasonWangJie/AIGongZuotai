# 2026-11-15-082：媒体公平调度的维护与独立验收

## 目的

V16-A3.3新增020调度投影、工作区轮转及持久渠道冷却。维护不能因为任务暂不可执行而丢失私有文件可达性；公平与积压性能需要真实SQL执行计划及可复现业务轨迹，不能靠小样本或规划推定完成。

## 范围

批次V16-A3.3，模块H03、H04、H06、H07、H09；依据[冻结契约](../planning/v16a33-contract.md)。082负责020离线门禁、独立集成/升级/大积压SQL验收及维护Wiki。080负责核心，081负责HTTP观察与共享审核仲裁，083由集成人维护README、索引、任务源、TODO和联合证据。当前实现中，未验收；079历史成绩不替代本批。

## 实际文件

- `internal/studio/storage_maintenance.go`、`ledger_admin.go`、`operations_admin.go`：精确020栅栏和报告。
- `internal/studio/storage_maintenance_delivery_test.go`：仅当前020／旧019／缺019／未来021格式兼容，原锁与收据断言保留。
- `internal/studio/storage_maintenance_review_test.go`、`media_review_dispatch_upgrade_test.go`：仅当前20断言；原018构造和019零回填保留。
- 新`internal/studio/media_scheduler_integration_test.go`：真实App受理/公平/预算/取消与HTTP429冷却重启。
- 新`internal/studio/media_scheduler_upgrade_test.go`：原019小规模逐表保护/GC和20万任务生产迁移入口。
- 新`internal/studio/media_scheduler_plan_test.go`：180个真实生产SQL计划、两种规模、六种分布与预选游标验证。
- 本台账、新`docs/wiki/media-scheduler-validation.md`及`docs/wiki/storage-maintenance.md`；共享索引由083同步。
- ignored本地环境准备：`.runtime/v16a33-s3-start.ps1`与`v16a33-s3-probe.py`，仅供根执行，不加入源码或凭据；实际服务/对象证据由083记录。

## 接口与数据影响

离线维护、账本、运营角色和数据库归属存储迁移将只接受精确001—020，不自动迁移。调度投影不属于文件删除授权：任务、参考、审核候选及018全部计划/完整收据继续保留原可达性。020新增派生状态不能改写原请求、handle、019审核状态、资产或冻结权益。不存在公开多供应商路由能力。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| 契约、现有SQL及文件归属核对 | 通过 | 已读README/Wiki/台账索引、V16任务源、A3.3契约；已与080对齐四表命名和16工作区/8任务窗口 |
| `go test ./internal/studio -run '^$' -count=1` | 通过 | 最近0.038s；仅编译，不运行PG。开发并行期曾遇核心文件time未使用，负责人接齐后通过，未改其文件 |
| `go vet ./internal/studio ./cmd/maintenance ./cmd/ledger ./cmd/operations ./cmd/storage` | 通过 | 退出0，无PG执行 |
| 根执行`^TestPostgresMediaScheduler(WorkspaceFairnessAndCancelKeepsBudgets\|HTTP429CooldownSurvivesRestart\|Upgrade019PreservesEvidenceAndMaintenance)$` | 通过7.970s | 三项2.17/4.52/1.24s，根确认进程已退出。随后升级夹具查询状态名统一真实RUNNING，最终全量须覆盖 |
| 根首轮`^TestPostgresMediaScheduler(LargePlan\|LargeUpgrade019)$` | 整组失败194.902s | LargePlan180计划与分页/游标通过173.83s（10k14.12/100k159.68）；LargeUpgrade21.03s失败，真实生产迁移5.0542081s触发拒绝，未降低数据规模 |
| 首轮180原始计划独立读取 | 通过 | ignored报告609181字节；100k组statement执行max0.054ms/P95 0.048ms、叶buffer max35、叶行max16、过滤max0。这不是完整预选或端到端成绩 |
| 根最终`^TestPostgresMediaSchedulerLargePlan$`／180计划＋48次完整预选 | 通过174.770s | 函数174.73s，10k14.89s／100k159.82s；真实日志与621171字节报告已只读核对。所有48次游标推进，examined≤128，QueryCalls 10—71；不覆盖首轮事实 |
| 根`^TestPostgresMediaSchedulerLargeUpgrade019$`修后复验 | 通过30.741s | 真实20万旧任务迁移11.2288314s，59张原表行数/摘要不变。080使用专用启动60秒，普通业务仍5秒；082补齐旧部署默认ws_demo/prj_demo。只针对该编译快照，后续020语义变化须再验 |
| 根冷却实际dump/restore专项（083拥有） | 通过4.545s | 独立恢复库的四张020表、原handle/正文和服务端截止保留，恢复后无提前查询或再POST；这不是生产云灾备 |
| 当前020全部维护、最终完整PG与适用S3联备 | 未运行 | 根083统一执行；本代理不启动PG/S3。079历史不能替代本批 |
| 新S3启动/探针脚本静态解析 | 通过 | PowerShell AST与Python ast通过，未启动服务、未执行HTTP/PG。复用已验证4.53及原版本化桶、生成新前缀；真实启动/2版本/匿名403另由根执行 |

## 风险与限制

必须保留十万排队任务、大量终态历史和99%不符合lane/未来/冷却/租约的验收范围。固定集合轮转不等于工作区数量无限时仍有绝对延迟上界；本机真实SQL、HTTP与版本化S3也不代表生产容量、多供应商路由或云灾备。原019待核实/STARTED不能因新调度变为可重试。

仅选择器bulk夹具在一个事务内短暂禁用新投影trigger，提交前恢复并核对启用状态，再全量反连接和计数验证。原19约束不关闭，失败事务回滚；这不是受理吞吐或真实phase claim证明。另有原019直接插入20万任务后走生产迁移入口的独立验证，使用实际生产迁移预算，不从测试放宽。新App测试始终走真实受理和trigger。

020集合回填开始读取019表后，缺019负例仅省略原文件会在建库时失败，尚未到离线门禁。根额外授权该内存FS夹具在020前执行原019 SQL字节，但从未登记019，只登记020；没有删除已登记迁移、没有修改磁盘SQL，明确属于故意损坏登记的负例。四个离线入口拒绝断言不变，最终PG另验。

S3脚本交根执行后，本代理又追加了data/filer/auth路径必须位于原runDir内、filer.toml元数据目录匹配的静态检查。该增强不追溯声称包含在已启动PID24276的脚本快照；根另行核验实际路径并在083记录。脚本随后冻结，代理未启动服务或执行对象操作，实际探针及停止由根负责。

最终大计划证据位于ignored的`.runtime/v16-a33-large-final.log`及`.runtime/v16-a33-large-plans-final.json`，报告SHA-256为`1ad805becf04f38ce9958d4ac8a542059a86c6b74cf3d09f842bc3d67a55681b`。每规模90个预热单SQL样本和24次完整预选，各六分布。100k＋100k组单SQL样本P95 0.048ms／最大0.050ms、叶buffer最大42；完整预选中位10.16645ms／样本P95 16.5018ms／最大18.1860ms。10k＋10k组完整预选中位7.1690ms／样本P95 10.8245ms／最大11.4088ms。P95按nearest-rank计算；六种固定夹具各4次，不能视为生产负载延迟分布。

完整预选包括BEGIN到COMMIT及本地筛选，实际最多128个候选、71次pgx查询调用；这些调用不是网络包数，也不包含后续strict claim、Provider或端到端任务耗时。99%未来/冷却/租约/混合场景的前4次均检查128项并推进游标、返回无候选，是有界跳过页的结果，不冒称每次都找到尾部1%。strict claim缺渠道行锁竞态随后由080修正，根确认selector/020未改，该修改不改变本次预选测量范围；最终全PG仍须覆盖真实领取。

后续全量发现游标/外键死锁，与上述缺渠道行问题不同。selector两处锁改为`FOR NO KEY UPDATE`后，根重新测量180计划/48完整预选，通过170.780s；新报告`.runtime/v16-a33-large-plans-lock-fixed.json`为621201字节，SHA256 `e4bbd2516c52c50bb9c59018758782f62d87bca1b5a1002b47fceca6687dff4b`。100k组24次中位9.64395ms、P95 16.3019ms、最大16.6752ms，48游标均推进，最多128候选/71调用。020与单SELECT常量保持，但完整预选包括锁操作所以未复用旧测量；174.770s记录保留为修正前历史，最终联合083另记。

## 回滚方式

020启用后保留当前库与私有目录、owner、交付及已启用队列密钥和对应对象版本。需回退时恢复配套019程序的独立升级前联备，不删除020登记、调度行、审核证据或owner绕过格式门禁。

## 文档同步

- README、台账/Wiki索引、任务源与双TODO：根083统一维护，当前尚未完成。
- Wiki：已新增`media-scheduler-validation.md`并同步`storage-maintenance.md`，如实区分计划、失败和实测。

联合收口补记：根完整check、最终全PG 443.228s（实际版本化S3/联备启用）、六smoke、同构建回归及治理通过，详见083和联合JSON。本记录的中间待验/失败历史保留；A3.3本地验收，完整V16未完成。
