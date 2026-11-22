# 098 · Run 持久回放与浏览器断线补读

## 目的

V16-B1 让浏览器从最后收到的 Run 序号接回已经提交的文本。订阅者错过热通知、Run 离开内存或日志已裁剪时，仍可通过原 Run 的只读接口恢复输出。

## 范围

覆盖单控制写者下的 Run SSE、租户鉴权和工作台事件归并。持久格式、执行授权、逐块提交与重启处理分别见 [096](2026-11-21-096-run-stream-store.md) 与 [097](2026-11-21-097-run-stream-execution.md)。

## 实际文件

- `internal/studio/http.go` 沿用每工作区和全局订阅配额，登记并持有读者直到 HTTP 处理退出。
- `internal/studio/run_stream_replay.go` 实现作用域内的 PG 分页、快照和热订阅衔接。
- `internal/studio/run_stream_replay_test.go` 验证多页、热通道溢出、淘汰后回放、旧 Run、过期游标和租户边界。
- `apps/workbench/src/runStreamReplay.test.tsx` 挂载工作台实际使用的 `useStudio`，以模拟 HTTP 和 EventSource 驱动断线恢复及四种终态。

## 接口与数据影响

`GET /api/v1/runs/{id}/events` 优先使用 `Last-Event-ID`，其次使用 `after`。显式零和正游标均从提交窗口补读。省略游标且热环没有完整起点时，首帧沿用完整快照。越前游标返回 409，越权资源返回 404。窗口过期、旧 Run 或受审核门禁保护的 Run 返回当前允许公开的快照，快照和持久水位取自同一读事务。

读者先订阅热事件，再按序读取持久日志。每页最多 32 个事件和 1 MiB 事件数据。写入客户端时不持有应用锁或数据库事务。缓冲已满时重新订阅并补读，按 Run 序号跳过重复事件。打开订阅、恢复窗口和确认终态使用 GET。

## 验证结果

- 集成人串行运行的真实 PG 组合 12 项通过，用时 29.033 秒。`TestPostgresRunStreamReplayAcrossLiveHandoff` 用时 1.70 秒，在一槽队列上阻塞读者、并发提交、交接分页与热通知，逐项核对原增量和终态。它也验证显式零、无游标、越前、冷读及跨租户访问。`TestPostgresRunStreamExpiredAndLegacySnapshots` 用时 1.37 秒，检查 280 个增量触发的裁剪及旧格式 Run。
- 首轮旧回归 `TestPostgresRunCachePinsExecutionAndSubscribers` 暴露省略游标时，冷终态返回逐行事件。修正后保留原断言，重跑 1.68 秒通过；同一组合中的运营热 Run 快照回归用时 2.22 秒。
- `go test ./internal/studio -run '^TestSSEObservationPreservesStreamingAndWorkspaceAdmission$' -count=1` 通过，0.315 秒。
- `npm --prefix apps/workbench test -- src/runStreamReplay.test.tsx src/state.test.ts src/moderation.test.tsx src/durableEvents.test.ts` 通过，4 个文件、48 个用例，Vitest 用时 2.74 秒。浏览器逻辑从 451 建立订阅，同一增量重复到达只追加一次，遇到缺口重新 GET 并从 620 订阅，窗外快照替换到 777；完成、取消、失败和中断分别经终态事件及 GET 确认。`npm --prefix apps/workbench run typecheck` 通过。

## 风险与限制

窗口容量为每 Run 最多 256 行、1 MiB，慢读者追出窗口后以完整快照恢复。内存演示仍使用热环。前端用例使用 jsdom 和模拟 EventSource；PG 用例经过真实本机 HTTP 或 Go HTTP handler。生产网络、真实供应商和跨实例租约另行验收。

## 回滚方式

需回退时停机恢复与 021 格式配套的数据库、私有目录、适用密钥和应用版本，保留迁移及目录归属记录。

## 文档同步

集成人统一更新根 README、Wiki、台账索引、[任务源](../planning/tasks.json)、生成的 TODO 和进度；B1 与 B2 验收范围见[持久流契约](../planning/v16b-contract.md)。
