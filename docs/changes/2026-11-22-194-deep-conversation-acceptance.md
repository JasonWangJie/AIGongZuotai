# 194　V17 单会话一万条历史的持久工作台验收

## 目的

按原方案检查同一会话、同一条分支的一万条历史。演练从真实注册账户和空会话开始，记录实际受理、执行、分页和浏览器回读。每轮的预期正文、消息次序和父关系均可由固定规则重算。

## 范围

V17 的 H02 项目与创作数据、H03 模型与执行、H07 可靠性与性能和 H08 前端体验。使用 036 长会话配置、本机专用 PostgreSQL schema、真实工作台 HTTP、标准 live 聊天适配器与环回 HTTP/SSE 夹具。浏览器模式另读构建完成的工作台 dist。

## 实际文件

- `internal/studio/conversation_deep_acceptance_test.go` 建立、核对并分页读取单链，检查原幂等、明确继续、原祖先 IDs、模型上下文与独立数据库计数。
- `internal/studio/conversation_deep_billing_test.go` 从 65 个真实受理的历史轮次重新开库，走真实本机报价、输入输出审核和 CREDIT 演练结算。
- `internal/studio/conversation_deep_browser_test.go` 通过测试专用进程保护和 stdin 交接临时登录信息与每页预期摘要，核对浏览器的聚合输出。
- `apps/workbench/scripts/conversation-deep-browser.mjs` 在真实页面加载历史，检查同一身份、固定叶、游标与逐页摘要，重新访问已淘汰的早、中、晚三页。
- `docs/changes/2026-11-22-194-deep-conversation-acceptance.md` 记录复验步骤与证据。

## 接口与数据影响

`DEEP_CONVERSATION_TEST=pilot` 建立 150 轮、300 个历史节点。`full` 建立 5,000 轮、10,000 个节点。每一次 POST 都必须在受理后以原 Run 的 GET snapshot 读到正确终态才能推进下一轮。上游逐次检查实际完整 role/content、模型、认证及消息数，超过窗口时只接受最近 64 个完整轮次。

流程重放第一轮原键，并在同一固定叶通过独立认证 API 读取 15 或 500 页，每页 20 条。开启 `DEEP_CONVERSATION_BROWSER=1` 时，页面从登录起读取同一批记录，核对首、中、末区域的原正文与原游标回源；浏览器生成 POST 数必须为零。最后显式继续一次并核对新增两条节点、原父完整祖先、冻结上下文和独立 PostgreSQL 数据。正式数据量分别为翻页时的 10,000 节点和继续后的 10,002 节点。

使用独立库或由测试创建的独立 schema。生成、审计、上下文、计费开关及 Outbox 走原业务流程。夹具对应用库的独立 SQL 仅查询，冷读历史不会创建生成请求。浏览器临时资料经进程 stdin 传入；可选报告仅含聚合数值、模式和分段耗时。

## 验证结果

| 检查 | 当前证据 |
| --- | --- |
| `go test -overlay .runtime/v17-deep-acceptance/compile-overlay.json ./internal/studio -run '^TestDeepHistoryFixtureChecksCompleteWireContext$' -count=1 -v` | 本机通过，包耗时 0.218 秒。真实环回 HTTP 核对首轮、64 轮窗口及继续指令，交换内容会拒绝。 |
| `node --check .runtime/v17-deep-acceptance/apps/workbench/scripts/conversation-deep-browser.mjs` | 语法检查通过。 |
| 036 真实 PG，pilot、full 及真实 Chromium | 等待集成人在固定制品上串行执行。正式墙钟、表与索引大小、待投影与缓存峰值、DOM 验收以正式报告为准。 |
| `TestPostgresDeepConversationQuoteModerationAndFrozenRequest036` | 待集成人串行执行。在独立短长链上停写、用正式离线工具预演并确认赋予夹具权益，再重启启用本机审核及报价。审核等待期间核验 App 状态锁可用、报价未消费、项目无新增 Run；确认后核对原审核消息、完整轮次、冻结请求、已结算 reservation 及原键重放。 |

在完成 036、191 和前端构建的隔离工作副本中设置回环 `TEST_DATABASE_URL`。先设 `DEEP_CONVERSATION_TEST=pilot`，再设 `full`。每次使用新的绝对 `DEEP_CONVERSATION_REPORT` 文件路径。带浏览器时另设 `DEEP_CONVERSATION_BROWSER=1` 和绝对 `DEEP_CONVERSATION_DIST`。运行 `go test ./internal/studio -run '^TestPostgresDeepConversationHistory036$' -count=1 -timeout 75m -v`。选择短档也会超过 035 原分支深度。

## 风险与限制

夹具采用单用户串行负载。过程中的第 100、150 轮及后续每 500 轮记录经过时间、当时的 Outbox pending、驻留 Run 数和本 schema 普通表的含索引与 TOAST 字节；采样值不能替代连续峰值、WAL 或主机磁盘占用。默认空闲 Run 缓存为 512。Outbox 的单次投影预算为 100，250 毫秒唤醒，pending 上限 10,000。若系统实际拒绝受理，测试将失败并保留失败阶段，不会自行清除账务、待投影事件或用 SQL 补造数据。一万条主线关闭计费及外部审核，独立报价审核专项使用模拟权益和本机适配器。实际商户、网关和并发生产容量另行验证。

浏览器仅保留有界观察元数据，认证 GET 的正文逐页计算摘要后释放。驻留正文和 DOM 数为实际页面采样。持续回复的内存与帧延迟沿用该项目独立稳态验收。

## 回滚方式

停用测试入口环境变量即可退出本项验收。交付物只增加测试与本机脚本；库写入限于独立测试 schema 与其私有目录。每次由创建这些资源的测试负责回收。

## 文档同步

集成人在根 README、会话 Wiki、V17 任务源、进度和台账索引登记正式证据；分别列出 035 已有历史、036 单链一万条和持续 30 分钟回复。

## 036 集成人实测

150 轮先导在正常服务入口完成，提供者实际收到 151 次调用（含最终显式继续），原页 15 页，重放不增加调用。在另一次经过 Chromium 的先导中，读取 300 条原消息并复验三个正文淘汰后的重读，整个试具 30.34 秒。独立的 65 轮报价、锁外输入审核、冻结上下文、公开输出和最终权益结算专项通过，4.76 秒。

## 036 单分支正式验收

本机 PostgreSQL 通过正常受理入口完成同一分支 5,000 轮，逐轮核对上游上下文。Chromium 读取 500 个授权页、索引 10,000 条消息，实测最多挂载 10 行、缓存 120 条正文；首、中、末三处淘汰内容均通过原页重读。显式继续后原库有 10,002 个节点与 5,001 份冻结上下文，原键重放未增加上游调用。专项 219.573 秒。 [完整聚合数据](../wiki/assets/performance-conversation-history-036.json)。

## 036 本地累计检查

本轮主工作树执行 `npm run check`，通过 Go vet、普通 Go 测试、567 项工作台测试、161 模块构建、生成 TODO 与文档。再分别执行 smoke、runtime、catalog、references、library、conversations 六套回环冒烟，全部通过。各真实 PostgreSQL 专项和单分支 5,000 轮的证据另列；历史 035 全 PG 回归仅对应原固定制品。
