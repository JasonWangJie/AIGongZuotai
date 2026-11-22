# 097 · 聊天执行授权、提交后广播与审核门禁

## 目的

V16-B1。普通聊天与会话聊天只在执行授权事务确认后调用模型；未启用输出审核时，断线用户随后可从 PG 读取已向浏览器公开的增量。

## 范围

Go 聊天执行、同一 Run 正在提交时的取消顺序、输出审核、终态保存与原有内存演示。执行基于单控制写者。多执行者租约及故障后旧执行者写入校验由 B2 单独验收。

## 实际文件

- `internal/studio/app.go` 接入 PG 启动授权和逐增量事务。每个 Run 记录正在提交的写入和正在等待的取消。其他 Run 可以继续使用应用互斥锁，同一 Run 的取消先等待本次写入并阻止后续增量抢在取消决定前发布。
- `internal/studio/run_stream_execution_test.go` 验证真实 PG 行锁阻塞时的可见性及取消顺序，并检查审核通过、拦截、待复核和取消时的持久事件和终态。
- 本台账配套 [B1/B2 契约](../planning/v16b-contract.md)。格式与持久存储、恢复、授权补读和前端行为由各自台账登记。

## 接口与数据影响

PG 模式使用每次执行在数据库确认的唯一授权，每个非空有效文本块独立推进 Run 序号。应用确认提交后才将内容加入公开输出和本地 SSE 通知。有新公开内容时，周期任务约每秒将已确认的前缀写入主业务文档，并同事务投影原有项目通知。增量提交自身不产生逐块的项目 Outbox 通知。写检查点与同一 Run 的增量、取消、终态协调；等待 PG 时释放应用锁。审核候选继续留在私有内存，只有 ALLOWED 与 COMPLETED 同事务确认后才能公开。原键、输出字节上限、审核/权益终态和内存演示保持既有语义。受理、取消、补读沿用原 API。

## 验证结果

- `go test ./internal/studio -run '^(TestConversationMemoryBranchReplayAndCapacityPreserveHistory|TestRunIdempotencyLifecycleAndReplay|TestReplayWindowSnapshotAndLastEventIDPrecedence|TestBoundedAdmissionAndSlowSubscriber)$' -count=1`，四项通过，0.286 秒。
- `TestPostgresRunStreamAppendBeforeBroadcastAndConcurrentCancel` 用独立连接锁住该 Run 的持久流水位，在已有明确执行授权后阻住第一个增量。断言等待期间热输出、序号和事件都未前进，并检查取消只保留已提交的首个文本块、第二个块被拒、原键不重复执行。
- `TestPostgresRunStreamModerationKeepsCandidatesPrivate` 在两个私有片段已产生、审核尚未放行时检查 PG 水位和事件表，终态分别核验允许、阻止、待复核与取消的可见正文。
- `TestPostgresRunStreamCancelBackpressureKeepsOriginalExecution` 在取消事务中注入 Outbox 满，核对回滚后原 RUNNING 前缀与序号，原供应端连接继续产生后续增量且只执行一次。
- 集成人首轮真实 PG 专项中，增量提交与并发取消 1.16 秒通过，审核四种结局 1.58 秒通过。修正后重跑时，增量提交与并发取消 1.30 秒、审核 2.06 秒、取消背压 2.06 秒，均通过。
- 首轮旧进程用例 `TestPostgresConversationActualCrashPreservesPartialAndExplicitContinue` 失败。少量文本产生后供应端停顿，执行路径停用了原每秒业务文档检查点，读取 `studio_runs.document` 的会话功能无法及时看到回答。已恢复按进展写主文档并发送原项目元数据通知，普通四项回归重跑 0.313 秒通过。原进程用例串行复验 3.04 秒通过，保留了原始检查点、原键和显式继续断言。

## 风险与限制

每个可见增量产生一次 PG 提交，供应端回调会等待该提交。不同 Run 的数据库写入仍受单控制连接限制；尚无多写者、生产吞吐或真实模型联调结论。提交确认丢失后依赖下次启动读取已提交前缀，不启动新模型请求。

## 回滚方式

须与 021 格式和其他 B1 组件共同回退。停机后按联备流程恢复配套数据库、私有目录、已启用密钥和相应版本应用；不得清除迁移登记或单独用旧执行代码写入 021 数据。

## 文档同步

由集成人统一同步根 README、Wiki、[任务源](../planning/tasks.json)、生成的 TODO、进度与台账索引。
