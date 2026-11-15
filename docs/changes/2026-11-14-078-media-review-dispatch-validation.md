# 2026-11-14-078：首次媒体审核分派的维护门禁与独立验收

## 目的

V16-A3.2把首次整组媒体审核交给独立持久分派，维护工具须识别019完整格式，不能把等待或已开始的私有审核产物当成孤立文件。本记录同时收集独立App/HTTP/PG验收，保留原018升级字节与隔离证据。

## 范围

批次V16-A3.2；模块H03、H04、H06、H07、H09。依据[冻结契约](../planning/v16a32-contract.md)，078仅拥有维护/离线格式门禁及独立验收。核心019和执行交接由076负责，共享审核许可由077负责，根079维护README、索引、任务源、TODO与联合放行。状态：本专项已通过，整批仍等待根联合验收；075成绩仅为A3.1历史事实。

## 实际文件

- `internal/studio/storage_maintenance.go`、`storage_maintenance_delivery.go`：019精确门禁与审核阶段/关联任务版本计划摘要。
- `internal/studio/ledger_admin.go`、`operations_admin.go`：019精确门禁与报告；`MigrateOwnedAssets`复用维护入口，因此同步受019栅栏约束，未增加绕过路径。
- `internal/studio/media_review_dispatch_integration_test.go`：App、真实媒体/审核HTTP、共享输入许可、固定指标、两连接取消、图/视频/PARTIAL/BLOCK及权益。
- `internal/studio/media_review_dispatch_upgrade_test.go`：原嵌入001—018建库、原字节/幂等保护、旧写者拒绝、旧候选零READY及新任务首次审核。
- `internal/studio/storage_maintenance_review_test.go`：实际READY→FINISHED与Task.Version变动的计划失效、全部收据/文件保护及019离线入口。
- `internal/studio/storage_maintenance_delivery_test.go`：仅把当前格式兼容用例推进019/旧018/缺018/未来020；其他GC/收据断言保持，074/075历史成绩不改。
- `docs/wiki/media-review-dispatch-validation.md`、`docs/wiki/storage-maintenance.md`及本台账。

## 接口与数据影响

维护、账本、运营角色和PG归属存储迁移现在仅接受精确001—019；不在离线工具执行迁移，不删除owner或版本登记。维护/账本/角色报告推进schema_version=19。019分派不增加正文文件，候选和018交付收据继续完整可达。维护摘要加入有序审核分派的候选/策略摘要、结果状态、阶段/版本、attempt/时间及关联Task.Version；即使文件集合不变，阶段推进或任务修订也使旧计划失效。没有新公共HTTP接口，不改变生成、审核或收费授权。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| 契约与文件归属核对 | 通过 | 已读v16a32-contract、根README、Wiki/台账索引、TODO和维护说明；不修改核心或根集成文件 |
| `go test ./internal/studio -run '^$'` | 通过 | 0.031s，当前普通编译；无PG |
| 根首次普通Go全量 | 失败 | 根报告studio9.914s；新memory审核夹具被既有生产门禁拒绝。已删除memory入口，完整逻辑保留在PG验收，未放宽生产校验；根复验另记079 |
| `node .runtime/v16a32-check.mjs target '^TestPostgresMediaReviewDispatch\|^TestPostgresDelivery(Maintenance\|Migration\|Offline)'` | 失败 | 根授权独占窗口session2698，23.693s、exit1。074五函数与019格式三子例均通过；新六函数最终决定停STARTED/取消409 MEDIA_REVIEW_STATE_CHANGED，未记通过 |
| 首轮失败定位与修正 | 已修复 | 审核时间身份的reflect.DeepEqual把PG重读的时区Location差异当成attempt变化；076改为Time.Equal，仍拒绝实际时刻变化。078保留全部失败断言，不修改核心 |
| `node .runtime/v16a32-check.mjs target '^TestPostgresMediaReviewDispatch'` | 通过 | session81141、exit0、15.003s；6函数及整组4子例全部通过：慢审核1.58s、共享输入许可1.52s、两连接取消1.45s、整组权益6.40s、018升级1.58s、维护阶段2.44s |
| `go vet ./internal/studio ./cmd/storage ./cmd/maintenance ./cmd/ledger ./cmd/operations` | 通过 | exit0；未启动PG或外部服务 |
| 归属Go文件`gofmt -l`与归属文件`git diff --check` | 通过 | gofmt无输出；diff仅提示既有Windows换行转换，不含空白错误；根治理检查由079统一执行 |

第二次PG按根授权启动；结束后收到撤回窗口的交错通知，核心1191也已结束，均未报告写锁争用。仅凭消息不能断定实际运行重叠，不宣称调度完全无交错。根079记录协调问题并改为之后所有PG由根亲自串行执行；最终全量再次覆盖两组。本专项此后不再启动PG，也不因协调交错删除任何失败/成功事实。

## 风险与限制

真实审核渠道未选型；本机HTTP与固定媒体只能验证协议、字节和隔离语义。共享许可公平与饥饿策略属于A3.3，完整V16未完成。018候选不得补造READY，STARTED后宕机或确认不明不得自动重审。此记录不宣称生产容量、云灾备或公众收费放行。

## 回滚方式

019启用后停止写入并保留当前数据库、私有文件/owner、S3对象和所需密钥。回退使用升级前匹配018程序的独立联备；不删除019迁移、分派、候选或owner以强制旧程序接管。

## 文档同步

- README、台账/Wiki索引、任务源和双TODO：根079集成时统一维护，本记录未宣称已同步。
- Wiki：已新增`media-review-dispatch-validation.md`，同步`storage-maintenance.md`的019门禁、摘要、首轮失败及15.003s修后通过；联合验收状态仍由根079维护。

联合收口补记：根最终check、全PG368.160s（实际版本化S3及联备启用）、六smoke、构建和文档治理通过，详见079。本文中间失败/待集成交接事实保留，完整V16不标完成。
