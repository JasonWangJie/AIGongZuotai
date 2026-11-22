# 188　V17 已有会话的有界历史消息读取

## 目的

把 035 既有分支按页读取。旧实现先读取分支内全部用户正文及关联 Run 文档，再根据请求条数和 JSON 大小裁页。翻到较老的历史消息会重复加载之前已经读过的回复。

## 范围

本次属于 V17 的 H02 项目与创作数据和 H07 可靠性与性能。只调整普通工作台既有的会话消息 GET。035 的持久写入上限仍为每会话 256 节点、单分支 128 节点。内存演示保留单独的短分支路径。

## 实际文件

- `internal/studio/conversation_http.go` 分开数据库与内存入口，数据库读取在 App 状态锁外进行。
- `internal/studio/conversation_message_page.go` 在一个可重复读、只读事务内读取作用域中的会话版本与头节点，验证分支游标，按需读取节点与 Run，并执行原有 JSON 大小判断。
- `internal/studio/conversation_message_page_test.go` 对照既有完整分支查询，并通过真实 PostgreSQL 的查询追踪和页外行锁验证实际读取集合。
- `docs/changes/2026-11-22-188-conversation-message-pages.md` 记录本项实现与验证边界。

## 接口与数据影响

`GET /api/v1/conversations/{id}/messages` 沿用指定项目、选择的叶子、包含账户和分支的 v1 游标、默认 20 条和最大 40 条。省略叶子时选取事务中的当前 head。游标未经签名，服务端在原工作区、项目、会话内沿父节点验证起点确属选中分支，后续页最多核查 128 个窄节点。首屏的递归深度最多为请求数量加一。首屏与后续页都最多向应用返回 41 个候选 ID。

正文查询逐项读取一个用户节点或助手及其 Run 的持久业务快照。结果继续使用既有审核放行条件，保留错误、序号、模型名称和时间。缓冲区按实际 Go JSON 编码字节、分隔符和原 4096 字节封套预留计算 1 MiB 上限。遇到一条放不下的消息时仅探测这一条，返回的下一游标从该消息继续。按条数到界时仅需下一 ID。每页仍按祖先到后代的方向返回。

## 验证结果

| 命令或检查 | 结果 | 证据与限制 |
| --- | --- | --- |
| `go test -overlay .runtime/v17-message-read/overlay.json ./internal/studio -run '^TestConversation(ContextTrimsTurnsAndProtectsContinue\|MemoryBranchReplayAndCapacityPreserveHistory\|EmptyStopCanReplyButCannotContinue\|MessageWireBudgetAndSlowResponseDoesNotHoldStateLock)$' -count=1 -v` | 通过 | 四项短会话用例，包耗时 0.278 秒，覆盖完整轮次裁剪、原键与分支、空答和转义预算。 |
| `TestPostgresConversationPageMatchesOriginalShortBranch` | 等待集成人串行实跑 | 使用真实受理和规则审核产生公开及受限兄弟分支。对三种消息数和三种固定叶子逐页比对原始 JSON，并验证拼造同会话兄弟游标被拒绝。 |
| `TestPostgresConversationPageHydratesOnlyReturnedRowsAndByteBoundary` | 等待集成人串行实跑 | 预置三次真实生成的 128 KiB 控制字符输出。独立连接锁定页外 Run；查询追踪核对条数页仅取当前消息和字节页仅多取一个边界消息，输出语句数、字节与耗时。另持 App 状态锁检查独立读取可完成。 |

集成人在冻结 035 源码上串行运行 `go test -p 1 -count=1 -v ./internal/studio -run "^TestPostgresConversationPage(MatchesOriginalShortBranch|HydratesOnlyReturnedRowsAndByteBoundary)$"`。两项真实 PG 专项通过，包耗时 5.630 秒；含审核隔离与兄弟分支的逐字节旧响应对照耗时 2.79 秒。持有页外大回复行锁时，limit=1 实际只读一份正文，5 条 SQL、响应 787,440 字节、30.51 毫秒；limit=40 命中字节预算，实际读取两份返回正文与一份边界候选，7 条 SQL、响应 787,788 字节、31.56 毫秒。

## 风险与限制

服务继续使用原单控制连接及其存储互斥和五秒事务预算。为准确停在转义后的字节界限，正文每次一个 SQL 往返，普通满页最多 40 次正文查询；高时延数据库的影响待相应环境测量。助手展示的是 035 的业务快照，活跃流的实时增量仍由原 SSE 交付。真实万条单会话及前端历史虚拟化有各自的验收条件。

## 回滚方式

恢复本次的后端读取实现并重新构建，旧的读者可以继续使用同一 035 数据与分支游标。此项没有新增迁移或已写入的派生数据。

## 文档同步

由集成人在根 README、会话 Wiki、变更索引、V17 任务源及进度页登记本次本地结果，区分已有会话的优化与万条历史验收。

## 035 工作台与持久会话实测

035 本机 PostgreSQL 经正常入口生成单分支 64 轮，Chromium 在 7 页中逐条核对 128 条消息。首屏 20 条仅挂载 4 条，采样峰值 8 条。6 次前插与缩窄至 390 像素时，锚点最大偏移分别为 0.140625 和 0.140625 CSS 像素。键盘跨窗口、项目切换、未发送草稿和迟到读取均通过，浏览器生成 POST 为零。 [聚合报告与工作台制品摘要](../wiki/assets/performance-conversation-history-035.json)。
