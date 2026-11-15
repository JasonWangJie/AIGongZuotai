# 2026-11-14-065：V16-A1 文本工作区公平有界调度

## 目的

原文本执行使用全局 FIFO，一个工作区的排队请求会排在其他工作区之前，且缺少独立的工作区执行、冻结上游模型执行和工作区排队上限。本批加入工作区轮转与有界调度，在保持原子受理、原幂等及审核/账务恢复规则的前提下分配已有文本执行容量。

## 范围

关联 V16-A1、H03 模型与执行及 H07 可靠性与性能，依赖已验收 V05 资源准入与 V09 持久事件/租约边界。覆盖单控制写者下旧单轮 Run、多轮会话生成、配置和固定运维指标；不修改媒体提交/查询/导入执行通道，不引入多凭据路由、供应商自动重试或持久 token 回放。

本记录覆盖已落地的调度核心与普通回归；数据库联合验证由独立代理及集成人串行执行并另记证据。完整 V16 仍未验收，任务状态只能由集成人根据实际 checks/evidence 更新，不能凭本页勾选完成。

## 实际文件

以下是本核心子项实际修改文件；并行配置、指标及独立数据库测试由其所属台账记录，不把计划文件列为本子项修改。

- `internal/studio/execution_scheduler.go`：调度配置、工作区轮转、冻结上游模型分组、队列/执行容量及聚合快照。
- `internal/studio/execution_scheduler_test.go`：配置边界、轮转删除、双层并发、模型阻塞跳过、取消、隐私、5000 步状态不变量及 HTTP 池检查。
- `internal/studio/app.go`、`internal/studio/conversation.go`：两类文本受理成功后入队、worker 分派、排队取消与完整执行退出释放预算。
- `internal/studio/provider.go`：文本网关独立有界连接池，保持上下文超时与流式读取。
- `docs/wiki/execution-scheduler.md`、本台账：契约、限制与实际证据。

独立 App/PG 测试由 `internal/studio/execution_integration_test.go` 与 066 记录；环境、脚本隔离、`chat_execution_observability.go` 固定指标、README/导航/索引/任务源由集成人登记 067。原 `execution_observability.go` 保持既有观测功能，不被聊天指标文件替换。

## 接口与数据影响

新增 Go 配置 `Config.ChatScheduling`。环境变量为 `CHAT_WORKERS`、`CHAT_QUEUE_SIZE`、`CHAT_WORKSPACE_CONCURRENCY`、`CHAT_MODEL_CONCURRENCY`、`CHAT_WORKSPACE_QUEUE_SIZE`；未设置时分别默认 4、32、`min(2, Workers)`、`Workers`、`min(8, QueueSize)`。执行类合法范围 1—64，排队类范围 1—4096；显式非法值启动失败，不回显原值。环境变量的配置边界与调度器零值默认应在联合检查时一致核验。

调度器在 `a.mu` 内执行短暂的容量检查和内存状态变更，不在锁内等待网络或执行容量。持久受理确认后入队；原幂等回放先于当前容量判断，不重复入队。拒收不创建资源、消息、冻结或幂等，不消费报价。模型别名按原受理冻结 `upstreamModel` 共享执行容量，不用新目录重新构造已受理请求。

全局等待队列满返回 503 `RUN_QUEUE_FULL`，工作区等待队列满返回 429 `EXECUTION_WORKSPACE_QUEUE_LIMIT`，均为明确的新请求未受理；数据库提交确认不明产生的 503 不能按容量拒收解释。

同工作区通常 FIFO，队首模型满时允许后续其他可执行模型先运行。排队取消立即移除；运行中的预算持续到 `executeRun` 完整退出，覆盖输出审核和终态持久化背压。当前只有一个文本网关凭据，全局 worker 数就是该共享凭据的执行并发上限，不等同于供应商 QPS 或 token 配额。

没有新增数据库迁移、持久调度队列表或逐 token 日志。COMMIT 确认不明不入队并沿用数据库熔断；重启时未完成文本运行恢复为 `INTERRUPTED`，禁止自动重发。原权限、审核隔离、账本、Outbox 和媒体租约边界不因调度容量变化而放宽。

观测新增固定 `studio_chat_scheduler_*` 聚合系列，涵盖排队/执行/工作区数量、五项上限、分派数和等待时长；不使用工作区、模型、凭据或内容标签。计数与时长仅属当前进程，最大等待不是百分位，也不是生产容量证明。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| 只读核对 README、Wiki/台账索引、台账模板、当前 TODO、任务源及相关模块说明 | 通过 | 已按既有文档格式登记；本批依赖和未实施范围明确。只读审阅不证明实现正确。 |
| 只读核对环境变量解析与固定指标源码 | 通过 | 当前工作树已有严格数字/范围校验和无动态标签的指标输出；专项执行证据由集成人维护。 |
| 只读核对调度器源码与文档契约 | 通过 | 已核对默认/范围、轮转/模型阻塞跳过、容量错误及聚合快照；不代表 App 接线或并发测试已通过。 |
| `go test ./internal/studio -run '^TestNonexistentV16Compile$'` 首次编译 | 失败，已修正 | 并行指标文件曾覆盖原观测同名文件，导致旧方法暂缺；集成人恢复原文件，将新指标移至独立 `chat_execution_observability.go`，后续下列实际回归编译通过。 |
| `go test ./internal/studio -run '^(TestChatScheduling|TestChatScheduler|TestChatExecution)' -count=1 -v` | 通过，0.036s | 六函数覆盖轮转游标删除与唤醒、工作区/冻结模型上限、模型队首阻塞、队列/取消/聚合隐私、5000 步状态不变量及 HTTP 池无总流超时。 |
| 同一专项在固定指标测试接入后复验 | 通过，0.054s | 七函数通过，新增集成人的真实队列指标隐私检查；本次同时编译独立 066 App/PG 测试文件，但未选择执行 PG 函数。 |
| `gofmt -w internal/studio/execution_scheduler.go internal/studio/execution_scheduler_test.go internal/studio/app.go internal/studio/conversation.go internal/studio/provider.go` | 通过 | 首次使用 PowerShell 未展开的 `execution_scheduler*.go` 导致 gofmt 路径错误；改显式文件路径后完成格式化，未隐藏首次失败。 |
| `go test ./internal/studio -run '^(TestRun|TestConversation|TestBounded|TestChat|TestModeration|TestAdmission|TestCancel|TestBilling)' -count=1` | 通过，1.499s | 普通回归包含两类文本接线、审核、资源准入、取消及权益相关测试；此正则不选择 `TestPostgres*`，不作为数据库验收。 |
| PostgreSQL 原子受理、回滚、提交确认不明及恢复专项 | 未运行 | 本文档子项不启动 PG、不占用共享验证窗口；待集成负责人串行验证。 |
| Go 编译、完整测试、静态检查与构建 | 未运行 | 不在并行实现期间运行或引用旧批次结果。 |
| `npm run check:docs`、`npm run check:todo` 及变更同步检查 | 未运行 | 待 README、索引、任务源/TODO 与最终实现一起收口后由集成人运行。 |

待验证场景包括：跨工作区轮转和单工作区洪峰；全局、工作区、冻结模型并发边界；同上游别名共享容量；队首模型阻塞但后续其他模型可执行；队列满时原键回放；排队取消及时释放；执行取消、输出审核和终态背压期间不提前释放；确定回滚零入队；COMMIT 确认不明及重启零自动 POST；环境解析和指标隐私。未设置测试数据库而跳过不能记作 PG 通过。

## 风险与限制

当前仍是单控制写者和进程内调度；多实例分别计数不能形成全局配额。公平轮转只约束具备执行条件的等待项，不抢占长请求，不承诺完成顺序或最大等待时长。执行预算含审核与终态背压，调大 worker 数也不能直接推导吞吐提高。

只有一个文本网关凭据；模型分组并发上限不是完整供应商限流模型。媒体执行通道拆分、跨实例配额、持久流日志/回放、真实网关容量与长稳态验收仍是后续范围。默认值仅用于本地开发；本次不发真实收费请求，也不把 fixture 或单元测试当作生产成绩。

## 回滚方式

停机并保留现有数据库与私有文件后，可恢复本批之前的匹配二进制及配置，撤销文本调度接线与五个新增环境变量；本批不新增数据库格式，不需要删除任何迁移登记、用户记录或目录 owner。回退程序仍须支持当前 017 及既有审核/权益数据格式，不能借回滚调度绕过格式栅栏。

切换执行配置通过受控重启完成；重启后的未完成文本运行按既有规则标记 `INTERRUPTED`，不重建请求或自动重发。保留原幂等、已提交业务和审计记录供核对，不能通过删除记录释放未知责任。

## 文档同步

- README：集成人补充文本调度配置、当前能力与本 065 最近变更链接，尚未由本子项修改。
- Wiki：本子项新增[文本执行公平调度](../wiki/execution-scheduler.md)；导航与现有准入/租约文档的历史口径由集成人同步。
- 台账索引：集成人登记本 065；联合验证结果在交付前追加本记录，不覆盖已有历史事实。
- 任务源/TODO：集成人按 V16-A1 实际证据更新 `docs/planning/tasks.json`，运行 `npm run todo:sync` 同步根入口与完整清单；本子项不提前标记 V16 完成。
